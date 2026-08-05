package suite

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/target"
	"github.com/morzecrew/morzer/internal/adapters/target/localdir"
	"github.com/morzecrew/morzer/internal/adapters/target/sftp"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/fakes"
)

// TestATargetRegistryRefusesANilTarget. A nil target is a wiring mistake, and
// the two spellings of it fail differently: an untyped nil would leave a build
// silently missing a transport, while a nil *sftp.Target is not caught by
// t == nil at all -- it registers, answers for sftp://, and takes the process
// down at shutdown when Close dereferences the receiver. Both belong at
// startup, where the error still has somewhere to go.
func TestATargetRegistryRefusesANilTarget(t *testing.T) {
	t.Run("untyped", func(t *testing.T) {
		_, err := target.NewRegistry(localdir.New(), nil)
		require.Error(t, err, "a nil in the target list is a wiring mistake, not a target to skip")
		assert.Contains(t, err.Error(), "nil")
	})

	t.Run("typed nil pointer", func(t *testing.T) {
		var missing *sftp.Target

		_, err := target.NewRegistry(localdir.New(), missing)
		require.Error(t, err, "a nil *sftp.Target must not reach the registry: closing it panics")
		assert.Contains(t, err.Error(), "nil")
	})
}

// TestATargetRegistryClosesATargetThatIsNotComparable. Nothing says a target
// has to be a pointer. One implemented on a value with a slice field registers
// and pushes exactly like any other, but hashing it as a map key panics -- so
// deduplicating the close loop through a set keyed by the interface turned the
// shutdown path into a crash for a target that had done nothing wrong.
func TestATargetRegistryClosesATargetThatIsNotComparable(t *testing.T) {
	var closes int
	odd := unhashableTarget{
		BackupTarget: fakes.NewBackupTarget(),
		// Two schemes, so the count below means something: the set that
		// used to deduplicate this loop is gone, and what replaced it is
		// one entry per target rather than per scheme.
		schemes: []string{"one", "two"},
		closes:  &closes,
	}

	registry, err := target.NewRegistry(odd)
	require.NoError(t, err)

	var closeErr error
	require.NotPanics(t, func() { closeErr = registry.Close() },
		"a target whose type is not comparable must still close")
	require.NoError(t, closeErr)
	assert.Equal(t, 1, closes,
		"a target answering for two schemes must be closed once, not once per scheme")
}

// unhashableTarget is a target on a value receiver holding a slice, which makes
// the type unusable as a map key. closes is a pointer because the registry
// stores a copy of the value.
type unhashableTarget struct {
	*fakes.BackupTarget
	schemes []string
	closes  *int
}

var _ ports.BackupTarget = unhashableTarget{}

func (u unhashableTarget) Schemes() []string { return u.schemes }
func (u unhashableTarget) Close() error {
	*u.closes++
	return nil
}
