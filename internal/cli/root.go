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
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/morzecrew/morzer/internal/adapters/backup/hookbackup"
	"github.com/morzecrew/morzer/internal/adapters/health"
	"github.com/morzecrew/morzer/internal/adapters/hooks"
	"github.com/morzecrew/morzer/internal/adapters/render/gotemplate"
	"github.com/morzecrew/morzer/internal/adapters/runtime/compose"
	"github.com/morzecrew/morzer/internal/adapters/secrets/sopsage"
	signminisign "github.com/morzecrew/morzer/internal/adapters/sign/minisign"
	"github.com/morzecrew/morzer/internal/adapters/source"
	"github.com/morzecrew/morzer/internal/adapters/source/https"
	"github.com/morzecrew/morzer/internal/adapters/source/local"
	"github.com/morzecrew/morzer/internal/adapters/source/oci"
	"github.com/morzecrew/morzer/internal/adapters/supervisor/systemd"
	"github.com/morzecrew/morzer/internal/adapters/target"
	"github.com/morzecrew/morzer/internal/adapters/target/localdir"
	"github.com/morzecrew/morzer/internal/adapters/target/s3"
	"github.com/morzecrew/morzer/internal/adapters/target/sftp"
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
	"github.com/morzecrew/morzer/internal/ports"
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

	// machineProducts is every product with installation state under the
	// resolved root, as discovery found it, sorted. Held because "this
	// machine has no installation", "it has three and you named none" and
	// "it has three and none is called that" are different answers to the
	// same failed lookup, and nothing at the point of failure can tell them
	// apart without the inventory.
	machineProducts []string

	// machineInventory is why that list is empty, when it is empty because
	// nobody could read it.
	//
	// Path resolution cannot refuse on it: it runs for every command, and
	// `morzer version` on a machine whose /etc this process may not read is
	// still a question with an answer. But a command that is about to act on
	// *one* installation, chosen from an inventory nobody could take, would
	// otherwise proceed against the placeholder layout and report that no
	// installation exists — which is the same wrong answer the ambiguity
	// refusal was written to stop, arriving by the other road.
	machineInventory error

	// cancelOperation cancels the context every command runs under. It is
	// what the live view's Ctrl-C invokes: raw mode suppresses the SIGINT
	// main's handler listens for, so the keystroke has to reach the same
	// cancellation by another road.
	cancelOperation context.CancelFunc

	// jsonData and jsonRecord carry a command's result to the envelope
	// writer. They are fields rather than return values because cobra's
	// RunE signature returns only an error.
	jsonData   any
	jsonRecord *domain.OperationRecord

	// jsonStreamed marks a command that has already written its own
	// machine-readable output, one object per line, and must not have an
	// envelope appended to it.
	//
	// The single documented exception to the one-object contract, and it
	// belongs to `morzer logs`: a stream has no end at which to write an
	// envelope. Set once the first record is about to go out, so a command
	// that failed before streaming anything still gets its `ok:false` --
	// after that the exit code is what says whether the stream ended
	// cleanly, and the diagnostic is on stderr where a consumer parsing
	// lines will not meet it.
	jsonStreamed bool
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
	return ExecuteWith(ctx, build, args, ui.DefaultStreams())
}

// ExecuteWith runs the CLI against the given streams.
//
// The seam the command tests drive: they invoke exactly what an operator's
// shell invokes -- flag parsing, argument validation, confirmations, the error
// formatter and the exit-code mapping -- and read back what was written, rather
// than calling the operation underneath and assuming the wiring above it works.
func ExecuteWith(ctx context.Context, build BuildInfo, args []string, streams ui.Streams) int {
	// A caller that named only the writers gets the process's own input, so
	// adding In to Streams did not silently turn `secret set` into a nil
	// dereference for anyone already calling this.
	if streams.In == nil {
		streams.In = os.Stdin
	}
	// Derived here so the live view can cancel it; see App.cancelOperation.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	app := &App{Build: build, Stream: streams, cancelOperation: cancel}

	// Recorded before any manifest is read, so a release built for a newer
	// manager reports that rather than an unknown field. A build with no
	// stamped version -- `go run`, a test binary -- parses to zero, and the
	// check is skipped rather than guessed at.
	if v, err := domain.ParseVersion(build.Version); err == nil {
		release.SetManagerVersion(v)
	}

	root := newRootCommand(app)

	// Flag errors are the one case where printing usage helps: the operator
	// mistyped something and the valid options are the answer. It goes to
	// stderr explicitly -- cobra's Usage() writes to the out-writer, and
	// stdout belongs to results: a piped consumer (`--json | jq`) must
	// never receive help text in its data stream because of a typo.
	//
	// It is also where a flag error is *typed*. Deciding that from the
	// message text afterwards means matching substrings against every error
	// the program can produce, and "invalid argument" is what a kernel says
	// about EINVAL as readily as what cobra says about --timeout=soon.
	root.SetFlagErrorFunc(func(cmd *cobra.Command, ferr error) error {
		fmt.Fprint(cmd.ErrOrStderr(), cmd.UsageString())
		return domain.Usage("%s", ferr.Error()).
			WithHint("run `morzer --help`, or `morzer <command> --help`")
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

	// A failure before the presenter existed still owes the caller an
	// envelope. The presenter is built in PersistentPreRunE, which never
	// runs for an unknown flag, an unknown command, or an invalid
	// --log-format -- so exactly the mistakes a script makes were the ones
	// that produced no `ok:false` at all, and a consumer parsing stdout got
	// empty input rather than an error it could read.
	if err != nil && app.json == nil && wantsJSON(app.Flags.json, flagLookup(root, args), args) {
		app.json = jsonout.New(jsonout.Options{
			Out:            app.Stream.Out,
			ManagerVersion: app.Build.Version,
			APIVersions:    apiVersionStrings(),
		})
	}

	if app.json != nil && !app.jsonStreamed {
		// In JSON mode the envelope is the whole output, including for
		// errors, so it is written here rather than per-command.
		if writeErr := app.json.Write(app.command, app.jsonData, app.jsonRecord, err); writeErr != nil {
			fmt.Fprintf(app.Stream.Err, "cannot write json output: %v\n", writeErr)
			return domain.ExitInternal
		}
		return exitCodeFor(err)
	}

	// A container's own non-zero exit is not reported again: `morzer exec`
	// has already passed the command's stderr through, and a manager
	// sentence under it would be this program taking the credit for
	// somebody else's error message.
	if err != nil && !silentFailure(err) {
		app.printError(err)
	}
	return exitCodeFor(err)
}

// wantsJSON reports whether the caller asked for machine-readable output.
//
// The parsed flag is the answer whenever parsing got that far. It does not,
// when the failure *is* the parse: cobra stops at the first unknown flag, so
// `morzer --wat --json` never records it. The raw arguments are the only
// remaining evidence, and the cost of reading them wrong is an error envelope
// where a plain error would have gone -- on a run that has already failed.
func wantsJSON(parsed bool, lookup func(string) *pflag.Flag, args []string) bool {
	if parsed {
		return true
	}
	// The last assignment wins, because that is what cobra would have done
	// with them: `--json=true --json=false` asked for plain output, and
	// returning on the first truthy one would have overruled the operator's
	// own correction.
	wants := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		// Everything after the terminator is an operand, not a flag:
		// `morzer --wat -- --json` asked for plain output and a literal
		// argument that happens to look like a flag.
		if arg == "--" {
			break
		}
		if arg == "--json" {
			wants = true
			continue
		}
		// The same spellings cobra's own boolean parser takes:
		// --json=1, --json=TRUE, --json=t.
		if value, ok := strings.CutPrefix(arg, "--json="); ok {
			if parsed, err := strconv.ParseBool(value); err == nil {
				wants = parsed
			}
			continue
		}
		// A flag that takes a value eats the token after it, so in
		// `--timeout --json` the operator never asked for JSON -- cobra
		// read it as the duration, and failing on that is what brought
		// the run here. Counting it would answer a malformed command
		// line with an envelope nobody requested.
		//
		// Long spellings only: every shorthand this CLI defines is a
		// boolean, and a boolean eats nothing. A value-taking shorthand
		// would need the same treatment, which is what the test below
		// pinning `--dry-run` is there to make visible.
		if name, ok := strings.CutPrefix(arg, "--"); ok && !strings.Contains(name, "=") {
			if f := lookup(name); f != nil && f.Value.Type() != "bool" {
				i++
			}
		}
	}
	return wants
}

// flagLookup resolves a long flag name the way cobra would for these arguments:
// against the command they select, falling back to the root's persistent set.
//
// It has to work when parsing has already failed, which is the only time
// wantsJSON runs -- so it goes through Find, which resolves the command from the
// positional arguments alone and does not care that a flag further along is
// unknown.
func flagLookup(root *cobra.Command, args []string) func(string) *pflag.Flag {
	target := root
	if found, _, err := root.Find(args); err == nil && found != nil {
		target = found
	}
	return func(name string) *pflag.Flag {
		if f := target.Flags().Lookup(name); f != nil {
			return f
		}
		return root.PersistentFlags().Lookup(name)
	}
}

// closeSources releases anything a release source or a backup target is
// holding: a downloaded bundle, an SSH connection.
//
// Only the network transports have anything to release, so this is a type
// assertion rather than a port method: making every source implement Close so
// one of them can would be ceremony imposed on the simple case by the complex
// one.
func (a *App) closeSources() {
	if a.Deps == nil {
		return
	}
	for what, holder := range map[string]any{
		"a release source": a.Deps.Source,
		"a backup target":  a.Deps.Targets,
	} {
		closer, ok := holder.(io.Closer)
		if !ok {
			continue
		}
		if err := closer.Close(); err != nil && a.log != nil {
			a.log.Debug("cannot clean up "+what, "error", err)
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

	// What is left after the FlagErrorFunc has typed the flag failures:
	// command resolution and argument-count validation, which cobra
	// returns directly with no sentinel to match on.
	//
	// Prefix, never substring. Cobra composes these messages itself, so
	// they begin with the phrase; an operational error that merely
	// *contains* one -- "open /etc/demo: invalid argument", which is how
	// EINVAL reads -- is not a typo, and reporting it as exit 2 would send
	// an operator looking for a flag they spelled correctly.
	msg := err.Error()
	for _, prefix := range []string{
		"unknown command",
		"unknown subcommand",
		"unknown flag",
		"unknown shorthand flag",
		"accepts ",
		"requires at least",
		// MarkFlagsMutuallyExclusive violations, e.g. -v with -q.
		"if any flags in the group",
	} {
		if strings.HasPrefix(msg, prefix) {
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
			if err := app.setup(cmd.Context()); err != nil {
				return err
			}
			return app.confirmInstallationChosen(cmd)
		},
	}

	// `--version` is near-universal convention and cobra provides it from
	// the Version field. The guard keeps CommandTree() -- built with an
	// empty BuildInfo for the documentation checker -- identical to what it
	// was, while every real invocation has a version ("dev" at minimum).
	if app.Build.Version != "" {
		root.Version = app.Build.Version
		root.SetVersionTemplate("{{.Name}} {{.Version}}\n")
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
	// The help text names the automatic cases because otherwise this flag
	// ends up in every systemd unit and CI job by superstition.
	pf.BoolVar(&f.plainOut, "plain", false,
		"line-oriented output; already automatic under CI, systemd, TERM=dumb and without a terminal")
	pf.BoolVar(&f.resume, "resume", false, "continue an interrupted operation")
	pf.BoolVar(&f.wait, "wait", false, "wait for the deployment lock instead of failing")
	pf.StringVar(&f.configDir, "config", "", "path to installation.yaml")
	pf.StringVar(&f.product, "product", "", "product name (inferred from the installation when omitted)")

	// --root relocates the whole layout. It exists so the test suite and
	// the acceptance scenario can exercise the real code paths without
	// root and without touching the host's /etc.
	pf.StringVar(&f.root, "root", "", "prefix for all managed paths (for testing)")
	_ = pf.MarkHidden("root")

	// Stating the contract beats resolving the conflict silently: before
	// this, `-v -q` meant quiet and nothing said so.
	root.MarkFlagsMutuallyExclusive("verbose", "quiet")

	// Registration order is the display order, here and in every subcommand
	// list. Cobra sorts alphabetically by default, which in a group of three
	// puts `apply` before `init` -- converge before create, on the screen
	// whose whole job is telling a new operator what to run first. The
	// subcommand lists this also unsorts are already written in a deliberate
	// order (`release`: inspect, author, distribute), and alphabetical was
	// never a decision anyone made there either.
	//
	// It is a package-level switch in cobra, so it applies to every command
	// tree in the process, including an embedder's own. Cobra offers no
	// per-command equivalent; setting it here rather than in main keeps the
	// binary and CommandTree() -- which the documentation checker walks --
	// rendering the same help.
	cobra.EnableCommandSorting = false

	// Ordered by when an operator meets them, not alphabetically. The first
	// screen of `--help` is the only documentation many operators read, and
	// an alphabetical list puts `release` between `init` and `restore` while
	// saying nothing about which three commands are needed on day one.
	//
	// Cobra places a command with no GroupID in an "Additional Commands"
	// section at the bottom, which is where its generated `help` and
	// `completion` belong. TestEveryCommandIsGrouped keeps anything else
	// from landing there quietly.
	for _, g := range []struct{ id, title string }{
		{groupStart, "Getting started:"},
		{groupOperate, "Operating:"},
		{groupData, "Data:"},
		{groupBundles, "Bundles:"},
		{groupMachine, "Machine:"},
	} {
		root.AddGroup(&cobra.Group{ID: g.id, Title: g.title})
	}

	// The scope beside the group, for the same reason: both are properties
	// of the whole list, and both are wrong in a way nothing else notices.
	// A subtree that is uniform declares once here and its children inherit;
	// `release` and `installation` hold commands of both kinds and declare
	// per command in their own files.
	root.AddCommand(
		grouped(groupStart, machineScope(newInitCommand(app))),
		grouped(groupStart, installationScope(newApplyCommand(app))),
		grouped(groupStart, installationScope(newStatusCommand(app))),

		grouped(groupOperate, installationScope(newUpdateCommand(app))),
		grouped(groupOperate, installationScope(newRollbackCommand(app))),
		grouped(groupOperate, installationScope(newConfigCommand(app))),
		grouped(groupOperate, installationScope(newSecretCommand(app))),
		grouped(groupOperate, machineScope(newDoctorCommand(app))),

		// The four that reach into what is running, after the ones that
		// change it: an operator meets `update` and `doctor` first, and
		// these are what they run when the answer was not there.
		grouped(groupOperate, installationScope(newLogsCommand(app))),
		grouped(groupOperate, installationScope(newPsCommand(app))),
		grouped(groupOperate, installationScope(newStatsCommand(app))),
		grouped(groupOperate, installationScope(newExecCommand(app))),

		grouped(groupData, perCommandScope(newAttestCommand(app))),
		grouped(groupData, installationScope(newBackupCommand(app))),
		grouped(groupData, installationScope(newRestoreCommand(app))),

		grouped(groupBundles, perCommandScope(newReleaseCommand(app))),

		grouped(groupMachine, newListCommand(app, "ls")),
		grouped(groupMachine, perCommandScope(newInstallationCommand(app))),
		grouped(groupMachine, machineScope(newVersionCommand(app))),
	)

	// `completion install` hangs off the command cobra generates, which
	// cobra creates lazily on first use. Asking for it here is what makes
	// it exist in time to be extended -- and what makes it exist for
	// CommandTree(), which the documentation checker walks without ever
	// running anything.
	root.InitDefaultCompletionCmd()
	for _, sub := range root.Commands() {
		if strings.Fields(sub.Use)[0] == "completion" {
			sub.AddCommand(newCompletionInstallCommand(app))
		}
	}
	return root
}

// The groups of the top-level help, as identifiers rather than repeated
// strings: a typo'd GroupID is a command cobra silently drops to the bottom of
// the list.
const (
	groupStart   = "start"
	groupOperate = "operate"
	groupData    = "data"
	groupBundles = "bundles"
	groupMachine = "machine"
)

// Scope declares what a command acts on, which decides whether it may run on a
// machine holding several installations that nobody chose between.
//
// RFC 0020 §9 left this open, and the gap was real: the ambiguity refusal fired
// where an installation was *loaded*, so `release list` and `secret list` --
// which read the store and the secret state without loading one -- answered
// about the placeholder layout and reported "no releases are installed" on a
// machine holding three. The two alternatives were a maintained list of commands
// that need an installation, and refusing during path resolution, which would
// refuse `morzer version` on a machine with two installations.
//
// This is the third: the scope is declared where the command is built, and
// TestEveryCommandDeclaresItsScope refuses a command that declares nothing. A
// list nobody can forget to append to, because the compiler's own tree is what
// is walked.
//
// Undeclared resolves to installation scope, which is the safe direction: a new
// command is refused on an ambiguous machine until somebody says it is about the
// machine. The test is what stops that default from being how scope is chosen.
const (
	scopeAnnotation = "morzer.scope"

	// scopeMachine is a command that acts on the host, on a file, or on an
	// installation it names itself. It runs whatever the machine holds.
	scopeMachine = "machine"

	// scopeInstallation is a command that acts on one installation, which
	// therefore has to be the one the operator meant.
	scopeInstallation = "installation"

	// scopeDelegated is a parent whose children declare their own, because
	// the subtree holds commands of both kinds: `release show ./bundle`
	// inspects a directory while `release list` reads this installation's
	// store.
	//
	// Not a scope a command runs under -- scopeOf walks past it, so a child
	// that declares nothing still falls through to the safe default, and
	// TestEveryCommandDeclaresItsScope still fails that child. What it
	// declares is ownership: this command is one of ours, which is the
	// question IsGenerated asks.
	scopeDelegated = "per-command"
)

// machineScope marks a command that needs no installation chosen for it.
func machineScope(cmd *cobra.Command) *cobra.Command {
	return withScope(cmd, scopeMachine)
}

// installationScope marks a command that acts on one installation.
func installationScope(cmd *cobra.Command) *cobra.Command {
	return withScope(cmd, scopeInstallation)
}

// perCommandScope marks a parent whose subtree declares per command.
func perCommandScope(cmd *cobra.Command) *cobra.Command {
	return withScope(cmd, scopeDelegated)
}

func withScope(cmd *cobra.Command, scope string) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[scopeAnnotation] = scope
	return cmd
}

// scopeOf resolves the scope a command runs under.
//
// Inherited from the nearest ancestor that declares one, so `release verify` and
// `release list` can differ while `secret` declares once for eight subcommands.
// A command that declares nothing and inherits nothing is installation-scoped:
// the refusal is the safe answer, and a command that should not be refused says
// so in one word.
func scopeOf(cmd *cobra.Command) string {
	for c := cmd; c != nil; c = c.Parent() {
		scope, ok := c.Annotations[scopeAnnotation]
		if !ok || scope == scopeDelegated {
			// A delegating parent answers nothing about its
			// children: that is what delegating means, and reading
			// it as a scope would give every child of `release` one
			// nobody chose.
			continue
		}
		return scope
	}
	return scopeInstallation
}

// IsGenerated reports whether cobra built this command rather than this
// project.
//
// Asked by everything that walks the tree -- the documentation checker, the
// generated command index, the grouping and scope tests -- so all of them
// cover exactly the same set. It used to be a list of names, `help` and
// `completion`, kept in three places; that list was fine until `completion`
// grew a subcommand this project *does* own, at which point a name check would
// have dropped `completion install` from the index and from coverage without
// saying so.
//
// The declaration is the scope annotation, which every command this project
// registers carries and cobra's own commands do not. It is not a second marker
// to keep in step: TestEveryCommandDeclaresItsScope already fails a command of
// ours that has no scope, so a command that reads as generated here is a
// command that fails there.
func IsGenerated(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if _, ok := c.Annotations[scopeAnnotation]; ok {
			return false
		}
	}
	return true
}

// confirmInstallationChosen refuses an installation-scoped command on a machine
// whose installations nobody chose between.
//
// Here rather than inside each operation, because the question is about the
// *command*, and asking it once is what makes the answer uniform. The lifecycle
// layer keeps its own copy of the refusal for the lookup path: an embedder
// assembling ops.Deps directly never passes through here, and the two commands
// that select their own installation mid-run -- `init`, `installation import` --
// are past this point when they do it.
func (a *App) confirmInstallationChosen(cmd *cobra.Command) error {
	if scopeOf(cmd) == scopeMachine {
		return nil
	}
	// The inventory first, because a lookup that could not be taken is not
	// the same as one that came back empty. Without this the placeholder
	// layout answered on behalf of a machine nobody could read: `status` on
	// a /etc this process may not open reported "no installation found at
	// /etc/morzer" and advised `morzer init`, which is advice to create one
	// beside however many are already there.
	if a.machineInventory != nil {
		return a.machineInventory
	}
	return a.Deps.RequireInstallationChosen()
}

// grouped assigns a command to a section of `--help`.
//
// A helper rather than a field set at each constructor, because the grouping is
// a property of the *listing*, decided here where the whole list is visible --
// and a constructor that carried its own GroupID would put the ordering
// decision in thirteen files.
func grouped(id string, cmd *cobra.Command) *cobra.Command {
	cmd.GroupID = id
	return cmd
}

// setup resolves the output mode, builds the logger, and wires every adapter.
func (a *App) setup(ctx context.Context) error {
	f := a.Flags

	// The injected streams, not the process's own descriptors: an embedder
	// running against buffers must not get the live renderer because the
	// terminal *this process* was started from happens to be one.
	a.Mode = ui.ResolveMode(ui.ModeOptions{
		JSON:   f.json,
		Plain:  f.plainOut,
		Quiet:  f.quiet,
		Stdout: a.Stream.Out,
		Stderr: a.Stream.Err,
	})

	// The logger writes to stderr always. stdout is the result.
	var logFormat logging.Format
	switch f.logFormat {
	case "text":
		logFormat = logging.FormatText
	case "json":
		logFormat = logging.FormatJSON
	default:
		// A typo silently meaning "text" would hold until the day
		// someone greps the logs it should have structured.
		return domain.Usage("invalid --log-format %q", f.logFormat).
			WithHint("valid formats: text, json")
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
		// The escape hatch RFC 0010 §5.4 asks for, for the operator whose
		// registry does not carry busybox. An environment variable rather
		// than a flag or a state field: the backup that needs it is the
		// scheduled one, and a systemd `Environment=` override reaches
		// that without regenerating a unit or migrating any state.
		compose.WithHelperImage(os.Getenv(VolumeHelperImageEnv)),
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

	// The same shape again, for the other direction: where a backup goes
	// once it has been taken.
	targets, err := target.NewRegistry(localdir.New(), sftp.New(), s3.New())
	if err != nil {
		return err
	}

	deps := &ops.Deps{
		Paths:  paths,
		State:  stateStore,
		Locker: locker,
		// The same constructor, for the installations this command is
		// not pointed at. `ls` derives one layout per product it finds
		// on the machine, so the set is not known here -- but the
		// adapter still is, which is the whole rule this file keeps.
		StateFor: func(p domain.Paths) ports.StateStore { return state.New(p) },
		Runtime:  runtime,
		Secrets:  secrets,
		Source:   sources,
		Targets:  targets,
		// Both, always. The checksum verifier answers "is this the
		// artifact I was told to expect"; minisign answers "did a key
		// this machine trusts publish it". A build with only the first
		// could not make require_signature mean anything.
		Verifier: verify.NewChain(checksum.New(), minisign.New()),
		// This machine's own key, which is a different thing from the
		// verifier above it despite the shared format: that one checks
		// a vendor's signature over a release, and this one signs
		// statements about this installation. RFC 0028 §2.
		Signer:         signminisign.New(paths.SigningKeyFile(), paths.Product),
		Checker:        signminisign.NewChecker(),
		Renderer:       gotemplate.New(),
		Supervisor:     systemd.New(runner),
		Hooks:          hookRunner,
		Tools:          toolRegistry,
		Bus:            bus,
		ManagerPath:    systemd.ManagerPath(),
		ManagerVersion: parseBuildVersion(a.Build.Version),
		Redactor:       redactor,
		TargetPrefix:   a.Flags.root,
		// What the CLI found on the machine and whether the operator
		// named one, so a lookup that comes back empty can say which of
		// several situations it is in.
		MachineProducts: a.machineProducts,
		ProductNamed:    a.installationChosen(),
	}

	// Notification targets live in the installation, which does not exist
	// during `init`, so this is best-effort and lazy in the same sense the
	// backup engine is. No installation, no targets, or a target that will
	// not resolve all leave Notifier nil -- and ops.notify's nil check is
	// then the path every installation that asked for nothing exercises.
	if inst, err := stateStore.LoadInstallation(ctx); err == nil {
		deps.Notifier = a.buildNotifiers(ctx, inst, secrets)
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

// VolumeHelperImageEnv names an image to read and write volumes through,
// instead of the digest-pinned busybox the manager defaults to.
//
// For the operator whose registry does not carry busybox, or whose air-gapped
// mirror carries something else. Any image with a POSIX `tar`, `du` and `sh`
// will do. Empty or unset means the default.
const VolumeHelperImageEnv = "MORZER_VOLUME_HELPER_IMAGE"

// ProductEnv selects an installation for a shell session.
//
// Below both flags and above discovery: `--product` and `--config` override it,
// which is what makes it usable in the case it exists for -- a session pinned to
// one installation where a single command needs another. It reaches nothing that
// is not this process: the generated systemd units pass --config, which outranks
// it, so setting it globally does not redirect anybody's timers.
//
// Deliberately not a `morzer use` that writes a selection to disk. That is
// kubectl's context, and its failure mode is why operators wrap kubectl in
// scripts that print the context in the prompt: a mutable global that decides
// which deployment a destructive command hits. A variable dies with the shell
// that set it.
const ProductEnv = "MORZER_PRODUCT"

// backupEngineOption adjusts how the backup adapter is wired.
//
// Variadic rather than a parameter, because a dozen commands attach the engine
// and exactly one of them -- `morzer backup` -- has anything to say about it.
type backupEngineOption func(*hookbackup.Config)

// withDowntime decides whether the backup may stop services to read a volume
// the release has not declared safe to read live.
func withDowntime(allowed bool) backupEngineOption {
	return func(cfg *hookbackup.Config) { cfg.AllowDowntime = allowed }
}

// movesVolumeData reports whether the invoked command writes a backup or reads
// one back, and so must not run against an engine that has silently lost the
// ability to do either.
//
// Keyed on the invoked command path rather than on an option the caller passes.
// A dozen commands attach the engine and every one of them that must stay
// tolerant already discards the error with `_ =`, so a new parameter would mean
// editing all of them to say "no change" -- and the two that must not proceed
// would be the two easiest to forget the day a thirteenth caller is added.
// Naming them in one place is the smaller change and the one that stays true.
func movesVolumeData(commandPath string) bool {
	// The path is "<root> <command> [subcommand...]". `backup` on its own
	// is the command that takes a backup; `backup list`, `backup verify`,
	// `backup fetch` and the rest only read, and are meant to work on an
	// installation too broken to resolve.
	parts := strings.Fields(commandPath)
	if len(parts) != 2 {
		return false
	}
	switch parts[1] {
	case "backup", "restore":
		return true
	case "update":
		// `update` takes the pre-update backup, and that one matters
		// most of all: it is what a rollback restores from when the new
		// release turns out to be wrong. A silently volume-less copy
		// there is a rollback that returns the database and none of the
		// files, discovered at the worst moment.
		//
		// `rollback` is deliberately absent -- it only lists backups to
		// name one in a refusal, and must keep working on exactly the
		// broken installation this guards against.
		return true
	default:
		return false
	}
}

// attachBackupEngine wires the backup adapter once a release is known.
func (a *App) attachBackupEngine(ctx context.Context, opts ...backupEngineOption) error {
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

	// The runtime, so a backup can read the project's named volumes and a
	// restore can write them back.
	//
	// A project configuration that will not assemble -- a parameter the
	// release no longer declares, a profile with no Compose file -- leaves
	// two choices, and which is right depends on what is about to happen.
	// Handing the engine no runtime disables volume capture entirely and
	// says nothing: a release with a backup hook would then produce a backup
	// holding that hook's database dump, none of the project's named
	// volumes, and a success message. The operator finds out during a
	// restore, which is the one moment nothing can be done about it.
	//
	// So the two commands that move volume data refuse, carrying the
	// configuration error that caused it. Everything else stays tolerant,
	// because a configuration that will not resolve is exactly when `backup
	// list`, `doctor` and `status` have to keep answering -- doctor reports
	// this same failure by name in backup.volume-coverage.
	runtimeConfig, cfgErr := d.RuntimeConfigFor(rel, inst)
	runtime := d.Runtime
	if cfgErr != nil {
		if movesVolumeData(a.command) {
			return cfgErr
		}
		runtime = nil
	}

	cfg := hookbackup.Config{
		Hooks:          d.Hooks,
		Release:        rel,
		Installation:   inst,
		Paths:          d.Paths,
		ManagerVersion: a.Build.Version,

		Runtime:       runtime,
		RuntimeConfig: runtimeConfig,

		// The default, and the safe one: an undeclared volume is
		// captured with its services stopped rather than skipped. See
		// `morzer backup --no-downtime` for the other choice.
		AllowDowntime: true,

		// The identity document the backup carries, built by the same
		// function `installation export` uses. One producer, which is
		// the whole point: the backup used to copy the operator-facing
		// installation.yaml, and `doctor` ships a check for that file
		// disagreeing with the authoritative state.
		Export: func(ctx context.Context) (domain.InstallationExport, bool, error) {
			return ops.ExportForBackup(ctx, d)
		},

		// A backup is encrypted to whoever can already read this
		// deployment's secrets -- this machine's key plus whatever
		// offline and operator keys have been added. Read at backup
		// time rather than captured here, so a key added this morning
		// can read a backup taken this afternoon.
		Recipients: func(ctx context.Context) ([]string, error) {
			recipients, err := d.Secrets.Recipients(ctx)
			if err != nil {
				return nil, err
			}
			keys := make([]string, 0, len(recipients))
			for _, r := range recipients {
				keys = append(keys, r.PublicKey)
			}
			return keys, nil
		},
	}

	for _, opt := range opts {
		opt(&cfg)
	}

	d.Backup = hookbackup.New(cfg)
	return nil
}

// installationChosen reports whether the operator said which installation this
// command means, by any of the three documented routes.
//
// One reader for the three, because the question the refusal asks is "did
// anybody choose", not "which flag was passed" -- and a route left out of this
// answer is a session that selected an installation and is then told to select
// one.
func (a *App) installationChosen() bool {
	return a.Flags.product != "" || a.Flags.configDir != "" || os.Getenv(ProductEnv) != ""
}

// resolvePaths determines the on-disk layout.
//
// The product name comes from the installation when one exists, from --product
// otherwise. That ordering matters: a machine with an installation must not
// have its paths changed by a mistyped flag.
func (a *App) resolvePaths(ctx context.Context) (domain.Paths, error) {
	if a.Flags.configDir != "" {
		return a.pathsFromConfig()
	}

	product, fromEnv := a.Flags.product, false
	if product == "" {
		// An environment variable a flag overrides is the ordinary
		// shape, and it is not in the --product/--config exclusion:
		// refusing it there would make the variable useless in exactly
		// the case it exists for -- a shell session pinned to one
		// installation where a single command needs another.
		product, fromEnv = os.Getenv(ProductEnv), os.Getenv(ProductEnv) != ""
	}

	// Recorded whether or not it is used, because the *number* of
	// installations is what makes an unfound one ambiguous rather than
	// absent, and the lifecycle layer is where that difference is reported
	// (ops.Deps.MachineProducts). The --config branch above records it too,
	// against the root it derives: both flags name an installation, so both
	// must be able to say what the machine has when it does not have that
	// one.
	a.discoverProducts(a.Flags.root)

	if product == "" && len(a.machineProducts) == 1 {
		// Exactly one, or the operator must say which: guessing between
		// two installations is how a command acts on the wrong
		// deployment.
		product = a.machineProducts[0]
	}
	if product == "" {
		// No installation to name, or more than one and no flag. A
		// placeholder keeps `init`, `version` and `release verify`
		// working -- they touch no installation, and refusing them here
		// would make an ambiguous machine unable to run the commands
		// that would resolve the ambiguity. A command that does need one
		// fails when it reads it, with the alternatives named.
		product = "morzer"
	}
	if err := domain.ValidateProductName(product); err != nil {
		if fromEnv {
			// Where it came from, because an operator who set the
			// variable in a profile weeks ago is otherwise told that
			// a name they did not type on this command line is
			// invalid.
			return domain.Paths{}, domain.AsError(err).
				WithHint("%s names %q; unset it, or pass --product",
					ProductEnv, product)
		}
		return domain.Paths{}, err
	}

	if a.Flags.root != "" {
		return domain.PathsUnder(a.Flags.root, product), nil
	}
	return domain.DefaultPaths(product), nil
}

// pathsFromConfig derives the layout from an explicit installation.yaml.
//
// The file itself is a report rather than a control -- nothing reads it back --
// so what --config selects is the *layout it sits in*: /etc/<product> names the
// product, and whatever precedes it is the root. That is exactly what the
// generated systemd units pass, and until now the flag was parsed and
// discarded: a unit naming one installation ran against whichever one discovery
// happened to find, and exited 0 having managed the wrong deployment.
//
// A path that does not fit the layout is refused rather than guessed at. The
// manager owns four directories derived from one name; a lone file somewhere
// else does not tell it where the other three are.
func (a *App) pathsFromConfig() (domain.Paths, error) {
	path, err := filepath.Abs(a.Flags.configDir)
	if err != nil {
		return domain.Paths{}, domain.Usage("cannot resolve --config %q", a.Flags.configDir)
	}

	const wrongShape = "--config names the installation file inside the layout, " +
		"e.g. /etc/demo/installation.yaml. Use --root to relocate the layout itself."

	if filepath.Base(path) != domain.InstallationFileName {
		return domain.Paths{}, domain.Usage("--config must name a %s file", domain.InstallationFileName).
			WithHint("%s", wrongShape)
	}

	etcProduct := filepath.Dir(path)
	product := filepath.Base(etcProduct)
	etc := filepath.Dir(etcProduct)
	if filepath.Base(etc) != "etc" {
		return domain.Paths{}, domain.Usage("--config %q is not inside an etc/<product> directory", path).
			WithHint("%s", wrongShape)
	}
	if err := domain.ValidateProductName(product); err != nil {
		return domain.Paths{}, err
	}

	root := filepath.Dir(etc)
	if root == string(filepath.Separator) {
		root = ""
	}

	// Two flags naming different deployments is a question, not something
	// to resolve by precedence: whichever one lost would be acted on
	// silently.
	if a.Flags.product != "" && a.Flags.product != product {
		return domain.Paths{}, domain.Usage(
			"--product %s and --config %s name different installations",
			a.Flags.product, path).
			WithHint("pass one or the other")
	}
	if a.Flags.root != "" {
		// Normalised on both sides: --root / and a config under /etc are
		// the same layout, and the empty root above is how that layout
		// is spelled here.
		given, err := filepath.Abs(a.Flags.root)
		if err != nil || filepath.Clean("/"+given) != filepath.Clean("/"+root) {
			return domain.Paths{}, domain.Usage(
				"--root %s and --config %s name different installations",
				a.Flags.root, path).
				WithHint("pass one or the other")
		}
	}

	// The same inventory the discovery branch records, against the root this
	// path sits in. Without it a --config naming an installation the machine
	// does not have was answered as a bare machine -- `morzer init` -- while
	// the identical --product was answered with the names it does have. The
	// systemd units pass --config, so the wrong half of that pair is the one
	// an operator reads after a unit fails.
	a.discoverProducts(root)

	if root == "" {
		return domain.DefaultPaths(product), nil
	}
	return domain.PathsUnder(root, product), nil
}

// confirmProductMatchesConfig refuses a command whose --config and whose
// command-local --product name different installations.
//
// The root's own --product is compared inside pathsFromConfig, during the
// persistent pre-run. A command with a --product of its own -- `init` has one,
// because it may learn the name from a bundle -- is parsed into a different
// variable and is not visible there, so `morzer --config /etc/demo/... init
// --product other` selected demo and then rewired to other.
func (a *App) confirmProductMatchesConfig(product string) error {
	if a.Flags.configDir == "" || product == "" {
		return nil
	}
	path, err := filepath.Abs(a.Flags.configDir)
	if err != nil {
		return domain.Usage("cannot resolve --config %q", a.Flags.configDir)
	}
	if named := filepath.Base(filepath.Dir(path)); named != product {
		return domain.Usage("--product %s and --config %s name different installations",
			product, path).
			WithHint("pass one or the other")
	}
	return nil
}

// discoverProducts records the machine's inventory, and why it is empty when
// nobody could take it.
//
// The enumeration itself belongs to the lifecycle layer, where `ls` reports it
// and where an unreadable /etc is an error rather than an empty machine. Path
// resolution cannot refuse on that half: it runs for every command, including
// the ones that touch no installation, so a /etc this process may not read must
// not stop `morzer version` from answering.
//
// So it is *held* rather than dropped, and confirmInstallationChosen is where it
// is asked -- at the one point that already knows whether this command is about
// an installation.
func (a *App) discoverProducts(root string) {
	inv, err := ops.DiscoverProducts(root)

	// Products only. A directory discovery could not open is not an
	// installation this command could act on either, and counting it would
	// tell an unprivileged operator that a machine with one deployment has
	// four -- `/etc` holds several root-only directories on any real host.
	// `morzer ls` is where they are reported.
	a.machineProducts, a.machineInventory = inv.Products, err
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
		// Only when there is one. A command may have rendered its report
		// already -- `release verify` prints what it verified and then
		// finishes with a summary -- and an unconditional assignment
		// here replaced that payload with the nil of a Result that
		// carries only a sentence. The summary is narration; the report
		// is the answer, and narration must never outrank it.
		if result.Data != nil {
			a.jsonData = result.Data
		}
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
