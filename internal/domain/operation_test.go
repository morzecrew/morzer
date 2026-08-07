package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestDurationIsArithmeticRatherThanAClockRead.
//
// The domain package is otherwise pure -- no clocks, no filesystem -- and this
// was the one place a wall-clock read hid inside a getter. It still reads one
// for the convenience of callers that have no clock of their own, but the
// arithmetic is a function of its arguments and can be asserted as one.
func TestDurationIsArithmeticRatherThanAClockRead(t *testing.T) {
	start := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

	running := OperationRecord{StartedAt: NewTime(start)}
	assert.Equal(t, 90*time.Second,
		running.DurationAt(start.Add(90*time.Second)),
		"a running operation's duration is time so far")

	finished := OperationRecord{
		StartedAt:  NewTime(start),
		FinishedAt: NewTime(start.Add(2 * time.Minute)),
	}
	assert.Equal(t, 2*time.Minute, finished.DurationAt(start.Add(time.Hour)),
		"a finished operation's duration is a property of the record, not of "+
			"when somebody asks")

	// A record that never started has no duration rather than one measured
	// from the zero time, which would read as fifty-five years.
	assert.Zero(t, OperationRecord{}.DurationAt(start))
}
