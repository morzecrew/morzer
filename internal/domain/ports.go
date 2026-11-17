package domain

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// A `requirements.ports` entry is `[address:]port[/protocol]`, the spelling a
// Compose short-syntax published port uses without its container side (RFC
// 0059 §5.1): "443", "8447/udp", "203.0.113.11:443", ":443",
// "[2001:db8::1]:443/tcp". An empty address, like a missing one, is every
// address, as Compose reads ":443:443" (decision 2).

// PortsMinManagerVersion is the manager release that reads an entry with an
// address or a protocol, and `.Profiles` and `has` in a manifest's templates
// (RFC 0059 decisions 8 and 9). An older manager refuses each of them anyway;
// the floor makes that one message naming the version.
const PortsMinManagerVersion = "0.18.0"

// PortProtocol is the transport a port requirement names.
type PortProtocol string

const (
	PortTCP PortProtocol = "tcp"
	PortUDP PortProtocol = "udp"
)

// PortRequirement is one port a release needs, resolved.
type PortRequirement struct {
	// Address is the zero netip.Addr for every address.
	Address  netip.Addr
	Port     uint16
	Protocol PortProtocol

	// Parameters are those the entry's template names, so a refusal can
	// say which one to change (logs/ports-by-address.md).
	Parameters []string
}

// String is the entry's canonical spelling, used in every message:
// "443/tcp", "203.0.113.11:443/tcp", "[2001:db8::1]:53/udp".
func (r PortRequirement) String() string {
	port := strconv.Itoa(int(r.Port)) + "/" + string(r.Protocol)
	switch {
	case !r.Address.IsValid():
		return port
	case r.Address.Is6():
		return "[" + r.Address.String() + "]:" + port
	default:
		return r.Address.String() + ":" + port
	}
}

// Overlaps says whether two claims on the same protocol and port would meet:
// either is every address, they are one address, or one is the unspecified
// address of the other's family (RFC 0059 decision 4).
func (r PortRequirement) Overlaps(o PortRequirement) bool {
	a, b := r.Address, o.Address
	switch {
	case r.Protocol != o.Protocol || r.Port != o.Port:
		return false
	case !a.IsValid() || !b.IsValid() || a == b:
		return true
	}
	return a.Is4() == b.Is4() && (a.IsUnspecified() || b.IsUnspecified())
}

// ParsePortEntry reads one entry in the grammar above.
func ParsePortEntry(text string) (PortRequirement, error) {
	text = strings.TrimSpace(text)
	r := PortRequirement{Protocol: PortTCP}

	rest := text
	if i := strings.LastIndex(rest, "/"); i >= 0 {
		switch PortProtocol(rest[i+1:]) {
		case PortTCP, PortUDP:
			r.Protocol = PortProtocol(rest[i+1:])
		default:
			return r, errors.New("the protocol must be tcp or udp")
		}
		rest = rest[:i]
	}

	port := rest
	if strings.HasPrefix(rest, "[") {
		end := strings.Index(rest, "]:")
		if end < 0 {
			return r, errors.New("a bracketed IPv6 address must be followed by :<port>")
		}
		addr, err := netip.ParseAddr(rest[1:end])
		if err != nil || !addr.Is6() {
			return r, errors.New("the address in brackets must be an IPv6 address")
		}
		// An IPv4-mapped address is its IPv4 address: it binds and
		// overlaps as one.
		r.Address, port = addr.Unmap(), rest[end+2:]
	} else if i := strings.LastIndex(rest, ":"); i >= 0 {
		if strings.Contains(rest[:i], ":") {
			return r, errors.New("an IPv6 address needs brackets, as in [2001:db8::1]:443")
		}
		if host := rest[:i]; host != "" {
			addr, err := netip.ParseAddr(host)
			if err != nil || !addr.Is4() {
				return r, errors.New("the address must be an IP address, not a host name")
			}
			r.Address = addr
		}
		port = rest[i+1:]
	}
	if r.Address.Zone() != "" {
		return r, errors.New("an address with a zone is not supported")
	}

	if strings.Contains(port, "-") {
		return r, errors.New("a port range is not supported; list each port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || strings.TrimLeft(port, "0123456789") != "" {
		return r, errors.New("the port must be a number from 1 to 65535")
	}
	r.Port = uint16(n)
	return r, nil
}

// newPortsGrammar names the first use of what RFC 0059 added, or "": a literal
// entry with an address or a protocol, `.Profiles` or `has` in either templated
// field, or a templated entry whose text outside its actions is more than
// digits (decision 8). A lone `{{ .Parameters.port }}`, or
// `8{{ .Parameters.suffix }}`, resolves today as it will then, and needs no
// floor.
func (m *Manifest) newPortsGrammar() string {
	for i, raw := range m.Requirements.Ports {
		field := fmt.Sprintf("requirements.ports[%d]", i)
		text := strings.TrimSpace(string(raw))
		if !strings.Contains(text, "{{") {
			if strings.Trim(text, "0123456789") != "" {
				return fmt.Sprintf("%s %q names an address or a protocol", field, text)
			}
			continue
		}
		use := readTemplate(field, text)
		switch {
		case use.profiles:
			return field + " reads .Profiles or has"
		case strings.ContainsAny(use.text, ":/["):
			return fmt.Sprintf("%s writes %q beside its template", field, strings.TrimSpace(use.text))
		}
	}
	for i, check := range m.Health.Checks {
		field := fmt.Sprintf("health.checks[%d].url", i)
		if check.Type == HealthHTTP && strings.Contains(check.URL, "{{") && readTemplate(field, check.URL).profiles {
			return field + " reads .Profiles or has"
		}
	}
	return ""
}

// checkTemplateParameters refuses a templated port entry or health URL that
// reads, through `index`, a parameter the release does not declare. A field
// reference fails at resolution, but `index .Parameters "name"` renders empty,
// and an empty entry requires nothing.
func (m *Manifest) checkTemplateParameters(v *validationErrors) {
	check := func(field, text string) {
		if !strings.Contains(text, "{{") {
			return
		}
		for _, name := range readTemplate(field, text).indexed {
			if _, ok := m.Parameters[name]; !ok {
				v.add(field, "reads parameter %q, which the release does not declare", name)
			}
		}
	}
	for i, raw := range m.Requirements.Ports {
		check(fmt.Sprintf("requirements.ports[%d]", i), string(raw))
	}
	for i, c := range m.Health.Checks {
		if c.Type == HealthHTTP {
			check(fmt.Sprintf("health.checks[%d].url", i), c.URL)
		}
	}
}
