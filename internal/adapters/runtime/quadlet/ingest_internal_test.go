package quadlet

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/fakes"
)

const (
	appDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000001"
	appRef    = "registry.example/demo/app@" + appDigest
	appAlias  = "registry.example/demo/app:morzer-sha256-" +
		"0000000000000000000000000000000000000000000000000000000000000001"
)

// emptyLayout is an OCI layout with nothing in it, enough to serve.
func emptyLayout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.json"),
		[]byte(`{"schemaVersion":2,"manifests":[]}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "oci-layout"),
		[]byte(`{"imageLayoutVersion":"1.0.0"}`), 0o644))
	return dir
}

// commandsWith is every command the runner ran that carries word.
func commandsWith(runner *fakes.Scripted, word string) []string {
	var out []string
	for _, c := range runner.Calls() {
		if line := strings.Join(c.Argv, " "); strings.Contains(line, word) {
			out = append(out, line)
		}
	}
	return out
}

// A bundled image is pulled out of the loopback registry with TLS
// verification off on that pull, named by its alias, and its loopback name
// removed. A pull from a registry never turns the verification off.
func TestABundledImageIsPulledFromLoopbackWithTLSOffOnThatPullAlone(t *testing.T) {
	runner := fakes.NewScripted()
	runner.OnExit("image exists", 1, "") // absent, so there is something to do
	r := New(runner)

	require.NoError(t, r.IngestImages(context.Background(), emptyLayout(t), []string{appRef}))

	pulls := commandsWith(runner, " pull ")
	require.Len(t, pulls, 1, runner.CommandLines())
	assert.True(t, strings.HasPrefix(pulls[0], "podman pull --tls-verify=false 127.0.0.1:"), pulls[0])
	assert.True(t, strings.HasSuffix(pulls[0], "/demo/app@"+appDigest), pulls[0])
	tags := commandsWith(runner, " tag ")
	require.Len(t, tags, 1, runner.CommandLines())
	assert.True(t, strings.HasSuffix(tags[0], " "+appAlias), tags[0])
	untags := commandsWith(runner, " rmi ")
	require.Len(t, untags, 1, runner.CommandLines())
	assert.Contains(t, untags[0], "127.0.0.1:")
	assert.NotContains(t, untags[0], appAlias, "the untag removed the alias the units resolve through")

	require.NoError(t, r.Pull(context.Background(), ports.RuntimeConfig{}, []string{appRef}))
	pulls = commandsWith(runner, " pull ")
	require.Len(t, pulls, 2)
	assert.Equal(t, "podman pull "+appRef, pulls[1], "a registry pull turned the verification off")
}

// An image already here is not loaded again, and the layout is not opened.
func TestABundledImageAlreadyHereIsNotLoadedAgain(t *testing.T) {
	runner := fakes.NewScripted() // `image exists` succeeds: present
	r := New(runner)

	require.NoError(t, r.IngestImages(context.Background(), t.TempDir(), []string{appRef}))
	assert.Empty(t, commandsWith(runner, " pull "), runner.CommandLines())
	assert.True(t, runner.Ran("image exists "+appAlias), runner.CommandLines())
}
