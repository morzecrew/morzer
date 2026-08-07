package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/infra/logging"
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

	// Push copies the backup to every configured target. On by default: a
	// target an operator configured and the manager quietly stopped using
	// is the failure this whole mechanism exists to prevent.
	//
	// Turning it off is for the operator who knows the medium is
	// disconnected and wants a local backup anyway.
	Push bool

	// PruneRemote applies the retention policy on each target too. On by
	// default, because a target nothing prunes fills up, and the first sign
	// of that is a failed push during an incident.
	PruneRemote bool
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

	// What happened to the volumes, said out loud rather than left in the
	// manifest. An operator whose uploads volume was skipped because the
	// vendor excluded it has to learn that from the command that took the
	// backup, not from a file they will read for the first time during a
	// restore.
	if summary := d.volumeSummary(ctx, ref); summary != "" {
		out.Summary += ", " + summary
	}

	// Where it went is the part worth saying out loud. A backup on one disk
	// and a backup in two places read the same in a log otherwise, and the
	// difference is the whole point of having configured a target.
	if pushed := engine.MustGet[[]ports.RemoteRef](result.State, engine.KeyPushedBackups); len(pushed) > 0 {
		targets := make([]ports.TargetRef, len(pushed))
		for i, p := range pushed {
			targets[i] = p.Target
		}
		out.Summary += ", copied to " + targetSummary(targets)
	}

	out.Data = ref
	return out, nil
}

// volumeSummary describes what the backup did about the project's storage.
//
// Best effort, and deliberately: this runs after a backup has succeeded, and a
// manifest that will not re-read is a reason to say less, not a reason to
// report the backup as failed.
func (d *Deps) volumeSummary(ctx context.Context, ref ports.BackupRef) string {
	manifest, err := d.Backup.Inspect(ctx, ref)
	if err != nil {
		return ""
	}

	var hot, cold int
	for _, c := range manifest.VolumeRecords() {
		if c.Volume.Consistency == ports.ConsistencyHot {
			hot++
			continue
		}
		cold++
	}

	var parts []string
	switch {
	case cold > 0 && hot > 0:
		parts = append(parts, fmt.Sprintf("%d volume(s): %d cold, %d hot", cold+hot, cold, hot))
	case cold > 0:
		parts = append(parts, fmt.Sprintf("%d volume(s) captured cold", cold))
	case hot > 0:
		parts = append(parts, fmt.Sprintf("%d volume(s) captured hot", hot))
	}

	// Named, not counted. "2 volumes were not captured" tells an operator
	// they have a problem without telling them which one, which is the
	// worst of both messages.
	if skipped := uncapturedNames(manifest.Uncaptured); skipped != "" {
		parts = append(parts, "not captured: "+skipped)
	}
	return strings.Join(parts, ", ")
}

// uncapturedNames lists what was left out, bounded so a project with fifty
// bind mounts does not produce a summary line nobody reads.
func uncapturedNames(uncaptured []ports.UncapturedVolume) string {
	const limit = 4

	names := make([]string, 0, len(uncaptured))
	for _, u := range uncaptured {
		names = append(names, u.Volume)
	}
	if len(names) == 0 {
		return ""
	}
	if len(names) > limit {
		extra := len(names) - limit
		names = append(names[:limit], fmt.Sprintf("and %d more", extra))
	}
	return strings.Join(names, ", ")
}

// PushOptions configures a push of a backup that already exists.
type PushOptions struct {
	Options

	// BackupID selects the backup; empty means the most recent.
	BackupID string
}

// Push copies an existing backup to every configured target.
//
// It exists because the push step fails the backup when a target is
// unreachable, and the remedy has to be something better than "take another
// backup": the data is already on the disk, correct and verified, and what
// failed was the network. This is the retry.
func Push(ctx context.Context, d *Deps, opts PushOptions) (Result, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return Result{}, err
	}
	if !inst.Backup.HasTargets() {
		return Result{}, domain.Usage("this installation configures no backup targets").
			WithHint("add one with `morzer backup target add file:///mnt/backups`")
	}

	// Bounded by `--timeout`, like every other operation. It is applied to
	// the context here because that flag otherwise reaches only the step
	// engine, which this command does not run: an operator who bounded a
	// push to five minutes got no bound at all, and a target that accepts a
	// connection and then stops answering held the command open indefinitely.
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	var out Result
	// Under the deployment lock, like every other command that writes. A
	// manual push used to run beside the scheduled backup: the push copying
	// a backup to a target while the backup's retention pass pruned that
	// same target, each acting on a listing the other had just changed. It
	// also made `--wait` a flag that did nothing, because nothing waited.
	err = d.withLock(ctx, d.newOpID(), domain.OpTypeBackup, opts.Options,
		func(ctx context.Context) error {
			// Read again inside the lock. With `--wait` the lock is held
			// by somebody else for as long as their operation takes, and
			// the target list read before that wait is the list as it was
			// minutes ago -- including targets since removed.
			inst, err := d.loadInstallation(ctx)
			if err != nil {
				return err
			}

			ref, err := d.resolveBackupRef(ctx, opts.BackupID)
			if err != nil {
				return err
			}

			// Verified before it is copied, for the same reason the backup
			// operation verifies before it pushes: a backup that fails its
			// checksums must not be put anywhere a restore might find it.
			if err := d.Backup.Verify(ctx, ref); err != nil {
				return err
			}

			targets, err := d.resolveTargets(ctx, inst)
			if err != nil {
				return err
			}

			// After resolving and verifying, so a dry run still answers the
			// questions worth asking -- does the backup exist, does it
			// verify, where would it go -- and before the one thing that
			// writes.
			if opts.DryRun {
				out = Result{
					Summary: fmt.Sprintf("would copy backup %s to %s",
						ref.ID, targetSummary(targets)),
					Data: ref,
				}
				return nil
			}

			for _, target := range targets {
				if _, err := d.Targets.Push(ctx, target, ref.Path, ref.ID); err != nil {
					// Only this target's partial write is removed.
					// The copies already placed on the targets
					// before it are complete and verified, and
					// deleting them would leave the deployment
					// with less off-machine data than the retry
					// started with.
					d.unpush(ctx, []ports.RemoteRef{{Target: target, ID: ref.ID}})

					return domain.BackupError(err,
						"cannot copy backup %s to %s", ref.ID, target)
				}
			}

			out = Result{
				Summary: fmt.Sprintf("backup %s copied to %s", ref.ID, targetSummary(targets)),
				Data:    ref,
			}
			return nil
		})
	if err != nil {
		return Result{}, err
	}
	return out, nil
}

// backupSteps assembles the sequence.
//
// The order is the argument: create, verify, push, prune. Pushing after
// verification because copying a backup that failed its own checksums is
// putting a known-bad file in a second place; pruning after pushing because
// retention that ran first could remove the copy the push was about to make.
func backupSteps(d *Deps, inst domain.Installation, rel domain.Release, opts BackupOptions) []engine.Step {
	steps := []engine.Step{
		stepCreateBackup(d, inst, rel, opts),
	}
	if opts.Verify {
		steps = append(steps, stepVerifyBackup(d))
	}
	if opts.Push && inst.Backup.HasTargets() {
		steps = append(steps, stepPushBackup(d, inst))
	}
	if opts.Prune {
		steps = append(steps, stepPruneBackups(d, inst, rel))
		if opts.PruneRemote && inst.Backup.HasTargets() {
			steps = append(steps, stepPruneRemoteBackups(d, inst, rel))
		}
	}
	return steps
}

// stepPushBackup copies the backup to every configured target.
//
// A failed push fails the backup. Retention failing is `Continue` because a
// disk that stays fuller than intended is a smaller problem than a backup
// reported as failed after it produced one -- but this is not that. The purpose
// of a target is that the data is somewhere the machine's failure does not
// reach, and reporting success for a backup that is still only on the machine
// that will die is precisely the state targets exist to end.
//
// `Abort` rather than `Compensate`, and the reason is worth writing down
// because the obvious choice is wrong. Compensation walks *every* completed
// step newest-first, and the oldest of those is `create-backup`, whose
// compensation deletes the backup. A failed push would therefore have left the
// operator with no backup at all -- strictly worse than before targets existed,
// and the exact opposite of the promise that the local copy is kept either way.
//
// So the partial remote copies are cleaned up here, inline, where the cleanup
// can be scoped to what this step did. RFC 0009 §5.4 says Compensate; it was
// wrong, and the amendment records why.
func stepPushBackup(d *Deps, inst domain.Installation) engine.Step {
	return engine.Step{
		ID:          "push-backup",
		Description: "copy the backup to " + describeTargetCount(len(inst.Backup.Targets)),
		Idempotent:  true, // a re-push overwrites; it does not duplicate
		OnFailure:   engine.Abort,
		Timeout:     2 * time.Hour,
		Execute: func(ctx context.Context, st *engine.State) error {
			ref, err := engine.GetTyped[ports.BackupRef](st, engine.KeyBackupRef)
			if err != nil {
				return err
			}
			targets, err := d.resolveTargets(ctx, inst)
			if err != nil {
				return err
			}

			pushed := make([]ports.RemoteRef, 0, len(targets))
			for _, target := range targets {
				remote, pushErr := d.Targets.Push(ctx, target, ref.Path, ref.ID)
				if pushErr != nil {
					// Only this target's partial write is
					// removed -- not the copies that already
					// landed whole on the targets before it.
					//
					// Removing those too was a defect with a
					// long fuse. Three targets and the third
					// permanently unreachable meant every
					// nightly backup was copied to the two
					// good media and then deleted from them,
					// so a deployment with two working
					// targets kept no off-machine copy at
					// all -- worse than having none
					// configured, and invisible until the
					// machine was gone.
					d.unpush(ctx, []ports.RemoteRef{{Target: target, ID: ref.ID}})

					return domain.BackupError(pushErr,
						"the backup was taken but could not be copied to %s", target).
						WithHint("the backup is still on this machine at %s; "+
							"fix the target, then run `morzer backup push %s`",
							ref.Path, ref.ID)
				}
				pushed = append(pushed, remote)
			}

			st.Set(engine.KeyPushedBackups, pushed)
			st.Detail("%s", targetSummary(targets))
			return nil
		},
	}
}

// unpush removes copies a failed push left behind, best effort.
//
// Failures are logged and dropped. The operation is already failing, and
// replacing "the backup could not be copied to your bucket" with "the cleanup
// of the copy that failed also failed" would bury the sentence the operator
// needs. What is left behind has no manifest, so nothing will offer it as a
// backup; the next successful push overwrites it.
func (d *Deps) unpush(ctx context.Context, refs []ports.RemoteRef) {
	// A detached context, because the commonest reason a push failed is that
	// this one was cancelled or timed out -- and cleanup on a dead context
	// fails instantly, leaving exactly the partial copy it was meant to
	// remove. Bounded, so a target that has stopped answering cannot hold the
	// operation open after it has already failed.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()

	for _, remote := range refs {
		if err := d.Targets.Remove(cleanup, remote); err != nil {
			logging.FromContext(ctx).Warn("cannot remove a partial backup copy",
				"target", remote.Target.String(), "backup", remote.ID, "error", err)
		}
	}
}

// stepPruneRemoteBackups applies retention on each target.
//
// `Continue`, like the local pass and for the same reason: the backup is taken
// and it is off the machine, which is everything the operation promised. A
// target that stays fuller than intended is a warning, not a failed backup.
func stepPruneRemoteBackups(d *Deps, inst domain.Installation, rel domain.Release) engine.Step {
	return engine.Step{
		ID:          "prune-remote-backups",
		Description: "apply retention on the backup targets",
		Idempotent:  true,
		OnFailure:   engine.Continue,
		Timeout:     30 * time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			targets, err := d.resolveTargets(ctx, inst)
			if err != nil {
				return err
			}
			policy := ports.RetentionPolicy{
				Keep:        inst.RetentionBackups(rel.Manifest),
				KeepReasons: []string{"pre-update"},
			}

			// Every target is pruned even after one fails. Returning at
			// the first error left later targets silently unpruned, and
			// a target nothing prunes fills up -- which surfaces much
			// later, as a failed push during an incident.
			var total int
			var errs []error
			for _, target := range targets {
				removed, err := d.remoteRetention(ctx, target, policy)
				total += len(removed)
				if err != nil {
					errs = append(errs, err)
				}
			}
			if total > 0 {
				st.Detail("removed %d old backup(s) from the target(s)", total)
			}
			return errors.Join(errs...)
		},
	}
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
		return out, restoreLeftBehind(result.Record, runErr)
	}
	out.Summary = fmt.Sprintf("restored %s from backup %s", inst.Product, ref.ID)
	return out, nil
}

// restoreLeftBehind says what state an interrupted restore leaves.
//
// A restore stops the product before it writes anything, and an interruption
// deliberately skips compensation: the operator pressed ctrl-C, and a long
// automatic bring-up is not what "stop" means. What that leaves is a deployment
// that is down -- which is a fine outcome to have chosen and a terrible one to
// have to infer from silence.
//
// It is a hint rather than an automatic recovery on purpose. Restore is not
// resumable: its middle step overwrites a database, no automatic action can
// tell how far a hook got, and guessing a repair is the failure mode the design
// forbids. The two roads forward are the operator's to choose.
func restoreLeftBehind(rec domain.OperationRecord, err error) error {
	if rec.Status != domain.StatusInterrupted || !servicesLeftStopped(rec) {
		return err
	}
	recovery := "the services were stopped before the restore began, and an interrupted " +
		"operation is not brought back up automatically. Run `morzer apply` to " +
		"start the current release again, or re-run the restore to try once more. " +
		"`morzer status` shows which."

	// Appended rather than assigned: WithHint replaces, and whatever the
	// interrupted step said about itself is the other half of what the
	// operator needs.
	if existing := domain.AsError(err).Hint; existing != "" {
		return domain.AsError(err).WithHint("%s %s", existing, recovery)
	}
	return domain.AsError(err).WithHint("%s", recovery)
}

// servicesLeftStopped reports whether the stop succeeded and nothing brought
// the product back.
func servicesLeftStopped(rec domain.OperationRecord) bool {
	stopped := false
	for _, step := range rec.Steps {
		switch step.ID {
		case "stop-services":
			stopped = step.Status == domain.StepSucceeded
		case "reapply-release":
			if step.Status == domain.StepSucceeded {
				return false
			}
		}
	}
	return stopped
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
