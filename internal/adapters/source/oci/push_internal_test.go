package oci

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

// TestPushRefusesAnArchiveThePullSideWouldRefuse. An installation pulls at
// most MaxBlobSize; a layer published past it is one nobody can install from
// this registry, and the vendor learns that from a customer.
func TestPushRefusesAnArchiveThePullSideWouldRefuse(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "demo-1.2.0.tar.zst")
	require.NoError(t, os.WriteFile(archive, make([]byte, 64), 0o600))

	s := New()
	t.Cleanup(func() { _ = s.Close() })
	s.maxBlob = 32

	// Port 1 on loopback: a push that got as far as the registry fails with
	// "cannot reach", which is not this refusal.
	_, err := s.Push(context.Background(), PushRequest{
		Archive:   archive,
		Reference: "127.0.0.1:1/demo/bundle",
		Version:   domain.MustParseVersion("1.2.0"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "limit")
	assert.Equal(t, domain.ExitUsage, domain.ExitCode(err))
}
