// Package compose implements ports.Runtime over the `docker compose` CLI.
//
// Parsing and running are treated as different problems. Everything that
// mutates goes through the binary, because reimplementing Compose's
// orchestration would be exactly the "reimplement rather than coordinate"
// mistake the design forbids. Where a machine-readable format exists
// (`--format json`), it is used: parsing human-readable output is a bug
// waiting for the next release of the tool.
package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// Name is the provider name a manifest selects with providers.runtime.name.
const Name = "compose"

// Runtime is the Compose adapter.
type Runtime struct {
	runner exec.Runner

	// docker is the base command. `docker compose` (the plugin) is the
	// only supported form: standalone docker-compose v1 is end-of-life and
	// its behaviour differs in ways that would need a second code path.
	docker string

	// onLine forwards subprocess output to the live view. Set by the
	// lifecycle layer for the step it is running.
	onLine func(exec.Line)

	// redact is the secret list handed to every subprocess.
	redact []string

	// helperImage is the container a volume's contents are read and
	// written through. Empty means DefaultHelperImage.
	helperImage string
}

// New returns a Compose runtime.
func New(runner exec.Runner, opts ...Option) *Runtime {
	r := &Runtime{runner: runner, docker: "docker"}
	for _, o := range opts {
		o(r)
	}
	return r
}

type Option func(*Runtime)

// WithOutputSink forwards subprocess output.
func WithOutputSink(fn func(exec.Line)) Option {
	return func(r *Runtime) { r.onLine = fn }
}

// WithRedaction registers values to scrub from output and errors.
func WithRedaction(values []string) Option {
	return func(r *Runtime) { r.redact = values }
}

var (
	_ ports.Runtime        = (*Runtime)(nil)
	_ ports.OptionResolver = (*Runtime)(nil)
)

// args builds a `docker compose` invocation for a project.
//
// The project name and every file are passed explicitly on each call rather
// than relying on the working directory or COMPOSE_PROJECT_NAME. Implicit
// project selection is how a command ends up acting on the wrong deployment.
func (r *Runtime) args(cfg ports.RuntimeConfig, rest ...string) []string {
	argv := []string{r.docker, "compose"}
	if p := r.project(cfg); p != "" {
		argv = append(argv, "--project-name", p)
	}
	if cfg.WorkingDir != "" {
		argv = append(argv, "--project-directory", cfg.WorkingDir)
	}
	for _, f := range cfg.Files {
		argv = append(argv, "--file", f)
	}
	for _, p := range cfg.Profiles {
		argv = append(argv, "--profile", p)
	}
	return append(argv, rest...)
}

func (r *Runtime) command(cfg ports.RuntimeConfig, timeout time.Duration, argv ...string) exec.Command {
	return exec.Command{
		Argv: argv,
		Dir:  cfg.WorkingDir,
		// Filtered, not inherited: a declared parameter is the only
		// way an operator value reaches a Compose file. See
		// exec.FilteredEnv.
		Env:           exec.FilteredEnv(exec.PassthroughEnv, cfg.Env),
		Timeout:       timeout,
		Redact:        r.redact,
		OnLine:        r.onLine,
		CaptureOutput: true,
	}
}

// Name is the key a manifest declares this runtime under.
//
// The literal lives here rather than above `internal/adapters` deliberately:
// this is the layer that is allowed to know a runtime's name, and the whole
// point of the port method is that nothing higher up has to.
func (r *Runtime) Name() string { return "compose" }

// OptionProject is the manifest option naming this deployment's Compose
// project -- the namespace containers, networks and volumes are created in.
//
// Declared here because this is the layer that knows what a project is. The
// manager carries the option and bounds its shape; it does not know that
// `project` means anything, and a manager that did would be branching on a
// runtime's name to find out (RFC 0023 decision 7).
const OptionProject = "project"

// project resolves the Compose project name for a configuration.
//
// The option when the vendor set one, the product name otherwise. The fallback
// lives here rather than in the manifest's defaults, where it used to: filling
// it in up there made every release carry a project whether or not it had asked
// for one, and a release on the `runtimes:` spelling inherited it from a field
// on the deprecated block.
//
// It is the namespace durable things are created in, so changing it between
// releases points a deployment at different volumes. That is the manager's
// refusal to make, not this adapter's -- the installation records what it was
// created with.
func (r *Runtime) project(cfg ports.RuntimeConfig) string {
	if p := cfg.Options[OptionProject]; p != "" {
		return p
	}
	return cfg.Product
}

// ResolveOptions reports the options as this runtime will read them.
//
// One default to fill in, and it is the one that matters: an absent `project`
// means the product name, so a release that writes that name out in full is
// declaring what was already in force rather than renaming anything. The
// manager compares these rather than the declared map, which is what lets it
// tell those two apart (ports.OptionResolver).
//
// Everything else is copied through untouched, including keys this runtime does
// not understand. They are still compared, and Validate is where an unknown one
// is refused -- dropping it here would turn "this release changed a setting you
// cannot see" into silence.
func (r *Runtime) ResolveOptions(cfg ports.RuntimeConfig) map[string]string {
	resolved := make(map[string]string, len(cfg.Options)+1)
	maps.Copy(resolved, cfg.Options)
	resolved[OptionProject] = r.project(cfg)
	return resolved
}

// checkOptions refuses a manifest option this runtime has never heard of.
//
// The manager cannot make this refusal -- it has no list, and building one up
// there would be the layer above the adapters holding a catalogue of what each
// runtime understands. So an unknown key survives until here, and here it
// stops, from Validate: the path `apply`, `doctor` and `release verify
// --render-check` all run before anything is deployed.
//
// Refused rather than ignored, because ignoring fails silently and expensively.
// A vendor who mistypes `project` deploys under the product name instead of the
// one they chose, and Compose creates a fresh set of volumes for it without
// complaint -- the same rename this whole wave exists to stop.
func checkOptions(options map[string]string) error {
	known := map[string]bool{OptionProject: true}

	unknown := make([]string, 0, len(options))
	for key := range options {
		if !known[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)

	names := slices.Sorted(maps.Keys(known))

	return domain.ValidationError(nil,
		"this release sets runtime options the compose runtime does not know: %s",
		strings.Join(unknown, ", ")).
		WithHint("it understands: %s", strings.Join(names, ", "))
}

// HookVars is this runtime's contribution to the hook ABI.
//
// One variable, and the same one vendors' hooks have always read. It is
// supplied rather than core because a project is Compose's own idea: under a
// runtime that has none, the variable is absent rather than empty, which a hook
// can test for (RFC 0023 §2.2).
func (r *Runtime) HookVars(cfg ports.RuntimeConfig) map[string]string {
	p := r.project(cfg)
	if p == "" {
		return nil
	}
	return map[string]string{"COMPOSE_PROJECT": p}
}

// RequiredTools names what has to be on the host before this runtime can do
// anything, in the order an operator should read them.
//
// Both, and the second is not redundant: the daemon and the CLI plugin are
// separately installable and separately versioned, and a host with `docker` but
// no `compose` plugin is a real machine that fails at the first operation with
// an error about an unknown subcommand. They are also two entries in the tool
// catalogue with two different probes, so naming one would check one.
//
// The names are the catalogue's, spelled here because this is the layer allowed
// to know them -- the same argument as Name above.
func (r *Runtime) RequiredTools() []string { return []string{"docker", "compose"} }

// Validate parses and checks the merged configuration without side effects.
//
// `docker compose config` does the merging, interpolation, and schema
// validation that Compose itself will do later, so a configuration error
// surfaces during preflight rather than at the moment containers are being
// started.
func (r *Runtime) Validate(ctx context.Context, cfg ports.RuntimeConfig) (ports.Rendered, error) {
	if err := checkOptions(cfg.Options); err != nil {
		return ports.Rendered{}, err
	}
	cmd := r.command(cfg, 60*time.Second, r.args(cfg, "config", "--format", "json")...)
	// The merged config is data, not progress: streaming it into the live
	// view would flood it with YAML.
	cmd.OnLine = nil

	res, err := r.runner.Run(ctx, cmd)
	if err != nil {
		return ports.Rendered{}, wrapExit(err, "compose configuration is invalid",
			"run `docker compose config` against the release to see the full diagnostic")
	}

	services, err := serviceNames(res.Stdout)
	if err != nil {
		return ports.Rendered{}, err
	}
	return ports.Rendered{Config: []byte(res.Stdout), Services: services}, nil
}

// serviceNames extracts service names from `compose config --format json`.
func serviceNames(raw string) ([]string, error) {
	var doc struct {
		Services map[string]json.RawMessage `json:"services"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return nil, domain.RuntimeError(err, "cannot parse the merged compose configuration")
	}
	// Sorted so plans and diffs are stable between runs.
	return slices.Sorted(maps.Keys(doc.Services)), nil
}

// Pull fetches images.
//
// Compose pulls what the merged configuration references, which is what the
// manifest pinned by digest. The image list is passed for the progress
// reporting and for the contract test; Compose resolves the rest itself.
func (r *Runtime) Pull(ctx context.Context, cfg ports.RuntimeConfig, images []string) error {
	// The images the manifest pins, not the ones the Compose file happens to
	// name.
	//
	// `docker compose pull` was what this used to run, and it ignored the
	// argument entirely. The two agree only for as long as every service
	// interpolates a manifest image -- and diverge silently the moment one
	// does not, at which point the release pulls something nobody pinned.
	// The manifest is the authority on what a release consists of.
	if len(images) == 0 {
		return nil
	}

	for _, ref := range images {
		// Already here is already correct: a digest-pinned reference
		// cannot mean different bytes on a second pull, and skipping is
		// what lets a boot-time apply work without a network.
		if present, err := r.HasImage(ctx, ref); err == nil && present {
			continue
		}

		cmd := r.command(cfg, 30*time.Minute, r.docker, "pull", ref)
		if _, err := r.runner.Run(ctx, cmd); err != nil {
			return wrapExit(err, "cannot pull "+domain.ShortImageRef(ref),
				"check registry reachability and credentials; `morzer doctor` tests both")
		}
	}
	return nil
}

// Up converges the project to running.
//
// It is idempotent by construction: Compose reconciles against the declared
// state, so calling Up on a converged project changes nothing.
func (r *Runtime) Up(ctx context.Context, cfg ports.RuntimeConfig, opts ports.UpOptions) error {
	argv := r.args(cfg, "up", "--detach")
	if opts.Wait {
		argv = append(argv, "--wait")
		if opts.WaitTimeout > 0 {
			argv = append(argv, "--wait-timeout", strconv.Itoa(int(opts.WaitTimeout.Seconds())))
		}
	}
	if opts.RemoveOrphans {
		argv = append(argv, "--remove-orphans")
	}
	argv = append(argv, opts.Services...)

	timeout := opts.WaitTimeout
	if timeout > 0 {
		// Give Compose room past its own wait timeout to report the
		// failure, rather than killing it at the same instant and
		// losing the diagnostic.
		timeout += 60 * time.Second
	}

	if _, err := r.runner.Run(ctx, r.command(cfg, timeout, argv...)); err != nil {
		return wrapExit(err, "cannot start services",
			"run `morzer status` for per-service state, or `docker compose logs` for detail")
	}
	return nil
}

// Down stops the project.
//
// Volumes are removed only when explicitly requested. That flag traces back to
// an operator confirmation at the CLI layer -- nothing in the lifecycle layer
// sets it on its own, because a compensation that deletes application data
// would be worse than the failure it is undoing.
func (r *Runtime) Down(ctx context.Context, cfg ports.RuntimeConfig, opts ports.DownOptions) error {
	argv := r.args(cfg, "down")
	if opts.Volumes {
		argv = append(argv, "--volumes")
	}
	if opts.RemoveOrphans {
		argv = append(argv, "--remove-orphans")
	}
	if opts.Timeout > 0 {
		argv = append(argv, "--timeout", strconv.Itoa(int(opts.Timeout.Seconds())))
	}

	if _, err := r.runner.Run(ctx, r.command(cfg, 10*time.Minute, argv...)); err != nil {
		return wrapExit(err, "cannot stop services", "")
	}
	return nil
}

func (r *Runtime) Restart(ctx context.Context, cfg ports.RuntimeConfig, services []string) error {
	argv := append(r.args(cfg, "restart"), services...)
	if _, err := r.runner.Run(ctx, r.command(cfg, 10*time.Minute, argv...)); err != nil {
		return wrapExit(err, "cannot restart services", "")
	}
	return nil
}

// Stop halts services without removing anything.
//
// `compose stop` rather than `compose down`: down removes containers and
// networks, and the caller here means to put back exactly what it took away.
func (r *Runtime) Stop(ctx context.Context, cfg ports.RuntimeConfig, services []string, timeout time.Duration) error {
	argv := r.args(cfg, "stop")
	if timeout > 0 {
		// Rounded up, never down to zero. Compose takes whole seconds,
		// so a sub-second grace truncates to `--timeout 0` -- which is
		// an immediate SIGKILL, the opposite of the small grace the
		// caller asked for.
		seconds := int(timeout.Seconds())
		if seconds < 1 {
			seconds = 1
		}
		argv = append(argv, "--timeout", strconv.Itoa(seconds))
	}
	argv = append(argv, services...)

	// The subprocess bound has to outlast the grace Compose was given, or
	// the runner kills the CLI mid-stop and the services stay up while the
	// caller is told they were stopped.
	limit := timeout + 5*time.Minute
	if limit < 15*time.Minute {
		limit = 15 * time.Minute
	}

	if _, err := r.runner.Run(ctx, r.command(cfg, limit, argv...)); err != nil {
		return wrapExit(err, "cannot stop services", "")
	}
	return nil
}

// Start starts what Stop halted.
//
// `compose start` and emphatically not `compose up`: up reconciles against the
// declared configuration and will recreate a container whose definition has
// drifted. Resuming a stack after a backup must not be the thing that applies a
// change nobody asked for.
func (r *Runtime) Start(ctx context.Context, cfg ports.RuntimeConfig, services []string) error {
	argv := append(r.args(cfg, "start"), services...)
	if _, err := r.runner.Run(ctx, r.command(cfg, 15*time.Minute, argv...)); err != nil {
		return wrapExit(err, "cannot start services",
			"run `morzer status` for per-service state, or `docker compose logs` for detail")
	}
	return nil
}

// RunOneShot runs a service to completion -- migrations above all.
//
// A non-zero exit is returned as data in ExitResult rather than as an error,
// because the caller decides what it means: a migration exiting 2 means
// "nothing to do" under the hook ABI, not failure.
func (r *Runtime) RunOneShot(ctx context.Context, cfg ports.RuntimeConfig, service string, opts ports.RunOptions) (ports.ExitResult, error) {
	argv := r.args(cfg, "run", "--no-deps")
	if opts.Remove {
		argv = append(argv, "--rm")
	}
	for k, v := range opts.Env {
		// Values here come from the hook ABI, never from the secret
		// store: argv is world-readable through /proc.
		argv = append(argv, "--env", k+"="+v)
	}
	argv = append(argv, service)
	argv = append(argv, opts.Argv...)

	res, err := r.runner.Run(ctx, r.command(cfg, opts.Timeout, argv...))
	out := ports.ExitResult{
		ExitCode: res.ExitCode,
		Stdout:   res.Stdout,
		Stderr:   res.Stderr,
		Duration: res.Duration,
	}

	if err != nil {
		var exitErr *exec.ExitError
		if asExit(err, &exitErr) {
			// The process ran and failed: that is a result.
			out.ExitCode = exitErr.ExitCode
			return out, nil
		}
		return out, wrapExit(err, fmt.Sprintf("cannot run service %q", service), "")
	}
	return out, nil
}

func (r *Runtime) Exec(ctx context.Context, cfg ports.RuntimeConfig, service string, argv []string) (ports.ExitResult, error) {
	full := append(r.args(cfg, "exec", "--no-TTY", service), argv...)

	res, err := r.runner.Run(ctx, r.command(cfg, 5*time.Minute, full...))
	out := ports.ExitResult{
		ExitCode: res.ExitCode,
		Stdout:   res.Stdout,
		Stderr:   res.Stderr,
		Duration: res.Duration,
	}
	if err != nil {
		var exitErr *exec.ExitError
		if asExit(err, &exitErr) {
			out.ExitCode = exitErr.ExitCode
			return out, nil
		}
		return out, wrapExit(err, fmt.Sprintf("cannot exec in service %q", service), "")
	}
	return out, nil
}

// psEntry is the shape `docker compose ps --format json` emits, one object per
// line.
type psEntry struct {
	Name     string `json:"Name"`
	Service  string `json:"Service"`
	Image    string `json:"Image"`
	State    string `json:"State"`
	Health   string `json:"Health"`
	ExitCode int    `json:"ExitCode"`
	Status   string `json:"Status"`
}

// Status reports the state of every service.
func (r *Runtime) Status(ctx context.Context, cfg ports.RuntimeConfig) ([]ports.ServiceState, error) {
	argv := r.args(cfg, "ps", "--all", "--format", "json")
	cmd := r.command(cfg, 60*time.Second, argv...)
	cmd.OnLine = nil

	res, err := r.runner.Run(ctx, cmd)
	if err != nil {
		return nil, wrapExit(err, "cannot read service status",
			"check that the Docker daemon is running: `docker info`")
	}
	return parsePS(res.Stdout)
}

// decodeJSONLines reads both shapes the docker CLI has emitted across
// versions: a JSON array, and newline-delimited objects.
//
// One reader for every `--format json` this adapter parses. Which shape arrives
// is a property of the tool and not of the command, so two copies of the rule
// would be two places to notice the day a Compose release changes it -- and the
// second copy is how one of them stops being noticed.
func decodeJSONLines[T any](raw, what string) ([]T, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	var out []T
	if strings.HasPrefix(raw, "[") {
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return nil, domain.RuntimeError(err, "cannot parse %s output", what)
		}
		return out, nil
	}
	for line := range strings.SplitSeq(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry T
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, domain.RuntimeError(err, "cannot parse %s output", what)
		}
		out = append(out, entry)
	}
	return out, nil
}

func parsePSEntries(raw string) ([]psEntry, error) {
	return decodeJSONLines[psEntry](raw, "compose ps")
}

// parsePS reduces the listing to the port's vocabulary.
func parsePS(raw string) ([]ports.ServiceState, error) {
	entries, err := parsePSEntries(raw)
	if err != nil {
		return nil, err
	}

	out := make([]ports.ServiceState, 0, len(entries))
	for _, e := range entries {
		name := e.Service
		if name == "" {
			// A listing that named no service: the container is the
			// only handle there is, so it becomes the name and the
			// Container field stays empty rather than repeating it
			// as an instance the listing never reported.
			name = e.Name
		}
		state := ports.ServiceState{
			Name:     name,
			Image:    e.Image,
			State:    strings.ToLower(e.State),
			Health:   normaliseHealth(e.Health),
			ExitCode: e.ExitCode,
			Status:   e.Status,
		}
		if e.Service != "" {
			state.Container = e.Name
		}
		out = append(out, state)
	}
	return out, nil
}

// normaliseHealth maps Docker's health strings onto the port's vocabulary.
// An empty string means no healthcheck is declared, which is distinct from
// unknown -- absence of a probe is not evidence of illness.
func normaliseHealth(s string) ports.ServiceHealth {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "healthy":
		return ports.HealthHealthy
	case "unhealthy":
		return ports.HealthUnhealthy
	case "starting":
		return ports.HealthStarting
	case "":
		return ports.HealthNone
	default:
		return ports.HealthUnknown
	}
}

// Logs streams service logs.
//
// This is the one method that does not go through the shared runner: the
// caller wants a live stream it closes itself, and the runner's model is
// run-to-completion.
func (r *Runtime) Logs(ctx context.Context, cfg ports.RuntimeConfig, opts ports.LogOptions) (io.ReadCloser, error) {
	argv := r.args(cfg, "logs", "--no-color")
	if opts.Timestamps {
		argv = append(argv, "--timestamps")
	}
	if opts.Follow {
		argv = append(argv, "--follow")
	}
	if opts.Tail > 0 {
		argv = append(argv, "--tail", strconv.Itoa(opts.Tail))
	}
	if !opts.Since.IsZero() {
		argv = append(argv, "--since", opts.Since.Format(time.RFC3339))
	}
	argv = append(argv, opts.Services...)

	return startStream(ctx, argv, cfg)
}

// wrapExit turns an exec error into a runtime domain error, preserving a
// process's own diagnostic rather than replacing it with a generic message.
func wrapExit(err error, message, hint string) error {
	if err == nil {
		return nil
	}
	// An interruption or a preflight failure is already correctly typed;
	// re-wrapping it as a runtime error would send it to the wrong exit
	// code.
	if de := domain.AsError(err); de.Code == domain.CodeInterrupted || de.Code == domain.CodePreflight {
		return err
	}

	e := domain.RuntimeError(err, "%s", message)
	if hint != "" {
		e = e.WithHint("%s", hint)
	}
	return e
}

// HasImage reports whether an image is already in the local store.
//
// `docker image inspect` answers from the daemon's own index without touching
// the network, which is what makes this usable as the pre-flight for an offline
// install: an operator can ask on a connected machine whether a disconnected
// one would come up.
//
// A non-zero exit means absent; anything else means the question could not be
// answered, and saying so beats reporting "absent" for a daemon that is down.
func (r *Runtime) HasImage(ctx context.Context, imageRef string) (bool, error) {
	res, err := r.runner.Run(ctx, exec.Command{
		Argv:          []string{r.docker, "image", "inspect", imageRef},
		Timeout:       30 * time.Second,
		Redact:        r.redact,
		CaptureOutput: true,
	})
	if err == nil {
		return true, nil
	}

	var exitErr *exec.ExitError
	if !asExit(err, &exitErr) {
		return false, domain.RuntimeError(err, "cannot inspect local images")
	}

	stderr := strings.ToLower(res.Stderr + exitErr.Stderr)
	if strings.Contains(stderr, "no such image") || strings.Contains(stderr, "not found") {
		return false, nil
	}
	return false, domain.RuntimeError(err, "cannot inspect %s: %s",
		imageRef, firstLine(exitErr.Stderr))
}

// ProbeRegistry checks that an image's registry is reachable, without
// transferring any layers.
//
// `docker manifest inspect` fetches only the manifest document, which makes
// this cheap enough for `doctor` to run on every invocation. Distinguishing
// "unreachable" from "unauthorised" matters: the remedies are a network fix
// and a `docker login` respectively.
func (r *Runtime) ProbeRegistry(ctx context.Context, imageRef string) error {
	res, err := r.runner.Run(ctx, exec.Command{
		Argv:          []string{r.docker, "manifest", "inspect", imageRef},
		Timeout:       30 * time.Second,
		Redact:        r.redact,
		CaptureOutput: true,
	})
	if err == nil {
		return nil
	}

	var exitErr *exec.ExitError
	if !asExit(err, &exitErr) {
		return domain.RuntimeError(err, "cannot probe the registry")
	}

	stderr := strings.ToLower(res.Stderr + exitErr.Stderr)
	switch {
	case strings.Contains(stderr, "unauthorized") || strings.Contains(stderr, "authentication required"):
		return domain.RuntimeError(err, "authentication required").
			WithHint("run `docker login <registry>` as the user the manager runs as")
	case strings.Contains(stderr, "manifest unknown") || strings.Contains(stderr, "not found"):
		return domain.RuntimeError(err, "the image does not exist in the registry").
			WithHint("the release may reference a digest that was never published")
	default:
		return domain.RuntimeError(err, "%s", firstLine(exitErr.Stderr))
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unreachable"
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

var (
	_ ports.RegistryProber = (*Runtime)(nil)
	_ ports.ImageInspector = (*Runtime)(nil)
)
