package ops_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// A scalar keeps the text it was written as, and the parameter's own type
// decides what it means (RFC 0056 decision 4). Decoded into Go values first,
// `1.10` would reach a string parameter as 1.1 and `0x1F` as 31.
func TestAChangeKeepsEachScalarAsWritten(t *testing.T) {
	change, err := ops.ParseConfigChange([]byte(`
settings:
  installation.profile: xdns,openvpn
parameters:
  http_port: 9000
  quoted: "9000"
  word: yes
  flag: true
  version: 1.10
  hex: 0x1F
  empty: ''
  rules: |
    allow a
    allow b
secrets:
  upstream_openvpn: |
    client
    remote vpn.example 1194
  token: abc
unset:
  settings: [update.channel]
  parameters: [legacy_proxy]
  secrets: []
`))
	require.NoError(t, err)

	assert.Equal(t, map[string]string{"installation.profile": "xdns,openvpn"}, change.Settings)
	assert.Equal(t, map[string]string{
		"http_port": "9000", "quoted": "9000", "word": "yes", "flag": "true",
		"version": "1.10", "hex": "0x1F", "empty": "", "rules": "allow a\nallow b\n",
	}, change.Parameters)
	assert.Equal(t, "client\nremote vpn.example 1194", change.Secrets["upstream_openvpn"].Reveal(),
		"a secret loses exactly one trailing newline, as a piped `secret set` value does")
	assert.Equal(t, "abc", change.Secrets["token"].Reveal())
	assert.Equal(t, ops.ConfigChangeUnset{
		Settings: []string{"update.channel"}, Parameters: []string{"legacy_proxy"},
	}, change.Unset)
	assert.False(t, change.IsEmpty())
}

func TestAChangeSecretKeepsAllButOneTrailingNewline(t *testing.T) {
	change, err := ops.ParseConfigChange([]byte("secrets:\n  key: |+\n    value\n\n"))
	require.NoError(t, err)
	assert.Equal(t, "value\n", change.Secrets["key"].Reveal())
}

func TestAnEmptyChangeDocumentChangesNothing(t *testing.T) {
	for _, doc := range []string{"", "# nothing\n", "settings:\nparameters: {}\nunset:\n"} {
		change, err := ops.ParseConfigChange([]byte(doc))
		require.NoError(t, err, "%q", doc)
		assert.True(t, change.IsEmpty(), "%q", doc)
	}
}

func TestAChangeDocumentIsStrict(t *testing.T) {
	for doc, want := range map[string]string{
		"settngs:\n  a: b\n":                `line 1: "settngs" is not a section`,
		"parameters:\n  a: [1, 2]\n":        "line 2: parameters.a is not a single value",
		"parameters:\n  a: {b: c}\n":        "parameters.a is not a single value",
		"parameters:\n  a:\n":               "parameters.a has no value",
		"parameters:\n  a: ~\n":             "parameters.a has no value",
		"parameters:\n  a: !!str 5\n":       "parameters.a is not a single value",
		"parameters:\n  a: &x v\n":          "parameters.a is not a single value",
		"parameters:\n  a: 1\n  a: 2\n":     "not valid YAML at line 3, column 3",
		"secrets: [a]\n":                    "secrets is a mapping",
		"unset:\n  parameters: a\n":         "unset.parameters is a list of names",
		"unset:\n  params: [a]\n":           "unset.params is not a list unset takes",
		"unset:\n  secrets: [a, a]\n":       `unset.secrets names "a" twice`,
		"- a\n":                             "the change document is a mapping",
		"settings: {}\n---\nsettings: {}\n": "holds 2 YAML documents",
		"parameters: {a: b\n":               "not valid YAML",
	} {
		_, err := ops.ParseConfigChange([]byte(doc))
		require.Error(t, err, "%q was accepted", doc)
		assert.Contains(t, err.Error(), want, "%q", doc)
		assert.Equal(t, domain.ExitUsage, domain.ExitCode(err), "%q", doc)
	}
}

func TestAChangeDocumentIsBounded(t *testing.T) {
	big := "secrets:\n  a: " + strings.Repeat("x", ops.ChangeDocumentLimit) + "\n"
	_, err := ops.ParseConfigChange([]byte(big))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "larger than 1 MiB")
}

// The parser's own message quotes the document around the fault, and a change
// document holds secret values, so a syntax error names only the position.
func TestABrokenChangeDocumentQuotesNoValue(t *testing.T) {
	_, err := ops.ParseConfigChange([]byte("secrets:\n  key: \"hunter2-MUST-NOT-PRINT\n  other: [\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid YAML")
	assert.NotContains(t, err.Error(), "hunter2-MUST-NOT-PRINT")
	assert.NotContains(t, domain.AsError(err).Hint, "hunter2-MUST-NOT-PRINT")
}

const envelope = `api_version: selfhost/v1alpha1
kind: desired-state
product: demo
installation: inst_01
serial: 1794310000123
issued_at: 2026-11-10T12:00:00Z
expires_at: 2026-11-17T12:00:00Z
`

// A desired-state document is a change document's body inside an envelope,
// which ParseDesiredState reads and ParseConfigChange refuses.
func TestADesiredStateDocumentCarriesAnEnvelope(t *testing.T) {
	env, change, err := ops.ParseDesiredState([]byte(envelope + "parameters:\n  site: example.com\n"))
	require.NoError(t, err)
	assert.Equal(t, "demo", env.Product)
	assert.Equal(t, "inst_01", env.Installation)
	assert.Equal(t, int64(1794310000123), env.Serial)
	assert.Equal(t, "2026-11-17T12:00:00Z", env.ExpiresAt.UTC().Format(time.RFC3339))
	assert.Equal(t, map[string]string{"site": "example.com"}, change.Parameters)

	_, err = ops.ParseConfigChange([]byte(envelope))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"api_version" is not a section of a change document`)
}

func TestADesiredStateEnvelopeIsComplete(t *testing.T) {
	for _, tc := range []struct{ drop, replace, want string }{
		{drop: "serial: 1794310000123\n", want: "has no serial"},
		{drop: "expires_at: 2026-11-17T12:00:00Z\n", want: "has no expires_at"},
		{replace: "kind: desired-state", want: `of kind "other"`},
		{replace: "serial: 1794310000123", want: `serial "-5" is not a positive whole number`},
		{replace: "expires_at: 2026-11-17T12:00:00Z", want: `expires_at "next week" is not an RFC 3339 time`},
		{replace: "api_version: selfhost/v1alpha1", want: `api_version "selfhost/v9"`},
	} {
		doc := envelope
		if tc.drop != "" {
			doc = strings.Replace(doc, tc.drop, "", 1)
		}
		if tc.replace != "" {
			key := strings.SplitN(tc.replace, ":", 2)[0]
			value := map[string]string{
				"kind": "other", "serial": "-5", "expires_at": "next week", "api_version": "selfhost/v9",
			}[key]
			doc = strings.Replace(doc, tc.replace, key+": "+value, 1)
		}
		_, _, err := ops.ParseDesiredState([]byte(doc))
		require.Error(t, err, "%q", doc)
		assert.Contains(t, err.Error(), tc.want)
	}
}
