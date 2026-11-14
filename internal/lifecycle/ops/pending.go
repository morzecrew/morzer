package ops

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
)

// Values a document gives the release the channel staged (RFC 0058): a sync
// keeps them pending, and the next update takes the ones its release declares
// as if they were given with --set.

// splitStaged moves the parameters a sync sorted to the staged release out of
// the change, into the part that waits for that release.
func splitStaged(change ConfigChange, staged *stagedNames) (now, later ConfigChange) {
	if staged == nil || len(staged.parameters) == 0 {
		return change, ConfigChange{}
	}
	now = change
	now.Parameters, later.Parameters = map[string]string{}, map[string]string{}
	for name, value := range change.Parameters {
		if staged.has("parameters", name) {
			later.Parameters[name] = value
		} else {
			now.Parameters[name] = value
		}
	}
	now.Unset.Parameters = nil
	for _, name := range change.Unset.Parameters {
		if staged.has("parameters", name) {
			later.Unset.Parameters = append(later.Unset.Parameters, name)
		} else {
			now.Unset.Parameters = append(now.Unset.Parameters, name)
		}
	}
	return now, later
}

// planPending is the pending section a sync leaves (RFC 0058 §5.2), merged as
// RFC 0057 merges: a name the document sets is set, one it unsets is removed,
// and the rest stay. A value is checked against the staged release as `--set`
// is against the release being installed, and kept parsed, as config set keeps
// a recorded one. It returns the names whose value it sets or removes.
//
// A name in now, which the document applies to the installed release, is
// removed too: it is the authority's later word for that name, and an update
// that stopped after installing the release left its older value here.
func planPending(
	inst domain.Installation, staged *stagedNames, later ConfigChange, now []string,
) (*domain.PendingValues, []string, []string, []error) {
	values := maps.Clone(inst.PendingParameters())
	if values == nil {
		values = map[string]string{}
	}
	var (
		release    domain.Version
		problems   []error
		set, unset []string
	)
	if inst.Pending != nil {
		release = inst.Pending.Release
	}
	for _, name := range sortedUnion(later.Unset.Parameters, now) {
		if _, ok := values[name]; ok {
			delete(values, name)
			unset = append(unset, name)
		}
	}
	if len(later.Parameters) > 0 {
		release = staged.rel.Version()
	}
	for _, name := range sortedStrings(slices.Collect(maps.Keys(later.Parameters))) {
		spec, declared := staged.rel.Manifest.Parameters[name]
		if !declared {
			problems = append(problems, undeclaredIn(name, staged.rel))
			continue
		}
		value, err := spec.Parse(later.Parameters[name])
		if err != nil {
			problems = append(problems, domain.RefusedParameterValue(err, name, spec))
			continue
		}
		if was, ok := values[name]; !ok || was != value {
			set = append(set, name)
		}
		values[name] = value
	}
	// The section names one release as what its values were checked
	// against, so the values kept from an earlier one are checked again
	// before it names a new one. A name the new release does not declare
	// stays, and the update to it drops it, naming it (decision 7).
	if inst.Pending != nil && !release.Equal(inst.Pending.Release) {
		for _, name := range sortedStrings(slices.Collect(maps.Keys(values))) {
			spec, declared := staged.rel.Manifest.Parameters[name]
			if _, given := later.Parameters[name]; given || !declared {
				continue
			}
			if _, err := spec.Parse(values[name]); err != nil {
				problems = append(problems, domain.RefusedParameterValue(err, name, spec))
			}
		}
	}
	if len(values) == 0 {
		return nil, set, unset, problems
	}
	return &domain.PendingValues{Release: release, Parameters: values}, set, unset, problems
}

// clearsPending reports whether an update's record step clears what a document
// left pending: whenever some is, unless the run a resume continues recorded
// already, since what is pending then is for a later update.
func clearsPending(inst domain.Installation, opts UpdateOptions) bool {
	return inst.Pending != nil && !opts.pendingDone
}

// recordLanded reports whether the record step of the run a resume continues
// wrote what it took from pending though the journal never heard the step
// finish. The write that recorded them cleared pending, so whatever is pending
// since is a later document's.
//
// Three things together, since a recorded value can equal a pending one before
// any write: the step began, every such name is recorded with a digest that run
// journalled, and none of them is pending any more.
func recordLanded(inst domain.Installation, prior domain.OperationRecord) bool {
	began := slices.ContainsFunc(prior.Steps, func(s domain.StepRecord) bool {
		return s.ID == "record-parameters" &&
			(s.Status == domain.StepRunning || s.Status == domain.StepFailed || s.Status == domain.StepInterrupted)
	})
	names := slices.DeleteFunc(strings.Split(prior.Flags["pending"], ","), func(n string) bool { return n == "" })
	journalled := strings.Split(prior.Flags["set_digest"], ",")
	for _, name := range names {
		value, ok := inst.Parameters[name]
		_, stillPending := inst.PendingParameters()[name]
		if !ok || stillPending || !slices.Contains(journalled, parameterDigest(name, value)) {
			return false
		}
	}
	return began && len(names) > 0
}

// undeclaredPending is the pending names rel does not declare, which an update
// to it drops (RFC 0058 decision 7).
func undeclaredPending(inst domain.Installation, rel domain.Release) []string {
	var out []string
	for _, name := range slices.Sorted(maps.Keys(inst.PendingParameters())) {
		if _, declared := rel.Manifest.Parameters[name]; !declared {
			out = append(out, name)
		}
	}
	return out
}

// refuseSetOfManaged is RFC 0057 decision 29 for `update --set` (RFC 0058
// decision 6): while sync is enabled, a value given for a name the release
// being installed lets the authority manage, or for one a document left
// pending, is refused unless --force, naming the authority's key.
func refuseSetOfManaged(inst domain.Installation, rel domain.Release, given map[string]string, force bool) error {
	if force || !inst.Sync.Enabled {
		return nil
	}
	var managed []string
	for _, name := range slices.Sorted(maps.Keys(given)) {
		_, pending := inst.PendingParameters()[name]
		if pending || rel.Manifest.Managed.Allows("parameters", name) {
			managed = append(managed, name)
		}
	}
	if len(managed) == 0 {
		return nil
	}
	return domain.Usage("%s managed by the authority this installation follows (key %s)",
		strings.Join(managed, ", "), domain.MinisignKeyID(inst.Sync.Authority)).
		WithHint("give it in what the authority publishes; --force sets it here, " +
			"until the authority's next document sets it back")
}

// refuseChangedPending is refuseChangedInstallation for what a document left
// pending for the release an update installs: a sync that wrote it while the
// update waited for the lock changed what the update takes as given, or what
// its record step drops and its journal names. Every name, declared or not.
func (d *Deps) refuseChangedPending(ctx context.Context, planned domain.Installation) error {
	now, err := d.State.LoadInstallation(ctx)
	if err != nil {
		return err
	}
	before, after := planned.PendingParameters(), now.PendingParameters()
	var changed []string
	for _, name := range sortedUnion(slices.Collect(maps.Keys(before)), slices.Collect(maps.Keys(after))) {
		was, had := before[name]
		is, has := after[name]
		if had != has || was != is {
			changed = append(changed, name)
		}
	}
	if len(changed) == 0 {
		return nil
	}
	slices.Sort(changed)
	return domain.Usage("the values pending for %s changed while waiting for the deployment lock",
		strings.Join(changed, ", ")).
		WithHint("run the same command again to take the values pending now")
}

// fromTheAuthority reports whether a value for name reaches rel from the
// authority this installation follows rather than from `--set`, which needs
// --force for it while sync is enabled (RFC 0058 decision 6).
func fromTheAuthority(inst domain.Installation, rel domain.Release, name string) bool {
	return inst.Sync.Enabled && rel.Manifest.Managed.Allows("parameters", name)
}

// unreadableRelease is a release on this machine that will not read, as this
// machine's error rather than the document's. Not chained: a validation error
// inside would class a sync's cycle `invalid` and refuse the serial for good,
// though the next stage or repair fixes it.
func unreadableRelease(which string, version domain.Version, err error) error {
	return domain.InstallationError(nil, "the %s release %s cannot be read: %s", which, version, err.Error())
}
