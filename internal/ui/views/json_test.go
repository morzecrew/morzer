package views_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui/views"
)

// The `--json` contract, pinned where the refactor could have moved it.
//
// Routing every report through one renderer meant giving four commands a named
// type in place of the `map[string]any` they built inline, because the registry
// dispatches on type and a map would claim every other map-shaped report in the
// program. Each conversion is a chance to rename a key, drop one, or add one —
// and `--json` is what scripts read, so a presentation refactor that moved it
// would be a breaking change wearing a refactor's clothes.
//
// Written as the literal object each command used to build. Comparing against
// the struct's own tags would only assert that the struct agrees with itself.

func TestVersionKeepsItsPublishedKeys(t *testing.T) {
	got := marshal(t, views.Version{
		Version:              "1.4.0",
		Commit:               "abc1234",
		Built:                "2026-08-11T00:00:00Z",
		SupportedAPIVersions: []string{"selfhost/v1alpha1"},
	})

	require.Equal(t, map[string]any{
		"version":                "1.4.0",
		"commit":                 "abc1234",
		"built":                  "2026-08-11T00:00:00Z",
		"supported_api_versions": []any{"selfhost/v1alpha1"},
	}, got)
}

// TestVersionKeepsEveryKeyEvenWhenEmpty.
//
// The map this replaced had no omitempty, because it had no tags at all: a
// development build with no commit still answered `"commit": ""`. A struct is
// where omitempty gets added by habit, and `jq -e .commit` would start failing
// on exactly the builds a developer runs.
func TestVersionKeepsEveryKeyEvenWhenEmpty(t *testing.T) {
	got := marshal(t, views.Version{Version: "dev"})

	for _, key := range []string{"version", "commit", "built", "supported_api_versions"} {
		require.Containsf(t, got, key, "%q disappeared when it was empty", key)
	}
}

func TestVerifiedKeepsItsPublishedKeys(t *testing.T) {
	version, err := domain.ParseVersion("1.4.0")
	require.NoError(t, err)

	got := marshal(t, views.Verified{
		Valid: true, Name: "demo", VersionInfo: version,
		Digest: "sha256:abc", RenderCheck: true,
	})

	require.Equal(t, map[string]any{
		"valid":        true,
		"name":         "demo",
		"version":      "1.4.0",
		"digest":       "sha256:abc",
		"render_check": true,
	}, got)
}

func TestBuiltKeepsItsPublishedKeys(t *testing.T) {
	version, err := domain.ParseVersion("1.4.0")
	require.NoError(t, err)

	got := marshal(t, views.Built{
		Root: "/opt/demo/releases/1.4.0", VersionInfo: version,
		Digest: "sha256:abc", Name: "demo",
	})

	// Name is new to the rendering and deliberately absent here: it was not
	// in the map, and a refactor does not get to add a key either.
	require.Equal(t, map[string]any{
		"root":    "/opt/demo/releases/1.4.0",
		"version": "1.4.0",
		"digest":  "sha256:abc",
	}, got)
}

func TestKeyPairKeepsItsPublishedKeys(t *testing.T) {
	got := marshal(t, views.KeyPair{PublicKey: "age1abc", Path: "/root/demo-recovery.key"})

	require.Equal(t, map[string]any{
		"public_key": "age1abc",
		"path":       "/root/demo-recovery.key",
	}, got)
}

// TestVerboseIsInvisibleToJSON.
//
// `--verbose` selects a second view of the same report. The wrapper embeds the
// report, so its fields are promoted and the encoding is unchanged — which is
// the whole reason it is a wrapper rather than a field. If it ever stops being
// embedded, `doctor --json --verbose` grows a nesting level and every script
// reading `.data.results` breaks.
func TestVerboseIsInvisibleToJSON(t *testing.T) {
	report, ok := doctorFixtures()[1].value.(ops.DoctorReport)
	require.True(t, ok, "the mixed fixture is no longer a DoctorReport")

	plain, err := json.Marshal(report)
	require.NoError(t, err)
	wrapped, err := json.Marshal(views.Verbose{DoctorReport: report})
	require.NoError(t, err)

	require.JSONEq(t, string(plain), string(wrapped))
}

// TestTheListingIsOneArrayWhicheverViewDrewIt.
//
// `--status` selects a second view of the same listing, the way `--verbose`
// does above, and for the same reason: whether an operator asked for a Docker
// call is a presentation choice, and a flag inside the report would travel
// through the lifecycle layer into the machine contract. A named slice type
// encodes exactly as its slice does, so `morzer ls --json` is one array either
// way -- with a `services` key per row when it was asked for and none when it
// was not.
func TestTheListingIsOneArrayWhicheverViewDrewIt(t *testing.T) {
	entries := []ops.InstallationEntry{
		{Product: "demo", Path: "/etc/demo", SchemaVersion: 5, Units: 5},
	}

	plain, err := json.Marshal(entries)
	require.NoError(t, err)
	wrapped, err := json.Marshal(views.WithServices(entries))
	require.NoError(t, err)

	require.JSONEq(t, string(plain), string(wrapped))
	require.Equal(t, byte('['), plain[0],
		"the listing stopped being an array, so every `jq '.data[]'` reading it breaks")
}

// TestAnUnreadableRowCarriesNothingItCouldNotRead is decision 5c in the
// contract rather than in the table.
//
// A consumer that sees `schema_version` believes this manager read it. On a row
// whose whole point is that it could not, every interpreted key has to be
// absent -- not zero, absent, because `"mode": ""` is a claim about the mode.
func TestAnUnreadableRowCarriesNothingItCouldNotRead(t *testing.T) {
	got := marshal(t, ops.InstallationEntry{
		Product: "legacy", Path: "/etc/legacy", Units: 5,
		Problem: "installation was written by a newer manager",
	})

	require.Equal(t, map[string]any{
		"product": "legacy",
		"path":    "/etc/legacy",
		"units":   float64(5),
		"problem": "installation was written by a newer manager",
	}, got)
}

func marshal(t *testing.T, v any) map[string]any {
	t.Helper()

	b, err := json.Marshal(v)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got))
	return got
}
