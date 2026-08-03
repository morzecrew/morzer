package contract

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

// ReleaseSourceFactory builds a source and a reference that points at the
// bundle in bundleDir, expressed the way this source expects it.
//
// The indirection is the whole design of the suite: every adapter is handed the
// *same* bundle and must produce the *same* answers about it, whatever shape it
// carries the bytes in. An adapter that packed, transferred or unpacked
// differently enough to change the digest would fail here rather than during an
// update on a machine where the recorded digest no longer matches.
type ReleaseSourceFactory func(t *testing.T, bundleDir string) (ports.ReleaseSource, ports.Ref)

// RunReleaseSourceSuite runs every ReleaseSource contract test.
//
// bundleDir must be a valid release bundle; the suite never modifies it.
func RunReleaseSourceSuite(t *testing.T, bundleDir string, newSource ReleaseSourceFactory) {
	t.Helper()

	// The reference answer. Every adapter's Resolve and Fetch must agree
	// with it, because this is the digest an operator records and later
	// pins with --digest.
	wantDigest, err := atomicfs.DigestTree(bundleDir)
	require.NoError(t, err)

	wantRelease, err := release.Load(bundleDir)
	require.NoError(t, err)

	t.Run("declares at least one scheme", func(t *testing.T) {
		source, _ := newSource(t, bundleDir)
		schemes := source.Schemes()
		require.NotEmpty(t, schemes,
			"a source that declares no scheme can never be selected by a registry")
		for _, s := range schemes {
			assert.NotEmpty(t, s, "an empty scheme string would match a malformed reference")
		}
	})

	t.Run("resolve reports the version and digest without altering the bundle", func(t *testing.T) {
		source, ref := newSource(t, bundleDir)

		resolved, err := source.Resolve(context.Background(), ref)
		require.NoError(t, err)

		assert.Equal(t, wantRelease.Version(), resolved.Version)
		assert.True(t, atomicfs.SameDigest(wantDigest, resolved.Digest),
			"resolve must report the same content digest every other transport reports")

		// Resolve is what runs before the deployment lock is taken and
		// during a dry run, so it must be safe to call on a bundle an
		// operator is still deciding about.
		after, err := atomicfs.DigestTree(bundleDir)
		require.NoError(t, err)
		assert.Equal(t, wantDigest, after, "resolve must not modify the bundle it was asked about")
	})

	t.Run("fetch produces a bundle with the digest resolve reported", func(t *testing.T) {
		source, ref := newSource(t, bundleDir)
		ctx := context.Background()

		dest := filepath.Join(t.TempDir(), "fetched")
		path, err := source.Fetch(ctx, ref, dest)
		require.NoError(t, err)
		require.NotEmpty(t, path)

		// This is the assertion the whole port rests on: a digest
		// recorded from one transport has to verify from another, or
		// pinning a release means nothing the moment a vendor changes
		// how they publish.
		got, err := atomicfs.DigestTree(string(path))
		require.NoError(t, err)
		assert.Equal(t, wantDigest, got,
			"a bundle delivered by any source must hash identically to the same bundle on disk")

		fetched, err := release.Load(string(path))
		require.NoError(t, err, "what a source fetches must be loadable as a release")
		assert.Equal(t, wantRelease.Version(), fetched.Version())
	})

	t.Run("fetch preserves the executable bit on hooks", func(t *testing.T) {
		source, ref := newSource(t, bundleDir)

		dest := filepath.Join(t.TempDir(), "fetched")
		path, err := source.Fetch(context.Background(), ref, dest)
		require.NoError(t, err)

		// A declared hook that arrives non-executable is a release
		// validation error, so a transport that loses the bit produces
		// bundles that cannot be installed -- and the digest, which
		// records the bit, would differ too.
		info, err := os.Stat(filepath.Join(string(path), "hooks", "migrate"))
		require.NoError(t, err, "the example bundle's hooks must survive the transport")
		assert.NotZero(t, info.Mode().Perm()&0o100,
			"hooks must remain executable; a bundle whose hooks cannot run is not installable")
	})

	t.Run("fetching twice produces identical bundles", func(t *testing.T) {
		source, ref := newSource(t, bundleDir)
		ctx := context.Background()

		first := filepath.Join(t.TempDir(), "a")
		second := filepath.Join(t.TempDir(), "b")

		_, err := source.Fetch(ctx, ref, first)
		require.NoError(t, err)
		_, err = source.Fetch(ctx, ref, second)
		require.NoError(t, err)

		digestA, err := atomicfs.DigestTree(first)
		require.NoError(t, err)
		digestB, err := atomicfs.DigestTree(second)
		require.NoError(t, err)

		assert.Equal(t, digestA, digestB,
			"two fetches of one reference must be indistinguishable; a release store holds only one")
	})

	t.Run("resolve refuses a digest that does not match", func(t *testing.T) {
		source, ref := newSource(t, bundleDir)

		ref.Digest = "sha256:" +
			"0000000000000000000000000000000000000000000000000000000000000000"

		_, err := source.Resolve(context.Background(), ref)
		require.Error(t, err, "a pinned digest that does not match must refuse before anything is copied")
		assert.True(t, errors.Is(err, domain.ErrDigestMismatch),
			"the refusal must be a digest mismatch, so callers can tell it from a missing bundle")
	})

	t.Run("list enumerates or says it cannot", func(t *testing.T) {
		source, ref := newSource(t, bundleDir)

		versions, err := source.List(context.Background(), ref)
		if err != nil {
			// "This source cannot answer that" is a legitimate answer
			// and a different one from "there are no versions here".
			assert.True(t, errors.Is(err, domain.ErrUnsupported),
				"a source that cannot enumerate must say so with ErrUnsupported, got: %v", err)
			return
		}
		for _, v := range versions {
			assert.False(t, v.IsZero(), "an enumerated version must be parseable")
		}
	})

	t.Run("a missing reference is not found rather than a crash", func(t *testing.T) {
		source, ref := newSource(t, bundleDir)
		ref.Location = filepath.Join(t.TempDir(), "does-not-exist")

		_, err := source.Resolve(context.Background(), ref)
		require.Error(t, err, "a reference to nothing must be an error, not an empty success")
	})
}
