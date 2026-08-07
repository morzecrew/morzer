package cli

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
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
