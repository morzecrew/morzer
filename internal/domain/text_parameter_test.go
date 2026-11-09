package domain_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

// Multi-line text parameters, RFC 0054.

var textSpec = domain.ParameterSpec{Type: domain.ParamText}

// Line endings become LF and trailing newlines go; nothing else is trimmed, so
// indentation survives and an editor's final newline changes nothing
// (decision 2).
func TestATextValueIsNormalisedAndNotTrimmed(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"one line", "DOMAIN-SUFFIX,example.ru", "DOMAIN-SUFFIX,example.ru"},
		{"a final newline", "a\nb\n", "a\nb"},
		{"several final newlines", "a\nb\n\n\n", "a\nb"},
		{"CRLF line endings", "a\r\nb\r\n", "a\nb"},
		{"CR line endings", "a\rb\r", "a\nb"},
		{"CR and CRLF mixed", "a\r\nb\rc", "a\nb\nc"},
		{"leading indentation", "  key: 1\n  other: 2\n", "  key: 1\n  other: 2"},
		{"a blank line inside", "a\n\nb", "a\n\nb"},
		{"trailing spaces on a line", "a  \nb", "a  \nb"},
		{"a tab", "a\tb", "a\tb"},
		{"empty", "", ""},
		{"only newlines", "\n\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := textSpec.Parse(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// A NUL could not reach any Compose call or hook, and invalid UTF-8 would
// change on the state's first JSON save, so both are refused (decision 3).
func TestATextValueRefusesWhatCannotBeDelivered(t *testing.T) {
	for _, tc := range []struct{ name, in, refusal string }{
		{"a NUL byte", "a\x00b", "holds a NUL byte"},
		{"invalid UTF-8", "a\xffb", "not valid UTF-8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := textSpec.Parse(tc.in)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.refusal)
		})
	}
}

// The limit is on the normalised value, so a final newline does not push a
// value at the limit over it (decision 4).
func TestATextValueHoldsAtMost64KiB(t *testing.T) {
	limit := int(domain.TextParameterLimit)
	require.Equal(t, 64*1024, limit)

	at := strings.Repeat("a", limit)
	got, err := textSpec.Parse(at + "\n")
	require.NoError(t, err)
	assert.Len(t, got, limit)

	_, err = textSpec.Parse(at + "a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the value is 64.0KiB, and a text parameter holds at most 64KiB")
	assert.Contains(t, domain.AsError(err).Hint, "environment")
}

// `--set-file` refuses a NUL and invalid UTF-8 whatever the type, because
// `string` accepts both (decision 9), and passes anything else through as is
// for the type's own Parse.
func TestAFileValueIsRefusedForWhatNoTypeCanCarry(t *testing.T) {
	got, err := domain.ParameterFileValue("site_name", []byte("  Demo Corp\n"))
	require.NoError(t, err)
	assert.Equal(t, "  Demo Corp\n", got)

	_, err = domain.ParameterFileValue("site_name", []byte("Demo\x00Corp"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `parameter "site_name"`)
	assert.Contains(t, err.Error(), "NUL")

	_, err = domain.ParameterFileValue("site_name", []byte{'D', 0xff})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UTF-8")

	parsed, err := domain.ParameterSpec{Type: domain.ParamString}.Parse("Demo\x00Corp")
	require.NoError(t, err, "string itself is not narrowed (decision 15)")
	assert.Equal(t, "Demo\x00Corp", parsed)
}

func TestLinesAreCountedAndTheFirstOneShown(t *testing.T) {
	assert.Equal(t, 0, domain.LineCount(""))
	assert.Equal(t, 1, domain.LineCount("a"))
	assert.Equal(t, 3, domain.LineCount("a\n\nb"))
	assert.Equal(t, "a", domain.FirstLine("a\nb"))
	assert.Equal(t, "a", domain.FirstLine("a"))
	assert.Equal(t, "1 line", domain.Lines(1))
	assert.Equal(t, "3 lines", domain.Lines(3))
}

// A release declaring `text` needs the manager that reads it; one that
// declares none does not (decision 12). A default the type refuses is a
// manifest error, as for every type.
func TestATextParameterNeedsTheFloor(t *testing.T) {
	floor := "compatibility.min_manager_version: must be at least " +
		domain.TextParametersMinManagerVersion + `, because a parameter has type "text"`

	m := validManifestWithParameters(map[string]domain.ParameterSpec{"rules": textSpec})
	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), floor)

	m.Compatibility.MinManagerVersion = domain.MustParseVersion(domain.TextParametersMinManagerVersion)
	err = m.Validate()
	if err != nil {
		assert.NotContains(t, err.Error(), "min_manager_version")
		assert.NotContains(t, err.Error(), "parameters.rules")
	}

	m.Parameters["rules"] = domain.ParameterSpec{Type: domain.ParamText, Default: "a\x00b"}
	err = m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parameters.rules.default")

	plain := validManifestWithParameters(map[string]domain.ParameterSpec{
		"site_name": {Type: domain.ParamString},
	})
	if err := plain.Validate(); err != nil {
		assert.NotContains(t, err.Error(), "min_manager_version")
	}
}

// A refused text value names its reason in the message, which it can because
// no text refusal quotes the value. The other types quote theirs, and an
// error's message reaches fleet rows, so their message stays value-free.
func TestARefusedTextValueSaysWhyAndNoValueTravels(t *testing.T) {
	declared := map[string]domain.ParameterSpec{
		"http_port": {Type: domain.ParamPort},
		"rules":     textSpec,
	}

	_, err := domain.ResolveParameters(declared, map[string]string{"http_port": "PORT-VALUE-70000"})
	require.Error(t, err)
	assert.Equal(t, `parameter "http_port"`, domain.AsError(err).Message)
	assert.Contains(t, domain.AsError(err).Hint, "http_port takes a port value")

	over := strings.Repeat("TEXT-VALUE", int(domain.TextParameterLimit)/10+1)
	_, err = domain.ResolveParameters(declared, map[string]string{"rules": over})
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Message, `parameter "rules": the value is `)
	assert.NotContains(t, domain.AsError(err).Message, "TEXT-VALUE")
	assert.Contains(t, domain.AsError(err).Hint, "environment")

	for _, bad := range []string{"TEXT-VALUE\x00", "TEXT-VALUE\xff"} {
		_, err = domain.ResolveParameters(declared, map[string]string{"rules": bad})
		require.Error(t, err)
		assert.NotContains(t, domain.AsError(err).Message, "TEXT-VALUE")
	}
}
