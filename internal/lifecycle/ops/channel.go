package ops

import (
	"context"
	"fmt"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

// FollowChannelOptions are the inputs to one poll of a channel.
type FollowChannelOptions struct {
	Options

	// Ref overrides the configured channel. Empty follows what the
	// installation records.
	Ref string

	// Explicit marks a poll an operator asked for by name, which is its own
	// authorisation -- the same rule `update --check` follows. A timer
	// leaves it false and is gated by `update.check`.
	Explicit bool
}

// FollowChannelResult is what one poll found.
type FollowChannelResult struct {
	// Ref is the channel that was read.
	Ref string `json:"ref"`

	// UpstreamDigest is what it points at now.
	UpstreamDigest string `json:"upstream_digest"`

	// Moved is true when that differs from what was last seen. False is the
	// overwhelmingly common answer and the cheap one: a peek and nothing
	// else.
	Moved bool `json:"moved"`

	// Candidate is what the poll left behind: a staged release, or a
	// recorded refusal. Zero when the channel had not moved.
	Candidate domain.UpdateCandidate `json:"candidate,omitzero"`

	// Notes are the incoming release's notes, when it ships any. Read from
	// the staged bundle, so they exist only once something was staged.
	Notes string `json:"-"`
}

// Summary renders the outcome for a human.
func (r FollowChannelResult) Summary() string {
	switch {
	case !r.Moved:
		return fmt.Sprintf("%s is unchanged; nothing to stage", r.Ref)
	case r.Candidate.IsZero():
		// Moved, with no candidate: a plan. The version is unknown
		// because finding it out is the download a plan declines.
		return fmt.Sprintf("%s has moved; a real run would fetch and stage it", r.Ref)
	case r.Candidate.IsStaged():
		return fmt.Sprintf("staged %s, not installed -- `morzer update --to %s` applies it",
			r.Candidate, r.Candidate.Version)
	default:
		return fmt.Sprintf("%s moved, and its bundle was refused: %s",
			r.Ref, r.Candidate.Refused)
	}
}

// FollowChannel reads a channel and stages what it points at.
//
// The shape of the operation is the point: **peek, then decide, then fetch**.
// A channel is watched on a timer, so the common case -- nothing has changed --
// must cost one manifest request and no bundle. RFC 0016 §5.2 originally priced
// a tick at "one Resolve"; resolving an OCI reference downloads the layer to
// compute a content digest, so a poll built that way would pull the whole
// release every tick to find out it already had it. ports.ChannelPeeker exists
// for that reason and the difference is measured, not asserted.
//
// It stages and stops. Installing is a separate decision with downtime attached,
// and moving the network, the credentials and the verification off the human's
// critical path is most of the value here while carrying nearly none of the
// risk.
func FollowChannel(ctx context.Context, d *Deps, opts FollowChannelOptions) (FollowChannelResult, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return FollowChannelResult{}, err
	}

	ref := opts.Ref
	if ref == "" {
		ref = inst.Update.Channel
	}
	if ref == "" {
		return FollowChannelResult{}, domain.ValidationError(domain.ErrNotFound,
			"this installation follows no channel").
			WithHint("set one with `morzer config set update.channel=" +
				"oci://registry.example/demo/bundle:stable`, or pass a reference")
	}

	// The same consent rule the check follows: an operator typing the
	// command is the authorisation, and an unprompted run needs the setting.
	// A poll contacts the vendor's registry, which for a self-hosted product
	// is a phone-home nobody agreed to.
	if !inst.Update.CheckAllowed(opts.Explicit) {
		return FollowChannelResult{}, domain.ValidationError(domain.ErrUnsupported,
			"update checking is not enabled on this installation").
			WithHint("following a channel contacts the vendor's registry; enable it " +
				"with `morzer config set update.check=true`")
	}

	parsed, err := ports.ParseRef(ref)
	if err != nil {
		return FollowChannelResult{}, err
	}
	peeker, ok := d.Source.(ports.ChannelPeeker)
	if !ok {
		return FollowChannelResult{}, domain.Internal(nil,
			"the configured release source cannot follow a channel")
	}

	state, err := peeker.Peek(ctx, parsed)
	if err != nil {
		return FollowChannelResult{}, err
	}

	result := FollowChannelResult{
		Ref:            ports.RedactRefCredentials(ref),
		UpstreamDigest: state.UpstreamDigest,
	}

	seen, err := d.lastSeenUpstream(ctx)
	if err != nil {
		return FollowChannelResult{}, err
	}
	if state.UpstreamDigest == seen {
		// The assertion that makes a five-minute cadence affordable:
		// an unmoved tag ends here, having transferred a manifest.
		return result, nil
	}
	result.Moved = true

	// A plan stops here. Staging fetches a bundle into the release store and
	// writes a record the next tick reads -- both are changes to the
	// machine, and a --dry-run that made them would be the one command in
	// this program that lies about what it does.
	//
	// What it can honestly report is that the channel has moved. The version
	// behind it is not knowable without the download this is declining to
	// do.
	if opts.DryRun {
		return result, nil
	}

	candidate := domain.UpdateCandidate{
		SchemaVersion:  domain.UpdateCandidateSchemaVersion,
		SourceRef:      result.Ref,
		UpstreamDigest: state.UpstreamDigest,
		SeenAt:         domain.NewTime(d.now()),
	}

	rel, stageErr := d.stageCandidate(ctx, state.Pinned)
	if stageErr != nil {
		// A refusal is recorded, not merely returned. The tag will still
		// point here on the next tick, and a poll that remembered only
		// its successes would re-download this bundle every tick forever
		// to reach the same refusal.
		candidate.Refused = domain.AsError(stageErr).Message
		if err := d.State.SetUpdateCandidate(ctx, candidate); err != nil {
			return result, err
		}
		result.Candidate = candidate
		return result, stageErr
	}

	candidate.Name = rel.Name()
	candidate.Version = rel.Version()
	candidate.Digest = rel.Digest
	candidate.Root = rel.Root
	if err := d.State.SetUpdateCandidate(ctx, candidate); err != nil {
		return result, err
	}

	result.Candidate = candidate
	result.Notes = release.Notes(rel)

	d.notify(ctx, events.UpdateStaged(rel.Name(), rel.Version().String(),
		result.Ref, release.NotesSummary(result.Notes)))
	return result, nil
}

// lastSeenUpstream is what this machine has already decided about.
//
// The candidate first and the installed release second, because the candidate is
// the more recent decision: a staged 1.4.0 and an installed 1.3.0 both came from
// this channel, and the question a poll asks is "is there anything I have not
// seen", not "is there anything newer than what is running".
//
// An empty answer means unknown -- a release installed from a path, or from
// before this was recorded -- and unknown is treated as moved. That costs one
// fetch and then records an answer, which is the right direction: the
// alternative is a machine that never notices its channel again.
func (d *Deps) lastSeenUpstream(ctx context.Context) (string, error) {
	candidate, err := d.State.UpdateCandidate(ctx)
	if err != nil {
		return "", err
	}
	if !candidate.IsZero() {
		return candidate.UpstreamDigest, nil
	}

	current, err := d.State.CurrentRelease(ctx)
	if err != nil {
		return "", err
	}
	return current.UpstreamDigest, nil
}

// adoptStagedCandidate carries the channel's facts onto the release record.
//
// This is the only place a release acquires an upstream digest, and it is here
// rather than in the update options because `update --to 1.4.0` on a staged
// candidate is an operator installing what the poll fetched -- the ref and the
// digest are already recorded, and asking them to re-supply what the machine
// knows would be the manager forgetting on purpose.
//
// A failure to read the candidate is not a failure to record the release. The
// candidate is derived state whose whole cost of loss is one fetch; the release
// pointer is what the deployment is.
func (d *Deps) adoptStagedCandidate(ctx context.Context, record *domain.ReleaseRecord) {
	candidate, err := d.State.UpdateCandidate(ctx)
	if err != nil || !candidate.IsStaged() || !candidate.Version.Equal(record.Version) {
		return
	}
	record.UpstreamDigest = candidate.UpstreamDigest
	if record.SourceRef == "" {
		record.SourceRef = candidate.SourceRef
	}
}

// retireStagedCandidate forgets a candidate that is no longer waiting.
//
// Installed is the obvious case. Superseded is the one worth stating: an
// operator who installs 1.5.0 by hand while 1.4.0 sits staged has answered the
// question the candidate was asking, and leaving it would have `status` offering
// an older release as though it were the next one.
//
// A candidate *newer* than what just became current survives -- that is a
// rollback, or an update to something else while the newer one still waits.
func (d *Deps) retireStagedCandidate(ctx context.Context, record domain.ReleaseRecord) error {
	candidate, err := d.State.UpdateCandidate(ctx)
	if err != nil || candidate.IsZero() {
		// Unreadable is not fatal here for the same reason as above:
		// this file is rebuilt by the next poll.
		return nil //nolint:nilerr // derived state; the next poll rewrites it
	}

	// A *refusal* is not retired by anything an operator installs. It
	// records which upstream digest was already judged unusable, and
	// clearing it would have the next poll re-download that bundle to reach
	// the same refusal -- which is the whole reason refusals are recorded.
	// Its Version is zero, so without this it would look like a candidate
	// older than everything and be cleared by every `apply`.
	if !candidate.IsStaged() {
		return nil
	}
	if candidate.Version.GreaterThan(record.Version) {
		return nil
	}
	return d.State.ClearUpdateCandidate(ctx)
}

// stageCandidate resolves, fetches and verifies the bundle a channel points at.
//
// The reference is the pinned one from the peek, never the tag. A channel is a
// tag that exists to move, so a fetch addressed by tag can bring down a
// different bundle from the one the decision was made about -- a window that is
// tolerable when a human typed a reference seconds ago, and not when a loop is
// watching something built to change (RFC 0016 decision 22).
func (d *Deps) stageCandidate(ctx context.Context, pinned ports.Ref) (domain.Release, error) {
	resolved, err := d.Source.Resolve(ctx, pinned)
	if err != nil {
		return domain.Release{}, err
	}

	current, err := d.State.CurrentRelease(ctx)
	if err != nil {
		return domain.Release{}, err
	}
	if !current.IsZero() && resolved.Version.LessThan(current.Version) {
		// A channel that moved backwards is a vendor pulling a release,
		// and the answer is not to quietly downgrade a running machine:
		// `update` refuses a backwards move anyway, so staging one would
		// only produce a candidate nobody can install.
		return domain.Release{}, domain.ValidationError(nil,
			"the channel points at %s, older than the installed %s",
			resolved.Version, current.Version).
			WithHint("a channel that moved backwards usually means a release was " +
				"withdrawn; `morzer rollback` is how a machine goes back")
	}
	if !current.IsZero() && current.Version.Equal(resolved.Version) {
		if atomicfs.SameDigest(current.Digest, resolved.Digest) {
			// The tag moved to a different artefact carrying the
			// same bundle -- a re-push of identical content. There
			// is nothing to stage, and saying so is more useful
			// than staging the release that is already running.
			return domain.Release{}, domain.ValidationError(nil,
				"the channel now points at %s, which is already installed",
				resolved.Version)
		}
		return domain.Release{}, domain.ValidationError(domain.ErrDigestMismatch,
			"the channel published %s again with different content", resolved.Version).
			WithHint("a version is immutable; a vendor iterating should publish a " +
				"prerelease (1.4.1-dev.7.gabc1234) rather than republishing one")
	}

	if resolved.Version.Prerelease() != "" {
		inst, err := d.loadInstallation(ctx)
		if err != nil {
			return domain.Release{}, err
		}
		if !inst.IsDev() {
			// Not a refusal of the channel, a refusal of this
			// artefact: a vendor whose stable tag briefly points at
			// a build has not reconfigured the customer's machine.
			return domain.Release{}, domain.ValidationError(nil,
				"the channel points at the prerelease %s, and this is not a sandbox",
				resolved.Version).
				WithHint("prerelease versions are admissible on an installation " +
					"created with `--mode dev`")
		}
	}

	rel, _, err := FetchIntoStore(ctx, d, pinned, resolved)
	if err != nil {
		return domain.Release{}, err
	}
	return rel, nil
}

// UnattendedOptions are the inputs to one scheduled tick.
type UnattendedOptions struct {
	Options
}

// UnattendedResult is what the tick did.
type UnattendedResult struct {
	// Follow is the channel poll that opened it. Reported whatever
	// happened next, because "the tag has not moved" is the answer on most
	// ticks and the one that says the machine is watching at all.
	Follow FollowChannelResult `json:"follow"`

	// Applied is true when a release was installed without a human.
	Applied bool `json:"applied"`

	// Assessment is why not, when a release was staged and left alone. Zero
	// when nothing was waiting.
	Assessment domain.UnattendedAssessment `json:"assessment,omitzero"`

	// Update is the operation record, when one ran.
	Update *Result `json:"update,omitempty"`
}

// Summary renders the tick for a journal a human reads at 09:00.
func (r UnattendedResult) Summary() string {
	switch {
	case r.Applied && r.Update != nil:
		return r.Update.Summary
	case r.Follow.Candidate.IsStaged():
		return fmt.Sprintf("%s is staged and was not installed: %s",
			r.Follow.Candidate.Version, r.Assessment.Why())
	default:
		return r.Follow.Summary()
	}
}

// RunUnattended is one tick of the update timer.
//
// Poll, stage, and install only what declares it cannot end needing a database
// restore. Everything else is left staged and notified rather than silently
// skipped -- which is where most of the value is: the network, the credentials
// and the verification are off the human's critical path, and the only thing
// still waiting for them is the decision that costs downtime.
//
// The promise the gate makes is narrower than it first reads, and the naming
// matters because an operator will rely on it: this does *not* promise no human
// will be needed. An unattended update can still stop at
// requires-manual-intervention through a failed migration hook, a health check
// that never passes, or a converge the engine cannot compensate. What it bounds
// is the *unrecoverable* failure -- the one that cannot wait until morning.
func RunUnattended(ctx context.Context, d *Deps, opts UnattendedOptions) (UnattendedResult, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return UnattendedResult{}, err
	}

	// Explicit is false: a timer is the unprompted path by definition, so
	// it is gated by `update.check` exactly as `doctor` and `status` are.
	follow, err := FollowChannel(ctx, d, FollowChannelOptions{Options: opts.Options})
	out := UnattendedResult{Follow: follow}
	if err != nil {
		return out, err
	}

	candidate, err := d.State.UpdateCandidate(ctx)
	if err != nil {
		return out, err
	}
	if !candidate.IsStaged() {
		return out, nil
	}

	if !inst.Update.AutoApply {
		// Not a refusal to report as a failure: staging *is* the
		// configured behaviour here, and a timer that exited non-zero
		// every night because a release is waiting would train an
		// operator to ignore the unit.
		out.Assessment = domain.UnattendedAssessment{
			Reasons: []string{"update.auto_apply is off, so installing is your decision"},
		}
		return out, nil
	}

	out.Assessment, err = d.assessUnattended(ctx, inst, candidate)
	if err != nil {
		return out, err
	}
	if !out.Assessment.OK {
		return out, nil
	}

	// A dry run stops here on purpose. It has already reported what it
	// would install, and a plan that converged a deployment would be the
	// one command in this program that lies about what it does.
	if opts.DryRun {
		return out, nil
	}

	result, err := Update(ctx, d, UpdateOptions{Options: opts.Options, To: candidate.Version.String()})
	out.Update = &result
	out.Applied = err == nil
	return out, err
}

// assessUnattended answers whether the staged candidate may install itself.
//
// Two gates, and they are different questions. CheckUpgrade asks whether this
// machine may move to that release at all -- the same gate an operator's
// `update` runs, so a candidate failing it would fail for them too.
// AssessUnattended asks the narrower one: if it goes wrong, can this machine
// get back without a restore decision at three in the morning?
func (d *Deps) assessUnattended(
	ctx context.Context, inst domain.Installation, candidate domain.UpdateCandidate,
) (domain.UnattendedAssessment, error) {
	current, err := d.State.CurrentRelease(ctx)
	if err != nil {
		return domain.UnattendedAssessment{}, err
	}
	if current.IsZero() {
		return domain.UnattendedAssessment{
			Reasons: []string{"no release is installed, so there is nothing to update"},
		}, nil
	}

	installed, err := d.resolveCurrentRelease(ctx, current)
	if err != nil {
		return domain.UnattendedAssessment{}, err
	}
	target, err := release.Load(candidate.Root)
	if err != nil {
		return domain.UnattendedAssessment{}, err
	}

	assessment := domain.AssessUnattended(
		installed.Manifest.Compatibility, target.Manifest.Compatibility, inst)

	if report := domain.CheckUpgrade(current.Version, target.Version(),
		target.Manifest.Compatibility, d.ManagerVersion,
		current.SchemaAtInstall); !report.OK {
		assessment.OK = false
		assessment.Reasons = append(assessment.Reasons, report.Problems...)
	}
	return assessment, nil
}
