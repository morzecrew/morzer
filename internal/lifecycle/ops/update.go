package ops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

	// Before the new bundle is resolved, and from the release running now.
	// An installation created before schema 10 has no recorded options, and
	// an update is exactly the operation that can change them -- so the
	// baseline has to be taken from what is installed, while it still is.
	//
	// **The resolution error is returned, not skipped.** Skipping it left
	// the baseline nil, which reads as "created before schema 10" and waves
	// any change through -- so an installation whose release tree had been
	// pruned or damaged was the one case where a renamed project could still
	// take a deployment's volumes away from it. Reproduced, then fixed:
	// TestAnUpdateWithNoResolvableBaselineIsRefused.
	//
	// The same release is what a failure would fall back to, and what it can
	// read decides whether falling back is still possible once migrations
	// have run (stepMigrate). Unreadable here is not a refusal when the
	// baseline is already recorded: it is recorded as unknown, which the
	// migrate step treats as unable to read anything.
	baseline := inst.RuntimeOptions
	var fallback *fallbackRelease
	if !from.IsZero() {
		running, relErr := d.resolveCurrentRelease(ctx, from)
		if relErr != nil && baseline == nil {
			return Result{}, relErr
		}
		if baseline == nil {
			baseline = runtimeBaseline(inst, running)
		}
		fallback = &fallbackRelease{
			Version:       from.Version,
			Compatibility: running.Manifest.Compatibility,
			Known:         relErr == nil,
		}
		if relErr == nil {
			fallback.Secrets = declaredSecrets(running)
		}
	}
	inst.RuntimeOptions = baseline

	source, staged, cleanup, err := d.resolveUpdateTarget(ctx, opts)
	// Whatever had to be brought down to read the bundle is scratch, and
	// goes when the operation does. The release itself has already been
	// copied into the store by then.
	defer cleanup()
	if err != nil {
		return Result{}, err
	}

	// The release about to be introduced, against what this installation was
	// created with. Before the operation rather than inside it: a
	// precondition found mid-step is reported as a compensated failure, and
	// the sentence that says the project changed would arrive under an exit
	// code meaning "nothing happened".
	if err := d.refuseRuntimeOptionChange(inst, staged); err != nil {
		return Result{}, err
	}

	opID := d.newOpID()
	var prior *domain.OperationRecord
	if opts.Resume {
		if prior, err = d.findResumable(ctx, domain.OpTypeUpdate); err != nil {
			return Result{}, err
		}
		// The choice the interrupted run journaled holds for its resume.
		// generate-secrets re-runs, as every idempotent step does, and with
		// this command's options it would generate what the operator said
		// to leave alone.
		if prior != nil && prior.Flags["generate_secrets"] == "false" {
			opts.NoGenerateSecrets = true
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
		Steps:       updateSteps(d, inst, from, fallback, source, staged, opts),
		Flags:       updateFlags(inst, opts),
	}

	var result engine.Result
	runErr := d.withLock(ctx, opID, domain.OpTypeUpdate, opts.Options, func(ctx context.Context) error {
		// Under the lock, because it is a read-modify-write of a record
		// `config set` and `init --repair` also write.
		if !opts.DryRun {
			if err := d.persistRuntimeBaseline(ctx, baseline); err != nil {
				return err
			}
		}
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
		if err := d.refuseMovedRelease(ctx, from); err != nil {
			return err
		}
		result, err = d.Engine.Run(ctx, op, d.engineOptions(opts.Options, inst.ID, prior))
		return err
	})

	out := Result{
		Record:  result.Record,
		Summary: updateSummary(result.Record, from, staged, opts.DryRun),
		Data: map[string]any{
			"from":   from.Version.String(),
			"to":     staged.Version().String(),
			"digest": staged.Digest,
			"root":   staged.Root,
		},
	}
	d.notifyFinished(ctx, opID, domain.OpTypeUpdate, result.Record, runErr)

	// An update is the operation the chain check is really about: it is one
	// of the two that move a version, so a machine whose updates filed no
	// statement has a chain nothing can follow. Emitted on failure too --
	// the update that rolled back is what an incident review reads.
	if !opts.DryRun {
		in := attestationInputs(attestedInstallation(inst, staged, result.Record),
			staged, from, from.Version, renderedConfigFor(result.State))
		in.Verification.SignatureVerified, in.Verification.KeyID = verifiedHere(result.State)
		emitAttestation(ctx, d, result.Record, in)
	}

	if runErr != nil {
		return out, runErr
	}
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
	d.warnDeprecations(source.Manifest)
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
						"claiming the same version. A vendor iterating on a release "+
						"should publish a prerelease (1.4.1-dev.7.gabc1234) rather than "+
						"republishing one", shortDigest(existing.Digest), shortDigest(source.Digest))
		}
	}

	return source, staged, cleanup, nil
}

func updateFlags(inst domain.Installation, opts UpdateOptions) map[string]string {
	// Redacted as the stored source reference is: the flags go into the
	// journal and into every notification, and a bundle URL may carry a
	// password.
	flags := map[string]string{"ref": ports.RedactRefCredentials(opts.Ref)}
	if inst.Policy.SkipBackupBeforeUpdate {
		// A durable choice rather than a per-run one, and just as
		// consequential: the journal is where an incident review looks
		// for why there is no backup from just before this update.
		flags["skip_backup_policy"] = "true"
	}
	if opts.SkipBackup {
		// Recorded because it is the choice an incident review will want
		// to see was made deliberately.
		flags["skip_backup"] = "true"
	}
	if opts.ExpectDigest != "" {
		flags["expect_digest"] = opts.ExpectDigest
	}
	if opts.NoGenerateSecrets {
		// The reason a new release's secret is still unset after an
		// update that could have generated it (RFC 0036 decision 5).
		flags["generate_secrets"] = "false"
	}
	return flags
}

// updateSummary says what happened, or what would. See applySummary: a plan
// runs no steps, and a summary that does not ask describes one as finished.
func updateSummary(rec domain.OperationRecord, from domain.ReleaseRecord,
	to domain.Release, dryRun bool) string {
	if rec.Status != domain.StatusSucceeded {
		return ""
	}
	if from.IsZero() {
		if dryRun {
			return fmt.Sprintf("would install %s %s", to.Name(), to.Version())
		}
		return fmt.Sprintf("installed %s %s", to.Name(), to.Version())
	}
	if dryRun {
		return fmt.Sprintf("would update %s from %s to %s", to.Name(), from.Version, to.Version())
	}
	return fmt.Sprintf("updated %s from %s to %s", to.Name(), from.Version, to.Version())
}

// updateSteps is the update pipeline: four steps of its own, then apply's.
func updateSteps(
	d *Deps,
	inst domain.Installation,
	from domain.ReleaseRecord,
	fallback *fallbackRelease,
	source, staged domain.Release,
	opts UpdateOptions,
) []engine.Step {
	// The parameters the new release stopped declaring are dropped from the
	// installation before anything downstream resolves them, and the step
	// makes that durable. Both halves are needed: without the step the
	// value comes back on the next command, and without the drop here the
	// converge steps below still resolve against it and refuse the update.
	// A parameter the new release stopped declaring has to leave the
	// resolution before anything downstream resolves against it -- every
	// consumer validates the whole recorded map, so one retired name would
	// refuse the converge. That part is in memory and costs nothing to
	// redo.
	//
	// Making it durable is a separate step, and it runs *last*: see
	// stepRetireParameters.
	retired := domain.Parameters(inst.Parameters).ValidateAgainst(staged.Manifest.Parameters)
	if len(retired) > 0 {
		inst = withoutParameters(inst, retired)
	}

	steps := []engine.Step{
		stepVerifyBundle(d, inst, source, opts),
		stepCheckCompatibility(d, from, fallback, staged),
		stepPreUpdateBackup(d, inst, from, staged, opts),
	}
	// Nothing was running before a first install, so there is nothing to
	// bring back.
	if !from.IsZero() {
		steps = append(steps, stepRestorePoint(d, inst, from, opts.Profile))
	}
	steps = append(steps, stepStageUpdate(d, from, source, staged, opts))

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
	// Between staging and converging, because the images have to be on the
	// machine before the preflight that refuses without them -- and because
	// the bundle they come out of is only on disk once staging has put it
	// there. A dry run ingests from wherever the bundle currently sits, for
	// the same reason the converge does.
	steps = append(steps, stepIngestImages(d, fixedRelease(converge)))
	// Before the converge, so load-secrets reads, registers and renders
	// what it generates, and still refuses a new secret with no generator.
	steps = append(steps, stepGenerateSecrets(d, converge, fallback, opts.Options))
	// The ref the operator resolved is recorded with the release once it
	// becomes current, which is what gives `update --check` something to
	// query later without asking them to remember.
	convergeOpts := opts.Options
	convergeOpts.SourceRef = ports.RedactRefCredentials(opts.Ref)
	convergeOpts.PreviousVersion = from.Version
	steps = append(steps, applySteps(d, inst, converge, convergeOpts)...)
	steps = append(steps, stepRetireParameters(d, staged, retired))

	// Retention runs once a newer backup exists, and the pre-update backup
	// is one. Last, so a failed update removes nothing.
	prune := stepPruneBackups(d, inst, staged)
	prune.Check = tookNoBackup
	steps = append(steps, prune)
	if inst.Backup.HasTargets() {
		steps = append(steps, stepPruneRemoteBackups(d, inst, staged))
	}

	// A sandbox prunes after every update; a production machine does not,
	// and that asymmetry is deliberate rather than an oversight. On a
	// production host retention is an operator's decision about disk they
	// own, and removing a release behind their back is removing something
	// `rollback --to` might have wanted. A dev loop builds a release per
	// commit, each with its own images/ layout, and nothing else in the
	// system ever reclaims them.
	if inst.IsDev() {
		steps = append(steps, stepPruneReleases(d))
	}
	return steps
}

// withoutParameters copies an installation with the named parameters removed.
func withoutParameters(inst domain.Installation, names []string) domain.Installation {
	next := make(map[string]string, len(inst.Parameters))
	for k, v := range inst.Parameters {
		next[k] = v
	}
	for _, name := range names {
		delete(next, name)
	}
	inst.Parameters = next
	return inst
}

// attestedInstallation is the installation a statement describes once an
// update or a rollback to rel has finished. Its retire-parameters step is the
// last one that can fail, so the values rel does not declare are gone exactly
// when the operation succeeded, and the config digest beside the names was
// rendered without them.
func attestedInstallation(inst domain.Installation, rel domain.Release, rec domain.OperationRecord) domain.Installation {
	if rec.Status != domain.StatusSucceeded {
		return inst
	}
	return withoutParameters(inst, domain.Parameters(inst.Parameters).ValidateAgainst(rel.Manifest.Parameters))
}

// stepRetireParameters drops recorded values the new release no longer
// declares, and is the last step of the update on purpose. A rollback ends with
// it too, for the values only the release it leaves declared.
//
// A vendor may stop declaring a parameter; that is their decision and not a
// failure, which is why the operator is told rather than refused. But the
// recorded value cannot simply stay: every later resolve validates the whole
// map against the release's declarations, so one retired name would refuse
// `apply`, `config` and `status` from the first command after the update
// onwards -- with a message about a parameter the operator never typed.
//
// Last, because that is what makes it safe to interrupt. Nothing runs after it,
// so no failure can require it to be undone -- and a compensation could not
// undo it reliably anyway: a resumed run reloads an installation the values are
// already gone from, and would have nothing to put back. Everything before it
// unwinds with the values still recorded, which is correct, because what
// unwinds to is the release that declares them.
//
// Aborting rather than compensating for the same reason: by the time this runs
// the deployment is converged and healthy, and rolling that back over a failed
// bookkeeping write would be the tail wagging the dog. The operator is left
// with a working deployment and a state file one parameter stale, which the
// next command reports.
//
// What it retires is decided when it runs, from the installation as recorded
// then, not from planned: planned was read before the operation took the lock,
// and a `config set` landing in between would otherwise stay recorded. Only a
// name the release does not declare can arrive that way, and nothing the
// converge rendered reads one, so the steps before it are no less right.
func stepRetireParameters(d *Deps, staged domain.Release, planned []string) engine.Step {
	description := "retire parameters the release does not declare"
	if len(planned) > 0 {
		// Named in the description, because a dry run is where an
		// operator gets to see a state change before it happens, and
		// "some parameters" is not something anybody can review.
		description = "retire parameters: " + strings.Join(planned, ", ")
	}
	undeclared := func(inst domain.Installation) []string {
		return domain.Parameters(inst.Parameters).ValidateAgainst(staged.Manifest.Parameters)
	}

	return engine.Step{
		ID:          "retire-parameters",
		Description: description,
		Idempotent:  true,
		OnFailure:   engine.Abort,
		Timeout:     time.Minute,
		Check: func(ctx context.Context, st *engine.State) (bool, error) {
			current, err := d.State.LoadInstallation(ctx)
			if err != nil {
				return false, err
			}
			return len(undeclared(current)) == 0, nil
		},
		Execute: func(ctx context.Context, st *engine.State) error {
			current, err := d.State.LoadInstallation(ctx)
			if err != nil {
				return err
			}
			retired := undeclared(current)

			if err := d.saveInstallation(ctx, withoutParameters(current, retired)); err != nil {
				return err
			}

			// After the write, not before it: an operator told their
			// values were retired by an operation that then failed to
			// record it would have been told something untrue about
			// their own configuration.
			d.Bus.Publish(events.Message(events.LevelWarn,
				"release %s does not declare %s; the recorded value(s) are retired",
				staged.Version(), strings.Join(retired, ", ")))
			st.Detail("retired %s", strings.Join(retired, ", "))
			return nil
		},
	}
}

// verifiedHere is the signature this operation's verify-bundle verified, as a
// statement reports its own check: true and the key, or nothing. The one
// operation that verifies a signature itself is `update`. Nil state is an update
// refused before the engine ran, which verified nothing.
func verifiedHere(st *engine.State) (*bool, string) {
	if st == nil {
		return nil, ""
	}
	check, err := engine.GetTyped[domain.SignatureCheck](st, keySignatureCheck)
	if err != nil {
		return nil, ""
	}
	verified := true
	return &verified, check.KeyID
}

// keySignatureCheck is the signature verify-bundle verified, if it verified one,
// which record-state writes into the release record.
const keySignatureCheck = "signature-check"

// verifyBundle runs the verifier and returns the id of the key a signature
// verified against, or "" when it verified none -- including when the verifier
// cannot say, which is then read as no signature verified rather than guessed.
func (d *Deps) verifyBundle(ctx context.Context, bundle ports.BundlePath, expect ports.Expectation) (string, error) {
	if r, ok := d.Verifier.(ports.SignatureReporter); ok {
		return r.VerifySigned(ctx, bundle, expect)
	}
	return "", d.Verifier.Verify(ctx, bundle, expect)
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
			key, err := d.verifyBundle(ctx, ports.BundlePath(source.Root), ports.Expectation{
				Digest:     opts.ExpectDigest,
				Required:   inst.Policy.RequireSignature,
				PublicKeys: inst.Policy.SigningKeys,
			})
			if err != nil {
				return err
			}
			if key != "" {
				st.Set(keySignatureCheck, domain.SignatureCheck{
					KeyID: key, OperationID: st.OpID, At: domain.NewTime(d.now()),
				})
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
func stepCheckCompatibility(
	d *Deps, from domain.ReleaseRecord, fallback *fallbackRelease, staged domain.Release,
) engine.Step {
	return engine.Step{
		ID:          "check-compatibility",
		Description: "check compatibility",
		Idempotent:  true,
		OnFailure:   engine.Abort,
		Timeout:     time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			// Here because this step runs early and again on every
			// resume, which is what keeps the state the migrate step
			// reads from depending on which process set it.
			if fallback != nil {
				st.Set(engine.KeyFallbackRelease, *fallback)
			}

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

// tookNoBackup skips the update's local retention when it took no backup of
// its own: retention makes room for a newer backup, and there is none.
func tookNoBackup(_ context.Context, st *engine.State) (bool, error) {
	ref, err := engine.GetTyped[ports.BackupRef](st, engine.KeyBackupRef)
	return err != nil || ref.IsZero(), nil
}

// stepPreUpdateBackup takes the backup an operator will want if this goes
// wrong.
//
// It has no Compensate on purpose: a backup taken immediately before a failed
// update is the most valuable artifact in the system at that moment, and its
// `pre-update` reason exempts the newest of them from retention pruning.
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
			case inst.Policy.SkipBackupBeforeUpdate:
				// The installation's own policy, which until now
				// was recorded, compared by doctor, and read by
				// nothing that decides anything.
				st.Warn("skipping the pre-update backup: " +
					"skip_backup_before_update is set for this installation")
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
			// The update is about to reach a registry for its images, so
			// the helper this backup reads volumes through is fetched now
			// rather than refused for below. The pull step comes too late:
			// a machine installed before apply pulled the helper, or one
			// whose unused busybox `docker image prune -a` removed, has none.
			if len(staged.Manifest.PulledImageRefs()) > 0 {
				if current, err := d.currentReleaseFor(ctx); err == nil {
					if cfg, err := d.runtimeConfig(current, inst, ""); err == nil {
						d.pullVolumeHelper(ctx, st, cfg)
					}
				}
			}

			ref, err := d.Backup.Create(ctx, ports.Scope{
				Components: ports.AllComponents,
				Reason:     ports.ReasonPreUpdate,
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
			// wrote them. Moved-aside debris trees are exempt: they
			// are kept for inspection (bounded to one, below), and a
			// back-to-back scheduled update must not erase the one
			// thing left to diagnose a corrupted release with.
			stale, _ := filepath.Glob(filepath.Join(parent, ".staging-*"))
			for _, dir := range stale {
				if strings.HasPrefix(filepath.Base(dir), ".staging-debris-") {
					continue
				}
				_ = atomicfs.RemoveAll(dir)
			}

			// A tree at the final path reached Execute only because
			// Check's release.Load failed -- usually a partial
			// extraction, but a transient I/O or permission failure
			// reads the same way, and deleting an operator's release
			// on that evidence would be irreversible.
			//
			// When that unreadable tree is the *currently installed*
			// release, nothing is touched: moving the live root aside
			// on a transient read failure would leave `current`
			// dangling, and a failed fetch would then have nothing to
			// compensate back to. The refusal names the real problem.
			//
			// Otherwise it is moved aside for inspection -- replacing
			// any older debris, so exactly one such tree is kept.
			if _, statErr := os.Stat(staged.Root); statErr == nil {
				// The live root can be known by record or only by the
				// `current` symlink; either way it is never moved.
				liveRoot := from.Root
				if liveRoot == "" {
					if link, lerr := os.Readlink(d.Paths.CurrentLink()); lerr == nil {
						liveRoot = link
					}
				}
				if liveRoot != "" && staged.Root == liveRoot {
					return domain.InstallationError(nil,
						"the currently installed release at %s cannot be read", staged.Root).
						WithHint("run `morzer doctor`; if the directory is damaged, " +
							"restore from a backup rather than updating over it")
				}
				// Renamed aside first, older debris removed after: the
				// reverse order would destroy the one kept tree and
				// then fail the rename on the same transient class
				// this block exists to survive, retaining nothing. A
				// failed removal here leaves an extra tree, not a
				// lost one, so it does not abort.
				aside := filepath.Join(parent,
					fmt.Sprintf(".staging-debris-%d", time.Now().UnixNano()))
				if err := os.Rename(staged.Root, aside); err != nil {
					return domain.Internal(err,
						"cannot move the unreadable tree at %s aside", staged.Root)
				}
				older, _ := filepath.Glob(filepath.Join(parent, ".staging-debris-*"))
				for _, dir := range older {
					if dir != aside {
						_ = atomicfs.RemoveAll(dir)
					}
				}
				st.Warn("moved an unreadable tree at %s aside as %s; it is kept until "+
					"the next tree needs moving aside", staged.Root, filepath.Base(aside))
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
			// A record from before roots were recorded names its
			// release only through the link, and the link has not
			// moved: record-state moves it, and record-state did not
			// run. Replacing it from an empty root failed instead.
			if from.Root == "" {
				return nil
			}
			return atomicfs.ReplaceSymlink(from.Root, d.Paths.CurrentLink())
		},
	}
}

// stepRestorePoint does nothing on the way forward. Its compensator is the one
// that brings the previous release's services back up.
//
// A step of its own because a compensator runs only when its step ran. The
// staging step, where this used to live, is skipped when the release is already
// in the store -- every `update --to`, and so every unattended install of what
// the channel staged -- and the restore was skipped with it. Placed before
// staging, so it is compensated after everything else: the configuration and
// secrets are back by then (restorePreviousRuntime).
func stepRestorePoint(d *Deps, inst domain.Installation, from domain.ReleaseRecord, profile string) engine.Step {
	return engine.Step{
		ID:          "restore-point",
		Description: "keep " + from.Version.String() + " to bring back on failure",
		Idempotent:  true,
		OnFailure:   engine.Abort,
		Execute:     func(context.Context, *engine.State) error { return nil },
		Compensate: func(ctx context.Context, st *engine.State) error {
			return d.restorePreviousRuntime(ctx, st, inst, from, profile)
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

// stepPruneReleases reclaims what a fast rebuild loop accumulates.
//
// Last, and non-fatal. It runs after the release is current and after the
// parameters are retired, so what it prunes is decided against final state --
// and a failure to reclaim disk must never fail an update that has already
// converged. The engine's Continue is exactly that: the operation carries on and
// the journal keeps the reason.
func stepPruneReleases(d *Deps) engine.Step {
	return engine.Step{
		ID:          "prune-releases",
		Description: "prune old releases",
		Idempotent:  true,
		OnFailure:   engine.Continue,
		// Directories and then their images, and removing an image's last
		// name deletes its layers, which for a large one takes minutes.
		Timeout: 30 * time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			res, err := PruneReleases(ctx, d, 0, false)
			if err != nil {
				return err
			}
			if len(res.Removed) == 0 {
				st.Detail("nothing to prune")
				return nil
			}
			st.Detail("removed %v", res.Removed)
			if len(res.Images.Removed) > 0 {
				st.Detail("removed %d image(s): %v", len(res.Images.Removed), res.Images.Removed)
			}
			for _, k := range res.Images.Kept {
				st.Detail("kept image %s: %s", k.Ref, k.Why)
			}
			// A warning, because the cause outlives this update: until
			// it is fixed, no update on this machine removes images.
			if res.Images.Skipped != "" {
				st.Warn("removed no images: %s", res.Images.Skipped)
			}
			return nil
		},
	}
}

// restorePreviousRuntime brings the release that was running back up, when the
// update got as far as replacing its containers. When it stopped earlier, the
// previous release is still up and only its secrets are put back, if
// render-secrets' compensator kept the new release's for those containers: the
// new release may not declare one they read (RFC 0001 decision 25).
//
// Compensation used to put back only files, so a failed update left the new
// release's containers running against the old release's configuration until
// somebody ran `apply` -- on an unattended machine, until the next boot (RFC
// 0001 §9, decision 19). Here, from the restore point's compensator, which
// runs after every later step's: the configuration compensator has put the old
// files back by now, and the old release's secrets are rendered over the new
// release's before its services start; rendering prunes any file the old
// release does not declare.
//
// Never reached past a point of no return: the engine does not compensate the
// steps before one, and the restore point is one of them.
//
// An error here is a compensation that did not complete, which ends the update
// at requires-manual-intervention with both failures in its error.
func (d *Deps) restorePreviousRuntime(
	ctx context.Context, st *engine.State, inst domain.Installation, from domain.ReleaseRecord, profile string,
) error {
	if _, touched := st.Get(keyServicesTouched); !touched {
		if _, kept := st.Get(keySecretsKept); kept {
			st.Detail("putting back the secrets of %s", from.Version)
			_, err := d.renderReleaseSecrets(ctx, from)
			return err
		}
		return nil
	}
	if _, stale := st.Get(keyConfigNotRestored); stale {
		return domain.InstallationError(nil,
			"not bringing %s back up: its configuration could not be put back", from.Version)
	}
	st.Detail("bringing %s back up", from.Version)

	previous, err := d.renderReleaseSecrets(ctx, from)
	if err != nil {
		return err
	}
	cfg, err := d.runtimeConfig(previous, inst, profile)
	if err != nil {
		return err
	}
	if err := d.Runtime.Up(ctx, cfg, ports.UpOptions{
		Wait:          true,
		WaitTimeout:   5 * time.Minute,
		RemoveOrphans: true,
	}); err != nil {
		return err
	}

	specs, err := d.checkSpecs(inst, previous, st.OpID, domain.OpTypeUpdate)
	if err != nil {
		return err
	}
	if len(specs) == 0 {
		return nil
	}
	_, err = d.Health.WaitReady(ctx, specs)
	return err
}

// renderReleaseSecrets renders a release's secrets over the directory its
// containers read, pruning any it does not declare.
func (d *Deps) renderReleaseSecrets(ctx context.Context, rec domain.ReleaseRecord) (domain.Release, error) {
	rel, err := d.resolveCurrentRelease(ctx, rec)
	if err != nil {
		return domain.Release{}, err
	}
	schema, err := release.LoadSecretSchema(rel)
	if err != nil {
		return domain.Release{}, err
	}
	if _, err := d.Secrets.Render(ctx, d.Paths.SecretsRenderDir(), schema); err != nil {
		return domain.Release{}, err
	}
	return rel, nil
}
