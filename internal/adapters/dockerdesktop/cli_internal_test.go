package dockerdesktop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
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
	saved, savedSystem := pluginDirs, systemPluginDirs
	pluginDirs = []string{t.TempDir()}
	// One system directory that exists, and one that does not.
	systemPluginDirs = []string{t.TempDir(), filepath.Join(t.TempDir(), "lib", "docker", "cli-plugins")}
	t.Cleanup(func() { pluginDirs, systemPluginDirs = saved, savedSystem })
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
		given:       os.Getenv("PATH"),
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

// A hook waits on more than the CLI: each directory the CLI searches for
// plugins, and every entry in it, has to be root's alone, and one that does not
// exist has to sit where only root could make it, since a hook that lists
// plugins runs each one as root (decision 24).
func TestAHookWaitsWhileAPluginTheCLIWouldFindCouldBeReplaced(t *testing.T) {
	for name, tc := range map[string]struct {
		arrange func(t *testing.T, r *rig, existing, missing string)
		says    string // "" when the hook runs
	}{
		"everything root's": {arrange: func(*testing.T, *rig, string, string) {}},
		"a user's plugin in a system directory": {
			arrange: func(t *testing.T, r *rig, existing, _ string) {
				plugin := filepath.Join(existing, "docker-scout")
				require.NoError(t, os.WriteFile(plugin, nil, 0o755))
				r.replaceable[plugin] = plugin + " is owned by uid 501, not root"
			},
			says: "docker-scout is owned by uid 501, not root",
		},
		"a system directory a group can write": {
			arrange: func(_ *testing.T, r *rig, existing, _ string) {
				r.replaceable[existing] = existing + " is writable by group 80"
			},
			says: "is writable by group 80",
		},
		"a missing one, where someone else could make it": {
			arrange: func(_ *testing.T, r *rig, _, missing string) {
				above := filepath.Dir(filepath.Dir(filepath.Dir(missing)))
				r.replaceable[above] = above + " is writable by every user"
			},
			says: "is writable by every user",
		},
		// Where nobody else may rename an entry, anyone may still add one.
		"a sticky system directory every user can write": {
			arrange: func(t *testing.T, _ *rig, existing, _ string) {
				sticky(t, existing)
			},
			says: "every user can add a plugin to",
		},
		"a missing one under a sticky directory every user can write": {
			arrange: func(t *testing.T, _ *rig, _, missing string) {
				sticky(t, filepath.Dir(filepath.Dir(filepath.Dir(missing))))
			},
			says: "every user can add a plugin to",
		},
		"a plugin beside Docker Desktop's copies": {
			arrange: func(t *testing.T, r *rig, _, _ string) {
				plugin := filepath.Join(r.managerDir, "docker", "cli-plugins", "docker-extra")
				require.NoError(t, os.MkdirAll(filepath.Dir(plugin), 0o700))
				require.NoError(t, os.WriteFile(plugin, nil, 0o755))
				r.replaceable[plugin] = plugin + " is owned by uid 501, not root"
			},
			says: "docker-extra is owned by uid 501, not root",
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			tc.arrange(t, r, systemPluginDirs[0], systemPluginDirs[1])

			err := r.engine().BeforeHook(context.Background())

			if tc.says == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, domain.ErrPreflight)
			assert.Contains(t, domain.AsError(err).Message, "a hook may list the docker CLI's plugins")
			assert.Contains(t, domain.AsError(err).Message, tc.says)
		})
	}
}

// sticky makes a directory every user can write and none rename another's
// entry in. os.Chmod reads the bit from os.ModeSticky, not from 0o1000.
func sticky(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.Chmod(dir, os.ModeSticky|0o777))
	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSticky, "%s is not sticky", dir)
}

// A relative plugin directory in root's config names a different directory
// for every process, a hook's among them, which runs from its release, so it
// is never the one the manager's config names.
func TestARelativePluginDirectoryIsNotSearched(t *testing.T) {
	r := newRig(t)
	require.NoError(t, os.RemoveAll(r.app))
	r.onPath["docker"] = "/opt/homebrew/bin/docker"
	here := t.TempDir()
	t.Chdir(here)
	require.NoError(t, os.MkdirAll(filepath.Join(here, "plugins"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(here, "plugins", "docker-compose"), nil, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(r.rootConfig, "config.json"), []byte(`{"cliPluginsExtraDirs": ["plugins"]}`), 0o600))

	_, err := r.engine().Settle(context.Background())

	require.ErrorContains(t, err, "has no Compose plugin")
}

// A directory whose name walks back past a link is one place to the kernel,
// which lists it, and another to a lexical join, which is how the CLI names
// each plugin it runs and how the manager checks one, so it is not searched
// either.
func TestAPluginDirectoryThatIsNotCleanIsNotSearched(t *testing.T) {
	r := newRig(t)
	require.NoError(t, os.RemoveAll(r.app))
	r.onPath["docker"] = "/opt/homebrew/bin/docker"
	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, "extra"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(base, "extra", "docker-compose"), nil, 0o755))
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(base, "link")))
	config, err := json.Marshal(map[string][]string{"cliPluginsExtraDirs": {base + "/link/../extra"}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(r.rootConfig, "config.json"), config, 0o600))

	_, err = r.engine().Settle(context.Background())

	require.ErrorContains(t, err, "has no Compose plugin")
}

// A trailing slash or a `.` names the same directory to the kernel and to a
// join, so such a directory is searched, and named in the manager's config
// clean.
func TestAPluginDirectorySpelledWithASlashOrADotIsSearchedClean(t *testing.T) {
	for name, spelling := range map[string]string{"a trailing slash": "%s/extra/", "a dot": "%s/./extra", "a doubled slash": "%s//extra"} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			require.NoError(t, os.RemoveAll(r.app))
			r.onPath["docker"] = "/opt/homebrew/bin/docker"
			base := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(base, "extra"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(base, "extra", "docker-compose"), nil, 0o755))
			config, err := json.Marshal(map[string][]string{"cliPluginsExtraDirs": {fmt.Sprintf(spelling, base)}})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(r.rootConfig, "config.json"), config, 0o600))

			_, err = r.engine().Settle(context.Background())

			require.NoError(t, err)
			var written map[string]any
			require.NoError(t, json.Unmarshal([]byte(read(t, filepath.Join(r.managerDir, "docker", "config.json"))), &written))
			assert.Equal(t, []any{filepath.Join(base, "extra")}, written["cliPluginsExtraDirs"])
		})
	}
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
	assert.Equal(t, filepath.Join(r.rootConfig, "config.json"), cli.RootConfig, "doctor names the file read")
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
	t.Setenv("DOCKER_CONFIG", os.Getenv("DOCKER_CONFIG"))
	r := newRig(t)
	engine := r.engine()
	engine.setEnv = true

	cli, err := engine.Settle(context.Background())

	require.NoError(t, err)
	found, err := lookIn(os.Getenv("PATH"), "docker")
	require.NoError(t, err)
	assert.Equal(t, cli.Path, found, "PATH is %s", os.Getenv("PATH"))
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

// On an installation the config is rebuilt from root's own before anything
// reaches a registry, an update's download of its release included, so a
// helper is never named that was not checked by this process, and each kept
// one is linked first on PATH, where its name resolves to the file checked.
func TestTheConfigIsRebuiltBeforeAnyRegistryIsReached(t *testing.T) {
	t.Setenv("PATH", os.Getenv("PATH"))
	t.Setenv("DOCKER_CONFIG", os.Getenv("DOCKER_CONFIG"))
	r := newRig(t)
	dir := filepath.Join(r.managerDir, "docker")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"credsStore": "stale"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(r.rootConfig, "config.json"), []byte(`{
		"auths": {"ghcr.io": {}}, "credsStore": "desktop", "credHelpers": {"b.example": "vault"}
	}`), 0o600))
	r.onPath["docker-credential-desktop"] = "/usr/local/bin/docker-credential-desktop"
	r.replaceable["/usr/local/bin/docker-credential-desktop"] = "uid 501"
	r.onPath["docker-credential-vault"] = "/usr/local/sbin/docker-credential-vault"
	engine := r.engine()
	engine.setEnv = true

	require.NoError(t, engine.Prepare())

	config := read(t, filepath.Join(dir, "config.json"))
	assert.NotContains(t, config, "stale")
	assert.NotContains(t, config, `"credsStore"`)
	link, err := os.Readlink(filepath.Join(dir, "helpers", "docker-credential-vault"))
	require.NoError(t, err)
	assert.Equal(t, "/usr/local/sbin/docker-credential-vault", link)
	assert.True(t, strings.HasPrefix(os.Getenv("PATH"), filepath.Join(dir, "helpers")+string(os.PathListSeparator)))
	assert.Equal(t, dir, os.Getenv("DOCKER_CONFIG"))

	// A helper left out, and the store the CLI looks for when a config names
	// none, answer by name with one that holds no credentials, as does one
	// linked before for a command that read the earlier config.
	none := filepath.Join(dir, "helpers", ".none")
	for _, name := range []string{"desktop", "osxkeychain"} {
		target, err := os.Readlink(filepath.Join(dir, "helpers", "docker-credential-"+name))
		require.NoError(t, err)
		assert.Equal(t, none, target, name)
	}
	require.NoError(t, os.WriteFile(filepath.Join(r.rootConfig, "config.json"), []byte(`{"auths": {}}`), 0o600))
	require.NoError(t, engine.Prepare())
	target, err := os.Readlink(filepath.Join(dir, "helpers", "docker-credential-vault"))
	require.NoError(t, err)
	assert.Equal(t, none, target, "a helper no longer checked kept its link for a command still reading the old config")
	out, err := osexec.Command(none, "get").Output()
	assert.Error(t, err)
	assert.Equal(t, "credentials not found in native keychain\n", string(out))

	// Before `init` creates the manager directory there is nothing to read,
	// and nothing is made.
	r.managerDir = filepath.Join(r.managerDir, "missing")
	require.NoError(t, r.engine().Prepare())
	assert.NoDirExists(t, r.managerDir)
}

// `docker` by name, as a hook runs it, is the CLI the engine settled on, even
// after Docker Desktop is gone and its earlier copies are still there.
func TestDockerByNameIsTheSettledCLI(t *testing.T) {
	t.Setenv("PATH", os.Getenv("PATH"))
	t.Setenv("DOCKER_CONFIG", os.Getenv("DOCKER_CONFIG"))
	r := newRig(t)
	_, err := r.engine().Settle(context.Background())
	require.NoError(t, err)

	require.NoError(t, os.RemoveAll(r.app))
	cliDir := t.TempDir()
	onPath := filepath.Join(cliDir, "docker")
	require.NoError(t, os.WriteFile(onPath, nil, 0o755))
	r.onPath["docker"] = onPath
	// A helper beside that CLI nobody checked, and the one checked elsewhere.
	require.NoError(t, os.WriteFile(filepath.Join(cliDir, "docker-credential-vault"), nil, 0o755))
	checked := filepath.Join(t.TempDir(), "docker-credential-vault")
	require.NoError(t, os.WriteFile(checked, nil, 0o755))
	r.onPath["docker-credential-vault"] = checked
	require.NoError(t, os.WriteFile(filepath.Join(r.rootConfig, "config.json"),
		[]byte(`{"credHelpers": {"b.example": "vault"}}`), 0o600))
	plugin := filepath.Join(r.rootConfig, "cli-plugins", "docker-compose")
	require.NoError(t, os.MkdirAll(filepath.Dir(plugin), 0o755))
	require.NoError(t, os.WriteFile(plugin, nil, 0o755))
	engine := r.engine()
	engine.setEnv = true

	cli, err := engine.Settle(context.Background())

	require.NoError(t, err)
	found, err := lookIn(os.Getenv("PATH"), "docker")
	require.NoError(t, err)
	assert.Equal(t, cli.Path, found, "a hook running docker by name finds another CLI than the one settled")
	helper, err := lookIn(os.Getenv("PATH"), "docker-credential-vault")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(r.managerDir, "docker", "helpers", "docker-credential-vault"), helper,
		"a helper beside the CLI answers before the one checked")
	assert.NoFileExists(t, filepath.Join(r.managerDir, "docker", "cli-plugins", "docker-compose"),
		"Docker Desktop's earlier plugin would load before the one checked")
}

// Nothing root runs is put under a manager directory whose way down someone
// other than root could change.
func TestTheManagerDirectoryMustBeRootsAllTheWayDown(t *testing.T) {
	r := newRig(t)
	r.replaceable[r.managerDir] = "uid 501"

	t.Setenv("PATH", os.Getenv("PATH"))
	t.Setenv("DOCKER_CONFIG", os.Getenv("DOCKER_CONFIG"))
	_, err := r.engine().Settle(context.Background())
	require.ErrorContains(t, err, "can be changed by uid 501")
	engine := r.engine()
	engine.setEnv = true
	require.Error(t, engine.Prepare())
	assert.NoDirExists(t, filepath.Join(r.managerDir, "docker"))
	assert.Equal(t, nowhere, os.Getenv("DOCKER_CONFIG"), "a refused config left a registry reached through another")
}

// A config that cannot be rebuilt is not left for a command to read, so none
// is read that this process did not check.
func TestAConfigThatCannotBeRebuiltIsNotLeft(t *testing.T) {
	r := newRig(t)
	dir := filepath.Join(r.managerDir, "docker")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"credsStore": "stale"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "helpers"), nil, 0o600))

	require.Error(t, r.engine().Prepare())

	assert.NoFileExists(t, filepath.Join(dir, "config.json"))
}

// Root finds a tool by name only where nobody but root could have put it, from
// before `init` makes the manager directory, and one found only elsewhere is
// refused by name rather than called missing (decision 25). `sudo` on a Mac
// keeps the user's PATH, which names their own directories and Homebrew's.
func TestRootFindsAToolByNameOnlyWhereOnlyRootCouldHavePutIt(t *testing.T) {
	t.Setenv("PATH", os.Getenv("PATH"))
	t.Setenv("DOCKER_CONFIG", os.Getenv("DOCKER_CONFIG"))
	ctx := context.Background()
	r := newRig(t)
	roots, users, sticky := t.TempDir(), t.TempDir(), t.TempDir()
	require.NoError(t, os.Chmod(sticky, os.ModeSticky|0o777))
	for _, dir := range []string{roots, users, sticky} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "sops"), nil, 0o755))
	}
	r.replaceable[users] = users + " is owned by uid 501, not root"
	given := strings.Join([]string{users, "relative", filepath.Join(roots, "missing"), sticky, roots}, string(os.PathListSeparator))
	ready := func() Runner {
		engine := r.engine()
		engine.setEnv, engine.given = true, given
		require.NoError(t, engine.Prepare())
		return Runner{Runner: exec.New(), Engine: engine}
	}

	r.managerDir = filepath.Join(r.managerDir, "missing")
	runner := ready()
	assert.Equal(t, roots, os.Getenv("PATH"))
	found, err := runner.Look("sops")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(roots, "sops"), found)

	// Found where only root can write, and itself someone else's.
	r.replaceable[found] = found + " is owned by uid 501, not root"
	_, err = runner.Look("sops")
	require.ErrorContains(t, err, "root does not run sops at "+found+": "+found+" is owned by uid 501, not root")
	assert.ErrorIs(t, err, domain.ErrToolMissing)

	require.NoError(t, os.Remove(found))
	_, err = runner.Look("sops")
	require.ErrorContains(t, err, "root does not run sops at "+filepath.Join(users, "sops")+": "+users+" is owned by uid 501")
	_, err = runner.Run(ctx, exec.Command{Argv: []string{"sops", "--version"}})
	require.ErrorContains(t, err, "root does not run sops at "+filepath.Join(users, "sops"))
	require.NoError(t, os.Remove(filepath.Join(users, "sops")))
	_, err = runner.Look("sops")
	require.ErrorContains(t, err, "every user can add a tool to "+sticky)
	_, err = runner.Look("age")
	require.ErrorContains(t, err, `"age" was not found in PATH`)

	// Once the manager directory exists, the checked helpers come first, then
	// what was kept, and nothing else.
	r.managerDir = filepath.Dir(r.managerDir)
	ready()
	assert.Equal(t, filepath.Join(r.managerDir, "docker", "helpers")+string(os.PathListSeparator)+roots, os.Getenv("PATH"))
}
