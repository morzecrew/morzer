package oci

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRegistryCredentialsAreFoundWithoutHOME: systemd sets no HOME for a unit
// without User=, which is every timer this manager installs, and the docker
// CLI falls back to the passwd entry when HOME is unset. So `docker compose
// pull` under the update timer authenticated, and the manager's own fetch of
// the channel, which stopped at HOME, went anonymous.
func TestRegistryCredentialsAreFoundWithoutHOME(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("DOCKER_CONFIG", "")

	path, err := dockerConfigPath()
	require.NoError(t, err)

	u, err := user.Current()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(u.HomeDir, ".docker", "config.json"), path,
		"not the file `docker login` wrote for this user")
}

// TestDockerConfigWinsOverHOME: the docker CLI's precedence, so both read the
// same file whichever is set.
func TestDockerConfigWinsOverHOME(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DOCKER_CONFIG", dir)

	path, err := dockerConfigPath()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "config.json"), path)
}

// TestARepositoryAuthenticatesWithTheDockerConfig drives the production
// construction, not a test factory: every suite test swaps the repository
// factory out, so nothing exercised credential resolution at all. A loopback
// registry is reached over plain HTTP by the production rule.
func TestARepositoryAuthenticatesWithTheDockerConfig(t *testing.T) {
	const want = "Basic " + "dXNlcjpzM2NyZXQ=" // user:s3cret
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != want {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/v2/":
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/tags/list"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"name":"demo","tags":["1.2.0"]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")

	dir := t.TempDir()
	auth := base64.StdEncoding.EncodeToString([]byte("user:s3cret"))
	config := fmt.Sprintf(`{"auths":{%q:{"auth":%q}}}`, host, auth)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte(config), 0o600))
	t.Setenv("HOME", "")
	t.Setenv("DOCKER_CONFIG", dir)

	repo, err := OpenRepository(host + "/demo:1.2.0")
	require.NoError(t, err)
	var tags []string
	err = repo.Tags(context.Background(), "", func(page []string) error {
		tags = append(tags, page...)
		return nil
	})
	require.NoError(t, err, "the registry refused the credentials docker login stored")
	assert.Equal(t, []string{"1.2.0"}, tags)
}
