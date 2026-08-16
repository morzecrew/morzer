package suite

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/release"
)

// The three-tier example is the bundle the documentation site draws its
// second worked example from, and the one the acceptance run drives. These
// tests are what stop it rotting between acceptance runs: they need no Docker,
// so `just test` catches a broken example in milliseconds.

func webBundle(t *testing.T) domain.Release {
	t.Helper()

	wd, err := os.Getwd()
	require.NoError(t, err)

	rel, err := release.Load(filepath.Join(wd, "..", "..", "testdata", "bundle-web"))
	require.NoError(t, err, "the three-tier example must be a valid bundle")
	return rel
}

func TestTheThreeTierExampleIsValid(t *testing.T) {
	rel := webBundle(t)

	assert.Equal(t, "web", rel.Name())
	assert.Equal(t, "1.0.0", rel.Version().String())

	for _, profile := range []string{"embedded", "external-db"} {
		files, err := rel.RuntimeFilePaths(domain.LegacyRuntimeName, profile)
		require.NoError(t, err, "profile %q must resolve", profile)
		assert.Len(t, files, 2, "the base file plus the profile's own")
	}
}

// TestEachTierPublishesItsOwnPort is the question the example exists to answer.
func TestEachTierPublishesItsOwnPort(t *testing.T) {
	rel := webBundle(t)

	params, err := domain.ResolveParameters(rel.Manifest.Parameters, map[string]string{
		"http_port": "8443",
		"api_port":  "9443",
	})
	require.NoError(t, err)

	ports, err := rel.Manifest.ResolvePorts(params)
	require.NoError(t, err)
	assert.Equal(t, []int{8443, 9443}, ports,
		"preflight must check both published ports, each from its own parameter")

	checks, err := rel.Manifest.ResolveHealthChecks(params)
	require.NoError(t, err)

	byName := map[string]string{}
	for _, c := range checks {
		byName[c.Name] = c.URL
	}
	assert.Contains(t, byName["web"], ":8443/", "the web check follows http_port")
	assert.Contains(t, byName["api"], ":9443/", "the api check follows api_port")
}

// TestAParameterIsScopedToTheTierThatUsesIt pins the declarations `config set`
// reads to decide whether to re-create anything and what to claim afterwards.
//
// Note what this does *not* prove. Compose re-creates only services whose
// effective configuration changed, so unrelated tiers stay up whatever these
// lists say -- verified by removing the scoping entirely and watching the
// acceptance run still pass. What the lists decide is whether a re-create step
// runs at all, and what the summary is allowed to tell the operator.
//
// It also has to match the topology, which nothing verifies: a parameter both
// tiers read but that names only one leaves the other running a stale value.
// That is why log_level's list is asserted here rather than assumed.
func TestAParameterIsScopedToTheTierThatUsesIt(t *testing.T) {
	params := webBundle(t).Manifest.Parameters

	assert.Equal(t, []string{"frontend"}, params["http_port"].Services)
	assert.Equal(t, []string{"backend"}, params["api_port"].Services)
	assert.Equal(t, []string{"frontend", "backend"}, params["log_level"].Services)

	// Declaring nothing is the vendor saying "this needs a full apply". It is
	// a deliberate state, not an omission, so it is asserted.
	assert.Empty(t, params["max_upload"].Services,
		"max_upload is read at start-up, so a re-create would not apply it")
}

// TestOnlyTheBackendHoldsTheDatabaseCredential pins the security shape the
// example teaches. A frontend that never receives the password cannot leak it,
// and the Compose file is where that is decided.
func TestOnlyTheBackendHoldsTheDatabaseCredential(t *testing.T) {
	rel := webBundle(t)

	path, err := rel.Path("compose/compose.yaml")
	require.NoError(t, err)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var file struct {
		Services map[string]struct {
			Secrets []string `yaml:"secrets"`
		} `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &file))

	assert.Empty(t, file.Services["frontend"].Secrets,
		"the frontend needs no credential, so it must not be given one")
	assert.ElementsMatch(t, []string{"db_password", "session_key"},
		file.Services["backend"].Secrets,
		"the backend is the tier that authenticates")

	// And the secret schema agrees, so a rotation restarts the tiers that
	// actually hold the value.
	schema, err := release.LoadSecretSchema(rel)
	require.NoError(t, err)

	for _, decl := range schema.Secrets {
		assert.NotContains(t, decl.Services, "frontend",
			"secret %q lists the frontend, which holds nothing", decl.Name)
	}
}

// TestTheExampleRendersItsConfiguration catches a template that references a
// parameter the manifest does not declare -- a failure that would otherwise
// wait for an operator's first apply.
func TestTheExampleRendersItsConfiguration(t *testing.T) {
	rel := webBundle(t)

	path, err := rel.Path("templates/application.yaml.tmpl")
	require.NoError(t, err)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	declared := rel.Manifest.Parameters
	for _, name := range []string{"http_port", "api_port", "log_level", "max_upload"} {
		assert.Contains(t, string(raw), ".Parameters."+name,
			"the template should show %q, which is why it is declared", name)
		assert.Contains(t, declared, name)
	}
}
