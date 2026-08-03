package suite

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

func TestConfigListReportsDefaultsAndOverrides(t *testing.T) {
	h := newHarness(t)
	inst := h.install()
	inst.Parameters = map[string]string{"log_level": "debug"}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	report, err := ops.ConfigList(context.Background(), h.Deps)
	require.NoError(t, err)

	byName := map[string]ops.ConfigEntry{}
	for _, e := range report.Parameters {
		byName[e.Name] = e
	}

	// The source is the point of the report: "what have I changed on this
	// machine" is not answerable from the values alone.
	assert.Equal(t, "18080", byName["http_port"].Value)
	assert.Equal(t, "release", byName["http_port"].Source)
	assert.Equal(t, "debug", byName["log_level"].Value)
	assert.Equal(t, "installation", byName["log_level"].Source)
}

// TestConfigListReportsStaleValues covers the update case the RFC left open: a
// release that drops a parameter leaves a recorded value bound to nothing.
// Reported rather than refused, because dropping it was the vendor's decision.
func TestConfigListReportsStaleValues(t *testing.T) {
	h := newHarness(t)
	inst := h.install()
	inst.Parameters = map[string]string{"http_port": "9000", "gone": "1"}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	report, err := ops.ConfigList(context.Background(), h.Deps)
	require.NoError(t, err)
	assert.Equal(t, []string{"gone"}, report.Stale)
}

func TestConfigSetRecordsAValueAndSaysWhatItDid(t *testing.T) {
	h := newHarness(t)
	h.install()

	result, err := ops.ConfigSet(context.Background(), h.Deps, ops.ConfigSetOptions{
		Set: map[string]string{"http_port": "9000"},
	})
	require.NoError(t, err)

	entry, err := ops.ConfigGet(context.Background(), h.Deps, "http_port")
	require.NoError(t, err)
	assert.Equal(t, "9000", entry.Value)
	assert.Equal(t, "installation", entry.Source)

	// Nothing is running in this harness, so the summary must not claim a
	// service was re-created. A change that says it took effect when it did
	// not is worse than one that says nothing.
	assert.Contains(t, result.Summary, "next `morzer apply`")
	assert.NotContains(t, result.Summary, "re-created")
}

func TestConfigSetRefusesAValueTheReleaseDoesNotAccept(t *testing.T) {
	h := newHarness(t)
	h.install()

	for name, set := range map[string]map[string]string{
		"an undeclared parameter": {"htpp_port": "9000"},
		"a value outside an enum": {"log_level": "chatty"},
		"a port out of range":     {"http_port": "70000"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ops.ConfigSet(context.Background(), h.Deps, ops.ConfigSetOptions{Set: set})
			require.Error(t, err)

			// And nothing was recorded: a refusal must not leave
			// half a change behind.
			inst, err := h.Deps.State.LoadInstallation(context.Background())
			require.NoError(t, err)
			assert.Empty(t, inst.Parameters)
		})
	}
}

func TestConfigUnsetReturnsToTheReleaseDefault(t *testing.T) {
	h := newHarness(t)
	h.install()
	ctx := context.Background()

	_, err := ops.ConfigSet(ctx, h.Deps, ops.ConfigSetOptions{
		Set: map[string]string{"http_port": "9000"},
	})
	require.NoError(t, err)

	_, err = ops.ConfigSet(ctx, h.Deps, ops.ConfigSetOptions{Unset: []string{"http_port"}})
	require.NoError(t, err)

	entry, err := ops.ConfigGet(ctx, h.Deps, "http_port")
	require.NoError(t, err)
	assert.Equal(t, "18080", entry.Value)
	assert.Equal(t, "release", entry.Source,
		"unsetting must return the value to the release's, not record the default")
}

func TestConfigSetIsANoOpWhenTheValueAlreadyMatches(t *testing.T) {
	h := newHarness(t)
	h.install()
	ctx := context.Background()

	_, err := ops.ConfigSet(ctx, h.Deps, ops.ConfigSetOptions{
		Set: map[string]string{"http_port": "9000"},
	})
	require.NoError(t, err)

	result, err := ops.ConfigSet(ctx, h.Deps, ops.ConfigSetOptions{
		Set: map[string]string{"http_port": "9000"},
	})
	require.NoError(t, err)
	assert.Contains(t, result.Summary, "no change")
	// No operation was run, so nothing was journaled for a change that did
	// not happen.
	assert.Empty(t, result.Record.ID)
}

// TestConfigSetKeepsTheOperatorFacingFileAccurate is the other half of the
// installation.yaml problem: the file is a report, so it has to stay true.
func TestConfigSetKeepsTheOperatorFacingFileAccurate(t *testing.T) {
	h := newHarness(t)
	h.install()

	_, err := ops.ConfigSet(context.Background(), h.Deps, ops.ConfigSetOptions{
		Set: map[string]string{"log_level": "warn"},
	})
	require.NoError(t, err)

	raw, err := os.ReadFile(h.Deps.Paths.InstallationFile())
	require.NoError(t, err)

	var onDisk domain.Installation
	require.NoError(t, yaml.Unmarshal(raw, &onDisk))
	assert.Equal(t, "warn", onDisk.Parameters["log_level"])

	// And the header no longer claims an edit takes effect, because it
	// never did.
	assert.NotContains(t, string(raw), "override release defaults")
	assert.Contains(t, string(raw), "morzer config set")
}

// TestDoctorReportsAHandEditThatChangesNothing is the diagnosis for the most
// confusing failure this layout allows: an operator edits installation.yaml,
// sees no effect, and cannot tell what is broken.
func TestDoctorReportsAHandEditThatChangesNothing(t *testing.T) {
	h := newHarness(t)
	h.install()
	ctx := context.Background()

	// Write the file, then edit it behind the manager's back.
	_, err := ops.ConfigSet(ctx, h.Deps, ops.ConfigSetOptions{
		Set: map[string]string{"log_level": "warn"},
	})
	require.NoError(t, err)

	path := h.Deps.Paths.InstallationFile()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	edited := strings.Replace(string(raw), "log_level: warn", "log_level: debug", 1)
	require.NotEqual(t, string(raw), edited, "the fixture must actually change something")
	require.NoError(t, os.WriteFile(path, []byte(edited), 0o640))

	report, err := ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)

	found := findCheck(t, report, "config.installation-file")
	assert.Equal(t, "warn", found.Status,
		"a hand edit that decides nothing must be reported")
	assert.Contains(t, found.Message, "parameters.log_level",
		"the report must name what disagrees, not just that something does")
	assert.Contains(t, found.Remedy, "config set")

	// And the recorded state is untouched: the file is a report.
	entry, err := ops.ConfigGet(ctx, h.Deps, "log_level")
	require.NoError(t, err)
	assert.Equal(t, "warn", entry.Value)
}

func TestDoctorIsQuietWhenTheFileAgrees(t *testing.T) {
	h := newHarness(t)
	h.install()

	_, err := ops.ConfigSet(context.Background(), h.Deps, ops.ConfigSetOptions{
		Set: map[string]string{"log_level": "warn"},
	})
	require.NoError(t, err)

	report, err := ops.Doctor(context.Background(), h.Deps)
	require.NoError(t, err)
	assert.Equal(t, "ok", findCheck(t, report, "config.installation-file").Status)
}

func findCheck(t *testing.T, report ops.DoctorReport, id string) (found checkResult) {
	t.Helper()
	for _, r := range report.Results {
		if r.ID == id {
			return checkResult{Status: string(r.Status), Message: r.Message, Remedy: r.Remedy}
		}
	}
	t.Fatalf("doctor produced no check %q", id)
	return
}

type checkResult struct{ Status, Message, Remedy string }
