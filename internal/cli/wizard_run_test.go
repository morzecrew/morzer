package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/test/fakes"
)

// The wizard is a first run: the moment somebody decides whether this tool can
// be trusted with their data. It is also the one place in the program that
// waits for a person, which is why nothing could drive it until now.
//
// It is driven here through huh's accessible renderer -- the line-oriented one
// it ships for screen readers -- which is what a stream that cannot be put in
// raw mode gets. No pseudo-terminal, and no test-only branch in the wizard:
// the same code path a screen-reader user takes.

// scriptedInput answers one line per Read.
//
// A single strings.Reader does not work, and the reason is worth writing down:
// huh's accessible renderer builds a fresh bufio.Scanner *per field*, so the
// first field's scanner buffers the whole reader and every later field sees
// EOF. Handing back one line at a time is also what a terminal does, so this
// is the faithful fixture rather than a workaround.
type scriptedInput struct{ lines []string }

func (s *scriptedInput) Read(p []byte) (int, error) {
	if len(s.lines) == 0 {
		return 0, io.EOF
	}
	line := s.lines[0] + "\n"
	s.lines = s.lines[1:]
	return copy(p, line), nil
}

// wizardApp builds an App whose forms read from a script.
//
// A select field in accessible mode asks for a number; an input field takes
// the line as typed.
func wizardApp(t *testing.T, answers ...string) (*App, *strings.Builder) {
	t.Helper()

	var shown strings.Builder
	return &App{
		Stream: ui.Streams{
			Out: &shown,
			Err: &shown,
			In:  &scriptedInput{lines: answers},
		},
		Deps: &ops.Deps{Secrets: fakes.NewSecretStore()},
	}, &shown
}

func TestFormsAreLineOrientedWhenTheInputIsNotATerminal(t *testing.T) {
	app, _ := wizardApp(t)

	if !app.accessibleForms() {
		t.Fatal("a stream that cannot be put in raw mode was given a terminal UI, " +
			"which has nothing to draw on and nothing to read from")
	}

	// And an operator who asked for it gets it regardless.
	t.Setenv("ACCESSIBLE", "1")
	if !app.accessibleForms() {
		t.Error("ACCESSIBLE was exported and ignored")
	}
}

// TestTheWizardFillsOnlyWhatTheFlagsLeftEmpty is the property that keeps `init`
// scriptable: a command line that means what it says runs untouched.
func TestTheWizardFillsOnlyWhatTheFlagsLeftEmpty(t *testing.T) {
	app, _ := wizardApp(t)

	// Nothing missing, so nothing is asked -- with an empty script, any
	// question at all would fail the read.
	given := ops.InitOptions{
		Product:           "demo",
		Domains:           []string{"demo.example"},
		RecoveryRecipient: "age1already-supplied",
	}

	got, err := runInitWizard(context.Background(), app, given)
	require.NoError(t, err)
	assert.Equal(t, given.Product, got.Product)
	assert.Equal(t, given.RecoveryRecipient, got.RecoveryRecipient)
	assert.Equal(t, given.Domains, got.Domains)
}

// TestTheWizardCollectsWhatIsMissing walks the path a first run actually takes.
func TestTheWizardCollectsWhatIsMissing(t *testing.T) {
	// Product, then domains, then the recovery choice: "3" is "proceed
	// without one", which is the only branch that writes nothing to disk.
	app, shown := wizardApp(t, "demo", "demo.example, www.demo.example", "3")

	got, err := runInitWizard(context.Background(), app, ops.InitOptions{})
	require.NoError(t, err)

	assert.Equal(t, "demo", got.Product)
	assert.Equal(t, []string{"demo.example", "www.demo.example"}, got.Domains,
		"the domain list was not split, so `init` would create one installation "+
			"whose canonical domain is a comma-separated string")
	assert.True(t, got.NoRecoveryKey)
	assert.Empty(t, got.RecoveryRecipient)

	assert.Contains(t, shown.String(), "Product name")

	// The consequence of declining has to survive into accessible mode,
	// where huh prints titles and drops descriptions. It is in the title
	// for exactly that reason.
	assert.Contains(t, shown.String(), "losing this machine loses its secrets",
		"the recovery question does not say what declining costs")
}

func TestTheWizardRefusesAProductNameThatIsAPath(t *testing.T) {
	// The first answer is rejected by the field's own validator, so the
	// second is the one that lands. Every managed path derives from this
	// name, and `../etc` would put the installation somewhere else entirely.
	app, shown := wizardApp(t, "../etc", "demo", "", "3")

	got, err := runInitWizard(context.Background(), app, ops.InitOptions{})
	require.NoError(t, err)
	assert.Equal(t, "demo", got.Product)
	assert.NotContains(t, shown.String(), "\n../etc\n")
}

// TestTheWizardTakesAnExistingRecoveryKey is the second of the three answers.
func TestTheWizardTakesAnExistingRecoveryKey(t *testing.T) {
	store := fakes.NewSecretStore()
	valid := "age1lggyhqrw2nlhcxprm67z43rta597azn8gknawjehu9d9dl0jq3yqqvfafg"

	// Choice 2, then the key. A bad key is refused by the field before the
	// wizard ever returns it, so the first answer here is the typo.
	app, _ := wizardApp(t, "demo", "", "2", "not-an-age-key", valid)
	app.Deps.Secrets = store

	got, err := runInitWizard(context.Background(), app, ops.InitOptions{})
	require.NoError(t, err)
	assert.Equal(t, valid, got.RecoveryRecipient,
		"the pasted recovery key did not reach the options")
	assert.False(t, got.NoRecoveryKey)
}

// TestTheWizardGeneratesARecoveryKey is the recommended answer, and the only
// one that writes a private key to disk.
func TestTheWizardGeneratesARecoveryKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo-recovery.key")

	// Choice 1, then where to write it.
	app, shown := wizardApp(t, "demo", "", "1", path)

	got, err := runInitWizard(context.Background(), app, ops.InitOptions{})
	require.NoError(t, err)

	require.NotEmpty(t, got.RecoveryRecipient)
	assert.True(t, strings.HasPrefix(got.RecoveryRecipient, "age1"),
		"the generated recipient is not an age public key: %q", got.RecoveryRecipient)
	assert.False(t, got.NoRecoveryKey)

	info, err := os.Stat(path)
	require.NoError(t, err, "the recovery key was not written")
	assert.Equal(t, os.FileMode(0o400), info.Mode().Perm(),
		"a private key readable by anyone but its owner is not a recovery key")

	// The warning is the whole point of generating it here rather than
	// telling the operator to run age-keygen.
	out := shown.String()
	assert.Contains(t, out, "MOVE THE PRIVATE HALF OFF THIS MACHINE")
	assert.Contains(t, out, got.RecoveryRecipient,
		"the public half is not shown, so the operator cannot record it")
	assert.Contains(t, out, path)
}

// TestGeneratingIntoSomewhereUnwritableIsReported. An operator who typed a
// path they cannot write must be told, not left with an installation that
// believes it has a recovery key.
func TestGeneratingIntoSomewhereUnwritableIsReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}

	parent := t.TempDir()
	require.NoError(t, os.Chmod(parent, 0o500))
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	app, _ := wizardApp(t, filepath.Join(parent, "keys", "recovery.key"))

	_, err := generateRecoveryKey(context.Background(), app,
		ops.InitOptions{Product: "demo"})
	require.Error(t, err, "a recovery key that could not be written was reported written")
}

func TestResolveRecoveryChoiceTakesEachAnswer(t *testing.T) {
	t.Run("declining", func(t *testing.T) {
		app, _ := wizardApp(t)

		got, err := resolveRecoveryChoice(context.Background(), app,
			ops.InitOptions{Product: "demo"}, recoveryChoiceNone)
		require.NoError(t, err)
		assert.True(t, got.NoRecoveryKey)
		assert.Empty(t, got.RecoveryRecipient)
	})

	t.Run("an unknown answer generates, because that is the safe default", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "recovery.key")
		app, _ := wizardApp(t, path)

		got, err := resolveRecoveryChoice(context.Background(), app,
			ops.InitOptions{Product: "demo"}, "something-nobody-defined")
		require.NoError(t, err)
		assert.NotEmpty(t, got.RecoveryRecipient,
			"an answer nobody recognised must fall to generating a key, not to "+
				"proceeding without one")
	})
}

// TestEndOfInputDoesNotCancelInAccessibleMode records a limitation rather than
// a behaviour anyone chose.
//
// huh's accessible renderer ignores the context entirely and swallows each
// field's error -- its own source says "no way to bubble up errors or signal
// cancellation" -- so ctrl-D during the wizard completes the form with
// defaults instead of aborting it. The `Interrupted` branches in this file are
// therefore reachable only at a real terminal, and are the one part of P6 no
// test drives.
//
// What makes that survivable is what the defaults are, which is what this
// asserts: an operator who walks away gets a generated recovery key rather
// than an installation quietly created without one.
func TestEndOfInputDoesNotCancelInAccessibleMode(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	// Only the product name is answered; everything after it hits EOF.
	app, _ := wizardApp(t, "demo")

	got, err := runInitWizard(context.Background(), app, ops.InitOptions{})
	require.NoError(t, err, "if this now fails, huh has learned to signal "+
		"cancellation -- delete this test and assert CodeInterrupted instead")

	assert.Equal(t, "demo", got.Product)
	assert.False(t, got.NoRecoveryKey,
		"walking away from the recovery question waived the recovery key, which "+
			"is the one default that must never be reached by silence")
	assert.NotEmpty(t, got.RecoveryRecipient,
		"the safe default is to generate a key, and it was not generated")

	// And it landed under the temporary home rather than wherever the
	// process happened to be. An earlier draft of this file wrote a real
	// age private key into the repository, which is a thing a test must
	// never be able to do -- so the location is asserted, not assumed.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var found bool
	for _, e := range entries {
		if strings.Contains(e.Name(), "recovery") {
			found = true
		}
	}
	assert.True(t, found, "the generated key is not under the test's own HOME: %v", entries)
}

// TestTheWizardOffersTheProfilesTheBundleDeclares rather than asking an
// operator to remember them.
func TestTheWizardOffersTheProfilesTheBundleDeclares(t *testing.T) {
	bundle := testdataBundle(t)

	// Product supplied, so the first question is the profile: "2" selects
	// the second of the manifest's profiles in sorted order.
	app, shown := wizardApp(t, "1", "", "3")

	got, err := runInitWizard(context.Background(), app, ops.InitOptions{
		Product: "demo", ReleasePath: bundle,
	})
	require.NoError(t, err)

	assert.NotEmpty(t, got.Profile, "no profile was collected")
	assert.Contains(t, shown.String(), "Deployment profile")
	assert.Contains(t, shown.String(), got.Profile)
}

func TestDefaultRecoveryKeyPath(t *testing.T) {
	t.Setenv("HOME", "/home/operator")
	assert.Equal(t, "/home/operator/demo-recovery.key", defaultRecoveryKeyPath("demo"))

	// No home to write into. Falling back to the working directory is
	// better than an empty path, which would write to "/demo-recovery.key".
	t.Setenv("HOME", "")
	got := defaultRecoveryKeyPath("demo")
	assert.Equal(t, "./demo-recovery.key", got,
		"with no home directory the default is not relative to anything")
}

// testdataBundle locates the example bundle from internal/cli.
func testdataBundle(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return filepath.Join(wd, "..", "..", "testdata", "bundle")
}
