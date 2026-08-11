package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/domain"
)

// Where a shell reads completions from, and how to put one there.
//
// cobra generates the script and always could; what an operator wants is for it
// to end up somewhere their shell reads, which differs per shell and is the
// part nobody remembers. This is the only implementation of that knowledge in
// the project, and it stays that way: RFC 0022's install script calls
// `morzer completion install --print-path` rather than learning the paths
// itself, because a completion written to the wrong directory produces no error
// at all -- just a Tab key that does nothing -- so a second copy would drift
// without ever announcing it.

// shellTarget is where one shell's completion goes, and what it needs besides
// the file.
type shellTarget struct {
	// Dir and File are relative to the home-directory base or to the
	// system prefix, resolved by target().
	dir  func(env func(string) string, home string) string
	file string

	// system is the distribution-wide directory, used by --system.
	system string

	// note is printed after a successful write when the shell needs
	// something more than the file. Empty when it does not.
	note func(path string) string
}

// completionTargets is a table rather than a heuristic.
//
// A heuristic that guessed wrong would write a file that is never read, and the
// failure of a completion is silence: no error, no warning, just a Tab key that
// does nothing. Every path here is one a shell documents.
var completionTargets = map[string]shellTarget{
	"bash": {
		dir: func(env func(string) string, home string) string {
			return filepath.Join(dataHome(env, home), "bash-completion", "completions")
		},
		file:   "morzer",
		system: "/usr/share/bash-completion/completions",
		note: func(string) string {
			// bash reads nothing from that directory on its own:
			// the bash-completion package is what loads it, and
			// dynamic loading from the user directory needs 2.8 or
			// newer. Said every time rather than probed, because
			// probing means sourcing the shell's own startup, and a
			// note an operator can ignore is cheaper than a check
			// that runs somebody's rc file.
			return "bash reads this directory through the bash-completion package " +
				"(2.8 or newer). Without it, source the file from ~/.bashrc instead."
		},
	},
	"zsh": {
		dir: func(env func(string) string, home string) string {
			return filepath.Join(dataHome(env, home), "zsh", "site-functions")
		},
		file:   "_morzer",
		system: "/usr/share/zsh/site-functions",
		note: func(path string) string {
			return "if this directory is not already on your fpath, add " +
				"`fpath=(" + filepath.Dir(path) + " $fpath)` to ~/.zshrc before `compinit`."
		},
	},
	"fish": {
		dir: func(env func(string) string, home string) string {
			return filepath.Join(configHome(env, home), "fish", "completions")
		},
		file:   "morzer.fish",
		system: "/usr/share/fish/vendor_completions.d",
		// fish reads it on the next start. Nothing to say.
	},
}

// dataHome and configHome are the XDG bases, with the defaults the
// specification gives when the variables are unset.
func dataHome(env func(string) string, home string) string {
	if v := env("XDG_DATA_HOME"); v != "" {
		return v
	}
	return filepath.Join(home, ".local", "share")
}

func configHome(env func(string) string, home string) string {
	if v := env("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	return filepath.Join(home, ".config")
}

// completionPath resolves where one shell's completion belongs.
//
// The environment and the home directory are parameters rather than reads, so
// the table is testable against a fake HOME -- which is what makes it a table
// worth having, since every entry is otherwise only checked by an operator
// noticing that Tab does nothing.
func completionPath(shell string, system bool, env func(string) string, home string) (string, error) {
	target, ok := completionTargets[shell]
	if !ok {
		return "", unknownShell(shell)
	}
	if system {
		return filepath.Join(target.system, target.file), nil
	}
	if home == "" {
		return "", domain.Usage("cannot resolve your home directory").
			WithHint("set HOME, or pass --system to write the distribution's directory")
	}
	return filepath.Join(target.dir(env, home), target.file), nil
}

func unknownShell(shell string) error {
	named := completionShells()
	if shell == "" {
		return domain.Usage("no shell named, and $SHELL is not set").
			WithHint("name one of %s", strings.Join(named, ", "))
	}
	return domain.Usage("%s is not a shell this command can place a completion for", shell).
		WithHint("it can place %s; `morzer completion %s` writes the script to stdout",
			strings.Join(named, ", "), shell)
}

// completionShells are the shells this command can place a file for, sorted.
func completionShells() []string {
	out := make([]string, 0, len(completionTargets))
	for name := range completionTargets {
		out = append(out, name)
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// shellFromEnv reads the shell out of $SHELL, which holds a path.
//
// The base name only, and no interpretation beyond it: `/usr/bin/zsh` is zsh
// and `/bin/false` is not a shell this command knows, which is the same answer
// as an unset variable and is handled the same way.
func shellFromEnv(env func(string) string) string {
	return filepath.Base(strings.TrimSpace(env("SHELL")))
}

func newCompletionInstallCommand(app *App) *cobra.Command {
	var (
		system    bool
		printPath bool
	)

	cmd := machineScope(&cobra.Command{
		Use:   "install [bash|zsh|fish]",
		Short: "Write the completion script where this shell will read it",
		Long: "Generates the completion script and puts it where the named shell reads\n" +
			"completions from, creating the directory when it does not exist. The\n" +
			"shell defaults to the base name of $SHELL.\n\n" +
			"Where each one reads from is a table rather than a guess, because the\n" +
			"failure of a completion is silence: no error, no warning, just a Tab key\n" +
			"that does nothing. `--print-path` prints the path and writes nothing,\n" +
			"which is what a script uses to report where the file went.\n\n" +
			"A shell it cannot place a file for is not a failure: the script goes to\n" +
			"stdout with a note naming the ones it can, and the command exits 0. That\n" +
			"keeps `morzer completion install > somewhere` useful.\n\n" +
			"Writes inside your home directory. `--system` writes the distribution's\n" +
			"own completion directory instead, and needs the privileges for it.",
		Example: "  morzer completion install\n" +
			"  morzer completion install zsh\n" +
			"  morzer completion install --print-path\n" +
			"  sudo morzer completion install bash --system",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			shell := shellFromEnv(os.Getenv)
			if len(args) == 1 {
				shell = args[0]
			}

			path, err := completionPath(shell, system, os.Getenv, os.Getenv("HOME"))
			if err != nil {
				if printPath {
					// The one caller of --print-path is a
					// script capturing the path. There is
					// none, and saying so beats printing
					// something it would then write to.
					return err
				}
				return app.completionToStdout(cmd.Root(), err)
			}

			if printPath {
				// Exactly the path and a newline: the whole
				// point is `$(morzer completion install
				// --print-path)`, and anything else in it would
				// end up in somebody's variable.
				app.passThrough(path + "\n")
				return nil
			}

			return app.installCompletion(cmd.Root(), shell, path)
		},
	})

	f := cmd.Flags()
	f.BoolVar(&printPath, "print-path", false,
		"print where the completion would go, and write nothing")
	f.BoolVar(&system, "system", false,
		"write the distribution's system-wide completion directory (needs privileges)")

	return cmd
}

// installCompletion generates the script and writes it.
//
// Idempotent by construction: the same path and the same bytes every run, so a
// provisioning script that calls this on every converge changes nothing after
// the first.
func (a *App) installCompletion(root *cobra.Command, shell, path string) error {
	script, err := completionScript(root, shell)
	if err != nil {
		return err
	}

	// 0755, and world-readable on purpose: `--system` writes
	// /usr/share/..., which every user's shell has to be able to read, and
	// a directory root created at 0750 would make completions invisible to
	// everyone but root. Under $HOME the mode is the one the XDG
	// directories already have.
	//nolint:gosec // a completion directory is read by other users' shells by design
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return domain.Internal(err, "cannot create %s", filepath.Dir(path))
	}
	if err := os.WriteFile(path, script, 0o644); err != nil {
		return domain.Internal(err, "cannot write %s", path).
			WithHint("`--system` writes a directory that usually needs root; " +
				"without it the file goes under your home directory")
	}

	// On stderr, because stdout is where `--print-path` puts the path and a
	// caller that captured both would get a sentence in its variable.
	fmt.Fprintf(a.Stream.Err, "wrote the %s completion to %s\n", shell, path)
	if note := completionTargets[shell].note; note != nil {
		fmt.Fprintf(a.Stream.Err, "%s\n", note(path))
	}
	return nil
}

// completionToStdout is the answer for a shell this command cannot place a file
// for.
//
// Exit 0 with the script on stdout, because the operator asked for a completion
// and there is one -- they simply have to put it somewhere themselves. Failing
// here would also fail the optional completion step of an install script, which
// is a poor reason to fail an install.
func (a *App) completionToStdout(root *cobra.Command, why error) error {
	script, err := completionScript(root, "bash")
	if err != nil {
		return err
	}

	// The hint as well as the message: the hint is what names the shells it
	// *can* place, which is the whole content of this answer. Printing only
	// the refusal would leave an operator with a script on stdout and no
	// idea which shells would have had it filed for them.
	e := domain.AsError(why)
	fmt.Fprintf(a.Stream.Err, "%s\n%s\nwriting the bash script to stdout instead.\n",
		e.Message, e.Hint)
	a.passThrough(string(script))
	return nil
}

// completionScript asks cobra for the script.
func completionScript(root *cobra.Command, shell string) ([]byte, error) {
	var b bytes.Buffer

	var err error
	switch shell {
	case "bash":
		err = root.GenBashCompletionV2(&b, true)
	case "zsh":
		err = root.GenZshCompletion(&b)
	case "fish":
		err = root.GenFishCompletion(&b, true)
	default:
		return nil, unknownShell(shell)
	}
	if err != nil {
		return nil, domain.Internal(err, "cannot generate the %s completion", shell)
	}
	return b.Bytes(), nil
}
