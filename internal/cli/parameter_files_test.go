package cli

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/ui"
)

// refusedReader fails a test that reads it: standard input named twice is
// refused before it is read once, or a terminal user waits for an EOF they did
// not know was needed.
type refusedReader struct{ t *testing.T }

func (r refusedReader) Read([]byte) (int, error) {
	r.t.Error("standard input was read before --set-file was refused")
	return 0, io.EOF
}

func TestStandardInputNamedTwiceIsRefusedBeforeItIsRead(t *testing.T) {
	app := &App{Stream: ui.Streams{In: refusedReader{t}}}
	_, _, err := app.readParameterFiles(nil, []string{"a_value=-", "b_value=-"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "it can be read once")
}

func TestANameGivenBothWaysIsRefusedBeforeStandardInputIsRead(t *testing.T) {
	app := &App{Stream: ui.Streams{In: refusedReader{t}}}
	_, _, err := app.readParameterFiles(map[string]string{"a_value": "x"}, []string{"a_value=-"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `parameter "a_value" is set twice`)
}

// The same for a setting beside a value from standard input, which `config set`
// refuses as mixing the two.
func TestASettingBesideStandardInputIsRefusedBeforeItIsRead(t *testing.T) {
	app := &App{Stream: ui.Streams{In: refusedReader{t}, Out: io.Discard, Err: io.Discard}}
	cmd := newConfigSetCommand(app)
	cmd.SetArgs([]string{"update.check=true", "--set-file", "log_level=-"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.ExecuteContext(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parameters and installation settings are set separately")
}
