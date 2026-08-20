package checksum_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/verify/checksum"
	"github.com/morzecrew/morzer/internal/release"
)

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

// The producer and the completeness check must exclude the same set, and this
// is what says so.
//
// It is not a preference. `release build` writes in place (RFC 0014 decision
// 10), so a vendor runs `verify` against the working copy they built in. If the
// sums stop listing `.git` and the completeness walk keeps finding it, every
// vendor's own bundle fails their own check with one problem per object file --
// which is a worse failure than the leak, because it happens to everyone.
func TestABundleBuiltInsideAWorkingCopyVerifiesInPlace(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "manifest.yaml", "api_version: selfhost/v1alpha1\n")
	write(t, dir, "compose/compose.yaml", "services: {}\n")
	write(t, dir, ".git/config", "[remote \"origin\"]\n\turl = https://x-token@example/repo\n")
	write(t, dir, ".git/objects/ab/cdef", "object")
	write(t, dir, ".DS_Store", "finder")

	require.NoError(t, release.WriteSums(dir))

	sums, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	require.NoError(t, err)
	require.NotContains(t, string(sums), ".git/",
		"the checksum list is signed over; a repository in it is a repository published")
	require.NotContains(t, string(sums), ".DS_Store")

	require.NoError(t, checksum.VerifySumsFile(dir),
		"a bundle must verify in the directory it was built in")
}

// And an archive published before the exclusion still verifies.
//
// Its sums list `.git/config` and the extracted tree contains it, so the listed
// side matches and the completeness side no longer looks. Asserted rather than
// reasoned about: this is the compatibility question the change had to answer,
// and "it should be fine" is not an answer to it.
func TestABundleWhoseSumsAlreadyListTheSourceTreeStillVerifies(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "manifest.yaml", "api_version: selfhost/v1alpha1\n")
	write(t, dir, ".git/config", "[core]\n")

	require.NoError(t, release.WriteSums(dir))

	// What a 0.2.0-built bundle looks like: the source tree in the list.
	sums := filepath.Join(dir, "SHA256SUMS")
	existing, err := os.ReadFile(sums)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(sums,
		append(existing, []byte(sha256Line(t, dir, ".git/config"))...), 0o644))

	require.NoError(t, checksum.VerifySumsFile(dir),
		"an already-published bundle listing its source tree must keep verifying")
}

func sha256Line(t *testing.T, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	require.NoError(t, err)
	return hexDigest(data) + "  " + rel + "\n"
}

func hexDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
