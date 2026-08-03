package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/state"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/test/fakes"
)

// An editor session is the one place in this program where a decrypted secret
// is written to a filesystem, so what these assert is mostly about what is
// *not* left behind.
//
// The editor is a shell script rather than a real editor. `sh -c '<script>'`
// receives the file path as $0, which is the standard way to script $EDITOR and
// what the RFC's test plan called for.

// editApp builds the smallest App an edit session needs.
func editApp(t *testing.T, seed map[string]string) (*App, *fakes.SecretStore, string) {
	t.Helper()

	root := t.TempDir()
	paths := domain.PathsUnder(root, "demo")

	store := fakes.NewSecretStore()
	store.Seed(seed)

	// A real state store over a temp directory. With no release installed
	// it reports exactly that, which is the branch these tests want: nothing
	// is running, so nothing is restarted.
	app := &App{
		Stream: ui.Streams{Out: os.Stderr, Err: os.Stderr},
		Deps: &ops.Deps{
			Paths:   paths,
			State:   state.New(paths),
			Secrets: store,
		},
	}
	return app, store, root
}

// scriptedEditor returns an editor command that runs a shell script over the
// file it is handed.
func scriptedEditor(script string) []string {
	return []string{"sh", "-c", script}
}

func loaded(t *testing.T, store *fakes.SecretStore) map[string]string {
	t.Helper()
	set, err := store.Load(context.Background())
	require.NoError(t, err)

	out := map[string]string{}
	for _, name := range set.Names() {
		value, _ := set.Get(name)
		out[name] = value.Reveal()
	}
	return out
}

func TestSecretEditWritesOnlyWhatChanged(t *testing.T) {
	app, store, _ := editApp(t, map[string]string{
		"db_password": "old-password",
		"session_key": "untouched-key",
	})

	// Change one value and leave the other exactly as it was.
	err := app.editSecretsWith(context.Background(), nil,
		scriptedEditor(`sed -i 's/old-password/new-password/' "$0"`))
	require.NoError(t, err)

	after := loaded(t, store)
	assert.Equal(t, "new-password", after["db_password"])
	assert.Equal(t, "untouched-key", after["session_key"],
		"a secret nobody edited must not be rewritten")
}

func TestSecretEditAddsAndRemoves(t *testing.T) {
	app, store, _ := editApp(t, map[string]string{
		"keep":   "kept-value",
		"delete": "doomed-value",
	})

	err := app.editSecretsWith(context.Background(), nil, scriptedEditor(
		`grep -v '^delete:' "$0" > "$0.tmp" && printf 'added: brand-new\n' >> "$0.tmp" && mv "$0.tmp" "$0"`))
	require.NoError(t, err)

	after := loaded(t, store)
	assert.Equal(t, map[string]string{"keep": "kept-value", "added": "brand-new"}, after,
		"deleting a line removes the secret; adding one adds it")
}

func TestSecretEditLeavesNoPlaintextBehind(t *testing.T) {
	app, _, root := editApp(t, map[string]string{"db_password": "the-value-to-find"})

	// An editor that leaves a backup file beside the one it was given, the
	// way vim and emacs do. The session directory has to take those with it.
	err := app.editSecretsWith(context.Background(), nil,
		scriptedEditor(`cp "$0" "$0~"; sed -i 's/the-value-to-find/replaced/' "$0"`))
	require.NoError(t, err)

	renderDir := domain.PathsUnder(root, "demo").SecretsRenderDir()
	assertNoPlaintext(t, renderDir, "the-value-to-find")
}

func TestSecretEditCleansUpAfterAFailedEditor(t *testing.T) {
	app, store, root := editApp(t, map[string]string{"db_password": "the-value-to-find"})

	// `:cq` in vim: the operator saying "forget it". The value is written to
	// the file before the editor runs, so the cleanup has to happen on this
	// path too -- and it is the path least likely to be exercised by hand.
	err := app.editSecretsWith(context.Background(), nil,
		scriptedEditor(`sed -i 's/the-value-to-find/changed/' "$0"; exit 1`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no secrets were changed")

	assert.Equal(t, "the-value-to-find", loaded(t, store)["db_password"],
		"an aborted edit must change nothing")

	renderDir := domain.PathsUnder(root, "demo").SecretsRenderDir()
	assertNoPlaintext(t, renderDir, "the-value-to-find")
	assertNoPlaintext(t, renderDir, "changed")
}

// assertNoPlaintext walks a directory asserting no file contains the value,
// and that no edit session was left behind at all.
func assertNoPlaintext(t *testing.T, dir, value string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return
	}
	require.NoError(t, err)

	for _, e := range entries {
		assert.False(t, strings.HasPrefix(e.Name(), ".edit-"),
			"the edit session directory %q survived", e.Name())

		data, readErr := os.ReadFile(filepath.Join(dir, e.Name()))
		if readErr != nil {
			continue
		}
		assert.NotContains(t, string(data), value,
			"%s still holds a plaintext secret", e.Name())
	}
}

func TestSecretEditRefusesAFileThatDoesNotParse(t *testing.T) {
	app, store, _ := editApp(t, map[string]string{"db_password": "original"})

	err := app.editSecretsWith(context.Background(), nil,
		scriptedEditor(`printf 'this: is: not: yaml\n  - neither[ is\n' > "$0"`))
	require.Error(t, err)

	// The operator has just lost an edit. What they need to know next is
	// whether they also broke something, so the message says.
	assert.Contains(t, err.Error(), "nothing was changed")
	assert.Equal(t, "original", loaded(t, store)["db_password"])
}

func TestSecretEditRefusesAnEmptyValue(t *testing.T) {
	app, store, _ := editApp(t, map[string]string{"db_password": "original"})

	err := app.editSecretsWith(context.Background(), nil,
		scriptedEditor(`printf 'db_password: ""\n' > "$0"`))
	require.Error(t, err)

	// An empty value is almost always a half-finished edit rather than an
	// intention, and the store refuses one anyway. Saying which line, and
	// that deleting removes, is more use than passing it through.
	assert.Contains(t, err.Error(), "left empty")
	assert.Contains(t, domain.AsError(err).Hint, "delete the line")
	assert.Equal(t, "original", loaded(t, store)["db_password"])
}

func TestSecretEditWithNoChangesTouchesNothing(t *testing.T) {
	app, store, _ := editApp(t, map[string]string{"db_password": "unchanged"})

	require.NoError(t, app.editSecretsWith(context.Background(), nil,
		scriptedEditor(`true`)))

	assert.Equal(t, "unchanged", loaded(t, store)["db_password"])
}

func TestSecretEditNamesOnlyWhatWasAskedFor(t *testing.T) {
	app, _, root := editApp(t, map[string]string{
		"db_password": "in-scope",
		"session_key": "out-of-scope",
	})

	// The file the editor opens is what a scoped session shows. A secret the
	// operator did not name should not be in front of them at all.
	captured := filepath.Join(root, "captured.yaml")
	require.NoError(t, app.editSecretsWith(context.Background(),
		[]string{"db_password"}, scriptedEditor(`cp "$0" `+captured)))

	data, err := os.ReadFile(captured)
	require.NoError(t, err)
	assert.Contains(t, string(data), "db_password")
	assert.NotContains(t, string(data), "out-of-scope")
}

func TestSecretEditRefusesAnUnknownName(t *testing.T) {
	app, _, _ := editApp(t, map[string]string{"db_password": "value"})

	err := app.editSecretsWith(context.Background(), []string{"nope"}, scriptedEditor(`true`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
}

func TestEditorCommandPrefersVisual(t *testing.T) {
	t.Setenv("VISUAL", "code --wait")
	t.Setenv("EDITOR", "vi")

	got, err := editorCommand()
	require.NoError(t, err)

	// The order git, crontab and sudoedit use. An operator with both set
	// means the graphical one for an interactive session.
	assert.Equal(t, []string{"code", "--wait"}, got,
		"VISUAL wins, and a command with arguments is split")

	t.Setenv("VISUAL", "")
	got, err = editorCommand()
	require.NoError(t, err)
	assert.Equal(t, []string{"vi"}, got)
}
