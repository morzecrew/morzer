package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

// portedManifest is a release whose port declaration, port requirement and
// health probe all derive from one parameter.
func portedManifest() *domain.Manifest {
	m := validManifestWithParameters(map[string]domain.ParameterSpec{
		"http_port": {Type: domain.ParamPort, Default: "18080"},
	})
	m.Requirements.Ports = []domain.PortSpec{"{{ .Parameters.http_port }}"}
	m.Health.Checks = []domain.HealthCheck{
		{Name: "api", Type: domain.HealthHTTP,
			URL: "http://127.0.0.1:{{ .Parameters.http_port }}/health/ready"},
		{Name: "db", Type: domain.HealthCommand, Command: []string{"hooks/check-db"}},
	}
	return m
}

// TestAChangedPortMovesEverythingTogether is the defect this closes, as a
// regression test.
//
// Before parameters, a Compose file could publish 9000 while
// `requirements.ports` said 18080 and the health probe asked 18080 — so an
// operator who changed the port got a working deployment and an `apply` that
// failed at "wait for health checks". The three must move as one.
func TestAChangedPortMovesEverythingTogether(t *testing.T) {
	m := portedManifest()

	params, err := domain.ResolveParameters(m.Parameters, map[string]string{"http_port": "9000"})
	require.NoError(t, err)

	required, err := m.ResolvePorts(params)
	require.NoError(t, err)
	assert.Equal(t, []int{9000}, required, "preflight must check the port that will be published")

	checks, err := m.ResolveHealthChecks(params)
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:9000/health/ready", checks[0].URL,
		"the probe must ask the port that will be published")

	// And unset, everything falls back to the vendor's default together.
	defaults, err := domain.ResolveParameters(m.Parameters, nil)
	require.NoError(t, err)

	required, err = m.ResolvePorts(defaults)
	require.NoError(t, err)
	assert.Equal(t, []int{18080}, required)

	checks, err = m.ResolveHealthChecks(defaults)
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:18080/health/ready", checks[0].URL)
}

// TestALiteralPortStillWorks keeps every manifest written before parameters
// existed valid. `ports: [18080]` is a number in YAML and must stay one.
func TestALiteralPortStillWorks(t *testing.T) {
	m := validManifestWithParameters(nil)
	m.Requirements.Ports = []domain.PortSpec{"18080", "5432"}

	got, err := m.ResolvePorts(domain.Parameters{})
	require.NoError(t, err)
	assert.Equal(t, []int{18080, 5432}, got)
}

func TestNonHTTPChecksAreLeftAlone(t *testing.T) {
	m := portedManifest()
	params, err := domain.ResolveParameters(m.Parameters, nil)
	require.NoError(t, err)

	checks, err := m.ResolveHealthChecks(params)
	require.NoError(t, err)
	assert.Equal(t, []string{"hooks/check-db"}, checks[1].Command,
		"a command check is the vendor's executable, not a template")
}

// TestATypoInAManifestTemplateIsRefused is the reason missingkey=error is set.
// Rendering `{{ .Parameters.htpp_port }}` as the empty string would produce
// `http://127.0.0.1:/health/ready` — a URL nothing serves, discovered two
// minutes later as a health-check timeout.
func TestATypoInAManifestTemplateIsRefused(t *testing.T) {
	m := portedManifest()
	m.Health.Checks[0].URL = "http://127.0.0.1:{{ .Parameters.htpp_port }}/health/ready"

	params, err := domain.ResolveParameters(m.Parameters, nil)
	require.NoError(t, err)

	_, err = m.ResolveHealthChecks(params)
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Hint, "http_port",
		"the hint must name what is available")
}

func TestAPortThatResolvesToNonsenseIsRefused(t *testing.T) {
	m := validManifestWithParameters(map[string]domain.ParameterSpec{
		"listen": {Type: domain.ParamString, Default: "http"},
	})
	m.Requirements.Ports = []domain.PortSpec{"{{ .Parameters.listen }}"}

	params, err := domain.ResolveParameters(m.Parameters, nil)
	require.NoError(t, err)

	_, err = m.ResolvePorts(params)
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Message, "not a port number")
}

// TestTheTemplateContextIsParametersOnly pins decision 6. A health URL able to
// interpolate a secret path is a health URL able to put one in a log line.
func TestTheTemplateContextIsParametersOnly(t *testing.T) {
	params := domain.Parameters{"http_port": "9000"}

	for _, expr := range []string{
		"{{ .Secrets.db_password }}",
		"{{ .Paths.Secrets }}",
		"{{ .Installation.ID }}",
	} {
		_, err := params.Resolve("health.checks[0].url", "http://x/"+expr)
		assert.Error(t, err, "%s must not resolve: the context is .Parameters only", expr)
	}
}

// TestAManifestWithNoTemplatesIsUntouched keeps the overwhelmingly common case
// free of the templating path entirely, so a bug in it cannot reach a manifest
// that does not use the feature.
func TestAManifestWithNoTemplatesIsUntouched(t *testing.T) {
	params := domain.Parameters{}
	const url = "http://127.0.0.1:18080/health/ready"

	got, err := params.Resolve("health.checks[0].url", url)
	require.NoError(t, err)
	assert.Equal(t, url, got)
}
