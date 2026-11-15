package filevault

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFdesetupStatusIsRead(t *testing.T) {
	for _, c := range []struct {
		name, out string
		want      bool
	}{
		{"on", "FileVault is On.\n", true},
		{"encrypting", "FileVault is On.\nEncryption in progress: Percent completed = 45\n", true},
		{"off", "FileVault is Off.\n", false},
		{"decrypting", "FileVault is Off.\nDecryption in progress: Percent completed = 12\n", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := parse(c.out)
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
	_, err := parse("fdesetup: command not permitted\n")
	require.ErrorContains(t, err, "fdesetup status said")
	_, err = parse("")
	require.Error(t, err)
}
