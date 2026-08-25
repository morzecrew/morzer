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
// Only api_version deprecations reach a warning now.
//
// This asserted that the two kinds were independent, using `runtime:` as the
// field-level example -- the only one there has ever been. Decision 23 removed
// it, so the field half has no member to demonstrate with, and a manifest
// carrying `runtime:` is refused by Validate long before anything warns about
// it. What survives is the claim worth keeping: the api_version warning does
// not depend on a field deprecation existing beside it.
func TestAnAPIVersionDeprecationIsReportedOnItsOwn(t *testing.T) {
	const stale = domain.APIVersion("morze.dev/v0alpha0")
	deprecateAPIVersion(t, stale, "upgrade the bundle to v1alpha1")

	d, seen := warned(t)
	d.warnDeprecations(domain.Manifest{
		APIVersion: stale,
		Runtimes:   domain.Runtimes{"compose": {Files: []string{"compose.yaml"}}},
	})

	require.Len(t, *seen, 1, "one deprecation, one warning: %v", *seen)
	assert.True(t, mentions(*seen, string(stale)),
		"the api_version warning must name the version: %v", *seen)
}

// A legacy block produces no warning, because it is not deprecated -- it is
// refused. Pinned so that a reintroduced fold would show up as a warning
// nobody meant to restore.
func TestALegacyBlockWarnsAboutNothingBecauseItIsRefused(t *testing.T) {
	d, seen := warned(t)
	d.warnDeprecations(domain.Manifest{
		APIVersion: domain.APIVersionV1Alpha1,
		Runtime:    domain.RuntimeSpec{Files: []string{"compose.yaml"}},
	})

	assert.Empty(t, *seen,
		"`runtime:` stopped being read in 0.3.0; warning about it would offer "+
			"a grace period the loader will not honour")
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
//
// What it now does say is the other half. Silence read as a clean bill of
// health: the plan printed its steps whether it had checked the bundle or not,
// and nothing distinguished the two.
func TestAPlanDoesNotReachForARemoteBundle(t *testing.T) {
	d, seen := warned(t)
	src := &countingSource{}
	d.Source = src

	validated, err := d.checkPlannedRelease(
		context.Background(), "oci://registry.invalid/demo:1.2.0")

	require.NoError(t, err, "declining to look is not a refusal")
	assert.False(t, validated, "nothing was validated")
	assert.Zero(t, src.fetches,
		"a plan must not pull from a registry to decide whether to warn")
	require.Len(t, *seen, 1, "and it says so rather than staying silent")
	assert.Contains(t, (*seen)[0], "did not validate")
}

// The local half of the same guard: a directory *is* reached for, so the
// decision is a scheme test rather than a blanket refusal to look.
//
// And a source that cannot produce the bundle is now a refusal. The plan
// declines to look in exactly one case, the remote reference above; everywhere
// else, failing to look is the operation failing, which is what a plan is for
// saying in advance.
func TestAPlanDoesReachForALocalBundle(t *testing.T) {
	d, _ := warned(t)
	src := &countingSource{}
	d.Source = src

	validated, err := d.checkPlannedRelease(context.Background(), t.TempDir())

	assert.Equal(t, 1, src.fetches,
		"a local reference is materialised through the source, whatever shape it is")
	require.Error(t, err, "a bundle that cannot be read is not a bundle that is fine")
	assert.False(t, validated)
}
