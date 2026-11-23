package cli

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// init's credential is held before its first fetch, which may be the one
// that reads the product's name before the operation starts (RFC 0038
// decision 12): read from the flags, it already answers for its server.
func TestInitHoldsItsCredentialBeforeItsFirstFetch(t *testing.T) {
	token := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(token, []byte("tok-123\n"), 0o600))
	app := &App{sourceCredentials: ops.NewSourceCredentials(func() *ops.Deps { return nil })}
	cmd := &cobra.Command{}
	cmd.Flags().String("source-auth", "", "")
	cmd.Flags().String("source-secret", "", "")

	src, err := app.initSource(cmd, "https://Downloads.Vendor.Example/demo-1.2.0.tar.zst", false,
		"bearer", "", token)
	require.NoError(t, err)

	entry, found, err := app.sourceCredentials.SourceFor(context.Background(), "downloads.vendor.example")
	require.NoError(t, err)
	require.True(t, found, "the credential is not held for the first fetch")
	assert.Equal(t, src.Entry, entry)
	value, err := app.sourceCredentials.RevealSource(context.Background(), entry)
	require.NoError(t, err)
	assert.Equal(t, "tok-123\n", value)

	_, found, err = app.sourceCredentials.SourceFor(context.Background(), "mirror.example")
	require.NoError(t, err)
	assert.False(t, found, "the held credential answers for another server")
}

// The wizard prints the command that reproduces its run, which names the file
// the credential came from and never the credential.
func TestTheEquivalentCommandNamesTheCredentialsFileNotItsValue(t *testing.T) {
	src, err := ops.NewInitSource("https://downloads.vendor.example/b.tar.zst", domain.SourceAuthBasic,
		"vendor_login", "customer:hunter2", "/root/vendor-login")
	require.NoError(t, err)
	got := EquivalentCommand(ops.InitOptions{Product: "demo", ReleasePath: "https://downloads.vendor.example/b.tar.zst", Source: src})
	for _, want := range []string{"--source-secret-file /root/vendor-login", "--source-auth basic", "--source-secret vendor_login"} {
		assert.Contains(t, got, want)
	}
	assert.NotContains(t, got, "hunter2")

	src, err = ops.NewInitSource("https://downloads.vendor.example/b.tar.zst", domain.SourceAuthBearer, "", "t", "/root/t")
	require.NoError(t, err)
	got = EquivalentCommand(ops.InitOptions{Product: "demo", Source: src})
	assert.NotContains(t, got, "--source-auth", "the default scheme is spelled out")
	assert.NotContains(t, got, "--source-secret ", "the default secret name is spelled out")
}

// A credential is a token or a login, read from a regular file and bounded as
// it is read: a pipe or a device would hang init or exhaust its memory, and a
// file may grow after its size was looked at.
func TestInitReadsACredentialFileWithABound(t *testing.T) {
	app := &App{}
	cmd := &cobra.Command{}
	cmd.Flags().String("source-auth", "", "")
	cmd.Flags().String("source-secret", "", "")
	release := "https://downloads.vendor.example/demo-1.2.0.tar.zst"

	fifo := filepath.Join(t.TempDir(), "fifo")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	_, err := app.initSource(cmd, release, false, "bearer", "", fifo)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a regular file")

	_, err = app.initSource(cmd, release, false, "bearer", "", "/dev/zero")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a regular file")

	big := filepath.Join(t.TempDir(), "big")
	require.NoError(t, os.WriteFile(big, make([]byte, maxSourceSecretFile+1), 0o600))
	_, err = app.initSource(cmd, release, false, "bearer", "", big)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "larger than")
}
