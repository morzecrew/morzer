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
			"for a sandbox is refused on production by name.",
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
	return &cobra.Command{
		Use:   "run <task> [-- args...]",
		Short: "Run a task the installed release declares",
		Long: "Runs the task as an operation: secrets and configuration are rendered\n" +
			"as `apply` renders them, then the task's service runs to completion\n" +
			"with the task's command. Its dependencies are started first, so a\n" +
			"task runs whether or not the deployment is up.\n\n" +
			"Arguments go after `--`, and only to a task whose release declares\n" +
			"`args: true` for it.\n\n" +
			"The task's output is printed when it finishes, its own stdout on\n" +
			"stdout, and the invocation exits with the task's exit code. --verbose\n" +
			"shows the output as it arrives.\n\n" +
			"Not an interactive session: there is no TTY and no stdin.",
		Example: "  morzer task list\n" +
			"  morzer task run reindex\n" +
			"  morzer task run seed -- --size s",
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

			opts := ops.TaskOptions{Options: app.operationOptions(), Name: args[0], Args: args[1:]}
			var run ops.TaskRun
			err := app.runOperation(cmd.Context(), func(ctx context.Context) (ops.Result, error) {
				res, err := ops.RunTask(ctx, app.Deps, opts)
				run, _ = res.Data.(ops.TaskRun)
				return res, err
			})
			return app.finishTask(run, err)
		},
	}
}

// finishTask passes on what the task printed and decides the exit.
//
// After the operation and never during it: the live view owns the terminal
// while one runs, and plain mode's stderr is the step narration (RFC 0035
// decision 11). With --json the output is already in the envelope.
func (a *App) finishTask(run ops.TaskRun, err error) error {
	if a.json == nil {
		fmt.Fprint(a.Stream.Err, endLine(run.Stderr))
		a.passThrough(endLine(run.Stdout))
		if run.Truncated {
			a.warnOutputCut("the task's", "")
		}
	}
	// The task's own status, as exec's (decision 12). The step's error
	// is already on the screen, and the task's own words follow it.
	if err != nil && run.ExitCode != 0 {
		return exitStatus{code: run.ExitCode, cause: domain.AsError(err)}
	}
	return err
}

// endLine ends text with a newline, so a task's last line is not followed on
// the same line by whatever prints next.
func endLine(s string) string {
	if s == "" || strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}
