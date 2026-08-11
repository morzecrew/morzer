package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
)

// The path table is the whole of `completion install`, and it is the part
// nothing else can catch: a completion written to the wrong directory produces
// no error — the shell simply reads nothing, and Tab does nothing. So every row
// is asserted against a fake home rather than left to an operator to discover.

// env builds a lookup over a fixed set, so a test says exactly what the process
// environment holds rather than depending on the machine running it.
func env(pairs map[string]string) func(string) string {
	return func(name string) string { return pairs[name] }
}

func TestEachShellGetsThePathItReadsFrom(t *testing.T) {
	const home = "/home/ada"

	for shell, want := range map[string]string{
		"bash": "/home/ada/.local/share/bash-completion/completions/morzer",
		"zsh":  "/home/ada/.local/share/zsh/site-functions/_morzer",
		"fish": "/home/ada/.config/fish/completions/morzer.fish",
	} {
		t.Run(shell, func(t *testing.T) {
			got, err := completionPath(shell, false, env(nil), home)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("got %s, want %s", got, want)
			}
		})
	}
}

func TestTheXDGBasesAreHonoured(t *testing.T) {
	// The defaults above are what the specification says to use when these
	// are unset. Where they are set, they win — an operator who moved their
	// data directory did so to move files like this one.
	vars := env(map[string]string{
		"XDG_DATA_HOME":   "/mnt/data",
		"XDG_CONFIG_HOME": "/mnt/config",
	})

	for shell, want := range map[string]string{
		"bash": "/mnt/data/bash-completion/completions/morzer",
		"zsh":  "/mnt/data/zsh/site-functions/_morzer",
		"fish": "/mnt/config/fish/completions/morzer.fish",
	} {
		got, err := completionPath(shell, false, vars, "/home/ada")
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s: got %s, want %s", shell, got, want)
		}
	}
}

// TestARelativeXDGBaseIsIgnoredRatherThanUsed.
//
// The specification requires these to be absolute and requires a relative value
// to be ignored. Honouring one writes the completion under whatever directory
// the operator happened to be in — which is the failure this whole table exists
// to prevent, since a completion in the wrong place produces no error at all.
func TestARelativeXDGBaseIsIgnoredRatherThanUsed(t *testing.T) {
	vars := env(map[string]string{
		"XDG_DATA_HOME":   "relative/data",
		"XDG_CONFIG_HOME": "../config",
	})

	for shell, want := range map[string]string{
		"bash": "/home/ada/.local/share/bash-completion/completions/morzer",
		"zsh":  "/home/ada/.local/share/zsh/site-functions/_morzer",
		"fish": "/home/ada/.config/fish/completions/morzer.fish",
	} {
		got, err := completionPath(shell, false, vars, "/home/ada")
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s: got %s, want the $HOME default %s", shell, got, want)
		}
	}
}

func TestSystemPathsIgnoreTheHomeDirectoryEntirely(t *testing.T) {
	// Including the XDG bases: `sudo morzer completion install --system`
	// runs with root's environment or the operator's depending on how sudo
	// is configured, and a system path that moved with either would land
	// somewhere no shell reads.
	vars := env(map[string]string{"XDG_DATA_HOME": "/mnt/data"})

	for shell, want := range map[string]string{
		"bash": "/usr/share/bash-completion/completions/morzer",
		"zsh":  "/usr/share/zsh/site-functions/_morzer",
		"fish": "/usr/share/fish/vendor_completions.d/morzer.fish",
	} {
		got, err := completionPath(shell, true, vars, "/home/ada")
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s: got %s, want %s", shell, got, want)
		}
	}
}

func TestAShellItCannotPlaceAFileForIsNamedWithTheOnesItCan(t *testing.T) {
	_, err := completionPath("nushell", false, env(nil), "/home/ada")
	if err == nil {
		t.Fatal("an unknown shell resolved to a path")
	}
	if code := domain.ExitCode(err); code != domain.ExitUsage {
		t.Errorf("exit %d, want %d", code, domain.ExitUsage)
	}

	hint := domain.AsError(err).Hint
	for _, shell := range []string{"bash", "zsh", "fish"} {
		if !strings.Contains(hint, shell) {
			t.Errorf("the hint does not name %s: %q", shell, hint)
		}
	}
}

func TestNoShellAtAllSaysSoRatherThanGuessing(t *testing.T) {
	// An empty $SHELL is not bash. Guessing would write a file the
	// operator's shell never reads, and the silence is the whole problem.
	_, err := completionPath("", false, env(nil), "/home/ada")
	if err == nil {
		t.Fatal("an unnamed shell resolved to a path")
	}
	if msg := domain.AsError(err).Message; !strings.Contains(msg, "SHELL") {
		t.Errorf("the refusal does not name $SHELL: %q", msg)
	}
}

func TestNoHomeDirectoryIsARefusalAndNotARelativePath(t *testing.T) {
	// The failure this prevents is `.local/share/...` relative to whatever
	// the working directory happens to be, which writes a file nothing
	// reads and reports success.
	got, err := completionPath("bash", false, env(nil), "")
	if err == nil {
		t.Fatalf("an empty home resolved to %q", got)
	}
	if hint := domain.AsError(err).Hint; !strings.Contains(hint, "--system") {
		t.Errorf("the refusal does not name the way out: %q", hint)
	}
}

// TestAnUnsetShellStaysUnsetRatherThanBecomingADot.
//
// `filepath.Base("")` is `"."`, so an unset $SHELL used to arrive at the
// refusal as a shell named `.` — which produced "`.` is not a shell this
// command can place a completion for" and a hint offering `morzer completion .`,
// a command that does not exist. The branch that says $SHELL is unset was
// reachable only by typing an empty argument, which is nobody.
//
// The test above resolves the path from a shell name and so never saw this: the
// defect lives between the two functions, in the one composition that runs when
// an operator types `morzer completion install` with no argument at all.
func TestAnUnsetShellStaysUnsetRatherThanBecomingADot(t *testing.T) {
	for _, value := range []string{"", "   "} {
		shell := shellFromEnv(env(map[string]string{"SHELL": value}))
		if shell != "" {
			t.Errorf("SHELL=%q resolved to shell %q", value, shell)
		}

		msg := domain.AsError(unknownShell(shell)).Message
		if !strings.Contains(msg, "SHELL") {
			t.Errorf("SHELL=%q produced %q, which does not say the variable is unset",
				value, msg)
		}
	}
}

func TestTheShellIsTheBaseNameOfItsPath(t *testing.T) {
	for value, want := range map[string]string{
		"/usr/bin/zsh":  "zsh",
		"/bin/bash":     "bash",
		"  /bin/fish  ": "fish",
		// Unset, empty, or whitespace: the same answer, and not `.`.
		"":    "",
		"   ": "",
		// Not a shell this command knows, which is the same answer as
		// an unset variable and is handled the same way.
		"/bin/false": "false",
	} {
		if got := shellFromEnv(env(map[string]string{"SHELL": value})); got != want {
			t.Errorf("SHELL=%q resolved to %q, want %q", value, got, want)
		}
	}
}

// TestEveryPlaceableShellHasAScriptToWrite.
//
// The table says where a shell's completion goes; the switch in
// completionScript says how to generate one. They are two lists that must
// agree, and adding a row to the first without a case in the second produces a
// command that resolves a path, creates the directory, and then refuses -- with
// "not a shell this command can place a completion for", about a shell it just
// finished placing.
func TestEveryPlaceableShellHasAScriptToWrite(t *testing.T) {
	root := CommandTree()

	for shell := range completionTargets {
		script, err := completionScript(root, shell)
		if err != nil {
			t.Errorf("%s is in the path table and has no script: %v", shell, err)
			continue
		}
		if len(script) == 0 {
			t.Errorf("%s generated an empty script", shell)
		}
	}

	if _, err := completionScript(root, "nushell"); err == nil {
		t.Error("a shell with no case generated a script")
	}
}

func TestEveryPlaceableShellHasASystemPathToo(t *testing.T) {
	// The table is one map, and a row with a home path and no system path
	// would make `--system` write to `/morzer` — under root, silently.
	for shell, target := range completionTargets {
		if target.system == "" || !filepath.IsAbs(target.system) {
			t.Errorf("%s has no absolute system directory: %q", shell, target.system)
		}
		if target.file == "" {
			t.Errorf("%s names no file", shell)
		}
	}
}
