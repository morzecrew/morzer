package suite

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/registry/remote"

	"github.com/morzecrew/morzer/internal/adapters/source/local"
	"github.com/morzecrew/morzer/internal/adapters/source/oci"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
	"github.com/morzecrew/morzer/test/fakes"
)

// RFC 0039 §6: push against an in-process registry that can be written to.
// The refusals carry the weight here. A push that overwrote a published version
// with different content is the failure the command exists to prevent, and it
// is silent everywhere downstream -- the new bytes are internally consistent.

// pushRegistry serves an empty writable registry and returns the repository
// reference push takes, with no tag: push names the version itself.
func pushRegistry(t *testing.T) (*fakes.Registry, string) {
	t.Helper()
	reg := fakes.NewRegistry()
	srv := httptest.NewServer(reg)
	t.Cleanup(srv.Close)
	return reg, strings.TrimPrefix(srv.URL, "http://") + "/demo/bundle"
}

// archiveOf archives a bundle directory and returns the archive with the
// bundle's content digest. Different timestamps give different archive bytes
// for the same content, which is the case push must not mistake for a change.
func archiveOf(t *testing.T, dir string, at time.Time) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "demo-1.2.0.tar.zst")
	require.NoError(t, release.WriteArchive(dir, path, at))

	resolved, err := local.New().Resolve(context.Background(),
		ports.Ref{Scheme: local.Scheme, Location: path})
	require.NoError(t, err)
	return path, resolved.Digest
}

// changedBundle is the example bundle with different content and the same
// version.
func changedBundle(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "bundle")
	copyBundle(t, testBundlePath(t), dir)
	notes := filepath.Join(dir, "RELEASE.md")
	data, err := os.ReadFile(notes)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(notes, append(data, "\nA late edit.\n"...), 0o644))
	return dir
}

func pushOf(repo, archive, contentDigest string, channels ...string) oci.PushRequest {
	return oci.PushRequest{
		Archive:       archive,
		Reference:     repo,
		Version:       domain.MustParseVersion("1.2.0"),
		Product:       "demo",
		ContentDigest: contentDigest,
		Channels:      channels,
	}
}

func newPushSource(t *testing.T) *oci.Source {
	t.Helper()
	src := oci.New()
	t.Cleanup(func() { _ = src.Close() })
	return src
}

// publishByHand publishes an archive the way `oras push` does, with whatever
// manifest annotations the test supplies, and tags it.
func publishByHand(t *testing.T, repoRef, archive, tag string, annotations map[string]string) string {
	t.Helper()
	ctx := context.Background()

	repo, err := remote.NewRepository(repoRef)
	require.NoError(t, err)
	repo.PlainHTTP = true

	data, err := os.ReadFile(archive)
	require.NoError(t, err)
	layer := ocispec.Descriptor{
		MediaType:   oci.MediaType,
		Digest:      digest.FromBytes(data),
		Size:        int64(len(data)),
		Annotations: map[string]string{ocispec.AnnotationTitle: filepath.Base(archive)},
	}
	f, err := os.Open(archive)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	require.NoError(t, repo.Push(ctx, layer, f))

	manifest, err := oras.PackManifest(ctx, repo, oras.PackManifestVersion1_1, oci.ArtifactType,
		oras.PackManifestOptions{Layers: []ocispec.Descriptor{layer}, ManifestAnnotations: annotations})
	require.NoError(t, err)
	require.NoError(t, repo.Tag(ctx, manifest, tag))
	return manifest.Digest.String()
}

func TestPushPublishesWhatThePullSideReads(t *testing.T) {
	reg, repo := pushRegistry(t)
	archive, contentDigest := archiveOf(t, testBundlePath(t), time.Unix(0, 0))
	ctx := context.Background()

	res, err := newPushSource(t).Push(ctx, pushOf(repo, archive, contentDigest, "stable"))
	require.NoError(t, err)
	assert.True(t, res.Published)

	tagged, ok := reg.Tagged("1.2.0")
	require.True(t, ok, "the version tag was not written")
	assert.Equal(t, tagged, res.ManifestDigest)
	stable, _ := reg.Tagged("stable")
	assert.Equal(t, tagged, stable, "the channel points somewhere other than the version")
	require.Len(t, res.Channels, 1)
	assert.True(t, res.Channels[0].Moved)

	// The shape `oras push --artifact-type` makes (decision 5), with the
	// annotations a later push reads back (decision 6).
	m := reg.Manifest("1.2.0")
	assert.Equal(t, oci.ArtifactType, m.ArtifactType)
	require.Len(t, m.Layers, 1)
	assert.Equal(t, oci.MediaType, m.Layers[0].MediaType)
	assert.Equal(t, filepath.Base(archive), m.Layers[0].Annotations[ocispec.AnnotationTitle],
		"`oras pull` saves a layer only under its title")
	assert.Equal(t, "demo", m.Annotations[ocispec.AnnotationTitle])
	assert.Equal(t, "1.2.0", m.Annotations[ocispec.AnnotationVersion])
	assert.Equal(t, contentDigest, m.Annotations[oci.AnnotationContentDigest])

	// The round trip: what push wrote is what the pull side installs, and
	// what `update --check` offers.
	pull := newPushSource(t)
	resolved, err := pull.Resolve(ctx, ports.Ref{Scheme: oci.Scheme, Location: repo + ":1.2.0"})
	require.NoError(t, err)
	assert.Equal(t, contentDigest, resolved.Digest)

	versions, err := pull.List(ctx, ports.Ref{Scheme: oci.Scheme, Location: repo})
	require.NoError(t, err)
	require.Len(t, versions, 1, "a channel tag was listed as a version")
	assert.Equal(t, "1.2.0", versions[0].String())
}

// TestARerunConvergesAfterAFailedChannelMove is decision 3's reason for exit 0
// on identical content: the push landed, the channel did not, and the pipeline
// runs the same step again.
func TestARerunConvergesAfterAFailedChannelMove(t *testing.T) {
	reg, repo := pushRegistry(t)
	archive, contentDigest := archiveOf(t, testBundlePath(t), time.Unix(0, 0))
	ctx := context.Background()
	src := newPushSource(t)

	reg.FailTag = "edge"
	res, err := src.Push(ctx, pushOf(repo, archive, contentDigest, "stable", "edge", "beta"))
	require.Error(t, err, "a failed channel move must fail the command")
	require.Len(t, res.Channels, 3, "every requested channel is reported")
	assert.True(t, res.Channels[0].Moved)
	assert.False(t, res.Channels[1].Moved)
	assert.NotEmpty(t, res.Channels[1].Error)
	assert.False(t, res.Channels[2].Moved, "a failure stops the channels after it")
	assert.Empty(t, res.Channels[2].Error)
	_, moved := reg.Tagged("beta")
	assert.False(t, moved)

	reg.FailTag = ""
	writes, reads := reg.Writes(), reg.BlobReads()
	res, err = src.Push(ctx, pushOf(repo, archive, contentDigest, "stable", "edge", "beta"))
	require.NoError(t, err)
	assert.False(t, res.Published, "the version was already there")
	version, _ := reg.Tagged("1.2.0")
	assert.Equal(t, version, res.ManifestDigest)
	for _, ch := range []string{"stable", "edge", "beta"} {
		got, _ := reg.Tagged(ch)
		assert.Equal(t, version, got, ch)
	}
	assert.Equal(t, writes+3, reg.Writes(), "a re-run writes the channel tags and nothing else")
	// The same archive is the same layer digest, which the registry checked
	// when it was pushed: nothing needs downloading to recognise it.
	assert.Equal(t, reads, reg.BlobReads(), "the re-run downloaded the bundle it had just pushed")
}

// TestPushRecognisesTheSameBundleInAnotherArchive. The archive's bytes carry
// its timestamp, so the same bundle archived twice has two layer digests. An
// artifact pushed by hand carries no content-digest annotation either, so the
// layer is pulled and compared by content.
func TestPushRecognisesTheSameBundleInAnotherArchive(t *testing.T) {
	reg, repo := pushRegistry(t)
	first, _ := archiveOf(t, testBundlePath(t), time.Unix(0, 0))
	second, contentDigest := archiveOf(t, testBundlePath(t), time.Unix(3600, 0))
	published := publishByHand(t, repo, first, "1.2.0", nil)

	writes := reg.Writes()
	res, err := newPushSource(t).Push(context.Background(), pushOf(repo, second, contentDigest))
	require.NoError(t, err)
	assert.False(t, res.Published)
	assert.Equal(t, published, res.ManifestDigest)
	assert.Equal(t, writes, reg.Writes(), "the same content was published again")
}

func TestPushRefusesAVersionPublishedWithDifferentContent(t *testing.T) {
	archive := func(t *testing.T) (string, string) {
		return archiveOf(t, testBundlePath(t), time.Unix(0, 0))
	}

	cases := []struct {
		name string
		// publish puts a different 1.2.0 in the registry and returns the
		// manifest digest the tag must still point at afterwards.
		publish func(t *testing.T, repo, ours string) string
	}{
		{
			// Refused on the annotation alone, without pulling anything.
			name: "the annotation names another content digest",
			publish: func(t *testing.T, repo, ours string) string {
				other, _ := archiveOf(t, testBundlePath(t), time.Unix(7200, 0))
				return publishByHand(t, repo, other, "1.2.0",
					map[string]string{oci.AnnotationContentDigest: "sha256:" + strings.Repeat("0", 64)})
			},
		},
		{
			name: "an artifact with no annotation and different content",
			publish: func(t *testing.T, repo, ours string) string {
				other, _ := archiveOf(t, changedBundle(t), time.Unix(0, 0))
				return publishByHand(t, repo, other, "1.2.0", nil)
			},
		},
		{
			// The annotation is a hint anyone who can push may write. It
			// decides only in the refusing direction (decision 6).
			name: "an annotation that matches while the content differs",
			publish: func(t *testing.T, repo, ours string) string {
				other, _ := archiveOf(t, changedBundle(t), time.Unix(0, 0))
				return publishByHand(t, repo, other, "1.2.0",
					map[string]string{oci.AnnotationContentDigest: ours})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg, repo := pushRegistry(t)
			ours, contentDigest := archive(t)
			published := tc.publish(t, repo, contentDigest)

			writes := reg.Writes()
			res, err := newPushSource(t).Push(context.Background(),
				pushOf(repo, ours, contentDigest, "stable"))
			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrValidation)
			assert.Contains(t, err.Error(), "different content")
			assert.Equal(t, domain.ExitUsage, domain.ExitCode(err))

			assert.False(t, res.Published)
			assert.Equal(t, writes, reg.Writes(), "a refused push wrote to the registry")
			tagged, _ := reg.Tagged("1.2.0")
			assert.Equal(t, published, tagged)
			_, moved := reg.Tagged("stable")
			assert.False(t, moved, "a refused push moved a channel")
		})
	}
}

// TestPushWarnsWhenAChannelMovesBackwards, and moves it anyway (decision 4):
// machines already refuse to follow a channel backwards, and a new installation
// taking the older version may be what the vendor wants.
func TestPushWarnsWhenAChannelMovesBackwards(t *testing.T) {
	reg, repo := pushRegistry(t)
	newer, _ := archiveOf(t, changedBundle(t), time.Unix(0, 0))
	publishByHand(t, repo, newer, "stable",
		map[string]string{ocispec.AnnotationVersion: "1.3.0"})
	publishByHand(t, repo, newer, "edge", nil)

	archive, contentDigest := archiveOf(t, testBundlePath(t), time.Unix(0, 0))
	res, err := newPushSource(t).Push(context.Background(),
		pushOf(repo, archive, contentDigest, "stable", "edge"))
	require.NoError(t, err)

	require.Len(t, res.Warnings, 1, "only the channel whose version is known can be warned about")
	assert.Contains(t, res.Warnings[0], "stable")
	assert.Contains(t, res.Warnings[0], "1.3.0")
	version, _ := reg.Tagged("1.2.0")
	stable, _ := reg.Tagged("stable")
	assert.Equal(t, version, stable, "a warning is not a refusal")
}

func TestPushDryRunWritesNothing(t *testing.T) {
	reg, repo := pushRegistry(t)
	archive, contentDigest := archiveOf(t, testBundlePath(t), time.Unix(0, 0))
	req := pushOf(repo, archive, contentDigest, "stable")
	req.DryRun = true

	res, err := newPushSource(t).Push(context.Background(), req)
	require.NoError(t, err)
	assert.Zero(t, reg.Writes(), "a dry run wrote to the registry")
	assert.True(t, res.Published, "a dry run reports what the push would do")
	assert.Empty(t, res.ManifestDigest, "no manifest exists to name")
	require.Len(t, res.Channels, 1)
	assert.True(t, res.Channels[0].Moved)

	// And it refuses what the push would refuse.
	reg2, repo2 := pushRegistry(t)
	other, _ := archiveOf(t, changedBundle(t), time.Unix(0, 0))
	publishByHand(t, repo2, other, "1.2.0", nil)
	writes := reg2.Writes()
	req = pushOf(repo2, archive, contentDigest)
	req.DryRun = true
	_, err = newPushSource(t).Push(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "different content")
	assert.Equal(t, writes, reg2.Writes())
}

// TestPushMovesNoChannelWhenTheVersionTagMoved: another pipeline's tag write
// landed after this one's, so the version no longer names what this push
// wrote, and a channel moved now would point at somebody else's bundle.
func TestPushMovesNoChannelWhenTheVersionTagMoved(t *testing.T) {
	reg, repo := pushRegistry(t)
	other, _ := archiveOf(t, changedBundle(t), time.Unix(0, 0))
	decoy := publishByHand(t, repo, other, "decoy", nil)
	reg.Retag, reg.Decoy = "1.2.0", decoy

	archive, contentDigest := archiveOf(t, testBundlePath(t), time.Unix(0, 0))
	_, err := newPushSource(t).Push(context.Background(),
		pushOf(repo, archive, contentDigest, "stable"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), decoy)
	assert.Equal(t, domain.ExitUsage, domain.ExitCode(err))
	_, moved := reg.Tagged("stable")
	assert.False(t, moved)
}

// TestPushRefusesWhatItCannotPublishToBeforeAnyRequest: the reference and the
// channel names are the vendor's own arguments, and a mistake in one is
// reported as such rather than after a round trip to the registry.
func TestPushRefusesWhatItCannotPublishToBeforeAnyRequest(t *testing.T) {
	archive, contentDigest := archiveOf(t, testBundlePath(t), time.Unix(0, 0))

	cases := []struct {
		name     string
		mutate   func(req *oci.PushRequest)
		mentions string
	}{
		{
			// Decision 2: List reads tags as versions, so a tag that is not
			// the bundle's version is `update --check` offering a version
			// the bundle does not carry.
			name:     "a tag other than the archive's version",
			mutate:   func(req *oci.PushRequest) { req.Reference += ":1.3.0" },
			mentions: "1.3.0",
		},
		{
			name:     "a digest reference",
			mutate:   func(req *oci.PushRequest) { req.Reference += "@sha256:" + strings.Repeat("a", 64) },
			mentions: "digest",
		},
		{
			// The same hazard through the channel: a channel named like
			// a version is listed as one.
			name:     "a channel named like a version",
			mutate:   func(req *oci.PushRequest) { req.Channels = []string{"stable", "1.4.0"} },
			mentions: "1.4.0",
		},
		{
			name:     "a channel that is not a tag",
			mutate:   func(req *oci.PushRequest) { req.Channels = []string{"stable/edge"} },
			mentions: "stable/edge",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg, repo := pushRegistry(t)
			req := pushOf(repo, archive, contentDigest)
			tc.mutate(&req)

			_, err := newPushSource(t).Push(context.Background(), req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.mentions)
			assert.Equal(t, domain.ExitUsage, domain.ExitCode(err))
			assert.Zero(t, reg.Requests(), "refused after asking the registry")
		})
	}

	// And the tag that does match is accepted.
	reg, repo := pushRegistry(t)
	_, err := newPushSource(t).Push(context.Background(), pushOf(repo+":1.2.0", archive, contentDigest))
	require.NoError(t, err)
	_, ok := reg.Tagged("1.2.0")
	assert.True(t, ok)
}
