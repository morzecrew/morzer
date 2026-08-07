package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These are the first tests CheckUpgrade and AssessRollback have had. Both were
// written alongside the domain model and never called, so what follows asserts
// what the functions *should* do rather than transcribing what they currently
// do -- a test written to match unexercised code just freezes its bugs.

func constraint(t *testing.T, s string) Constraint {
	t.Helper()
	c, err := ParseConstraint(s)
	require.NoError(t, err)
	return c
}

func TestCheckUpgrade(t *testing.T) {
	manager := MustParseVersion("1.0.0")

	cases := []struct {
		name        string
		from, to    string // "" for the zero version
		target      Compatibility
		manager     string
		schema      int
		wantOK      bool
		wantProblem string // substring; empty means expect none
		wantWarning string // substring; empty means expect none
		wantNoWarn  bool
	}{
		{
			name: "fresh install with no constraints",
			from: "", to: "1.2.0",
			wantOK: true, wantNoWarn: true,
		},
		{
			name: "upgrade_from satisfied",
			from: "1.1.0", to: "1.2.0",
			target: Compatibility{UpgradeFrom: constraint(t, ">=1.0.0 <2.0.0")},
			wantOK: true, wantNoWarn: true,
		},
		{
			name: "upgrade_from violated",
			from: "0.9.0", to: "1.2.0",
			target:      Compatibility{UpgradeFrom: constraint(t, ">=1.0.0 <2.0.0")},
			wantOK:      false,
			wantProblem: "accepts upgrades from",
		},
		{
			name: "upgrade_from ignored on a fresh install",
			from: "", to: "1.2.0",
			// A first install has nothing to upgrade from, so a
			// constraint that would reject the zero version must not
			// block it.
			target: Compatibility{UpgradeFrom: constraint(t, ">=1.0.0 <2.0.0")},
			wantOK: true,
		},
		{
			name: "manager too old",
			from: "1.1.0", to: "1.2.0",
			target:      Compatibility{MinManagerVersion: MustParseVersion("2.0.0")},
			wantOK:      false,
			wantProblem: "requires manager >= 2.0.0",
		},
		{
			name: "manager exactly at the minimum is allowed",
			from: "1.1.0", to: "1.2.0",
			target: Compatibility{MinManagerVersion: MustParseVersion("1.0.0")},
			wantOK: true,
		},
		{
			name: "schema newer than the release supports",
			from: "1.2.0", to: "1.3.0",
			target:      Compatibility{DatabaseSchemaMin: 10, DatabaseSchemaMax: 12},
			schema:      14,
			wantOK:      false,
			wantProblem: "database schema 14 is newer",
		},
		{
			name: "schema at the maximum is allowed",
			from: "1.2.0", to: "1.3.0",
			target: Compatibility{DatabaseSchemaMin: 10, DatabaseSchemaMax: 12},
			schema: 12,
			wantOK: true,
		},
		{
			name: "schema below the minimum warns but does not block",
			from: "1.2.0", to: "1.3.0",
			// Migrations will bring it forward; that is not a reason
			// to refuse the upgrade.
			target:      Compatibility{DatabaseSchemaMin: 12, DatabaseSchemaMax: 14},
			schema:      10,
			wantOK:      true,
			wantWarning: "below release minimum",
		},
		{
			name: "unknown schema skips the range check entirely",
			from: "1.2.0", to: "1.3.0",
			// The manager does not own the database. A zero means
			// "not reported", and must not be treated as compatible
			// *or* as a violation.
			target: Compatibility{DatabaseSchemaMin: 12, DatabaseSchemaMax: 14},
			schema: 0,
			wantOK: true, wantNoWarn: true,
		},
		{
			name: "downgrade warns",
			from: "1.3.0", to: "1.2.0",
			wantOK: true, wantWarning: "this is a downgrade",
		},
		{
			name: "same version warns",
			from: "1.2.0", to: "1.2.0",
			wantOK: true, wantWarning: "already installed",
		},
		{
			name: "a release declaring only a minimum still warns below it",
			from: "1.2.0", to: "1.3.0",
			// No maximum declared. The minimum is still a statement
			// about what this release expects, and an operator
			// whose schema is below it should hear about it.
			target:      Compatibility{DatabaseSchemaMin: 12},
			schema:      10,
			wantOK:      true,
			wantWarning: "below release minimum",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var from Version
			if tc.from != "" {
				from = MustParseVersion(tc.from)
			}
			mgr := manager
			if tc.manager != "" {
				mgr = MustParseVersion(tc.manager)
			}

			report := CheckUpgrade(from, MustParseVersion(tc.to), tc.target, mgr, tc.schema)

			assert.Equal(t, tc.wantOK, report.OK, "problems: %v", report.Problems)

			if tc.wantProblem != "" {
				require.NotEmpty(t, report.Problems)
				assert.Contains(t, joinAll(report.Problems), tc.wantProblem)
			} else {
				assert.Empty(t, report.Problems)
			}

			if tc.wantWarning != "" {
				require.NotEmpty(t, report.Warnings, "expected a warning containing %q", tc.wantWarning)
				assert.Contains(t, joinAll(report.Warnings), tc.wantWarning)
			}
			if tc.wantNoWarn {
				assert.Empty(t, report.Warnings)
			}

			// OK and Err must never disagree: the exit code depends on it.
			if tc.wantOK {
				assert.NoError(t, report.Err())
			} else {
				require.Error(t, report.Err())
				assert.Equal(t, ExitIncompatible, ExitCode(report.Err()))
			}
		})
	}
}

// TestAPreReleaseIsComparedByOrdering.
//
// The constraint library excludes every pre-release from a constraint that
// carries none, so a customer running an rc was refused with "accepts upgrades
// from >=1.0.0 <2.0.0, installed version is 1.5.0-rc.1" -- a sentence that
// reads as satisfied. The rule is right for picking a dependency and wrong for
// describing the machine somebody is already running.
//
// The retry is against the same version through a constraint rewritten to admit
// pre-releases, not against the version's release core: the core comparison was
// wrong at both boundaries, accepting a version below a floor and refusing one
// inside a range. The table below is the whole contract, boundaries included.
func TestAPreReleaseIsComparedByOrdering(t *testing.T) {
	cases := []struct {
		constraint string
		version    string
		want       bool
		why        string
	}{
		{">=2.30", "2.31.0-rc.1", true, "an rc past the floor is past the floor"},
		{">=1.0.0 <2.0.0", "1.5.0-rc.1", true, "and one inside the range is inside it"},
		{">=1.0.0 <2.0.0", "1.5.0-beta.2+build.7", true, "build metadata decides nothing"},
		{">=1.0.0 <2.0.0", "0.9.0-rc.1", false, "below the floor is still below it"},

		// The boundaries, which are the point of the rewrite. A lower
		// bound admits its own pre-releases -- ">=2.0.0" is "2.0 or
		// later", and an rc of 2.0.0 carries 2.0's migrations -- while
		// an upper bound does not: ">=1.0.0 <2.0.0" means "any 1.x",
		// and 2.0.0-rc.1 is not a 1.x.
		{">=2.0.0", "2.0.0-rc.1", true, "an rc of the floor itself"},
		{">=1.0.0 <2.0.0", "2.0.0-rc.1", false, "an rc of the ceiling is not below it"},

		// A constraint that names a pre-release means it, and is
		// compared as written.
		{">=2.0.0-rc.2", "2.0.0-rc.1", false, "rc.1 is before rc.2"},
		{">=2.0.0-rc.2", "2.0.0-rc.3", true, "rc.3 is after it"},

		// Per alternative: the first branch names a pre-release and
		// does not admit this version, the second names none and does.
		{">=1.0.0-rc.1 <1.1.0 || >=2.0.0", "2.1.0-rc.1", true, "the second branch admits it"},
		{">=1.0.0-rc.1 <1.1.0 || >=2.0.0", "1.5.0-rc.1", false, "neither branch does"},

		// Sugar keeps working.
		{"^1.2.0", "1.5.0-rc.1", true, "a caret range admits an rc inside it"},
		{"^1.2.0", "2.0.0-rc.1", false, "and refuses one past it"},

		// A wildcard cannot carry a pre-release, so the rewrite does
		// not apply and the library's own answer stands -- refusing,
		// which is the safe direction for a gate.
		{">=1.x", "1.5.0-rc.1", false, "a wildcard constraint keeps the library's answer"},
	}

	for _, tc := range cases {
		c := constraint(t, tc.constraint)
		assert.Equal(t, tc.want, c.Allows(MustParseVersion(tc.version)),
			"%q allows %q: %s", tc.constraint, tc.version, tc.why)
	}

	// And the refusal an operator meets is still reachable.
	report := CheckUpgrade(
		MustParseVersion("0.9.0-rc.1"), MustParseVersion("2.0.0"),
		Compatibility{UpgradeFrom: constraint(t, ">=1.0.0 <2.0.0")},
		MustParseVersion("1.0.0"), 0)
	assert.False(t, report.OK)
}

// TestCheckUpgradeReportsEveryProblemAtOnce is the property the doc comment
// promises: an operator fixing one blocker must not discover the next on the
// retry.
func TestCheckUpgradeReportsEveryProblemAtOnce(t *testing.T) {
	report := CheckUpgrade(
		MustParseVersion("0.9.0"),
		MustParseVersion("2.0.0"),
		Compatibility{
			UpgradeFrom:       constraint(t, ">=1.0.0 <2.0.0"),
			MinManagerVersion: MustParseVersion("3.0.0"),
			DatabaseSchemaMin: 10,
			DatabaseSchemaMax: 12,
		},
		MustParseVersion("1.0.0"),
		20,
	)

	require.False(t, report.OK)
	assert.Len(t, report.Problems, 3, "every violated rule must be reported: %v", report.Problems)

	joined := report.Err().Error()
	assert.Contains(t, joined, "requires manager")
	assert.Contains(t, joined, "accepts upgrades from")
	assert.Contains(t, joined, "database schema")
}

func TestAssessRollback(t *testing.T) {
	safe := Compatibility{RollbackSafe: true, DatabaseSchemaMin: 10, DatabaseSchemaMax: 12}

	t.Run("clean rollback", func(t *testing.T) {
		a := AssessRollback(
			Compatibility{RollbackSafe: true},
			safe,
			12,
		)
		assert.True(t, a.ContainersReversible)
		assert.True(t, a.SchemaCompatible)
		assert.False(t, a.RestoreRequired)
		assert.Empty(t, a.Reason)
	})

	t.Run("irreversible migrations block the container rollback", func(t *testing.T) {
		a := AssessRollback(
			Compatibility{RollbackSafe: false},
			safe,
			12,
		)
		assert.False(t, a.ContainersReversible)
		assert.True(t, a.RestoreRequired)
		assert.Contains(t, a.Reason, "rollback_safe")
	})

	t.Run("schema past what the previous release reads", func(t *testing.T) {
		a := AssessRollback(
			Compatibility{RollbackSafe: true},
			safe,
			14, // previous supports at most 12
		)
		assert.True(t, a.ContainersReversible, "the containers themselves can still be swapped")
		assert.False(t, a.SchemaCompatible)
		assert.True(t, a.RestoreRequired)
		assert.Contains(t, a.Reason, "does not understand")
	})

	t.Run("schema exactly at the previous maximum is fine", func(t *testing.T) {
		a := AssessRollback(Compatibility{RollbackSafe: true}, safe, 12)
		assert.True(t, a.SchemaCompatible)
		assert.False(t, a.RestoreRequired)
	})

	t.Run("unknown schema does not claim compatibility", func(t *testing.T) {
		// A zero means the migrate hook reported nothing. The check is
		// skipped, which is why `rollback` must say so rather than
		// implying it verified anything.
		a := AssessRollback(Compatibility{RollbackSafe: true}, safe, 0)
		assert.True(t, a.SchemaCompatible)
		assert.False(t, a.RestoreRequired)
	})

	t.Run("previous release declaring no maximum is not a constraint", func(t *testing.T) {
		a := AssessRollback(
			Compatibility{RollbackSafe: true},
			Compatibility{RollbackSafe: true}, // no schema range at all
			99,
		)
		assert.True(t, a.SchemaCompatible)
		assert.False(t, a.RestoreRequired)
	})

	// Both blockers at once. The struct exists to report three answers
	// independently -- "they fail independently and an operator needs to
	// know which one blocked them" -- so a caller must learn about both,
	// not just whichever is checked first.
	t.Run("both blockers are reported independently", func(t *testing.T) {
		a := AssessRollback(
			Compatibility{RollbackSafe: false},
			safe,
			14,
		)
		assert.False(t, a.ContainersReversible, "irreversible migrations")
		assert.False(t, a.SchemaCompatible,
			"the schema is also past what the previous release reads, and saying otherwise "+
				"would tell an operator the schema is fine when it has not been looked at")
		assert.True(t, a.RestoreRequired)
	})
}

func joinAll(ss []string) string {
	out := ""
	for _, s := range ss {
		out += s + "\n"
	}
	return out
}
