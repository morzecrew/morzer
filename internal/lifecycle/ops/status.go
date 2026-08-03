package ops

import (
	"context"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// Status is the answer to "what is deployed and is it working".
//
// It is a plain struct rather than an operation with steps: it mutates
// nothing, takes no lock, and must work even when the installation is broken.
// A status command that fails because the system is unhealthy is a status
// command that is useless exactly when it is needed.
type Status struct {
	Product        string `json:"product"`
	InstallationID string `json:"installation_id"`
	Profile        string `json:"profile,omitempty"`
	PublicURL      string `json:"public_url,omitempty"`

	CurrentRelease  domain.ReleaseRecord `json:"current_release"`
	PreviousRelease domain.ReleaseRecord `json:"previous_release,omitempty"`

	Services []ports.ServiceState `json:"services"`
	Health   []ports.HealthResult `json:"health,omitempty"`

	LastBackup    *BackupSummary          `json:"last_backup,omitempty"`
	LastOperation *domain.OperationRecord `json:"last_operation,omitempty"`

	// NeedsAttention lists operations flagged requires-manual-intervention.
	// They keep surfacing here until an operator clears them explicitly,
	// which is the whole point of the state existing.
	NeedsAttention []domain.OperationRecord `json:"needs_attention,omitempty"`

	// LockHeldBy is set when another operation is running.
	LockHeldBy *ports.LockOwner `json:"lock_held_by,omitempty"`

	// Problems are conditions worth an operator's notice that are not
	// themselves failures of this command.
	Problems []string `json:"problems,omitempty"`
}

type BackupSummary struct {
	ID   string      `json:"id"`
	At   domain.Time `json:"at"`
	Age  string      `json:"age"`
	Size int64       `json:"size,omitempty"`
}

// Healthy reports whether everything the manager can see is in order.
func (s Status) Healthy() bool {
	if len(s.NeedsAttention) > 0 {
		return false
	}
	for _, svc := range s.Services {
		if !svc.Running() {
			return false
		}
	}
	for _, h := range s.Health {
		if !h.OK {
			return false
		}
	}
	return true
}

// GetStatus assembles the status report.
//
// Every section degrades independently: a failure to reach Docker records a
// problem and leaves Services empty rather than aborting, so an operator whose
// daemon is down still learns which release is installed and when the last
// backup ran.
func GetStatus(ctx context.Context, d *Deps) (Status, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return Status{}, err
	}

	out := Status{
		Product:        inst.Product,
		InstallationID: inst.ID,
		Profile:        inst.Profile,
		PublicURL:      inst.PublicURL(),
	}

	if out.CurrentRelease, err = d.State.CurrentRelease(ctx); err != nil {
		out.Problems = append(out.Problems, "cannot read the current release: "+domain.AsError(err).Message)
	}
	if out.PreviousRelease, err = d.State.PreviousRelease(ctx); err != nil {
		out.Problems = append(out.Problems, "cannot read the previous release: "+domain.AsError(err).Message)
	}

	if owner, held, err := d.Locker.Owner(ctx, "deployment"); err == nil && held {
		out.LockHeldBy = &owner
	}

	if rec, ok, err := d.State.LastOperation(ctx); err == nil && ok {
		out.LastOperation = &rec
	}
	if unfinished, err := d.State.UnfinishedOperations(ctx); err == nil {
		for _, rec := range unfinished {
			if rec.Status.NeedsAttention() {
				out.NeedsAttention = append(out.NeedsAttention, rec)
			}
		}
	}

	// Service and health state need a release to know what to look at.
	if !out.CurrentRelease.IsZero() {
		rel, relErr := d.resolveCurrentRelease(ctx, out.CurrentRelease)
		if relErr != nil {
			out.Problems = append(out.Problems, domain.AsError(relErr).Message)
		} else {
			d.fillRuntimeStatus(ctx, &out, inst, rel)
		}
	}

	if d.Backup != nil {
		d.fillBackupStatus(ctx, &out, inst)
	}

	return out, nil
}

func (d *Deps) fillRuntimeStatus(ctx context.Context, out *Status, inst domain.Installation, rel domain.Release) {
	cfg, err := d.runtimeConfig(rel, inst, "")
	if err != nil {
		out.Problems = append(out.Problems, domain.AsError(err).Message)
		return
	}

	services, err := d.Runtime.Status(ctx, cfg)
	if err != nil {
		out.Problems = append(out.Problems,
			"cannot read service status: "+domain.AsError(err).Message)
		return
	}
	out.Services = services

	// Health probes are only meaningful once something is running.
	// Probing a stopped deployment would report a wall of connection
	// refusals that say nothing the service list has not already said.
	if len(rel.Manifest.Health.Checks) == 0 || !anyRunning(services) {
		return
	}

	// A short bound: status reports the current state, it does not wait
	// for a desired one.
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	specs := d.checkSpecs(inst, rel, "", domain.OpTypeApply)
	if results, err := d.Health.CheckOnce(probeCtx, specs); err == nil {
		out.Health = results
	}
}

func anyRunning(services []ports.ServiceState) bool {
	for _, s := range services {
		if s.Running() {
			return true
		}
	}
	return false
}

func (d *Deps) fillBackupStatus(ctx context.Context, out *Status, inst domain.Installation) {
	backups, err := d.Backup.List(ctx)
	if err != nil || len(backups) == 0 {
		return
	}

	latest := backups[0]
	age := d.now().Sub(latest.At.Time)
	out.LastBackup = &BackupSummary{
		ID:   latest.ID,
		At:   latest.At,
		Age:  age.Round(time.Minute).String(),
		Size: latest.Size,
	}

	if stale := inst.Policy.StaleBackupAfter.Duration(); stale > 0 && age > stale {
		out.Problems = append(out.Problems,
			"the most recent backup is "+age.Round(time.Hour).String()+" old")
	}
}

// ClearIntervention marks a requires-manual-intervention operation as resolved.
//
// It is deliberately explicit and deliberately manual: the flag exists to stop
// automation proceeding over a state a human has not looked at, so nothing but
// a human saying so may clear it. The resolution is journaled as a new record
// rather than by editing the old one -- the journal is append-only, and
// rewriting history would lose the fact that intervention was ever needed.
func ClearIntervention(ctx context.Context, d *Deps, opID string) (Result, error) {
	unfinished, err := d.State.UnfinishedOperations(ctx)
	if err != nil {
		return Result{}, err
	}

	var target *domain.OperationRecord
	for i := range unfinished {
		if !unfinished[i].Status.NeedsAttention() {
			continue
		}
		if opID == "" || unfinished[i].ID == opID {
			target = &unfinished[i]
			break
		}
	}
	if target == nil {
		if opID != "" {
			return Result{}, domain.Usage(
				"operation %s is not flagged as requiring manual intervention", opID)
		}
		return Result{Summary: "no operations require manual intervention"}, nil
	}

	resolved := *target
	resolved.Status = domain.StatusFailed
	resolved.FinishedAt = domain.NewTime(d.now())
	if resolved.Flags == nil {
		resolved.Flags = map[string]string{}
	}
	resolved.Flags["intervention_cleared_at"] = d.now().UTC().Format(time.RFC3339)

	if err := d.State.AppendOperation(ctx, resolved); err != nil {
		return Result{}, err
	}

	return Result{
		Record:  resolved,
		Summary: "cleared the manual-intervention flag on operation " + resolved.ID,
	}, nil
}
