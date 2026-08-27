package scheme

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ptrAdapter is the ordinary shape: a pointer receiver, comparable.
type ptrAdapter struct {
	schemes  []string
	closes   *int
	closeErr error
}

func (a *ptrAdapter) Schemes() []string { return a.schemes }

func (a *ptrAdapter) Close() error {
	if a.closes != nil {
		*a.closes++
	}
	return a.closeErr
}

// valueAdapter is a target implemented on a value with a slice field. Nothing
// says an adapter has to be a pointer, and hashing this one as a map key
// panics -- which is why Close walks the argument list instead of a set.
type valueAdapter struct {
	schemes []string
	closes  *int
}

func (a valueAdapter) Schemes() []string { return a.schemes }
func (a valueAdapter) Close() error      { *a.closes++; return nil }

func TestNewIndexRefusesANilAdapter(t *testing.T) {
	t.Run("untyped", func(t *testing.T) {
		_, err := NewIndex[Adapter]("release source", &ptrAdapter{schemes: []string{"file"}}, nil)
		require.Error(t, err, "a nil in the list is a wiring mistake, not an adapter to skip")
		assert.Contains(t, err.Error(), "nil release source")
	})

	t.Run("typed nil pointer", func(t *testing.T) {
		// The spelling t == nil does not catch: it satisfies the
		// interface, registers, and takes the process down at shutdown.
		var missing *ptrAdapter

		_, err := NewIndex[Adapter]("backup target", &ptrAdapter{schemes: []string{"file"}}, missing)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "nil backup target")
	})
}

func TestNewIndexRefusesDuplicateAndEmptyWiring(t *testing.T) {
	_, err := NewIndex("release source",
		&ptrAdapter{schemes: []string{"file"}}, &ptrAdapter{schemes: []string{"file"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `two release sources both claim the "file" scheme`)

	_, err = NewIndex("backup target", &ptrAdapter{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "declares no schemes")

	_, err = NewIndex[*ptrAdapter]("backup target")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no backup targets were registered")
}

func TestLookupAndSchemes(t *testing.T) {
	one := &ptrAdapter{schemes: []string{"ssh", "file"}}
	two := &ptrAdapter{schemes: []string{"s3"}}

	index, err := NewIndex("backup target", one, two)
	require.NoError(t, err)

	assert.Equal(t, []string{"file", "s3", "ssh"}, index.Schemes(), "sorted, so refusals read the same every run")

	got, ok := index.Lookup("ssh")
	require.True(t, ok)
	assert.Same(t, one, got)

	_, ok = index.Lookup("ftp")
	assert.False(t, ok)
}

// TestCloseVisitsEachAdapterOnce. Two schemes on one adapter must not close it
// twice, and an adapter whose type is not comparable must not panic the walk.
func TestCloseVisitsEachAdapterOnce(t *testing.T) {
	var ptrCloses, valueCloses int
	index, err := NewIndex[Adapter](
		"backup target",
		&ptrAdapter{schemes: []string{"ssh", "file"}, closes: &ptrCloses},
		valueAdapter{schemes: []string{"one", "two"}, closes: &valueCloses},
	)
	require.NoError(t, err)

	require.NotPanics(t, func() { require.NoError(t, index.Close()) })
	assert.Equal(t, 1, ptrCloses)
	assert.Equal(t, 1, valueCloses)
}

// TestCloseKeepsGoingAfterAFailure. An adapter that cannot tidy up must not
// leave the next one's socket open.
func TestCloseKeepsGoingAfterAFailure(t *testing.T) {
	boom := errors.New("cannot close")
	var closes int

	index, err := NewIndex[Adapter](
		"release source",
		&ptrAdapter{schemes: []string{"file"}, closeErr: boom},
		&ptrAdapter{schemes: []string{"https"}, closes: &closes},
	)
	require.NoError(t, err)

	err = index.Close()
	require.ErrorIs(t, err, boom)
	assert.Equal(t, 1, closes, "the second source was closed despite the first failing")
}
