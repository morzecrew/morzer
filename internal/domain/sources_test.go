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
	for _, tc := range []struct{ name, raw, want string }{
		{"a host", "https://downloads.vendor.example", "downloads.vendor.example"},
		{"upper case and the default port", "https://Downloads.Vendor.Example:443/", "downloads.vendor.example"},
		{"another port", "https://downloads.vendor.example:8443", "downloads.vendor.example:8443"},
		{"the default port with a leading zero", "https://downloads.vendor.example:0443", "downloads.vendor.example"},
		{"another port with a leading zero", "https://downloads.vendor.example:08443", "downloads.vendor.example:8443"},
		{"IPv6", "https://[2001:db8::1]", "[2001:db8::1]"},
		{"IPv6 with a port", "https://[2001:db8::1]:8443", "[2001:db8::1]:8443"},
		{"scoped IPv6", "https://[fe80::1%25eth0]", "[fe80::1%25eth0]"},
		{"scoped IPv6 with a port", "https://[fe80::1%25eth0]:8443", "[fe80::1%25eth0]:8443"},
		// An IPv6 address has many spellings, and one is the address's own.
		{"IPv6 written in full", "https://[2001:0DB8:0:0:0:0:0:1]", "[2001:db8::1]"},
		{"IPv6 with leading zeros and a port", "https://[2001:0db8::0001]:8443", "[2001:db8::1]:8443"},
		{"scoped IPv6 written in full", "https://[fe80:0:0:0:0:0:0:1%25eth0]", "[fe80::1%25eth0]"},
		{"a path", "https://downloads.vendor.example:443/path", ""},
		{"plain http", "http://downloads.vendor.example", ""},
		{"a password", "https://user:s3cr3t@downloads.vendor.example", ""},
		{"a query", "https://downloads.vendor.example/?a=b", ""},
		{"a port out of range", "https://downloads.vendor.example:99999", ""},
		{"no host", "https://", ""},
		{"a password and a bad port", "https://user:s3cr3t@downloads.vendor.example:bad", ""},
		{"an opaque URL with a password", "https:user:s3cr3t@downloads.vendor.example", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.ParseSourceURL(tc.raw)
			if tc.want == "" {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), "s3cr3t", "a refusal quoted the password")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.NoError(t, domain.HTTPSSource{Host: got, Auth: domain.SourceAuthBearer, Secret: "t"}.Validate(),
				"an authority add writes is refused when read back")
		})
	}
}

func TestASourceEntryReadFromStateIsChecked(t *testing.T) {
	good := domain.HTTPSSource{Host: "downloads.vendor.example", Auth: domain.SourceAuthBearer, Secret: "vendor_token"}
	require.NoError(t, good.Validate())

	for _, tc := range []struct {
		name   string
		source domain.HTTPSSource
	}{
		{"no host", domain.HTTPSSource{Auth: domain.SourceAuthBearer, Secret: "t"}},
		{"upper case host", domain.HTTPSSource{Host: "Downloads.Vendor.Example", Auth: domain.SourceAuthBearer, Secret: "t"}},
		{"default port", domain.HTTPSSource{Host: "downloads.vendor.example:443", Auth: domain.SourceAuthBearer, Secret: "t"}},
		{"a leading zero", domain.HTTPSSource{Host: "downloads.vendor.example:08443", Auth: domain.SourceAuthBearer, Secret: "t"}},
		{"IPv6 not in its own spelling", domain.HTTPSSource{Host: "[2001:0db8::1]", Auth: domain.SourceAuthBearer, Secret: "t"}},
		{"a port out of range", domain.HTTPSSource{Host: "downloads.vendor.example:99999", Auth: domain.SourceAuthBearer, Secret: "t"}},
		{"a path", domain.HTTPSSource{Host: "downloads.vendor.example/x", Auth: domain.SourceAuthBearer, Secret: "t"}},
		{"unknown auth", domain.HTTPSSource{Host: "downloads.vendor.example", Auth: "digest", Secret: "t"}},
		{"bad secret name", domain.HTTPSSource{Host: "downloads.vendor.example", Auth: domain.SourceAuthBasic, Secret: "a/b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Error(t, tc.source.Validate())
		})
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
