// Package hooks implements the hook ABI: the contract between the manager and
// the executables a release ships.
//
// Hooks are the only way to add product-specific logic without changing the
// manager, so this ABI is a public, versioned contract. Everything here is
// therefore deliberately conservative: a documented environment, a structured
// result channel that is not stdout, three exit-code meanings, and a timeout
// that reaches the whole process group.
package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
)

// ResultFD is the descriptor a hook writes its structured result to.
//
// It is not stdout: stdout goes to the log and the live view, and a hook that
// had to keep its human output free of JSON would be a hook whose logging is
// constrained by the manager's parsing. Separating them means a hook can print
// whatever it likes and still return data.
const ResultFD = 3

// Exit-code meanings. Anything not listed is a failure.
const (
	// ExitSuccess means the hook did its work.
	ExitSuccess = 0
	// ExitSkipped means there was nothing to do. It is distinct from
	// success so `apply` can report "migrations: nothing to run" rather
	// than implying work happened.
	ExitSkipped = 2
)

// Phase is the lifecycle point a hook is invoked at, passed as <P>_PHASE.
type Phase string

const (
	PhasePreflight   Phase = "preflight"
	PhasePreUpdate   Phase = "pre-update"
	PhasePostUpdate  Phase = "post-update"
	PhaseMigrate     Phase = "migrate"
	PhaseSmokeTest   Phase = "smoke-test"
	PhaseBackup      Phase = "backup"
	PhaseRestore     Phase = "restore"
	PhaseHealthCheck Phase = "health-check"
)

// Env is everything a hook is told about the world it runs in.
//
// The field set is the stable part of the ABI. Adding a variable is a minor
// change; removing or repurposing one is not.
type Env struct {
	Product        string
	InstallationID string
	OperationID    string
	OperationType  domain.OperationType
	Phase          Phase

	ReleaseVersion  domain.Version
	ReleaseDir      string
	PreviousVersion domain.Version

	DataDir    string
	BackupDir  string
	SecretsDir string
	ConfigFile string

	ComposeProject string

	DryRun   bool
	LogLevel string

	// Extra carries operation-specific variables, already fully named.
	Extra map[string]string
}

// prefix derives the environment-variable prefix from the product name.
//
// Variables are namespaced per product rather than under a fixed MORZER_
// prefix because hooks ship inside a product's own release: the author always
// knows the name, and the namespacing keeps two products' hooks from colliding
// if they ever run in the same shell.
func prefix(product string) string {
	p := strings.ToUpper(product)
	p = strings.ReplaceAll(p, "-", "_")
	p = strings.ReplaceAll(p, ".", "_")
	if p == "" {
		return "PRODUCT"
	}
	return p
}

// Prefix is the environment-variable namespace for this product. Exported so
// the lifecycle layer can name Compose interpolation variables the same way,
// keeping one convention rather than two.
func (e Env) Prefix() string { return prefix(e.Product) }

// Vars renders the environment as a map.
func (e Env) Vars() map[string]string {
	p := prefix(e.Product)
	set := func(m map[string]string, key, value string) {
		if value != "" {
			m[p+"_"+key] = value
		}
	}

	out := make(map[string]string, 16)
	set(out, "PRODUCT", e.Product)
	set(out, "INSTALLATION_ID", e.InstallationID)
	set(out, "OPERATION_ID", e.OperationID)
	set(out, "OPERATION_TYPE", string(e.OperationType))
	set(out, "PHASE", string(e.Phase))
	set(out, "RELEASE_VERSION", e.ReleaseVersion.String())
	set(out, "RELEASE_DIR", e.ReleaseDir)
	set(out, "PREVIOUS_VERSION", e.PreviousVersion.String())
	set(out, "DATA_DIR", e.DataDir)
	set(out, "BACKUP_DIR", e.BackupDir)
	set(out, "SECRETS_DIR", e.SecretsDir)
	set(out, "CONFIG_FILE", e.ConfigFile)
	set(out, "COMPOSE_PROJECT", e.ComposeProject)
	set(out, "LOG_LEVEL", e.LogLevel)

	// DRY_RUN is always present, including as "0". A hook checking for the
	// variable's existence rather than its value would otherwise mutate
	// during a plan.
	out[p+"_DRY_RUN"] = boolVar(e.DryRun)
	out[p+"_RESULT_FD"] = strconv.Itoa(ResultFD)

	for k, v := range e.Extra {
		out[k] = v
	}
	return out
}

func boolVar(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// Result is what a hook may report through the result descriptor. Every field
// is optional: a hook that writes nothing is not in error.
type Result struct {
	// Message is a one-line summary for the operator.
	Message string `json:"message,omitempty"`

	// Skipped lets a hook say it did nothing while still exiting zero.
	Skipped bool `json:"skipped,omitempty"`

	// SchemaVersion is how a migrate hook reports the database schema it
	// left behind. Rollback needs this, and asking the product later would
	// mean running its tooling just to pose a question it already answered.
	SchemaVersion int `json:"schema_version,omitempty"`

	// Artifacts are files the hook produced, e.g. a database dump.
	Artifacts []Artifact `json:"artifacts,omitempty"`

	// Data is free-form output for hooks with something else to say.
	Data map[string]any `json:"data,omitempty"`
}

// Artifact is a file a hook produced, with its checksum so the backup manifest
// is self-describing.
type Artifact struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

// Outcome is the full result of running a hook.
type Outcome struct {
	ExitCode int
	Skipped  bool
	Result   Result
	Stdout   string
	Stderr   string
	Duration time.Duration
}

// Runner executes hooks.
type Runner struct {
	runner exec.Runner
	redact []string
	onLine func(exec.Line)
}

func NewRunner(runner exec.Runner, opts ...Option) *Runner {
	r := &Runner{runner: runner}
	for _, o := range opts {
		o(r)
	}
	return r
}

type Option func(*Runner)

func WithRedaction(values []string) Option {
	return func(r *Runner) { r.redact = values }
}

func WithOutputSink(fn func(exec.Line)) Option {
	return func(r *Runner) { r.onLine = fn }
}

// Run executes a hook from a release.
//
// The command is resolved against the release root, so a hook named `backup`
// runs the bundle's `hooks/backup` and never something on PATH that shares its
// name. Hooks run only from a release that has already been verified.
func (r *Runner) Run(ctx context.Context, rel domain.Release, command []string, env Env, timeout time.Duration) (Outcome, error) {
	if len(command) == 0 {
		return Outcome{}, domain.Internal(nil, "hook invoked with no command")
	}

	argv := append([]string(nil), command...)
	resolved, err := rel.Path(argv[0])
	if err != nil {
		return Outcome{}, err
	}
	argv[0] = resolved

	info, err := os.Stat(resolved)
	if err != nil {
		return Outcome{}, domain.ValidationError(err,
			"the release declares hook %q but it is missing", command[0]).
			WithHint("a declared-but-missing hook is a broken bundle")
	}
	if info.Mode().Perm()&0o111 == 0 {
		return Outcome{}, domain.ValidationError(nil,
			"hook %q is not executable (mode %04o)", command[0], info.Mode().Perm()).
			WithHint("run `chmod +x %s` in the bundle source and rebuild it", command[0])
	}

	// The result pipe. The read end stays here; the write end becomes fd 3
	// in the child and is closed locally straight after start, so the read
	// returns EOF when the hook exits rather than blocking forever.
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		return Outcome{}, domain.Internal(err, "cannot create the hook result pipe")
	}

	resultCh := make(chan []byte, 1)
	go func() {
		// Bounded: a hook that streams gigabytes into fd 3 is broken,
		// and must not take the manager's memory with it.
		data, _ := io.ReadAll(io.LimitReader(readEnd, 1<<20))
		_ = readEnd.Close()
		resultCh <- data
	}()

	res, runErr := r.runner.Run(ctx, exec.Command{
		Argv: argv,
		// The working directory is the release root, so a hook can use
		// relative paths to its own files.
		Dir:           rel.Root,
		Env:           exec.BaseEnv(env.Vars()),
		Timeout:       timeout,
		Redact:        r.redact,
		OnLine:        r.onLine,
		CaptureOutput: true,
		ExtraFiles:    []*os.File{writeEnd},
	})

	// Closing our copy of the write end is what lets the reader see EOF.
	_ = writeEnd.Close()
	resultData := <-resultCh

	outcome := Outcome{
		ExitCode: res.ExitCode,
		Stdout:   res.Stdout,
		Stderr:   res.Stderr,
		Duration: res.Duration,
	}

	if parsed, ok := parseResult(resultData); ok {
		outcome.Result = parsed
		outcome.Skipped = parsed.Skipped
	}

	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			outcome.ExitCode = exitErr.ExitCode
			if exitErr.ExitCode == ExitSkipped {
				// Exit 2 is "nothing to do", not a failure.
				outcome.Skipped = true
				return outcome, nil
			}
			return outcome, hookFailure(command[0], exitErr, outcome)
		}
		return outcome, runErr
	}

	return outcome, nil
}

// parseResult decodes the hook's structured output, tolerating silence.
//
// A hook that writes nothing to fd 3 is the common case -- most hooks just do
// work and exit -- so failing to parse an empty buffer would make every simple
// hook look broken.
func parseResult(data []byte) (Result, bool) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return Result{}, false
	}
	var out Result
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return Result{}, false
	}
	return out, true
}

// hookFailure builds an error that quotes what the hook actually said.
func hookFailure(name string, exitErr *exec.ExitError, outcome Outcome) error {
	detail := strings.TrimSpace(outcome.Result.Message)
	if detail == "" {
		detail = lastLines(outcome.Stderr, 3)
	}
	if detail == "" {
		detail = lastLines(outcome.Stdout, 3)
	}

	msg := fmt.Sprintf("hook %q failed with exit code %d", name, exitErr.ExitCode)
	if detail != "" {
		msg += ": " + detail
	}

	return domain.RuntimeError(exitErr, "%s", msg).
		WithHint("the hook ships with the release; its full output is in the log")
}

// lastLines keeps the tail of a hook's output. The last thing a failing script
// prints is almost always the reason it failed.
func lastLines(s string, n int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "; "))
}
