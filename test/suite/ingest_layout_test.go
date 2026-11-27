//go:build docker || podman

package suite

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// unknownDigestForCorruptBytes stores the layout's manifest under a digest it
// does not hash to, and returns the reference that now addresses it.
//
// Two problems are solved at once, and the second is why this is not simply a
// byte flip. Flipping bytes under the existing digest was tried and the ingest
// succeeded: the daemon already knew that manifest digest -- from the seeding
// push earlier in this same test -- and resolved it out of its own distribution
// metadata without fetching anything at all. Removing every local reference
// does not clear that memory.
//
// A digest nothing has ever computed cannot be short-circuited, so the daemon
// has to ask for it, and the bytes it gets back do not hash to what it asked
// for. That is exactly the condition decision 21 says must be refused.
func unknownDigestForCorruptBytes(t *testing.T, layoutDir, vendorRef string) string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(layoutDir, "index.json"))
	require.NoError(t, err)

	var index struct {
		SchemaVersion int              `json:"schemaVersion"`
		MediaType     string           `json:"mediaType"`
		Manifests     []map[string]any `json:"manifests"`
	}
	require.NoError(t, json.Unmarshal(raw, &index))
	require.NotEmpty(t, index.Manifests, "the layout's index names nothing")

	real, ok := index.Manifests[0]["digest"].(string)
	require.True(t, ok, "the index entry carries no digest")
	algorithm, encoded, ok := strings.Cut(real, ":")
	require.True(t, ok)

	data, err := os.ReadFile(filepath.Join(layoutDir, "blobs", algorithm, encoded))
	require.NoError(t, err)

	// Derived from the content, so the same bundle produces the same wrong
	// digest on every run -- a test that needs a fresh identity each time is
	// a test that leaves a trail on the machine that runs it.
	sum := sha256.Sum256(append(data, []byte("not this manifest")...))
	wrong := "sha256:" + hex.EncodeToString(sum[:])

	blob := filepath.Join(layoutDir, "blobs", "sha256", strings.TrimPrefix(wrong, "sha256:"))
	require.NoError(t, os.WriteFile(blob, data, 0o444))

	index.Manifests[0]["digest"] = wrong
	rewritten, err := json.Marshal(index)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(layoutDir, "index.json"), rewritten, 0o644))

	return vendorRef[:strings.LastIndex(vendorRef, "@")] + "@" + wrong
}
