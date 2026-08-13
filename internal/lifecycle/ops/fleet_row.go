package ops

import (
	"context"
	"strconv"

	"github.com/morzecrew/morzer/internal/domain"
)

// Deriving the row (RFC 0026 decision 8: no new state).
//
// Every field below comes from a computation that already existed for another
// command. That is the decision, and it is worth saying why it is a decision
// rather than a nicety: a fleet view whose payload needed the manager to record
// something extra would be a second source of truth about installation state,
// and a second source of truth disagrees with the first exactly when it
// matters. If publishing ever needs a new file on disk, the payload has grown
// into a record and this design is the wrong one.
//
// The other half of decision 8 is what §10.2 measured: `ls` answers without a
// Docker call. That is what lets a publisher report the case where the runtime
// itself is what is broken -- which is the case a fleet view exists for.

// fleetRow assembles this installation's row.
//
// Nothing here returns an error. Every failure is a field on the row saying so,
// because RFC 0026 decision 4 is that a row carries its problems: a publisher
// that refused to publish because the daemon was down would go silent exactly
// when the machine had something to say.
func (d *Deps) fleetRow(ctx context.Context, inst domain.Installation) domain.FleetRow {
	in := domain.FleetRowInputs{
		Installation:   inst,
		ManagerVersion: d.ManagerVersion.String(),
		PublishedAt:    domain.NewTime(d.now()),
	}

	// The key that will actually sign, so the row names the key that signed
	// it rather than the one state remembers.
	if key := d.signingKeyForDocument(ctx, inst, "publishing"); key != "" {
		in.Installation.Signing.PublicKey = key
	}

	current, currentErr := d.State.CurrentRelease(ctx)
	if currentErr == nil && !current.IsZero() {
		in.Version = current.Version.String()
	}

	in.Health = d.fleetHealth(ctx, inst, current, currentErr)
	in.Drift = d.fleetDrift(ctx, inst, current, currentErr)
	in.LastOperation = d.fleetLastOperation(ctx)

	return domain.NewFleetRow(in)
}

// fleetHealth asks the runtime what is running, through the same function `ls
// --status` uses.
//
// The same function and not a copy of it, which is the whole point: two
// computations of "how many services are up" would agree the day they were
// written and disagree on the day a fleet screen and `morzer ls` were both open
// on the same machine. It also brings the per-row timeout with it, so one
// wedged daemon costs this row its counts and nothing else.
func (d *Deps) fleetHealth(
	ctx context.Context,
	inst domain.Installation,
	current domain.ReleaseRecord,
	currentErr error,
) domain.FleetHealth {
	out := domain.FleetHealth{Attention: d.fleetAttention(ctx)}

	if currentErr != nil {
		out.Problem = "cannot read the current release: " + domain.AsError(currentErr).Message
		return out
	}

	var entry InstallationEntry
	d.fillServiceCounts(ctx, &entry, inst, current, DefaultStatusTimeout)

	out.Problem = entry.ServicesProblem
	if entry.Services != nil {
		total, running := entry.Services.Total, entry.Services.Running
		out.Services, out.Running = &total, &running
	}
	return out
}

// fleetAttention counts the operations flagged requires-manual-intervention.
//
// Read from state files, so it is answerable on a machine whose runtime is
// down -- which is the machine most likely to have one. A read that fails
// contributes zero rather than a problem of its own: the count sits beside a
// Problem field that the runtime half already fills, and a second explanation
// competing for that one line would bury the first.
func (d *Deps) fleetAttention(ctx context.Context) int {
	unfinished, err := d.State.UnfinishedOperations(ctx)
	if err != nil {
		return 0
	}

	var n int
	for _, rec := range unfinished {
		if rec.Status.NeedsAttention() {
			n++
		}
	}
	return n
}

// fleetDrift counts the configuration targets that differ from what the release
// renders.
//
// A count, never the diff -- the number is the signal, and the files are on the
// machine for somebody who is allowed to look. `configComparison` is the same
// computation the support bundle's config-diff.txt is built from, so an
// operator holding both cannot be shown two different answers.
//
// Targets that could not be *read* are excluded from the count and named in the
// problem instead. An unreadable `/etc` is a permission fault, and counting it
// as drift would publish "3 targets differ" for a machine where nothing had
// changed at all.
func (d *Deps) fleetDrift(
	ctx context.Context,
	inst domain.Installation,
	current domain.ReleaseRecord,
	currentErr error,
) domain.FleetDrift {
	switch {
	case currentErr != nil:
		return domain.FleetDrift{Problem: "cannot read the current release"}
	case current.IsZero():
		return domain.FleetDrift{Problem: "no release is installed, so nothing renders configuration"}
	}

	rel, err := d.resolveCurrentRelease(ctx, current)
	if err != nil {
		return domain.FleetDrift{Problem: domain.AsError(err).Message}
	}

	comparison, err := configComparison(ctx, d, inst, rel)
	if err != nil {
		return domain.FleetDrift{Problem: domain.AsError(err).Message}
	}

	count := len(comparison.Diffs)
	out := domain.FleetDrift{Targets: &count}
	if n := len(comparison.Unreadable); n > 0 {
		out.Problem = pluralTargets(n) + " could not be read, so the count is of the rest"
	}
	return out
}

// pluralTargets renders "1 configuration target" or "3 configuration targets".
func pluralTargets(n int) string {
	if n == 1 {
		return "1 configuration target"
	}
	return strconv.Itoa(n) + " configuration targets"
}

// fleetLastOperation is what this installation last did, or nil.
//
// Nil rather than a zero-valued record: an installation that has never finished
// an operation is a real state -- an `init` that has not been followed by an
// `apply` -- and a row carrying an operation with an empty id and no outcome
// would read as a corrupt journal rather than a new machine.
func (d *Deps) fleetLastOperation(ctx context.Context) *domain.FleetOperation {
	rec, ok, err := d.State.LastOperation(ctx)
	if err != nil || !ok {
		return nil
	}

	at := rec.FinishedAt
	if at.IsZero() {
		// An operation that never finished. Its start is the honest
		// timestamp, and the outcome it carries says what happened.
		at = rec.StartedAt
	}
	return &domain.FleetOperation{
		ID:      rec.ID,
		Kind:    string(rec.Type),
		Outcome: string(rec.Status),
		At:      at,
	}
}
