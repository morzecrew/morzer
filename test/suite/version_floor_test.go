package suite

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// RFC 0037 P1: a release older than the one installed is refused outside
// `rollback`, and a channel is refused a release older than one it already
// delivered.

// renumberedArchive is the 1.3.0 fixture renumbered to version, retargeted into the
// harness root and archived, for a registry to serve.
func renumberedArchive(t *testing.T, h *harness, version string) []byte {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "bundle-"+version)
	copyBundle(t, filepath.Join(testBundlePath(t), "..", "bundle-1.3.0"), dir)
	retargetManifest(t, dir, h.Root)
	recoverableUpdate(t, dir)
	for file, line := range map[string]string{"manifest.yaml": "  version: 1.3.0\n", "VERSION": "1.3.0\n"} {
		path := filepath.Join(dir, file)
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		renumbered := strings.Replace(string(raw), line, strings.Replace(line, "1.3.0", version, 1), 1)
		require.NotEqual(t, string(raw), renumbered, "%s no longer says %q", file, line)
		require.NoError(t, os.WriteFile(path, []byte(renumbered), 0o644))
	}
	path := filepath.Join(t.TempDir(), "bundle.tar.zst")
	writeTarZst(t, dir, path)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	return raw
}

func (h *harness) marks() domain.ChannelMarks {
	h.t.Helper()
	raw, err := os.ReadFile(h.Paths.ChannelMarksFile())
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(h.t, err)
	var marks domain.ChannelMarks
	require.NoError(h.t, json.Unmarshal(raw, &marks))
	return marks
}

func (h *harness) channel() string {
	h.t.Helper()
	inst, err := h.Deps.State.LoadInstallation(context.Background())
	require.NoError(h.t, err)
	return ports.RedactRefCredentials(inst.Update.Channel)
}

// tick moves the harness clock on, which is fixed otherwise: the follower
// compares when a refusal was recorded with when its channel's mark was set.
func (h *harness) tick(by time.Duration) {
	now := h.Deps.Now().Add(by)
	h.Deps.Now = func() time.Time { return now }
}

func (h *harness) current() string {
	h.t.Helper()
	current, err := h.Deps.State.CurrentRelease(context.Background())
	require.NoError(h.t, err)
	return current.Version.String()
}

// The installed floor holds whatever the release came from, the store or a
// path, and `--allow-older` crosses it on purpose and says so in the journal.
func TestAnUpdateOlderThanTheInstalledReleaseIsRefused(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()
	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: stageRecoverableUpgradeSource(t, h)})
	require.NoError(t, err)
	setSchema(t, h, 12)

	path := filepath.Join(t.TempDir(), "bundle-1.2.0")
	copyBundle(t, testBundlePath(t), path)
	retargetManifest(t, path, h.Root)
	for name, opts := range map[string]ops.UpdateOptions{
		"from the store": {To: "1.2.0"},
		"from a path":    {Ref: path},
	} {
		_, err := ops.Update(ctx, h.Deps, opts)
		require.Error(t, err, name)
		assert.Equal(t, domain.ExitIncompatible, domain.ExitCode(err), "%s: %v", name, err)
		assert.Contains(t, err.Error(), "1.2.0 is older than the installed 1.3.0", name)
		assert.Contains(t, domain.AsError(err).Hint, "--allow-older", name)
		assert.Equal(t, "1.3.0", h.current(), name)
	}

	res, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{To: "1.2.0", AllowOlder: true})
	require.NoError(t, err)
	assert.Equal(t, "1.2.0", h.current())
	assert.Equal(t, "true", res.Record.Flags["allow_older"], "the journal does not say the floor was crossed")
}

// A channel that delivered 1.3.0 and now serves 1.2.5 is refused, though
// 1.2.5 is newer than what is installed; the tick installs nothing.
func TestAChannelIsRefusedAReleaseOlderThanOneItDelivered(t *testing.T) {
	h, registry, _, _ := followingHarnessWith(t, recoverableUpdate)
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	_, err := ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true})
	require.NoError(t, err)
	assert.Equal(t, "1.3.0", h.marks()[h.channel()].Version.String(), "a verified fetch raised no mark")

	h.tick(time.Hour)
	registry.republish(renumberedArchive(t, h, "1.2.5"))
	_, err = ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the channel points at 1.2.5, older than the 1.3.0 it already delivered")
	candidate, err := h.Deps.State.UpdateCandidate(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, candidate.Refused, "the refusal was not recorded, so every tick fetches again")

	armAutoApply(t, h)
	h.tick(time.Hour)
	fetched := registry.blobBytes.Load()
	res, err := ops.RunUnattended(ctx, h.Deps, ops.UnattendedOptions{})
	require.NoError(t, err, "a tick that finds the recorded refusal has nothing to report as a failure")
	assert.Equal(t, fetched, registry.blobBytes.Load(), "the tick fetched a bundle it had already refused")
	assert.False(t, res.Applied)
	assert.Equal(t, "1.2.0", h.current())
	assert.Equal(t, "1.3.0", h.marks()[h.channel()].Version.String())
}

// A peek and `update --check` see tags, never a verified bundle, so a registry
// advertising 9.9.9 raises nothing and blocks nothing (decision 12).
func TestAPeekAndACheckRaiseNoMark(t *testing.T) {
	h, registry, _, _ := followingHarness(t)
	registry.tags = []string{"1.2.0", "1.3.0", "9.9.9", "latest"}
	ctx := context.Background()

	_, err := ops.CheckForUpdate(ctx, h.Deps, ops.UpdateCheckOptions{Explicit: true})
	require.NoError(t, err)
	_, err = ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true, Options: ops.Options{DryRun: true}})
	require.NoError(t, err)
	assert.Empty(t, h.marks())
}

// Each channel is judged by what it delivered: following another starts from
// that channel's mark (decision 11).
func TestEachChannelIsJudgedByItsOwnMark(t *testing.T) {
	h, registry, srv, _ := followingHarnessWith(t, recoverableUpdate)
	ctx := context.Background()
	_, err := ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true})
	require.NoError(t, err)
	stable := h.channel()

	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Update.Channel = ociRef(srv, "demo/bundle", "lts").String()
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))
	registry.republish(renumberedArchive(t, h, "1.2.5"))

	res, err := ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true})
	require.NoError(t, err, "another channel was judged by stable's mark")
	assert.Equal(t, "1.2.5", res.Candidate.Version.String())
	marks := h.marks()
	assert.Equal(t, "1.3.0", marks[stable].Version.String())
	assert.Equal(t, "1.2.5", marks[h.channel()].Version.String())
}

// The mark is written before the candidate (decision 15): a crash between the
// two leaves a mark and no candidate, and the next poll stages the same
// release again, which the mark admits.
func TestAMarkIsRaisedBeforeTheCandidateIsRecorded(t *testing.T) {
	h, _, _, _ := followingHarnessWith(t, recoverableUpdate)
	ctx := context.Background()
	// The candidate's write fails, as a crash after the mark would leave
	// it unwritten.
	store := h.Deps.State
	h.Deps.State = candidateWriteFails{store}

	_, err := ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true})
	require.Error(t, err)
	assert.Equal(t, "1.3.0", h.marks()[h.channel()].Version.String(), "the mark was not written first")

	h.Deps.State = store
	res, err := ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true})
	require.NoError(t, err, "the next poll did not recover")
	assert.Equal(t, "1.3.0", res.Candidate.Version.String())
}

// candidateWriteFails is a state store whose candidate record cannot be
// written.
type candidateWriteFails struct{ ports.StateStore }

func (candidateWriteFails) SetUpdateCandidate(context.Context, domain.UpdateCandidate) error {
	return domain.InstallationError(nil, "the candidate cannot be written")
}

// Going back on purpose, by `rollback` or `update --allow-older`, lowers the
// followed channel's mark to where it went, so the follower takes the
// vendor's next patch instead of refusing it. See logs/version-floor.md.
func TestGoingBackOnPurposeLowersTheFollowedMark(t *testing.T) {
	for name, goBack := range map[string]func(ctx context.Context, d *ops.Deps) error{
		"rollback": func(ctx context.Context, d *ops.Deps) error {
			_, err := ops.Rollback(ctx, d, ops.RollbackOptions{})
			return err
		},
		"update --allow-older": func(ctx context.Context, d *ops.Deps) error {
			_, err := ops.Update(ctx, d, ops.UpdateOptions{To: "1.2.0", AllowOlder: true})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, registry, _, _ := followingHarnessWith(t, recoverableUpdate)
			h.setHookEnv()
			applyBaseline(t, h)
			ctx := context.Background()
			_, err := ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true})
			require.NoError(t, err)
			_, err = ops.Update(ctx, h.Deps, ops.UpdateOptions{To: "1.3.0"})
			require.NoError(t, err)
			setSchema(t, h, 12)

			// The vendor withdrew 1.3.0: the channel serves 1.2.5, and the
			// follower refuses it and records the refusal.
			h.tick(time.Hour)
			registry.republish(renumberedArchive(t, h, "1.2.5"))
			_, err = ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true})
			require.Error(t, err)

			h.tick(time.Hour)
			require.NoError(t, goBack(ctx, h.Deps))
			assert.Equal(t, "1.2.0", h.current())
			assert.Equal(t, "1.2.0", h.marks()[h.channel()].Version.String(), "the mark stayed above")

			// The channel has not moved since: the recorded refusal must not
			// keep the now admissible 1.2.5 out.
			res, err := ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true})
			require.NoError(t, err, "the follower refused the patch after going back")
			assert.Equal(t, "1.2.5", res.Candidate.Version.String())
		})
	}
}

// A resumed update keeps --allow-older from the journal: the interrupted run
// had already crossed the floor on the operator's word, and the resume must not
// refuse the release it is finishing.
func TestAResumedUpdateKeepsAllowOlder(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	_, err := ops.Update(context.Background(), h.Deps, ops.UpdateOptions{Ref: stageRecoverableUpgradeSource(t, h)})
	require.NoError(t, err)
	setSchema(t, h, 12)
	// Later than the update above: the harness clock is fixed, and two
	// operations in one instant order by the random half of their IDs.
	later := h.Deps.Now().Add(time.Minute)
	h.Deps.Now = func() time.Time { return later }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.Runtime.OnCall = func(method string) {
		if method == "Up" {
			cancel()
		}
	}
	first, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{To: "1.2.0", AllowOlder: true})
	require.Error(t, err)
	require.Equal(t, domain.StatusInterrupted, first.Record.Status, "%v", err)
	h.Runtime.OnCall = nil

	_, err = ops.Update(context.Background(), h.Deps, ops.UpdateOptions{
		To: "1.2.0", Options: ops.Options{Resume: true},
	})
	require.NoError(t, err, "the resume refused the release the interrupted update was installing")
	assert.Equal(t, "1.2.0", h.current())
}

// The override restored on --resume is for the release the interrupted run
// was installing, not for any other the resuming command names.
func TestAResumeKeepsAllowOlderOnlyForTheSameRelease(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	_, err := ops.Update(context.Background(), h.Deps, ops.UpdateOptions{Ref: stageRecoverableUpgradeSource(t, h)})
	require.NoError(t, err)
	setSchema(t, h, 12)
	later := h.Deps.Now().Add(time.Minute)
	h.Deps.Now = func() time.Time { return later }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.Runtime.OnCall = func(method string) {
		if method == "Up" {
			cancel()
		}
	}
	// An older release than 1.3.0 in the store, other than the one the
	// interrupted run was installing.
	_, err = ops.Update(ctx, h.Deps, ops.UpdateOptions{To: "1.2.0", AllowOlder: true})
	require.Error(t, err)
	h.Runtime.OnCall = nil

	other := filepath.Join(t.TempDir(), "bundle-1.1.0")
	copyBundle(t, testBundlePath(t), other)
	retargetManifest(t, other, h.Root)
	for file, pair := range map[string][2]string{"manifest.yaml": {"version: 1.2.0", "version: 1.1.0"}, "VERSION": {"1.2.0", "1.1.0"}} {
		path := filepath.Join(other, file)
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Contains(t, string(raw), pair[0])
		require.NoError(t, os.WriteFile(path, []byte(strings.Replace(string(raw), pair[0], pair[1], 1)), 0o644))
	}
	_, err = ops.Update(context.Background(), h.Deps, ops.UpdateOptions{
		Ref: other, Options: ops.Options{Resume: true},
	})
	require.Error(t, err, "the resume crossed the floor for a release nobody allowed")
	assert.Equal(t, domain.ExitIncompatible, domain.ExitCode(err), "%v", err)
}

// A channel switched while the update runs is the one whose mark the update
// lowers: the installation is read again when the operation finishes, under the
// lock, which is also what covers a switch made while it waited for it.
func TestGoingBackLowersTheChannelFollowedWhenItFinishes(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()
	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: stageRecoverableUpgradeSource(t, h)})
	require.NoError(t, err)
	setSchema(t, h, 12)

	const stable, lts = "oci://registry.example/demo/bundle:stable", "oci://registry.example/demo/bundle:lts"
	at := domain.NewTime(h.Deps.Now())
	raw, err := json.Marshal(domain.ChannelMarks{
		stable: {Version: domain.MustParseVersion("1.3.0"), At: at},
		lts:    {Version: domain.MustParseVersion("1.3.0"), At: at},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(h.Paths.ChannelMarksFile(), raw, 0o640))
	follow := func(channel string) {
		inst, err := h.Deps.State.LoadInstallation(ctx)
		require.NoError(t, err)
		inst.Update.Channel = channel
		require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))
	}
	follow(stable)
	h.Runtime.OnCall = func(method string) {
		if method == "Up" {
			follow(lts)
		}
	}

	_, err = ops.Update(ctx, h.Deps, ops.UpdateOptions{To: "1.2.0", AllowOlder: true})
	require.NoError(t, err)
	marks := h.marks()
	assert.Equal(t, "1.2.0", marks[lts].Version.String(), "the channel followed at the end kept its mark")
	assert.Equal(t, "1.3.0", marks[stable].Version.String(), "a channel no longer followed was lowered")
}

// A marks file this machine cannot read is not the channel's doing, so it is
// not recorded as a refusal of what the channel serves: once it is readable,
// the next poll stages the release.
func TestAnUnreadableMarksFileIsNotRecordedAsARefusal(t *testing.T) {
	h, _, _, _ := followingHarnessWith(t, recoverableUpdate)
	ctx := context.Background()
	require.NoError(t, os.MkdirAll(filepath.Dir(h.Paths.ChannelMarksFile()), 0o755))
	require.NoError(t, os.WriteFile(h.Paths.ChannelMarksFile(), []byte("{not json"), 0o640))

	_, err := ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true})
	require.Error(t, err)
	candidate, err := h.Deps.State.UpdateCandidate(ctx)
	require.NoError(t, err)
	assert.Empty(t, candidate.Refused, "a local fault was recorded as the channel's refusal")

	require.NoError(t, os.Remove(h.Paths.ChannelMarksFile()))
	res, err := ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true})
	require.NoError(t, err)
	assert.Equal(t, "1.3.0", res.Candidate.Version.String())
}

// A crash after a lowered mark is written leaves a refusal recorded against the
// old one; the poll looks again because the refusal predates the mark, so no
// second write has to happen for the follower to resume.
// Times are kept to the second, so a lowering in the same second as the
// refusal counts as after it.
func TestARefusalOlderThanItsChannelsMarkIsLookedAtAgain(t *testing.T) {
	for name, later := range map[string]time.Duration{"an hour later": time.Hour, "in the same second": 0} {
		t.Run(name, func(t *testing.T) {
			h, registry, _, _ := followingHarnessWith(t, recoverableUpdate)
			ctx := context.Background()
			_, err := ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true})
			require.NoError(t, err)
			h.tick(time.Hour)
			registry.republish(renumberedArchive(t, h, "1.2.5"))
			_, err = ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true})
			require.Error(t, err)

			// What a lowering leaves on disk, and nothing else.
			h.tick(later)
			raw, err := json.Marshal(domain.ChannelMarks{h.channel(): {
				Version: domain.MustParseVersion("1.2.0"), At: domain.NewTime(h.Deps.Now()),
			}})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(h.Paths.ChannelMarksFile(), raw, 0o640))

			res, err := ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true})
			require.NoError(t, err, "a refusal made against a mark since lowered kept the release out")
			assert.Equal(t, "1.2.5", res.Candidate.Version.String())
		})
	}
}

// A marks file that cannot be read is reported on the poll that needs it, and
// not hidden behind a recorded refusal as an unmoved channel.
func TestAnUnreadableMarksFileIsNotHiddenByARecordedRefusal(t *testing.T) {
	h, registry, _, _ := followingHarnessWith(t, recoverableUpdate)
	ctx := context.Background()
	_, err := ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true})
	require.NoError(t, err)
	h.tick(time.Hour)
	registry.republish(renumberedArchive(t, h, "1.2.5"))
	_, err = ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true})
	require.Error(t, err)

	require.NoError(t, os.WriteFile(h.Paths.ChannelMarksFile(), []byte("{not json"), 0o640))
	_, err = ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true})
	require.Error(t, err, "the poll reported an unmoved channel over a marks file it could not read")
	assert.Contains(t, err.Error(), "channel marks")
}
