package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/lifecycle/engine"
	"github.com/morzecrew/morzer/internal/release"
)

// RollbackOptions configures a rollback.
type RollbackOptions struct {
	Options
}

// RollbackReport is what `rollback` answers before it acts, and what it
// returns to the caller either way.
//
// The three questions are reported separately because they fail independently
// and an operator needs to know which one blocked them. Collapsing them into
// one boolean would hide the difference between "your migrations are
// irreversible" and "your schema has moved on", which have different remedies.
type RollbackReport struct {
	From domain.Version `json:"from"`
	To   domain.Version `json:"to"`

	Assessment domain.RollbackAssessment `json:"assessment"`

	// SchemaVersion is the database schema the assessment was made against,
	// or 0 when no release reported one.
	SchemaVersion int `json:"schema_version"`

	// SuggestedBackup is the most recent backup, named in a refusal so the
	// operator does not have to go looking for the alternative.
	SuggestedBackup string `json:"suggested_backup,omitempty"`
}

// Rollback returns to the previous release.
//
// It is not "update in reverse": it assesses first and refuses when the answers
// do not permit a safe return. A rollback that leaves an old binary reading a
// new schema corrupts data quietly, and the operator's real option -- a restore
// from the pre-update backup -- is one command away and named in the refusal.
func Rollback(ctx context.Context, d *Deps, opts RollbackOptions) (Result, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return Result{}, err
	}

	current, previous, err := d.rollbackEndpoints(ctx)
	if err != nil {
		return Result{}, err
	}

	currentRel, err := release.Load(current.Root)
	if err != nil {
		return Result{}, err
	}
	previousRel, err := release.Load(previous.Root)
	if err != nil {
		return Result{}, domain.InstallationError(err,
			"the previous release at %s cannot be read", previous.Root).
			WithHint("it may have been pruned; restore from a backup instead")
	}

	report := RollbackReport{
		From:          current.Version,
		To:            previous.Version,
		SchemaVersion: current.SchemaAtInstall,
		Assessment: domain.AssessRollback(
			currentRel.Manifest.Compatibility,
			previousRel.Manifest.Compatibility,
			current.SchemaAtInstall,
		),
	}
	report.SuggestedBackup = d.latestBackupID(ctx)

	// The assessment runs before the engine rather than as a step.
	//
	// A refused rollback is not an operation that failed -- it is one that
	// never started, and journaling it as a failure would put a record of
	// work that did not happen next to records of work that did. Checking
	// here also means the assessment is available to --dry-run, which runs
	// no Execute at all.
	d.reportRollback(report)

	if err := rollbackRefusal(report); err != nil {
		return Result{Data: report}, err
	}

	opID := d.newOpID()
	op := engine.Operation{
		ID:          opID,
		Type:        domain.OpTypeRollback,
		Description: describe(domain.OpTypeRollback, current.Version, previous.Version),
		From:        current.Version,
		To:          previous.Version,
		Steps:       rollbackSteps(d, inst, current, previousRel, opts),
		Flags:       map[string]string{"to": previous.Version.String()},
	}

	var result engine.Result
	runErr := d.withLock(ctx, opID, domain.OpTypeRollback, opts.Options, func(ctx context.Context) error {
		var err error
		result, err = d.Engine.Run(ctx, op, d.engineOptions(opts.Options, inst.ID, nil))
		return err
	})

	out := Result{Record: result.Record, Data: report}
	if result.Record.Status == domain.StatusSucceeded {
		out.Summary = fmt.Sprintf("rolled %s back from %s to %s",
			previousRel.Name(), current.Version, previous.Version)
	}
	if runErr != nil {
		return out, runErr
	}

	d.notify(ctx, events.OperationFinished(opID, domain.OpTypeRollback,
		result.Record.Status, result.Record.Duration(), nil))
	return out, nil
}

// rollbackEndpoints resolves what is running and what to return to.
func (d *Deps) rollbackEndpoints(ctx context.Context) (current, previous domain.ReleaseRecord, err error) {
	if current, err = d.State.CurrentRelease(ctx); err != nil {
		return current, previous, err
	}
	if current.IsZero() {
		return current, previous, domain.InstallationError(domain.ErrReleaseNotFound,
			"no release is installed, so there is nothing to roll back").
			WithHint("run `morzer update <bundle>` to install one")
	}

	if previous, err = d.State.PreviousRelease(ctx); err != nil {
		return current, previous, err
	}
	if previous.IsZero() {
		return current, previous, domain.InstallationError(domain.ErrReleaseNotFound,
			"no previous release to roll back to").
			WithHint("only one release has ever been installed; " +
				"to undo its effects, restore from a backup")
	}
	if previous.Version.Equal(current.Version) {
		return current, previous, domain.InstallationError(nil,
			"the previous release is the same version as the current one (%s)", current.Version).
			WithHint("there is nothing to roll back to; restore from a backup instead")
	}

	return current, previous, nil
}

// latestBackupID names the most recent backup, or empty when there is none or
// no backup engine is wired. Best effort: a refusal message is more useful with
// it and still correct without.
func (d *Deps) latestBackupID(ctx context.Context) string {
	if d.Backup == nil {
		return ""
	}
	backups, err := d.Backup.List(ctx)
	if err != nil || len(backups) == 0 {
		return ""
	}
	return backups[0].ID
}

// reportRollback tells the operator all three answers, whether or not the
// rollback proceeds. An operator who is about to be refused should see the same
// assessment as one who is not.
func (d *Deps) reportRollback(r RollbackReport) {
	if d.Bus == nil {
		return
	}
	a := r.Assessment

	d.Bus.Publish(events.Message(events.LevelInfo,
		"rollback %s → %s: containers reversible: %s, schema compatible: %s, restore required: %s",
		r.From, r.To, yesNo(a.ContainersReversible), yesNo(a.SchemaCompatible), yesNo(a.RestoreRequired)))

	if r.SchemaVersion == 0 {
		d.Bus.Publish(events.Message(events.LevelWarn,
			"database schema version is unknown, so schema compatibility was not checked"))
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// rollbackRefusal turns a blocking assessment into the error that stops the
// operation.
//
// It refuses rather than warning, and `--force` is deliberately not consulted:
// force authorises destructive actions, not incorrect ones. A warning that can
// be scrolled past is not a safety mechanism when the failure mode is quiet
// data corruption.
func rollbackRefusal(r RollbackReport) error {
	a := r.Assessment
	if !a.RestoreRequired {
		return nil
	}

	restore := "restore from a backup: `morzer restore --force --confirm <installation-id>`"
	if r.SuggestedBackup != "" {
		restore = fmt.Sprintf(
			"restore from a backup instead, most recently %s: "+
				"`morzer restore --backup %s --force --confirm <installation-id>`",
			r.SuggestedBackup, r.SuggestedBackup)
	}

	var blockers []string
	if !a.ContainersReversible {
		blockers = append(blockers, "the installed release declares its migrations irreversible")
	}
	if !a.SchemaCompatible {
		blockers = append(blockers, fmt.Sprintf(
			"the database schema is at %d, past what %s can read", r.SchemaVersion, r.To))
	}

	return domain.IncompatibleError(domain.ErrIrreversible,
		"cannot roll back %s to %s: %s", r.From, r.To, strings.Join(blockers, "; ")).
		WithHint("%s", restore)
}

// rollbackSteps moves the pointer, then converges to the previous release using
// apply's pipeline.
func rollbackSteps(
	d *Deps,
	inst domain.Installation,
	current domain.ReleaseRecord,
	previousRel domain.Release,
	opts RollbackOptions,
) []engine.Step {
	steps := []engine.Step{
		stepPointToPrevious(d, current, previousRel),
	}
	return append(steps, applySteps(d, inst, previousRel, opts.Options)...)
}

// stepPointToPrevious swaps the release pointer and the current symlink.
//
// It runs before convergence rather than after so that the compensation, which
// runs newest-first and therefore last, has something to undo: a failed
// rollback must leave the pointer where the operation found it. The final
// apply step writes the same pointer again, which is idempotent.
func stepPointToPrevious(d *Deps, current domain.ReleaseRecord, previousRel domain.Release) engine.Step {
	return engine.Step{
		ID:          "point-to-previous",
		Description: "point at " + previousRel.Version().String(),
		Idempotent:  true,
		OnFailure:   engine.Compensate,
		Timeout:     time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			record := domain.ReleaseRecord{
				SchemaVersion: domain.InstallationSchemaVersion,
				Name:          previousRel.Name(),
				Version:       previousRel.Version(),
				Digest:        previousRel.Digest,
				Root:          previousRel.Root,
				InstalledAt:   domain.NewTime(d.now()),
				OperationID:   st.OpID,
				// Carried forward rather than reset. The migrate hook
				// overwrites this at the end of the pipeline with what
				// the database actually reports; preserving it here
				// means a failure between the two steps still leaves a
				// record that describes the database rather than one
				// implying the rollback migrated it back.
				SchemaAtInstall: current.SchemaAtInstall,
			}
			if err := d.State.SetCurrentRelease(ctx, record); err != nil {
				return err
			}
			return atomicfs.ReplaceSymlink(previousRel.Root, d.Paths.CurrentLink())
		},
		Compensate: func(ctx context.Context, st *engine.State) error {
			if err := d.State.SetCurrentRelease(ctx, current); err != nil {
				return err
			}
			return atomicfs.ReplaceSymlink(current.Root, d.Paths.CurrentLink())
		},
	}
}
