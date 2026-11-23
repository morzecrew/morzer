package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/infra/logging"
	"github.com/morzecrew/morzer/internal/ports"
)

// A vendor may publish a signed index beside a channel (RFC 0037 §5.2): the
// release the channel points at, under a sequence, until an expiry. A channel
// that never moves looks like one an attacker froze, and an index that expires
// without being re-signed is how a machine tells the two apart. The follower
// reads it on every poll, before the unmoved-tag return (decision 17), and
// only the follower does (decision 30).

// errIndexDisagrees marks a bundle refused because the channel's index names
// another one (decision 10), so the refusal records the index it was judged
// against and is looked at again once that changes (decision 29).
var errIndexDisagrees = errors.New("the channel's tag and its index disagree")

// ChannelIndexView is what `status`, `doctor` and a poll report of a channel's
// index: what was accepted, and what the last poll found wrong.
type ChannelIndexView struct {
	Accepted *domain.AcceptedIndex `json:"accepted,omitempty"`

	// Expired is the accepted index past its expiry: the freeze signal.
	Expired bool `json:"expired,omitempty"`

	// Unchecked is an index this machine pins no key to check.
	Unchecked bool `json:"unchecked,omitempty"`

	// Problem is why the last poll accepted nothing new.
	Problem string `json:"problem,omitempty"`

	// Disagrees is a tag that still serves another release than the
	// accepted index names.
	Disagrees string `json:"disagrees,omitempty"`
}

// channelIndexView is the record as reported, or nil when there is nothing to
// report.
func channelIndexView(rec domain.ChannelIndexRecord, now time.Time) *ChannelIndexView {
	if rec.IsZero() {
		return nil
	}
	return &ChannelIndexView{
		Accepted:  rec.Accepted,
		Expired:   rec.Accepted != nil && rec.Accepted.Expired(now),
		Unchecked: rec.Unchecked,
		Problem:   rec.Problem,
		Disagrees: rec.Disagrees,
	}
}

// Lines are the view as sentences an operator reads, most urgent first.
func (v *ChannelIndexView) Lines(now time.Time) []string {
	if v == nil {
		return nil
	}
	var out []string
	if v.Unchecked {
		out = append(out, "the update channel publishes a signed index this machine cannot check, "+
			"since it pins no signing key")
	}
	if v.Expired {
		out = append(out, fmt.Sprintf("the update channel's index expired %s ago, naming %s: its vendor "+
			"has not re-signed it, or the channel is being held back",
			roundDays(now.Sub(v.Accepted.ExpiresAt.Time)), v.Accepted.Release.Version))
	}
	if v.Disagrees != "" {
		out = append(out, v.Disagrees+", which is what a channel held back looks like")
	}
	if v.Problem != "" {
		out = append(out, v.Problem)
	}
	return out
}

// channelIndexRecords reads the records. A missing file is none; one that
// cannot be read is reported rather than read as none, which would forget an
// accepted sequence and admit a replay.
func (d *Deps) channelIndexRecords() (domain.ChannelIndexRecords, error) {
	path := d.Paths.ChannelIndexesFile()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return domain.ChannelIndexRecords{}, nil
	}
	if err != nil {
		return nil, domain.InstallationError(err, "cannot read %s", path)
	}
	records := domain.ChannelIndexRecords{}
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, domain.InstallationError(err, "the channel indexes at %s are not valid JSON", path)
	}
	// JSON's null decodes to no map at all, which is damage rather than
	// an empty record: reading it as none would forget every sequence.
	if records == nil {
		return nil, domain.InstallationError(nil, "the channel indexes at %s are null, not a record", path)
	}
	return records, nil
}

// saveChannelIndexRecords writes the records, leaving out empty ones, and
// removes the file when none is left.
func (d *Deps) saveChannelIndexRecords(records domain.ChannelIndexRecords) error {
	kept := domain.ChannelIndexRecords{}
	for channel, rec := range records {
		if !rec.IsZero() {
			kept[channel] = rec
		}
	}
	path := d.Paths.ChannelIndexesFile()
	if len(kept) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return domain.InstallationError(err, "cannot remove %s", path)
		}
		return nil
	}
	data, err := json.MarshalIndent(kept, "", "  ")
	if err != nil {
		return domain.Internal(err, "cannot serialise the channel indexes")
	}
	return atomicfs.WriteFile(path, append(data, '\n'), 0o640)
}

// replaceChannelIndexes makes the accepted indexes exactly these, each keyed
// with its credentials removed, as an import replaces the marks (decision 31).
func (d *Deps) replaceChannelIndexes(from domain.ChannelIndexes) error {
	records := domain.ChannelIndexRecords{}
	for channel, idx := range from {
		key := channelKey(channel)
		if have, ok := records[key]; ok && have.Accepted.Sequence >= idx.Sequence {
			continue
		}
		records[key] = domain.ChannelIndexRecord{Accepted: &idx}
	}
	return d.saveChannelIndexRecords(records)
}

// exportedChannelIndexes is what the channels' indexes said when they were
// accepted, or none when the record cannot be read: an export is taken when a
// machine may be failing, as exportedChannelMarks has it.
func exportedChannelIndexes(ctx context.Context, d *Deps) domain.ChannelIndexes {
	records, err := d.channelIndexRecords()
	if err != nil {
		logging.FromContext(ctx).Warn("the export carries no channel indexes", "error", err)
		return nil
	}
	var out domain.ChannelIndexes
	for channel, rec := range records {
		if rec.Accepted == nil {
			continue
		}
		if out == nil {
			out = domain.ChannelIndexes{}
		}
		out[channel] = *rec.Accepted
	}
	return out
}

// judgedIndex is the index the tag is judged against: the accepted one while
// it holds and this machine can check one (decisions 10, 25 and 26). One that
// has expired no longer judges anything, and is the warning instead (decision
// 3), so a vendor who stops re-signing does not block every later release.
func judgedIndex(inst domain.Installation, rec domain.ChannelIndexRecord, now time.Time) *domain.AcceptedIndex {
	if len(inst.Policy.SigningKeys) == 0 || rec.Accepted == nil || rec.Accepted.Expired(now) {
		return nil
	}
	return rec.Accepted
}

// indexPoll is what one poll found under the channel's index tag.
type indexPoll struct {
	// consulted is a machine with a key to check the index against
	// (decision 26).
	consulted bool

	// unknown is a read that failed on a machine that does not consult the
	// index: there is nothing to record, so the record stays as it was.
	unknown bool

	found     bool
	published ports.PublishedIndex

	// refused is a read the registry refused, which counts as no index.
	refused string
}

// readChannelIndex fetches the index published beside ref. With a key pinned,
// a registry that cannot be reached fails the poll, as a failed peek does; one
// that refuses the read counts as having no index, and says why.
func (d *Deps) readChannelIndex(ctx context.Context, inst domain.Installation, ref ports.Ref) (indexPoll, error) {
	poll := indexPoll{consulted: len(inst.Policy.SigningKeys) > 0}
	reader, ok := d.Source.(ports.ChannelIndexReader)
	if !ok {
		return poll, nil
	}
	published, found, err := reader.FetchIndex(ctx, ref)
	switch {
	case err == nil:
		poll.found, poll.published = found, published
	case !poll.consulted:
		poll.unknown = true
	case errors.Is(err, domain.ErrRuntime):
		return poll, err
	default:
		poll.refused = domain.AsError(err).Message
	}
	return poll, nil
}

// tagContent is what this machine knows the tag to serve now: the release the
// last fetch from it verified, staged or installed. Zero when it has fetched
// nothing the tag points at now, which a moved tag's staging then fetches.
type tagContent struct {
	digest  string
	version domain.Version
}

func (d *Deps) tagContent(ctx context.Context, upstream string) (tagContent, error) {
	candidate, err := d.State.UpdateCandidate(ctx)
	if err != nil {
		return tagContent{}, err
	}
	if candidate.IsStaged() && candidate.UpstreamDigest == upstream {
		return tagContent{digest: candidate.Digest, version: candidate.Version}, nil
	}
	current, err := d.State.CurrentRelease(ctx)
	if err != nil {
		return tagContent{}, err
	}
	if upstream != "" && current.UpstreamDigest == upstream {
		return tagContent{digest: current.Digest, version: current.Version}, nil
	}
	return tagContent{}, nil
}

// judgeChannelIndex is the record a poll leaves, from the one before it and
// what it found. An index it cannot accept counts as none: the tag is then
// judged against the index accepted before, if any, and a channel that never
// had one is followed as today. The reason is recorded for `status` and
// `doctor`.
func (d *Deps) judgeChannelIndex(
	inst domain.Installation, prev domain.ChannelIndexRecord, poll indexPoll, tag tagContent, now time.Time,
) domain.ChannelIndexRecord {
	if poll.unknown {
		return prev
	}
	next := domain.ChannelIndexRecord{Accepted: prev.Accepted, ExpiryNotified: prev.ExpiryNotified}
	if !poll.consulted {
		// Not checked, only noted, so `doctor` can say a pinned key
		// would make it count (decision 26).
		next.Unchecked = poll.found && poll.published.Problem == ""
		return next
	}
	switch {
	case poll.refused != "":
		next.Problem = "the registry refused to show the update channel's index: " + poll.refused
	case !poll.found:
		// Removing the index does not undo it (decision 25).
		if acc := prev.Accepted; acc != nil && acc.Expired(now) {
			next.Problem = "the update channel publishes no index now, and the one accepted has expired"
		} else if acc != nil {
			next.Problem = fmt.Sprintf("the update channel publishes no index now; its tag is judged "+
				"against the one accepted, sequence %d, until that expires", acc.Sequence)
		}
	default:
		next.Accepted, next.Problem = d.acceptChannelIndex(inst, prev.Accepted, poll.published)
	}
	if judged := judgedIndex(inst, next, now); judged != nil && tag.digest != "" &&
		!atomicfs.SameDigest(judged.Release.Digest, tag.digest) {
		next.Disagrees = fmt.Sprintf("the update channel's index names %s, and its tag still serves %s",
			judged.Release.Version, tag.version)
	}
	return next
}

// acceptChannelIndex checks a published index against the keys, the channel
// and the index accepted before, and returns the index accepted after it, with
// the reason when that is not the published one.
func (d *Deps) acceptChannelIndex(
	inst domain.Installation, prev *domain.AcceptedIndex, published ports.PublishedIndex,
) (*domain.AcceptedIndex, string) {
	if published.Problem != "" {
		return prev, "the update channel's index cannot be read: " + published.Problem
	}
	if d.Checker == nil {
		return prev, "this manager cannot check the update channel index's signature"
	}
	if !slices.ContainsFunc(inst.Policy.SigningKeys, func(key string) bool {
		return d.Checker.Check(published.Index, published.Signature, key)
	}) {
		return prev, "the update channel's index is not signed by a key this installation trusts"
	}
	idx, err := domain.ParseChannelIndex(published.Index)
	if err != nil {
		return prev, "the update channel's index cannot be read: " + domain.AsError(err).Message
	}
	// Another channel's index, copied under this one's tag, would move this
	// channel to a release the vendor published for that one.
	if idx.Channel != published.Channel {
		return prev, fmt.Sprintf("the update channel's index names the channel %s, not %s",
			idx.Channel, published.Channel)
	}
	sum := sha256.Sum256(published.Index)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	switch {
	case prev == nil || idx.Sequence > prev.Sequence:
		return &domain.AcceptedIndex{
			Sequence: idx.Sequence, Digest: digest, Release: idx.Release, ExpiresAt: idx.ExpiresAt,
		}, ""
	case idx.Sequence == prev.Sequence && digest == prev.Digest:
		return prev, ""
	case idx.Sequence == prev.Sequence:
		return prev, fmt.Sprintf("the update channel's index under sequence %d differs from the one "+
			"accepted under it; its vendor publishes a change under a higher sequence", idx.Sequence)
	default:
		return prev, fmt.Sprintf("the update channel's index has sequence %d, lower than the %d "+
			"accepted, so it is refused as a replay", idx.Sequence, prev.Sequence)
	}
}

// expiryDue reports an accepted index that has expired and was not notified
// yet, and marks it notified: each index's expiry is notified once (decision
// 28).
func expiryDue(inst domain.Installation, rec *domain.ChannelIndexRecord, now time.Time) bool {
	acc := rec.Accepted
	if len(inst.Policy.SigningKeys) == 0 || acc == nil || !acc.Expired(now) || rec.ExpiryNotified == acc.Sequence {
		return false
	}
	rec.ExpiryNotified = acc.Sequence
	return true
}

// pollChannelIndex reads and judges the channel's index, and returns the
// record the poll leaves. It writes only when the record changed, which is
// once per re-sign rather than once per tick, and under the deployment lock,
// judging again from the record as it stands there. An expiry is notified
// once, after the write that records it.
//
// forgotten is a plan's `--forget-index`: the record is judged from nothing, as
// it would be once forgotten, and nothing is written.
func (d *Deps) pollChannelIndex(
	ctx context.Context, opts Options, inst domain.Installation, ref ports.Ref, channel, upstream string,
	forgotten bool,
) (domain.ChannelIndexRecord, error) {
	poll, err := d.readChannelIndex(ctx, inst, ref)
	if err != nil {
		return domain.ChannelIndexRecord{}, err
	}
	tag, err := d.tagContent(ctx, upstream)
	if err != nil {
		return domain.ChannelIndexRecord{}, err
	}
	judge := func(inst domain.Installation) (domain.ChannelIndexRecords, domain.ChannelIndexRecord, bool, bool, error) {
		records, err := d.channelIndexRecords()
		if err != nil {
			return nil, domain.ChannelIndexRecord{}, false, false, err
		}
		prev := records[channel]
		if forgotten {
			prev = domain.ChannelIndexRecord{}
		}
		next := d.judgeChannelIndex(inst, prev, poll, tag, d.now())
		notify := expiryDue(inst, &next, d.now())
		records[channel] = next
		return records, next, notify, !sameIndexRecord(prev, next), nil
	}

	_, next, _, changed, err := judge(inst)
	if err != nil || !changed || opts.DryRun {
		return next, err
	}
	notify := false
	err = d.withLock(ctx, d.newOpID(), domain.OpTypeRelease, opts, func(ctx context.Context) error {
		// The keys as they stand now: a key removed while this waited
		// must not set the replay floor.
		current, err := d.loadInstallation(ctx)
		if err != nil {
			return err
		}
		records, fresh, due, moved, err := judge(current)
		if err != nil {
			return err
		}
		next = fresh
		if !moved {
			return nil
		}
		notify = due
		return d.saveChannelIndexRecords(records)
	})
	if err != nil {
		return next, err
	}
	if notify {
		d.notify(ctx, events.ChannelIndexExpired(inst.Product, channel, *next.Accepted))
	}
	return next, nil
}

// sameIndexRecord compares two records as they would be written.
func sameIndexRecord(a, b domain.ChannelIndexRecord) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

// forgetChannelIndex forgets the channel's accepted index, for a vendor who
// stopped publishing one (decision 25), and journals it. It returns the
// sequence forgotten, zero when none was accepted, and then journals nothing.
// A plan forgets nothing: pollChannelIndex judges as if it had.
func (d *Deps) forgetChannelIndex(ctx context.Context, opts Options, inst domain.Installation, channel string) (int64, error) {
	if opts.DryRun {
		records, err := d.channelIndexRecords()
		if err != nil || records[channel].Accepted == nil {
			return 0, err
		}
		return records[channel].Accepted.Sequence, nil
	}
	var forgot int64
	opID := d.newOpID()
	started := d.now()
	err := d.withLock(ctx, opID, domain.OpTypeRelease, opts, func(ctx context.Context) error {
		records, err := d.channelIndexRecords()
		if err != nil {
			return err
		}
		acc := records[channel].Accepted
		if acc == nil {
			return nil
		}
		// Journalled first: an index let go with nothing journalled is a
		// replay guard dropped that no record shows, and a retry would find
		// nothing to forget. A record whose deletion then fails is followed
		// by the retry's own.
		if err := d.State.AppendOperation(ctx, domain.OperationRecord{
			SchemaVersion:  domain.OperationSchemaVersion,
			ID:             opID,
			Type:           domain.OpTypeRelease,
			Status:         domain.StatusSucceeded,
			StartedAt:      domain.NewTime(started),
			FinishedAt:     domain.NewTime(d.now()),
			ManagerVersion: d.ManagerVersion.String(),
			InstallationID: inst.ID,
			Flags: map[string]string{
				"forget_index": channel,
				"sequence":     strconv.FormatInt(acc.Sequence, 10),
			},
		}); err != nil {
			return err
		}
		delete(records, channel)
		if err := d.saveChannelIndexRecords(records); err != nil {
			return err
		}
		forgot = acc.Sequence
		return nil
	})
	return forgot, err
}

// refuseHeldForIndex is heldForIndex again under the update's lock, for the
// release the tick is installing: a poll that accepted another index while the
// tick waited for the lock has the last word. It judges that release against
// the followed channel's index whatever the candidate record now says, since
// the same poll may have replaced it. Only an unattended update asks, since an
// operator's update does not read the index (decision 30).
//
// channel is the channel the tick staged the release from. Changed since, the
// release is not installed: the new channel's index says nothing about it.
func (d *Deps) refuseHeldForIndex(ctx context.Context, staged domain.Release, channel string) error {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return err
	}
	if followed := channelKey(inst.Update.Channel); followed != channel {
		return domain.ValidationError(nil,
			"the followed channel changed while the tick waited, so %s from %s is not installed",
			staged.Version(), channel).
			WithHint("the next tick follows %s", followed)
	}
	installing := domain.UpdateCandidate{SourceRef: channel, Version: staged.Version(), Digest: staged.Digest}
	if held := d.heldForIndex(inst, installing); held != "" {
		return domain.ValidationError(nil, "%s", held)
	}
	return nil
}

// heldForIndex is why the tick leaves a staged release uninstalled because of
// the channel's index, or empty. An expired index holds it under auto_apply
// (decision 3), and so does an index that names another release: the tag
// still serves something the vendor no longer says it points at.
func (d *Deps) heldForIndex(inst domain.Installation, candidate domain.UpdateCandidate) string {
	if len(inst.Policy.SigningKeys) == 0 {
		return ""
	}
	records, err := d.channelIndexRecords()
	if err != nil {
		return fmt.Sprintf("the channel's index record cannot be read (%s), so %s waits for an operator",
			domain.AsError(err).Message, candidate.Version)
	}
	acc := records[candidate.SourceRef].Accepted
	switch {
	case acc == nil:
		return ""
	case acc.Expired(d.now()):
		return fmt.Sprintf("the channel's index expired at %s, so %s is staged and not installed: "+
			"a channel whose index is not re-signed may be held back, and `morzer update --to %s` "+
			"installs it once you have looked", acc.ExpiresAt.Format(time.RFC3339), candidate.Version, candidate.Version)
	case !atomicfs.SameDigest(acc.Release.Digest, candidate.Digest):
		return fmt.Sprintf("the channel's index names %s, not the staged %s, so it waits for an operator",
			acc.Release.Version, candidate.Version)
	}
	return ""
}
