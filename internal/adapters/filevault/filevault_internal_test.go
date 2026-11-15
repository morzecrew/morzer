package filevault

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFdesetupStatusIsRead(t *testing.T) {
	for out, want := range map[string]bool{
		"FileVault is On.\n": true,
		"FileVault is On.\nEncryption in progress: Percent completed = 45\n": true,
		"FileVault is Off.\n": false,
		"FileVault is Off.\nDecryption in progress: Percent completed = 12\n": false,
	} {
		got, err := parse(out)
		require.NoError(t, err, out)
		assert.Equal(t, want, got, out)
	}
	_, err := parse("fdesetup: command not permitted\n")
	require.ErrorContains(t, err, "fdesetup status said")
	_, err = parse("")
	require.Error(t, err)
}
