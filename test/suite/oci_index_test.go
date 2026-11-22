package suite

import (
	"bytes"
	"context"
	"testing"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/registry/remote"

	"github.com/morzecrew/morzer/internal/adapters/source/oci"
	"github.com/morzecrew/morzer/internal/ports"
)

// RFC 0037 §5.2: a channel's index is an artifact of its own under the
// channel's tag plus ".index". What is here is the artifact: that what is
// pushed reads back byte for byte, and that anything else under the tag is
// reported as found and unreadable rather than as no index.

func channelRef(repo string) ports.Ref {
	return ports.Ref{Scheme: oci.Scheme, Location: repo + ":stable"}
}

func TestAnIndexReadsBackAsPushed(t *testing.T) {
	reg, repo := pushRegistry(t)
	src := newPushSource(t)
	ctx := context.Background()

	_, found, err := src.FetchIndex(ctx, channelRef(repo))
	require.NoError(t, err)
	assert.False(t, found, "a channel with no index tag has no index")

	index, sig := []byte(`{"schema":1}`+"\n"), []byte("untrusted comment: x\nsig\n")
	digest, err := src.PushIndex(ctx, channelRef(repo), index, sig)
	require.NoError(t, err)
	tagged, ok := reg.Tagged("stable.index")
	require.True(t, ok, "the index is not under stable.index")
	assert.Equal(t, digest, tagged)

	// The layers by the media types and titles a hand-made index uses too.
	manifest := reg.Manifest("stable.index")
	assert.Equal(t, oci.IndexArtifactType, manifest.ArtifactType)
	require.Len(t, manifest.Layers, 2)
	assert.Equal(t, oci.IndexMediaType, manifest.Layers[0].MediaType)
	assert.Equal(t, "index.json", manifest.Layers[0].Annotations[ocispec.AnnotationTitle])
	assert.Equal(t, oci.IndexSignatureMediaType, manifest.Layers[1].MediaType)
	assert.Equal(t, "index.json.minisig", manifest.Layers[1].Annotations[ocispec.AnnotationTitle])

	got, found, err := src.FetchIndex(ctx, channelRef(repo))
	require.NoError(t, err)
	require.True(t, found)
	assert.Empty(t, got.Problem)
	assert.Equal(t, index, got.Index)
	assert.Equal(t, sig, got.Signature)
}

// Something under the index tag that is not an index is found, with the
// reason: the caller decides what an unreadable index means, and "no index"
// would let a vendor's mistake, or anyone who can push, switch the check off.
func TestAnArtifactThatIsNotAnIndexIsFoundAndUnreadable(t *testing.T) {
	t.Run("a bundle under the index tag", func(t *testing.T) {
		_, repo := pushRegistry(t)
		archive, _ := archiveOf(t, testBundlePath(t), time.Unix(0, 0))
		publishByHand(t, repo, archive, "stable.index", nil)

		got, found, err := newPushSource(t).FetchIndex(context.Background(), channelRef(repo))
		require.NoError(t, err)
		require.True(t, found)
		assert.Contains(t, got.Problem, "has no "+oci.IndexMediaType)
		assert.Empty(t, got.Index)
	})

	t.Run("a layer over the bound", func(t *testing.T) {
		reg, repo := pushRegistry(t)
		pushIndexLayers(t, repo, bytes.Repeat([]byte("x"), oci.MaxIndexLayerSize+1), []byte("sig"))
		before := reg.BlobReads()

		got, found, err := newPushSource(t).FetchIndex(context.Background(), channelRef(repo))
		require.NoError(t, err)
		require.True(t, found)
		assert.Contains(t, got.Problem, "over the limit")
		// Refused by its declared size, so nothing was downloaded.
		assert.Equal(t, before, reg.BlobReads())
	})
}

// pushIndexLayers publishes an index artifact by hand, as `oras push` would,
// with whatever bytes the test supplies.
func pushIndexLayers(t *testing.T, repoRef string, index, sig []byte) {
	t.Helper()
	ctx := context.Background()
	repo, err := remote.NewRepository(repoRef)
	require.NoError(t, err)
	repo.PlainHTTP = true

	var layers []ocispec.Descriptor
	for _, part := range []struct {
		mediaType string
		data      []byte
	}{{oci.IndexMediaType, index}, {oci.IndexSignatureMediaType, sig}} {
		desc, err := oras.PushBytes(ctx, repo, part.mediaType, part.data)
		require.NoError(t, err)
		layers = append(layers, desc)
	}
	manifest, err := oras.PackManifest(ctx, repo, oras.PackManifestVersion1_1, oci.IndexArtifactType,
		oras.PackManifestOptions{Layers: layers})
	require.NoError(t, err)
	require.NoError(t, repo.Tag(ctx, manifest, "stable.index"))
}
