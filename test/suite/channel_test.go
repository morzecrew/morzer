package suite

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/source"
	"github.com/morzecrew/morzer/internal/adapters/source/local"
	"github.com/morzecrew/morzer/internal/adapters/source/oci"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// A channel is a mutable tag polled on a timer, so the two things these tests
// are about are the cost of a tick and what a tick is allowed to change.

// recordingNotifier keeps what left the machine.
type recordingNotifier struct {
	mu     sync.Mutex
	events []events.Event
}

func (n *recordingNotifier) Name() string { return "recording" }

func (n *recordingNotifier) Notify(_ context.Context, ev events.Event) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.events = append(n.events, ev)
	return nil
}

func (n *recordingNotifier) kinds() []events.Kind {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]events.Kind, 0, len(n.events))
	for _, ev := range n.events {
		out = append(out, ev.Kind)
	}
	return out
}

// followingHarness is an installed 1.2.0 machine watching a registry that
// publishes 1.3.0 under a moving tag.
func followingHarness(t *testing.T) (*harness, *fakeRegistry, *httptest.Server, *recordingNotifier) {
	t.Helper()
	return followingHarnessWith(t, nil)
}

// followingHarnessWith lets a test change the published bundle before it is
// packed, which is the only way to vary what the *vendor* declared -- the
// manifest is inside the archive the registry serves, so patching it afterwards
// would change a digest the manager verifies.
func followingHarnessWith(
	t *testing.T, patch func(t *testing.T, bundleDir string),
) (*harness, *fakeRegistry, *httptest.Server, *recordingNotifier) {
	t.Helper()

	h := newHarness(t)
	h.install()

	// The bundle behind the tag is the 1.3.0 fixture, retargeted the way
	// every other update test retargets it -- so what the channel delivers
	// is a release this machine could really install.
	staged := filepath.Join(t.TempDir(), "bundle-1.3.0")
	copyBundle(t, filepath.Join(testBundlePath(t), "..", "bundle-1.3.0"), staged)
	retargetManifest(t, staged, h.Root)
	if patch != nil {
		patch(t, staged)
	}

	archivePath := filepath.Join(t.TempDir(), "bundle.tar.zst")
	writeTarZst(t, staged, archivePath)
	archive, err := os.ReadFile(archivePath)
	require.NoError(t, err)

	srv := newFakeRegistry(t, archive, oci.MediaType, 0)
	registry, ok := srv.Config.Handler.(*fakeRegistry)
	require.True(t, ok)

	// The registry rather than the OCI source alone, exactly as production
	// wires it: `update --to` installs from the store by path, so a harness
	// holding only the registry transport would fail on the scheme rather
	// than on anything this is testing.
	sources, err := source.NewRegistry(local.New(), newTestSource(t, srv))
	require.NoError(t, err)
	h.Deps.Source = sources

	notifier := &recordingNotifier{}
	h.Deps.Notifier = notifier

	inst, err := h.Deps.State.LoadInstallation(context.Background())
	require.NoError(t, err)
	inst.Update.Check = true
	inst.Update.Channel = ociRef(srv, "demo/bundle", "stable").String()
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	return h, registry, srv, notifier
}

// TestAnUnmovedChannelStagesNothingAndDownloadsNothing.
//
// This is the assertion that makes a five-minute cadence affordable, and it has
// to be a byte count. A poll that returned "nothing new" after downloading the
// whole release would pass any test written about its *answer*, and would move
// a 200 MB bundle 288 times a day to keep saying it.
func TestAnUnmovedChannelStagesNothingAndDownloadsNothing(t *testing.T) {
	h, registry, _, notifier := followingHarness(t)
	ctx := context.Background()

	// The first poll moves the release, which is the point of the second.
	first, err := ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true})
	require.NoError(t, err)
	require.True(t, first.Moved)
	require.True(t, first.Candidate.IsStaged())

	fetched := registry.blobBytes.Load()
	require.NotZero(t, fetched, "the first poll must have fetched the bundle")

	second, err := ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true})
	require.NoError(t, err)

	assert.False(t, second.Moved, "the tag has not moved, so there is nothing to stage")
	assert.Equal(t, fetched, registry.blobBytes.Load(),
		"a poll of an unmoved channel transferred %d more bytes; a tick must cost a manifest",
		registry.blobBytes.Load()-fetched)

	// And nobody was told twice about the same release.
	assert.Len(t, notifier.kinds(), 1)
}

// TestAMovedChannelStagesTheReleaseWithoutInstallingIt.
//
// Staging is the middle state the whole design is built around: the network, the
// credentials and the verification happen ahead of time, and the human decides
// only when downtime happens.
func TestAMovedChannelStagesTheReleaseWithoutInstallingIt(t *testing.T) {
	h, _, _, notifier := followingHarness(t)
	ctx := context.Background()

	result, err := ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true})
	require.NoError(t, err)

	require.True(t, result.Candidate.IsStaged())
	assert.Equal(t, "1.3.0", result.Candidate.Version.String())
	assert.NotEmpty(t, result.Candidate.UpstreamDigest)

	// In the store, verified, and readable as a release.
	assert.DirExists(t, filepath.Join(h.Paths.ReleasesDir(), "1.3.0"))

	// And *not* installed. A poll that converged a deployment would be
	// unattended apply, which is a separate decision with downtime attached.
	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.2.0", current.Version.String(),
		"following a channel installed the release; staging must stop short of that")

	// The record survives the process, because the timer is a new process
	// every tick.
	stored, err := h.Deps.State.UpdateCandidate(ctx)
	require.NoError(t, err)
	assert.Equal(t, result.Candidate.UpstreamDigest, stored.UpstreamDigest)

	// Somebody was told, because the decision is waiting for a person who is
	// not at the terminal where the timer ran.
	assert.Contains(t, notifier.kinds(), events.KindUpdateStaged)

	// The notes ship with the bundle and are what the operator is deciding
	// on. RFC 0002 P5's gate, finally open.
	assert.NotEmpty(t, result.Notes,
		"the staged release ships RELEASE.md; nothing read it")
}

// theCommandAVendorWrote is one line, longer than any measure this project
// holds prose to, inside a fenced block.
const theCommandAVendorWrote = "morzer apply --installation demo --profile production " +
	"--release 1.3.0 --wait-for-health --timeout 15m"

// TestStagedNotesArriveAsTheVendorWroteThem.
//
// Release notes are a vendor's Markdown, and nothing between the bundle and the
// operator reflows them. A wrap inserted at a column splits the fenced command
// below, and the operator who copies it gets something that does not run --
// which is what a word wrapper applied to Markdown did, before it was removed
// in favour of not transforming the notes at all.
//
// The assertion is the whole line in one Contains. Asserting on the words would
// pass against output that had broken the line between them.
func TestStagedNotesArriveAsTheVendorWroteThem(t *testing.T) {
	notes := "# demo 1.3.0\n\nRun this afterwards:\n\n```sh\n" +
		theCommandAVendorWrote + "\n```\n"

	h, registry, _, _ := followingHarnessWith(t, func(t *testing.T, bundleDir string) {
		t.Helper()
		require.NoError(t, os.WriteFile(
			filepath.Join(bundleDir, "RELEASE.md"), []byte(notes), 0o644))
	})

	// The tag this harness publishes is the one the installation already
	// follows, so the poll needs no explicit reference.
	_ = registry
	result, err := ops.FollowChannel(context.Background(), h.Deps,
		ops.FollowChannelOptions{Explicit: true})
	require.NoError(t, err)

	assert.Contains(t, result.Notes, theCommandAVendorWrote,
		"the fenced command reached the operator broken across lines")
	assert.Equal(t, strings.TrimSpace(notes), strings.TrimSpace(result.Notes),
		"something between the bundle and the operator rewrote the notes")
}

// TestInstallingAStagedReleaseRetiresTheCandidate.
//
// Two halves. The candidate is forgotten, so `status` stops offering a release
// that is now running -- and the upstream digest moves onto the release record,
// which is what stops the next poll re-fetching the bundle it has just
// installed.
func TestInstallingAStagedReleaseRetiresTheCandidate(t *testing.T) {
	h, registry, _, _ := followingHarness(t)
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	staged, err := ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true})
	require.NoError(t, err)
	require.True(t, staged.Candidate.IsStaged())

	_, err = ops.Update(ctx, h.Deps, ops.UpdateOptions{To: "1.3.0"})
	require.NoError(t, err)

	candidate, err := h.Deps.State.UpdateCandidate(ctx)
	require.NoError(t, err)
	assert.True(t, candidate.IsZero(),
		"the candidate survived being installed, so `status` still offers it")

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, staged.Candidate.UpstreamDigest, current.UpstreamDigest,
		"the release did not adopt the channel's digest, so the next poll re-fetches it")

	// Measured, because that is the claim: the tick after an install is free.
	before := registry.blobBytes.Load()
	after, err := ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true})
	require.NoError(t, err)
	assert.False(t, after.Moved)
	assert.Equal(t, before, registry.blobBytes.Load(),
		"the poll after an install downloaded the release again")
}

// TestARefusedCandidateIsNotFetchedTwice.
//
// A channel that moves to something unusable is not hypothetical -- a vendor
// republishing a tag, or moving it backwards, produces one. The refusal has to
// be *recorded*, or every tick re-downloads the same bundle to reach the same
// answer, forever.
func TestARefusedCandidateIsNotFetchedTwice(t *testing.T) {
	h, registry, _, _ := followingHarness(t)
	ctx := context.Background()

	// The machine is already running 1.4.0, so the channel's 1.3.0 is a
	// downgrade: `update` refuses a backwards move, so staging one would
	// only produce a candidate nobody can install.
	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	current.Version = domain.MustParseVersion("1.4.0")
	require.NoError(t, h.Deps.State.SetCurrentRelease(ctx, current))

	_, err = ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true})
	require.Error(t, err)

	candidate, err := h.Deps.State.UpdateCandidate(ctx)
	require.NoError(t, err)
	require.False(t, candidate.IsStaged())
	assert.NotEmpty(t, candidate.Refused, "the refusal was not recorded")

	before := registry.blobBytes.Load()
	_, err = ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true})
	require.NoError(t, err, "a second poll of an unchanged tag has nothing to refuse")
	assert.Equal(t, before, registry.blobBytes.Load(),
		"the refused bundle was downloaded a second time")
}

// TestAnUnpromptedPollNeedsPermission.
//
// The same rule `update --check` follows: a poll contacts the vendor's registry,
// which for a self-hosted product is a phone-home nobody agreed to. A timer
// leaves Explicit false and is refused until the operator turns checking on.
func TestAnUnpromptedPollNeedsPermission(t *testing.T) {
	h, registry, _, _ := followingHarness(t)
	ctx := context.Background()

	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Update.Check = false
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

	before := registry.manifestBytes.Load()

	_, err = ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{})
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrUnsupported)

	assert.Equal(t, before, registry.manifestBytes.Load(),
		"the refusal still contacted the registry, which is the whole thing it refuses")

	// And the operator's own command is its own authorisation.
	_, err = ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true})
	require.NoError(t, err)
}

// TestFollowingNothingIsARefusalThatNamesTheSetting.
func TestFollowingNothingIsARefusalThatNamesTheSetting(t *testing.T) {
	h := newHarness(t)
	h.install()

	_, err := ops.FollowChannel(context.Background(), h.Deps,
		ops.FollowChannelOptions{Explicit: true})
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Hint, "update.channel")
}

// TestAnApplyDoesNotEraseARecordedRefusal.
//
// Found by re-reading the retirement rule against a state it was not written
// for. A refusal carries no version, so "clear anything not newer than what just
// became current" swept it away on *every* `apply` -- and the next poll then
// re-downloaded the bundle it had already judged unusable, forever. The refusal
// record exists precisely to stop that.
func TestAnApplyDoesNotEraseARecordedRefusal(t *testing.T) {
	h, registry, _, _ := followingHarness(t)
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	// Make the channel offer a downgrade, which is refused and recorded.
	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	current.Version = domain.MustParseVersion("1.4.0")
	require.NoError(t, h.Deps.State.SetCurrentRelease(ctx, current))

	_, err = ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true})
	require.Error(t, err)
	refused, err := h.Deps.State.UpdateCandidate(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, refused.Refused)

	// An ordinary converge, which touches nothing about the channel.
	_, err = ops.Apply(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)

	kept, err := h.Deps.State.UpdateCandidate(ctx)
	require.NoError(t, err)
	assert.Equal(t, refused.UpstreamDigest, kept.UpstreamDigest,
		"the apply forgot which artefact had been refused")

	before := registry.blobBytes.Load()
	_, err = ops.FollowChannel(ctx, h.Deps, ops.FollowChannelOptions{Explicit: true})
	require.NoError(t, err)
	assert.Equal(t, before, registry.blobBytes.Load(),
		"the refused bundle was downloaded again after an apply")
}

// TestAPlanStagesNothing.
//
// Staging writes a bundle into the release store and a record the next tick
// reads. Both are changes to the machine, so a `--dry-run` that made them would
// be the one command in this program that lies about what it does — and the
// operator asking for a plan is often the one deciding whether to allow a
// download at all.
func TestAPlanStagesNothing(t *testing.T) {
	h, registry, _, notifier := followingHarness(t)
	ctx := context.Background()

	res, err := ops.FollowChannel(ctx, h.Deps,
		ops.FollowChannelOptions{Options: ops.Options{DryRun: true}, Explicit: true})
	require.NoError(t, err)

	// It still says what it found. A plan that reported nothing would be
	// useless, and the peek costs a manifest whether or not anything follows.
	assert.True(t, res.Moved)
	assert.Contains(t, res.Summary(), "would")

	assert.Zero(t, registry.blobBytes.Load(),
		"a plan downloaded the bundle it was planning to download")

	candidate, err := h.Deps.State.UpdateCandidate(ctx)
	require.NoError(t, err)
	assert.True(t, candidate.IsZero(), "a plan left a candidate record behind")
	assert.Empty(t, notifier.kinds(), "a plan told somebody a release was staged")
}

// TestAnUnreadableInstallationCannotSoftenTheSignaturePolicy.
//
// The fetch loaded the installation for its signature policy and ignored the
// error, so a state file that could not be parsed left `Expectation` zeroed:
// signatures not required, no pinned keys, and a bundle admitted on its content
// digest alone -- which any registry that served those bytes can satisfy.
//
// Absence is the one policy-free case, and it is asked about separately. This
// machine has a policy; what it does not have is a readable copy of it.
//
// What is asserted here is the refusal and that nothing was written, not that a
// signature was checked: the suite wires the checksum verifier, which answers
// only "is this the artifact I was told to expect". That the policy reaches the
// verifier at all is asserted separately, against an expectation this one cannot
// see.
func TestAnUnreadableInstallationCannotSoftenTheSignaturePolicy(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	inst := h.install()
	inst.Policy.RequireSignature = true
	inst.Policy.SigningKeys = []string{"RWQfakekey"}
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

	ref, err := ports.ParseRef(filepath.Join(testBundlePath(t), "..", "bundle-1.3.0"))
	require.NoError(t, err)
	resolved, err := h.Deps.Source.Resolve(ctx, ref)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(h.Paths.InstallationState(),
		[]byte("{\"schema_version\": 5, \"product\":\n"), 0o640))

	_, _, err = ops.FetchIntoStore(ctx, h.Deps, ref, resolved)
	require.Error(t, err,
		"a machine that cannot read its own policy verified the bundle anyway")

	// The refusal is about the state rather than about the signature: the
	// manager cannot say the bundle failed a policy it was unable to read.
	assert.Contains(t, err.Error(), h.Paths.InstallationState())

	// And nothing was left behind for `update --to` to find.
	_, err = os.Stat(h.Paths.ReleaseDir(resolved.Version.String()))
	assert.True(t, os.IsNotExist(err), "the unverified bundle stayed in the release store")
}

// checkingVerifier records the expectation it was handed and can refuse.
//
// The suite wires the checksum verifier, which answers "is this the artifact I
// was told to expect" and deliberately says nothing about signatures -- the
// chain verifier answers that. So a test about *policy reaching the verifier*
// has to observe the expectation rather than the outcome, or it would pass on an
// adapter that never looks at the field.
type checkingVerifier struct {
	inner ports.Verifier
	seen  []ports.Expectation
	err   error
}

func (v *checkingVerifier) Name() string { return "checking" }

func (v *checkingVerifier) Verify(
	ctx context.Context, bundle ports.BundlePath, expect ports.Expectation,
) error {
	v.seen = append(v.seen, expect)
	if v.err != nil {
		return v.err
	}
	return v.inner.Verify(ctx, bundle, expect)
}

// TestAStoredBundleIsCheckedAgainstTodaysPolicy.
//
// The store is not a decision that was made once. A bundle can arrive before an
// installation exists, or before `policy.require_signature` is turned on, and
// the early return for "this version is already here" compared digests and
// nothing else -- so the machine reported it as present and usable under a
// policy it would not pass.
//
// `update` would still refuse it at verify-bundle, which is what keeps this out
// of the "an unsigned release gets installed" class. What it produces instead is
// a machine that stages the release, offers it in `status`, prints its notes for
// `update --check`, and then aborts the update the operator was told to run.
//
// The release stays in the store. It predates the rule that now rejects it, and
// removing an operator's bundle because a policy tightened is their call.
func TestAStoredBundleIsCheckedAgainstTodaysPolicy(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	h.install()

	verifier := &checkingVerifier{inner: h.Deps.Verifier}
	h.Deps.Verifier = verifier

	ref, err := ports.ParseRef(filepath.Join(testBundlePath(t), "..", "bundle-1.3.0"))
	require.NoError(t, err)
	resolved, err := h.Deps.Source.Resolve(ctx, ref)
	require.NoError(t, err)

	// Fetched under no signature policy, which is this machine's state
	// today.
	_, present, err := ops.FetchIntoStore(ctx, h.Deps, ref, resolved)
	require.NoError(t, err)
	require.False(t, present)
	require.Len(t, verifier.seen, 1)
	require.False(t, verifier.seen[0].Required)

	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Policy.RequireSignature = true
	inst.Policy.SigningKeys = []string{"RWQfakekey"}
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

	// The second fetch finds it present. It must still be put to the
	// verifier, and with the policy in force now rather than the one that
	// was in force when it arrived.
	_, present, err = ops.FetchIntoStore(ctx, h.Deps, ref, resolved)
	require.NoError(t, err)
	assert.True(t, present)
	require.Len(t, verifier.seen, 2,
		"a bundle already in the store was returned without being verified at all")
	assert.True(t, verifier.seen[1].Required,
		"the stored bundle was checked against a policy this machine no longer has")
	assert.Equal(t, inst.Policy.SigningKeys, verifier.seen[1].PublicKeys)

	// And a refusal is a refusal: the release is reported as unusable and
	// left where it is.
	verifier.err = domain.ValidationError(nil, "no acceptable signature")
	_, _, err = ops.FetchIntoStore(ctx, h.Deps, ref, resolved)
	require.Error(t, err)
	assert.DirExists(t, h.Paths.ReleaseDir(resolved.Version.String()),
		"the refusal deleted a release the operator fetched under the old policy")
}
