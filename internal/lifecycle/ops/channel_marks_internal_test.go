package ops

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

// An import replaces the marks rather than merging them: an installation
// imported over another must not keep that one's history, and an export
// written with a password in a channel's reference must not put it on disk.
func TestAnImportReplacesTheMarksWithoutCredentials(t *testing.T) {
	d := &Deps{Paths: domain.PathsUnder(t.TempDir(), "demo")}
	require.NoError(t, os.MkdirAll(filepath.Dir(d.Paths.ChannelMarksFile()), 0o755))
	v := domain.MustParseVersion
	require.NoError(t, d.saveChannelMarks(domain.ChannelMarks{
		"oci://registry.example/other/bundle:stable": {Version: v("9.0.0")},
	}))

	require.NoError(t, d.replaceChannelMarks(domain.ChannelMarks{
		"oci://user:hunter2@registry.example/demo/bundle:stable": {Version: v("1.4.0")},
		"oci://user:rotated@registry.example/demo/bundle:stable": {Version: v("1.3.0")},
	}))
	raw, err := os.ReadFile(d.Paths.ChannelMarksFile())
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "hunter2")
	assert.NotContains(t, string(raw), "rotated")
	var got domain.ChannelMarks
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Len(t, got, 1, "the replaced installation's marks survived, or one channel became two")
	for _, mark := range got {
		assert.Equal(t, "1.4.0", mark.Version.String(), "two spellings of one channel kept the lower mark")
	}

	require.NoError(t, d.replaceChannelMarks(nil))
	_, err = os.Stat(d.Paths.ChannelMarksFile())
	assert.True(t, os.IsNotExist(err), "an export with no marks left the old ones in place")
}
