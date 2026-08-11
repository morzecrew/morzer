package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/ui/tty"
	"github.com/morzecrew/morzer/internal/ui/views"
)

// The four commands that reach into a running deployment.
//
// What they have in common is the knowledge they save an operator from
// reassembling: the Compose project name, which files the profile selected, and
// the environment the manager interpolates. None of them takes the deployment
// lock -- they are what somebody runs *while* an update is in flight.

// defaultLogTail is how much history `logs` shows when nobody said.
//
// A hundred lines is a screen or two: enough to see what a service said as it
// died, and not so much that the answer scrolls past. Following without saying
// otherwise takes the whole retained backlog instead, because a follow is a
// subscription and its history is the part that already happened -- an explicit
// `--tail` still wins in either case.
const defaultLogTail = 100

func newLogsCommand(app *App) *cobra.Command {
	var (
		follow   bool
		tail     int
		since    string
		noRedact bool
	)

	cmd := &cobra.Command{
		Use:   "logs [service...]",
		Short: "Read the deployment's logs",
		Long: "Streams the logs of the running deployment, resolving the Compose\n" +
			"project, its files and its environment for you -- which is the part an\n" +
			"operator otherwise reassembles by hand at the worst possible moment.\n\n" +
			"Scoped to the project rather than to the services the manifest names, so\n" +
			"a sidecar a vendor's Compose file starts is included: what an operator\n" +
			"debugging wants is what is there.\n\n" +
			"This installation's secret values are scrubbed from the stream. That is\n" +
			"best effort and not a guarantee -- a service that logs something derived\n" +
			"from a secret is beyond any redactor -- so logs are still not something\n" +
			"to paste into a ticket unread.\n\n" +
			"Takes no lock: reading logs must never queue behind an update, since\n" +
			"during one is when they are most wanted.",
		Example: "  morzer logs --tail 50\n" +
			"  morzer logs app db --follow\n" +
			"  morzer logs --since 15m\n" +
			"  morzer logs --json | jq -r 'select(.service == \"app\") | .line'",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			at, err := parseSince(since, time.Now())
			if err != nil {
				return err
			}

			// The backlog: a hundred lines when nobody said, and the
			// whole of it when following, because a follow's history
			// is what already happened rather than what is coming.
			if !cmd.Flags().Changed("tail") && follow {
				tail = 0
			}

			if noRedact {
				// On stderr and every time. A flag that turns off
				// the one filter between vendor output and a
				// terminal should not be something an operator
				// discovers they left on.
				fmt.Fprintf(app.Stream.Err,
					"warning: --no-redact is on, so this installation's secret "+
						"values are not scrubbed from the stream\n")
			}

			stream, err := ops.StreamLogs(cmd.Context(), app.Deps, ops.LogsOptions{
				Services:   args,
				Follow:     follow,
				Tail:       tail,
				Since:      at,
				Structured: app.json != nil,
				Redact:     !noRedact,
			})
			if err != nil {
				return err
			}
			defer func() { _ = stream.Close() }()

			if !noRedact && !stream.RedactionArmed {
				// Said out loud, because the alternative is an
				// operator reading an unfiltered stream believing
				// it was filtered -- which is worse than the
				// stream they asked for with --no-redact.
				fmt.Fprintf(app.Stream.Err,
					"warning: this installation's secret values could not be read, "+
						"so nothing can be scrubbed from this stream\n")
			}

			return app.copyLogs(cmd.Context(), stream)
		},
	}

	f := cmd.Flags()
	f.BoolVarP(&follow, "follow", "f", false, "keep the stream open and print new lines as they arrive")
	f.IntVar(&tail, "tail", defaultLogTail,
		"lines of history to show before following; 0 for the whole retained backlog")
	f.StringVar(&since, "since", "",
		"only lines after this: a duration (`10m`, `2h`) or an RFC 3339 timestamp with a zone")
	f.BoolVar(&noRedact, "no-redact", false,
		"do not scrub this installation's secret values from the stream")

	return cmd
}

// copyLogs puts the stream on stdout in whatever form was asked for.
//
// The one place in this package that writes a result to stdout without going
// through `app.render`, and it is the exception RFC 0021 decision 9 names: a
// stream has no end at which to write an envelope, so `--json` here is one
// object per line and the single-envelope rule gains exactly one documented
// hole. TestNoCommandPrintsAReportItself knows about this function by name.
func (a *App) copyLogs(ctx context.Context, stream *ops.LogStream) error {
	// Whatever happens from here, the envelope must not be written: either
	// records have already gone out and appending one would corrupt the
	// stream, or nothing has and the error is the whole story on stderr.
	// The exit code is the contract for "did this end cleanly".
	if a.json != nil {
		a.jsonStreamed = true
		err := stream.Lines(func(line ports.LogLine) error {
			return json.NewEncoder(a.Stream.Out).Encode(line)
		})
		return endOfStream(ctx, err)
	}

	// Byte for byte, in both human modes. There is nothing here for a view
	// to decide: the runtime's own framing is what every `docker compose
	// logs` example shows, and a manager that re-laid-out somebody's log
	// lines would be the reason they went back to running docker by hand.
	_, err := io.Copy(a.Stream.Out, stream)
	return endOfStream(ctx, err)
}

// endOfStream decides whether a stream that stopped is a failure.
//
// Ctrl-C during `--follow` is an operator who has finished reading, so it exits
// 0: the reader is closed, the runtime's process group is signalled, and there
// is nothing to report. Anything else the runtime did is a real error with the
// runtime's own message.
func endOfStream(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return nil
	}
	if err == nil || errors.Is(err, io.EOF) {
		return nil
	}
	return domain.RuntimeError(err, "the log stream ended early")
}

// parseSince reads `--since`.
//
// Two forms and no third: a duration back from now, or an absolute instant. A
// timestamp with no zone is refused rather than assumed local, because "which
// midnight" is exactly the question a log query must not guess -- and the
// machine's zone is rarely the operator's.
func parseSince(value string, now time.Time) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}

	if d, err := time.ParseDuration(value); err == nil {
		if d < 0 {
			return time.Time{}, domain.Usage("--since %s is negative", value).
				WithHint("a duration counts backwards from now: `--since 10m`")
		}
		return now.Add(-d), nil
	}
	if at, err := time.Parse(time.RFC3339, value); err == nil {
		return at, nil
	}

	// A timestamp that parses only without a zone gets its own message: the
	// operator wrote a time, and "invalid value" would send them looking at
	// the date rather than at the missing offset.
	if _, err := time.Parse("2006-01-02T15:04:05", value); err == nil {
		return time.Time{}, domain.Usage("--since %s names no time zone", value).
			WithHint("add one, e.g. %sZ for UTC — which midnight is meant is not "+
				"something a log query should guess", value)
	}
	return time.Time{}, domain.Usage("--since %s is neither a duration nor a timestamp", value).
		WithHint("a duration counts backwards from now (`10m`, `2h`), " +
			"or an RFC 3339 instant with a zone (`2026-08-10T09:12:33Z`)")
}

func newPsCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "ps",
		Short: "List the deployment's containers",
		Long: "The service table on its own: what is running, whether it is healthy,\n" +
			"which image, and what the runtime says about it.\n\n" +
			"`morzer status` answers this and three other questions at once. This one\n" +
			"exists because an operator watching a crash loop asks it repeatedly and\n" +
			"does not want the other three answers each time.\n\n" +
			"Takes no lock and changes nothing.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			services, err := ops.ListServices(cmd.Context(), app.Deps)
			if err != nil {
				return err
			}
			return app.render(services)
		},
	}
}

// statsWatchFloor is the fastest `--watch` will re-sample.
//
// Below a second the reading is mostly the sampler: `docker stats` walks every
// container's cgroup, and an interval shorter than that measures the manager
// rather than the deployment.
const statsWatchFloor = time.Second

func newStatsCommand(app *App) *cobra.Command {
	var (
		watch    bool
		interval time.Duration
	)

	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Show CPU, memory and I/O per container",
		Long: "One sample of what the deployment is using, one row per container.\n\n" +
			"Never an aggregate per service: a scaled service is several containers,\n" +
			"and one row under the service's name would be one replica's numbers\n" +
			"wearing the whole service's label. The total line covers the two figures\n" +
			"that add -- CPU and memory -- and not the memory limit, which does not.\n\n" +
			"A dash rather than a zero where the host does not account for something:\n" +
			"block I/O is unmeasured under a rootless daemon, and a container that has\n" +
			"written nothing also reports zero.\n\n" +
			"`--watch` re-samples until interrupted. In a terminal it redraws; in a\n" +
			"pipe or a journal it appends a block per sample, because a log that\n" +
			"rewrites itself is not a log.",
		Example: "  morzer stats\n" +
			"  morzer stats --watch --interval 5s\n" +
			"  morzer stats --json | jq '[.[] | .memory_bytes] | add'",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !watch {
				stats, err := ops.SampleStats(cmd.Context(), app.Deps)
				if err != nil {
					return err
				}
				return app.render(stats)
			}

			if app.json != nil {
				// Decision 10: the streaming exception is one
				// this design carries once, and `logs` has it.
				return domain.Usage("--watch and --json cannot be combined").
					WithHint("loop around `morzer stats --json`, which is also the " +
						"form that composes with `sleep`")
			}
			if interval < statsWatchFloor {
				return domain.Usage("--interval %s is below the %s floor",
					interval, statsWatchFloor).
					WithHint("a shorter interval measures the sampler rather than " +
						"the deployment")
			}
			return app.watchStats(cmd.Context(), interval)
		},
	}

	f := cmd.Flags()
	f.BoolVar(&watch, "watch", false, "re-sample until interrupted")
	f.DurationVar(&interval, "interval", 2*time.Second, "how often --watch re-samples")

	return cmd
}

// watchStats re-samples until the operator stops it.
//
// Two shapes, and the difference is not cosmetic. At a terminal it redraws one
// frame, because fifty copies of the same table would bury whatever was on the
// screen before. Everywhere else it appends -- which is the opposite of what
// `status --watch` does, and deliberately: a status watch is a dashboard with
// nothing worth keeping, while a sequence of samples is a time series, and
// `morzer stats --watch > samples.txt` for ten minutes is a real way to catch a
// leak.
func (a *App) watchStats(ctx context.Context, interval time.Duration) error {
	if a.rich() {
		a.plain.Mute()
		defer a.plain.Unmute()

		return tty.Watch(ctx, tty.WatchOptions[[]ports.ServiceStats]{
			Output:   a.Stream.Err,
			Input:    a.terminalInput(),
			Theme:    a.theme(),
			Interval: interval,
			Subject:  "statistics",
			Refresh: func(ctx context.Context) ([]ports.ServiceStats, error) {
				return ops.SampleStats(ctx, a.Deps)
			},
			Body: views.StatsDoc,
			// A daemon that refuses twice running has gone rather
			// than hiccuped, and a watch that redrew the error
			// until somebody pressed q would exit 0 about it.
			StopAfterFailures: 2,
		})
	}
	return a.appendStats(ctx, interval)
}

// appendStats writes one block per sample.
//
// A failed sample prints its reason and the watch continues: a daemon hiccup
// must not end something an operator set running and walked away from. Two in a
// row ends it non-zero, because by then it is not a hiccup.
func (a *App) appendStats(ctx context.Context, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	failures := 0
	for {
		stats, err := ops.SampleStats(ctx, a.Deps)
		switch {
		case ctx.Err() != nil:
			// Interrupted mid-sample. The failure is the operator's own
			// ctrl-C reaching the runtime, and reporting it as one the
			// deployment had would be the manager blaming the daemon
			// for a keystroke.
			return nil

		case err != nil:
			failures++
			if failures >= 2 {
				return err
			}
			fmt.Fprintf(a.Stream.Err, "cannot read statistics: %s\n",
				domain.AsError(err).Message)
		default:
			failures = 0
			// Stamped, because a file holding twenty tables with
			// nothing between them is not a time series -- which is
			// the whole reason this form appends rather than
			// redraws.
			if renderErr := a.render(views.Sample{At: time.Now(), Stats: stats}); renderErr != nil {
				return renderErr
			}
		}

		select {
		case <-ctx.Done():
			// Interrupted is how a watch ends. The operator
			// stopped reading; nothing failed.
			return nil
		case <-ticker.C:
		}
	}
}

func newExecCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec <service> -- <command> [args...]",
		Short: "Run a command inside a running service",
		Long: "Runs one command inside a running container of the named service and\n" +
			"propagates its exit code, so `morzer exec db -- psql -c 'select 1'`\n" +
			"fails an invocation whose command failed.\n\n" +
			"Everything after `--` is the command line inside the container and\n" +
			"nothing else: there is no --user, and no shortcut for running as root.\n" +
			"A manager flag that made privilege escalation a keystroke would be a\n" +
			"manager opinion about something it cannot audit.\n\n" +
			"Not an interactive shell. There is no TTY and no stdin, so a command\n" +
			"that prompts will wait for an answer nobody can give.\n\n" +
			"Journalled with its argv and never its output, with this installation's\n" +
			"known secret values scrubbed. A credential the manager has never been\n" +
			"told about is beyond that, so an argv is still not the place for one.\n\n" +
			"Takes no lock, and refuses a service that is not running.",
		Example: "  morzer exec db -- psql -U demo -c 'select count(*) from users'\n" +
			"  morzer exec app -- ls -la /var/lib/demo\n" +
			"  morzer exec app -- env | grep DEMO_",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			// ArgsLenAtDash is where cobra saw the terminator. Without
			// one, `morzer exec app ls` would run `ls` too -- which is
			// convenient right up to the first `morzer exec app --json`,
			// where the flag belongs to the manager and the operator
			// meant it for the container.
			if cmd.ArgsLenAtDash() != 1 {
				return domain.Usage("the command inside the container goes after `--`").
					WithHint("morzer exec %s -- %s", args[0], strings.Join(args[1:], " "))
			}

			// Refused rather than ignored. The command is the
			// operator's own and the manager cannot say what it would
			// do, so there is no plan to show -- and a `--dry-run`
			// that ran it anyway is how somebody's `rm` gets executed
			// by the flag they typed to stop it.
			if app.Flags.dryRun {
				return domain.Usage("--dry-run cannot plan a command inside a container").
					WithHint("morzer exec runs what you name and nothing else; " +
						"the argv on your command line is the whole plan")
			}

			result, err := ops.ExecInService(cmd.Context(), app.Deps, ops.ExecOptions{
				Service: args[0],
				Argv:    args[1:],
			})
			if err != nil {
				return err
			}

			if app.json != nil {
				app.jsonData = result
			} else {
				// The command's own streams, unchanged and
				// unframed: an operator piping `morzer exec db --
				// pg_dump` into a file must get the dump and not
				// a report about it.
				fmt.Fprint(app.Stream.Err, result.Stderr)
				app.passThrough(result.Stdout)
			}

			// The container's status, propagated. A manager that
			// returned 0 for a command that failed inside would make
			// this unusable in a script.
			if result.ExitCode != 0 {
				return exitStatus{
					code: result.ExitCode,
					cause: domain.RuntimeError(domain.ErrRuntime,
						"the command in %s exited %d",
						result.Service, result.ExitCode),
				}
			}
			return nil
		},
	}
	return cmd
}

// exitStatus carries a container command's own exit code out to the process
// status.
//
// It wraps a domain error rather than replacing one, because two different
// readers need two different things. A `--json` consumer needs the envelope it
// gets for every other failure, with a code that means something in this
// program's vocabulary. A shell needs `$?` to be what the command inside the
// container returned -- and those codes are not this program's: `psql` exits 3
// for a script error while 3 is morzer's preflight failure, so the mapping
// table cannot be asked to serve both.
//
// Nothing is printed for it in human mode: the command already said whatever it
// had to say on its own streams, and a manager sentence underneath would be
// this program taking the credit for somebody else's error message.
type exitStatus struct {
	cause *domain.Error
	code  int
}

func (e exitStatus) Error() string { return e.cause.Error() }

// Unwrap is what lets domain.AsError and the JSON envelope see an ordinary
// runtime error underneath, while the process status stays the container's.
func (e exitStatus) Unwrap() error { return e.cause }

// exitCodeFor is domain.ExitCode, with the one case that outranks the table.
func exitCodeFor(err error) int {
	var status exitStatus
	if errors.As(err, &status) {
		return status.code
	}
	return domain.ExitCode(err)
}

// silentFailure reports whether the error has already been reported by whatever
// produced it.
func silentFailure(err error) bool {
	var status exitStatus
	return errors.As(err, &status)
}

// passThrough puts bytes this program did not compose on stdout.
//
// The second half of the exception `copyLogs` carries, and the same argument:
// what a command inside a container printed is the operator's answer, and a
// view that framed, wrapped or coloured it would break the pipe it was written
// for. TestNoCommandPrintsAReportItself knows this function by name.
//
// Nothing is returned: a failed write to stdout is a closed pipe -- `morzer exec
// … | head` -- and reporting it would turn the ordinary end of a pipeline into
// a failed command.
func (a *App) passThrough(s string) {
	_, _ = io.WriteString(a.Stream.Out, s)
}
