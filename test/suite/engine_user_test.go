package suite

import (
	"context"
	"errors"
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
	"github.com/morzecrew/morzer/internal/ports"
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
	st, ok := info.Sys().(*syscall.Stat_t)
	require.True(t, ok, "this platform reports no owner")
	return [2]int{int(st.Uid), int(st.Gid)}
}

// That user gets the rendered secrets, their directory and DataDir, the
// declared owner set aside, and doctor says owner is not enforced.
func TestTheEngineUserOwnsWhatAContainerReadsAndWrites(t *testing.T) {
	uid, gid := givableOwner(t)
	h := onEngineAsUser(t, func() (int, int, error) { return uid, gid, nil })
	// A file a hook left in DataDir, as one that runs as root would.
	left := filepath.Join(h.Paths.DataDir(), "dump", "left-by-a-hook")
	require.NoError(t, os.MkdirAll(filepath.Dir(left), 0o750))
	require.NoError(t, os.WriteFile(left, []byte("x"), 0o640))

	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)

	want := [2]int{uid, gid}
	assert.Equal(t, want, ownerOf(t, h.Paths.SecretsRenderDir()))
	for _, name := range []string{"db_password", "session_key"} {
		assert.Equal(t, want, ownerOf(t, filepath.Join(h.Paths.SecretsRenderDir(), name)), name)
	}
	assert.Equal(t, want, ownerOf(t, h.Paths.DataDir()))
	assert.Equal(t, want, ownerOf(t, filepath.Dir(left)))
	assert.Equal(t, want, ownerOf(t, left), "an entry inside DataDir was not given")

	check := doctorCheck(t, h, "secrets.owner")
	assert.Equal(t, events.CheckWarn, check.Status)
	assert.Contains(t, check.Message, "owner is not enforced on this host")
}

// A container reads a file only when that user can, and no group holds that
// user alone, so the rendered configuration stays root's and every account may
// read it (RFC 0055 decision 16). On an engine that reads as root, its mode is
// the release's.
func TestTheEngineUserReadsTheRenderedConfiguration(t *testing.T) {
	uid, gid := givableOwner(t)
	h := onEngineAsUser(t, func() (int, int, error) { return uid, gid, nil })
	config := filepath.Join(h.Root, "etc", "demo", "application.yaml")

	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)

	info, err := os.Stat(config)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm(), "the release declares 0640")
	assert.Equal(t, [2]int{os.Geteuid(), os.Getegid()}, ownerOf(t, config), "the configuration was given away")

	// The same bytes at a mode that user cannot read, as a build before this
	// one rendered them, or a hand left them, are not converged.
	require.NoError(t, os.Chmod(config, 0o640))
	_, err = ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)
	info, err = os.Stat(config)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm(), "a file with the same bytes kept a mode that user cannot read")

	h = newHarness(t)
	h.install()
	_, err = ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)
	info, err = os.Stat(filepath.Join(h.Root, "etc", "demo", "application.yaml"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), info.Mode().Perm(), "an engine that reads as root got another mode")
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

// A restart starts processes that write DataDir, so it gives DataDir first,
// as a start does: the restart after a secret change goes through it.
func TestARestartGivesDataDirFirst(t *testing.T) {
	uid, gid := givableOwner(t)
	h := onEngineAsUser(t, func() (int, int, error) { return uid, gid, nil })
	left := filepath.Join(h.Paths.DataDir(), "left-by-a-hook")
	require.NoError(t, os.WriteFile(left, []byte("x"), 0o640))
	cfg, err := h.Deps.RuntimeConfigFor(h.Release, installation(t, h))
	require.NoError(t, err)

	require.NoError(t, h.Deps.RestartServices(context.Background(), cfg, []string{"app"}))
	assert.Equal(t, [2]int{uid, gid}, ownerOf(t, left))
}

// A one-shot run, a migration's or a task's, starts its service and what that
// depends on before services start, so DataDir is given first there too: on a
// fresh installation a database would otherwise start on a directory it cannot
// write.
func TestAOneShotGivesDataDirFirst(t *testing.T) {
	uid, gid := givableOwner(t)
	user := func() (int, int, error) { return uid, gid, nil }
	h := taskHarness(t, "", reindexTask)
	h.Deps.EngineUser = user
	h.Secrets.RenderOwner = user
	left := filepath.Join(h.Paths.DataDir(), "left-by-a-hook")
	require.NoError(t, os.WriteFile(left, []byte("x"), 0o640))
	before := len(h.Runtime.OneShots)

	_, err := ops.RunTask(context.Background(), h.Deps, ops.TaskOptions{Name: "reindex"})
	require.NoError(t, err)
	require.Len(t, h.Runtime.OneShots, before+1)
	assert.Equal(t, [2]int{uid, gid}, ownerOf(t, left))
}

// On a Mac RunDir's entry is in a directory its group can write, and doctor
// fails while an account other than root is in that group: it could move
// RunDir aside and redirect where root writes decrypted secrets.
func TestDoctorFailsWhileSomeoneElseCanWriteWhereRunDirSits(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	parent := filepath.Dir(h.Paths.RunDir)
	var asked string
	writers := []string{"alice"}
	h.Deps.RunDirWriters = func(_ context.Context, dir string) ([]string, error) {
		asked = dir
		return writers, nil
	}

	check := doctorCheck(t, h, "storage.run-directory")
	assert.Equal(t, parent, asked)
	assert.Equal(t, events.CheckFail, check.Status)
	assert.Contains(t, check.Message, "alice can write "+parent)
	assert.Contains(t, check.Remedy, "take alice out of the group")

	writers = nil
	assert.Equal(t, events.CheckOK, doctorCheck(t, h, "storage.run-directory").Status)

	h.Deps.RunDirWriters = func(context.Context, string) ([]string, error) {
		return nil, errors.New("dscl: not permitted")
	}
	assert.Equal(t, events.CheckWarn, doctorCheck(t, h, "storage.run-directory").Status)
}

// And nothing renders while one can: the render refuses before it creates
// anything under RunDir, rather than leaving it to doctor.
func TestNoSecretRendersWhileSomeoneElseCanWriteWhereRunDirSits(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	writers := []string{"alice"}
	h.Deps.RunDirWriters = func(context.Context, string) ([]string, error) { return writers, nil }
	require.NoError(t, os.RemoveAll(h.Paths.RunDir), "a boot clears RunDir")

	_, err := ops.Apply(ctx, h.Deps, ops.Options{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "alice can write "+filepath.Dir(h.Paths.RunDir))
	assert.NoDirExists(t, h.Paths.RunDir)
	assert.Empty(t, h.Runtime.UpOptions)

	writers = nil
	_, err = ops.Apply(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(h.Paths.SecretsRenderDir(), "db_password"))
}

// On a Mac doctor says which docker CLI root runs, fails while none may run,
// warns when a credential helper of root's config was left out, and warns when
// Docker Desktop is a release the acceptance run has not passed on.
func TestDoctorSaysWhichDockerCLIRootRuns(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	cli := ports.EngineCLI{Path: "/var/lib/demo/manager/docker/bin/docker", DesktopVersion: "4.44.1", Verified: []string{"4.44"}}
	var settleErr error
	h.Deps.EngineCLI = func(context.Context) (ports.EngineCLI, error) { return cli, settleErr }

	check := doctorCheck(t, h, "runtime.cli")
	assert.Equal(t, events.CheckOK, check.Status)
	assert.Contains(t, check.Message, "Docker Desktop 4.44.1")
	assert.Equal(t, events.CheckOK, doctorCheck(t, h, "runtime.engine-version").Status)

	cli.DroppedHelpers = []string{"desktop"}
	check = doctorCheck(t, h, "runtime.cli")
	assert.Equal(t, events.CheckWarn, check.Status)
	assert.Contains(t, check.Message, "without the credential helper desktop")

	cli.DesktopVersion = "4.45.0"
	check = doctorCheck(t, h, "runtime.engine-version")
	assert.Equal(t, events.CheckWarn, check.Status)
	assert.Contains(t, check.Message, "Docker Desktop 4.45.0 is not a release the acceptance run passed on (it passed on 4.44)")
	cli.Verified = nil
	assert.Contains(t, doctorCheck(t, h, "runtime.engine-version").Message, "(none has yet)")

	settleErr = domain.Preflight(nil, "the docker CLI at /opt/homebrew/bin/docker can be replaced by uid 501")
	check = doctorCheck(t, h, "runtime.cli")
	assert.Equal(t, events.CheckFail, check.Status)
	assert.Contains(t, check.Message, "can be replaced by uid 501")
}

// A directory a container cannot reach refuses apply before anything starts,
// and the probe writes DataDir as services will find it, given to the engine's
// user (RFC 0055 §5.3 and decision 16).
func TestApplyRefusesADirectoryTheEnginesFileSharingCannotReach(t *testing.T) {
	uid, gid := givableOwner(t)
	h := onEngineAsUser(t, func() (int, int, error) { return uid, gid, nil })
	h.Runtime.Fail["ProbeFileSharing"] = domain.Preflight(nil, "the engine does not share %s with containers",
		h.Paths.OptDir).WithHint("add it to the engine's file sharing")

	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})

	require.Error(t, err)
	assert.Equal(t, domain.ExitPreflight, domain.ExitCode(err))
	assert.Empty(t, h.Runtime.UpOptions)
	assert.Equal(t, []string{h.Paths.DataDir()}, h.Runtime.SharingWrite)
	assert.Equal(t, []string{h.Paths.EtcDir, h.Paths.VarDir, h.Paths.OptDir, h.Paths.RunDir}, h.Runtime.SharingRead)
	assert.Equal(t, [2]int{uid, gid}, ownerOf(t, h.Paths.DataDir()), "DataDir was probed before it was given")
}

// doctor changes nothing: it writes DataDir only once the engine's user owns
// it. A probe that did not run warns, and a host whose engine reads as root
// has no such check.
func TestDoctorProbesTheEnginesFileSharing(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	assert.NotContains(t, doctorIDs(t, h), "runtime.file-sharing")

	h.Deps.EngineUser = func() (int, int, error) { return os.Geteuid() + 1, os.Getegid(), nil }
	check := doctorCheck(t, h, "runtime.file-sharing")
	assert.Equal(t, events.CheckOK, check.Status)
	assert.Empty(t, h.Runtime.SharingWrite)
	assert.Contains(t, h.Runtime.SharingRead, h.Paths.DataDir())

	h.Deps.EngineUser = func() (int, int, error) { return os.Geteuid(), os.Getegid(), nil }
	check = doctorCheck(t, h, "runtime.file-sharing")
	assert.Equal(t, events.CheckOK, check.Status)
	assert.Contains(t, check.Message, "and writes "+h.Paths.DataDir())

	// One this process cannot stat is still probed, for the engine to say
	// why a container cannot reach it.
	if os.Geteuid() != 0 {
		above := filepath.Dir(h.Paths.OptDir)
		require.NoError(t, os.Chmod(above, 0o600))
		t.Cleanup(func() { _ = os.Chmod(above, 0o755) })
		doctorCheck(t, h, "runtime.file-sharing")
		assert.Contains(t, h.Runtime.SharingRead, h.Paths.OptDir)
		require.NoError(t, os.Chmod(above, 0o755))
	}

	h.Runtime.Fail["ProbeFileSharing"] = domain.RuntimeError(nil, "cannot run the file-sharing probe through busybox")
	check = doctorCheck(t, h, "runtime.file-sharing")
	assert.Equal(t, events.CheckWarn, check.Status)
	assert.Contains(t, check.Message, "nothing was probed")

	h.Runtime.Fail["ProbeFileSharing"] = domain.Preflight(nil, "a container cannot write %s", h.Paths.DataDir())
	check = doctorCheck(t, h, "runtime.file-sharing")
	assert.Equal(t, events.CheckFail, check.Status)
	assert.Contains(t, check.Message, "a container cannot write")
}

func doctorIDs(t *testing.T, h *harness) []string {
	t.Helper()
	report, err := ops.Doctor(context.Background(), h.Deps)
	require.NoError(t, err)
	var ids []string
	for _, r := range report.Results {
		ids = append(ids, r.ID)
	}
	return ids
}
