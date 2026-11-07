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
		OpRestore: {{Kind: OperationKindHook, Command: []string{"hooks/restore"}, Restartable: new(true)}},
	}
	return m
}

// TestARestoreHookMayBeDeclaredRestartable at the floor, and the step reads it.
func TestARestoreHookMayBeDeclaredRestartable(t *testing.T) {
	m := manifestWithRestartableRestore()
	require.NoError(t, m.Validate())
	hook, ok := m.Operation(OpRestore)
	require.True(t, ok)
	assert.True(t, hook.IsRestartable())
}

// Only the restore hook is ever run again by a resume, so the promise means
// nothing anywhere else, and a vendor writing it there has misread it.
func TestRestartableIsRefusedOnEveryOtherOperation(t *testing.T) {
	for _, name := range []string{OpMigrate, OpSmokeTest, OpBackup} {
		t.Run(name, func(t *testing.T) {
			m := manifestWithRestartableRestore()
			m.Operations[name] = OperationSteps{{Kind: OperationKindHook, Command: []string{"hooks/" + name}, Restartable: new(true)}}
			err := m.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "operations."+name+".restartable: may be declared only on restore")
		})
	}
}

// Left out, it is accepted on every operation at any floor, and the step is not
// restartable. Declared false on restore, it needs the floor all the same: an
// older manager refuses the key, not its value.
func TestRestartableLeftOutChangesNothing(t *testing.T) {
	m := validManifest()
	m.Operations = map[string]OperationSteps{
		OpMigrate: {{Kind: OperationKindHook, Command: []string{"hooks/migrate"}}},
		OpRestore: {{Kind: OperationKindHook, Command: []string{"hooks/restore"}}},
	}
	require.NoError(t, m.Validate())
	hook, _ := m.Operation(OpRestore)
	assert.False(t, hook.IsRestartable())

	m.Operations[OpRestore] = OperationSteps{{Kind: OperationKindHook, Command: []string{"hooks/restore"}, Restartable: new(false)}}
	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "compatibility.min_manager_version")
}

// TestARestartableRestoreNeedsTheManagerThatReadsIt, which an older one refuses.
func TestARestartableRestoreNeedsTheManagerThatReadsIt(t *testing.T) {
	m := manifestWithRestartableRestore()
	m.Compatibility.MinManagerVersion = MustParseVersion("0.13.0")
	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "compatibility.min_manager_version: must be at least "+RestartableRestoreMinManagerVersion)
}
