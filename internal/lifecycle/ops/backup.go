package ops

import (
	"context"
	"fmt"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/lifecycle/engine"
	"github.com/morzecrew/morzer/internal/ports"
)

// BackupOptions configures a backup run.
type BackupOptions struct {
	Options

	// Reason records why the backup was taken. It lands in the backup
	// manifest and makes retention decisions explicable months later.
	Reason string

	// Components limits the scope; empty means everything.
	Components []ports.Component

	Labels map[string]string

	// Verify re-reads the backup and checks its checksums before
	// reporting success. On by default: a backup that has never been read
	// back is a hope, not a backup.
	Verify bool

	// Prune applies the retention policy after a successful backup.
	Prune bool
}

// Backup coordinates a backup of the database, files, configuration, the
// encrypted secret state and the release manifest.
func Backup(ctx context.Context, d *Deps, opts BackupOptions) (Result, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return Result{}, err
	}
	current, err := d.State.CurrentRelease(ctx)
	if err != nil {
		return Result{}, err
	}
	rel, err := d.resolveCurrentRelease(ctx, current)
	if err != nil {
		return Result{}, err
	}

	if opts.Reason == "" {
		opts.Reason = "manual"
	}

	opID := d.newOpID()
	op := engine.Operation{
		ID:          opID,
		Type:        domain.OpTypeBackup,
		Description: "backup " + inst.Product,
		To:          rel.Version(),
		Steps:       backupSteps(d, inst, rel, opts),
	}

	var result engine.Result
	runErr := d.withLock(ctx, opID, domain.OpTypeBackup, opts.Options, func(ctx context.Context) error {
		var err error
		result, err = d.Engine.Run(ctx, op, d.engineOptions(opts.Options, inst.ID, nil))
		return err
	})

	out := Result{Record: result.Record}
	if runErr != nil {
		return out, runErr
	}

	ref := engine.MustGet[ports.BackupRef](result.State, engine.KeyBackupRef)
	out.Summary = fmt.Sprintf("backup %s created (%s)", ref.ID, domain.ByteSize(ref.Size))
	out.Data = ref
	return out, nil
}

func backupSteps(d *Deps, inst domain.Installation, rel domain.Release, opts BackupOptions) []engine.Step {
	steps := []engine.Step{
		stepCreateBackup(d, inst, rel, opts),
	}
	if opts.Verify {
		steps = append(steps, stepVerifyBackup(d))
	}
	if opts.Prune {
		steps = append(steps, stepPruneBackups(d, inst, rel))
	}
	return steps
}

// stepCreateBackup runs the release's backup hook and wraps the result.
//
// Compensation deletes a backup this step created. A backup that exists but
// failed verification is worse than none: someone will eventually restore it.
func stepCreateBackup(d *Deps, inst domain.Installation, rel domain.Release, opts BackupOptions) engine.Step {
	return engine.Step{
		ID:          "create-backup",
		Description: "create backup",
		Idempotent:  false, // each run produces a new, separately-identified backup
		OnFailure:   engine.Compensate,
		Timeout:     2 * time.Hour,
		Execute: func(ctx context.Context, st *engine.State) error {
			ref, err := d.Backup.Create(ctx, ports.Scope{
				Components: opts.Components,
				Reason:     opts.Reason,
			}, opts.Labels)
			if err != nil {
				return err
			}
			st.Set(engine.KeyBackupRef, ref)
			st.Detail("%s", ref.ID)
			return nil
		},
		Compensate: func(ctx context.Context, st *engine.State) error {
			ref, err := engine.GetTyped[ports.BackupRef](st, engine.KeyBackupRef)
			if err != nil || ref.IsZero() {
				return nil
			}
			// Pruning to zero would refuse to delete the most recent
			// backup, so removal goes through the engine's own
			// knowledge of what it just created.
			return d.removeBackup(ctx, ref)
		},
	}
}

func stepVerifyBackup(d *Deps) engine.Step {
	return engine.Step{
		ID:          "verify-backup",
		Description: "verify backup checksums",
		Idempotent:  true,
		OnFailure:   engine.Compensate,
		Timeout:     30 * time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			ref, err := engine.GetTyped[ports.BackupRef](st, engine.KeyBackupRef)
			if err != nil {
				return err
			}
			return d.Backup.Verify(ctx, ref)
		},
	}
}

// stepPruneBackups applies retention. Failure is non-fatal: a disk that stays
// fuller than intended is a smaller problem than a backup operation reported
// as failed after it successfully produced a backup.
func stepPruneBackups(d *Deps, inst domain.Installation, rel domain.Release) engine.Step {
	return engine.Step{
		ID:          "prune-backups",
		Description: "apply retention policy",
		Idempotent:  true,
		OnFailure:   engine.Continue,
		Timeout:     5 * time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			removed, err := d.Backup.Prune(ctx, ports.RetentionPolicy{
				Keep: inst.RetentionBackups(rel.Manifest),
				// A pre-update backup is exempt until the update
				// it guards has been confirmed good.
				KeepReasons: []string{"pre-update"},
			})
			if err != nil {
				return err
			}
			if len(removed) > 0 {
				st.Detail("removed %d old backup(s)", len(removed))
			}
			return nil
		},
	}
}

// removeBackup deletes one backup, bypassing the retention policy's refusal to
// remove the most recent one. Used only for compensation of a backup this
// process just created.
func (d *Deps) removeBackup(ctx context.Context, ref ports.BackupRef) error {
	if ref.Path == "" {
		return nil
	}
	return atomicfs.RemoveAll(ref.Path)
}

// RestoreOptions configures a restore.
type RestoreOptions struct {
	Options

	// BackupID selects the backup; empty means the most recent.
	BackupID string

	Components []ports.Component

	// ConfirmedInstallationID is the ID the operator typed to confirm.
	// Restore is destructive, so the confirmation is the installation's
	// own identifier rather than a y/n: it cannot be answered by reflex.
	ConfirmedInstallationID string

	// AllowCrossInstallation permits restoring a backup that belongs to a
	// different installation.
	//
	// It is separate from Force because Force is already mandatory for any
	// restore at all. Passing Force down as the cross-installation
	// authorisation made the guard unreachable: every restore that got far
	// enough to be checked had already been forced. This is the flag that
	// actually says "yes, another machine's data, on purpose".
	AllowCrossInstallation bool

	// IdentityFile decrypts the backup, defaulting to this machine's own
	// age identity.
	//
	// The case it exists for: the machine that took the backup is gone, and
	// the key in hand is the offline recovery one. A rebuilt machine has a
	// new identity that was never a recipient of the old machine's backups,
	// so without this the backups an operator carefully kept offsite would
	// be the one thing they could not read.
	IdentityFile string
}

// Restore returns the system to a backed-up state.
//
// The order matters and is the reason this is a step sequence rather than a
// call to the engine adapter: writers are stopped before anything is restored,
// and the release is re-applied afterwards, because restored data and running
// containers holding stale state is a combination that corrupts quietly.
func Restore(ctx context.Context, d *Deps, opts RestoreOptions) (Result, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return Result{}, err
	}

	if !opts.Force {
		return Result{}, domain.Usage("restore is destructive and requires --force").
			WithHint("it replaces the current database and files with the backup's contents")
	}
	// The typed confirmation is checked here rather than at the CLI layer
	// so a scripted caller cannot skip it by calling the operation
	// directly.
	if opts.ConfirmedInstallationID != inst.ID {
		return Result{}, domain.Usage(
			"restore requires confirming the installation id").
			WithHint("re-run with --confirm %s", inst.ID)
	}

	current, err := d.State.CurrentRelease(ctx)
	if err != nil {
		return Result{}, err
	}
	rel, err := d.resolveCurrentRelease(ctx, current)
	if err != nil {
		return Result{}, err
	}

	ref, err := d.resolveBackupRef(ctx, opts.BackupID)
	if err != nil {
		return Result{}, err
	}

	opID := d.newOpID()
	op := engine.Operation{
		ID:          opID,
		Type:        domain.OpTypeRestore,
		Description: "restore " + inst.Product + " from " + ref.ID,
		To:          rel.Version(),
		Flags:       map[string]string{"backup_id": ref.ID, "force": "true"},
		Steps:       restoreSteps(d, inst, rel, ref, opts),
	}

	var result engine.Result
	runErr := d.withLock(ctx, opID, domain.OpTypeRestore, opts.Options, func(ctx context.Context) error {
		var err error
		result, err = d.Engine.Run(ctx, op, d.engineOptions(opts.Options, inst.ID, nil))
		return err
	})

	out := Result{Record: result.Record}
	if runErr != nil {
		return out, runErr
	}
	out.Summary = fmt.Sprintf("restored %s from backup %s", inst.Product, ref.ID)
	return out, nil
}

func restoreSteps(d *Deps, inst domain.Installation, rel domain.Release, ref ports.BackupRef, opts RestoreOptions) []engine.Step {
	return []engine.Step{
		stepVerifyBackupBeforeRestore(d, ref),
		stepCheckRestoreCompatibility(d, rel, ref),
		stepStopServices(d, inst, rel),
		stepRunRestore(d, inst, ref, opts),
		stepReapply(d, inst, rel),
		stepRestoreSmokeTest(d, inst, rel),
	}
}

// stepVerifyBackupBeforeRestore checks the backup before anything is stopped.
//
// Discovering a corrupt backup after the product has been taken down is the
// worst possible ordering: the system would be off, the data unrestored, and
// the operator with no path forward.
func stepVerifyBackupBeforeRestore(d *Deps, ref ports.BackupRef) engine.Step {
	return engine.Step{
		ID:          "verify-backup",
		Description: "verify backup integrity",
		Idempotent:  true,
		OnFailure:   engine.Abort,
		Timeout:     30 * time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			return d.Backup.Verify(ctx, ref)
		},
	}
}

// stepCheckRestoreCompatibility refuses a backup the installed release cannot
// read.
func stepCheckRestoreCompatibility(d *Deps, rel domain.Release, ref ports.BackupRef) engine.Step {
	return engine.Step{
		ID:          "check-compatibility",
		Description: "check release compatibility",
		Idempotent:  true,
		OnFailure:   engine.Abort,
		Timeout:     time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			manifest, err := d.Backup.Inspect(ctx, ref)
			if err != nil {
				return err
			}

			if manifest.SchemaAtBackup > 0 {
				compat := rel.Manifest.Compatibility
				if compat.DatabaseSchemaMax > 0 && manifest.SchemaAtBackup > compat.DatabaseSchemaMax {
					return domain.IncompatibleError(nil,
						"the backup holds schema %d but release %s supports at most %d",
						manifest.SchemaAtBackup, rel.Version(), compat.DatabaseSchemaMax).
						WithHint("install a release that can read this schema before restoring")
				}
			}

			if !manifest.ReleaseVersion.Equal(rel.Version()) {
				// Not fatal: restoring an older backup and
				// letting migrations bring it forward is a
				// legitimate recovery path.
				st.Warn("the backup was taken on %s but %s is installed; "+
					"migrations will run after the restore",
					manifest.ReleaseVersion, rel.Version())
			}
			return nil
		},
	}
}

// stepStopServices takes the product down so nothing writes during the
// restore.
func stepStopServices(d *Deps, inst domain.Installation, rel domain.Release) engine.Step {
	return engine.Step{
		ID:          "stop-services",
		Description: "stop services",
		Idempotent:  true,
		OnFailure:   engine.Compensate,
		Timeout:     10 * time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			cfg, err := d.runtimeConfig(rel, inst, "")
			if err != nil {
				return err
			}
			st.Set(engine.KeyRuntimeConfig, cfg)

			// Volumes are emphatically not removed: the restore
			// hook writes into them, and destroying them here would
			// discard the only copy of anything the backup does not
			// cover.
			return d.Runtime.Down(ctx, cfg, ports.DownOptions{Timeout: 2 * time.Minute})
		},
		Compensate: func(ctx context.Context, st *engine.State) error {
			cfg, err := engine.GetTyped[ports.RuntimeConfig](st, engine.KeyRuntimeConfig)
			if err != nil {
				return nil
			}
			// Bring the product back up: an aborted restore should
			// leave it serving, not stopped.
			return d.Runtime.Up(ctx, cfg, ports.UpOptions{Wait: true, WaitTimeout: 5 * time.Minute})
		},
	}
}

// stepRunRestore executes the release's restore hook.
//
// Deliberately not compensable. Once the hook has begun overwriting a
// database, no automatic action can put back what was there, and pretending
// otherwise would be the "guess a repair" failure mode the design forbids. A
// failure here escalates to requires-manual-intervention.
func stepRunRestore(d *Deps, inst domain.Installation, ref ports.BackupRef, opts RestoreOptions) engine.Step {
	return engine.Step{
		ID:          "restore-data",
		Description: "restore database and files",
		Idempotent:  false,
		OnFailure:   engine.Compensate,
		// Once the hook has begun overwriting a database, no automatic
		// action can put back what was there.
		RequiresInterventionOnFailure: true,
		Timeout:                       3 * time.Hour,
		Execute: func(ctx context.Context, st *engine.State) error {
			return d.Backup.Restore(ctx, ref, ports.RestoreOptions{
				Components: opts.Components,
				// Not opts.Force. Force authorises destroying
				// this machine's data and is required for every
				// restore; using it here too meant the
				// cross-installation guard was checked only
				// after the one thing that disabled it.
				Force:                opts.AllowCrossInstallation,
				TargetInstallationID: inst.ID,
				IdentityFile:         opts.IdentityFile,
			})
		},
	}
}

// stepReapply reconverges the release over the restored data.
func stepReapply(d *Deps, inst domain.Installation, rel domain.Release) engine.Step {
	return engine.Step{
		ID:          "reapply-release",
		Description: "re-apply the release",
		Idempotent:  true,
		OnFailure:   engine.Compensate,
		Timeout:     30 * time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			cfg, err := engine.GetTyped[ports.RuntimeConfig](st, engine.KeyRuntimeConfig)
			if err != nil {
				if cfg, err = d.runtimeConfig(rel, inst, ""); err != nil {
					return err
				}
			}
			return d.Runtime.Up(ctx, cfg, ports.UpOptions{
				Wait:          true,
				WaitTimeout:   10 * time.Minute,
				RemoveOrphans: true,
			})
		},
	}
}

func stepRestoreSmokeTest(d *Deps, inst domain.Installation, rel domain.Release) engine.Step {
	step := stepSmokeTest(d, inst, rel, Options{})
	step.ID = "smoke-test"
	step.Description = "smoke test after restore"
	// A failing smoke test after a restore is information, not a reason to
	// undo the restore: there is nothing to undo it to.
	step.OnFailure = engine.Abort
	return step
}

// resolveBackupRef selects the backup to restore.
func (d *Deps) resolveBackupRef(ctx context.Context, id string) (ports.BackupRef, error) {
	backups, err := d.Backup.List(ctx)
	if err != nil {
		return ports.BackupRef{}, err
	}
	if len(backups) == 0 {
		return ports.BackupRef{}, domain.BackupError(domain.ErrNotFound, "no backups exist").
			WithHint("take one with `morzer backup`")
	}
	if id == "" {
		return backups[0], nil
	}
	for _, b := range backups {
		if b.ID == id {
			return b, nil
		}
	}
	return ports.BackupRef{}, domain.BackupError(domain.ErrNotFound, "no backup with id %q", id).
		WithHint("run `morzer backup list` to see what is available")
}
