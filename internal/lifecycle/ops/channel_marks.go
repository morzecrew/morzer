package ops

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
)

// A channel mark is the newest release this installation fetched and verified
// from a channel (RFC 0037 §5.1). Staging refuses anything older from that
// channel: it delivered the newer one, and serving an older one now is what a
// rollback attack on the channel looks like. Only a verified fetch raises a
// mark, never a peek or `update --check`, so a registry advertising a false
// high version blocks nothing.

// channelMarks reads the marks. A missing file is none; one that cannot be read
// is reported rather than treated as none, which would admit every release.
func (d *Deps) channelMarks() (domain.ChannelMarks, error) {
	data, err := os.ReadFile(d.Paths.ChannelMarksFile())
	if errors.Is(err, fs.ErrNotExist) {
		return domain.ChannelMarks{}, nil
	}
	if err != nil {
		return nil, domain.InstallationError(err, "cannot read %s", d.Paths.ChannelMarksFile())
	}
	marks := domain.ChannelMarks{}
	if err := json.Unmarshal(data, &marks); err != nil {
		return nil, domain.InstallationError(err,
			"the channel marks at %s are not valid JSON", d.Paths.ChannelMarksFile())
	}
	return marks, nil
}

func (d *Deps) saveChannelMarks(marks domain.ChannelMarks) error {
	data, err := json.MarshalIndent(marks, "", "  ")
	if err != nil {
		return domain.Internal(err, "cannot serialise the channel marks")
	}
	return atomicfs.WriteFile(d.Paths.ChannelMarksFile(), append(data, '\n'), 0o640)
}

// replaceChannelMarks makes the marks exactly these, each keyed with its
// credentials removed, and removes the file when there are none.
func (d *Deps) replaceChannelMarks(from domain.ChannelMarks) error {
	if len(from) == 0 {
		if err := os.Remove(d.Paths.ChannelMarksFile()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return domain.InstallationError(err, "cannot remove %s", d.Paths.ChannelMarksFile())
		}
		return nil
	}
	marks := domain.ChannelMarks{}
	for channel, mark := range from {
		key := channelKey(channel)
		if have, ok := marks[key]; !ok || have.Version.LessThan(mark.Version) {
			marks[key] = mark
		}
	}
	return d.saveChannelMarks(marks)
}

// channelKey names a channel as the marks do: by its reference with any
// credentials removed (decision 13), so the file is never a copy of a password.
func channelKey(ref string) string { return ports.RedactRefCredentials(ref) }

// refuseBelowMark refuses a release older than what the channel already
// delivered.
func refuseBelowMark(marks domain.ChannelMarks, channel string, v domain.Version) error {
	mark, ok := marks[channel]
	if !ok || !v.LessThan(mark.Version) {
		return nil
	}
	return domain.ValidationError(nil,
		"the channel points at %s, older than the %s it already delivered", v, mark.Version).
		WithHint("a channel serving an older release than it already served is what a rollback "+
			"attack looks like, and also what a withdrawn release looks like; to go back on "+
			"purpose, `morzer update --to %s --allow-older`", v)
}

// raiseChannelMark records a verified fetch from a channel. Staging has already
// refused anything below the channel's mark, so this only raises it, or
// refreshes its time when the same release is staged again.
func (d *Deps) raiseChannelMark(channel string, v domain.Version) error {
	marks, err := d.channelMarks()
	if err != nil {
		return err
	}
	marks[channel] = domain.ChannelMark{Version: v, At: domain.NewTime(d.now())}
	return d.saveChannelMarks(marks)
}

// lowerFollowedMarkOrWarn is lowerFollowedMark after an operation that has
// already succeeded: failing it now would report a converged machine as
// failed, so a mark left high is a warning the operator sees, and the
// follower's refusal names the way out. The installation is read again here,
// under the lock, because the channel it follows may have changed while the
// operation waited for it.
func (d *Deps) lowerFollowedMarkOrWarn(ctx context.Context, v domain.Version) {
	inst, err := d.loadInstallation(ctx)
	if err == nil {
		err = d.lowerFollowedMark(inst, v)
	}
	if err != nil {
		d.warnf("the followed channel's mark was not lowered to %s: %s; its follower may refuse "+
			"releases below the old mark", v, domain.AsError(err).Message)
	}
}

// lowerFollowedMark lowers the mark of the channel the installation follows to
// v, when an operator deliberately installed something below it: an
// `update --allow-older` or a `rollback`. Otherwise the follower would refuse
// every later release below the old mark, which leaves a machine whose vendor
// withdrew a release unable to follow its channel. Other channels' marks are
// left alone. See logs/version-floor.md.
func (d *Deps) lowerFollowedMark(inst domain.Installation, v domain.Version) error {
	if inst.Update.Channel == "" {
		return nil
	}
	marks, err := d.channelMarks()
	if err != nil {
		return err
	}
	channel := channelKey(inst.Update.Channel)
	mark, ok := marks[channel]
	if !ok || !v.LessThan(mark.Version) {
		return nil
	}
	// One write, and the poll does the rest: a refusal recorded before this
	// mark is looked at again (refusalOutdated), so no crash between two
	// writes can leave the follower refusing what the lower mark admits.
	marks[channel] = domain.ChannelMark{Version: v, At: domain.NewTime(d.now())}
	return d.saveChannelMarks(marks)
}

// refusalOutdated reports whether a recorded refusal predates its channel's
// mark, which happens only when the mark was lowered or replaced since. The
// refusal was then made against a mark that no longer stands, and the poll
// looks again instead of skipping the unmoved channel. Not before rather than
// after, because times are kept to the second: two polls in one second cost a
// fetch, where the other way a lowering in the same second as the refusal
// would be lost.
//
// A marks file that cannot be read is returned rather than read as "not
// outdated", which would report a channel as unmoved over it.
func (d *Deps) refusalOutdated(candidate domain.UpdateCandidate) (bool, error) {
	if candidate.Refused == "" {
		return false, nil
	}
	marks, err := d.channelMarks()
	if err != nil {
		return false, err
	}
	mark, ok := marks[candidate.SourceRef]
	return ok && !mark.At.Before(candidate.SeenAt.Time), nil
}
