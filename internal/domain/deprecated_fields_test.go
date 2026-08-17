package domain_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

// A field deprecation, unlike an api_version one, cannot be a map key: the
// question is whether the vendor wrote the field at all. RFC 0023 decision 9
// promised `runtime:` would stay readable and deprecated, and until this wave
// only the first half of that was true anywhere but a document.

func legacyManifest() domain.Manifest {
	return domain.Manifest{
		APIVersion: domain.APIVersionV1Alpha1,
		Kind:       domain.KindApplicationRelease,
		Metadata:   domain.Metadata{Name: "demo"},
		Runtime:    domain.RuntimeSpec{Project: "demo", Files: []string{"compose.yaml"}},
	}
}

func currentManifest() domain.Manifest {
	return domain.Manifest{
		APIVersion: domain.APIVersionV1Alpha1,
		Kind:       domain.KindApplicationRelease,
		Metadata:   domain.Metadata{Name: "demo"},
		Runtimes: domain.Runtimes{"compose": {
			Files:   []string{"compose.yaml"},
			Options: map[string]string{"project": "demo"},
		}},
	}
}

func TestTheLegacyRuntimeBlockIsReportedAsDeprecated(t *testing.T) {
	fields := legacyManifest().DeprecatedFields()
	require.Len(t, fields, 1, "a manifest declaring runtimes the old way has exactly one deprecated field")

	f := fields[0]
	assert.Equal(t, "runtime", f.Field,
		"spelled the way the vendor spelled it, or they go looking for a block that is not there")

	// The three things the sentence has to carry. A deprecation missing any
	// one of them cannot be acted on: what, when, and what instead.
	msg := f.Message()
	assert.Contains(t, msg, "`runtime`")
	assert.Contains(t, msg, domain.FieldRemovalRelease,
		"a deprecation with no removal release is the clockless state this wave exists to end")
	assert.Contains(t, msg, "runtimes.compose",
		"naming no successor makes it a complaint rather than an instruction")
}

func TestAManifestOnTheCurrentSpellingReportsNothing(t *testing.T) {
	assert.Empty(t, currentManifest().DeprecatedFields(),
		"a vendor who has already migrated must not be warned about a block they deleted")
}

// The empty case, decided rather than inherited. A manifest declaring no
// runtime at all is refused by Validate, but DeprecatedFields is reachable from
// anything holding a Manifest -- including the zero value -- and a warning
// about a block nobody wrote would be worse than silence.
func TestAManifestDeclaringNoRuntimeAtAllReportsNothing(t *testing.T) {
	assert.Empty(t, domain.Manifest{}.DeprecatedFields(),
		"the zero manifest declares nothing, so it has deprecated nothing")

	bare := currentManifest()
	bare.Runtimes = nil
	assert.Empty(t, bare.DeprecatedFields(),
		"a manifest with neither spelling names no deprecated field")
}

// The predicate is DeclaredRuntimes' own `fromLegacy`, deliberately. A second
// look at the struct could disagree with the loader about which spelling a
// bundle is written in, and the disagreement would be invisible: the warning
// would name a block the manager is not in fact reading.
func TestTheWarningFollowsWhichBlockWasActuallyRead(t *testing.T) {
	// `runtimes:` present and a legacy block behind it. Validate refuses this
	// pair, but the reader does not -- `runtimes:` wins, so nothing is
	// deprecated about what was read.
	both := currentManifest()
	both.Runtime = domain.RuntimeSpec{Files: []string{"old.yaml"}}

	declared, fromLegacy := both.DeclaredRuntimes()
	require.False(t, fromLegacy, "the fixture must exercise the case where `runtimes:` wins")
	require.Len(t, declared, 1)

	assert.Empty(t, both.DeprecatedFields(),
		"the warning must describe what was read, never what was merely present")
}

// The removal release is a promise made to vendors in a warning they can act
// on, so it is pinned here rather than left to whatever a later edit types.
func TestTheRemovalReleaseIsNamedAndIsAfterTheOneThatAddedTheReplacement(t *testing.T) {
	added, err := domain.ParseVersion(domain.RuntimesMinManagerVersion)
	require.NoError(t, err, "the scaffold stamps this as a min_manager_version, so it must parse as one")
	removed, err := domain.ParseVersion(domain.FieldRemovalRelease)
	require.NoError(t, err)

	assert.True(t, added.LessThan(removed),
		"the old spelling cannot stop being read before the new one is available: %s is not before %s",
		added, removed)
	assert.False(t, strings.TrimSpace(domain.FieldRemovalRelease) == "",
		"a deprecation without a removal release is a word in a document")
}
