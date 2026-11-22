package oci

import (
	"context"
	"errors"
	"fmt"
	"io"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// A channel's signed index (RFC 0037 §5.2) is an artifact of its own under the
// channel's tag plus ".index", carrying the index and its signature as two
// layers. These are the types a hand-made one must use too; the publishing
// guide passes them to `oras push`.
const (
	IndexArtifactType       = "application/vnd.morzer.channel.index.v1"
	IndexMediaType          = "application/vnd.morzer.channel.index.v1+json"
	IndexSignatureMediaType = "application/vnd.morzer.channel.index.v1.minisig"
)

// MaxIndexLayerSize bounds each layer of an index artifact. An index is a few
// hundred bytes, and a poll reads it on every tick.
const MaxIndexLayerSize = 64 << 10

// PublishedIndex is what the registry holds under a channel's index tag, not
// yet trusted: its signature and its fields are the caller's to check.
type PublishedIndex struct {
	Index     []byte
	Signature []byte

	// Problem says why the artifact under the tag is not an index, when it
	// is not one: no such layer, a layer over the bound, bytes that do not
	// match their digest. Index and Signature are then empty.
	Problem string
}

// FetchIndex reads the index published beside the channel ref names. found is
// false when the tag does not exist. An artifact that exists and is not a
// readable index is found, with its Problem set, so the caller decides what
// that means; err is a registry that cannot be reached.
func (s *Source) FetchIndex(ctx context.Context, ref ports.Ref) (PublishedIndex, bool, error) {
	reference, err := indexReference(ref)
	if err != nil {
		return PublishedIndex{}, false, err
	}
	repo, err := s.newRepository(reference)
	if err != nil {
		return PublishedIndex{}, false, err
	}

	// One manifest, checked against its digest, so both layers come from
	// the same artifact even if the tag moves meanwhile.
	var out PublishedIndex
	manifest, err := fetchManifest(ctx, repo, reference)
	switch {
	case errors.Is(err, domain.ErrReleaseNotFound):
		return out, false, nil
	case errors.Is(err, domain.ErrRuntime):
		return out, false, err
	case err != nil:
		out.Problem = domain.AsError(err).Message
		return out, true, nil
	}

	var indexLayer, sigLayer *ocispec.Descriptor
	for i, layer := range manifest.Layers {
		switch layer.MediaType {
		case IndexMediaType:
			indexLayer = &manifest.Layers[i]
		case IndexSignatureMediaType:
			sigLayer = &manifest.Layers[i]
		}
	}
	if indexLayer == nil || sigLayer == nil {
		out.Problem = fmt.Sprintf("%s has no %s layer and %s layer", reference, IndexMediaType, IndexSignatureMediaType)
		return out, true, nil
	}

	index, problem, err := readIndexLayer(ctx, repo, *indexLayer, reference)
	if err != nil || problem != "" {
		out.Problem = problem
		return out, true, err
	}
	sig, problem, err := readIndexLayer(ctx, repo, *sigLayer, reference)
	if err != nil || problem != "" {
		out.Problem = problem
		return out, true, err
	}
	out.Index, out.Signature = index, sig
	return out, true, nil
}

// readIndexLayer reads one small layer whole, checked against its digest.
// A problem is the artifact's; an error is the registry's.
func readIndexLayer(ctx context.Context, repo Registry, layer ocispec.Descriptor, reference string) ([]byte, string, error) {
	if layer.Size > MaxIndexLayerSize {
		return nil, fmt.Sprintf("a layer of %s declares %d bytes, over the limit of %d",
			reference, layer.Size, MaxIndexLayerSize), nil
	}
	body, err := repo.Fetch(ctx, layer)
	if err != nil {
		return nil, "", registryError(err, reference)
	}
	defer func() { _ = body.Close() }()

	// Checked here, as writeLayer checks a bundle: the client compares
	// headers and hands back the body as sent.
	verifier := content.NewVerifyReader(body, layer)
	data, err := io.ReadAll(io.LimitReader(verifier, MaxIndexLayerSize+1))
	if err != nil {
		return nil, "", domain.RuntimeError(err, "a layer of %s did not download completely", reference)
	}
	if len(data) > MaxIndexLayerSize {
		return nil, fmt.Sprintf("a layer of %s is over the limit of %d bytes", reference, MaxIndexLayerSize), nil
	}
	if err := verifier.Verify(); err != nil {
		return nil, fmt.Sprintf("a layer of %s does not match the digest its manifest gives it", reference), nil
	}
	return data, "", nil
}

// PushIndex publishes an index and its signature under the index tag of the
// channel ref names, replacing what the tag pointed at, and returns the
// artifact's manifest digest.
func (s *Source) PushIndex(ctx context.Context, ref ports.Ref, index, signature []byte) (string, error) {
	reference, err := indexReference(ref)
	if err != nil {
		return "", err
	}
	repo, err := remoteRepository(reference)
	if err != nil {
		return "", err
	}
	_, tag, _ := splitReference(reference)

	layers := make([]ocispec.Descriptor, 0, 2)
	for _, part := range []struct {
		mediaType, title string
		data             []byte
	}{
		{IndexMediaType, domain.ChannelIndexFile, index},
		{IndexSignatureMediaType, domain.ChannelIndexSignatureFile, signature},
	} {
		desc, err := oras.PushBytes(ctx, repo, part.mediaType, part.data)
		if err != nil {
			return "", registryError(err, reference)
		}
		// The title is what `oras pull` saves a layer under.
		desc.Annotations = map[string]string{ocispec.AnnotationTitle: part.title}
		layers = append(layers, desc)
	}
	manifest, err := oras.PackManifest(ctx, repo, oras.PackManifestVersion1_1, IndexArtifactType,
		oras.PackManifestOptions{Layers: layers})
	if err != nil {
		return "", registryError(err, reference)
	}
	if err := repo.Tag(ctx, manifest, tag); err != nil {
		return "", registryError(err, reference)
	}
	return manifest.Digest.String(), nil
}

// indexReference is the reference of the index beside the channel ref names:
// the same repository, under the channel's tag plus ".index".
func indexReference(ref ports.Ref) (string, error) {
	repository, channel, err := splitReference(ref.Location)
	if err != nil {
		return "", err
	}
	if channel == "" || ref.Location[len(repository)] == '@' {
		return "", domain.Usage("%s names no channel tag", ports.RedactRefCredentials(ref.String())).
			WithHint("an index sits beside a channel: pass oci://registry.example/demo/bundle:stable")
	}
	return repository + ":" + domain.ChannelIndexTag(channel), nil
}
