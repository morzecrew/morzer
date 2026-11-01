package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestValidateDomains is what `config set installation.domains` accepts: a host
// name or an https:// URL, the form PublicURL already reads, and nothing a
// template would render as a broken address.
func TestValidateDomains(t *testing.T) {
	for _, ok := range [][]string{
		{"demo.example"},
		{"demo.example", "www.demo.example"},
		{"localhost"},
		{"10.0.0.7"},
		{"https://demo.example"},
		{"https://demo.example:8443/app"},
		{"https://[::1]:8443"},
	} {
		assert.NoError(t, ValidateDomains(ok), "%q", ok)
	}

	for name, bad := range map[string][]string{
		"an empty entry":                   {"demo.example", ""},
		"a duplicate":                      {"demo.example", "demo.example"},
		"a duplicate in another case":      {"demo.example", "Demo.Example"},
		"a scheme other than https":        {"http://demo.example"},
		"a port without the URL form":      {"demo.example:8443"},
		"a path without the URL form":      {"demo.example/app"},
		"a space":                          {"demo example"},
		"a label starting with a hyphen":   {"-demo.example"},
		"an empty label":                   {"demo..example"},
		"a URL with no host":               {"https:///app"},
		"a URL carrying a user name":       {"https://admin@demo.example"},
		"a label longer than 63":           {"a234567890123456789012345678901234567890123456789012345678901234.example"},
		"an underscore, not a host letter": {"demo_app.example"},
	} {
		assert.Error(t, ValidateDomains(bad), name)
	}
}
