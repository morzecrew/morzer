package suite

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/release"
)

// These exercise the compatibility rules against the two real bundle fixtures
// rather than against hand-built Compatibility structs.
//
// The distinction matters: a hand-built struct tests the function, while a
// loaded manifest also tests that the YAML field names, the strict decoder and
// the defaults all line up. A typo in `database_schema_max` would pass every
// unit test in internal/domain and fail here.

func loadFixture(t *testing.T, name string) domain.Release {
	t.Helper()
	wd, err := filepath.Abs(".")
	require.NoError(t, err)

	rel, err := release.Load(filepath.Join(wd, "..", "..", "testdata", name))
	require.NoError(t, err, "fixture %s must be a valid bundle", name)
	return rel
}

// TestFixturesCarryTheExpectedRanges guards the tests below from silently
// becoming vacuous. If someone edits a fixture's schema range, the assertions
// that depend on it should fail here with an obvious message rather than
// passing for the wrong reason.
func TestFixturesCarryTheExpectedRanges(t *testing.T) {
	v120 := loadFixture(t, "bundle").Manifest
	v130 := loadFixture(t, "bundle-1.3.0").Manifest

	assert.Equal(t, "1.2.0", v120.Metadata.Version.String())
	assert.Equal(t, 10, v120.Compatibility.DatabaseSchemaMin)
	assert.Equal(t, 12, v120.Compatibility.DatabaseSchemaMax)
	assert.True(t, v120.Compatibility.RollbackSafe)

	assert.Equal(t, "1.3.0", v130.Metadata.Version.String())
	assert.Equal(t, 12, v130.Compatibility.DatabaseSchemaMin)
	assert.Equal(t, 14, v130.Compatibility.DatabaseSchemaMax)
	assert.Equal(t, ">=1.2.0 <2.0.0", v130.Compatibility.UpgradeFrom.String())
}

func TestUpgradeBetweenRealReleases(t *testing.T) {
	from := loadFixture(t, "bundle")
	to := loadFixture(t, "bundle-1.3.0")
	manager := domain.MustParseVersion("1.0.0")

	t.Run("1.2.0 to 1.3.0 at a schema both support", func(t *testing.T) {
		report := domain.CheckUpgrade(
			from.Version(), to.Version(), to.Manifest.Compatibility, manager, 12)

		assert.True(t, report.OK, "problems: %v", report.Problems)
		assert.NoError(t, report.Err())
		assert.Empty(t, report.Warnings)
	})

	t.Run("1.2.0 to 1.3.0 at a schema below the target minimum", func(t *testing.T) {
		// Schema 10 is inside 1.2.0's range and below 1.3.0's minimum.
		// Migrations will bring it forward, so this warns rather than
		// refusing.
		report := domain.CheckUpgrade(
			from.Version(), to.Version(), to.Manifest.Compatibility, manager, 10)

		assert.True(t, report.OK)
		require.Len(t, report.Warnings, 1)
		assert.Contains(t, report.Warnings[0], "below release minimum 12")
	})

	t.Run("a release older than upgrade_from allows is refused", func(t *testing.T) {
		// 1.3.0 declares >=1.2.0, deliberately narrower than 1.2.0's own
		// >=1.0.0. An installation still on 1.1.0 must be told to step
		// through rather than jump.
		report := domain.CheckUpgrade(
			domain.MustParseVersion("1.1.0"), to.Version(),
			to.Manifest.Compatibility, manager, 12)

		require.False(t, report.OK)
		assert.Contains(t, report.Problems[0], "accepts upgrades from")
		assert.Equal(t, domain.ExitIncompatible, domain.ExitCode(report.Err()))
	})

	t.Run("installing 1.2.0 fresh ignores upgrade_from", func(t *testing.T) {
		report := domain.CheckUpgrade(
			domain.Version{}, from.Version(),
			from.Manifest.Compatibility, manager, 0)

		assert.True(t, report.OK, "problems: %v", report.Problems)
	})
}

func TestRollbackBetweenRealReleases(t *testing.T) {
	previous := loadFixture(t, "bundle")      // 1.2.0, schema 10-12
	current := loadFixture(t, "bundle-1.3.0") // 1.3.0, schema 12-14

	t.Run("schema still inside the previous range", func(t *testing.T) {
		a := domain.AssessRollback(
			current.Manifest.Compatibility, previous.Manifest.Compatibility, 12)

		assert.True(t, a.ContainersReversible)
		assert.True(t, a.SchemaCompatible)
		assert.False(t, a.RestoreRequired)
	})

	t.Run("schema migrated past what the previous release reads", func(t *testing.T) {
		// This is the case rollback exists to refuse: 1.3.0 migrated the
		// database to 14, and 1.2.0 reads at most 12. Swapping the
		// containers back would leave the old code reading a schema it
		// does not understand.
		a := domain.AssessRollback(
			current.Manifest.Compatibility, previous.Manifest.Compatibility, 14)

		assert.True(t, a.ContainersReversible, "the containers themselves could still be swapped")
		assert.False(t, a.SchemaCompatible)
		assert.True(t, a.RestoreRequired)
		assert.Contains(t, a.Reason, "does not understand")
	})

	t.Run("an irreversible release blocks regardless of schema", func(t *testing.T) {
		irreversible := current.Manifest.Compatibility
		irreversible.RollbackSafe = false

		a := domain.AssessRollback(irreversible, previous.Manifest.Compatibility, 12)

		assert.False(t, a.ContainersReversible)
		assert.True(t, a.RestoreRequired)
		assert.Contains(t, a.Reason, "rollback_safe")
	})

	t.Run("both blockers at once are both reported", func(t *testing.T) {
		irreversible := current.Manifest.Compatibility
		irreversible.RollbackSafe = false

		a := domain.AssessRollback(irreversible, previous.Manifest.Compatibility, 14)

		assert.False(t, a.ContainersReversible)
		assert.False(t, a.SchemaCompatible)
		assert.Contains(t, a.Reason, "rollback_safe")
		assert.Contains(t, a.Reason, "does not understand")
	})
}

// TestFixturesAreDistinctReleases asserts the two bundles are separate
// identities. The digest is content-addressed, so two fixtures that drifted
// into being byte-identical would make every transition test meaningless.
func TestFixturesAreDistinctReleases(t *testing.T) {
	a := loadFixture(t, "bundle")
	b := loadFixture(t, "bundle-1.3.0")

	assert.NotEqual(t, a.Digest, b.Digest)
	assert.False(t, a.Version().Equal(b.Version()))
	assert.Equal(t, a.Name(), b.Name(), "both are releases of the same product")
}
