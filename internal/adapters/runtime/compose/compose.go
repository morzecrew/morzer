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

// WithDockerBinary overrides the docker executable, for tests and for hosts
// where it is not on the default PATH.
func WithDockerBinary(path string) Option {
	return func(r *Runtime) { r.docker = path }
}

// WithOutputSink forwards subprocess output.
func WithOutputSink(fn func(exec.Line)) Option {
	return func(r *Runtime) { r.onLine = fn }
}

// WithRedaction registers values to scrub from output and errors.
func WithRedaction(values []string) Option {
	return func(r *Runtime) { r.redact = values }
}

var _ ports.Runtime = (*Runtime)(nil)

// args builds a `docker compose` invocation for a project.
//
// The project name and every file are passed explicitly on each call rather
// than relying on the working directory or COMPOSE_PROJECT_NAME. Implicit
// project selection is how a command ends up acting on the wrong deployment.
func (r *Runtime) args(cfg ports.RuntimeConfig, rest ...string) []string {
	argv := []string{r.docker, "compose"}
	if cfg.Project != "" {
		argv = append(argv, "--project-name", cfg.Project)
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
		Argv:          argv,
		Dir:           cfg.WorkingDir,
		Env:           exec.BaseEnv(cfg.Env),
		Timeout:       timeout,
		Redact:        r.redact,
		OnLine:        r.onLine,
		CaptureOutput: true,
	}
}

// Validate parses and checks the merged configuration without side effects.
//
// `docker compose config` does the merging, interpolation, and schema
// validation that Compose itself will do later, so a configuration error
// surfaces during preflight rather than at the moment containers are being
// started.
func (r *Runtime) Validate(ctx context.Context, cfg ports.RuntimeConfig) (ports.Rendered, error) {
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
	names := make([]string, 0, len(doc.Services))
	for name := range doc.Services {
		names = append(names, name)
	}
	// Sorted so plans and diffs are stable between runs.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	return names, nil
}

// Pull fetches images.
//
// Compose pulls what the merged configuration references, which is what the
// manifest pinned by digest. The image list is passed for the progress
// reporting and for the contract test; Compose resolves the rest itself.
func (r *Runtime) Pull(ctx context.Context, cfg ports.RuntimeConfig, images []string) error {
	argv := r.args(cfg, "pull", "--policy", "missing")
	cmd := r.command(cfg, 30*time.Minute, argv...)

	if _, err := r.runner.Run(ctx, cmd); err != nil {
		return wrapExit(err, "cannot pull images",
			"check registry reachability and credentials; `morzer doctor` tests both")
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

// parsePS handles both shapes Compose has emitted across versions: a JSON
// array, and newline-delimited objects. Supporting both is cheaper than
// pinning a Compose version.
func parsePS(raw string) ([]ports.ServiceState, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	var entries []psEntry
	if strings.HasPrefix(raw, "[") {
		if err := json.Unmarshal([]byte(raw), &entries); err != nil {
			return nil, domain.RuntimeError(err, "cannot parse compose ps output")
		}
	} else {
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var e psEntry
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				return nil, domain.RuntimeError(err, "cannot parse compose ps output")
			}
			entries = append(entries, e)
		}
	}

	out := make([]ports.ServiceState, 0, len(entries))
	for _, e := range entries {
		name := e.Service
		if name == "" {
			name = e.Name
		}
		out = append(out, ports.ServiceState{
			Name:     name,
			Image:    e.Image,
			State:    strings.ToLower(e.State),
			Health:   normaliseHealth(e.Health),
			ExitCode: e.ExitCode,
			Status:   e.Status,
		})
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

var _ ports.RegistryProber = (*Runtime)(nil)
