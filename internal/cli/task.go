package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

func newTaskCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Run a command the release declares",
		Long: "A task is a command the release ships for an operator to run by name:\n" +
			"filling a sandbox with demo data, unlocking a user, rebuilding an index.\n" +
			"It runs in a one-shot container of a release service, through the\n" +
			"image's entrypoint, under the deployment lock, and is journalled.\n\n" +
			"The release says which machines a task may run on, and a task meant\n" +
			"for a sandbox is refused on production by name.\n\n" +
			"Docs: https://morzecrew.github.io/morzer/latest/reference/commands/#task",
	}
	cmd.AddCommand(newTaskListCommand(app), newTaskRunCommand(app))
	return cmd
}

func newTaskListCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the tasks the installed release declares",
		Long: "Every task the current release declares: the service it runs in, the\n" +
			"machine modes it allows, whether this machine is one of them, and\n" +
			"whether it takes arguments.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			tasks, err := ops.ListTasks(cmd.Context(), app.Deps)
			if err != nil {
				return err
			}
			return app.render(tasks)
		},
	}
}

func newTaskRunCommand(app *App) *cobra.Command {
	var (
		output bool
		inputs []string
	)
	cmd := &cobra.Command{
		Use:   "run <task> [-- args...]",
		Short: "Run a task the installed release declares",
		Long: "Runs the task as an operation: secrets and configuration are rendered\n" +
			"as `apply` renders them, then the task's service runs to completion\n" +
			"with the task's command. Its dependencies are started first, so a\n" +
			"task runs whether or not the deployment is up.\n\n" +
			"Arguments go after `--`, and only to a task whose release declares\n" +
			"`args: true` for it.\n\n" +
			"A task that declares inputs is given each as --input <name>=<path>.\n" +
			"The file is copied for the run, mounted read-only into the task's\n" +
			"container, and removed when the run ends.\n\n" +
			"The task's output is printed only when asked: --output prints it when\n" +
			"the task finishes, its own stdout on stdout, and --verbose shows it as\n" +
			"it arrives. A task that fails shows the last lines of its stderr. The\n" +
			"invocation exits with the task's exit code.\n\n" +
			"Not an interactive session: there is no TTY and no stdin.",
		Example: "  morzer task list\n" +
			"  morzer task run reindex\n" +
			"  morzer task run seed -- --size s\n" +
			"  morzer task run restore --input snapshot=./catalog.jsonl\n" +
			"  morzer task run report --output > report.txt",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// The task's arguments only after the terminator, for the
			// reason exec gives: without one, `morzer task run seed
			// --json` would hand the manager's flag to the vendor's
			// command or the other way round.
			if dash := cmd.ArgsLenAtDash(); dash > 1 || (dash < 0 && len(args) > 1) {
				return domain.Usage("the task's arguments go after `--`").
					WithHint("morzer task run %s -- %s", args[0], strings.Join(args[1:], " "))
			}
			if cmd.ArgsLenAtDash() == 0 {
				return domain.Usage("the task's name goes before `--`").
					WithHint("morzer task list names the tasks this release declares")
			}

			given, err := parseTaskInputs(inputs)
			if err != nil {
				return err
			}
			opts := ops.TaskOptions{Options: app.operationOptions(), Name: args[0], Args: args[1:], Inputs: given}
			var run ops.TaskRun
			err = app.runOperation(cmd.Context(), func(ctx context.Context) (ops.Result, error) {
				res, err := ops.RunTask(ctx, app.Deps, opts)
				run, _ = res.Data.(ops.TaskRun)
				return res, err
			})
			return app.finishTask(run, err, output)
		},
	}
	cmd.Flags().BoolVar(&output, "output", false,
		"print the task's stdout and stderr when it finishes")
	cmd.Flags().StringArrayVar(&inputs, "input", nil,
		"a file the task declares as an input, as name=path; repeat for each")
	return cmd
}

// parseTaskInputs reads each --input as name=path. A name given twice is
// refused rather than resolved, since an operator who typed it twice meant one
// of the two.
func parseTaskInputs(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(values))
	for _, v := range values {
		name, path, ok := strings.Cut(v, "=")
		if !ok || name == "" || path == "" {
			return nil, domain.Usage("--input %q is not name=path", v).
				WithHint("morzer task list names each task's inputs")
		}
		if _, dup := out[name]; dup {
			return nil, domain.Usage("--input %s is given twice", name)
		}
		out[name] = path
	}
	return out, nil
}

// taskFailureTailLines is how much of a failed task's stderr is shown unasked:
// the runtime's own bound on what an operator watching the run read last.
const taskFailureTailLines = 10

// finishTask passes on what the task printed, when asked, and decides the exit.
//
// After the operation and never during it: the live view owns the terminal
// while one runs, and plain mode's stderr is the step narration. Unasked, only
// a failure's last stderr lines, and not under --verbose, which printed them as
// they arrived (RFC 0035 decision 21). With --json the output is already in
// the envelope.
func (a *App) finishTask(run ops.TaskRun, err error, output bool) error {
	failed := err != nil && run.ExitCode != 0
	switch {
	case a.json != nil:
	case output:
		fmt.Fprint(a.Stream.Err, endLine(run.Stderr))
		a.passThrough(endLine(run.Stdout))
		if run.Truncated {
			a.warnOutputCut("the task's", "")
		}
	case failed && !a.Flags.verbose:
		fmt.Fprint(a.Stream.Err, endLine(lastLinesOf(run.Stderr, taskFailureTailLines)))
		if run.Truncated {
			a.warnOutputCut("the task's", "")
		}
	}
	// The task's own status, as exec's (decision 12). The step's error
	// is already on the screen, and the task's own words follow it.
	if failed {
		return exitStatus{code: run.ExitCode, cause: domain.AsError(err)}
	}
	return err
}

// lastLinesOf is the last n lines of s as they were printed, unlike
// domain.LastLines, which joins them onto one line for an error to quote.
func lastLinesOf(s string, n int) string {
	lines := strings.SplitAfter(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "")
}

// endLine ends text with a newline, so a task's last line is not followed on
// the same line by whatever prints next.
func endLine(s string) string {
	if s == "" || strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}
