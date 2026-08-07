package ops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/lifecycle/engine"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

// UpdateOptions configures an update.
type UpdateOptions struct {
	Options

	// Ref is the bundle reference: a path today, a URL or an OCI reference
	// once the sources in RFC 0004 land.
	Ref string

	// ExpectDigest pins the bundle. When set, a mismatch refuses the
	// update; when empty the digest is recorded but not compared, because
	// there is nothing to compare it against.
	ExpectDigest string

	// To selects a release already in the store by version, instead of
	// pointing at a bundle. Sugar for passing its path: the store is
	// populated by `release fetch`, and an operator should not have to know
	// the layout to install from it.
	To string
}

// Update installs a new release over the current one.
//
// The convergence half is `apply`'s pipeline, reused verbatim: what `update`
// adds in front of it is verification, a compatibility gate, a backup, and the
// staging step whose compensation returns the release pointer to where it
// started. A failed update leaves the previously running release current.
func Update(ctx context.Context, d *Deps, opts UpdateOptions) (Result, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return Result{}, err
	}

	// The release that is current when the operation begins. Zero on a
	// machine that has never had one, in which case `update` is a first
	// install and upgrade_from does not apply.
	from, err := d.State.CurrentRelease(ctx)
	if err != nil {
		return Result{}, err
	}

	source, staged, cleanup, err := d.resolveUpdateTarget(ctx, opts)
	// Whatever had to be brought down to read the bundle is scratch, and
	// goes when the operation does. The release itself has already been
	// copied into the store by then.
	defer cleanup()
	if err != nil {
		return Result{}, err
	}

	opID := d.newOpID()
	var prior *domain.OperationRecord
	if opts.Resume {
		if prior, err = d.findResumable(ctx, domain.OpTypeUpdate); err != nil {
			return Result{}, err
		}
	}
	if err := d.gateUnfinished(ctx, excludeID(prior)); err != nil {
		return Result{}, err
	}

	op := engine.Operation{
		ID:          opID,
		Type:        domain.OpTypeUpdate,
		Description: describe(domain.OpTypeUpdate, from.Version, staged.Version()),
		From:        from.Version,
		To:          staged.Version(),
		Steps:       updateSteps(d, inst, from, source, staged, opts),
		Flags:       updateFlags(opts),
	}

	var result engine.Result
	runErr := d.withLock(ctx, opID, domain.OpTypeUpdate, opts.Options, func(ctx context.Context) error {
		// Re-checked under the lock; see the same rechecks in Apply.
		var err error
		if opts.Resume {
			if prior, err = d.refreshResumable(ctx, domain.OpTypeUpdate, prior); err != nil {
				return err
			}
		}
		if err := d.gateUnfinished(ctx, excludeID(prior)); err != nil {
			return err
		}
		result, err = d.Engine.Run(ctx, op, d.engineOptions(opts.Options, inst.ID, prior))
		return err
	})

	out := Result{
		Record:  result.Record,
		Summary: updateSummary(result.Record, from, staged),
		Data: map[string]any{
			"from":   from.Version.String(),
			"to":     staged.Version().String(),
			"digest": staged.Digest,
			"root":   staged.Root,
		},
	}
	if runErr != nil {
		return out, runErr
	}

	d.notify(ctx, events.OperationFinished(opID, domain.OpTypeUpdate,
		result.Record.Status, result.Record.Duration(), nil))
	return out, nil
}

// materialiseSource returns a directory the bundle's manifest can be read from.
//
// A reference that already names an unpacked bundle is used in place. Anything
// else -- an archive today, a URL or an OCI artifact later -- has to be brought
// down before its manifest, its compatibility declarations or its hooks can be
// looked at, and the staging directory is where that happens. It is scratch by
// definition, which is what makes doing it during a dry run acceptable: a plan
// that refused to fetch could not tell the operator anything about the release
// they asked about.
//
// The caller removes what this creates; see the cleanup returned by
// resolveUpdateTarget.
func (d *Deps) materialiseSource(ctx context.Context, ref ports.Ref) (root string, cleanup func(), err error) {
	if isUnpackedBundle(ref.Location) {
		return ref.Location, func() {}, nil
	}

	if err := atomicfs.MkdirAll(d.Paths.StagingDir(), 0o750); err != nil {
		return "", func() {}, err
	}
	staging, err := os.MkdirTemp(d.Paths.StagingDir(), "fetch-")
	if err != nil {
		return "", func() {}, domain.Internal(err, "cannot create a staging directory")
	}
	cleanup = func() { _ = atomicfs.RemoveAll(staging) }

	if _, err := d.Source.Fetch(ctx, ref, staging); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return staging, cleanup, nil
}

// isUnpackedBundle reports whether a location can be read as a release where it
// stands, without fetching anything.
func isUnpackedBundle(location string) bool {
	if location == "" {
		return false
	}
	info, err := os.Stat(location)
	if err != nil || !info.IsDir() {
		return false
	}
	_, err = os.Stat(filepath.Join(location, release.ManifestFileName))
	return err == nil
}

// resolveUpdateTarget reads the bundle without mutating anything, and returns
// it twice: rooted where it currently lives, and rooted where it will live.
//
// Both are needed before the engine runs, because the step list is built up
// front and the apply steps close over the release they converge to. The
// content digest is computed from file contents, paths and the executable bit,
// so it is identical either side of the copy -- which is what lets the staging
// step assert the copy was faithful rather than assume it.
func (d *Deps) resolveUpdateTarget(
	ctx context.Context,
	opts UpdateOptions,
) (source, staged domain.Release, cleanup func(), err error) {
	cleanup = func() {}

	switch {
	case opts.Ref == "" && opts.To == "":
		return domain.Release{}, domain.Release{}, cleanup,
			domain.Usage("no release was given").
				WithHint("pass a bundle path, e.g. `morzer update ./bundle`, " +
					"or --to <version> for one already in the release store")
	case opts.Ref != "" && opts.To != "":
		return domain.Release{}, domain.Release{}, cleanup,
			domain.Usage("--to and a bundle path are alternatives").
				WithHint("--to installs a release already in the store; " +
					"a path installs one from outside it")
	case opts.To != "":
		// Already in the store: nothing to fetch, so the source and the
		// destination are the same directory and staging is a no-op.
		installed, err := d.resolveInstalled(opts.To)
		if err != nil {
			return domain.Release{}, domain.Release{}, cleanup, err
		}
		opts.Ref = installed.Root
	}

	if d.Source == nil {
		return domain.Release{}, domain.Release{}, cleanup,
			domain.Internal(nil, "no release source is configured")
	}

	ref, err := ports.ParseRef(opts.Ref)
	if err != nil {
		return domain.Release{}, domain.Release{}, cleanup, err
	}
	ref.Digest = opts.ExpectDigest

	// Resolve does not mutate the installation, so a reference this build
	// cannot fetch -- or one whose digest does not match -- fails before the
	// lock is even taken.
	resolved, err := d.Source.Resolve(ctx, ref)
	if err != nil {
		return domain.Release{}, domain.Release{}, cleanup, err
	}

	sourceRoot, cleanup, err := d.materialiseSource(ctx, ref)
	if err != nil {
		return domain.Release{}, domain.Release{}, cleanup, err
	}

	source, err = release.Load(sourceRoot)
	if err != nil {
		return domain.Release{}, domain.Release{}, cleanup, err
	}
	if warning, deprecated := source.Manifest.DeprecationWarning(); deprecated {
		d.Bus.Publish(events.Message(events.LevelWarn,
			"this bundle's api_version %s is deprecated: %s",
			source.Manifest.APIVersion, warning))
	}
	if resolved.Version.IsZero() || !resolved.Version.Equal(source.Version()) {
		return domain.Release{}, domain.Release{}, cleanup,
			domain.Internal(nil, "source resolved %s but the bundle declares %s",
				resolved.Version, source.Version())
	}

	staged = source
	staged.Root = d.Paths.ReleaseDir(source.Version().String())

	// A version already present in the store with different content is a
	// conflict, not something to overwrite: two different bundles claiming
	// one version is exactly what content-addressed identity exists to
	// catch. Detected here rather than mid-operation so it reports as a
	// validation failure with nothing journaled as rolled back.
	if existing, loadErr := release.Load(staged.Root); loadErr == nil {
		if !atomicfs.SameDigest(existing.Digest, source.Digest) {
			return domain.Release{}, domain.Release{}, cleanup,
				domain.ValidationError(domain.ErrDigestMismatch,
					"release %s is already installed with a different digest", source.Version()).
					WithHint("installed %s, incoming %s — these are different bundles "+
						"claiming the same version", shortDigest(existing.Digest), shortDigest(source.Digest))
		}
	}

	return source, staged, cleanup, nil
}

func updateFlags(opts UpdateOptions) map[string]string {
	flags := map[string]string{"ref": opts.Ref}
	if opts.SkipBackup {
		// Recorded because it is the choice an incident review will want
		// to see was made deliberately.
		flags["skip_backup"] = "true"
	}
	if opts.ExpectDigest != "" {
		flags["expect_digest"] = opts.ExpectDigest
	}
	return flags
}

func updateSummary(rec domain.OperationRecord, from domain.ReleaseRecord, to domain.Release) string {
	if rec.Status != domain.StatusSucceeded {
		return ""
	}
	if from.IsZero() {
		return fmt.Sprintf("installed %s %s", to.Name(), to.Version())
	}
	return fmt.Sprintf("updated %s from %s to %s", to.Name(), from.Version, to.Version())
}

// updateSteps is the update pipeline: four steps of its own, then apply's.
func updateSteps(
	d *Deps,
	inst domain.Installation,
	from domain.ReleaseRecord,
	source, staged domain.Release,
	opts UpdateOptions,
) []engine.Step {
	steps := []engine.Step{
		stepVerifyBundle(d, inst, source, opts),
		stepCheckCompatibility(d, from, staged),
		stepPreUpdateBackup(d, inst, from, staged, opts),
		stepStageUpdate(d, from, source, staged, opts),
	}

	// The convergence pipeline is apply's, reused against the new release.
	// Duplicating those eleven steps would mean two lists to keep in
	// agreement, and the second one would be the one nobody tests.
	//
	// A dry run plans against the bundle where it currently sits, because
	// nothing has been staged: reading from the release store would report
	// every template and hook as missing and tell the operator their bundle
	// was broken when it is merely not installed yet.
	converge := staged
	if opts.DryRun {
		converge = source
	}
	return append(steps, applySteps(d, inst, converge, opts.Options)...)
}

// stepVerifyBundle checks the bundle against what the operator expected before
// anything in it is trusted as configuration or executed as a hook.
func stepVerifyBundle(d *Deps, inst domain.Installation, source domain.Release, opts UpdateOptions) engine.Step {
	return engine.Step{
		ID:          "verify-bundle",
		Description: "verify bundle",
		Idempotent:  true,
		OnFailure:   engine.Abort, // nothing has mutated yet
		Timeout:     5 * time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			if d.Verifier == nil {
				return domain.Internal(nil, "no verifier is configured")
			}

			// The policy comes from the installation, never from the
			// bundle: a bundle asserting it needs no signature, or
			// naming the key that may sign it, would defeat the
			// check it is supposed to satisfy.
			err := d.Verifier.Verify(ctx, ports.BundlePath(source.Root), ports.Expectation{
				Digest:     opts.ExpectDigest,
				Required:   inst.Policy.RequireSignature,
				PublicKeys: inst.Policy.SigningKeys,
			})
			if err != nil {
				return err
			}

			if opts.ExpectDigest == "" {
				st.Detail("%s (digest not pinned)", shortDigest(source.Digest))
			} else {
				st.Detail("%s matches", shortDigest(source.Digest))
			}
			return nil
		},
	}
}

// stepCheckCompatibility is the gate. It mutates nothing and aborts on failure.
//
// `--force` deliberately does not bypass it: a release declaring it cannot be
// installed over what is running is stating a fact about its migrations, not
// expressing a preference.
func stepCheckCompatibility(d *Deps, from domain.ReleaseRecord, staged domain.Release) engine.Step {
	return engine.Step{
		ID:          "check-compatibility",
		Description: "check compatibility",
		Idempotent:  true,
		OnFailure:   engine.Abort,
		Timeout:     time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			report := domain.CheckUpgrade(
				from.Version,
				staged.Version(),
				staged.Manifest.Compatibility,
				d.ManagerVersion,
				from.SchemaAtInstall,
			)

			for _, w := range report.Warnings {
				st.Warn("%s", w)
			}
			if err := report.Err(); err != nil {
				return err
			}

			// The manager does not own the database. Saying so is
			// better than letting an operator believe the schema was
			// checked when no release reported one.
			if from.SchemaAtInstall == 0 && staged.Manifest.Compatibility.DatabaseSchemaMax > 0 {
				st.Warn("database schema version is unknown, so the schema range was not checked")
			}
			return nil
		},
	}
}

// stepPreUpdateBackup takes the backup an operator will want if this goes
// wrong.
//
// It has no Compensate on purpose: a backup taken immediately before a failed
// update is the most valuable artifact in the system at that moment, and its
// `pre-update` reason exempts it from retention pruning.
func stepPreUpdateBackup(
	d *Deps,
	inst domain.Installation,
	from domain.ReleaseRecord,
	staged domain.Release,
	opts UpdateOptions,
) engine.Step {
	return engine.Step{
		ID:          "pre-update-backup",
		Description: "pre-update backup",
		Idempotent:  false, // each run produces a separately identified backup
		OnFailure:   engine.Abort,
		Timeout:     2 * time.Hour,
		Check: func(ctx context.Context, st *engine.State) (bool, error) {
			switch {
			case from.IsZero():
				// A first install has nothing to back up.
				return true, nil
			case opts.SkipBackup:
				st.Warn("skipping the pre-update backup at the operator's request")
				return true, nil
			case d.Backup == nil:
				return false, domain.BackupError(domain.ErrUnsupported,
					"no backup engine is configured").
					WithHint("re-run with --skip-backup --force to proceed without one")
			default:
				return false, nil
			}
		},
		Execute: func(ctx context.Context, st *engine.State) error {
			ref, err := d.Backup.Create(ctx, ports.Scope{
				Components: ports.AllComponents,
				Reason:     "pre-update",
			}, map[string]string{
				"from": from.Version.String(),
				"to":   staged.Version().String(),
			})
			if err != nil {
				return err
			}
			st.Set(engine.KeyBackupRef, ref)
			st.Detail("%s", ref.ID)

			d.pushPreUpdateBackup(ctx, st, inst, ref)
			return nil
		},
	}
}

// stepStageUpdate copies the bundle into the release store.
//
// Its compensation is what makes the whole operation safe: it returns the
// release pointer and the `current` symlink to whatever they were when the
// operation began. Because compensation runs newest-first, this runs *after*
// the apply steps have undone their own work, which is the right order --
// secrets and configuration revert first, then the pointer.
func stepStageUpdate(d *Deps, from domain.ReleaseRecord, source, staged domain.Release, opts UpdateOptions) engine.Step {
	return engine.Step{
		ID:          "stage-release",
		Description: "stage release " + staged.Version().String(),
		Idempotent:  true,
		OnFailure:   engine.Compensate,
		Timeout:     15 * time.Minute,
		Check: func(ctx context.Context, st *engine.State) (bool, error) {
			existing, err := release.Load(staged.Root)
			if err != nil {
				return false, nil // not staged yet
			}
			if atomicfs.SameDigest(existing.Digest, staged.Digest) {
				return true, nil // already staged, byte for byte
			}
			// Unreachable: resolveUpdateTarget rejects this before the
			// operation starts. Kept as defence in depth, because
			// silently overwriting a release directory would break the
			// digest identity everything else depends on.
			return false, domain.ValidationError(domain.ErrDigestMismatch,
				"release %s is already installed with a different digest", staged.Version())
		},
		Execute: func(ctx context.Context, st *engine.State) error {
			ref, err := ports.ParseRef(opts.Ref)
			if err != nil {
				return err
			}

			// Staged into a hidden sibling and renamed into place. A
			// crash mid-extraction otherwise leaves a partial tree at
			// the digest-addressed path -- and if that partial tree
			// happens to load, the retry refuses it as "installed with
			// a different digest", an error whose only remedy is
			// hand-deleting a directory everything else treats as
			// immutable.
			parent := filepath.Dir(staged.Root)
			if err := atomicfs.MkdirAll(parent, 0o755); err != nil {
				return err
			}

			// Staging dirs from earlier crashes are this operation's
			// own debris, removed under the same deployment lock that
			// wrote them.
			stale, _ := filepath.Glob(filepath.Join(parent, ".staging-*"))
			for _, dir := range stale {
				_ = atomicfs.RemoveAll(dir)
			}

			// A tree at the final path reached Execute only because
			// Check's release.Load failed -- usually a partial
			// extraction, but a transient I/O or permission failure
			// reads the same way, and deleting an operator's release
			// on that evidence would be irreversible. Moved aside
			// into the staging namespace instead: this run proceeds,
			// the tree survives for inspection, and the *next* run's
			// sweep above reclaims it.
			if _, statErr := os.Stat(staged.Root); statErr == nil {
				aside := filepath.Join(parent,
					fmt.Sprintf(".staging-debris-%d", time.Now().UnixNano()))
				if err := os.Rename(staged.Root, aside); err != nil {
					return domain.Internal(err,
						"cannot move the unreadable tree at %s aside", staged.Root)
				}
				st.Warn("moved an unreadable tree at %s aside as %s; the next update reclaims it",
					staged.Root, filepath.Base(aside))
			}

			tmp, err := os.MkdirTemp(parent, ".staging-")
			if err != nil {
				return domain.Internal(err, "cannot create a staging directory in %s", parent)
			}
			defer func() { _ = atomicfs.RemoveAll(tmp) }()

			if _, err := d.Source.Fetch(ctx, ref, tmp); err != nil {
				return err
			}
			// MkdirTemp creates 0700; the release store is 0755, as
			// CopyTree and ExtractTarZst would have made the final
			// path themselves.
			if err := os.Chmod(tmp, 0o755); err != nil {
				return domain.Internal(err, "cannot set mode on the staged release")
			}
			// Every directory entry inside the tree, then the tree's
			// own entry. File contents were fsynced as they were
			// written; without the directory half a power cut can
			// keep the promoted root while losing entries inside it
			// -- a tree the digest blessed, minus files.
			atomicfs.SyncTree(tmp)
			if err := os.Rename(tmp, staged.Root); err != nil {
				return domain.Internal(err, "cannot move the staged release into place")
			}
			atomicfs.SyncDir(parent)
			return nil
		},
		Verify: func(ctx context.Context, st *engine.State) error {
			// The copy must be faithful. The digest covers contents,
			// paths and the executable bit, so a hook that lost its
			// exec bit in transit fails here rather than at the
			// moment it is run.
			landed, err := release.Load(staged.Root)
			if err != nil {
				return err
			}
			if !atomicfs.SameDigest(landed.Digest, source.Digest) {
				return domain.ValidationError(domain.ErrDigestMismatch,
					"the staged release does not match the bundle it came from").
					WithHint("expected %s, got %s",
						shortDigest(source.Digest), shortDigest(landed.Digest))
			}
			return nil
		},
		Compensate: func(ctx context.Context, st *engine.State) error {
			// The staged directory is deliberately left in place: it
			// is immutable and digest-addressed, and removing it
			// would destroy evidence an operator may want. `release
			// prune` reclaims it later.
			if from.IsZero() {
				// Nothing was current before this operation, so
				// there is nothing to restore the pointer to.
				st.Warn("no previous release to restore; run `morzer doctor` to check what is running")
				return nil
			}
			if err := d.State.SetCurrentRelease(ctx, from); err != nil {
				return err
			}
			return atomicfs.ReplaceSymlink(from.Root, d.Paths.CurrentLink())
		},
	}
}

// shortDigest trims a digest for a message. The full value is in the journal.
func shortDigest(d string) string {
	const prefix = "sha256:"
	if len(d) > len(prefix)+16 {
		return d[:len(prefix)+16] + "…"
	}
	return d
}
