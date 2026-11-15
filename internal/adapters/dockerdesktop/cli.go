package dockerdesktop

import (
	"context"
	"encoding/json"
	"errors"
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
// passed on (decision 9), bumped by the release pull request that records a
// newer one. File sharing and ownership are the least documented part of it.
var Verified = []string{"4.44"}

// pluginDirs is where the docker CLI looks for a plugin after the config's
// own directories, in its order, with Homebrew's, which its formula asks to be
// named in a config, before them.
var pluginDirs = []string{
	"/opt/homebrew/lib/docker/cli-plugins",
	"/usr/local/lib/docker/cli-plugins", "/usr/local/libexec/docker/cli-plugins",
	"/usr/lib/docker/cli-plugins", "/usr/libexec/docker/cli-plugins",
}

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

	// setPath puts the settled CLI's directory first on the process's
	// PATH, so every process the manager starts finds it (decision 22).
	setPath bool

	mu  sync.Mutex
	cli *ports.EngineCLI
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
		setPath:     true,
	}
}

// ConfigDir is the manager's DOCKER_CONFIG, for every process it starts
// (decision 22), whether or not it exists yet.
func (e *Engine) ConfigDir() string { return e.dir }

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
	if err := atomicfs.MkdirMineNoFollow(e.dir, 0o700); err != nil {
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
	} else {
		cli, pluginDir, err = e.fromPath()
	}
	if err != nil {
		return ports.EngineCLI{}, err
	}
	if cli.DroppedHelpers, err = e.writeConfig(pluginDir); err != nil {
		return ports.EngineCLI{}, err
	}
	cli.Verified = Verified
	if e.setPath {
		path := filepath.Dir(cli.Path) + string(os.PathListSeparator) + startEnv()["PATH"]
		if err := os.Setenv("PATH", path); err != nil {
			return ports.EngineCLI{}, domain.Internal(err, "cannot put %s first on PATH", filepath.Dir(cli.Path))
		}
	}
	e.cli = &cli
	return cli, nil
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
	for _, c := range copies {
		if err := e.copyChecked(ctx, c.from, c.to); err != nil {
			return ports.EngineCLI{}, err
		}
	}
	if err := atomicfs.WriteFile(stamp, []byte(version), 0o600); err != nil {
		return ports.EngineCLI{}, err
	}
	return cli, nil
}

// copyChecked copies from beside to, checks the copy's signature, and only
// then moves it over to. The original is never run, so it cannot be swapped
// between the check and the run.
func (e *Engine) copyChecked(ctx context.Context, from, to string) error {
	if err := atomicfs.MkdirMineNoFollow(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	next := to + ".new"
	defer func() { _ = os.Remove(next) }()
	if err := copyFile(from, next); err != nil {
		return err
	}
	requirement := `anchor apple generic and certificate leaf[subject.OU] = "` + TeamID + `"`
	if _, err := e.runner.Run(ctx, exec.Command{
		Argv: []string{"/usr/bin/codesign", "--verify", "--strict", "-R=" + requirement, next},
	}); err != nil {
		return domain.Preflight(err, "the copy of %s is not signed by Docker (team %s), so root does not run it", from, TeamID).
			WithHint("reinstall Docker Desktop from docker.com; the copies made before stay in use until one passes")
	}
	if err := os.Rename(next, to); err != nil {
		return domain.Internal(err, "cannot put the checked copy of %s in place", from)
	}
	atomicfs.SyncDir(filepath.Dir(to))
	return nil
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
	if raw, err := e.rootConfigField("credsStore"); err == nil && raw != nil {
		var name string
		if json.Unmarshal(raw, &name) == nil && name != "" {
			if e.helperTrusted(name) {
				out["credsStore"] = name
			} else {
				dropped = append(dropped, name)
			}
		}
	}
	if raw, err := e.rootConfigField("credHelpers"); err == nil && raw != nil {
		var helpers map[string]string
		if json.Unmarshal(raw, &helpers) == nil {
			kept := map[string]string{}
			for registry, name := range helpers {
				if e.helperTrusted(name) {
					kept[registry] = name
				} else if !slices.Contains(dropped, name) {
					dropped = append(dropped, name)
				}
			}
			if len(kept) > 0 {
				out["credHelpers"] = kept
			}
		}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, domain.Internal(err, "cannot encode the docker CLI's config")
	}
	slices.Sort(dropped)
	return dropped, atomicfs.WriteFile(filepath.Join(e.dir, "config.json"), data, 0o600)
}

func (e *Engine) helperTrusted(name string) bool {
	path, err := e.look("docker-credential-" + name)
	if err != nil {
		return false
	}
	who, err := e.replaceable(path)
	return err == nil && who == ""
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
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", domain.Preflight(nil, "%s was not found on PATH", name)
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return domain.Preflight(err, "cannot read %s", from)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return domain.Internal(err, "cannot create %s", to)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return domain.Internal(err, "cannot copy %s", from)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return domain.Internal(err, "cannot write %s", to)
	}
	return out.Close()
}
