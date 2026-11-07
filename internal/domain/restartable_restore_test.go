package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A release whose restore hook is restartable, at the manager version that
// reads the field (RFC 0051 decisions 1 and 12).
func manifestWithRestartableRestore() Manifest {
	m := validManifest()
	m.Compatibility.MinManagerVersion = MustParseVersion(RestartableRestoreMinManagerVersion)
	m.Operations = map[string]OperationSteps{
		OpRestore: {{Kind: OperationKindHook, Command: []string{"hooks/restore"}, Restartable: true}},
	}
	return m
}

func TestARestoreHookMayBeDeclaredRestartable(t *testing.T) {
	m := manifestWithRestartableRestore()
	require.NoError(t, m.Validate())
	hook, ok := m.Operation(OpRestore)
	require.True(t, ok)
	assert.True(t, hook.Restartable)
}

// Only the restore hook is ever run again by a resume, so the promise means
// nothing anywhere else, and a vendor writing it there has misread it.
func TestRestartableIsRefusedOnEveryOtherOperation(t *testing.T) {
	for _, name := range []string{OpMigrate, OpSmokeTest, OpBackup} {
		t.Run(name, func(t *testing.T) {
			m := manifestWithRestartableRestore()
			m.Operations[name] = OperationSteps{{Kind: OperationKindHook, Command: []string{"hooks/" + name}, Restartable: true}}
			err := m.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "operations."+name+".restartable: may be declared only on restore")
		})
	}
}

// false is the default and reads as the field left out, so it is accepted
// anywhere and needs no newer manager.
func TestRestartableFalseIsAcceptedAnywhere(t *testing.T) {
	m := validManifest()
	m.Operations = map[string]OperationSteps{
		OpMigrate: {{Kind: OperationKindHook, Command: []string{"hooks/migrate"}}},
		OpRestore: {{Kind: OperationKindHook, Command: []string{"hooks/restore"}}},
	}
	require.NoError(t, m.Validate())
}

func TestARestartableRestoreNeedsTheManagerThatReadsIt(t *testing.T) {
	m := manifestWithRestartableRestore()
	m.Compatibility.MinManagerVersion = MustParseVersion("0.13.0")
	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "compatibility.min_manager_version: must be at least "+RestartableRestoreMinManagerVersion)
}
