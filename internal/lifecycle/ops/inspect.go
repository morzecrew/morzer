package ops

// Into the running deployment: logs, process state, resource use, and a
// command inside a container.
//
// Four operations with one thing in common, which is why they share a file:
// each needs the installation, the current release and the runtime
// configuration, and then delegates to the runtime port. None of them takes the
// deployment lock -- they are what an operator runs *while* something else is
// happening, which is exactly the case a lock would break.

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/logging"
	"github.com/morzecrew/morzer/internal/ports"
)

// deployment is the trio every command in this file resolves before it can ask
// the runtime anything.
type deployment struct {
	Installation domain.Installation
	Release      domain.Release
	Config       ports.RuntimeConfig
}

// resolveDeployment loads the installation, the current release and the runtime
// configuration.
//
// One function because getting it wrong three times in slightly different ways
// is how `logs` ends up reading a different project from `ps`. The Compose
// project name, the file list and the interpolated environment are all
// knowledge the manager has and an operator does not, which is the whole reason
// these commands exist.
func resolveDeployment(ctx context.Context, d *Deps) (deployment, error) {
	if d.Runtime == nil {
		return deployment{}, domain.Internal(nil, "no container runtime is configured")
	}

	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return deployment{}, err
	}

	current, err := d.State.CurrentRelease(ctx)
	if err != nil {
		return deployment{}, err
	}
	if current.IsZero() {
		return deployment{}, domain.InstallationError(domain.ErrReleaseNotFound,
			"no release is installed, so there is nothing running to look at").
			WithHint("install one with `morzer update <bundle>`")
	}

	rel, err := d.resolveCurrentRelease(ctx, current)
	if err != nil {
		return deployment{}, err
	}
	cfg, err := d.runtimeConfig(rel, inst, "")
	if err != nil {
		return deployment{}, err
	}
	return deployment{Installation: inst, Release: rel, Config: cfg}, nil
}

// ----------------------------------------------------------------------------
// logs

// LogsOptions is what the operator asked for.
type LogsOptions struct {
	Services []string
	Follow   bool
	Tail     int
	Since    time.Time

	// Structured asks for the frame as well as the text: the emitting
	// container and the instant it wrote the line, which costs the runtime
	// a timestamp on every line and the manager one listing to attribute
	// containers to services.
	Structured bool

	// Redact scrubs this installation's secret values from the stream. On
	// by default at the CLI layer; a caller that clears it is asking for
	// the bytes the container wrote.
	Redact bool
}

// LogStream is a log stream, already redacted, that the caller closes.
type LogStream struct {
	io.ReadCloser

	// services maps container name to service, so a structured line can say
	// which service a replica belongs to. Empty for an unstructured stream,
	// which needs no attribution.
	services map[string]string

	// RedactionArmed reports whether the secret values were loaded. False
	// means nothing could be scrubbed, which the CLI says out loud rather
	// than letting an operator read an unfiltered stream believing it was
	// filtered.
	RedactionArmed bool
}

// attributionTimeout bounds the one listing a structured stream needs.
//
// The same five seconds `ls --status` gives a runtime query, and for the same
// reason: what is being bought is a nicety, and paying for it with a command
// that never starts is the wrong trade on a machine somebody is already
// debugging.
const attributionTimeout = 5 * time.Second

// StreamLogs opens the deployment's logs.
//
// No lock, deliberately: reading logs must never queue behind an update, since
// during one is exactly when they are most wanted.
func StreamLogs(ctx context.Context, d *Deps, opts LogsOptions) (*LogStream, error) {
	dep, err := resolveDeployment(ctx, d)
	if err != nil {
		return nil, err
	}

	stream := &LogStream{}
	if opts.Structured {
		// One listing, so a line's container can be attributed to its
		// service. Failure is not fatal: an unattributed record still
		// carries the container that wrote it, which is more than the
		// raw stream gives a machine reader -- and a daemon too wedged
		// to list containers is one whose logs are worth reading.
		//
		// Bounded for that reason. This call is a convenience on the way
		// to the stream, and an unbounded one would turn "logs answer
		// when the daemon is struggling" into "logs hang before they
		// start", which is the promise this command exists to keep.
		listCtx, cancel := context.WithTimeout(ctx, attributionTimeout)
		states, err := d.Runtime.Status(listCtx, dep.Config)
		cancel()
		if err != nil {
			logging.FromContext(ctx).Warn(
				"cannot attribute log lines to services", "error", err)
		}
		stream.services = make(map[string]string, len(states))
		for _, s := range states {
			if s.Container != "" {
				stream.services[s.Container] = s.Name
			}
		}
	}

	if opts.Redact {
		stream.RedactionArmed = d.armRedaction(ctx)
	}

	reader, err := d.Runtime.Logs(ctx, dep.Config, ports.LogOptions{
		Services: opts.Services,
		Follow:   opts.Follow,
		Tail:     opts.Tail,
		Since:    opts.Since,
		// Only for the structured form: the human stream is the
		// runtime's own layout, which is what an operator is used to
		// reading and what every `docker compose logs` example shows.
		Timestamps: opts.Structured,
	})
	if err != nil {
		return nil, err
	}

	stream.ReadCloser = reader
	if opts.Redact && d.Redactor != nil {
		stream.ReadCloser = d.Redactor.Stream(reader)
	}
	return stream, nil
}

// armRedaction loads this installation's secret values so the redactor can
// recognise them in vendor output.
//
// Best effort, and it reports whether it worked. Refusing to show logs because
// the secret state will not decrypt would take the tool away at the moment it
// is most needed -- a machine whose sops key is missing is a machine somebody is
// already debugging -- and pretending the stream was filtered would be worse
// than either. So the answer travels with the stream and the CLI says it.
func (d *Deps) armRedaction(ctx context.Context) bool {
	if d.Redactor == nil || d.Secrets == nil {
		return false
	}
	set, err := d.Secrets.Load(ctx)
	if err != nil {
		logging.FromContext(ctx).Warn(
			"cannot load the secret values, so nothing can be scrubbed from this stream",
			"error", err)
		return false
	}
	d.Redactor.RegisterSet(set)
	return true
}

// Lines takes the stream apart into records, calling yield for each.
//
// The framing is the port's contract, not Compose trivia: every runtime frames
// a line with the container that wrote it, so this parse belongs to the
// lifecycle layer rather than to one adapter.
//
// A line that does not carry a frame -- the runtime's own narration about a
// container that exited -- is yielded whole as text. Dropping it would hide the
// one line that explains why the rest stopped.
func (s *LogStream) Lines(yield func(ports.LogLine) error) error {
	scanner := bufio.NewScanner(s.ReadCloser)
	// A little above the bound the stream redactor holds a line to, so the
	// two agree about what a line is: a scanner that gave up first would
	// end the stream with `token too long` on a line the filter had already
	// decided to pass. Reachable only with redaction off, which is the one
	// case nothing upstream has bounded.
	scanner.Buffer(nil, maxStructuredLine)

	for scanner.Scan() {
		if err := yield(parseLogLine(scanner.Text(), s.services)); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return domain.RuntimeError(err,
				"a log line longer than %d bytes cannot be turned into a record",
				maxStructuredLine).
				WithHint("`morzer logs` without --json streams it unchanged")
		}
		return domain.RuntimeError(err, "the log stream ended early")
	}
	return nil
}

// maxStructuredLine is the longest line the structured form will frame.
//
// Above the redactor's own bound rather than equal to it, so a line that filter
// passed whole is never one this reader refuses.
const maxStructuredLine = logging.MaxRedactedLine + 4<<10

// parseLogLine splits one framed line.
func parseLogLine(raw string, services map[string]string) ports.LogLine {
	prefix, text, framed := strings.Cut(raw, "| ")
	if !framed {
		// Not a service line: the runtime narrating about the stream
		// itself. It has no container and no timestamp, and it is the
		// line an operator most needs when a container is restarting.
		return ports.LogLine{Text: raw}
	}

	out := ports.LogLine{Container: strings.TrimSpace(prefix), Text: text}
	out.Service = services[out.Container]

	// The instant, when the stream was asked for one. Parsed rather than
	// trusted: a container that writes something timestamp-shaped as its
	// first token would otherwise have its own text eaten.
	stamp, rest, split := strings.Cut(text, " ")
	if !split {
		return out
	}
	at, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return out
	}
	out.At, out.Text = at, rest
	return out
}

// ----------------------------------------------------------------------------
// ps

// ListServices reports what the project is running.
//
// The same slice `status` computes, on its own: `status` answers four questions
// at once, and an operator watching a crash loop wants one of them repeatedly.
func ListServices(ctx context.Context, d *Deps) ([]ports.ServiceState, error) {
	dep, err := resolveDeployment(ctx, d)
	if err != nil {
		return nil, err
	}
	return d.Runtime.Status(ctx, dep.Config)
}

// ----------------------------------------------------------------------------
// stats

// SampleStats reads resource use once.
//
// Once, because the cadence is the caller's: `--watch` calls this on a timer and
// a script loops around `stats --json`, and both are the same operation sampled
// at a different rate.
func SampleStats(ctx context.Context, d *Deps) ([]ports.ServiceStats, error) {
	dep, err := resolveDeployment(ctx, d)
	if err != nil {
		return nil, err
	}
	return d.Runtime.Stats(ctx, dep.Config)
}

// ----------------------------------------------------------------------------
// exec

// ExecOptions names the service and what to run in it.
//
// Deliberately not embedding Options. None of them applies: there is no lock to
// wait for, nothing destructive for --force to authorise, and no plan for
// --dry-run to produce -- the command is the operator's own and the manager
// cannot say what it would do. Embedding the struct and reading none of it
// would be a promise the code does not keep, which is how `--dry-run` ends up
// silently running somebody's `rm`.
type ExecOptions struct {
	Service string

	// Argv is the command inside the container and nothing else. The
	// adapter appends it after the service name, so a runtime-level option
	// written here reaches the process rather than the runtime.
	Argv []string
}

// ExecResult is what the command inside the container did.
//
// The exit code is the command's own, propagated so `morzer exec db -- psql -c
// 'select 1'` fails an invocation that failed. Output is returned to the caller
// and never journalled.
type ExecResult struct {
	Service  string        `json:"service"`
	ExitCode int           `json:"exit_code"`
	Stdout   string        `json:"stdout,omitempty"`
	Stderr   string        `json:"stderr,omitempty"`
	Duration time.Duration `json:"duration_ms"`
}

// ExecInService runs a command inside a running service.
//
// Journalled, and the argv is redacted before it is written. The journal's job
// here is that a human was in there at 03:14 and what they asked for; what it
// must not become is a store of the credentials they typed, and
// `morzer exec db -- psql 'postgresql://u:p@host/db'` puts a password in an
// argv as a matter of course.
//
// No lock. An operator reading state with `psql` while an update runs is the
// case this exists for.
func ExecInService(ctx context.Context, d *Deps, opts ExecOptions) (ExecResult, error) {
	if len(opts.Argv) == 0 {
		return ExecResult{}, domain.Usage("a command to run inside %s is required", opts.Service).
			WithHint("morzer exec %s -- <command> [args…]", opts.Service)
	}

	dep, err := resolveDeployment(ctx, d)
	if err != nil {
		return ExecResult{}, err
	}

	// Refused here rather than left to the runtime, which reports a
	// container that does not exist and says nothing about which services
	// do. Asked before the redactor is armed and before anything is
	// journalled: a refused command is not something a human did inside the
	// deployment.
	if err := confirmRunning(ctx, d, dep, opts.Service); err != nil {
		return ExecResult{}, err
	}

	// Armed before the record is built, because the record carries the argv
	// and an unarmed redactor would write the operator's password into the
	// journal verbatim.
	d.armRedaction(ctx)

	started := d.now()
	res, err := d.Runtime.Exec(ctx, dep.Config, opts.Service, opts.Argv)
	record := d.execRecord(dep, opts, started, res, err)
	if journalErr := d.State.AppendOperation(ctx, record); journalErr != nil {
		// Logged and dropped. The command ran inside the container
		// whether or not the manager could write it down, and failing
		// the invocation afterwards would report a failure that did not
		// happen while leaving whatever it did in place.
		logging.FromContext(ctx).Error("cannot journal the exec", "error", journalErr)
	}
	if err != nil {
		return ExecResult{Service: opts.Service}, err
	}

	return ExecResult{
		Service:  opts.Service,
		ExitCode: res.ExitCode,
		Stdout:   res.Stdout,
		Stderr:   res.Stderr,
		Duration: res.Duration,
	}, nil
}

// confirmRunning refuses a service that is not up, naming the state it is in.
func confirmRunning(ctx context.Context, d *Deps, dep deployment, service string) error {
	states, err := d.Runtime.Status(ctx, dep.Config)
	if err != nil {
		return err
	}

	names := make([]string, 0, len(states))
	for _, s := range states {
		names = append(names, s.Name)
		if s.Name != service {
			continue
		}
		if s.State == ports.StateRunning {
			return nil
		}
		// The state, because "exited (137)" and "restarting" send an
		// operator to different places -- and the second is why they
		// were reaching for `exec` in the first place.
		return domain.RuntimeError(domain.ErrRuntime,
			"service %s is %s, so there is no container to run a command in",
			service, s.State).
			WithHint("`morzer logs %s` says why; `morzer apply` brings it back", service)
	}

	hint := "run `morzer ps` to see what this deployment runs"
	if len(names) > 0 {
		hint = "this deployment runs " + strings.Join(names, ", ")
	}
	return domain.Usage("this deployment has no service named %q", service).
		WithHint("%s", hint)
}

// execRecord is the journal entry for one exec.
//
// It records the argv and never the output: what a later reader needs is that
// somebody was inside the deployment and what they asked it to do. The output
// is arbitrary vendor data plus whatever the operator's command printed, and a
// journal that held it would be a second copy of the product's data in a file
// nobody thinks of as one.
func (d *Deps) execRecord(
	dep deployment,
	opts ExecOptions,
	started time.Time,
	res ports.ExitResult,
	runErr error,
) domain.OperationRecord {
	status := domain.StatusSucceeded
	var failure *domain.Error
	switch {
	case runErr != nil:
		status, failure = domain.StatusFailed, domain.AsError(runErr)
	case res.ExitCode != 0:
		// The command ran and said no. That is not a failure of the
		// manager, and journalling it as one would make every
		// `grep -c 'select 1'` look like an incident -- but the exit
		// code is on the record, so an incident review can see it.
		status = domain.StatusSucceeded
	}

	flags := map[string]string{
		"service":   opts.Service,
		"argv":      d.redactArgv(opts.Argv),
		"exit_code": strconv.Itoa(res.ExitCode),
	}

	return domain.OperationRecord{
		SchemaVersion:  domain.OperationSchemaVersion,
		ID:             d.newOpID(),
		Type:           domain.OpTypeExec,
		Status:         status,
		StartedAt:      domain.NewTime(started),
		FinishedAt:     domain.NewTime(d.now()),
		ManagerVersion: d.ManagerVersion.String(),
		InstallationID: dep.Installation.ID,
		Flags:          flags,
		Error:          failure,
	}
}

// redactArgv renders the command for the journal with known secrets scrubbed.
//
// What it cannot catch is said plainly in the documentation rather than implied
// away: a redactor matches the values it has been told about, and a token an
// operator pasted from somewhere else is not one of them.
func (d *Deps) redactArgv(argv []string) string {
	joined := strings.Join(argv, " ")
	if d.Redactor == nil {
		return joined
	}
	return d.Redactor.Apply(joined)
}
