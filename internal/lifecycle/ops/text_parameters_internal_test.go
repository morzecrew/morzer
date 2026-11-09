package ops

import (
	"context"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/state"
)

// Multi-line text parameters, RFC 0054.

// The values that read worst as YAML: leading spaces, a tab and blank lines.
const awkwardText = "  key: 1\n\tindented\n\n\nlast"

// A text value is a string in the same map as every other parameter, so the
// JSON state and the installation.yaml report carry it unchanged, and doctor
// sees no drift between the two (RFC 0054 §3, decision 1).
func TestATextValueSurvivesTheStateAndTheReport(t *testing.T) {
	ctx := context.Background()
	paths := domain.PathsUnder(t.TempDir(), "demo")
	recorded := domain.Installation{
		SchemaVersion: domain.InstallationSchemaVersion,
		ID:            "inst_01",
		Product:       "demo",
		Parameters:    map[string]string{"rules": awkwardText},
	}

	st := state.New(paths)
	require.NoError(t, st.SaveInstallation(ctx, recorded))
	loaded, err := st.LoadInstallation(ctx)
	require.NoError(t, err)
	assert.Equal(t, awkwardText, loaded.Parameters["rules"])

	report, err := yaml.Marshal(recorded)
	require.NoError(t, err)
	var onDisk domain.Installation
	require.NoError(t, yaml.Unmarshal(report, &onDisk))
	assert.Equal(t, awkwardText, onDisk.Parameters["rules"])
	assert.Empty(t, installationDifferences(onDisk, recorded))
}

// A text default is described by its line count and a digest of the value
// delivered, so a change on any line is listed and a default that differs only
// by a final newline is not (decision 14).
func TestADiffDescribesATextDefaultByItsLinesAndDigest(t *testing.T) {
	text := func(def string) domain.ParameterSpec {
		return domain.ParameterSpec{Type: domain.ParamText, Default: def}
	}

	assert.Equal(t, "text, no default", describeParameter(text("")))
	described := describeParameter(text("a\nb"))
	assert.Regexp(t, `^text, default of 2 lines \(sha256 [0-9a-f]{12}\)$`, described)
	assert.Equal(t, described, describeParameter(text("a\nb\n")),
		"a final newline the deployment never sees reads as a change")
	assert.NotEqual(t, described, describeParameter(text("a\nc")), "a change on the second line is not listed")
	assert.Equal(t, "port, default 8080",
		describeParameter(domain.ParameterSpec{Type: domain.ParamPort, Default: "8080"}))
}

// The summary names a text parameter with its line counts before and after,
// from the default when nothing was recorded (§5.5).
func TestTheSummaryCountsATextParametersLines(t *testing.T) {
	rel := domain.Release{Manifest: domain.Manifest{Parameters: map[string]domain.ParameterSpec{
		"rules":     {Type: domain.ParamText},
		"motd":      {Type: domain.ParamText, Default: "welcome\nto demo\n"},
		"http_port": {Type: domain.ParamPort, Default: "8080"},
	}}}

	got := summaryNames(rel,
		map[string]string{"rules": "a"},
		map[string]string{"rules": "a\nb\nc", "motd": "hello", "http_port": "9000"},
		[]string{"http_port", "motd", "rules"})
	assert.Equal(t, []string{"http_port", "motd (2 → 1 lines)", "rules (1 → 3 lines)"}, got)
}
