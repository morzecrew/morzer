package quadlet

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The generator logs each file it loads before any reason, so a refusal
// reported by its first line names a file and says nothing. The reason is
// the `converting` line. Measured on Podman 6.1.1; 4.9.3 logs the same way.
func TestAGeneratorRefusalSaysWhy(t *testing.T) {
	stderr := `quadlet-generator[42]: Loading source unit file /tmp/x/demo-app.container
quadlet-generator[42]: Loading source unit file /tmp/x/demo-db.container
quadlet-generator[42]: converting "demo-app.container": invalid port format '${DEMO_PARAM_HTTP_PORT}'
quadlet-generator[42]: converting "demo-db.container": unsupported key 'Nope' in group 'Container'
quadlet-generator[42]: processing encountered some errors
`
	assert.Equal(t,
		`converting "demo-app.container": invalid port format '${DEMO_PARAM_HTTP_PORT}'; `+
			`converting "demo-db.container": unsupported key 'Nope' in group 'Container'`,
		generatorError(stderr, ""))
	assert.Equal(t, `converting "demo-db.container": unsupported key 'Nope' in group 'Container'`,
		generatorError(stderr, "demo-db.container"), "a unit's refusal names its own reason")
	assert.Equal(t, "quadlet-generator[42]: something else", generatorError("quadlet-generator[42]: something else\n", ""),
		"with no reason line, the first line is all there is")
}
