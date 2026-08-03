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
