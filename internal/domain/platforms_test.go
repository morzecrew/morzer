package domain

import (
	"errors"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The platforms a release and its profiles are made for, RFC 0055 §5.1.

// manifestForPlatforms is the overlays' release declaring platforms, at the
// manager version that reads them.
func manifestForPlatforms(release []Platform, profiles map[string][]Platform) Manifest {
	m := manifestWithCombinable()
	m.Compatibility.MinManagerVersion = MustParseVersion(PlatformsMinManagerVersion)
	m.Requirements.Platforms = release
	decl := m.Runtimes[LegacyRuntimeName]
	decl.Platforms = profiles
	m.Runtimes[LegacyRuntimeName] = decl
	return m
}

// Absent is Linux alone, so every release published before the key is refused
// on a Mac, and the refusal names the vendor's step (decision 2, §9).
func TestAReleaseThatDeclaresNoPlatformsRunsOnLinuxAlone(t *testing.T) {
	req := validManifest().Requirements
	assert.Equal(t, []Platform{PlatformLinux}, req.RunsOn())
	require.NoError(t, req.PlatformRefusal("linux", "release 1.2.0"))

	err := req.PlatformRefusal("darwin", "release 1.2.0")
	require.ErrorIs(t, err, ErrIncompatible)
	assert.Equal(t, "release 1.2.0 is not made for darwin", AsError(err).Message)
	assert.Equal(t, "it declares no requirements.platforms, which means linux alone. "+
		"Its vendor adds darwin to that list once the release runs there", AsError(err).Hint)

	req.Platforms = []Platform{PlatformLinux, PlatformDarwin}
	require.NoError(t, req.PlatformRefusal("darwin", "release 1.2.0"))
	req.Platforms = []Platform{PlatformDarwin}
	err = req.PlatformRefusal("linux", "release 1.2.0")
	require.ErrorIs(t, err, ErrIncompatible)
	assert.Contains(t, AsError(err).Hint, "its requirements.platforms lists darwin")
}

// A profile narrowed to Linux is refused on darwin wherever a selection is
// checked, and one not narrowed runs wherever the release does (decision 3).
func TestAProfileIsRefusedOnAPlatformItIsNotMadeFor(t *testing.T) {
	decl := manifestForPlatforms([]Platform{PlatformLinux, PlatformDarwin},
		map[string][]Platform{"xdns": {PlatformLinux}}).Runtimes[LegacyRuntimeName]

	got, err := decl.Selection([]string{"xdns", "embedded"}, "linux")
	require.NoError(t, err)
	assert.Equal(t, []string{"embedded", "xdns"}, got)
	_, err = decl.Selection([]string{"embedded", "xhttp-reality"}, "darwin")
	require.NoError(t, err)

	_, err = decl.Selection([]string{"embedded", "xdns"}, "darwin")
	require.ErrorIs(t, err, ErrValidation)
	assert.Equal(t, `profile "xdns" does not run on darwin`, AsError(err).Message)
	assert.Equal(t, "this release runs it on linux only", AsError(err).Hint)

	// A question offers only what this platform accepts.
	m := manifestForPlatforms(nil, map[string][]Platform{"xdns": {PlatformLinux}})
	assert.Equal(t, []string{"embedded", "external-db", "xdns", "xhttp-reality"}, m.ProfileNamesOn("linux"))
	assert.Equal(t, []string{"embedded", "external-db", "xhttp-reality"}, m.ProfileNamesOn("darwin"))

	// Which files a selection layers does not depend on where it runs.
	files, err := decl.FilesFor([]string{"xdns"})
	require.NoError(t, err)
	assert.Contains(t, files, "compose/xdns.yaml")
}

func TestPlatformsAreValidated(t *testing.T) {
	problem := func(m Manifest) string {
		t.Helper()
		err := m.Validate()
		if err == nil {
			return ""
		}
		return err.Error()
	}
	both := []Platform{PlatformLinux, PlatformDarwin}

	assert.Empty(t, problem(manifestForPlatforms(both, map[string][]Platform{"xdns": {PlatformLinux}})))
	assert.Empty(t, problem(manifestForPlatforms(nil, map[string][]Platform{"xdns": {PlatformLinux}})),
		"a profile may narrow a release that declares no platforms to Linux")

	for _, tc := range []struct {
		name     string
		release  []Platform
		profiles map[string][]Platform
		want     string
	}{
		{"an empty list", []Platform{}, nil, "requirements.platforms: must list at least one platform"},
		{"a value it does not take", []Platform{PlatformLinux, "windows"}, nil,
			`requirements.platforms: "windows" is not a platform: it takes linux, darwin`},
		{"a value twice", []Platform{PlatformDarwin, PlatformDarwin}, nil,
			"requirements.platforms: lists darwin twice"},
		{"a profile's empty list", both, map[string][]Platform{"xdns": {}},
			"runtimes.compose.platforms.xdns: must list at least one platform"},
		{"a profile not declared", both, map[string][]Platform{"tun": {PlatformLinux}},
			`runtimes.compose.platforms.tun: names "tun", which runtimes.compose.profiles does not declare`},
		{"a profile on a platform the release is not made for", nil,
			map[string][]Platform{"xdns": {PlatformDarwin}},
			"runtimes.compose.platforms.xdns: lists darwin, which requirements.platforms does not"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Contains(t, problem(manifestForPlatforms(tc.release, tc.profiles)), tc.want)
		})
	}

	// Either key under an older floor, which a 0.17 manager refuses as an
	// unknown field (decision 15, moved to 0.18.0).
	old := manifestForPlatforms(both, nil)
	old.Compatibility.MinManagerVersion = MustParseVersion("0.17.1")
	assert.Contains(t, problem(old), "compatibility.min_manager_version: must be at least 0.18.0, "+
		"because the release declares platforms")
	old = manifestForPlatforms(nil, map[string][]Platform{"xdns": {PlatformLinux}})
	old.Compatibility.MinManagerVersion = MustParseVersion("0.17.1")
	assert.Contains(t, problem(old), "must be at least 0.18.0")
}

// `platforms: []` reaches validation as a list written empty, not as an
// absent one, which would quietly mean Linux alone.
func TestAnEmptyPlatformsListIsAWrittenOne(t *testing.T) {
	var req Requirements
	require.NoError(t, yaml.UnmarshalWithOptions([]byte("platforms: []\n"), &req,
		yaml.Strict(), yaml.DisallowUnknownField()))
	m := validManifest()
	m.Requirements = req
	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requirements.platforms: must list at least one platform")
}

// `os: [darwin]` reads like an opt-in and is refused as the mapping it is not
// (§9), so a vendor who writes it finds out at `release verify`.
func TestDarwinUnderOSIsNotAnOptIn(t *testing.T) {
	var req Requirements
	err := yaml.UnmarshalWithOptions([]byte("os: [darwin]\n"), &req,
		yaml.Strict(), yaml.DisallowUnknownField())
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrValidation))
}
