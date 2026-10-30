package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// newShellCommand starts the operator's shell with MORZER_PRODUCT set.
//
// The variable already exists for a session pinned to one installation (RFC
// 0020), but `sudo` resets the environment and most commands need root, so an
// exported value is gone exactly where it is wanted. Set after sudo, inside the
// process sudo started, it survives. Nothing is written to disk: the selection
// lives in this process tree and ends with it, which keeps RFC 0020 decision 6
// (no `morzer use`).
func newShellCommand(app *App) *cobra.Command {
	return machineScope(&cobra.Command{
		Use:   "shell <product>",
		Short: "Start a shell pinned to one installation",
		Long: "Starts $SHELL, or /bin/sh, with MORZER_PRODUCT set, so every morzer\n" +
			"command inside it acts on that installation without --product. Run it\n" +
			"as `sudo morzer shell <product>`: sudo drops an exported MORZER_PRODUCT,\n" +
			"and this sets it after sudo. --product and --config still override it\n" +
			"for one command. Leaving the shell ends the selection; nothing is saved.\n\n" +
			"The invocation exits with the shell's own status.",
		Example: "  sudo morzer shell demo",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			product := args[0]

			// The shell owns stdout until it exits, so there is no single
			// JSON object this command could promise.
			if app.Flags.json {
				return domain.Usage("shell is interactive, and --json has nothing to report").
					WithHint("drop --json; the shell's own commands take it")
			}

			// One answer to "which installation is this shell on": a
			// second pin inside the first would leave two.
			if current := os.Getenv(ProductEnv); current != "" {
				return domain.Usage("this shell is already pinned to %s", current).
					WithHint("exit it first, then run `morzer shell %s`", product)
			}

			// The installations `ls` counts, broken ones included: the
			// operator may be about to run `doctor` on one. A directory it
			// could not open is not counted, and pinning to one would be a
			// guess -- but when it is the one named, sudo is the way in.
			entries, err := ops.ListInstallations(cmd.Context(), app.Deps, ops.ListOptions{})
			if err != nil {
				return err
			}
			var names []string
			for _, e := range entries {
				if e.Skipped {
					if e.Product == product {
						return domain.Usage("%s cannot be read by this process, so it is not known to be an installation", e.Path).
							WithHint("run `sudo morzer shell %s`", product)
					}
					continue
				}
				names = append(names, e.Product)
			}
			if !slices.Contains(names, product) {
				refusal := domain.Usage("this machine has no installation named %q", product)
				if len(names) == 0 {
					return refusal.WithHint("`morzer ls` lists none; create one with `morzer init`")
				}
				return refusal.WithHint("it has %s", strings.Join(names, ", "))
			}

			shell := os.Getenv("SHELL")
			if shell == "" {
				shell = "/bin/sh"
			}
			child := exec.Command(shell) //nolint:gosec // the operator's own shell, by design
			// The App's streams, which are the process's own files in
			// production: exec hands an *os.File over as the descriptor
			// itself, so the shell gets the terminal, not a copy of it.
			child.Stdin, child.Stdout, child.Stderr = app.Stream.In, app.Stream.Out, app.Stream.Err
			child.Env = append(os.Environ(), ProductEnv+"="+product)

			// The terminal's keys reach this process too. Caught rather than
			// ignored: an ignored signal is inherited through exec, and the
			// shell's own commands would then shrug off Ctrl-C. The root
			// context's cancellation is not watched either -- the shell is
			// the operator's to leave.
			keys := make(chan os.Signal, 8)
			signal.Notify(keys, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTSTP)
			defer func() { signal.Stop(keys); close(keys) }()
			go func() {
				for range keys { //nolint:revive // drained, deliberately
				}
			}()

			fmt.Fprintf(app.Stream.Err, "%s: a shell pinned to this installation; exit to leave\n", product)
			runErr := child.Run()
			fmt.Fprintf(app.Stream.Err, "%s: left the pinned shell\n", product)

			var exit *exec.ExitError
			switch {
			case runErr == nil:
				return nil
			case errors.As(runErr, &exit):
				code := exit.ExitCode()
				// Killed by a signal: report it as a shell does, 128 plus
				// the signal, so `$?` after the shell still says which.
				if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
					code = 128 + int(ws.Signal())
				}
				return exitStatus{code: code,
					cause: domain.Usage("the shell exited with status %d", code)}
			default:
				return domain.Usage("cannot start %s: %v", shell, runErr).
					WithHint("set SHELL to a shell this machine has")
			}
		},
	})
}
