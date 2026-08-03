// Package cli builds the cobra command tree and wires dependencies.
//
// This is the only place adapters are named. Everything below it speaks to
// ports, which is what makes replacing Compose or SOPS a change to this file
// plus one new package.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/adapters/backup/hookbackup"
	"github.com/morzecrew/morzer/internal/adapters/health"
	"github.com/morzecrew/morzer/internal/adapters/hooks"
	"github.com/morzecrew/morzer/internal/adapters/render/gotemplate"
	"github.com/morzecrew/morzer/internal/adapters/runtime/compose"
	"github.com/morzecrew/morzer/internal/adapters/secrets/sopsage"
	"github.com/morzecrew/morzer/internal/adapters/source"
	"github.com/morzecrew/morzer/internal/adapters/source/https"
	"github.com/morzecrew/morzer/internal/adapters/source/local"
	"github.com/morzecrew/morzer/internal/adapters/source/oci"
	"github.com/morzecrew/morzer/internal/adapters/supervisor/systemd"
	"github.com/morzecrew/morzer/internal/adapters/verify"
	"github.com/morzecrew/morzer/internal/adapters/verify/checksum"
	"github.com/morzecrew/morzer/internal/adapters/verify/minisign"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/infra/lock"
	"github.com/morzecrew/morzer/internal/infra/logging"
	"github.com/morzecrew/morzer/internal/infra/state"
	"github.com/morzecrew/morzer/internal/infra/tools"
	"github.com/morzecrew/morzer/internal/lifecycle/engine"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/release"
	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/internal/ui/jsonout"
	"github.com/morzecrew/morzer/internal/ui/plain"
)

// BuildInfo is stamped in at link time.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// globalFlags are the flags every command honours.
type globalFlags struct {
	json      bool
	dryRun    bool
	yes       bool
	force     bool
	timeout   time.Duration
	verbose   bool
	quiet     bool
	logFormat string
	noColor   bool
	plainOut  bool
	resume    bool
	wait      bool
	configDir string
	product   string
	root      string
}

// App holds everything a command needs, assembled once in PersistentPreRunE.
type App struct {
	Build  BuildInfo
	Flags  globalFlags
	Mode   ui.Mode
	Stream ui.Streams

	Deps *ops.Deps

	json  *jsonout.Presenter
	plain *plain.Presenter
	log   *slog.Logger

	// bus and redactor are retained so a command that learns the product
	// name only from its arguments -- `init` -- can rebuild the adapters
	// against the corrected paths.
	bus      *events.Bus
	redactor *logging.Redactor

	// command is the invoked path, recorded in the JSON envelope.
	command string

	// jsonData and jsonRecord carry a command's result to the envelope
	// writer. They are fields rather than return values because cobra's
	// RunE signature returns only an error.
	jsonData   any
	jsonRecord *domain.OperationRecord
}

// CommandTree returns the command tree with nothing wired.
//
// It exists for the documentation checker, which walks the tree to assert that
// every command and every flag is mentioned by some page. Building the tree is
// pure -- adapters are constructed in PersistentPreRunE, which this never runs
// -- so reading the CLI surface needs neither a machine with docker on it nor
// an installation to point at.
func CommandTree() *cobra.Command {
	return newRootCommand(&App{Stream: ui.DefaultStreams()})
}

// Execute builds the command tree and runs it, returning the process exit
// code.
//
// Exit-code mapping happens in exactly one place -- here -- so a new error
// type cannot accidentally acquire a new exit code by being handled somewhere
// else.
func Execute(ctx context.Context, build BuildInfo, args []string) int {
	app := &App{Build: build, Stream: ui.DefaultStreams()}
	root := newRootCommand(app)

	// Flag errors are the one case where printing usage helps: the operator
	// mistyped something and the valid options are the answer.
	root.SetFlagErrorFunc(func(cmd *cobra.Command, ferr error) error {
		_ = cmd.Usage()
		return ferr
	})

	root.SetArgs(args)
	root.SetOut(app.Stream.Out)
	root.SetErr(app.Stream.Err)

	// Cobra prints usage on its own for flag errors; suppressing that here
	// keeps the error path going through one formatter.
	root.SilenceErrors = true
	root.SilenceUsage = true

	err := classifyCLIError(root.ExecuteContext(ctx))

	// A source that downloaded something holds a temporary copy of it. The
	// command is over, so it goes -- before the JSON envelope is written,
	// because that is the last thing this process does.
	app.closeSources()

	if app.json != nil {
		// In JSON mode the envelope is the whole output, including for
		// errors, so it is written here rather than per-command.
		if writeErr := app.json.Write(app.command, app.jsonData, app.jsonRecord, err); writeErr != nil {
			fmt.Fprintf(app.Stream.Err, "cannot write json output: %v\n", writeErr)
			return domain.ExitInternal
		}
		return domain.ExitCode(err)
	}

	if err != nil {
		app.printError(err)
	}
	return domain.ExitCode(err)
}

// closeSources releases anything a release source is holding.
//
// Only the network transports have anything to release, so this is a type
// assertion rather than a port method: making every source implement Close so
// one of them can would be ceremony imposed on the simple case by the complex
// one.
func (a *App) closeSources() {
	if a.Deps == nil {
		return
	}
	if closer, ok := a.Deps.Source.(io.Closer); ok {
		if err := closer.Close(); err != nil && a.log != nil {
			a.log.Debug("cannot clean up a release source", "error", err)
		}
	}
}

// classifyCLIError maps cobra's own parse failures onto the usage exit code.
//
// Cobra reports an unknown flag or an unknown subcommand as a plain error with
// no sentinel, which would otherwise fall through to "internal error" (1) and
// tell an operator their typo was a bug in the manager. The mapping lives here
// because this is the one place exit codes are decided.
func classifyCLIError(err error) error {
	if err == nil {
		return nil
	}
	// A typed error already knows its own exit code.
	var typed *domain.Error
	if errors.As(err, &typed) {
		return err
	}

	// Cobra's parse-failure vocabulary. Matching on message text is
	// unpleasant, but cobra exposes no other signal, and the alternative --
	// reporting every typo as an internal error -- is worse.
	msg := err.Error()
	for _, prefix := range []string{
		"unknown command",
		"unknown flag",
		"unknown shorthand flag",
		"invalid argument",
		"flag needs an argument",
		"accepts ",
		"requires at least",
		"unknown subcommand",
	} {
		if strings.Contains(msg, prefix) {
			return domain.Usage("%s", msg).
				WithHint("run `morzer --help`, or `morzer <command> --help`")
		}
	}
	return err
}

// printError renders a failure for a human.
//
// Message says what happened, hint says what to do about it. Both are printed;
// an error without its remedy is a support ticket.
func (a *App) printError(err error) {
	e := domain.AsError(err)

	fmt.Fprintf(a.Stream.Err, "\nerror: %s\n", e.Message)
	if e.Hint != "" {
		fmt.Fprintf(a.Stream.Err, "hint:  %s\n", e.Hint)
	}
	if e.OpID != "" {
		fmt.Fprintf(a.Stream.Err, "       operation %s", e.OpID)
		if e.StepID != "" {
			fmt.Fprintf(a.Stream.Err, ", step %s", e.StepID)
		}
		_, _ = fmt.Fprintln(a.Stream.Err)
	}
}

func newRootCommand(app *App) *cobra.Command {
	root := &cobra.Command{
		Use:   "morzer",
		Short: "Manage a self-hosted product on a single machine",
		Long: "morzer installs, configures, updates, backs up and diagnoses a\n" +
			"self-hosted product deployed with Docker Compose on one Linux machine.\n\n" +
			"The unit of delivery is a release bundle; the unit of management is an\n" +
			"installation. Every mutating command takes a lock, journals what it did,\n" +
			"and can be planned first with --dry-run.",
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			app.command = cmd.CommandPath()
			return app.setup(cmd.Context())
		},
	}

	f := &app.Flags
	pf := root.PersistentFlags()
	pf.BoolVar(&f.json, "json", false, "machine-readable output; stdout carries exactly one JSON object")
	pf.BoolVar(&f.dryRun, "dry-run", false, "plan only, make no changes")
	pf.BoolVar(&f.yes, "yes", false, "assume yes for confirmations (destructive actions still need --force)")
	pf.BoolVar(&f.force, "force", false, "confirm a destructive operation")
	pf.DurationVar(&f.timeout, "timeout", 0, "overall time budget for the operation")
	pf.BoolVarP(&f.verbose, "verbose", "v", false, "verbose output")
	pf.BoolVarP(&f.quiet, "quiet", "q", false, "errors only")
	pf.StringVar(&f.logFormat, "log-format", "text", "log format: text or json")
	pf.BoolVar(&f.noColor, "no-color", false, "disable styling")
	pf.BoolVar(&f.plainOut, "plain", false, "line-oriented output, no interactive rendering")
	pf.BoolVar(&f.resume, "resume", false, "continue an interrupted operation")
	pf.BoolVar(&f.wait, "wait", false, "wait for the deployment lock instead of failing")
	pf.StringVar(&f.configDir, "config", "", "path to installation.yaml")
	pf.StringVar(&f.product, "product", "", "product name (inferred from the installation when omitted)")

	// --root relocates the whole layout. It exists so the test suite and
	// the acceptance scenario can exercise the real code paths without
	// root and without touching the host's /etc.
	pf.StringVar(&f.root, "root", "", "prefix for all managed paths (for testing)")
	_ = pf.MarkHidden("root")

	root.AddCommand(
		newInitCommand(app),
		newApplyCommand(app),
		newUpdateCommand(app),
		newRollbackCommand(app),
		newStatusCommand(app),
		newDoctorCommand(app),
		newBackupCommand(app),
		newRestoreCommand(app),
		newSecretCommand(app),
		newReleaseCommand(app),
		newInstallationCommand(app),
		newVersionCommand(app),
	)
	return root
}

// setup resolves the output mode, builds the logger, and wires every adapter.
func (a *App) setup(ctx context.Context) error {
	f := a.Flags

	a.Mode = ui.ResolveMode(ui.ModeOptions{
		JSON:   f.json,
		Plain:  f.plainOut,
		Quiet:  f.quiet,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	})

	// The logger writes to stderr always. stdout is the result.
	logFormat := logging.FormatText
	if f.logFormat == "json" {
		logFormat = logging.FormatJSON
	}
	logWriter := a.Stream.Err
	if a.Mode == ui.ModeJSON && !f.verbose {
		// In JSON mode the envelope is the contract; log narration on
		// stderr would be noise for a consumer that is not reading it.
		logWriter = io.Discard
	}

	logger, redactor := logging.New(logging.Options{
		Level:  logging.ParseLevel(f.verbose, f.quiet),
		Format: logFormat,
		Writer: logWriter,
	})
	a.log = logger

	bus := events.NewBus()
	a.bus = bus
	bus.OnPanic(func(r any) {
		// A presenter that panics is logged and dropped. The operation
		// continues: a terminal resized into nonsense must not abort an
		// update that is midway through migrating a database.
		logger.Error("a presenter panicked and was dropped", "panic", r)
	})
	// The log mirror of the event stream is subscribed only when it is not
	// duplicating a presenter: in JSON mode nothing else narrates, and
	// under --verbose the operator asked for the detail. Subscribing it
	// unconditionally would print every step twice.
	if f.verbose || a.Mode == ui.ModeJSON {
		bus.Subscribe(logging.EventSink(logger))
	}

	switch a.Mode {
	case ui.ModeJSON:
		a.json = jsonout.New(jsonout.Options{
			Out:            a.Stream.Out,
			EventStream:    eventStreamWriter(a.Stream.Err, f.verbose),
			IncludeEvents:  f.verbose,
			ManagerVersion: a.Build.Version,
			APIVersions:    apiVersionStrings(),
		})
		bus.Subscribe(a.json)
	default:
		// ModeRich falls back to plain until the Bubble Tea renderer
		// lands. The information is identical; only the motion is
		// missing.
		if !f.quiet {
			a.plain = plain.New(a.Stream.Err, f.verbose)
			bus.Subscribe(a.plain)
		}
	}

	return a.wire(ctx, bus, redactor, logger)
}

// eventStreamWriter returns the JSONL event destination, or nil when events
// are not being streamed.
func eventStreamWriter(stderr io.Writer, verbose bool) io.Writer {
	if !verbose {
		return nil
	}
	return stderr
}

func apiVersionStrings() []string {
	out := make([]string, len(domain.SupportedAPIVersions))
	for i, v := range domain.SupportedAPIVersions {
		out[i] = string(v)
	}
	return out
}

// wire assembles the dependency graph.
//
// This function is the architecture made concrete: every adapter is named
// exactly once, and everything downstream receives interfaces.
func (a *App) wire(ctx context.Context, bus *events.Bus, redactor *logging.Redactor, logger *slog.Logger) error {
	a.redactor = redactor

	paths, err := a.resolvePaths(ctx)
	if err != nil {
		return err
	}
	return a.wireAt(ctx, paths, bus, redactor, logger)
}

// wireAt builds the dependency graph against a specific path layout.
//
// It is separate from wire because the layout is derived from the product
// name, and `init` only learns that name from its own arguments -- after the
// persistent pre-run has already wired everything against a placeholder. Every
// adapter holds its paths at construction, so correcting the layout means
// rebuilding them, not just swapping a struct field.
func (a *App) wireAt(ctx context.Context, paths domain.Paths, bus *events.Bus, redactor *logging.Redactor, logger *slog.Logger) error {

	runner := exec.New()
	stateStore := state.New(paths)
	locker := lock.New(paths.LockDir())
	toolRegistry := tools.NewRegistry(runner)

	// Subprocess output is forwarded to the bus so the live view can tail
	// it. The adapters never learn what a presenter is.
	outputSink := func(line exec.Line) {
		bus.Publish(events.Event{
			Kind: events.KindStepOutput, At: time.Now(),
			Message: line.Text, Level: events.LevelDebug,
		})
	}

	runtime := compose.New(runner,
		compose.WithOutputSink(outputSink),
		compose.WithRedaction(redactor.Values()),
	)
	secrets := sopsage.New(runner, paths.SecretsFile(), paths.AgeIdentityFile())
	hookRunner := hooks.NewRunner(runner,
		hooks.WithOutputSink(outputSink),
		hooks.WithRedaction(redactor.Values()),
	)

	// One registry, indexed by reference scheme. Adding a transport is a new
	// adapter and one more argument here; nothing above this line changes.
	sources, err := source.NewRegistry(local.New(), https.New(), oci.New())
	if err != nil {
		return err
	}

	deps := &ops.Deps{
		Paths:   paths,
		State:   stateStore,
		Locker:  locker,
		Runtime: runtime,
		Secrets: secrets,
		Source:  sources,
		// Both, always. The checksum verifier answers "is this the
		// artifact I was told to expect"; minisign answers "did a key
		// this machine trusts publish it". A build with only the first
		// could not make require_signature mean anything.
		Verifier:       verify.NewChain(checksum.New(), minisign.New()),
		Renderer:       gotemplate.New(),
		Supervisor:     systemd.New(runner),
		Hooks:          hookRunner,
		Tools:          toolRegistry,
		Bus:            bus,
		ManagerPath:    systemd.ManagerPath(),
		ManagerVersion: parseBuildVersion(a.Build.Version),
		Redactor:       redactor,
		TargetPrefix:   a.Flags.root,
	}

	deps.Health = health.NewWaiter(
		health.NewHTTP(),
		health.NewTCP(),
		health.NewCommand(runner, redactor.Values()),
	)
	deps.Engine = engine.New(stateStore, bus)

	// The backup engine needs a release and an installation, which do not
	// exist during `init`. It is attached lazily by the commands that need
	// it, so `init` and `doctor` on a bare machine still work.
	a.Deps = deps
	return nil
}

// attachBackupEngine wires the backup adapter once a release is known.
func (a *App) attachBackupEngine(ctx context.Context) error {
	d := a.Deps

	inst, err := d.State.LoadInstallation(ctx)
	if err != nil {
		return err
	}
	current, err := d.State.CurrentRelease(ctx)
	if err != nil {
		return err
	}
	if current.IsZero() {
		return domain.InstallationError(domain.ErrReleaseNotFound,
			"no release is installed").
			WithHint("install one with `morzer update <bundle>`")
	}
	rel, err := release.Load(current.Root)
	if err != nil {
		return err
	}

	d.Backup = hookbackup.New(hookbackup.Config{
		Hooks:          d.Hooks,
		Release:        rel,
		Installation:   inst,
		Paths:          d.Paths,
		ManagerVersion: a.Build.Version,
	})
	return nil
}

// resolvePaths determines the on-disk layout.
//
// The product name comes from the installation when one exists, from --product
// otherwise. That ordering matters: a machine with an installation must not
// have its paths changed by a mistyped flag.
func (a *App) resolvePaths(ctx context.Context) (domain.Paths, error) {
	product := a.Flags.product

	if product == "" {
		// Look for exactly one installation under the default roots.
		if found, ok := discoverProduct(a.Flags.root); ok {
			product = found
		}
	}
	if product == "" {
		// No installation and no flag. A placeholder keeps `init`,
		// `version` and `doctor` working; `init` overrides it with the
		// name it was given.
		product = "morzer"
	}
	if err := domain.ValidateProductName(product); err != nil {
		return domain.Paths{}, err
	}

	if a.Flags.root != "" {
		return domain.PathsUnder(a.Flags.root, product), nil
	}
	return domain.DefaultPaths(product), nil
}

// discoverProduct finds an installed product by looking for its state file.
func discoverProduct(root string) (string, bool) {
	base := "/etc"
	if root != "" {
		base = root + "/etc"
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", false
	}

	var found []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(base + "/" + e.Name() + "/installation.yaml"); err == nil {
			found = append(found, e.Name())
		}
	}
	// Exactly one, or the operator must say which: guessing between two
	// installations is how a command acts on the wrong deployment.
	if len(found) == 1 {
		return found[0], true
	}
	return "", false
}

func parseBuildVersion(s string) domain.Version {
	v, err := domain.ParseVersion(s)
	if err != nil {
		// A dev build ("dev", "0.0.0-snapshot") is not a semantic
		// version. Reporting 0.0.0 keeps min_manager_version checks
		// conservative rather than crashing on startup.
		return domain.MustParseVersion("0.0.0")
	}
	return v
}

// operationOptions maps global flags onto lifecycle options.
func (a *App) operationOptions() ops.Options {
	return ops.Options{
		DryRun:  a.Flags.dryRun,
		Resume:  a.Flags.resume,
		Yes:     a.Flags.yes,
		Force:   a.Flags.force,
		Wait:    a.Flags.wait,
		Timeout: a.Flags.timeout,
	}
}

// finish records a command's result for the JSON envelope and prints the human
// summary.
func (a *App) finish(result ops.Result) {
	if a.json != nil {
		a.jsonData = result.Data
		if result.Record.ID != "" {
			rec := result.Record
			a.jsonRecord = &rec
		}
		return
	}
	if result.Summary != "" && !a.Flags.quiet {
		fmt.Fprintf(a.Stream.Err, "\n%s\n", result.Summary)
	}
}

// rewireForProduct rebuilds the dependency graph for a named product.
//
// Adapters capture their paths at construction -- the secret store holds the
// path to the age identity, the state store holds the manager directory -- so
// a product name learned after the pre-run has to rebuild them rather than
// merely reassign Deps.Paths. Getting this wrong is silent and confusing: the
// paths look right in the struct while the adapters still read the old ones.
func (a *App) rewireForProduct(ctx context.Context, product string) error {
	if err := domain.ValidateProductName(product); err != nil {
		return err
	}

	paths := domain.DefaultPaths(product)
	if a.Flags.root != "" {
		paths = domain.PathsUnder(a.Flags.root, product)
	}

	return a.wireAt(ctx, paths, a.bus, a.redactor, a.log)
}
