package release_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/release"
	"github.com/morzecrew/morzer/test/clitest"
)

// The scaffold moved to `runtimes:`, which is an unknown field to any manager
// older than the one that added it -- and under strict decoding an unknown
// field refuses the whole manifest. `compatibility.min_manager_version` is what
// turns that refusal into a sentence naming the real problem (RFC 0018
// decision 1), so for a scaffolded bundle the floor is not bookkeeping: without
// it the vendor's customer is told about a typo.
//
// Found by a sabotage that survived: deleting the floor from the scaffold broke
// no test, which meant the half of this wave's decision that the ruling was
// actually about had nothing holding it.

func scaffoldManifest(t *testing.T) domain.Manifest {
	t.Helper()
	r := clitest.New(t)
	dir := filepath.Join(t.TempDir(), "my-product")
	r.Run("release", "new", dir, "--vendor", "example").ExitCode(0)

	rel, err := release.Load(dir)
	require.NoError(t, err)
	return rel.Manifest
}

func TestTheScaffoldDeclaresTheManagerItNeeds(t *testing.T) {
	m := scaffoldManifest(t)

	require.False(t, m.Compatibility.MinManagerVersion.IsZero(),
		"a scaffold using `runtimes:` and declaring no floor hands the vendor's "+
			"customer an unknown-field error instead of an upgrade requirement")

	added, err := domain.ParseVersion(domain.RuntimesMinManagerVersion)
	require.NoError(t, err)
	assert.False(t, m.Compatibility.MinManagerVersion.LessThan(added),
		"the floor must be at least %s, the release that added the field the scaffold uses; got %s",
		added, m.Compatibility.MinManagerVersion)
}

// The consequence, not the declaration: an older manager meeting this bundle is
// told to upgrade, and told which version. Driven through the loader that
// really runs, with the manager's own version moved under the floor.
func TestAnOlderManagerIsToldToUpgradeRatherThanBlamedForATypo(t *testing.T) {
	m := scaffoldManifest(t)

	r := clitest.New(t)
	dir := filepath.Join(t.TempDir(), "my-product")
	r.Run("release", "new", dir, "--vendor", "example").ExitCode(0)

	// One release below the floor the scaffold declares.
	release.SetManagerVersion(domain.MustParseVersion("0.2.0"))
	t.Cleanup(func() { release.SetManagerVersion(domain.Version{}) })

	_, err := release.Load(dir)
	require.Error(t, err, "a manager below the declared floor must refuse the bundle")

	msg := domain.AsError(err).Message
	assert.Contains(t, msg, m.Compatibility.MinManagerVersion.String(),
		"the refusal must name the version to upgrade to, which is the only actionable part")
	assert.NotContains(t, strings.ToLower(msg), "unknown field",
		"naming the field is the confusing error this mechanism exists to replace")
	assert.Equal(t, domain.ExitIncompatible, domain.ExitCode(err))
}

// The production path the fixtures hid. `managerVersion` is zero in every test
// that does not set it, and zero is the one value that skips the comparison
// entirely -- so the whole floor mechanism was inert under `go test` while the
// real binary refused the bundle its own `release new` had just written,
// reporting it as a bug in the scaffold.
//
// A build between tags stamps itself from the *last* tag, so the build that
// first understood `runtimes:` calls itself 0.2.0-N-g<sha>, which semver reads
// as older than the 0.3.0 floor that build's own scaffold writes.
func TestABuildBetweenTagsIsNotRefusedByItsOwnScaffold(t *testing.T) {
	r := clitest.New(t)
	dir := filepath.Join(t.TempDir(), "my-product")
	r.Run("release", "new", dir, "--vendor", "example").ExitCode(0)

	// Exactly what `git describe --tags` produces on this tree.
	release.SetManagerVersion(domain.MustParseVersion("0.2.0-9-g8c5a81c"))
	t.Cleanup(func() { release.SetManagerVersion(domain.Version{}) })

	_, err := release.Load(dir)
	require.NoError(t, err,
		"a build between tags understates its own version, so it must decline the "+
			"comparison rather than refuse a bundle it can in fact read")
}

// A floor is not only a proxy for "this manifest has fields you do not know".
// A vendor may raise it for a behavioural reason, and then the manifest parses
// perfectly on the old manager and `checkManagerVersion` is the only thing
// standing between the operator and a release built for a manager they do not
// have.
//
// So the untagged-build exemption has to be narrow. `0.2.0-rc.1` is a
// deliberately versioned prerelease, not a build between tags, and it really is
// older than a 0.3.0 floor.
//
// Reported by codeant-ai on PR #53, reproduced red before the fix.
func TestADeliberatePrereleaseIsStillHeldToTheFloor(t *testing.T) {
	r := clitest.New(t)
	dir := raiseFloor(t, r.Bundle, "0.3.0")

	release.SetManagerVersion(domain.MustParseVersion("0.2.0-rc.1"))
	t.Cleanup(func() { release.SetManagerVersion(domain.Version{}) })

	_, err := release.Load(dir)
	require.Error(t, err,
		"an rc of 0.2.0 is below a 0.3.0 floor, and the manifest parses -- so nothing "+
			"else would refuse it")
	assert.Contains(t, domain.AsError(err).Message, "0.3.0")
	assert.Equal(t, domain.ExitIncompatible, domain.ExitCode(err))
}

// And the exemption still covers what it was written for: a build between tags,
// against a bundle it can in fact read.
func TestTheExemptionCoversOnlyBuildsBetweenTags(t *testing.T) {
	r := clitest.New(t)
	dir := raiseFloor(t, r.Bundle, "0.3.0")

	release.SetManagerVersion(domain.MustParseVersion("0.2.0-9-gab19822-dirty"))
	t.Cleanup(func() { release.SetManagerVersion(domain.Version{}) })

	_, err := release.Load(dir)
	require.NoError(t, err, "a build between tags understates itself and must not be refused on it")
}

// raiseFloor copies a bundle and rewrites its declared minimum manager, the way
// a vendor adopting a newer manifest feature would.
func raiseFloor(t *testing.T, src, floor string) string {
	t.Helper()

	dst := filepath.Join(t.TempDir(), "bundle")
	require.NoError(t, os.CopyFS(dst, os.DirFS(src)))

	path := filepath.Join(dst, "manifest.yaml")
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	rewritten := strings.Replace(string(data),
		`min_manager_version: "0.0.0"`, `min_manager_version: "`+floor+`"`, 1)
	require.NotEqual(t, string(data), rewritten,
		"the fixture must carry a min_manager_version to raise")
	require.NoError(t, os.WriteFile(path, []byte(rewritten), 0o644))
	return dst
}

// And the floor does not refuse the manager that wrote it: a vendor scaffolding
// on a current build must be able to load their own bundle.
func TestTheFloorAdmitsTheReleaseThatAddedTheField(t *testing.T) {
	r := clitest.New(t)
	dir := filepath.Join(t.TempDir(), "my-product")
	r.Run("release", "new", dir, "--vendor", "example").ExitCode(0)

	release.SetManagerVersion(domain.MustParseVersion(domain.RuntimesMinManagerVersion))
	t.Cleanup(func() { release.SetManagerVersion(domain.Version{}) })

	_, err := release.Load(dir)
	require.NoError(t, err, "the release that added `runtimes:` must be able to read a bundle using it")

	// The scaffold is on disk and unedited; nothing above depends on a
	// fixture this test wrote.
	_, statErr := os.Stat(filepath.Join(dir, "manifest.yaml"))
	require.NoError(t, statErr)
}
