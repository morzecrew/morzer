package dockerdesktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// The docker CLI root runs on a Mac (RFC 0055 decisions 18, 21 and 22).
//
// Docker Desktop's CLI and Compose plugin are its user's to replace, and so is
// every directory down to them, so root never runs them where they are. They
// are copied into a directory only root can write, checked there for Docker's
// signature, and run from there. Without Docker Desktop, the CLI on PATH runs
// only if nobody but root can replace it or the plugin it loads.

const (
	// App is where Docker Desktop's installer puts it, and the only place
	// read (logs/macos-host.md).
	App = "/Applications/Docker.app"

	// TeamID is the code-signing team of Docker, Inc. (decision 18).
	TeamID = "9BNSXJN65R"
)

// Verified is the Docker Desktop releases, major.minor, the acceptance run last
// passed on (decision 9), filled by the release pull request that records one.
// File sharing and ownership are the least documented part of it.
var Verified []string

// defaultStore is the credential helper the docker CLI looks for on PATH when
// its config names no store and no credentials, on a Mac.
const defaultStore = "osxkeychain"

// nowhere is DOCKER_CONFIG while the manager's config is not rebuilt: a
// directory that cannot exist, so a registry is reached anonymously.
var nowhere = filepath.Join(os.DevNull, "docker")

// noneHelper stands in, in the helpers' directory, for every credential helper
// root does not run: it holds no credentials, so a registry is reached without
// any rather than through a helper someone else could replace.
const noneHelper = `#!/bin/sh
# A credential helper root does not run: it holds no credentials.
case "$1" in
get) echo "credentials not found in native keychain"; exit 1 ;;
list) echo "{}" ;;
*) echo "this helper stores nothing" >&2; exit 1 ;;
esac
`

// systemPluginDirs are the docker CLI's own plugin directories, which it
// searches whatever its config names.
var systemPluginDirs = []string{
	"/usr/local/lib/docker/cli-plugins", "/usr/local/libexec/docker/cli-plugins",
	"/usr/lib/docker/cli-plugins", "/usr/libexec/docker/cli-plugins",
}

// pluginDirs is where a Compose plugin for a CLI on PATH is looked for after
// root's own config's: Homebrew's, which its formula asks to be named in a
// config, then the docker CLI's system directories, in its order.
var pluginDirs = append([]string{"/opt/homebrew/lib/docker/cli-plugins"}, systemPluginDirs...)

// startEnv is DOCKER_CONFIG and PATH as the process started, before they are
// replaced: root's own config is read from there, and the CLI and its helpers
// are looked up on the PATH root was given.
var startEnv = sync.OnceValue(func() map[string]string {
	return map[string]string{"DOCKER_CONFIG": os.Getenv("DOCKER_CONFIG"), "PATH": os.Getenv("PATH")}
})

// Engine settles the CLI for one installation, at the first command that
// needs it, and keeps the answer for the process.
type Engine struct {
	// dir is VarDir/manager/docker: the copies, the config and the
	// version they were copied at. It is DOCKER_CONFIG.
	dir    string
	runner exec.Runner
	app    string

	// rootConfig is root's own config directory, look a PATH lookup and
	// replaceable atomicfs.ReplaceableByNonRoot; a test stands them in.
	rootConfig  string
	look        func(string) (string, error)
	replaceable func(string) (string, error)

	// setEnv gives the process, and so every process the manager starts,
	// the settled CLI's directory first on PATH and the manager's
	// DOCKER_CONFIG (decision 22).
	setEnv bool

	mu  sync.Mutex
	cli *ports.EngineCLI
	// pluginDir is the directory the manager's config names for plugins.
	pluginDir string
}

// NewEngine returns the settler for an installation whose manager directory is
// managerDir.
func NewEngine(runner exec.Runner, managerDir string) *Engine {
	start := startEnv()
	// The docker CLI's rule, as the OCI source reads it: DOCKER_CONFIG, then
	// HOME, then the passwd entry, since launchd gives a daemon no HOME.
	rootConfig := start["DOCKER_CONFIG"]
	if rootConfig == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			if u, uerr := user.Current(); uerr == nil {
				home = u.HomeDir
			}
		}
		if home != "" {
			rootConfig = filepath.Join(home, ".docker")
		}
	}
	return &Engine{
		dir: filepath.Join(managerDir, "docker"), runner: runner, app: App,
		rootConfig:  rootConfig,
		look:        func(name string) (string, error) { return lookIn(start["PATH"], name) },
		replaceable: atomicfs.ReplaceableByNonRoot,
		setEnv:      true,
	}
}

// Settle returns the CLI to run, copying and checking it the first time, and
// again when Docker Desktop's version changes. A refusal is not kept, so a
// later command tries again once its cause is gone.
func (e *Engine) Settle(ctx context.Context) (ports.EngineCLI, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cli != nil {
		return *e.cli, nil
	}
	managerDir := filepath.Dir(e.dir)
	if _, err := os.Stat(managerDir); err != nil {
		return ports.EngineCLI{}, domain.Preflight(err,
			"the docker CLI root runs is kept in %s, which `init` creates", managerDir)
	}
	if err := e.ready(); err != nil {
		return ports.EngineCLI{}, err
	}

	var (
		cli       ports.EngineCLI
		pluginDir string
		err       error
	)
	if _, statErr := os.Stat(filepath.Join(e.app, "Contents", "Info.plist")); statErr == nil {
		cli, err = e.copyDesktop(ctx)
		pluginDir = filepath.Join(e.dir, "cli-plugins")
	} else if cli, pluginDir, err = e.fromPath(); err == nil {
		// Docker Desktop's earlier copies go: the CLI loads a plugin from
		// its config's directory before any other.
		_ = os.Remove(filepath.Join(e.dir, "bin", "docker"))
		_ = os.RemoveAll(filepath.Join(e.dir, "cli-plugins"))
		_ = os.Remove(filepath.Join(e.dir, "version"))
	}
	if err != nil {
		return ports.EngineCLI{}, err
	}
	if cli.DroppedHelpers, err = e.writeConfig(pluginDir); err != nil {
		return ports.EngineCLI{}, err
	}
	cli.Verified = Verified
	if err := e.useConfig(filepath.Dir(cli.Path)); err != nil {
		return ports.EngineCLI{}, err
	}
	e.cli, e.pluginDir = &cli, pluginDir
	return cli, nil
}

// BeforeHook refuses a hook until the CLI is settled, and while a plugin the
// CLI would find could be replaced by someone other than root: a hook that
// lists plugins, `docker info` say, runs every one it finds as root (RFC 0055
// decision 24). The manager's own commands list none.
func (e *Engine) BeforeHook(ctx context.Context) error {
	if _, err := e.Settle(ctx); err != nil {
		return err
	}
	e.mu.Lock()
	dirs := append([]string{filepath.Join(e.dir, "cli-plugins"), e.pluginDir}, systemPluginDirs...)
	e.mu.Unlock()
	for _, dir := range dirs {
		if err := e.pluginsTrusted(dir); err != nil {
			return err
		}
	}
	return nil
}

// addable says who other than root could add an entry to a directory, which
// a sticky bit does not stop, or "".
func addable(p string, info fs.FileInfo) string {
	_, gid := atomicfs.Owner(info)
	switch mode := info.Mode(); {
	case mode&0o002 != 0:
		return fmt.Sprintf("every user can add a plugin to %s", p)
	case mode&0o020 != 0:
		return fmt.Sprintf("group %d can add a plugin to %s", gid, p)
	}
	return ""
}

// pluginsTrusted refuses a plugin directory, or an entry in it, that someone
// other than root could replace. One that does not exist is judged by the
// nearest directory above it that does, where it could be made
// (logs/macos-host.md).
func (e *Engine) pluginsTrusted(dir string) error {
	refuse := func(who string) error {
		return domain.Preflight(nil, "a hook may list the docker CLI's plugins, which runs each one as root, and %s", who).
			WithHint("make it root's alone, or remove it; root runs no hook until then")
	}
	at := dir
	for {
		_, err := os.Lstat(at)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return domain.Internal(err, "cannot check the plugin directory %s", dir)
		}
		at = filepath.Dir(at)
	}
	if who, err := e.replaceable(at); err != nil || who != "" {
		if err != nil {
			return err
		}
		return refuse(who)
	}
	// The rule asks who could replace an entry, which a sticky directory
	// lets nobody else do. Anyone who can write it can still add a plugin,
	// or make the directory that is missing.
	info, err := os.Stat(at)
	if err != nil {
		return domain.Internal(err, "cannot check the plugin directory %s", dir)
	}
	if who := addable(at, info); who != "" {
		return refuse(who)
	}
	if at != dir {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return domain.Internal(err, "cannot list the plugin directory %s", dir)
	}
	for _, entry := range entries {
		who, err := e.replaceable(filepath.Join(dir, entry.Name()))
		if err != nil {
			return err
		}
		if who != "" {
			return refuse(who)
		}
	}
	return nil
}

// ready makes the manager's directory for the CLI, refusing one whose way down
// someone other than root could change, since what root runs is put there.
func (e *Engine) ready() error {
	managerDir := filepath.Dir(e.dir)
	who, err := e.replaceable(managerDir)
	if err != nil {
		return err
	}
	if who != "" {
		return domain.Preflight(nil, "%s can be changed by %s, so root keeps no docker CLI under it", managerDir, who)
	}
	return atomicfs.MkdirMineNoFollow(e.dir, 0o700)
}

// Prepare rebuilds the manager's config from root's own, and puts its CLI
// directory first on PATH, before anything reaches a registry, an update's
// download of its release included: a config an earlier run left would name
// helpers this process has not checked. Before `init` creates the manager
// directory there is nothing to rebuild, and a registry is reached
// anonymously (logs/macos-host.md).
func (e *Engine) Prepare() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	// Until it is rebuilt, a registry is reached anonymously.
	if e.setEnv {
		if err := os.Setenv("DOCKER_CONFIG", nowhere); err != nil {
			return domain.Internal(err, "cannot set DOCKER_CONFIG")
		}
	}
	if _, err := os.Stat(filepath.Dir(e.dir)); err != nil {
		return nil
	}
	if err := e.ready(); err != nil {
		return err
	}
	// Replaced in one rename, so a command reading it meanwhile reads a
	// whole config; removed when it cannot be rebuilt, so none is read that
	// this process did not check.
	if _, err := e.writeConfig(filepath.Join(e.dir, "cli-plugins")); err != nil {
		_ = os.Remove(filepath.Join(e.dir, "config.json"))
		return err
	}
	return e.useConfig("")
}

// useConfig gives the process the manager's DOCKER_CONFIG, and its PATH the
// checked helpers' directory first, then the settled CLI's, so every process
// the manager starts finds those by name (decision 22). The helpers come first:
// that directory holds nothing else, and a CLI's own directory can hold a
// helper of the same name nobody checked.
func (e *Engine) useConfig(cliDir string) error {
	if !e.setEnv {
		return nil
	}
	dirs := []string{filepath.Join(e.dir, "helpers")}
	if cliDir != "" {
		dirs = append(dirs, cliDir)
	}
	path := strings.Join(append(dirs, startEnv()["PATH"]), string(os.PathListSeparator))
	if err := os.Setenv("PATH", path); err != nil {
		return domain.Internal(err, "cannot put %s first on PATH", strings.Join(dirs, ", "))
	}
	if err := os.Setenv("DOCKER_CONFIG", e.dir); err != nil {
		return domain.Internal(err, "cannot set DOCKER_CONFIG")
	}
	return nil
}

// Runner runs every docker command with the settled CLI, and refuses one when
// none can be settled.
type Runner struct {
	exec.Runner
	Engine *Engine
}

func (r Runner) Run(ctx context.Context, cmd exec.Command) (exec.Result, error) {
	if len(cmd.Argv) > 0 && cmd.Argv[0] == "docker" {
		cli, err := r.Engine.Settle(ctx)
		if err != nil {
			return exec.Result{}, err
		}
		cmd.Argv = append([]string{cli.Path}, cmd.Argv[1:]...)
	}
	return r.Runner.Run(ctx, cmd)
}

func (r Runner) Look(name string) (string, error) {
	if name != "docker" {
		return r.Runner.Look(name)
	}
	cli, err := r.Engine.Settle(context.Background())
	return cli.Path, err
}

// copyDesktop copies Docker Desktop's CLI and Compose plugin when its version
// differs from the one the copies were made at, checking each copy before it
// replaces the last (decision 18).
func (e *Engine) copyDesktop(ctx context.Context) (ports.EngineCLI, error) {
	plist := filepath.Join(e.app, "Contents", "Info.plist")
	res, err := e.runner.Run(ctx, exec.Command{
		Argv: []string{"/usr/bin/plutil", "-extract", "CFBundleShortVersionString", "raw", "-o", "-", plist},
	})
	if err != nil {
		return ports.EngineCLI{}, domain.Preflight(err, "cannot read Docker Desktop's version from %s", plist)
	}
	version := strings.TrimSpace(res.Stdout)

	cli := ports.EngineCLI{Path: filepath.Join(e.dir, "bin", "docker"), DesktopVersion: version}
	stamp := filepath.Join(e.dir, "version")
	copies := []struct{ from, to string }{
		{filepath.Join(e.app, "Contents", "Resources", "bin", "docker"), cli.Path},
		{filepath.Join(e.app, "Contents", "Resources", "cli-plugins", "docker-compose"),
			filepath.Join(e.dir, "cli-plugins", "docker-compose")},
	}
	if have, err := os.ReadFile(stamp); err == nil && string(have) == version &&
		!slices.ContainsFunc(copies, func(c struct{ from, to string }) bool { return !exists(c.to) }) {
		return cli, nil
	}
	// Both are checked before either is placed, so a refusal keeps the pair
	// checked before rather than a CLI beside another release's plugin.
	var checked []string
	defer func() {
		for _, path := range checked {
			_ = os.Remove(path)
		}
	}()
	for _, c := range copies {
		path, err := e.checkedCopy(ctx, c.from, filepath.Dir(c.to))
		if path != "" {
			checked = append(checked, path)
		}
		if err != nil {
			return ports.EngineCLI{}, err
		}
	}
	for i, c := range copies {
		if err := os.Rename(checked[i], c.to); err != nil {
			return ports.EngineCLI{}, domain.Internal(err, "cannot put the checked copy of %s in place", c.from)
		}
		atomicfs.SyncDir(filepath.Dir(c.to))
	}
	checked = nil
	if err := atomicfs.WriteFile(stamp, []byte(version), 0o600); err != nil {
		return ports.EngineCLI{}, err
	}
	return cli, nil
}

// checkedCopy copies from into dir under a name of its own and checks the
// copy's signature, returning where it is. The original is never run, so it
// cannot be swapped between the check and the run; and no other process, root
// copying the same release at once, writes to that name, so it cannot be
// swapped between the check and the rename either.
func (e *Engine) checkedCopy(ctx context.Context, from, dir string) (string, error) {
	if err := atomicfs.MkdirMineNoFollow(dir, 0o700); err != nil {
		return "", err
	}
	out, err := os.CreateTemp(dir, "."+filepath.Base(from)+"-*.new")
	if err != nil {
		return "", domain.Internal(err, "cannot create a copy of %s", from)
	}
	path := out.Name()
	if err := copyTo(from, out); err != nil {
		return path, err
	}
	requirement := `anchor apple generic and certificate leaf[subject.OU] = "` + TeamID + `"`
	if _, err := e.runner.Run(ctx, exec.Command{
		Argv: []string{"/usr/bin/codesign", "--verify", "--strict", "-R=" + requirement, path},
	}); err != nil {
		return path, domain.Preflight(err, "the copy of %s is not signed by Docker (team %s), so root does not run it", from, TeamID).
			WithHint("reinstall Docker Desktop from docker.com; the copies made before stay in use until one passes")
	}
	return path, nil
}

// fromPath is decision 21: the CLI on PATH, and the Compose plugin it would
// load, each run only if nobody but root can replace it.
func (e *Engine) fromPath() (ports.EngineCLI, string, error) {
	docker, err := e.look("docker")
	if err != nil {
		return ports.EngineCLI{}, "", domain.Preflight(err, "neither Docker Desktop at %s nor a docker CLI on PATH was found", e.app).
			WithHint("install Docker Desktop")
	}
	if err := e.trusted(docker, "the docker CLI"); err != nil {
		return ports.EngineCLI{}, "", err
	}
	var dirs []string
	if extra, err := e.rootConfigField("cliPluginsExtraDirs"); err == nil && extra != nil {
		_ = json.Unmarshal(extra, &dirs)
	}
	if e.rootConfig != "" {
		dirs = append(dirs, filepath.Join(e.rootConfig, "cli-plugins"))
	}
	dirs = append(dirs, pluginDirs...)
	for _, dir := range dirs {
		plugin := filepath.Join(dir, "docker-compose")
		if !exists(plugin) {
			continue
		}
		if err := e.trusted(plugin, "the Compose plugin"); err != nil {
			return ports.EngineCLI{}, "", err
		}
		return ports.EngineCLI{Path: docker}, dir, nil
	}
	return ports.EngineCLI{}, "", domain.Preflight(nil, "the docker CLI at %s has no Compose plugin in %s", docker, strings.Join(dirs, ", "))
}

// trusted refuses a binary root would run that someone else could replace.
func (e *Engine) trusted(path, what string) error {
	who, err := e.replaceable(path)
	if err != nil {
		return err
	}
	if who != "" {
		return domain.Preflight(nil, "%s at %s can be replaced by %s, so root does not run it", what, path, who).
			WithHint("install Docker Desktop, or install %s where only root can write", what)
	}
	return nil
}

// writeConfig writes the manager's config: root's `auths`, a credential helper
// only when nobody but root can replace it, and the plugin directory searched
// first. It returns the helpers it left out. Nothing else of root's config is
// carried: a context, say, would point the CLI away from the socket the engine
// user is read from (decision 19).
func (e *Engine) writeConfig(pluginDir string) ([]string, error) {
	out := map[string]any{"cliPluginsExtraDirs": []string{pluginDir}}
	if auths, err := e.rootConfigField("auths"); err == nil && auths != nil {
		out["auths"] = auths
	}
	var dropped []string
	linked := map[string]string{}
	keep := func(name string) bool {
		path, ok := e.checkedHelper(name)
		if ok {
			linked["docker-credential-"+name] = path
		} else if !slices.Contains(dropped, name) {
			dropped = append(dropped, name)
		}
		return ok
	}
	if raw, err := e.rootConfigField("credsStore"); err == nil && raw != nil {
		var name string
		if json.Unmarshal(raw, &name) == nil && name != "" && keep(name) {
			out["credsStore"] = name
		}
	}
	if raw, err := e.rootConfigField("credHelpers"); err == nil && raw != nil {
		var helpers map[string]string
		if json.Unmarshal(raw, &helpers) == nil {
			kept := map[string]string{}
			for registry, name := range helpers {
				if keep(name) {
					kept[registry] = name
				}
			}
			if len(kept) > 0 {
				out["credHelpers"] = kept
			}
		}
	}
	// Before the config, so every helper it names resolves to its link.
	if err := e.linkHelpers(linked, append(slices.Clone(dropped), defaultStore)); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, domain.Internal(err, "cannot encode the docker CLI's config")
	}
	if err := atomicfs.WriteFile(filepath.Join(e.dir, "config.json"), data, 0o600); err != nil {
		return nil, err
	}
	slices.Sort(dropped)
	return dropped, nil
}

// checkedHelper is where docker-credential-<name> is on root's PATH, when
// nobody but root can replace it.
func (e *Engine) checkedHelper(name string) (string, bool) {
	path, err := e.look("docker-credential-" + name)
	if err != nil {
		return "", false
	}
	who, err := e.replaceable(path)
	return path, err == nil && who == ""
}

// linkHelpers links each kept helper into the helpers' directory, first on
// PATH, and points every other name the CLI may run there at noneHelper: the
// ones left out, the store the CLI looks for when its config names none, and
// any linked before, which a command that read an earlier config may still run.
// A helper is run by name, and a directory on root's PATH that someone else can
// write, or a link into Docker Desktop, would otherwise answer to it.
func (e *Engine) linkHelpers(linked map[string]string, shadowed []string) error {
	dir := filepath.Join(e.dir, "helpers")
	if err := atomicfs.MkdirMineNoFollow(dir, 0o700); err != nil {
		return err
	}
	none := filepath.Join(dir, ".none")
	if err := atomicfs.WriteFile(none, []byte(noneHelper), 0o700); err != nil {
		return err
	}
	for name, target := range linked {
		if err := atomicfs.ReplaceSymlink(target, filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	names := map[string]bool{}
	for _, name := range shadowed {
		names["docker-credential-"+name] = true
	}
	earlier, _ := filepath.Glob(filepath.Join(dir, "docker-credential-*"))
	for _, path := range earlier {
		names[filepath.Base(path)] = true
	}
	for name := range names {
		if _, kept := linked[name]; kept {
			continue
		}
		if err := atomicfs.ReplaceSymlink(none, filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

// rootConfigField reads one key of root's own config.json; an absent file or
// key is nil.
func (e *Engine) rootConfigField(key string) (json.RawMessage, error) {
	if e.rootConfig == "" {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(e.rootConfig, "config.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return doc[key], nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// lookIn is exec.LookPath against a given PATH.
func lookIn(path, name string) (string, error) {
	for _, dir := range filepath.SplitList(path) {
		candidate := filepath.Join(dir, name)
		//nolint:gosec // a lookup on root's own PATH, as exec.LookPath makes
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", domain.Preflight(nil, "%s was not found on PATH", name)
}

// copyTo copies from into out, executable, and closes out.
func copyTo(from string, out *os.File) error {
	defer func() { _ = out.Close() }()
	in, err := os.Open(from)
	if err != nil {
		return domain.Preflight(err, "cannot read %s", from)
	}
	defer func() { _ = in.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		return domain.Internal(err, "cannot copy %s", from)
	}
	if err := out.Chmod(0o755); err != nil {
		return domain.Internal(err, "cannot set the mode of %s", out.Name())
	}
	if err := out.Sync(); err != nil {
		return domain.Internal(err, "cannot write %s", out.Name())
	}
	return out.Close()
}
