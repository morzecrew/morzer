package suite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/release"
)

// An engine that reads the host's files as one user, as a Mac's Docker Desktop
// does (RFC 0055 decisions 16, 17 and 19).

// givableOwner is an owner this process can give a file to and a test can
// tell from its default: its own uid and a group other than its primary one,
// where it has one.
func givableOwner(t *testing.T) (uid, gid int) {
	t.Helper()
	groups, _ := os.Getgroups()
	for _, g := range groups {
		if g != os.Getegid() {
			return os.Geteuid(), g
		}
	}
	t.Log("this process belongs to no second group, so a change of owner is not observable")
	return os.Geteuid(), os.Getegid()
}

// onEngineAsUser is an installation whose engine reads the host as uid:gid, on
// a release that gives db_password to 65532, an owner this process could not
// give a file to.
func onEngineAsUser(t *testing.T, user func() (int, int, error)) *harness {
	t.Helper()
	h := newHarness(t)
	patchManifest(t, h.Release.Root, `min_manager_version: "0.0.0"`,
		`min_manager_version: "`+domain.SecretOwnerMinManagerVersion+`"`)
	path := filepath.Join(h.Release.Root, "secrets.schema.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	owned := strings.Replace(string(raw), "    services: [app, db]\n",
		"    services: [app, db]\n    owner: \"65532:65532\"\n", 1)
	require.NotEqual(t, string(raw), owned)
	require.NoError(t, os.WriteFile(path, []byte(owned), 0o644))
	rel, err := release.Load(h.Release.Root)
	require.NoError(t, err)
	h.Release = rel

	h.Deps.EngineUser = user
	h.Secrets.RenderOwner = user
	h.install()
	h.setHookEnv()
	return h
}

func ownerOf(t *testing.T, path string) [2]int {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	st := info.Sys().(*syscall.Stat_t)
	return [2]int{int(st.Uid), int(st.Gid)}
}

// That user gets the rendered secrets, their directory and DataDir, the
// declared owner set aside, and doctor says owner is not enforced.
func TestTheEngineUserOwnsWhatAContainerReadsAndWrites(t *testing.T) {
	uid, gid := givableOwner(t)
	h := onEngineAsUser(t, func() (int, int, error) { return uid, gid, nil })

	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)

	want := [2]int{uid, gid}
	assert.Equal(t, want, ownerOf(t, h.Paths.SecretsRenderDir()))
	for _, name := range []string{"db_password", "session_key"} {
		assert.Equal(t, want, ownerOf(t, filepath.Join(h.Paths.SecretsRenderDir(), name)), name)
	}
	assert.Equal(t, want, ownerOf(t, h.Paths.DataDir()))

	check := doctorCheck(t, h, "secrets.owner")
	assert.Equal(t, events.CheckWarn, check.Status)
	assert.Contains(t, check.Message, "owner is not enforced on this host")
}

// Who runs the engine is read before anything is rendered or started, and not
// knowing fails the render, with nothing written or started to undo.
func TestAnUnknownEngineUserRefusesBeforeAnythingStarts(t *testing.T) {
	h := onEngineAsUser(t, func() (int, int, error) {
		return 0, 0, domain.RuntimeError(nil, "no engine answers at /var/run/docker.sock")
	})

	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})

	require.Error(t, err)
	assert.Equal(t, domain.ExitCompensated, domain.ExitCode(err))
	assert.Contains(t, err.Error(), "no engine answers at /var/run/docker.sock")
	assert.Empty(t, h.Runtime.UpOptions)
	assert.NoFileExists(t, filepath.Join(h.Paths.SecretsRenderDir(), "db_password"))
}

// A boot clears RunDir, and a render recreates it, and what is under it, with
// their own modes rather than the secrets directory's 0700
// (logs/macos-host.md).
func TestARenderRecreatesRunDirWithItsOwnMode(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	require.NoError(t, os.RemoveAll(h.Paths.RunDir))

	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)

	for _, dir := range h.Paths.EphemeralDirs() {
		info, err := os.Stat(dir.Path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(dir.Mode), info.Mode().Perm(), dir.Path)
	}
}
