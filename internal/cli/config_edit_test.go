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
)

// `config edit` on a text parameter, RFC 0054 §5.3. What these pin is what
// happens around the editor: which value it is handed, what a save means, and
// that the directory is gone afterwards however the editor exits.

// textBundle is the example release with two text parameters: `rules`, with no
// default, and `motd`, whose default ends in the newline a `|` block leaves.
func textBundle(t *testing.T) string {
	t.Helper()
	src, err := filepath.Abs(filepath.Join("..", "..", "testdata", "bundle"))
	require.NoError(t, err)
	dst := filepath.Join(t.TempDir(), "bundle")
	require.NoError(t, filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	}))

	manifest := filepath.Join(dst, "manifest.yaml")
	raw, err := os.ReadFile(manifest)
	require.NoError(t, err)
	patched := strings.Replace(string(raw), "\nimages:\n", `  rules:
    type: text
    description: Routing rules, one per line
    services: [app]
  motd:
    type: text
    default: |
      welcome
      to demo
    services: [app]

images:
`, 1)
	patched = strings.Replace(patched, `min_manager_version: "0.0.0"`,
		`min_manager_version: "`+domain.TextParametersMinManagerVersion+`"`, 1)
	require.NotEqual(t, string(raw), patched)
	require.NoError(t, os.WriteFile(manifest, []byte(patched), 0o644))
	return dst
}

// editSession runs one edit with a scripted editor, which records the path it
// was handed, and returns what it set and that path. recorded seeds the
// installation's values.
func editSession(t *testing.T, name, script string, recorded ...string) (set map[string]string, path string, err error) {
	t.Helper()
	app, _ := restartAppAt(t, textBundle(t))
	if len(recorded) > 0 {
		ctx := context.Background()
		inst, loadErr := app.Deps.State.LoadInstallation(ctx)
		require.NoError(t, loadErr)
		inst.Parameters, loadErr = domain.ParseAssignments(recorded)
		require.NoError(t, loadErr)
		require.NoError(t, app.Deps.State.SaveInstallation(ctx, inst))
	}
	side := filepath.Join(t.TempDir(), "path")
	set = map[string]string{}
	err = app.editParameterWith(context.Background(), name,
		scriptedEditor(`printf %s "$0" > `+side+`; `+script),
		func(_ context.Context, name, value string) error {
			set[name] = value
			return nil
		})
	if recorded, readErr := os.ReadFile(side); readErr == nil {
		path = string(recorded)
	}
	return set, path, err
}

func TestConfigEditSetsWhatWasSaved(t *testing.T) {
	set, path, err := editSession(t, "rules", `printf 'DOMAIN-SUFFIX,example.ru\r\n  indented\n\n' > "$0"`)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"rules": "DOMAIN-SUFFIX,example.ru\n  indented"}, set,
		"the value is normalised as `text` parses it, and indentation kept")
	require.NotEmpty(t, path)
	assert.NoDirExists(t, filepath.Dir(path), "the edit directory was left behind")
}

// The editor is handed the effective value, parsed, plus one final newline;
// saving it as it was, in any editor's idea of a final newline, changes
// nothing.
func TestConfigEditOpensTheDefaultAndAnUnchangedSaveIsANoOp(t *testing.T) {
	for name, script := range map[string]string{
		"left as it was":          `printf 'welcome\nto demo\n' | cmp -s - "$0"`,
		"with the newline gone":   `printf 'welcome\nto demo' > "$0"`,
		"with CRLF and blank end": `printf 'welcome\r\nto demo\r\n\n' > "$0"`,
	} {
		t.Run(name, func(t *testing.T) {
			set, path, err := editSession(t, "motd", script)
			require.NoError(t, err)
			assert.Empty(t, set, "an unchanged value was set again")
			assert.NoDirExists(t, filepath.Dir(path))
		})
	}
}

// An emptied file changes nothing (RFC 0003 decision 20), and the hint names
// only what can record an empty value: a parameter with no default refuses one
// as missing.
func TestConfigEditOfAnEmptiedFileChangesNothing(t *testing.T) {
	set, path, err := editSession(t, "motd", `: > "$0"`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "motd was left empty, so nothing was changed")
	assert.Contains(t, domain.AsError(err).Hint, "`morzer config unset motd`")
	assert.Contains(t, domain.AsError(err).Hint, "`morzer config set motd=`")
	assert.Empty(t, set)
	assert.NoDirExists(t, filepath.Dir(path))

	set, _, err = editSession(t, "rules", `: > "$0"`, "rules=DOMAIN-SUFFIX,example.ru")
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Hint, "declares no default for rules, so it cannot be left empty")
	assert.NotContains(t, domain.AsError(err).Hint, "config set rules=")
	assert.Empty(t, set)
}

func TestConfigEditChangesNothingWhenTheEditFails(t *testing.T) {
	for name, tc := range map[string]struct{ script, refusal string }{
		"the editor exits non-zero":   {`printf changed > "$0"; exit 1`, "exited with an error; nothing was changed"},
		"the editor removes the file": {`rm "$0"`, "the edited file is gone; nothing was changed"},
		"a NUL byte is saved":         {`printf 'a\000b' > "$0"`, `parameter "rules" was not changed`},
	} {
		t.Run(name, func(t *testing.T) {
			set, path, err := editSession(t, "rules", tc.script)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.refusal)
			assert.Empty(t, set)
			assert.NoDirExists(t, filepath.Dir(path))
		})
	}
}

func TestConfigEditOpensOnlyATextParameter(t *testing.T) {
	set, path, err := editSession(t, "http_port", `printf 9000 > "$0"`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "`config edit` opens text parameters, and http_port is a port parameter")
	assert.Contains(t, domain.AsError(err).Hint, "`morzer config set http_port=<value>`")
	assert.Empty(t, set)
	assert.Empty(t, path, "the editor ran for a parameter it does not open")

	_, _, err = editSession(t, "ruels", `true`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `parameter "ruels"`)
}

func TestConfigEditNeedsATerminal(t *testing.T) {
	app, _ := restartAppAt(t, textBundle(t))
	app.Stream.In = strings.NewReader("")
	err := app.editParameter(context.Background(), "rules")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "`config edit` needs a terminal")
	assert.Contains(t, domain.AsError(err).Hint, "--set-file rules=<path>")
}
