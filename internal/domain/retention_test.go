package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Installation state is a file an operator can edit, and a negative count
// reaches the attestation prune as a slice bound (RFC 0049).
func TestAnInstallationRefusesANegativeAttestationCount(t *testing.T) {
	inst := Installation{SchemaVersion: InstallationSchemaVersion, ID: "inst_01", Product: "demo"}
	require.NoError(t, inst.Validate())
	inst.Policy.RetainAttestations = -1
	err := inst.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "policy.retain_attestations")
}
