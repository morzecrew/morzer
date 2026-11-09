package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Several profiles per installation, RFC 0053.

// overlays is a runtime with two alternatives and two overlays that may run
// beside one of them, the shape §5.1 writes.
func overlays() RuntimeDecl {
	return RuntimeDecl{
		Files: []string{"compose/compose.yaml"},
		Profiles: map[string][]string{
			"embedded":      {"compose/embedded.yaml"},
			"external-db":   {"compose/external-db.yaml"},
			"xdns":          {"compose/xdns.yaml", "compose/shared.yaml"},
			"xhttp-reality": {"compose/xhttp-reality.yaml", "compose/shared.yaml"},
		},
		Combinable: []string{"xdns", "xhttp-reality"},
	}
}

// manifestWithCombinable is a release declaring the overlays, at the manager
// version that reads `combinable` (decision 11).
func manifestWithCombinable() Manifest {
	m := validManifest()
	m.Compatibility.MinManagerVersion = MustParseVersion(CombinableProfilesMinManagerVersion)
	decl := overlays()
	decl.Options = m.Runtimes[LegacyRuntimeName].Options
	m.Runtimes[LegacyRuntimeName] = decl
	return m
}

// Every row of §5.2's table, each asserting which refusal (decision 4).
func TestASelectionIsCheckedAgainstTheDeclaration(t *testing.T) {
	for _, tc := range []struct {
		name     string
		selected []string
		want     []string
		refusal  string
		hint     string
	}{
		{"empty is the base files", nil, nil, "", ""},
		{"one outside combinable", []string{"embedded"}, []string{"embedded"}, "", ""},
		{"the overlays in the release's order, whatever order they are given in",
			[]string{"xhttp-reality", "xdns"}, []string{"xdns", "xhttp-reality"}, "", ""},
		{"one alternative first, then the overlays",
			[]string{"xhttp-reality", "external-db", "xdns"}, []string{"external-db", "xdns", "xhttp-reality"}, "", ""},
		{"a name not declared", []string{"xdns", "ipv6"}, nil,
			`unknown deployment profile "ipv6"`, "profiles declared by this release: embedded, external-db, xdns, xhttp-reality"},
		{"a name twice", []string{"xdns", "xdns"}, nil, `profile "xdns" is selected twice`, ""},
		{"an empty entry", []string{"xdns", ""}, nil, "an empty profile name in the selection", ""},
		{"two names outside combinable", []string{"embedded", "external-db"}, nil,
			"profiles embedded and external-db cannot be deployed together",
			"this release lets these run beside one other profile: xdns, xhttp-reality"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := overlays().Selection(tc.selected)
			if tc.refusal == "" {
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.refusal)
			var domErr *Error
			require.ErrorAs(t, err, &domErr)
			assert.Contains(t, domErr.Hint, tc.hint)
		})
	}
}

// A release that declares nothing combinable runs one profile at a time, which
// is every bundle written before RFC 0053 (decision 1), and the release's
// version is in the hint where it is known.
func TestWithNothingCombinableASecondProfileIsRefused(t *testing.T) {
	decl := overlays()
	decl.Combinable = nil
	m := validManifest()
	decl.Options = m.Runtimes[LegacyRuntimeName].Options
	m.Runtimes[LegacyRuntimeName] = decl
	rel := Release{Manifest: m, Root: "/opt/demo/releases/1.2.0"}

	_, err := rel.Selection(LegacyRuntimeName, []string{"xdns", "xhttp-reality"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "profiles xdns and xhttp-reality cannot be deployed together")
	var domErr *Error
	require.ErrorAs(t, err, &domErr)
	assert.Equal(t, "release 1.2.0 declares no profile combinable", domErr.Hint)
}

// Layering follows the selection's order, and a file two profiles list, or the
// base lists, is passed once (decision 3).
func TestTheFilesOfASelectionAreLayeredInTheReleasesOrder(t *testing.T) {
	files, err := overlays().FilesFor([]string{"xhttp-reality", "embedded", "xdns"})

	require.NoError(t, err)
	assert.Equal(t, []string{
		"compose/compose.yaml",
		"compose/embedded.yaml",
		"compose/xdns.yaml", "compose/shared.yaml",
		"compose/xhttp-reality.yaml",
	}, files)

	reordered := overlays()
	reordered.Combinable = []string{"xhttp-reality", "xdns"}
	files, err = reordered.FilesFor([]string{"xdns", "xhttp-reality"})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"compose/compose.yaml",
		"compose/xhttp-reality.yaml", "compose/shared.yaml",
		"compose/xdns.yaml",
	}, files, "a release that reorders combinable changes the layering")
}

func TestCombinableIsValidatedByFieldPath(t *testing.T) {
	valid := manifestWithCombinable()
	require.NoError(t, valid.Validate())

	for _, tc := range []struct {
		name       string
		combinable []string
		want       string
	}{
		{"a name the runtime does not declare", []string{"xdns", "ipv6"},
			`runtimes.compose.combinable[1]: names "ipv6", which runtimes.compose.profiles does not declare`},
		{"a name listed twice", []string{"xdns", "xdns"}, `runtimes.compose.combinable[1]: lists "xdns" twice`},
		{"a name a selection cannot carry", []string{"xdns", "a,b"},
			`runtimes.compose.combinable[1]: "a,b" is not a usable name for a combinable profile`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := manifestWithCombinable()
			decl := m.Runtimes[LegacyRuntimeName]
			decl.Combinable = tc.combinable
			decl.Profiles["a,b"] = []string{"compose/ab.yaml"}
			m.Runtimes[LegacyRuntimeName] = decl
			err := m.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// An older manager refuses the field as unknown, so a release using it says
// which manager it needs (decision 11, RFC 0018 decision 1).
func TestCombinableNeedsTheManagerThatReadsIt(t *testing.T) {
	m := manifestWithCombinable()
	m.Compatibility.MinManagerVersion = MustParseVersion(RestartableRestoreMinManagerVersion)

	err := m.Validate()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "compatibility.min_manager_version: must be at least "+
		CombinableProfilesMinManagerVersion+", because a runtime declares combinable profiles")
}

// Only names in `combinable` are held to the pattern, so a profile name that
// loads today still loads (decision 11).
func TestAProfileNameThatLoadsTodayStillLoads(t *testing.T) {
	m := validManifest()
	decl := m.Runtimes[LegacyRuntimeName]
	decl.Profiles = map[string][]string{"Edge Case,1": {"compose/edge.yaml"}}
	m.Runtimes[LegacyRuntimeName] = decl

	require.NoError(t, m.Validate())
}

// One runtime's own order, exactly; several runtimes in sorted order, a name
// already seen skipped (decision 18).
func TestCombinableNamesAcrossRuntimes(t *testing.T) {
	m := manifestWithCombinable()
	assert.Equal(t, []string{"xdns", "xhttp-reality"}, m.CombinableNames())

	m.Runtimes["quadlet"] = RuntimeDecl{
		Files:      []string{"app.container"},
		Profiles:   map[string][]string{"ipv6": {"ipv6.container"}, "xdns": {"xdns.container"}},
		Combinable: []string{"ipv6", "xdns"},
	}
	assert.Equal(t, []string{"xdns", "xhttp-reality", "ipv6"}, m.CombinableNames())
}

func TestJoinAnd(t *testing.T) {
	assert.Empty(t, JoinAnd(nil))
	assert.Equal(t, "a", JoinAnd([]string{"a"}))
	assert.Equal(t, "a and b", JoinAnd([]string{"a", "b"}))
	assert.Equal(t, "a, b and c", JoinAnd([]string{"a", "b", "c"}))
}
