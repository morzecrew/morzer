package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

func TestTaskInputsAreNameEqualsPath(t *testing.T) {
	got, err := parseTaskInputs([]string{"snapshot=./a=b.jsonl", "meta=/tmp/m"})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"snapshot": "./a=b.jsonl", "meta": "/tmp/m"}, got,
		"the path is everything after the first =")

	for _, bad := range [][]string{{"snapshot"}, {"=path"}, {"snapshot="}, {"a=1", "a=2"}} {
		_, err := parseTaskInputs(bad)
		require.Error(t, err, "%q", bad)
		assert.Equal(t, domain.ExitUsage, domain.ExitCode(err), "%q", bad)
	}
}
