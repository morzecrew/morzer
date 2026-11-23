package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

// Two spellings of one server are one entry: the port both sides default to
// is dropped, and the host is compared lower-cased (RFC 0038 decision 7).
func TestASourceIsNamedByItsAuthority(t *testing.T) {
	for raw, want := range map[string]string{
		"https://downloads.vendor.example":           "downloads.vendor.example",
		"https://Downloads.Vendor.Example:443/":      "downloads.vendor.example",
		"https://downloads.vendor.example:8443":      "downloads.vendor.example:8443",
		"https://[2001:db8::1]":                      "[2001:db8::1]",
		"https://[2001:db8::1]:8443":                 "[2001:db8::1]:8443",
		"https://downloads.vendor.example:443/path":  "",
		"http://downloads.vendor.example":            "",
		"https://user:pass@downloads.vendor.example": "",
		"https://downloads.vendor.example/?a=b":      "",
		"https://downloads.vendor.example:99999":     "",
		"https://":                                   "",
	} {
		got, err := domain.ParseSourceURL(raw)
		if want == "" {
			require.Error(t, err, raw)
			assert.NotContains(t, err.Error(), "pass", "a refusal quoted the password")
			continue
		}
		require.NoError(t, err, raw)
		assert.Equal(t, want, got, raw)
		assert.NoError(t, domain.HTTPSSource{Host: got, Auth: domain.SourceAuthBearer, Secret: "t"}.Validate(),
			"an authority add writes is refused when read back: %s", raw)
	}
}

func TestASourceEntryReadFromStateIsChecked(t *testing.T) {
	good := domain.HTTPSSource{Host: "downloads.vendor.example", Auth: domain.SourceAuthBearer, Secret: "vendor_token"}
	require.NoError(t, good.Validate())

	for name, s := range map[string]domain.HTTPSSource{
		"no host":         {Auth: domain.SourceAuthBearer, Secret: "t"},
		"upper case host": {Host: "Downloads.Vendor.Example", Auth: domain.SourceAuthBearer, Secret: "t"},
		"default port":    {Host: "downloads.vendor.example:443", Auth: domain.SourceAuthBearer, Secret: "t"},
		"a path":          {Host: "downloads.vendor.example/x", Auth: domain.SourceAuthBearer, Secret: "t"},
		"unknown auth":    {Host: "downloads.vendor.example", Auth: "digest", Secret: "t"},
		"bad secret name": {Host: "downloads.vendor.example", Auth: domain.SourceAuthBasic, Secret: "a/b"},
	} {
		assert.Error(t, s.Validate(), name)
	}
}

func TestAnInstallationRefusesTwoEntriesForOneServer(t *testing.T) {
	inst := domain.Installation{
		SchemaVersion: domain.InstallationSchemaVersion, ID: "i", Product: "demo",
		Sources: domain.SourcesConfig{HTTPS: []domain.HTTPSSource{
			{Host: "downloads.vendor.example", Auth: domain.SourceAuthBearer, Secret: "a"},
			{Host: "downloads.vendor.example", Auth: domain.SourceAuthBasic, Secret: "b"},
		}},
	}
	err := inst.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "second entry for downloads.vendor.example")
}

func TestInitNamesASourceSecretAfterItsServer(t *testing.T) {
	assert.Equal(t, "source_downloads_vendor_example", domain.DefaultSourceSecret("downloads.vendor.example"))
	assert.Equal(t, "source_downloads_vendor_example_8443", domain.DefaultSourceSecret("downloads.vendor.example:8443"))
	assert.True(t, domain.ValidSecretName(domain.DefaultSourceSecret("[2001:db8::1]:8443")))
}
