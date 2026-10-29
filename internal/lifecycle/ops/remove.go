package ops

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/lifecycle/engine"
	"github.com/morzecrew/morzer/internal/ports"
)

// RemoveOptions are the inputs to `installation remove` (RFC 0034).
type RemoveOptions struct {
	Options

	// Purge also removes what the default keeps: the named volumes, the
	// data, the backups, the attestations, the secret state and the machine
	// key. Backup targets are untouched either way.
	Purge bool

	// ConfirmedInstallationID is the id the operator typed. A production
	// installation is not removed without it, as it is not restored without
	// it: a typed id cannot be answered by reflex.
	ConfirmedInstallationID string
}

// RemoveReport is what a removal did, or would do.
type RemoveReport struct {
	Product        string `json:"product"`
	InstallationID string `json:"installation_id,omitempty"`
	DryRun         bool   `json:"dry_run"`
	Purged         bool   `json:"purged"`

	// Kept is every path the removal left on this machine, so an operator
	// does not need to know the layout to find what survived it.
	Kept []string `json:"kept"`

	// VolumesKept says the runtime's named volumes were left, which no path
	// in Kept can say.
	VolumesKept bool `json:"volumes_kept"`

	// Journal is where the installation's journal is now. Empty on a plan,
	// and when it could not be renamed.
	Journal string `json:"journal,omitempty"`

	Images ImagesPrune `json:"images"`
}

// keyRemovedImages carries remove-releases' image report to the result.
const keyRemovedImages = "removed-images"

// Remove takes an installation off this machine (RFC 0034).
//
// Every step is idempotent and aborts on failure, and nothing is compensated:
// putting units back and restarting a project the operator asked to remove
// would undo the request. A removal that fails part way is finished by running
// it again, which is why the installation record is the last thing it forgets
// -- the re-run finds the same installation and asks for the same confirmation.
func Remove(ctx context.Context, d *Deps, opts RemoveOptions) (Result, error) {
	if err := d.noSelection(); err != nil {
		return Result{}, err
	}

	exists, err := d.State.InstallationExists(ctx)
	if err != nil {
		return Result{}, err
	}
	var inst domain.Installation
	if exists {
		if inst, err = d.State.LoadInstallation(ctx); err != nil {
			// Refused rather than read as "no record": the record is
			// what says whether this is production and which id
			// confirms it, and a removal that guessed would skip the
			// confirmation on exactly the machine that needs it.
			return Result{}, domain.InstallationError(err,
				"cannot read the installation record that says how this removal is confirmed").
				WithHint("move %s aside, and the removal then treats this machine as one "+
					"with no installation record", d.Paths.InstallationState())
		}
	} else if left, err := d.anythingLeft(ctx); err != nil {
		return Result{}, err
	} else if !left {
		// A typo in --product would otherwise journal a removal of
		// nothing, and create the directories it journals into.
		return Result{}, domain.InstallationError(domain.ErrInstallation,
			"nothing of %s is on this machine", d.Paths.Product).
			WithHint("`morzer ls` lists the installations here")
	}

	// Both refusals before the lock, so they journal nothing -- and for a
	// plan as well, which refuses what the operation would refuse.
	if !opts.Force {
		return Result{}, domain.Usage("removing an installation is destructive and requires --force").
			WithHint("it takes %s down and removes its units, releases and configuration; "+
				"its data is kept unless --purge is given", d.Paths.Product)
	}
	// A dev installation was declared disposable when it was created, so
	// --force is enough there -- but a typed id that names another
	// installation is a removal aimed at the wrong machine, on any machine.
	if exists && (opts.ConfirmedInstallationID != "" || !inst.IsDev()) &&
		opts.ConfirmedInstallationID != inst.ID {
		return Result{}, domain.Usage("removing this installation requires confirming its id").
			WithHint("re-run with --confirm %s", inst.ID)
	}

	current, currentErr := d.State.CurrentRelease(ctx)
	rel, relErr := domain.Release{}, currentErr
	if currentErr == nil {
		rel, relErr = d.resolveCurrentRelease(ctx, current)
	}
	// Deployed unless nothing says a release was ever installed: no record
	// and no `current` link. A recorded release whose directory is gone was
	// running, and "not found" from it must not read as "never applied".
	deployed := exists && (currentErr != nil || !current.IsZero() ||
		!errors.Is(relErr, domain.ErrReleaseNotFound))

	var prior *domain.OperationRecord
	if opts.Resume {
		if prior, err = d.findResumable(ctx, domain.OpTypeRemove); err != nil {
			return Result{}, err
		}
	}

	opID := d.newOpID()
	op := engine.Operation{
		ID:          opID,
		Type:        domain.OpTypeRemove,
		Description: "remove " + d.Paths.Product,
		Steps:       removeSteps(d, inst, deployed, rel, relErr, opts.Purge),
		Flags:       removeFlags(opts),
	}

	report := RemoveReport{
		Product:        d.Paths.Product,
		InstallationID: inst.ID,
		DryRun:         opts.DryRun,
		Purged:         opts.Purge,
		VolumesKept:    !opts.Purge,
		Images:         ImagesPrune{Removed: []string{}, Kept: []KeptImage{}},
	}

	var result engine.Result
	runErr := d.withLock(ctx, opID, domain.OpTypeRemove, opts.Options, func(ctx context.Context) error {
		// Re-checked under the lock, as every operation planned from the
		// installed release is: with --wait, an update or a rollback queued
		// ahead may have moved it, and the project would be taken down
		// with the files of a release that is no longer the one running.
		if exists && currentErr == nil {
			if err := d.refuseMovedRelease(ctx, current); err != nil {
				return err
			}
		}

		// Resolved before the first step, because their credentials are
		// read from the secret state remove-configuration deletes (§5.4).
		var targets []ports.TargetRef
		if exists && !opts.DryRun {
			var err error
			if targets, err = d.attestationTargets(ctx, inst); err != nil {
				d.warnf("cannot resolve where attestations are pushed: %s",
					domain.AsError(err).Message)
			}
		}

		// Re-read under the lock, as apply does: the holder we waited
		// behind may have been finishing the same removal.
		if opts.Resume {
			var err error
			if prior, err = d.refreshResumable(ctx, domain.OpTypeRemove, prior); err != nil {
				return err
			}
		}

		var stepsErr error
		result, stepsErr = d.Engine.Run(ctx, op, d.engineOptions(opts.Options, inst.ID, prior))
		if opts.DryRun {
			return stepsErr
		}

		d.notifyFinished(ctx, opID, domain.OpTypeRemove, result.Record, stepsErr)

		// A failure is attested as well as a success, as every operation
		// does. With no installation record there is nothing to attest
		// to, and no target configured to push it to.
		if exists {
			in := attestationInputs(inst, rel, current, domain.Version{}, nil)
			if path, ok := fileAttestation(ctx, d, result.Record, in); ok {
				pushStatementTo(ctx, d, targets, path)
			}
		}

		// Named by the record, not by opID: a resumed removal continues
		// the record it resumed, under that record's id.
		if stepsErr == nil {
			report.Journal = d.finishRemoval(result.Record.ID)
		}
		return stepsErr
	})

	// No state when the lock was refused: the engine never ran.
	if result.State != nil {
		if images, err := engine.GetTyped[ImagesPrune](result.State, keyRemovedImages); err == nil {
			report.Images = images
		}
	}
	report.Kept = d.keptAfterRemoval(opts.Purge, opts.DryRun, report.Journal)

	out := Result{Record: result.Record, Data: report}
	if runErr != nil {
		return out, runErr
	}
	out.Summary = removeSummary(d.Paths.Product, opts)
	return out, nil
}

func removeFlags(opts RemoveOptions) map[string]string {
	flags := map[string]string{"force": "true"}
	if opts.Purge {
		// Recorded because it is the choice an incident review asks
		// about: whether the data went with the installation.
		flags["purge"] = "true"
	}
	return flags
}

func removeSummary(product string, opts RemoveOptions) string {
	switch {
	case opts.DryRun && opts.Purge:
		return fmt.Sprintf("would remove %s and its data", product)
	case opts.DryRun:
		return fmt.Sprintf("would remove %s and keep its data", product)
	case opts.Purge:
		return fmt.Sprintf("%s removed, and its data with it", product)
	default:
		return fmt.Sprintf("%s removed; its data was kept", product)
	}
}

// removeSteps is §5.3's table, in its order.
func removeSteps(
	d *Deps, inst domain.Installation, deployed bool, rel domain.Release, relErr error, purge bool,
) []engine.Step {
	steps := []engine.Step{
		stepRemoveUnits(d),
		stepTakeDownProject(d, inst, deployed, rel, relErr, purge),
		stepRemoveReleases(d),
		stepRemoveTree("remove-rendered-secrets", "remove rendered secrets", d.Paths.RunDir),
	}
	if purge {
		steps = append(steps, stepRemoveEntries("remove-data",
			"remove data, backups and attestations", d.Paths.VarDir,
			keepNames(filepath.Base(d.Paths.ManagerDir())), "", ""))
	}
	return append(steps,
		stepRemoveEntries("remove-configuration", "remove configuration and keys",
			d.Paths.EtcDir, d.configurationKept(purge),
			"and the signing key last, once it has signed this removal's statement", ""),
		// The record last within the step, as the step is last within
		// the operation: a failure part way leaves the confirmation a
		// retry has to ask for.
		stepRemoveEntries("forget-installation", "forget the installation",
			d.Paths.ManagerDir(), d.managerKept(purge), "",
			filepath.Base(d.Paths.InstallationState())),
	)
}

// stepRemoveUnits removes every unit the supervisor manages for the product.
//
// First, so no timer starts an update or a backup while the rest goes.
func stepRemoveUnits(d *Deps) engine.Step {
	// The files on disk, whether or not systemd is running to act on them.
	installed := func(ctx context.Context) ([]string, error) {
		if d.Supervisor == nil {
			return nil, nil
		}
		return d.Supervisor.InstalledUnits(ctx, d.Paths.Product)
	}
	return engine.Step{
		ID:          "remove-units",
		Description: "remove systemd units",
		Idempotent:  true,
		OnFailure:   engine.Abort,
		Timeout:     2 * time.Minute,
		Check: func(ctx context.Context, st *engine.State) (bool, error) {
			units, err := installed(ctx)
			return len(units) == 0, err
		},
		Execute: func(ctx context.Context, st *engine.State) error {
			units, err := installed(ctx)
			if err != nil {
				return err
			}
			// Left and said, not removed: with nothing to stop or disable
			// them, deleting the files leaves enablement links pointing
			// at nothing, and saying nothing leaves the product starting
			// at the next boot that runs systemd.
			if !d.Supervisor.Available(ctx) {
				st.Warn("%s left in place: systemd is not running here, so they cannot be "+
					"stopped or disabled; remove them once it is", strings.Join(units, ", "))
				return nil
			}
			st.Detail("%s", strings.Join(units, ", "))
			// Every managed name rather than the installed ones, as
			// init's compensation does: RemoveUnits tolerates a name
			// with no file, and stopping it costs nothing.
			return d.Supervisor.RemoveUnits(ctx, d.Supervisor.ManagedUnitNames(d.Paths.Product))
		},
		PlanDetail: func(ctx context.Context, st *engine.State) (string, string) {
			units, err := installed(ctx)
			if err != nil || len(units) == 0 {
				return "", ""
			}
			return strings.Join(units, ", "), ""
		},
	}
}

// stepTakeDownProject removes the containers and networks, and the named
// volumes only under --purge.
//
// With the release's own files when the current release resolves. Otherwise by
// project name alone, from an empty directory, where Compose finds what is left
// by its labels (decision 10). An installation whose release was never recorded
// was never applied, so nothing of it is running.
func stepTakeDownProject(
	d *Deps, inst domain.Installation, deployed bool, rel domain.Release, relErr error, purge bool,
) engine.Step {
	volumes := "keeping named volumes"
	if purge {
		volumes = "removing named volumes"
	}
	how := func() string {
		if relErr != nil {
			return fmt.Sprintf("by project name, because the current release cannot be read "+
				"(%s); %s", domain.AsError(relErr).Message, volumes)
		}
		return volumes
	}

	return engine.Step{
		ID:          "stop-services",
		Description: "take the project down",
		Idempotent:  true,
		OnFailure:   engine.Abort,
		Timeout:     10 * time.Minute,
		Check: func(ctx context.Context, st *engine.State) (bool, error) {
			return !deployed, nil
		},
		Execute: func(ctx context.Context, st *engine.State) error {
			var cfg ports.RuntimeConfig
			if relErr == nil {
				var err error
				if cfg, err = d.runtimeConfig(rel, inst, ""); err != nil {
					return err
				}
			} else {
				empty, err := os.MkdirTemp("", "morzer-remove-")
				if err != nil {
					return domain.Internal(err, "cannot create an empty project directory")
				}
				defer func() { _ = os.RemoveAll(empty) }()
				cfg = ports.RuntimeConfig{
					Product:    inst.Product,
					Options:    inst.RuntimeOptions,
					WorkingDir: empty,
				}
				// Every converge records the options, so only an
				// installation that has not converged since they were
				// recorded gets here without them -- and then the
				// release that named its project is the one that cannot
				// be read.
				if inst.RuntimeOptions == nil {
					st.Warn("this installation records no runtime options, so its project is "+
						"taken to be named %s; if its release named another, that project "+
						"is left running", inst.Product)
				}
			}
			st.Detail("%s", how())
			return d.Runtime.Down(ctx, cfg, ports.DownOptions{
				Volumes:       purge,
				RemoveOrphans: true,
				Timeout:       2 * time.Minute,
			})
		},
		PlanDetail: func(context.Context, *engine.State) (string, string) {
			if !deployed {
				return "", ""
			}
			return how(), ""
		},
	}
}

// stepRemoveReleases removes the release store, and then the images no release
// left on the machine names.
//
// Planned before anything is removed, because the manifests that name the
// images go with their directories -- and removed after, as a prune does, so an
// interrupted removal leaks names rather than ever removing an image a release
// on disk still needs.
func stepRemoveReleases(d *Deps) engine.Step {
	plan := func(ctx context.Context) imagePlan {
		entries, err := InstalledReleases(ctx, d)
		if err != nil {
			return imagePlan{skipped: "cannot list the releases: " + domain.AsError(err).Message}
		}
		return d.planImageRemoval(entries)
	}
	return engine.Step{
		ID:          "remove-releases",
		Description: "remove releases and their images",
		Idempotent:  true,
		OnFailure:   engine.Abort,
		Timeout:     10 * time.Minute,
		Check: func(ctx context.Context, st *engine.State) (bool, error) {
			there, err := atomicfs.Exists(d.Paths.OptDir)
			return !there, err
		},
		Execute: func(ctx context.Context, st *engine.State) error {
			p := plan(ctx)
			if err := atomicfs.RemoveAll(d.Paths.OptDir); err != nil {
				return err
			}
			images := removePlannedImages(ctx, p, false)
			st.Set(keyRemovedImages, images)
			st.Detail("%d image(s) removed, %d kept", len(images.Removed), len(images.Kept))
			if images.Skipped != "" {
				st.Warn("no image was removed: %s", images.Skipped)
			}
			return nil
		},
		PlanDetail: func(ctx context.Context, st *engine.State) (string, string) {
			images := removePlannedImages(ctx, plan(ctx), true)
			st.Set(keyRemovedImages, images)
			return fmt.Sprintf("%s, and %d image(s)", d.Paths.OptDir, len(images.Removed)), ""
		},
	}
}

// stepRemoveTree removes one directory and everything under it, overwriting
// files first: the tree it is used for holds rendered secrets.
func stepRemoveTree(id, description, dir string) engine.Step {
	return engine.Step{
		ID:          id,
		Description: description,
		Idempotent:  true,
		OnFailure:   engine.Abort,
		Timeout:     time.Minute,
		Check: func(ctx context.Context, st *engine.State) (bool, error) {
			there, err := atomicfs.Exists(dir)
			return !there, err
		},
		Execute: func(ctx context.Context, st *engine.State) error {
			return atomicfs.RemoveWithOverwrite(dir)
		},
		PlanDetail: func(context.Context, *engine.State) (string, string) { return dir, "" },
	}
}

// stepRemoveEntries removes everything directly under dir that keep does not
// name, and the entry called last after all the others. The note finishes the
// plan's line, for what the step leaves to after the operation.
func stepRemoveEntries(id, description, dir string, keep func(string) bool, note, last string) engine.Step {
	return engine.Step{
		ID:          id,
		Description: description,
		Idempotent:  true,
		OnFailure:   engine.Abort,
		Timeout:     10 * time.Minute,
		Check: func(ctx context.Context, st *engine.State) (bool, error) {
			names, err := removableEntries(dir, keep)
			return len(names) == 0, err
		},
		Execute: func(ctx context.Context, st *engine.State) error {
			names, err := removableEntries(dir, keep)
			if err != nil {
				return err
			}
			slices.SortStableFunc(names, func(a, b string) int {
				return cmp.Compare(boolRank(a == last), boolRank(b == last))
			})
			for _, name := range names {
				if err := atomicfs.RemoveAll(filepath.Join(dir, name)); err != nil {
					return err
				}
			}
			st.Detail("%s: %s", dir, strings.Join(names, ", "))
			return nil
		},
		PlanDetail: func(context.Context, *engine.State) (string, string) {
			names, err := removableEntries(dir, keep)
			if err != nil || len(names) == 0 {
				return note, ""
			}
			detail := fmt.Sprintf("%s: %s", dir, strings.Join(names, ", "))
			if note != "" {
				detail += "; " + note
			}
			return detail, ""
		},
	}
}

// removableEntries lists the names under dir that keep does not name. A
// directory that is not there has none.
func removableEntries(dir string, keep func(string) bool) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, domain.Internal(err, "cannot list %s", dir)
	}
	var names []string
	for _, e := range entries {
		if !keep(e.Name()) {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// boolRank orders false before true.
func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

func keepNames(names ...string) func(string) bool {
	return func(name string) bool { return slices.Contains(names, name) }
}

// configurationKept is what remove-configuration leaves under /etc/<product>.
//
// The signing key always, because it signs the removal's own statement after
// the steps and goes then (finishRemoval). The secret state and the machine key
// unless purging, because they unlock the data the default keeps: backups are
// encrypted to the machine key, and a kept database volume was made with the
// credentials the secret state holds (§5.1).
func (d *Deps) configurationKept(purge bool) func(string) bool {
	names := []string{filepath.Base(d.Paths.SigningDir())}
	if !purge {
		names = append(names,
			filepath.Base(d.Paths.AgeDir()),
			filepath.Base(d.Paths.SecretsFile()),
			// The recipients' roles, beside the secret state
			// (sopsage's annotationsPath).
			"secrets.recipients.yaml")
	}
	return keepNames(names...)
}

// managerKept is what forget-installation leaves in the manager's directory.
//
// The lock, which this operation holds. The journal, which the engine is
// writing and finishRemoval renames, and the journals of earlier removals.
// Under --purge all the journals go, and the engine's next append starts the
// live one again holding only this removal's record.
func (d *Deps) managerKept(purge bool) func(string) bool {
	locks := filepath.Base(d.Paths.LockDir())
	journal := filepath.Base(d.Paths.JournalFile())
	removed := filepath.Base(d.Paths.RemovedJournalFile("*"))
	return func(name string) bool {
		if name == locks {
			return true
		}
		if purge {
			return false
		}
		earlier, _ := filepath.Match(removed, name)
		return name == journal || earlier
	}
}

// anythingLeft reports whether any of the product's directories or units is on
// this machine.
func (d *Deps) anythingLeft(ctx context.Context) (bool, error) {
	for _, dir := range []string{d.Paths.EtcDir, d.Paths.VarDir, d.Paths.RunDir, d.Paths.OptDir} {
		if there, err := atomicfs.Exists(dir); err != nil || there {
			return there, err
		}
	}
	if d.Supervisor == nil || !d.Supervisor.Available(ctx) {
		return false, nil
	}
	units, err := d.Supervisor.InstalledUnits(ctx, d.Paths.Product)
	return len(units) > 0, err
}

// finishRemoval does what had to outlive the steps, and returns where the
// journal is now.
//
// The signing key goes after it has signed the removal's statement, and the
// journal is renamed after the engine's last append (§5.4). Each failure is a
// warning: the removal has already happened, and the report says what is left.
func (d *Deps) finishRemoval(opID string) string {
	if err := atomicfs.RemoveAll(d.Paths.SigningDir()); err != nil {
		d.warnf("the signing key at %s was not removed: %s",
			d.Paths.SigningDir(), domain.AsError(err).Message)
	}
	// Only once it is empty, which by default it is not: the secret state
	// and the machine key are still in it. The error is that answer.
	_ = os.Remove(d.Paths.EtcDir)

	archived := d.Paths.RemovedJournalFile(opID)
	if err := os.Rename(d.Paths.JournalFile(), archived); err != nil {
		d.warnf("the journal at %s was not renamed, so a later `init` will read it: %s",
			d.Paths.JournalFile(), err)
		return ""
	}
	return archived
}

// keptAfterRemoval lists the paths a removal leaves, or would leave, that are
// on disk now.
func (d *Deps) keptAfterRemoval(purge, dryRun bool, journal string) []string {
	var candidates []string
	// Under --purge the directory survives only to hold the removal's own
	// statement, which a plan does not write.
	if !purge || !dryRun {
		candidates = append(candidates, d.Paths.AttestationsDir())
	}
	if !purge {
		candidates = append(candidates,
			d.Paths.DataDir(), d.Paths.BackupsDir(), d.Paths.SecretsFile(), d.Paths.AgeDir())
	}
	if journal != "" {
		candidates = append(candidates, journal)
	}
	kept := []string{}
	for _, path := range candidates {
		if there, _ := atomicfs.Exists(path); there {
			kept = append(kept, path)
		}
	}
	return kept
}
