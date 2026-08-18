package ops

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/ports"
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

// mentions reports whether any warning carries want. Asserted on rather than
// counted, because a count cannot tell two different warnings from the same one
// published twice.
func mentions(seen []string, want string) bool {
	return slices.ContainsFunc(seen, func(s string) bool { return strings.Contains(s, want) })
}

// deprecateAPIVersion registers a stale api_version for one test and puts back
// whatever was there before.
//
// The map is empty today and this key is fabricated, so there is nothing to put
// back -- which is exactly why the restore is written this way. A test that
// deletes on the way out is correct only while the map stays empty, and "the
// map is empty" is a property of the production code that this test has no
// business depending on.
func deprecateAPIVersion(t *testing.T, v domain.APIVersion, warning string) {
	t.Helper()

	previous, had := domain.DeprecatedAPIVersions[v]
	domain.DeprecatedAPIVersions[v] = warning
	t.Cleanup(func() {
		if had {
			domain.DeprecatedAPIVersions[v] = previous
			return
		}
		delete(domain.DeprecatedAPIVersions, v)
	})
}

func TestADeprecatedAPIVersionReachesTheOperator(t *testing.T) {
	const stale = domain.APIVersion("morze.dev/v0alpha0")
	deprecateAPIVersion(t, stale, "upgrade the bundle to v1alpha1")

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
	deprecateAPIVersion(t, stale, "upgrade the bundle to v1alpha1")

	d, seen := warned(t)
	d.warnDeprecations(domain.Manifest{
		APIVersion: stale,
		Runtime:    domain.RuntimeSpec{Files: []string{"compose.yaml"}},
	})

	// Both named, not merely counted. Two warnings is also what an
	// implementation that published the api_version one twice would produce,
	// and this test's whole claim is that the two kinds are independent.
	require.Len(t, *seen, 2, "two independent deprecations, two warnings: %v", *seen)
	assert.True(t, mentions(*seen, string(stale)),
		"one of them must be the api_version warning: %v", *seen)
	assert.True(t, mentions(*seen, "`runtime` is deprecated"),
		"and the other the field warning: %v", *seen)
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

// countingSource records whether the plan reached for the bundle at all.
//
// The clitest for a remote reference asserts that no warning appears, and that
// assertion passes for two different reasons: the scheme guard declining, or a
// Fetch that fails because nothing serves `oci://` in a test. Measured — with
// the guard removed, that test still passed. So the guard needs a test that can
// see the difference, and the only observable that separates them is whether
// the source was asked.
type countingSource struct {
	ports.ReleaseSource
	fetches int
}

func (s *countingSource) Fetch(context.Context, ports.Ref, string) (ports.BundlePath, error) {
	s.fetches++
	return "", errors.New("no source in this test")
}

// A remote reference is declined before the source is touched.
//
// Not merely "no warning appears": no *pull* is attempted. That is the whole
// content of the decision -- a plan does not go to a registry to phrase an
// advisory -- and it is invisible in the output either way.
func TestAPlanDoesNotReachForARemoteBundle(t *testing.T) {
	d, seen := warned(t)
	src := &countingSource{}
	d.Source = src

	d.warnPlannedDeprecations(context.Background(), "oci://registry.invalid/demo:1.2.0")

	assert.Zero(t, src.fetches,
		"a plan must not pull from a registry to decide whether to warn")
	assert.Empty(t, *seen, "and it says nothing about a bundle it never read")
}

// The local half of the same guard: a directory *is* reached for, so the
// decision is a scheme test rather than a blanket refusal to look.
func TestAPlanDoesReachForALocalBundle(t *testing.T) {
	d, _ := warned(t)
	src := &countingSource{}
	d.Source = src

	d.warnPlannedDeprecations(context.Background(), t.TempDir())

	assert.Equal(t, 1, src.fetches,
		"a local reference is materialised through the source, whatever shape it is")
}
