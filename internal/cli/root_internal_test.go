package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ui"
)

// TestAnOperationalErrorIsNotReclassifiedAsATypo.
//
// classifyCLIError matched cobra's parse vocabulary anywhere in the message,
// against every error the program can produce -- and "invalid argument" is what
// a kernel says about EINVAL as readily as what cobra says about a bad
// --timeout. Reporting a filesystem failure as exit 2 sends an operator to look
// for a flag they spelled correctly.
func TestAnOperationalErrorIsNotReclassifiedAsATypo(t *testing.T) {
	operational := []string{
		"cannot read /etc/demo/installation.yaml: invalid argument",
		"the release accepts no parameter \"x\"",
		"the target requires at least one credential", // not cobra's phrasing
	}
	for _, msg := range operational {
		if got := domain.ExitCode(classifyCLIError(errors.New(msg))); got == domain.ExitUsage {
			t.Errorf("%q was classified as a usage error", msg)
		}
	}

	// Cobra's own, which begin with the phrase.
	for _, msg := range []string{
		`unknown command "sttaus" for "morzer"`,
		"accepts 1 arg(s), received 2",
		"if any flags in the group [verbose quiet] are set none of the others can be",
	} {
		if got := domain.ExitCode(classifyCLIError(errors.New(msg))); got != domain.ExitUsage {
			t.Errorf("%q exits %d, want %d", msg, got, domain.ExitUsage)
		}
	}

	// A typed error already knows its code and must not be re-decided.
	backup := domain.BackupError(nil, "unknown command in the restore hook")
	if got := domain.ExitCode(classifyCLIError(backup)); got != domain.ExitBackup {
		t.Errorf("a typed error was reclassified: exit %d", got)
	}
}

// TestWantsJSONSeesTheFlagCobraNeverReached. Cobra stops at the first unknown
// flag, so a --json after it is never parsed -- and that run is exactly the one
// that owes its caller an error envelope.
func TestWantsJSONSeesTheFlagCobraNeverReached(t *testing.T) {
	if !wantsJSON(false, []string{"--wat", "--json"}) {
		t.Error("--json after an unparsed flag was not seen")
	}
	if !wantsJSON(true, nil) {
		t.Error("the parsed flag was ignored")
	}
	if wantsJSON(false, []string{"status", "--plain"}) {
		t.Error("a run that never asked for json would be given an envelope")
	}
	// Everything after the terminator is an operand: `morzer --wat --
	// --json` asked for plain output and a literal argument that looks
	// like a flag.
	if wantsJSON(false, []string{"--wat", "--", "--json"}) {
		t.Error("an operand after -- was read as a flag")
	}
	// Every boolean spelling cobra itself accepts.
	for _, spelling := range []string{"--json=true", "--json=1", "--json=TRUE", "--json=t"} {
		if !wantsJSON(false, []string{"--wat", spelling}) {
			t.Errorf("%s asked for an envelope and would not have got one", spelling)
		}
	}
	for _, spelling := range []string{"--json=false", "--json=0", "--json=nonsense"} {
		if wantsJSON(false, []string{"--wat", spelling}) {
			t.Errorf("%s was read as a request for an envelope", spelling)
		}
	}
}

// TestConfigDerivesTheLayoutItSitsIn. The flag was parsed and discarded, so a
// systemd unit naming one installation ran against whichever one discovery
// happened to find.
func TestConfigDerivesTheLayoutItSitsIn(t *testing.T) {
	root := t.TempDir()
	app := &App{}
	app.Flags.configDir = filepath.Join(root, "etc", "demo", "installation.yaml")

	paths, err := app.resolvePaths(t.Context())
	if err != nil {
		t.Fatalf("a well-formed --config was refused: %v", err)
	}
	if paths.Product != "demo" {
		t.Errorf("product = %q, want demo", paths.Product)
	}
	if got := paths.InstallationFile(); got != app.Flags.configDir {
		t.Errorf("the layout does not contain the file it was derived from:\n  %s", got)
	}
	if paths.VarDir != filepath.Join(root, "var", "lib", "demo") {
		t.Errorf("the rest of the layout did not follow: %s", paths.VarDir)
	}
}

func TestConfigRefusesWhatItCannotDeriveALayoutFrom(t *testing.T) {
	root := t.TempDir()

	cases := map[string]string{
		"a file that is not the installation file": filepath.Join(root, "etc", "demo", "other.yaml"),
		"a file outside any etc directory":         filepath.Join(root, "srv", "demo", "installation.yaml"),
		"a product name that is not one":           filepath.Join(root, "etc", "Demo Product", "installation.yaml"),
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			app := &App{}
			app.Flags.configDir = path

			if _, err := app.resolvePaths(t.Context()); err == nil {
				t.Fatal("a path the manager cannot derive a layout from was accepted")
			}
		})
	}
}

// Two flags naming different deployments is a question, not something to
// resolve by precedence: whichever lost would be acted on silently.
func TestConfigAndProductMustAgree(t *testing.T) {
	root := t.TempDir()
	app := &App{}
	app.Flags.configDir = filepath.Join(root, "etc", "demo", "installation.yaml")
	app.Flags.product = "other"

	_, err := app.resolvePaths(t.Context())
	if err == nil {
		t.Fatal("--product and --config disagreed and one of them was picked silently")
	}
	if domain.ExitCode(err) != domain.ExitUsage {
		t.Errorf("exit %d, want %d", domain.ExitCode(err), domain.ExitUsage)
	}
}

// TestTheLiveViewReadsOnlyFromATerminal.
//
// The output mode is decided by stdout and stderr, so `morzer apply < /dev/null`
// at a terminal legitimately draws the live view -- and handing its reader a
// pipe would mean raw-mode setup on something that cannot be put in raw mode.
// Nil is the right answer there: Bubble Tea subscribes to no input, nothing
// goes raw, and ctrl-C stays the signal main already handles.
func TestTheLiveViewReadsOnlyFromATerminal(t *testing.T) {
	redirected, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = redirected.Close() }()

	for name, in := range map[string]io.Reader{
		"a pipe":            strings.NewReader(""),
		"a redirected file": redirected,
		"nothing at all":    nil,
	} {
		t.Run(name, func(t *testing.T) {
			app := &App{Stream: ui.Streams{In: in}}
			if got := app.terminalInput(); got != nil {
				t.Errorf("%s was handed to the live view as a keyboard", name)
			}
		})
	}

	// A real terminal is read from, because an embedder that supplied its
	// own pty must have its keys read from there.
	_, slave := openPTY(t)
	app := &App{Stream: ui.Streams{In: slave}}
	if got := app.terminalInput(); got != slave {
		t.Errorf("terminalInput = %v, want the injected terminal", got)
	}
}

// TestConfigAcceptsTheRootThatMeansTheSameLayout. `--root /` and a config under
// /etc name the same place, and the empty root is how that layout is spelled
// internally -- comparing the two spellings verbatim refused a pair that agrees.
func TestConfigAcceptsTheRootThatMeansTheSameLayout(t *testing.T) {
	app := &App{}
	app.Flags.configDir = "/etc/demo/installation.yaml"
	app.Flags.root = "/"

	paths, err := app.resolvePaths(t.Context())
	if err != nil {
		t.Fatalf("--root / and a config under /etc were called different installations: %v", err)
	}
	if paths.EtcDir != "/etc/demo" {
		t.Errorf("EtcDir = %q", paths.EtcDir)
	}
}
