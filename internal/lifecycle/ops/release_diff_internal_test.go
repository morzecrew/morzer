package ops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/release"
)

// TestADiffNamesEveryChangeAndNothingElse is the comparison's whole contract:
// one release changed in every way it reports, beside changes it deliberately
// leaves out -- a parameter's description, and the checksum list, which differs
// whenever anything does.
func TestADiffNamesEveryChangeAndNothingElse(t *testing.T) {
	from := diffBundle(t, "from", nil)
	to := diffBundle(t, "to", func(dir string) {
		rewrite(t, filepath.Join(dir, "manifest.yaml"),
			"version: 1.2.0", "version: 1.3.0",
			"0000000000000000000000000000000000000000000000000000000000000001",
			"0000000000000000000000000000000000000000000000000000000000000003",
			"  db: registry.example/demo/db@sha256:0000000000000000000000000000000000000000000000000000000000000002\n",
			"  worker: registry.example/demo/worker@sha256:0000000000000000000000000000000000000000000000000000000000000004\n",
			"default: 18080", "default: 8080",
			"description: Application log verbosity", "description: How much the application logs",
			"parameters:\n", "tasks:\n  seed:\n    service: app\n    command: [seed]\nparameters:\n  admin_email:\n    type: string\n    services: [app]\n",
			"      external-db: [compose/compose.external-db.yaml]\n", "",
			"database_schema_max: 12", "database_schema_max: 13\n  database_schema_produces: 13",
			`min_manager_version: "0.0.0"`, `min_manager_version: "0.8.0"`,
			`upgrade_from: ">=1.0.0 <2.0.0"`, `upgrade_from: ">=1.1.0 <2.0.0"`,
			"rollback_safe: true", "rollback_safe: false",
		)
		rewrite(t, filepath.Join(dir, "secrets.schema.yaml"),
			"  - name: smtp_password", "  - name: api_token")
		require.NoError(t, os.WriteFile(filepath.Join(dir, "VERSION"), []byte("1.3.0\n"), 0o644))
		require.NoError(t, os.Remove(filepath.Join(dir, "compose", "compose.external-db.yaml")))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks", "notes.txt"), []byte("new\n"), 0o644))
		f, err := os.OpenFile(filepath.Join(dir, "templates", "application.yaml.tmpl"), os.O_APPEND|os.O_WRONLY, 0)
		require.NoError(t, err)
		_, err = f.WriteString("# changed\n")
		require.NoError(t, err)
		require.NoError(t, f.Close())
	})

	diff, err := DiffReleases(from, to)
	require.NoError(t, err)

	assert.Equal(t, "demo 1.2.0", diff.From)
	assert.Equal(t, "demo 1.3.0", diff.To)
	assert.Equal(t, []Change{
		{Name: "app", Kind: ChangeChanged,
			From: "registry.example/demo/app@sha256:0000000000000000000000000000000000000000000000000000000000000001",
			To:   "registry.example/demo/app@sha256:0000000000000000000000000000000000000000000000000000000000000003"},
		{Name: "db", Kind: ChangeRemoved,
			From: "registry.example/demo/db@sha256:0000000000000000000000000000000000000000000000000000000000000002"},
		{Name: "worker", Kind: ChangeAdded,
			To: "registry.example/demo/worker@sha256:0000000000000000000000000000000000000000000000000000000000000004"},
	}, diff.Images)
	assert.Equal(t, []Change{
		{Name: "admin_email", Kind: ChangeAdded, To: "string, no default"},
		{Name: "http_port", Kind: ChangeChanged, From: "port, default 18080", To: "port, default 8080"},
	}, diff.Parameters, "a changed description is not a change")
	assert.Equal(t, []Change{
		{Name: "api_token", Kind: ChangeAdded},
		{Name: "smtp_password", Kind: ChangeRemoved},
	}, diff.Secrets)
	assert.Equal(t, []Change{
		{Name: "database_schema_max", Kind: ChangeChanged, From: "12", To: "13"},
		{Name: "database_schema_produces", Kind: ChangeAdded, To: "13"},
		{Name: "min_manager_version", Kind: ChangeChanged, From: "0.0.0", To: "0.8.0"},
		{Name: "rollback_safe", Kind: ChangeChanged, From: "true", To: "false"},
		{Name: "upgrade_from", Kind: ChangeChanged, From: ">=1.0.0 <2.0.0", To: ">=1.1.0 <2.0.0"},
	}, diff.Compatibility)
	assert.Equal(t, []Change{{Name: "seed", Kind: ChangeAdded}}, diff.Tasks)
	assert.Equal(t, []Change{{Name: "external-db", Kind: ChangeRemoved}}, diff.Profiles)

	var files []string
	for _, c := range diff.Files {
		files = append(files, c.Name+" "+string(c.Kind))
		if c.Kind == ChangeChanged {
			assert.NotEqual(t, c.From, c.To, "%s is changed with one checksum", c.Name)
		}
	}
	assert.Equal(t, []string{
		"VERSION changed",
		"compose/compose.external-db.yaml removed",
		"hooks/notes.txt added",
		"manifest.yaml changed",
		"secrets.schema.yaml changed",
		"templates/application.yaml.tmpl changed",
	}, files, "the checksum list is not compared")
	assert.False(t, diff.Same())

	same, err := DiffReleases(from, from)
	require.NoError(t, err)
	assert.True(t, same.Same(), "a release differs from itself: %+v", same)
}

// diffBundle copies the example bundle, changes it, writes its checksum list,
// and loads it.
func diffBundle(t *testing.T, name string, change func(dir string)) domain.Release {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.CopyFS(dir, os.DirFS(exampleBundle(t))))
	if change != nil {
		change(dir)
	}
	require.NoError(t, release.WriteSums(dir))
	rel, err := release.Load(dir)
	require.NoError(t, err)
	return rel
}

// rewrite replaces each old string with its new one, failing when one is not
// there, so a fixture that stops matching the example bundle says so.
func rewrite(t *testing.T, path string, pairs ...string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(raw)
	for i := 0; i < len(pairs); i += 2 {
		require.Containsf(t, text, pairs[i], "%s no longer holds %q", path, pairs[i])
		text = strings.Replace(text, pairs[i], pairs[i+1], 1)
	}
	require.NoError(t, os.WriteFile(path, []byte(text), 0o644))
}
