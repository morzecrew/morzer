package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

// Chain continuity is the question "did anything install a release without
// filing a record". Everything here is about not answering it wrongly in the
// two directions that would make the check useless: a break reported on an
// ordinary sequence trains a reader to ignore it, and a break missed is the
// gap an audit exists to find.

func stmt(id, kind, outcome, from, to string, started time.Time) domain.Statement {
	return domain.Statement{
		PredicateType: domain.PredicateType,
		Predicate: domain.Predicate{
			Operation: domain.AttestedOperation{
				ID: id, Kind: kind, Outcome: outcome,
				Started: domain.NewTime(started),
			},
			Release: domain.AttestedRelease{FromVersion: from, ToVersion: to},
		},
	}
}

func minute(min int) time.Time {
	return time.Date(2026, 8, 13, 9, min, 0, 0, time.UTC)
}

func TestAnUnbrokenSequenceOfUpdatesReportsNothing(t *testing.T) {
	breaks := domain.VerifyChain([]domain.Statement{
		stmt("op1", "update", "succeeded", "1.0.0", "1.1.0", minute(1)),
		stmt("op2", "update", "succeeded", "1.1.0", "1.2.0", minute(2)),
		stmt("op3", "update", "succeeded", "1.2.0", "1.3.0", minute(3)),
	})
	assert.Empty(t, breaks)
}

// The gap an audit is looking for: something moved the installation and filed
// no statement.
func TestAMissingStatementIsAChainBreak(t *testing.T) {
	breaks := domain.VerifyChain([]domain.Statement{
		stmt("op1", "update", "succeeded", "1.0.0", "1.1.0", minute(1)),
		// 1.1.0 -> 1.2.0 happened and nothing recorded it.
		stmt("op3", "update", "succeeded", "1.2.0", "1.3.0", minute(3)),
	})
	require.Len(t, breaks, 1)
	assert.Equal(t, "op1", breaks[0].After)
	assert.Equal(t, "op3", breaks[0].Before)
	assert.Contains(t, breaks[0].Detail, "1.2.0")
	assert.Contains(t, breaks[0].Detail, "1.1.0")
}

// A failed update left the installation where it was, so the next operation
// moves from the *same* version it did.
//
// Reading the predecessor's `to_version` regardless of outcome would report a
// break on every recovery from a failure -- which is the sequence most likely
// to be audited, and the one where a spurious finding does the most damage.
func TestAFailedUpdateDoesNotBreakTheChain(t *testing.T) {
	breaks := domain.VerifyChain([]domain.Statement{
		stmt("op1", "update", "succeeded", "1.0.0", "1.1.0", minute(1)),
		stmt("op2", "update", "failed", "1.1.0", "1.2.0", minute(2)),
		// The retry starts from where the failure left it.
		stmt("op3", "update", "succeeded", "1.1.0", "1.2.0", minute(3)),
	})
	assert.Empty(t, breaks, "a failed update was treated as though it had moved the installation")
}

// An apply converges onto the release already installed. It moves nothing and
// carries no from_version, and requiring one would report a break on the most
// ordinary operation there is -- systemd runs one at every boot.
func TestAnApplyBetweenUpdatesIsNotABreak(t *testing.T) {
	applied := stmt("op2", "apply", "succeeded", "", "1.1.0", minute(2))

	breaks := domain.VerifyChain([]domain.Statement{
		stmt("op1", "update", "succeeded", "1.0.0", "1.1.0", minute(1)),
		applied,
		stmt("op3", "update", "succeeded", "1.1.0", "1.2.0", minute(3)),
	})
	assert.Empty(t, breaks)
}

// The input is whatever order the files arrived in.
//
// A directory listing is alphabetical and an auditor's copy may have been
// assembled by hand, so ordering by the operation's own start time is what
// keeps the answer about the history rather than about the filenames.
func TestTheChainIsCheckedInTimeOrderNotInputOrder(t *testing.T) {
	shuffled := []domain.Statement{
		stmt("op3", "update", "succeeded", "1.2.0", "1.3.0", minute(3)),
		stmt("op1", "update", "succeeded", "1.0.0", "1.1.0", minute(1)),
		stmt("op2", "update", "succeeded", "1.1.0", "1.2.0", minute(2)),
	}
	assert.Empty(t, domain.VerifyChain(shuffled))
}

// A rollback is a move like any other and its continuity is checked the same
// way -- it is also the operation most worth checking, since it is what an
// incident review reads.
func TestARollbackJoinsTheChain(t *testing.T) {
	assert.Empty(t, domain.VerifyChain([]domain.Statement{
		stmt("op1", "update", "succeeded", "1.0.0", "1.1.0", minute(1)),
		stmt("op2", "rollback", "succeeded", "1.1.0", "1.0.0", minute(2)),
	}))

	breaks := domain.VerifyChain([]domain.Statement{
		stmt("op1", "update", "succeeded", "1.0.0", "1.1.0", minute(1)),
		// Rolled back from a version this machine was never on.
		stmt("op2", "rollback", "succeeded", "1.4.0", "1.3.0", minute(2)),
	})
	require.Len(t, breaks, 1)
}

// A statement that establishes nothing must not suppress the check across
// itself.
//
// A failed `apply` carries no from_version and left no version behind it, so
// comparing adjacent pairs had nothing to compare the next update against and
// skipped it -- which made an out-of-band install immediately after a failed
// apply invisible to the one check that exists to find it. That sequence is not
// exotic: an apply that fails is why somebody starts touching the machine by
// hand.
func TestAFailedApplyDoesNotHideTheGapAfterIt(t *testing.T) {
	breaks := domain.VerifyChain([]domain.Statement{
		stmt("op1", "update", "succeeded", "1.0.0", "1.1.0", minute(1)),
		stmt("op2", "apply", "failed", "", "1.1.0", minute(2)),
		// 1.1.0 -> 1.5.0 happened and nothing recorded it.
		stmt("op3", "update", "succeeded", "1.5.0", "1.6.0", minute(3)),
	})
	require.Len(t, breaks, 1, "the gap after a failed apply went unreported")
	assert.Equal(t, "op1", breaks[0].After)
	assert.Equal(t, "op3", breaks[0].Before)
	assert.Contains(t, breaks[0].Detail, "1.5.0")
	assert.Contains(t, breaks[0].Detail, "1.1.0")
}

// And a failed apply on an otherwise sound sequence is still not a break.
//
// The other direction of the same fix, because a check that reports a break on
// every failed apply is one an operator turns off.
func TestAFailedApplyBetweenSoundUpdatesIsNotABreak(t *testing.T) {
	assert.Empty(t, domain.VerifyChain([]domain.Statement{
		stmt("op1", "update", "succeeded", "1.0.0", "1.1.0", minute(1)),
		stmt("op2", "apply", "failed", "", "1.1.0", minute(2)),
		stmt("op3", "update", "succeeded", "1.1.0", "1.2.0", minute(3)),
	}))
}

// One gap is one finding, however many statements follow it.
//
// Carrying the version forward re-syncs to each statement's own claim, so a
// single out-of-band install is reported once. Keeping the old position after a
// break instead turned it into a finding on every statement after it -- and a
// check that reports a wall for one event is one whose output stops being read,
// which is the failure this whole file is written against.
func TestOneGapIsReportedOnce(t *testing.T) {
	breaks := domain.VerifyChain([]domain.Statement{
		stmt("op1", "update", "succeeded", "1.0.0", "1.1.0", minute(1)),
		// 1.1.0 -> 1.5.0 happened and nothing recorded it.
		stmt("op2", "update", "failed", "1.5.0", "1.6.0", minute(2)),
		stmt("op3", "update", "succeeded", "1.5.0", "1.6.0", minute(3)),
		stmt("op4", "update", "succeeded", "1.6.0", "1.7.0", minute(4)),
	})
	require.Len(t, breaks, 1, "one unrecorded install produced a finding per statement after it")
	assert.Equal(t, "op2", breaks[0].Before)
}

// A failed *config* is the same shape and reaches the same code, so it is
// checked too -- `config` began attesting in this wave, which is what put a
// second kind of non-moving statement into the sequence.
func TestAFailedConfigDoesNotHideTheGapAfterIt(t *testing.T) {
	breaks := domain.VerifyChain([]domain.Statement{
		stmt("op1", "update", "succeeded", "1.0.0", "1.1.0", minute(1)),
		stmt("op2", "config", "failed", "", "1.1.0", minute(2)),
		stmt("op3", "rollback", "succeeded", "1.4.0", "1.3.0", minute(3)),
	})
	require.Len(t, breaks, 1)
	assert.Contains(t, breaks[0].Detail, "1.4.0")
}

func TestASingleStatementHasNoChainToBreak(t *testing.T) {
	assert.Empty(t, domain.VerifyChain([]domain.Statement{
		stmt("op1", "update", "succeeded", "1.0.0", "1.1.0", minute(1)),
	}))
	assert.Empty(t, domain.VerifyChain(nil))
}
