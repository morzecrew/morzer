package quadlet

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
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

// The probe reads each answer Podman 6.1.1 gave a registry, as measured.
func TestARegistryProbeSaysWhatWentWrong(t *testing.T) {
	const prefix = `Error: reading image "docker://demo/app@sha256:00": reading manifest sha256:00 in docker.io/demo/app: `
	cases := []struct {
		name, stderr, message, hint string
	}{
		{"Docker Hub refuses", prefix + "requested access to the resource is denied",
			"the registry refused access", "podman login"},
		{"quay.io refuses", prefix + "unauthorized: access to the requested resource is not authorized",
			"the registry refused access", "podman login"},
		{"no such digest", prefix + "manifest unknown",
			"the image does not exist in the registry", "never published"},
		{"no registry answers",
			`Error: reading image "docker://registry.invalid/demo/app@sha256:00": pinging container registry registry.invalid: Get "https://registry.invalid/v2/": EOF`,
			`Error: reading image "docker://registry.invalid/demo/app@sha256:00": pinging container registry registry.invalid: Get "https://registry.invalid/v2/": EOF`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := fakes.NewScripted()
			runner.OnExit("manifest inspect", 125, tc.stderr)
			err := New(runner).ProbeRegistry(context.Background(), appRef)
			require.Error(t, err)
			e := domain.AsError(err)
			assert.Equal(t, tc.message, e.Message)
			if tc.hint == "" {
				assert.Empty(t, e.Hint)
			} else {
				assert.Contains(t, e.Hint, tc.hint)
			}
		})
	}

	runner := fakes.NewScripted()
	require.NoError(t, New(runner).ProbeRegistry(context.Background(), appRef))
	assert.Equal(t, []string{"podman manifest inspect " + appRef}, commandsWith(runner, "manifest"))
	assert.Empty(t, commandsWith(runner, " pull "), "the probe pulled")
}

// A digest pinned to one image's manifest, rather than to an index, is
// served by the registry and then refused by `podman manifest inspect`,
// which reads only lists. The registry answered, which is all the probe asks.
func TestARegistryThatServesOneImagesManifestAnswers(t *testing.T) {
	runner := fakes.NewScripted()
	runner.OnExit("manifest inspect", 125,
		`Error: parsing manifest blob "{\n  \"schemaVersion\": 2,\n  \"mediaType\": \"application/vnd.oci.image.manifest.v1+json\"\n}" `+
			`as a "application/vnd.oci.image.manifest.v1+json": Treating single images as manifest lists is not implemented`+"\n")
	assert.NoError(t, New(runner).ProbeRegistry(context.Background(), appRef))
}
