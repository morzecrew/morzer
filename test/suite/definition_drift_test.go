package suite

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// Services whose containers were created from another definition than the
// release declares, which the next `up` re-creates (RFC 0044 §5.4). What the
// runtime reports is the Compose adapter's to get right, and its tests read
// the shapes Compose prints; these are about what is made of the answer.

// One replica of two created from another definition is enough for the service
// to be listed. A service with no container is not.
func TestAServiceCreatedFromAnotherDefinitionIsListed(t *testing.T) {
	h := appliedHarness(t)
	ctx := context.Background()
	h.Runtime.Definitions = []ports.ServiceDefinition{
		{Service: "web", Declared: "aaa", Running: []string{"aaa", "old"}},
		{Service: "db", Declared: "bbb", Running: []string{"bbb"}},
		{Service: "idle", Declared: "ccc"},
	}

	report, err := ops.ConfigDiff(ctx, h.Deps)
	require.NoError(t, err)
	assert.True(t, report.ServicesCompared)
	require.Len(t, report.Services, 1)
	assert.Equal(t, "web", report.Services[0].Service)
	assert.Contains(t, report.Text(), "web (1 of 2 containers)")
	assert.Contains(t, report.Summary(), "1 service was created from another definition")

	raw, err := json.Marshal(report)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"services":[{"service":"web","declared":"aaa","running":["aaa","old"]}]`)
	assert.Contains(t, string(raw), `"services_compared":true`)

	doctor, err := ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	check := findCheck(t, doctor, "runtime.definition")
	assert.Equal(t, "warn", check.Status)
	assert.Contains(t, check.Message, "web")
	assert.NotContains(t, check.Message, "idle")

	h.Runtime.Definitions[0].Running = []string{"aaa", "aaa"}
	report, err = ops.ConfigDiff(ctx, h.Deps)
	require.NoError(t, err)
	assert.Empty(t, report.Services)
	assert.NotContains(t, report.Summary(), "service")
	doctor, err = ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	assert.Equal(t, "ok", findCheck(t, doctor, "runtime.definition").Status)
}

// A runtime that cannot answer, and one without the capability, are reported
// as not compared, never as drift (decision 11).
func TestServicesARuntimeCannotCompareAreNotDrift(t *testing.T) {
	h := appliedHarness(t)
	ctx := context.Background()
	h.Runtime.Definitions = []ports.ServiceDefinition{{Service: "web", Declared: "aaa", Running: []string{"old"}}}

	h.Runtime.Fail = map[string]error{"DefinitionDrift": domain.RuntimeError(errInjected, "cannot read the services' definition hashes")}
	report, err := ops.ConfigDiff(ctx, h.Deps)
	require.NoError(t, err, "a runtime that cannot answer failed the command")
	assert.False(t, report.ServicesCompared)
	assert.Empty(t, report.Services)
	assert.Contains(t, report.Summary(), "The services were not compared: cannot read the services' definition hashes.")
	raw, err := json.Marshal(report)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"services":[]`)
	assert.Contains(t, string(raw), `"services_compared":false`)
	doctor, err := ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	check := findCheck(t, doctor, "runtime.definition")
	assert.Equal(t, "ok", check.Status)
	assert.Contains(t, check.Message, "not compared")

	h.Runtime.Fail = nil
	h.Deps.Runtime = struct{ ports.Runtime }{h.Runtime}
	report, err = ops.ConfigDiff(ctx, h.Deps)
	require.NoError(t, err)
	assert.False(t, report.ServicesCompared)
	assert.Contains(t, report.Summary(), "records no service definitions")
}

// An update re-creates every service from the new release's definition, and
// nothing on disk is lost by it, so a drifted service does not hold the tick.
func TestADriftedServiceDoesNotHoldAnUnattendedUpdate(t *testing.T) {
	h, _, _, _ := followingHarnessWith(t, recoverableUpdate)
	h.setHookEnv()
	applyBaseline(t, h)
	armAutoApply(t, h)
	h.Runtime.Definitions = []ports.ServiceDefinition{{Service: "web", Declared: "aaa", Running: []string{"old"}}}

	res, err := ops.RunUnattended(context.Background(), h.Deps, ops.UnattendedOptions{})
	require.NoError(t, err)
	assert.True(t, res.Applied, "a drifted service held the tick: %s", res.Assessment.Why())
}
