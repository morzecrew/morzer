package clitest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/test/clitest"
)

// `completion install` driven the way an operator drives it, against a fake
// home. What the unit tests pin is the table; what these pin is that the file
// arrives, that running twice changes nothing, and that a shell this command
// cannot place a file for still hands over a script.

// fakeHome points the whole XDG layout at a temporary directory, so a test
// never writes into the developer's own completion directories.
func fakeHome(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

func TestCompletionInstallWritesWhereItSaidItWould(t *testing.T) {
	home := fakeHome(t)
	r := clitest.New(t)

	// The path first, because that is the contract an install script
	// depends on: `$(morzer completion install --print-path)`.
	printed := r.Run("completion", "install", "zsh", "--print-path").ExitCode(0)
	path := strings.TrimSpace(printed.Stdout)

	if want := filepath.Join(home, ".local", "share", "zsh", "site-functions", "_morzer"); path != want {
		t.Fatalf("--print-path printed %q, want %q", path, want)
	}
	// Exactly the path and a newline. Anything else on stdout ends up in
	// somebody's shell variable.
	if printed.Stdout != path+"\n" {
		t.Errorf("stdout carries more than the path:\n%q", printed.Stdout)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("--print-path wrote the file it was only asked to name")
	}

	r.Run("completion", "install", "zsh").ExitCode(0)

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("nothing at the path the command printed: %v", err)
	}
	if !strings.Contains(string(written), "compdef") {
		t.Errorf("the file is not a zsh completion:\n%s", first(string(written), 200))
	}

	// zsh needs the directory on fpath, and the note is the only thing that
	// tells an operator so.
	r.Run("completion", "install", "zsh").ExitCode(0).StderrContains("fpath")
}

func TestCompletionInstallIsIdempotent(t *testing.T) {
	fakeHome(t)
	r := clitest.New(t)

	path := strings.TrimSpace(
		r.Run("completion", "install", "fish", "--print-path").ExitCode(0).Stdout)

	r.Run("completion", "install", "fish").ExitCode(0)
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// A provisioning script that runs this on every converge must change
	// nothing after the first run: same path, same bytes.
	r.Run("completion", "install", "fish").ExitCode(0)
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Error("a second install produced different bytes")
	}
}

func TestCompletionInstallCreatesTheDirectory(t *testing.T) {
	home := fakeHome(t)
	r := clitest.New(t)

	// A fresh machine has none of these directories. Refusing to create one
	// would make this useless in exactly the case it exists for -- the
	// install script, on a host that has never had a completion.
	dir := filepath.Join(home, ".local", "share", "bash-completion", "completions")
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("the fixture already has the directory, so this proves nothing")
	}

	r.Run("completion", "install", "bash").ExitCode(0).
		StderrContains("bash-completion")

	if _, err := os.Stat(filepath.Join(dir, "morzer")); err != nil {
		t.Errorf("the directory was not created: %v", err)
	}
}

func TestAShellItCannotPlaceStillGetsAScript(t *testing.T) {
	fakeHome(t)
	r := clitest.New(t)

	// Exit 0, because the operator asked for a completion and there is one
	// -- they simply have to put it somewhere themselves. Failing here
	// would also fail an install script's optional completion step, which
	// is a poor reason to fail an install.
	out := r.Run("completion", "install", "nushell").ExitCode(0)

	if !strings.Contains(out.Stdout, "morzer") || len(out.Stdout) < 500 {
		t.Errorf("no completion script on stdout:\n%s", first(out.Stdout, 300))
	}
	out.StderrContains("bash", "zsh", "fish")
}

func TestPrintPathRefusesAShellItCannotPlace(t *testing.T) {
	fakeHome(t)
	r := clitest.New(t)

	// The one caller of --print-path is a script capturing the path. There
	// is none, and a completion script on stdout would be captured as one.
	out := r.Run("completion", "install", "nushell", "--print-path").Failed()
	if strings.Contains(out.Stdout, "#!") || len(out.Stdout) > 0 {
		t.Errorf("stdout is not empty:\n%s", first(out.Stdout, 200))
	}
}

func first(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// TestPrintPathHonoursTheOutputMode.
//
// `--print-path` writes a value a script substitutes, which is the same shape
// as `morzer config get` and takes the same route: the bare value in a human
// mode, and the envelope under `--json`. Writing the raw path in both would put
// a path *and* an envelope on stdout, which is the one thing `--json` promises
// never to do.
func TestPrintPathHonoursTheOutputMode(t *testing.T) {
	fakeHome(t)
	r := clitest.New(t)

	plain := strings.TrimSpace(r.Run("completion", "install", "fish", "--print-path").
		ExitCode(0).Stdout)

	out := r.Run("--json", "completion", "install", "fish", "--print-path").ExitCode(0)
	out.FieldEquals("ok", true)
	if got := out.Field("data.value"); got != plain {
		t.Errorf("the envelope carries %v, want the same path the plain form printed (%s)",
			got, plain)
	}
}

// TestAnUnplaceableShellUnderJSONRefusesRatherThanEmittingAScript.
//
// The human answer is the script on stdout and exit 0. Under `--json` that
// would be a shell script followed by an envelope, and nobody sources a
// completion out of one — so the refusal is what keeps the single-object
// contract, at no cost.
func TestAnUnplaceableShellUnderJSONRefusesRatherThanEmittingAScript(t *testing.T) {
	fakeHome(t)
	r := clitest.New(t)

	out := r.Run("--json", "completion", "install", "nushell").Failed()
	out.FieldEquals("ok", false)
	if strings.Contains(out.Stdout, "compdef") || strings.Contains(out.Stdout, "complete -o") {
		t.Errorf("a completion script reached stdout under --json:\n%s", first(out.Stdout, 200))
	}
}
