// Package quadlet is the Quadlet adapter: a deployment run by rootless Podman,
// each service a `.container` unit the user's own systemd manager starts (RFC
// 0023 P3b).
//
// The bundle's files reach the machine unchanged. The adapter installs them in
// the user's runtime directory with a drop-in of its own, and drives them with
// `systemctl --user` and `podman`:
//   - every file is named `<product>-<name>.<kind>`, so an installation's
//     units, containers, volumes and networks are its own
//     (logs/quadlet-adapter.md, Q1);
//   - a `.container` with `Type=oneshot` is one Up never starts (Q2);
//   - the drop-in references the adapter's own copy of the parameters, which
//     only Up writes, so Stop and Start bring back the values a service was
//     created with (RFC 0023 row 29);
//   - no unit carries an `[Install]` section, so only the manager starts one,
//     after it has rendered what the unit reads (row 28).
package quadlet

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/adapters/runtime/compose"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// Name is the key a manifest declares this runtime under.
const Name = "quadlet"

// Runtime drives a deployment's Quadlet units. It holds no state of its own:
// what is installed is read back from the unit directory on every call.
type Runtime struct {
	runner     exec.Runner
	podman     string
	systemctl  string
	journalctl string

	// generator is the Quadlet generator, found where Podman installs it
	// when empty.
	generator string

	// runtimeDir stands in for $XDG_RUNTIME_DIR, for tests.
	runtimeDir string

	onLine      func(exec.Line)
	redact      []string
	helperImage string

	// runID names a one-shot's transient unit.
	runID func() string
}

// New builds the adapter.
func New(runner exec.Runner, opts ...Option) *Runtime {
	r := &Runtime{
		runner: runner, podman: "podman", systemctl: "systemctl", journalctl: "journalctl",
		runID: randomID,
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// randomID is eight hex characters, enough that two one-shots of one service
// started in the same second do not share a unit.
func randomID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Option configures the adapter.
type Option func(*Runtime)

// WithOutputSink forwards each output line to fn, for the live view.
func WithOutputSink(fn func(exec.Line)) Option { return func(r *Runtime) { r.onLine = fn } }

// WithRedaction scrubs values from captured output, logs and errors.
func WithRedaction(values []string) Option { return func(r *Runtime) { r.redact = values } }

// WithHelperImage overrides the image volumes are read through, as
// compose.WithHelperImage does, trimmed and refused later when not pinned.
func WithHelperImage(ref string) Option {
	return func(r *Runtime) {
		if trimmed := strings.TrimSpace(ref); trimmed != "" {
			r.helperImage = trimmed
		}
	}
}

// WithRuntimeDir sets the directory $XDG_RUNTIME_DIR names, for tests.
func WithRuntimeDir(dir string) Option { return func(r *Runtime) { r.runtimeDir = dir } }

// WithGenerator sets the Quadlet generator's path.
func WithGenerator(path string) Option { return func(r *Runtime) { r.generator = path } }

// WithRunID sets how a one-shot's transient unit is named, for tests.
func WithRunID(fn func() string) Option { return func(r *Runtime) { r.runID = fn } }

var (
	_ ports.Runtime         = (*Runtime)(nil)
	_ ports.ToolRequirer    = (*Runtime)(nil)
	_ ports.ImageInspector  = (*Runtime)(nil)
	_ ports.ImageRemover    = (*Runtime)(nil)
	_ ports.ServiceImager   = (*Runtime)(nil)
	_ ports.VolumeInspector = (*Runtime)(nil)
	_ ports.VolumeCapturer  = (*Runtime)(nil)

	_ ports.DefinitionDrifter = (*Runtime)(nil)
)

// Name is the key a manifest declares this runtime under.
func (r *Runtime) Name() string { return Name }

// RequiredTools are Podman, which runs the containers, and systemctl, which
// starts their units.
func (r *Runtime) RequiredTools() []string { return []string{"podman", "systemctl"} }

// unitDir is where an installation's units live: a directory of the user's
// runtime directory that Quadlet reads, which a reboot clears. `apply
// --startup` writes the units again before it starts anything, so nothing
// starts at boot on what an earlier boot left (RFC 0023 row 28).
//
// Refused for root. A Quadlet installation runs as the user who owns it, with
// its units in that user's manager (RFC 0023 row 26), and rootful Podman is not
// built.
func (r *Runtime) unitDir(product string) (string, error) {
	if os.Geteuid() == 0 {
		return "", domain.Preflight(nil, "a Quadlet installation is not run by root").
			WithHint("run morzer as the user the installation belongs to; its units are that user's")
	}
	base := r.runtimeDir
	if base == "" {
		base = os.Getenv("XDG_RUNTIME_DIR")
	}
	if base == "" || !filepath.IsAbs(base) {
		return "", domain.Preflight(nil, "XDG_RUNTIME_DIR is not set, and Quadlet reads a user's units from it").
			WithHint("run morzer in the user's login session, or set XDG_RUNTIME_DIR to /run/user/%d", os.Getuid())
	}
	return filepath.Join(base, "containers", "systemd", "morzer", product), nil
}

// command is one run of a tool, with the environment the manager runs in,
// less what would send Podman elsewhere (localEnv). A unit's values reach it
// from its parameter file, not from here.
func (r *Runtime) command(timeout time.Duration, argv ...string) exec.Command {
	return exec.Command{
		Argv:          argv,
		Env:           localEnv(nil),
		Timeout:       timeout,
		Redact:        r.redact,
		OnLine:        r.onLine,
		CaptureOutput: true,
	}
}

// remoteEnv names the variables that send every `podman` command to a service
// elsewhere: a URL, or a connection `podman system connection` saved.
var remoteEnv = []string{"CONTAINER_HOST", "CONTAINER_CONNECTION"}

// localEnv is the manager's environment with overrides applied, and without
// remoteEnv. systemd runs the units on this user's Podman whatever the
// manager's shell holds, so the commands that drive them use that one too (RFC
// 0023 row 61).
func localEnv(overrides map[string]string) []string {
	return slices.DeleteFunc(exec.BaseEnv(overrides), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains(remoteEnv, name)
	})
}

// helperCommand is command in the shape compose.VolumeHelper takes.
func (r *Runtime) helperCommand(_ ports.RuntimeConfig, timeout time.Duration, argv ...string) exec.Command {
	return r.command(timeout, argv...)
}

// systemctlUser runs `systemctl --user`.
func (r *Runtime) systemctlUser(ctx context.Context, timeout time.Duration, args ...string) (exec.Result, error) {
	return r.runner.Run(ctx, r.command(timeout, append([]string{r.systemctl, "--user"}, args...)...))
}

// podmanRun runs podman.
func (r *Runtime) podmanRun(ctx context.Context, timeout time.Duration, args ...string) (exec.Result, error) {
	return r.runner.Run(ctx, r.command(timeout, append([]string{r.podman}, args...)...))
}

// volumes is the helper container volumes are read and written through,
// shared with Compose and run with Podman.
func (r *Runtime) volumes() compose.VolumeHelper {
	return compose.VolumeHelper{
		Runner:     r.runner,
		Bin:        r.podman,
		Image:      r.helperImage,
		Redact:     r.redact,
		HasImage:   r.HasImage,
		Command:    r.helperCommand,
		EngineHint: "check that Podman can run containers for this user",
		PullHint:   "run `podman pull %s` while this machine has a network, or `podman load` an archive of it",
	}
}
