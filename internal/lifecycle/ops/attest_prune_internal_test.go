package ops

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Two operations' prunes can overlap, so one finds a statement the other has
// removed, which is not an error (RFC 0049 decision 5).
func TestRemovingAStatementAlreadyGoneIsNotAnError(t *testing.T) {
	require.NoError(t, removeStatement(filepath.Join(t.TempDir(), "op_1.json")))
}
