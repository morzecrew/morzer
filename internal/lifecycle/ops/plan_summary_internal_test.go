package ops

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/morzecrew/morzer/internal/domain"
)

// What a summary claims about an operation that has not happened.
//
// `test/clitest` covers the plan sentences end to end, which is what an
// operator reads. These cover the halves it cannot: a real apply needs
// containers, and the all-steps-skipped branch needs a record rather than a
// deployment. Both tenses are asserted for each, because a summary that said
// "would" everywhere would satisfy every plan test and describe finished work
// as hypothetical.

func summarised(name, version string) domain.Release {
	return domain.Release{Manifest: domain.Manifest{Metadata: domain.Metadata{
		Name: name, Version: domain.MustParseVersion(version),
	}}}
}

func stepsWith(statuses ...domain.StepStatus) domain.OperationRecord {
	rec := domain.OperationRecord{Status: domain.StatusSucceeded}
	for i, s := range statuses {
		rec.Steps = append(rec.Steps, domain.StepRecord{ID: string(rune('a' + i)), Status: s})
	}
	return rec
}

func TestApplySummarySpeaksInTheTenseOfWhatHappened(t *testing.T) {
	rel := summarised("demo", "1.2.0")
	ran := stepsWith(domain.StepSucceeded, domain.StepSucceeded)

	assert.Equal(t, "demo 1.2.0 applied", applySummary(ran, rel, false))
	assert.Equal(t, "would apply demo 1.2.0", applySummary(ran, rel, true))
}

// Every step already satisfied is its own sentence, and it has a tense too.
func TestApplySummaryWhenEveryStepIsAlreadySatisfied(t *testing.T) {
	rel := summarised("demo", "1.2.0")
	none := stepsWith(domain.StepSkipped, domain.StepSkipped)

	assert.Equal(t, "demo 1.2.0 is already applied; nothing changed",
		applySummary(none, rel, false))
	assert.Equal(t, "demo 1.2.0 is already applied; there would be nothing to change",
		applySummary(none, rel, true))
}

// An apply that did not succeed gets no summary, in either tense.
//
// The pair to TestUpdateSummarySaysNothingAboutAnOperationThatFailed, which
// existed as behaviour on one side and not the other: a rolled-back apply
// printed a success sentence between the rollback and the error.
func TestApplySummarySaysNothingAboutAnOperationThatFailed(t *testing.T) {
	rel := summarised("demo", "1.2.0")
	for _, status := range []domain.OperationStatus{
		domain.StatusFailed, domain.StatusCompensated,
	} {
		rec := domain.OperationRecord{
			Status: status,
			Steps:  []domain.StepRecord{{ID: "a", Status: domain.StepSucceeded}},
		}
		assert.Empty(t, applySummary(rec, rel, false), "status %s, run", status)
		assert.Empty(t, applySummary(rec, rel, true), "status %s, plan", status)
	}
}

// The status guard does not swallow the plan sentences it sits above.
//
// Named for what it checks, which is narrower than it first appears. The
// fixture builds a succeeded record, so this pins the guard's boundary and
// nothing about the engine: that *a plan's* record is a succeeded one is a fact
// about `engine.Run`, and it is pinned end to end by the plan tests in
// `test/clitest` — if the engine ever reported a plan otherwise, every plan
// summary would vanish and those are what would fail.
func TestASucceededRecordStillGetsAPlanSummary(t *testing.T) {
	rel := summarised("demo", "1.2.0")
	ran := stepsWith(domain.StepSucceeded)

	assert.NotEmpty(t, applySummary(ran, rel, true))
}

func TestUpdateSummarySpeaksInTheTenseOfWhatHappened(t *testing.T) {
	to := summarised("demo", "1.3.0")
	from := domain.ReleaseRecord{Name: "demo", Version: domain.MustParseVersion("1.2.0")}
	ran := stepsWith(domain.StepSucceeded)

	assert.Equal(t, "updated demo from 1.2.0 to 1.3.0", updateSummary(ran, from, to, false))
	assert.Equal(t, "would update demo from 1.2.0 to 1.3.0", updateSummary(ran, from, to, true))
}

// No previous release is an install rather than an update, in both tenses.
func TestUpdateSummaryWithNothingInstalledBefore(t *testing.T) {
	to := summarised("demo", "1.3.0")
	ran := stepsWith(domain.StepSucceeded)

	assert.Equal(t, "installed demo 1.3.0", updateSummary(ran, domain.ReleaseRecord{}, to, false))
	assert.Equal(t, "would install demo 1.3.0", updateSummary(ran, domain.ReleaseRecord{}, to, true))
}

// An operation that did not succeed gets no summary at all, and a plan does not
// change that: the tense question only arises once there is something to report.
func TestUpdateSummarySaysNothingAboutAnOperationThatFailed(t *testing.T) {
	to := summarised("demo", "1.3.0")
	failed := domain.OperationRecord{Status: domain.StatusFailed}

	assert.Empty(t, updateSummary(failed, domain.ReleaseRecord{}, to, false))
	assert.Empty(t, updateSummary(failed, domain.ReleaseRecord{}, to, true))
}
