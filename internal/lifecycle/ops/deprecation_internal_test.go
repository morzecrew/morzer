package ops

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
)

// The two kinds of deprecation an operator can be told about, and the one
// caller that publishes both. The field half is exercised end to end elsewhere;
// this is the half that cannot be, because `DeprecatedAPIVersions` is empty and
// will stay empty until an api_version is actually withdrawn.
//
// That emptiness is why the branch was never executed by any test, on this path
// or on the one it was moved from. A detection branch nothing runs is a branch
// nobody knows works, and this one only ever runs on the day it matters.

func warned(t *testing.T) (*Deps, *[]string) {
	t.Helper()
	bus := events.NewStrictBus()
	var seen []string
	bus.SubscribeFunc(func(e events.Event) {
		if e.Level == events.LevelWarn {
			seen = append(seen, e.Message)
		}
	})
	return &Deps{Bus: bus}, &seen
}

func TestADeprecatedAPIVersionReachesTheOperator(t *testing.T) {
	const stale = domain.APIVersion("morze.dev/v0alpha0")
	domain.DeprecatedAPIVersions[stale] = "upgrade the bundle to v1alpha1"
	defer delete(domain.DeprecatedAPIVersions, stale)

	d, seen := warned(t)
	d.warnDeprecations(domain.Manifest{APIVersion: stale})

	require.Len(t, *seen, 1, "a deprecated api_version must produce exactly one warning")
	assert.Contains(t, (*seen)[0], string(stale), "the warning must name the version that is deprecated")
	assert.Contains(t, (*seen)[0], "upgrade the bundle to v1alpha1",
		"and carry what the map says to do about it")
}

// Both kinds at once, which is the manifest a vendor who has migrated neither
// would ship: each is reported on its own rather than one masking the other.
func TestBothKindsOfDeprecationAreReported(t *testing.T) {
	const stale = domain.APIVersion("morze.dev/v0alpha0")
	domain.DeprecatedAPIVersions[stale] = "upgrade the bundle to v1alpha1"
	defer delete(domain.DeprecatedAPIVersions, stale)

	d, seen := warned(t)
	d.warnDeprecations(domain.Manifest{
		APIVersion: stale,
		Runtime:    domain.RuntimeSpec{Files: []string{"compose.yaml"}},
	})

	require.Len(t, *seen, 2, "two independent deprecations, two warnings: %v", *seen)
}

// A manifest with nothing deprecated says nothing. The silent case is the one
// every real bundle takes today, so it is worth pinning rather than assuming.
func TestACurrentManifestProducesNoWarning(t *testing.T) {
	d, seen := warned(t)
	d.warnDeprecations(domain.Manifest{
		APIVersion: domain.APIVersionV1Alpha1,
		Runtimes:   domain.Runtimes{"compose": {Files: []string{"compose.yaml"}}},
	})

	assert.Empty(t, *seen)
}

// Deps without a bus is every unit test that builds one by hand, and a warning
// is not worth a nil dereference on a path an operator is mid-install on.
func TestWarningWithoutABusIsNotAPanic(t *testing.T) {
	d := &Deps{}
	assert.NotPanics(t, func() {
		d.warnDeprecations(domain.Manifest{
			Runtime: domain.RuntimeSpec{Files: []string{"compose.yaml"}},
		})
	})
}
