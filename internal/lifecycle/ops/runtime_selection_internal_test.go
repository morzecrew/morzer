package ops

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

// Which runtime an installation is fixed to, at the moment it is fixed.
// RFC 0023 decisions 3, 5 and 12.

func releaseDeclaring(runtimes domain.Runtimes) domain.Release {
	return domain.Release{
		Manifest: domain.Manifest{Runtimes: runtimes},
		Root:     "/opt/demo/releases/1",
	}
}

func TestASingleDeclaredRuntimeIsWhatTheInstallationIsFixedTo(t *testing.T) {
	got, err := runtimeForRelease(releaseDeclaring(domain.Runtimes{
		"quadlet": {Files: []string{"app.container"}},
	}))

	require.NoError(t, err)
	assert.Equal(t, "quadlet", got,
		"the recorded runtime is the one the release declares, not the one the manager happens to have")
}

// Decision 12. The manager cannot yet tell which of two declared runtimes it is
// able to drive, and the two ways to find out are both barred: a branch on a
// runtime's name above internal/adapters, which decision 7 forbids, or a name
// injected at the composition root that every test would set and no test would
// exercise as production leaves it. So it refuses.
//
// The refusal is asserted rather than the absence of a crash, because the
// failure this prevents is silent: picking one of the two and installing with
// it would produce a working machine running a substrate the operator never
// chose, and nothing downstream would ever contradict it.
func TestAReleaseDeclaringSeveralRuntimesIsRefusedRatherThanPickedFrom(t *testing.T) {
	got, err := runtimeForRelease(releaseDeclaring(domain.Runtimes{
		"compose": {Files: []string{"compose.yaml"}},
		"quadlet": {Files: []string{"app.container"}},
	}))

	require.Error(t, err)
	assert.Empty(t, got, "a refusal must not also return a runtime somebody might record")
	assert.Contains(t, err.Error(), "cannot yet choose between them")

	var domErr *domain.Error
	require.ErrorAs(t, err, &domErr)
	assert.Contains(t, domErr.Hint, "compose")
	assert.Contains(t, domErr.Hint, "quadlet")
	assert.Contains(t, domErr.Hint, "P3",
		"a refusal that expires should say what it is waiting for")
}

// A release declaring nothing at all records nothing, and
// Installation.RuntimeName reads that as the legacy runtime. Recording a
// default here instead would make "created before the field" and "chose the
// incumbent" the same value for ever after.
func TestAReleaseDeclaringNoRuntimeRecordsNothing(t *testing.T) {
	got, err := runtimeForRelease(releaseDeclaring(nil))

	require.NoError(t, err)
	assert.Empty(t, got)
}
