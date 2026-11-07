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

	// Unattended is the backup timer's run, `backup --unattended`. It is
	// skipped while the product is stopped (RFC 0040 decision 4): a hook
	// that runs inside a stopped service would fail every night of a long
	// window. An operator's `morzer backup` runs whatever the hooks can do.
	Unattended bool
}

// Backup coordinates a backup of the database, files, configuration, the
// encrypted secret state and the release manifest.
func Backup(ctx context.Context, d *Deps, opts BackupOptions) (Result, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return Result{}, err
	}
	// Exit 0 and a line in the unit's journal, as the update timer's staged
	// outcome is: a timer that failed every night of a maintenance window
	// would train an operator to ignore it.
	if opts.Unattended && inst.Stopped != nil {
		return Result{Summary: fmt.Sprintf("scheduled backup skipped: %s is %s; `morzer backup` takes one by hand",
			inst.Product, inst.Stopped.Describe())}, nil
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
		if err := d.refuseMovedRelease(ctx, current); err != nil {
			return err
		}
		var err error
		result, err = d.Engine.Run(ctx, op, d.engineOptions(opts.Options, inst.ID, nil))
		return err
	})

	out := Result{Record: result.Record}

	// Backup is the operation that most needs this: it is the only one
	// with a generated systemd timer, so on any machine that ran `init`
	// with systemd available it runs nightly with nobody watching. A
	// failure here has always been a journal entry nobody reads.
	d.notifyFinished(ctx, opID, domain.OpTypeBackup, result.Record, runErr)

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
		if opts.Push && opts.PruneRemote && inst.Backup.HasTargets() {
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

// stepPruneRemoteBackups applies retention on each target this run's backup
// reached. Retention runs once a newer backup exists (RFC 0001 decision 22), so
// a target the copy missed, or one a local-only backup never touched, keeps
// what it has.
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
			// Absent when no copy left the machine this run, as after an
			// update that took no backup: there is nothing to prune for.
			pushed := engine.MustGet[[]ports.RemoteRef](st, engine.KeyPushedBackups)
			policy := ports.RetentionPolicy{
				Keep:          inst.RetentionBackups(rel.Manifest),
				KeepPreUpdate: inst.RetentionPreUpdateBackups(),
			}

			// Every target is pruned even after one fails. Returning at
			// the first error left later targets silently unpruned, and
			// a target nothing prunes fills up -- which surfaces much
			// later, as a failed push during an incident.
			var total int
			var errs []error
			for _, remote := range pushed {
				removed, err := d.remoteRetention(ctx, remote.Target, policy)
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
			keep, err := d.unfinishedRestoreBackup(ctx)
			if err != nil {
				return err
			}
			removed, err := d.Backup.Prune(ctx, ports.RetentionPolicy{
				Keep: inst.RetentionBackups(rel.Manifest),
				// The newest pre-update backups are exempt, so the
				// update they guard can still be undone.
				KeepPreUpdate: inst.RetentionPreUpdateBackups(),
				KeepID:        keep,
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

// unfinishedRestoreBackup names the backup the most recent restore read, when
// that restore did not finish: running, interrupted, failed, or waiting on
// intervention, which clearing makes failed. A resume reads it again, so
// retention keeps it (RFC 0051 decision 3).
//
// A journal that cannot be read fails the pruning rather than guessing that no
// restore needs a backup.
func (d *Deps) unfinishedRestoreBackup(ctx context.Context) (string, error) {
	all, err := d.State.Operations(ctx, ports.Filter{})
	if err != nil {
		return "", domain.AsError(err).
			WithHint("retention needs the journal to keep the backup an unfinished restore reads")
	}
	for _, rec := range all {
		if rec.Type != domain.OpTypeRestore {
			continue
		}
		switch rec.Status {
		case domain.StatusRunning, domain.StatusInterrupted, domain.StatusFailed,
			domain.StatusManualIntervention:
			return rec.Flags[flagBackupID], nil
		}
		break
	}
	return "", nil
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

	// A resume continues the restore its record names, with the backup and
	// the scope that restore was started with (RFC 0051 decision 8). A flag
	// naming another would be a different restore.
	if opts.Resume {
		if err := refuseRestoreScopeOnResume(opts); err != nil {
			return Result{}, err
		}
	}

	// Asked again of a resume: it may run a destructive step again, and the
	// typed identifier costs it nothing it did not cost the restore (RFC
	// 0051 decision 2).
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

	var (
		prior *domain.OperationRecord
		ref   ports.BackupRef
	)
	if opts.Resume {
		if prior, err = d.findResumable(ctx, domain.OpTypeRestore); err != nil {
			return Result{}, err
		}
		if opts, err = resumedRestore(opts, *prior); err != nil {
			return Result{}, err
		}
		// The id alone, not looked up: a resume past the data steps does
		// not read the backup, and one that does is refused at the step
		// that reads it, as a fresh restore would be.
		ref = ports.BackupRef{ID: opts.BackupID}
	} else if ref, err = d.resolveBackupRef(ctx, opts.BackupID); err != nil {
		return Result{}, err
	}

	opID := d.newOpID()
	op := engine.Operation{
		ID:          opID,
		Type:        domain.OpTypeRestore,
		Description: "restore " + inst.Product + " from " + ref.ID,
		To:          rel.Version(),
		Flags:       restoreFlags(ref.ID, opts),
		Steps:       restoreSteps(d, inst, rel, ref, opts),
	}

	var result engine.Result
	runErr := d.withLock(ctx, opID, domain.OpTypeRestore, opts.Options, func(ctx context.Context) error {
		var err error
		if opts.Resume {
			if prior, err = d.refreshResumable(ctx, domain.OpTypeRestore, prior); err != nil {
				return err
			}
		}
		if err := d.refuseMovedRelease(ctx, current); err != nil {
			return err
		}
		if prior != nil {
			if err := d.refuseStaleRestore(ctx, *prior, current, inst.ID); err != nil {
				return err
			}
		}
		// The steps again from the record as it stands under the lock: a
		// `stop` that landed while this waited decides whether the
		// restore brings the product back up.
		now, err := d.State.LoadInstallation(ctx)
		if err != nil {
			return err
		}
		inst.Stopped = now.Stopped
		op.Steps = restoreSteps(d, inst, rel, ref, opts)
		result, err = d.Engine.Run(ctx, op, d.engineOptions(opts.Options, inst.ID, prior))
		return err
	})

	out := Result{Record: result.Record}
	d.notifyFinished(ctx, opID, domain.OpTypeRestore, result.Record, runErr)

	// A restore moves data rather than versions, so it carries no
	// from_version and does not join the chain -- but it is the operation
	// that most changes what a deployment *is*, and an audit that could not
	// see one would be missing the event most worth explaining.
	//
	// The backup it came from is in the record's flags, which is where an
	// incident review looks for it.
	if !opts.DryRun {
		emitAttestation(ctx, d, result.Record,
			attestationInputs(inst, rel, current, domain.Version{},
				renderedConfigFor(result.State)))
	}

	if runErr != nil {
		return out, restoreLeftBehind(result.Record, runErr, inst.ID, inst.Stopped != nil)
	}
	out.Summary = fmt.Sprintf("restored %s from backup %s", inst.Product, ref.ID)
	if inst.Stopped != nil {
		out.Summary += "; it stays stopped, and `morzer start` brings it back"
	}
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
// It is a hint rather than an automatic recovery on purpose: the roads forward
// are the operator's to choose. A resume continues from the step that was cut
// off, unless that step is the release's restore hook, which nothing can tell
// how far it got (RFC 0051 decision 10). `apply` starts the release over what
// is there and abandons the restore, which no resume continues after it.
func restoreLeftBehind(rec domain.OperationRecord, err error, installationID string, stopped bool) error {
	if rec.Status != domain.StatusInterrupted {
		return err
	}
	// `start` rather than `apply` when an operator had it stopped: `apply`
	// is refused then, and `start` is what clears the stop.
	command := "`morzer apply`"
	if stopped {
		command = "`morzer start`"
	}
	recovery := fmt.Sprintf("Finish it with `morzer restore --resume --force --confirm %s`. "+
		"%s starts the current release instead and abandons the restore, which cannot "+
		"be resumed after it.", installationID, command)
	// "Stopped at", not "inside": a ctrl-C between two steps journals the
	// next one interrupted before it starts, and the resume refuses it all
	// the same.
	if idx, ok := rec.FirstIncompleteStep(); ok && !rec.Steps[idx].StartsAgain() {
		recovery = fmt.Sprintf("It stopped at %q, which is not safe to start again, so "+
			"`morzer restore --resume` would be refused. Restore again from the start with %s, "+
			"or run %s to start the current release over what is there.",
			rec.Steps[idx].ID, freshRestoreCommand(rec, installationID), command)
	}
	if servicesLeftStopped(rec) {
		recovery = "the services were stopped before the restore began, and an interrupted " +
			"operation is not brought back up automatically. " + recovery
	}
	recovery += " `morzer status` shows where it stopped."

	// Appended rather than assigned: WithHint replaces, and whatever the
	// interrupted step said about itself is the other half of what the
	// operator needs.
	if existing := domain.AsError(err).Hint; existing != "" {
		return domain.AsError(err).WithHint("%s %s", existing, recovery)
	}
	return domain.AsError(err).WithHint("%s", recovery)
}

// freshRestoreCommand is the command that restores again from the start what a
// restore record restored.
func freshRestoreCommand(rec domain.OperationRecord, installationID string) string {
	command := "morzer restore --backup " + rec.Flags[flagBackupID]
	if components := rec.Flags[flagComponents]; components != "" {
		command += " --component " + components
	}
	if rec.Flags[flagCrossInstallation] == "true" {
		command += " --allow-cross-installation"
	}
	return "`" + command + " --force --confirm " + installationID + "`"
}

// The flags a restore journals: what it restored, and what a resume reads to
// restore the same (RFC 0051 decision 8). `--identity` is not among them: a
// key's path is the operator's to supply each time.
const (
	flagBackupID          = "backup_id"
	flagComponents        = "components"
	flagCrossInstallation = "allow_cross_installation"
)

// restoreFlags journals what a resume needs to restore the same thing again:
// the backup, the components and the cross-installation flag (RFC 0051
// decision 8).
func restoreFlags(backupID string, opts RestoreOptions) map[string]string {
	flags := map[string]string{flagBackupID: backupID, "force": "true"}
	if len(opts.Components) > 0 {
		names := make([]string, len(opts.Components))
		for i, c := range opts.Components {
			names[i] = string(c)
		}
		flags[flagComponents] = strings.Join(names, ",")
	}
	if opts.AllowCrossInstallation {
		flags[flagCrossInstallation] = "true"
	}
	return flags
}

// refuseRestoreScopeOnResume refuses the flags that would name a different
// restore than the one being resumed (RFC 0051 decision 8).
func refuseRestoreScopeOnResume(opts RestoreOptions) error {
	var named []string
	if opts.BackupID != "" {
		named = append(named, "--backup")
	}
	if len(opts.Components) > 0 {
		named = append(named, "--component")
	}
	if opts.AllowCrossInstallation {
		named = append(named, "--allow-cross-installation")
	}
	if len(named) == 0 {
		return nil
	}
	return domain.Usage("%s cannot be given with --resume", strings.Join(named, " and ")).
		WithHint("a resume restores the backup, and the components, the restore was started with")
}

// resumedRestore reads what the restore being resumed restored from its
// record.
func resumedRestore(opts RestoreOptions, prior domain.OperationRecord) (RestoreOptions, error) {
	opts.BackupID = prior.Flags[flagBackupID]
	if opts.BackupID == "" {
		return opts, domain.Usage("restore %s does not record which backup it restored", prior.ID).
			WithHint("restore again from the start with `morzer restore --backup <id>`")
	}
	if components := prior.Flags[flagComponents]; components != "" {
		for name := range strings.SplitSeq(components, ",") {
			opts.Components = append(opts.Components, ports.Component(name))
		}
	}
	opts.AllowCrossInstallation = prior.Flags[flagCrossInstallation] == "true"
	return opts, nil
}

// refuseStaleRestore refuses to resume a restore something has since built on
// (RFC 0051 decision 9). An `apply` may have written to volumes the restore had
// already put back, and the hook run again over them would leave the volumes
// and the database from different moments. An `update` or a `rollback` would
// run another release's hook over data checked for this one, and a `stop` or a
// `start` changes the steps. A backup changes nothing a restore wrote.
func (d *Deps) refuseStaleRestore(
	ctx context.Context, prior domain.OperationRecord, current domain.ReleaseRecord, installationID string,
) error {
	fresh := "restore again from the start with " + freshRestoreCommand(prior, installationID)
	all, err := d.State.Operations(ctx, ports.Filter{})
	if err != nil {
		return err
	}
	// Newest first, so everything before the restore's own record ran
	// after it began.
	for _, rec := range all {
		if rec.ID == prior.ID {
			break
		}
		if rec.Type != domain.OpTypeBackup {
			return domain.Usage("restore %s cannot be resumed: %s %s ran after it began",
				prior.ID, rec.Type, rec.ID).WithHint("%s", fresh)
		}
	}
	if !current.Version.Equal(prior.To) {
		return domain.Usage("restore %s cannot be resumed: it restored onto %s, and %s is installed",
			prior.ID, prior.To, describeRelease(current)).WithHint("%s", fresh)
	}
	return nil
}

// servicesLeftStopped reports whether the stop succeeded and nothing has since
// tried to bring the product back.
//
// "Has tried", not "has succeeded": an interruption during the re-apply may
// have started some or all of the services already, and claiming they are
// stopped would send the operator to run `apply` against a deployment that is
// half up -- which is fine, apply is idempotent, but the sentence would be
// false and the next one they read might not be. The hint is offered only when
// the re-apply was never entered, which is the case it was written for:
// ctrl-C in the window between the stop and the restore hook.
func servicesLeftStopped(rec domain.OperationRecord) bool {
	stopped := false
	for _, step := range rec.Steps {
		switch step.ID {
		case "stop-services":
			stopped = step.Status == domain.StepSucceeded
		case "reapply-release":
			if step.Status != domain.StepPending {
				return false
			}
		}
	}
	return stopped
}

func restoreSteps(d *Deps, inst domain.Installation, rel domain.Release, ref ports.BackupRef, opts RestoreOptions) []engine.Step {
	steps := []engine.Step{
		stepVerifyBackupBeforeRestore(d, ref),
		stepCheckRestoreCompatibility(d, rel, ref),
		stepStopServices(d, inst, rel),
		stepRestoreVolumes(d, inst, ref, opts),
		stepRunRestore(d, inst, rel, ref, opts),
	}
	// Restoring during a maintenance window is the natural use, and the
	// product stays down until `start` applies over the restored data
	// (RFC 0040 decision 3).
	if inst.Stopped != nil {
		return steps
	}
	return append(steps, stepReapply(d, inst, rel), stepRestoreSmokeTest(d, inst, rel))
}

// stepVerifyBackupBeforeRestore checks the backup before anything is stopped.
//
// Discovering a corrupt backup after the product has been taken down is the
// worst possible ordering: the system would be off, the data unrestored, and
// the operator with no path forward.
//
// Restartable rather than idempotent: done once, it need not be done again,
// and a resume after the data went back must not fail because the backup has
// since gone (RFC 0051 decision 6).
func stepVerifyBackupBeforeRestore(d *Deps, ref ports.BackupRef) engine.Step {
	return engine.Step{
		ID:          "verify-backup",
		Description: "verify backup integrity",
		Restartable: true,
		OnFailure:   engine.Abort,
		Timeout:     30 * time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			return d.Backup.Verify(ctx, ref)
		},
	}
}

// stepCheckRestoreCompatibility refuses a backup the installed release cannot
// read. Restartable, as verifying the backup is.
func stepCheckRestoreCompatibility(d *Deps, rel domain.Release, ref ports.BackupRef) engine.Step {
	return engine.Step{
		ID:          "check-compatibility",
		Description: "check release compatibility",
		Restartable: true,
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
			if err != nil || inst.Stopped != nil {
				return nil
			}
			// Bring the product back up: an aborted restore should
			// leave it serving, not stopped -- unless an operator
			// stopped it, when serving is what they asked it not to do.
			return d.Runtime.Up(ctx, cfg, ports.UpOptions{Wait: true, WaitTimeout: 5 * time.Minute})
		},
	}
}

// stepRestoreVolumes puts back the volumes, which the manager writes itself.
//
// Restartable: each volume is wiped and extracted again, so a run started again
// replaces whatever an interrupted one half-wrote, and once the step finished a
// resume does not write them twice (RFC 0051 decision 6). A failure needs a
// person all the same, as the hook's does: a volume half-written is in the
// state a database half-written is.
func stepRestoreVolumes(d *Deps, inst domain.Installation, ref ports.BackupRef, opts RestoreOptions) engine.Step {
	return engine.Step{
		ID:                            "restore-volumes",
		Description:                   "restore volumes",
		Restartable:                   true,
		OnFailure:                     engine.Compensate,
		RequiresInterventionOnFailure: true,
		Timeout:                       3 * time.Hour,
		Execute: func(ctx context.Context, st *engine.State) error {
			return d.Backup.Restore(ctx, ref, restorePart(inst, opts, ports.RestoreVolumes))
		},
	}
}

// stepRunRestore executes the release's restore hook, over the volumes the step
// before put back.
//
// Deliberately not compensable. Once the hook has begun overwriting a
// database, no automatic action can put back what was there, and pretending
// otherwise would be the "guess a repair" failure mode the design forbids. A
// failure here escalates to requires-manual-intervention.
//
// Restartable only when the release declares it of its hook (RFC 0051 decision
// 1): the vendor knows whether the hook replaces what it restores, and the
// manager cannot tell. The journal records the answer, so a resume reads the
// promise the run that was cut off made (decision 5).
func stepRunRestore(
	d *Deps, inst domain.Installation, rel domain.Release, ref ports.BackupRef, opts RestoreOptions,
) engine.Step {
	hook, _ := rel.Manifest.Operation(domain.OpRestore)
	return engine.Step{
		ID:          "restore-data",
		Description: "restore database and files",
		Idempotent:  false,
		Restartable: hook.IsRestartable(),
		OnFailure:   engine.Compensate,
		// Once the hook has begun overwriting a database, no automatic
		// action can put back what was there.
		RequiresInterventionOnFailure: true,
		Timeout:                       3 * time.Hour,
		Execute: func(ctx context.Context, st *engine.State) error {
			return d.Backup.Restore(ctx, ref, restorePart(inst, opts, ports.RestoreHook))
		},
	}
}

// restorePart is what one restore step asks the backup engine for.
func restorePart(inst domain.Installation, opts RestoreOptions, part ports.RestorePart) ports.RestoreOptions {
	return ports.RestoreOptions{
		Components: opts.Components,
		// Not opts.Force. Force authorises destroying this machine's
		// data and is required for every restore; using it here too
		// meant the cross-installation guard was checked only after the
		// one thing that disabled it.
		Force:                opts.AllowCrossInstallation,
		TargetInstallationID: inst.ID,
		IdentityFile:         opts.IdentityFile,
		Part:                 part,
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
