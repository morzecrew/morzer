package domain_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

func TestParameterValuesAreValidatedByType(t *testing.T) {
	cases := []struct {
		name string
		spec domain.ParameterSpec
		in   string
		want string // empty means the value must be refused
	}{
		{"a port", domain.ParameterSpec{Type: domain.ParamPort}, "18080", "18080"},
		{"a port with padding", domain.ParameterSpec{Type: domain.ParamPort}, " 9000 ", "9000"},
		{"port zero", domain.ParameterSpec{Type: domain.ParamPort}, "0", ""},
		{"a port above the range", domain.ParameterSpec{Type: domain.ParamPort}, "70000", ""},
		{"a negative port", domain.ParameterSpec{Type: domain.ParamPort}, "-1", ""},
		{"a word where a port belongs", domain.ParameterSpec{Type: domain.ParamPort}, "http", ""},

		{"an int", domain.ParameterSpec{Type: domain.ParamInt}, "-3", "-3"},
		{"not an int", domain.ParameterSpec{Type: domain.ParamInt}, "3.5", ""},

		{"a bool", domain.ParameterSpec{Type: domain.ParamBool}, "TRUE", "true"},
		{"not a bool", domain.ParameterSpec{Type: domain.ParamBool}, "yes-please", ""},

		{"anything is a string", domain.ParameterSpec{Type: domain.ParamString}, "a b c", "a b c"},

		{"an allowed enum value",
			domain.ParameterSpec{Type: domain.ParamEnum, Values: []string{"debug", "info"}}, "debug", "debug"},
		{"an enum value outside the set",
			domain.ParameterSpec{Type: domain.ParamEnum, Values: []string{"debug", "info"}}, "chatty", ""},

		{"a duration", domain.ParameterSpec{Type: domain.ParamDuration}, "90s", "1m30s"},
		{"not a duration", domain.ParameterSpec{Type: domain.ParamDuration}, "soon", ""},

		{"a size", domain.ParameterSpec{Type: domain.ParamBytes}, "25MiB", "25MiB"},
		{"not a size", domain.ParameterSpec{Type: domain.ParamBytes}, "lots", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.spec.Parse(tc.in)
			if tc.want == "" {
				assert.Error(t, err, "%q must be refused for type %s", tc.in, tc.spec.Type)
				return
			}
			require.NoError(t, err)
			// Normalised, not echoed: ` 9000 ` and `9000` must not
			// become two different ports downstream.
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestPortZeroIsRefused is called out separately because it is the one value
// that parses fine and still breaks the deployment: Compose reads a published
// port of 0 as "pick any", and the health check then probes a port nothing is
// listening on.
func TestPortZeroIsRefused(t *testing.T) {
	_, err := domain.ParameterSpec{Type: domain.ParamPort}.Parse("0")
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Message, "out of range")
}

func TestResolveParametersFillsDefaults(t *testing.T) {
	declared := map[string]domain.ParameterSpec{
		"http_port": {Type: domain.ParamPort, Default: "18080"},
		"log_level": {Type: domain.ParamEnum, Values: []string{"info", "debug"}, Default: "info"},
	}

	params, err := domain.ResolveParameters(declared, map[string]string{"log_level": "debug"})
	require.NoError(t, err)

	// Every declared parameter is present, so a consumer never has to tell
	// "unset" from "set to the default".
	assert.Equal(t, domain.Parameters{"http_port": "18080", "log_level": "debug"}, params)
}

// TestADeclarationWithNoDefaultIsPresentAndEmpty.
//
// "Every declared parameter is present" is the type's own contract and the
// documented promise, and a declaration without a default broke it: the name
// was skipped, so `Require` reported "the release declares no parameter %q"
// about a parameter the release declares.
//
// Present-and-empty rather than refused. Refusing here would be refusing on
// every operation, and only one of them can do anything about it: an update
// meeting a parameter the *new* release added would have no way forward,
// because the value cannot be set before the release that declares it is
// installed. MissingValues is what the commands that can act on it ask.
func TestADeclarationWithNoDefaultIsPresentAndEmpty(t *testing.T) {
	declared := map[string]domain.ParameterSpec{
		"admin_email": {Type: domain.ParamString},
		"http_port":   {Type: domain.ParamPort, Default: "18080"},
	}

	params, err := domain.ResolveParameters(declared, nil)
	require.NoError(t, err)
	assert.Equal(t, domain.Parameters{"admin_email": "", "http_port": "18080"}, params)

	got, err := params.Require("admin_email")
	require.NoError(t, err,
		"a declared parameter reported as undeclared, which is what the contract "+
			"exists to prevent")
	assert.Empty(t, got)

	// And the commands that can ask for a value are told which to ask for.
	assert.Equal(t, []string{"admin_email"}, domain.MissingValues(declared, nil))
	assert.Empty(t, domain.MissingValues(declared,
		map[string]string{"admin_email": "ops@example"}))
	assert.Equal(t, []string{"admin_email"}, domain.MissingValues(declared,
		map[string]string{"admin_email": "   "}),
		"whitespace is not a value somebody chose")

	supplied, err := domain.ResolveParameters(declared,
		map[string]string{"admin_email": "ops@example"})
	require.NoError(t, err)
	assert.Equal(t, "ops@example", supplied["admin_email"])
}

func TestAnUndeclaredParameterIsRefusedByName(t *testing.T) {
	declared := map[string]domain.ParameterSpec{
		"http_port": {Type: domain.ParamPort, Default: "18080"},
		"log_level": {Type: domain.ParamEnum, Values: []string{"info"}, Default: "info"},
	}

	_, err := domain.ResolveParameters(declared, map[string]string{"htpp_port": "9000"})
	require.Error(t, err)

	e := domain.AsError(err)
	assert.Contains(t, e.Message, "htpp_port")
	// The hint lists what *is* declared. A typo with no list is a puzzle.
	assert.Contains(t, e.Hint, "http_port")
	assert.Contains(t, e.Hint, "log_level")
}

func TestARefusedValueNamesWhatIsAccepted(t *testing.T) {
	declared := map[string]domain.ParameterSpec{
		"log_level": {
			Type: domain.ParamEnum, Values: []string{"debug", "info", "warn"},
			Default: "info", Description: "Application log verbosity",
		},
	}

	_, err := domain.ResolveParameters(declared, map[string]string{"log_level": "chatty"})
	require.Error(t, err)

	hint := domain.AsError(err).Hint
	for _, want := range []string{"debug", "info", "warn", "Application log verbosity"} {
		assert.Contains(t, hint, want)
	}
}

// TestAnInvalidDefaultIsTheVendorsBug catches the manifest error that would
// otherwise surface on an operator's machine, during an apply they had no part
// in: a default that does not satisfy its own declaration.
func TestAnInvalidDefaultIsTheVendorsBug(t *testing.T) {
	m := validManifestWithParameters(map[string]domain.ParameterSpec{
		"http_port": {Type: domain.ParamPort, Default: "not-a-port"},
	})

	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Message, "parameters.http_port.default")
}

func TestParameterDeclarationsAreValidated(t *testing.T) {
	cases := map[string]struct {
		params map[string]domain.ParameterSpec
		field  string
	}{
		"a missing type": {
			map[string]domain.ParameterSpec{"x": {Default: "1"}}, "parameters.x.type",
		},
		"an unknown type": {
			map[string]domain.ParameterSpec{"x": {Type: "colour"}}, "parameters.x.type",
		},
		"an enum with no values": {
			map[string]domain.ParameterSpec{"x": {Type: domain.ParamEnum}}, "parameters.x.values",
		},
		"values on a non-enum": {
			map[string]domain.ParameterSpec{"x": {Type: domain.ParamPort, Values: []string{"a"}}},
			"parameters.x.values",
		},
		"a name that would not survive becoming a variable": {
			map[string]domain.ParameterSpec{"HTTP-Port": {Type: domain.ParamPort}}, "parameters.HTTP-Port",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := validManifestWithParameters(tc.params).Validate()
			require.Error(t, err)
			assert.Contains(t, domain.AsError(err).Message, tc.field)
		})
	}
}

func TestParseAssignments(t *testing.T) {
	got, err := domain.ParseAssignments([]string{"http_port=9000", "motd=hello=world"})
	require.NoError(t, err)
	// Split on the first `=` only, so a value may contain one.
	assert.Equal(t, map[string]string{"http_port": "9000", "motd": "hello=world"}, got)

	_, err = domain.ParseAssignments([]string{"http_port"})
	assert.Error(t, err, "an assignment with no `=` is a usage error, not a parameter named http_port")

	_, err = domain.ParseAssignments([]string{"http_port=1", "http_port=2"})
	assert.Error(t, err, "setting one parameter twice is ambiguous and must be refused")
}

// validManifestWithParameters is the smallest manifest that passes validation,
// so a test asserting a parameter error is not reading someone else's.
func validManifestWithParameters(params map[string]domain.ParameterSpec) *domain.Manifest {
	return &domain.Manifest{
		APIVersion: domain.APIVersionV1Alpha1,
		Kind:       domain.KindApplicationRelease,
		Metadata:   domain.Metadata{Name: "demo", Version: domain.MustParseVersion("1.0.0")},
		Providers:  domain.Providers{Runtime: domain.Provider{Name: "compose"}},
		Runtime:    domain.RuntimeSpec{Project: "demo", Files: []string{"compose/compose.yaml"}},
		Parameters: params,
		Images: map[string]domain.ImageSpec{
			"app": {Ref: "registry.example/demo/app@sha256:" + strings.Repeat("0", 63) + "1"},
		},
	}
}
