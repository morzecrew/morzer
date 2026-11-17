package domain

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every row of RFC 0059 §5.1: what the grammar accepts, spelled canonically,
// and each form it refuses, with why.
func TestAPortEntryIsAPublishedPortWithoutTheContainerSide(t *testing.T) {
	for entry, want := range map[string]string{
		"443":                   "443/tcp",
		":443":                  "443/tcp",
		" 443 ":                 "443/tcp",
		"8447/udp":              "8447/udp",
		"203.0.113.11:443":      "203.0.113.11:443/tcp",
		"0.0.0.0:53/udp":        "0.0.0.0:53/udp",
		"[2001:db8::1]:443/tcp": "[2001:db8::1]:443/tcp",
		"[::]:443":              "[::]:443/tcp",
		"65535":                 "65535/tcp",
	} {
		t.Run(entry, func(t *testing.T) {
			got, err := ParsePortEntry(entry)
			require.NoError(t, err)
			assert.Equal(t, want, got.String())
		})
	}
	for entry, why := range map[string]string{
		"8000-8010":          "a port range is not supported",
		"443/sctp":           "the protocol must be tcp or udp",
		"443/":               "the protocol must be tcp or udp",
		"example.com:443":    "not a host name",
		"2001:db8::1:443":    "an IPv6 address needs brackets",
		"[fe80::1%eth0]:443": "an address with a zone is not supported",
		"[203.0.113.1]:443":  "must be an IPv6 address",
		"[2001:db8::1]443":   "must be followed by :<port>",
		"0":                  "from 1 to 65535",
		"65536":              "from 1 to 65535",
		"+443":               "from 1 to 65535",
		"443x":               "from 1 to 65535",
	} {
		t.Run(entry, func(t *testing.T) {
			_, err := ParsePortEntry(entry)
			require.Error(t, err)
			assert.Contains(t, err.Error(), why)
		})
	}
}

// RFC 0059 decision 4: two claims meet only on one protocol and port, and only
// where their addresses overlap.
func TestTwoPortClaimsMeetOnlyWhereTheirAddressesOverlap(t *testing.T) {
	claim := func(entry string) PortRequirement {
		r, err := ParsePortEntry(entry)
		require.NoError(t, err)
		return r
	}
	for _, tc := range []struct {
		a, b string
		meet bool
	}{
		{"443", "443", true},
		{"443", "203.0.113.11:443", true},
		{"443", "[2001:db8::1]:443", true},
		{"203.0.113.11:443", "203.0.113.11:443", true},
		{"203.0.113.10:443", "203.0.113.11:443", false},
		{"0.0.0.0:443", "203.0.113.11:443", true},
		{"[::]:443", "[2001:db8::1]:443", true},
		{"0.0.0.0:443", "[2001:db8::1]:443", false},
		{"[::]:443", "203.0.113.11:443", false},
		{"443/tcp", "443/udp", false},
		{"443", "444", false},
	} {
		t.Run(tc.a+" "+tc.b, func(t *testing.T) {
			assert.Equal(t, tc.meet, claim(tc.a).Overlaps(claim(tc.b)))
			assert.Equal(t, tc.meet, claim(tc.b).Overlaps(claim(tc.a)), "the rule is symmetric")
		})
	}
}

// RFC 0059 §5.2: an entry can depend on the profiles an installation selects,
// and one that renders empty requires nothing. A health URL reads them too
// (decision 5).
func TestAPortEntryCanDependOnTheProfilesSelected(t *testing.T) {
	m := validManifest()
	m.Parameters = map[string]ParameterSpec{"kcp_port": {Type: ParamPort, Default: new("8447")}}
	m.Requirements.Ports = []PortSpec{
		"80",
		`{{ if has "mkcp" .Profiles }}{{ .Parameters.kcp_port }}/udp{{ end }}`,
		`{{ if has "xdns" .Profiles }}53/udp{{ end }}`,
	}
	m.Health.Checks = []HealthCheck{{Name: "api", Type: HealthHTTP,
		URL: `http://127.0.0.1/{{ index .Profiles 0 }}`}}
	params, err := ResolveParameters(m.Parameters, nil)
	require.NoError(t, err)

	got, err := m.ResolvePorts(params, []string{"base", "mkcp"})
	require.NoError(t, err)
	require.Len(t, got, 2, "the xdns entry rendered empty, and requires nothing")
	assert.Equal(t, "80/tcp", got[0].String())
	assert.Equal(t, "8447/udp", got[1].String())
	assert.Equal(t, []string{"kcp_port"}, got[1].Parameters, "a refusal names the parameter the entry reads")
	assert.Empty(t, got[0].Parameters)

	got, err = m.ResolvePorts(params, nil)
	require.NoError(t, err)
	assert.Len(t, got, 1, "no installation, as in `release verify`, selects nothing")

	checks, err := m.ResolveHealthChecks(params, []string{"base", "mkcp"})
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1/base", checks[0].URL, "the selection is in layering order")

	m.Requirements.Ports = []PortSpec{" "}
	assert.ErrorContains(t, m.Validate(), "requirements.ports[0]: is empty",
		"a literal empty entry is a mistake, not a requirement")
}

// The manifest's `has` answers as the configuration templates' does, argument
// order included.
func TestTheManifestsHasIsTheConfigurationTemplates(t *testing.T) {
	has, ok := templateFuncs["has"].(func(string, []string) bool)
	require.True(t, ok)
	assert.True(t, has("xdns", []string{"base", "xdns"}))
	assert.False(t, has("xdns", []string{"base"}))
	assert.False(t, has("xdns", []string{}))
}

// RFC 0059 §5.5: each new form needs the floor, and today's forms do not.
func TestTheNewPortGrammarNeedsItsFloor(t *testing.T) {
	below := func(m *Manifest) { m.Compatibility.MinManagerVersion = MustParseVersion("0.17.0") }
	for name, edit := range map[string]func(*Manifest){
		"a literal address":  func(m *Manifest) { m.Requirements.Ports = []PortSpec{"203.0.113.11:443"} },
		"a literal protocol": func(m *Manifest) { m.Requirements.Ports = []PortSpec{"8447/udp"} },
		"an empty address":   func(m *Manifest) { m.Requirements.Ports = []PortSpec{":443"} },
		"profiles in a port": func(m *Manifest) { m.Requirements.Ports = []PortSpec{"{{ index .Profiles 0 }}"} },
		"has in a port":      func(m *Manifest) { m.Requirements.Ports = []PortSpec{`{{ if has "x" .Profiles }}53{{ end }}`} },
		"an address beside":  func(m *Manifest) { m.Requirements.Ports = []PortSpec{"{{ .Parameters.address }}:443"} },
		"a protocol beside":  func(m *Manifest) { m.Requirements.Ports = []PortSpec{"{{ .Parameters.port }}/udp"} },
		"a bracket beside":   func(m *Manifest) { m.Requirements.Ports = []PortSpec{"[{{ .Parameters.address }}]:443"} },
		"profiles in a health": func(m *Manifest) {
			m.Health.Checks = []HealthCheck{{Name: "api", Type: HealthHTTP, URL: "http://x/{{ index .Profiles 0 }}"}}
		},
		"profiles from the root": func(m *Manifest) { m.Requirements.Ports = []PortSpec{"{{ if $.Profiles }}53{{ end }}"} },
		"profiles in a defined template": func(m *Manifest) {
			m.Requirements.Ports = []PortSpec{`{{ define "p" }}{{ if .Profiles }}53{{ end }}{{ end }}{{ template "p" . }}`}
		},
		"an address in a defined template": func(m *Manifest) {
			m.Requirements.Ports = []PortSpec{`{{ define "a" }}203.0.113.11:{{ end }}{{ template "a" }}{{ .Parameters.port }}`}
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := validManifest()
			edit(&m)
			below(&m)
			err := m.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "compatibility.min_manager_version: must be at least "+PortsMinManagerVersion)

			m.Compatibility.MinManagerVersion = MustParseVersion(PortsMinManagerVersion)
			assert.NotContains(t, errString(m.Validate()), "min_manager_version")
		})
	}
	for name, ports := range map[string][]PortSpec{
		"a literal port":          {"443", "18080"},
		"a lone parameter":        {"{{ .Parameters.port }}"},
		"digits beside an action": {"8{{ .Parameters.suffix }}"},
	} {
		t.Run(name, func(t *testing.T) {
			m := validManifest()
			m.Requirements.Ports = ports
			below(&m)
			assert.NotContains(t, errString(m.Validate()), "min_manager_version")
		})
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// The zero address is every address, and String says so by leaving it out.
func TestEveryAddressIsTheZeroAddress(t *testing.T) {
	r := PortRequirement{Port: 53, Protocol: PortUDP}
	assert.False(t, r.Address.IsValid())
	assert.Equal(t, "53/udp", r.String())
	r.Address = netip.MustParseAddr("2001:db8::1")
	assert.Equal(t, "[2001:db8::1]:53/udp", r.String())
}

// A template that reads, through `index`, a parameter the release does not
// declare is refused at load: `index` renders it empty, and an empty entry
// requires nothing. A field reference fails when it resolves.
func TestAPortTemplateReadingAnUndeclaredParameterIsRefused(t *testing.T) {
	for name, edit := range map[string]func(*Manifest){
		"in a port":     func(m *Manifest) { m.Requirements.Ports = []PortSpec{`{{ index .Parameters "kcp_prot" }}`} },
		"from the root": func(m *Manifest) { m.Requirements.Ports = []PortSpec{`{{ index $.Parameters "kcp_prot" }}`} },
		"in a health url": func(m *Manifest) {
			m.Health.Checks = []HealthCheck{{Name: "api", Type: HealthHTTP, URL: `http://x:{{ index .Parameters "kcp_prot" }}/`}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := validManifest()
			m.Parameters = map[string]ParameterSpec{"kcp_port": {Type: ParamPort, Default: new("8447")}}
			edit(&m)
			assert.ErrorContains(t, m.Validate(), `reads parameter "kcp_prot", which the release does not declare`)
		})
	}
	m := validManifest()
	m.Parameters = map[string]ParameterSpec{"kcp_port": {Type: ParamPort, Default: new("8447")}}
	m.Requirements.Ports = []PortSpec{`{{ index .Parameters "kcp_port" }}`, "{{ $.Parameters.kcp_port }}"}
	require.NoError(t, m.Validate())
	params, err := ResolveParameters(m.Parameters, nil)
	require.NoError(t, err)
	got, err := m.ResolvePorts(params, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"kcp_port"}, got[0].Parameters)
	assert.Equal(t, []string{"kcp_port"}, got[1].Parameters)

	m.Requirements.Ports = []PortSpec{"{{ $.Parameters.kcp_prot }}"}
	require.NoError(t, m.Validate(), "a field reference is left to resolution")
	_, err = m.ResolvePorts(params, nil)
	assert.Error(t, err, "and fails there")
}
