package dockerdesktop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/test/fakes"
)

// rig is a Mac in miniature: Docker Desktop's bundle, an installation's manager
// directory, root's own Docker config, and stand-ins for PATH and for who can
// replace a file.
type rig struct {
	app, managerDir, rootConfig string
	runner                      *fakes.Scripted
	onPath, replaceable         map[string]string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{
		app: filepath.Join(t.TempDir(), "Docker.app"), managerDir: t.TempDir(), rootConfig: t.TempDir(),
		runner: fakes.NewScripted(), onPath: map[string]string{}, replaceable: map[string]string{},
	}
	r.desktop(t, "4.44.1", "cli 1", "compose 1")
	saved := pluginDirs
	pluginDirs = []string{t.TempDir()}
	t.Cleanup(func() { pluginDirs = saved })
	return r
}

// desktop installs a Docker Desktop release into the rig's bundle.
func (r *rig) desktop(t *testing.T, version, cli, compose string) {
	t.Helper()
	for path, body := range map[string]string{
		"Contents/Info.plist":                           "<plist/>",
		"Contents/Resources/bin/docker":                 cli,
		"Contents/Resources/cli-plugins/docker-compose": compose,
	} {
		full := filepath.Join(r.app, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(body), 0o755))
	}
	r.runner = fakes.NewScripted().OnOutput("plutil", version+"\n")
}

func (r *rig) engine() *Engine {
	return &Engine{
		dir: filepath.Join(r.managerDir, "docker"), runner: r.runner, app: r.app, rootConfig: r.rootConfig,
		look: func(name string) (string, error) {
			if path, ok := r.onPath[name]; ok {
				return path, nil
			}
			return "", os.ErrNotExist
		},
		replaceable: func(path string) (string, error) { return r.replaceable[path], nil },
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

func codesigned(runner *fakes.Scripted) []exec.Command {
	var out []exec.Command
	for _, c := range runner.Calls() {
		if c.Argv[0] == "/usr/bin/codesign" {
			out = append(out, c)
		}
	}
	return out
}

// Root runs copies of Docker Desktop's CLI and Compose plugin, each checked for
// Docker's signature in a directory only root can write, and copies again only
// when Docker Desktop's version changes (decision 18).
func TestDockerDesktopsCLIRunsFromACheckedCopy(t *testing.T) {
	r := newRig(t)

	cli, err := r.engine().Settle(context.Background())

	require.NoError(t, err)
	dir := filepath.Join(r.managerDir, "docker")
	assert.Equal(t, filepath.Join(dir, "bin", "docker"), cli.Path)
	assert.Equal(t, "4.44.1", cli.DesktopVersion)
	assert.Equal(t, "cli 1", read(t, cli.Path))
	assert.Equal(t, "compose 1", read(t, filepath.Join(dir, "cli-plugins", "docker-compose")))
	checks := codesigned(r.runner)
	require.Len(t, checks, 2)
	for _, c := range checks {
		assert.Contains(t, strings.Join(c.Argv, " "), `certificate leaf[subject.OU] = "9BNSXJN65R"`)
		assert.True(t, strings.HasSuffix(c.Argv[len(c.Argv)-1], ".new"), "the original was checked, not the copy root runs")
	}
	var config map[string]any
	require.NoError(t, json.Unmarshal([]byte(read(t, filepath.Join(dir, "config.json"))), &config))
	assert.Equal(t, []any{filepath.Join(dir, "cli-plugins")}, config["cliPluginsExtraDirs"])

	// The same release again copies nothing.
	r.runner = fakes.NewScripted().OnOutput("plutil", "4.44.1\n")
	_, err = r.engine().Settle(context.Background())
	require.NoError(t, err)
	assert.Empty(t, codesigned(r.runner))

	// A new one is copied and checked again.
	r.desktop(t, "4.45.0", "cli 2", "compose 2")
	cli, err = r.engine().Settle(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "cli 2", read(t, cli.Path))
	assert.Len(t, codesigned(r.runner), 2)
}

// A copy that fails the check is never put in place: the copies checked before
// stay the ones root runs.
func TestACopyThatFailsItsSignatureCheckIsNotRun(t *testing.T) {
	r := newRig(t)
	cli, err := r.engine().Settle(context.Background())
	require.NoError(t, err)

	r.desktop(t, "4.45.0", "replaced", "compose 2")
	r.runner.OnExit("codesign", 3, "code object is not signed at all")
	_, err = r.engine().Settle(context.Background())

	require.ErrorContains(t, err, "not signed by Docker (team 9BNSXJN65R)")
	assert.Equal(t, "cli 1", read(t, cli.Path))
	assert.NoFileExists(t, cli.Path+".new")
	assert.Equal(t, "4.44.1", read(t, filepath.Join(r.managerDir, "docker", "version")))
}

// The copies live in the installation's manager directory, which `init`
// creates; before it exists nothing is settled.
func TestNothingIsSettledBeforeTheManagerDirectoryExists(t *testing.T) {
	r := newRig(t)
	r.managerDir = filepath.Join(r.managerDir, "missing")

	_, err := r.engine().Settle(context.Background())

	require.ErrorContains(t, err, "which `init` creates")
	assert.NoDirExists(t, r.managerDir)
}

// Without Docker Desktop, the CLI on PATH runs only if nobody but root can
// replace it, nor the Compose plugin it loads, which the config then names
// first (decision 21).
func TestTheCLIOnPATHRunsOnlyIfOnlyRootCanReplaceIt(t *testing.T) {
	r := newRig(t)
	require.NoError(t, os.RemoveAll(r.app))
	r.onPath["docker"] = "/opt/homebrew/bin/docker"
	plugin := filepath.Join(r.rootConfig, "cli-plugins", "docker-compose")
	require.NoError(t, os.MkdirAll(filepath.Dir(plugin), 0o755))
	require.NoError(t, os.WriteFile(plugin, nil, 0o755))

	r.replaceable["/opt/homebrew/bin/docker"] = "uid 501"
	_, err := r.engine().Settle(context.Background())
	require.ErrorContains(t, err, "the docker CLI at /opt/homebrew/bin/docker can be replaced by uid 501")

	delete(r.replaceable, "/opt/homebrew/bin/docker")
	r.replaceable[plugin] = "uid 501"
	_, err = r.engine().Settle(context.Background())
	require.ErrorContains(t, err, "the Compose plugin at "+plugin+" can be replaced by uid 501")

	delete(r.replaceable, plugin)
	cli, err := r.engine().Settle(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "/opt/homebrew/bin/docker", cli.Path)
	assert.Empty(t, cli.DesktopVersion)
	assert.Contains(t, read(t, filepath.Join(r.managerDir, "docker", "config.json")), filepath.Dir(plugin))

	require.NoError(t, os.Remove(plugin))
	_, err = r.engine().Settle(context.Background())
	require.ErrorContains(t, err, "has no Compose plugin")
}

// The manager's config carries root's registry credentials, and a credential
// helper only when nobody but root can replace it (logs/macos-host.md). Nothing
// else of root's config comes along.
func TestTheManagersConfigCarriesOnlyCheckedHelpers(t *testing.T) {
	r := newRig(t)
	require.NoError(t, os.WriteFile(filepath.Join(r.rootConfig, "config.json"), []byte(`{
		"auths": {"ghcr.io": {"auth": "dXNlcjpwYXNz"}},
		"credsStore": "desktop",
		"credHelpers": {"a.example": "osxkeychain", "b.example": "vault"},
		"currentContext": "desktop-linux"
	}`), 0o600))
	r.onPath["docker-credential-desktop"] = "/usr/local/bin/docker-credential-desktop"
	r.replaceable["/usr/local/bin/docker-credential-desktop"] = "uid 501"
	r.onPath["docker-credential-vault"] = "/usr/local/sbin/docker-credential-vault"

	cli, err := r.engine().Settle(context.Background())

	require.NoError(t, err)
	assert.Equal(t, []string{"desktop", "osxkeychain"}, cli.DroppedHelpers)
	var config map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(read(t, filepath.Join(r.managerDir, "docker", "config.json"))), &config))
	assert.JSONEq(t, `{"ghcr.io": {"auth": "dXNlcjpwYXNz"}}`, string(config["auths"]))
	assert.JSONEq(t, `{"b.example": "vault"}`, string(config["credHelpers"]))
	assert.NotContains(t, config, "credsStore")
	assert.NotContains(t, config, "currentContext")
}

// Every docker command runs the settled CLI, and none runs while none can be.
func TestTheRunnerRunsDockerCommandsWithTheSettledCLI(t *testing.T) {
	r := newRig(t)
	inner := fakes.NewScripted()
	engine := r.engine()
	runner := Runner{Runner: inner, Engine: engine}

	_, err := runner.Run(context.Background(), exec.Command{Argv: []string{"docker", "compose", "version"}})
	require.NoError(t, err)
	_, err = runner.Run(context.Background(), exec.Command{Argv: []string{"sops", "--version"}})
	require.NoError(t, err)
	path, err := runner.Look("docker")
	require.NoError(t, err)

	calls := inner.Calls()
	require.Len(t, calls, 2)
	assert.Equal(t, []string{path, "compose", "version"}, calls[0].Argv)
	assert.Equal(t, []string{"sops", "--version"}, calls[1].Argv)

	r.managerDir = filepath.Join(r.managerDir, "missing")
	refused := Runner{Runner: inner, Engine: r.engine()}
	_, err = refused.Run(context.Background(), exec.Command{Argv: []string{"docker", "ps"}})
	require.ErrorContains(t, err, "which `init` creates")
	assert.Len(t, inner.Calls(), 2, "a docker command ran with no CLI settled")
}

// Once settled, the CLI's directory is first on PATH, so a process the manager
// starts that runs `docker` by name, a hook say, runs the checked copy
// (decision 22).
func TestTheSettledCLIIsFirstOnPATH(t *testing.T) {
	t.Setenv("PATH", os.Getenv("PATH"))
	r := newRig(t)
	engine := r.engine()
	engine.setPath = true

	cli, err := engine.Settle(context.Background())

	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(os.Getenv("PATH"), filepath.Dir(cli.Path)+string(os.PathListSeparator)),
		"PATH is %s", os.Getenv("PATH"))
}

// racingRunner checks a copy's signature as codesign would, then does what a
// second root process copying the same release concurrently does: writes its
// own copy beside the file being placed, under the name a shared copy would
// have.
type racingRunner struct {
	*fakes.Scripted
	tamper func(checked string)
}

func (r racingRunner) Run(ctx context.Context, cmd exec.Command) (exec.Result, error) {
	res, err := r.Scripted.Run(ctx, cmd)
	if cmd.Argv[0] == "/usr/bin/codesign" {
		r.tamper(cmd.Argv[len(cmd.Argv)-1])
	}
	return res, err
}

// Bytes another process writes after a copy is checked never reach the file
// root runs: each copy is checked under a name of its own.
func TestACheckedCopyCannotBeSwappedBeforeItIsPlaced(t *testing.T) {
	r := newRig(t)
	engine := r.engine()
	bin := filepath.Join(r.managerDir, "docker", "bin", "docker")
	engine.runner = racingRunner{Scripted: r.runner, tamper: func(string) {
		_ = os.WriteFile(bin+".new", []byte("unchecked"), 0o755)
	}}

	cli, err := engine.Settle(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "cli 1", read(t, cli.Path), "bytes written after the check were placed")
}

// A refusal of either copy keeps the pair checked before: a CLI never runs
// beside a plugin from another release because the second check failed.
func TestARefusedPluginKeepsThePairCheckedBefore(t *testing.T) {
	r := newRig(t)
	cli, err := r.engine().Settle(context.Background())
	require.NoError(t, err)

	r.desktop(t, "4.45.0", "cli 2", "compose 2")
	r.runner.OnExit("docker-compose", 3, "code object is not signed at all")
	_, err = r.engine().Settle(context.Background())

	require.ErrorContains(t, err, "not signed by Docker")
	assert.Equal(t, "cli 1", read(t, cli.Path), "the CLI was replaced although its plugin was refused")
	assert.Equal(t, "compose 1", read(t, filepath.Join(r.managerDir, "docker", "cli-plugins", "docker-compose")))
	leftovers, _ := filepath.Glob(filepath.Join(r.managerDir, "docker", "*", "*.new"))
	assert.Empty(t, leftovers)
}
