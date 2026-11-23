package suite

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	signer "github.com/morzecrew/morzer/internal/adapters/sign/minisign"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// A channel's signed index (RFC 0037 §5.2), against the real minisign
// verifier: the follower accepts a fresh one, refuses what would let a
// registry hold the channel back, and warns when the index goes stale.

// indexVendor is the vendor's side of an indexed channel: the key its bundles
// and its index are signed with, and the release the tag points at.
type indexVendor struct {
	t        *testing.T
	registry *fakeRegistry
	keyPath  string
	release  domain.ChannelIndexRelease
	issued   time.Time
}

// publish signs an index under seq, valid for a week from the harness clock,
// and puts it beside the channel.
func (v indexVendor) publish(seq int64, release domain.ChannelIndexRelease) domain.ChannelIndex {
	idx := domain.ChannelIndex{
		Schema: domain.ChannelIndexSchema, Channel: "stable", Sequence: seq,
		IssuedAt: domain.NewTime(v.issued), ExpiresAt: domain.NewTime(v.issued.Add(7 * 24 * time.Hour)),
		Release: release,
	}
	v.publishIndex(idx, v.keyPath)
	return idx
}

func (v indexVendor) publishIndex(idx domain.ChannelIndex, keyPath string) {
	data, err := idx.Encode()
	require.NoError(v.t, err)
	sig, err := signer.New(keyPath, "vendor").Sign(context.Background(), data, idx.Comment())
	require.NoError(v.t, err)
	v.registry.publishIndex(data, sig.Encoded)
}

// unservedRelease is a release the channel does not serve.
var unservedRelease = domain.ChannelIndexRelease{
	Version: domain.MustParseVersion("1.4.0"),
	Digest:  "sha256:" + strings.Repeat("ab", 32),
}

// indexedChannel is a machine following a channel, pinning the vendor's key.
func indexedChannel(t *testing.T) (*harness, indexVendor, *recordingNotifier) {
	t.Helper()
	h, registry, _, notifier := followingHarness(t)
	ctx := context.Background()

	keyPath := filepath.Join(t.TempDir(), "vendor.key")
	pub, err := signer.New(keyPath, "vendor").EnsureKey(ctx)
	require.NoError(t, err)
	h.Deps.Checker = signer.NewChecker()

	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Policy.SigningKeys = []string{pub.Line}
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

	ref, err := ports.ParseRef(inst.Update.Channel)
	require.NoError(t, err)
	resolved, err := h.Deps.Source.Resolve(ctx, ref)
	require.NoError(t, err)

	return h, indexVendor{
		t: t, registry: registry, keyPath: keyPath,
		release: domain.ChannelIndexRelease{Version: resolved.Version, Digest: resolved.Digest},
		issued:  h.Deps.Now().Add(-time.Hour),
	}, notifier
}

func followOnce(t *testing.T, h *harness) ops.FollowChannelResult {
	t.Helper()
	res, err := ops.FollowChannel(context.Background(), h.Deps, ops.FollowChannelOptions{Explicit: true})
	if res.Candidate.Refused == "" || res.Candidate.Refused != domain.AsError(err).Message {
		// A refusal is recorded and returned; anything else is a failure.
		require.NoError(t, err)
	}
	return res
}

func clockLater(h *harness, by time.Duration) {
	at := h.Deps.Now().Add(by)
	h.Deps.Now = func() time.Time { return at }
}

func TestAFreshIndexIsAcceptedAndWhatItNamesIsStaged(t *testing.T) {
	h, vendor, _ := indexedChannel(t)
	vendor.publish(1000, vendor.release)

	res := followOnce(t, h)
	require.True(t, res.Candidate.IsStaged(), res.Candidate.Refused)
	require.NotNil(t, res.Index)
	require.NotNil(t, res.Index.Accepted)
	assert.Equal(t, int64(1000), res.Index.Accepted.Sequence)
	assert.Empty(t, res.Index.Problem)

	status, err := ops.GetStatus(context.Background(), h.Deps)
	require.NoError(t, err)
	require.NotNil(t, status.Following.Index)
	assert.Equal(t, int64(1000), status.Following.Index.Accepted.Sequence)
	assert.Equal(t, events.CheckOK, doctorCheck(t, h, "release.channel-index").Status)
}

// Decisions 10 and 29: a bundle the index does not name is refused, and
// fetched again once the vendor re-signs.
func TestABundleTheIndexDoesNotNameIsRefusedUntilTheIndexIsReSigned(t *testing.T) {
	h, vendor, _ := indexedChannel(t)
	vendor.publish(1000, unservedRelease)

	res := followOnce(t, h)
	require.False(t, res.Candidate.IsStaged())
	assert.Contains(t, res.Candidate.Refused, "its index (sequence 1000) names 1.4.0")
	assert.Equal(t, int64(1000), res.Candidate.IndexSequence)
	marks, err := os.ReadFile(h.Deps.Paths.ChannelMarksFile())
	if err == nil {
		assert.NotContains(t, string(marks), "1.3.0", "a refused bundle raises no mark")
	}

	// Unmoved, and judged against the same index: not fetched again.
	assert.False(t, followOnce(t, h).Moved)

	vendor.publish(2000, vendor.release)
	again := followOnce(t, h)
	assert.True(t, again.Moved, "a newer index is a reason to look at the refusal again")
	assert.True(t, again.Candidate.IsStaged(), again.Candidate.Refused)
}

func TestAnIndexTheFollowerCannotAcceptCountsAsNone(t *testing.T) {
	otherKey := filepath.Join(t.TempDir(), "other.key")
	_, err := signer.New(otherKey, "other").EnsureKey(context.Background())
	require.NoError(t, err)

	for name, plant := range map[string]func(v indexVendor){
		"signed by another key": func(v indexVendor) {
			v.publishIndex(domain.ChannelIndex{
				Schema: 1, Channel: "stable", Sequence: 1000, IssuedAt: domain.NewTime(v.issued),
				ExpiresAt: domain.NewTime(v.issued.Add(time.Hour * 24)), Release: unservedRelease,
			}, otherKey)
		},
		"for another channel": func(v indexVendor) {
			v.publishIndex(domain.ChannelIndex{
				Schema: 1, Channel: "beta", Sequence: 1000, IssuedAt: domain.NewTime(v.issued),
				ExpiresAt: domain.NewTime(v.issued.Add(time.Hour * 24)), Release: unservedRelease,
			}, v.keyPath)
		},
		"not an index": func(v indexVendor) {
			v.registry.publishIndex([]byte("{}"), []byte("not a signature"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, vendor, _ := indexedChannel(t)
			plant(vendor)

			// Followed as a channel with no index is: what it names
			// counts for nothing.
			res := followOnce(t, h)
			assert.True(t, res.Candidate.IsStaged(), res.Candidate.Refused)
			require.NotNil(t, res.Index)
			assert.Nil(t, res.Index.Accepted)
			assert.NotEmpty(t, res.Index.Problem)
			assert.Equal(t, events.CheckWarn, doctorCheck(t, h, "release.channel-index").Status)
		})
	}
}

// Decision 16: a replay, and a different index under an accepted sequence,
// leave the accepted one standing.
func TestAReplayedOrRewrittenIndexLeavesTheAcceptedOneStanding(t *testing.T) {
	for name, tc := range map[string]struct {
		seq  int64
		says string
	}{
		"a lower sequence":               {500, "refused as a replay"},
		"the same sequence, other bytes": {1000, "differs from the one accepted"},
	} {
		t.Run(name, func(t *testing.T) {
			h, vendor, _ := indexedChannel(t)
			vendor.publish(1000, vendor.release)
			require.True(t, followOnce(t, h).Candidate.IsStaged())

			vendor.issued = vendor.issued.Add(time.Minute)
			vendor.publish(tc.seq, vendor.release)
			res := followOnce(t, h)
			require.NotNil(t, res.Index.Accepted)
			assert.Equal(t, int64(1000), res.Index.Accepted.Sequence)
			assert.Contains(t, res.Index.Problem, tc.says)
		})
	}

	t.Run("the same index again", func(t *testing.T) {
		h, vendor, _ := indexedChannel(t)
		vendor.publish(1000, vendor.release)
		followOnce(t, h)
		before, err := os.Stat(h.Deps.Paths.ChannelIndexesFile())
		require.NoError(t, err)

		res := followOnce(t, h)
		assert.Empty(t, res.Index.Problem)
		after, err := os.Stat(h.Deps.Paths.ChannelIndexesFile())
		require.NoError(t, err)
		assert.Equal(t, before.ModTime(), after.ModTime(), "an unchanged index writes nothing")
	})
}

// §6: an index that expires while the tag stays still is the freeze signal,
// notified once (decision 28) and held from an unattended install (decision 3).
func TestAnIndexThatExpiresWhileTheTagStaysStillIsTheFreezeSignal(t *testing.T) {
	h, vendor, notifier := indexedChannel(t)
	vendor.publish(1000, vendor.release)
	require.True(t, followOnce(t, h).Candidate.IsStaged())

	clockLater(h, 8*24*time.Hour)
	res := followOnce(t, h)
	assert.False(t, res.Moved)
	require.NotNil(t, res.Index)
	assert.True(t, res.Index.Expired)
	followOnce(t, h)

	expired := 0
	for _, kind := range notifier.kinds() {
		if kind == events.KindChannelIndexExpired {
			expired++
		}
	}
	assert.Equal(t, 1, expired, "an index's expiry is notified once")

	status, err := ops.GetStatus(context.Background(), h.Deps)
	require.NoError(t, err)
	assert.Contains(t, strings.Join(status.Problems, "\n"), "index expired 1d ago")
	check := doctorCheck(t, h, "release.channel-index")
	assert.Equal(t, events.CheckWarn, check.Status)
	assert.Contains(t, check.Message, "expired")

	ctx := context.Background()
	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Update.AutoApply, inst.Policy.RequireSignature = true, true
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))
	tick, err := ops.RunUnattended(ctx, h.Deps, ops.UnattendedOptions{})
	require.NoError(t, err)
	assert.False(t, tick.Applied)
	assert.Contains(t, strings.Join(tick.Assessment.Reasons, "\n"), "index expired")
}

// A registry that passes the vendor's fresh index through and holds the tag
// still is caught, though that index never expires.
func TestATagHeldApartFromAFreshIndexIsReportedAndNotInstalled(t *testing.T) {
	h, vendor, _ := indexedChannel(t)
	vendor.publish(1000, vendor.release)
	require.True(t, followOnce(t, h).Candidate.IsStaged())

	vendor.publish(2000, unservedRelease)
	res := followOnce(t, h)
	assert.False(t, res.Moved)
	assert.Contains(t, res.Index.Disagrees, "names 1.4.0, and its tag still serves 1.3.0")
	assert.Equal(t, events.CheckWarn, doctorCheck(t, h, "release.channel-index").Status)

	ctx := context.Background()
	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Update.AutoApply, inst.Policy.RequireSignature = true, true
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))
	tick, err := ops.RunUnattended(ctx, h.Deps, ops.UnattendedOptions{})
	require.NoError(t, err)
	assert.False(t, tick.Applied)
	assert.Contains(t, strings.Join(tick.Assessment.Reasons, "\n"), "names 1.4.0, not the staged 1.3.0")
}

// Decision 25: removing the index does not undo it. The tag is judged against
// the accepted one until it expires, and `--forget-index` lets go of it.
func TestARemovedIndexStillJudgesTheTagUntilItExpiresOrIsForgotten(t *testing.T) {
	setup := func(t *testing.T) *harness {
		h, vendor, _ := indexedChannel(t)
		vendor.publish(1000, unservedRelease)
		require.NotEmpty(t, followOnce(t, h).Candidate.Refused)
		vendor.registry.publishIndex(nil, nil)

		res := followOnce(t, h)
		assert.False(t, res.Moved, "the refusal still stands against the accepted index")
		assert.Contains(t, res.Index.Problem, "publishes no index now")
		return h
	}

	t.Run("until it expires", func(t *testing.T) {
		h := setup(t)
		clockLater(h, 8*24*time.Hour)
		res := followOnce(t, h)
		assert.True(t, res.Candidate.IsStaged(), "an expired index judges nothing: %s", res.Candidate.Refused)
	})

	t.Run("until it is forgotten", func(t *testing.T) {
		h := setup(t)
		ctx := context.Background()
		res, err := ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true, ForgetIndex: true})
		require.NoError(t, err, res.Candidate.Refused)
		assert.Equal(t, int64(1000), res.ForgotIndex)
		assert.True(t, res.Candidate.IsStaged(), res.Candidate.Refused)
		assert.Nil(t, res.Index)

		journal, err := h.Deps.State.Operations(ctx, ports.Filter{})
		require.NoError(t, err)
		forgot := false
		for _, rec := range journal {
			forgot = forgot || rec.Flags["forget_index"] == res.Ref && rec.Flags["sequence"] == "1000"
		}
		assert.True(t, forgot, "forgetting an index is journalled")

		// Nothing left to forget: nothing journalled.
		again, err := ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true, ForgetIndex: true})
		require.NoError(t, err)
		assert.Zero(t, again.ForgotIndex)
	})
}

// Decision 26: with no key pinned the index is not consulted, only noted.
func TestAMachineWithNoKeyDoesNotConsultTheIndex(t *testing.T) {
	h, vendor, _ := indexedChannel(t)
	ctx := context.Background()
	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Policy.SigningKeys = nil
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))
	vendor.publish(1000, unservedRelease)

	res := followOnce(t, h)
	assert.True(t, res.Candidate.IsStaged(), res.Candidate.Refused)
	require.NotNil(t, res.Index)
	assert.True(t, res.Index.Unchecked)
	assert.Nil(t, res.Index.Accepted)
	check := doctorCheck(t, h, "release.channel-index")
	assert.Equal(t, events.CheckWarn, check.Status)
	assert.Contains(t, check.Remedy, "release trust add")
}

func TestAnIndexReadTheRegistryRefusesCountsAsNoneAndAnUnansweredOneFails(t *testing.T) {
	t.Run("refused", func(t *testing.T) {
		h, vendor, _ := indexedChannel(t)
		vendor.registry.indexStatus = http.StatusForbidden
		res := followOnce(t, h)
		assert.True(t, res.Candidate.IsStaged(), res.Candidate.Refused)
		assert.Contains(t, res.Index.Problem, "refused to show")
	})
	t.Run("unanswered", func(t *testing.T) {
		h, vendor, _ := indexedChannel(t)
		vendor.registry.indexStatus = http.StatusServiceUnavailable
		_, err := ops.FollowChannel(context.Background(), h.Deps, ops.FollowChannelOptions{Explicit: true})
		require.Error(t, err)
		assert.ErrorIs(t, err, domain.ErrRuntime)
	})
}

// An unmoved channel with an index still costs a manifest and no bundle.
func TestAnUnmovedIndexedChannelDownloadsNoBundle(t *testing.T) {
	h, vendor, _ := indexedChannel(t)
	vendor.publish(1000, vendor.release)
	followOnce(t, h)

	fetched := vendor.registry.blobBytes.Load()
	reads := vendor.registry.indexReads.Load()
	assert.False(t, followOnce(t, h).Moved)
	assert.Equal(t, fetched, vendor.registry.blobBytes.Load())
	assert.Equal(t, reads+1, vendor.registry.indexReads.Load(), "the index is read on every poll")
}

// An index accepted while an unattended install waits for the lock is checked
// again under it: the install must not land a release the index no longer
// names.
func TestAnUnattendedInstallRechecksTheIndexUnderTheLock(t *testing.T) {
	h, registry := recoverableChannel(t)
	ctx := context.Background()
	keyPath := filepath.Join(t.TempDir(), "vendor.key")
	pub, err := signer.New(keyPath, "vendor").EnsureKey(ctx)
	require.NoError(t, err)
	h.Deps.Checker = signer.NewChecker()
	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Policy.SigningKeys = []string{pub.Line}
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))
	ref, err := ports.ParseRef(inst.Update.Channel)
	require.NoError(t, err)
	resolved, err := h.Deps.Source.Resolve(ctx, ref)
	require.NoError(t, err)
	vendor := indexVendor{t: t, registry: registry, keyPath: keyPath, issued: h.Deps.Now().Add(-time.Hour),
		release: domain.ChannelIndexRelease{Version: resolved.Version, Digest: resolved.Digest}}
	vendor.publish(1000, vendor.release)
	require.True(t, followOnce(t, h).Candidate.IsStaged())

	// Another poll, landing while the tick waits for the lock, accepts an
	// index naming a release the tag does not serve.
	channel := ports.RedactRefCredentials(inst.Update.Channel)
	h.Locker.BeforeAcquire = func() {
		raw, err := json.Marshal(domain.ChannelIndexRecords{channel: {Accepted: &domain.AcceptedIndex{
			Sequence: 2000, Digest: "sha256:" + strings.Repeat("cd", 32), Release: unservedRelease,
			ExpiresAt: domain.NewTime(h.Deps.Now().Add(24 * time.Hour)),
		}}})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(h.Deps.Paths.ChannelIndexesFile(), raw, 0o640))
	}

	res, err := ops.RunUnattended(ctx, h.Deps, ops.UnattendedOptions{})
	require.Error(t, err)
	assert.False(t, res.Applied)
	assert.Contains(t, err.Error(), "names 1.4.0, not the staged 1.3.0")
}

// Forgetting is journalled before the index is let go, so a journal that
// cannot be written leaves the index accepted and the retry journals it.
func TestAnIndexIsForgottenOnlyOnceTheJournalSaysSo(t *testing.T) {
	h, vendor, _ := indexedChannel(t)
	vendor.publish(1000, vendor.release)
	followOnce(t, h)
	ctx := context.Background()

	// A directory where the journal goes: appending to it fails.
	journal := h.Deps.Paths.JournalFile()
	_, statErr := os.Stat(journal)
	had := statErr == nil
	if had {
		require.NoError(t, os.Rename(journal, journal+".aside"))
	}
	require.NoError(t, os.Mkdir(journal, 0o750))
	_, err := ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true, ForgetIndex: true})
	require.Error(t, err)
	records, err := os.ReadFile(h.Deps.Paths.ChannelIndexesFile())
	require.NoError(t, err)
	assert.Contains(t, string(records), `"sequence": 1000`, "the index was let go with nothing journalled")

	require.NoError(t, os.Remove(journal))
	if had {
		require.NoError(t, os.Rename(journal+".aside", journal))
	}
	res, err := ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true, ForgetIndex: true})
	require.NoError(t, err)
	assert.Equal(t, int64(1000), res.ForgotIndex)
}
