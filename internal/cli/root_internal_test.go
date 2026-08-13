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
	// The real command tree, because the scan has to know which flags take
	// a value and a hand-written stub would only pin what this test assumed.
	root := newRootCommand(&App{Stream: ui.DefaultStreams()})
	wantsJSON := func(parsed bool, args []string) bool {
		return wantsJSON(parsed, flagLookup(root, args), args)
	}

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
	// The last one wins, as cobra would read them: an operator who wrote a
	// correction meant the correction.
	if wantsJSON(false, []string{"--json=TRUE", "--wat", "--json=false"}) {
		t.Error("a later --json=false was overruled by an earlier truthy one")
	}
	if !wantsJSON(false, []string{"--json=false", "--wat", "--json"}) {
		t.Error("a later --json was overruled by an earlier false one")
	}

	// A flag that takes a value eats the token after it. `--timeout --json`
	// is cobra reading "--json" as a duration and failing on it -- nobody
	// asked for an envelope, and writing one puts JSON on the stdout of a
	// caller that was never parsing any.
	if wantsJSON(false, []string{"--timeout", "--json", "--wat"}) {
		t.Error("a --json consumed as --timeout's value was read as a request")
	}
	// The same for a flag that only exists on the subcommand, which is why
	// the lookup resolves against the command the arguments select.
	if wantsJSON(false, []string{"init", "--product", "--json", "--wat"}) {
		t.Error("a --json consumed as --product's value was read as a request")
	}
	// And a boolean does not eat anything, so the token after one still
	// counts.
	if !wantsJSON(false, []string{"--dry-run", "--json"}) {
		t.Error("a boolean flag was treated as taking a value")
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

	// The other direction -- that a real terminal *is* read from -- needs a
	// pty to allocate, so it lives beside the rest of the pty tests in
	// prompt_pty_linux_test.go. Both halves matter and only one of them is
	// portable.
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

// TestAmbiguityIsRecordedRatherThanCollapsed.
//
// Discovery used to answer "exactly one, or nothing", and `resolvePaths` fell
// back to the placeholder product for both. A machine with two installations
// therefore reported "no installation found at /etc/morzer" and advised `morzer
// init` — which would have created a third.
//
// The refusal itself lives in the lifecycle layer, where the failed lookup
// happens; what is asserted here is the half the CLI owns, which is that the
// inventory survives the resolution instead of being reduced to a boolean.
func TestAmbiguityIsRecordedRatherThanCollapsed(t *testing.T) {
	root := t.TempDir()
	for _, product := range []string{"other", "demo"} {
		dir := filepath.Join(root, "etc", product)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "installation.yaml"), []byte("product: x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// A file beside them, because /etc holds plenty of those and one read as
	// a product would put a made-up name in a refusal that lists what the
	// machine has.
	if err := os.WriteFile(filepath.Join(root, "etc", "hostname"), []byte("host\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := &App{}
	app.Flags.root = root

	paths, err := app.resolvePaths(t.Context())
	if err != nil {
		t.Fatalf("resolving paths on an ambiguous machine failed: %v", err)
	}

	// Sorted, so a refusal naming them is stable rather than dependent on
	// readdir order.
	if got := app.machineProducts; len(got) != 2 || got[0] != "demo" || got[1] != "other" {
		t.Errorf("machineProducts = %v, want [demo other]", got)
	}
	// And no installation was picked: guessing between two is how a command
	// acts on the wrong deployment.
	if paths.Product != "morzer" {
		t.Errorf("product = %q; one of two installations was chosen", paths.Product)
	}
}

// TestOneInstallationIsStillFound. The regression that would matter most: every
// other test in this repository runs on a machine with exactly one.
func TestOneInstallationIsStillFound(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "etc", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "installation.yaml"), []byte("product: demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := &App{}
	app.Flags.root = root

	paths, err := app.resolvePaths(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if paths.Product != "demo" {
		t.Errorf("product = %q, want demo", paths.Product)
	}
}
