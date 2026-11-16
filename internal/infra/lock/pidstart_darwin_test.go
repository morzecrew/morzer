package lock

import (
	"os"
	"testing"
	"time"
)

// The darwin start time, which only the macOS lane runs (RFC 0055 §5.8). Named
// for the file under test so the build constraint is the filename rather than
// a tag somebody has to notice (RFC 0029 decision 8).

// A live process's start time is stable, and in the unit pidstart_darwin.go
// says: a figure in ticks or in seconds would put this test binary's start
// decades away from now, and a conversion that moved with the clock would
// differ between two reads.
func TestPIDStartIsThisProcesssOwnAndStable(t *testing.T) {
	first := pidStart(os.Getpid())
	if first == 0 {
		t.Fatal("this process's own start time could not be read")
	}
	if again := pidStart(os.Getpid()); again != first {
		t.Fatalf("this process's start time moved from %d to %d between two reads", first, again)
	}
	started := time.UnixMicro(int64(first))
	if since := time.Since(started); since < 0 || since > 24*time.Hour {
		t.Errorf("this process started at %s, %s ago, which is not microseconds since the epoch", started, since)
	}
}

// A recorded start one unit off the live one is a different process wearing
// the same PID.
func TestAStartOneMicrosecondOffIsAContradiction(t *testing.T) {
	live := pidStart(os.Getpid())
	if !startTimeContradicts(live+1, live) {
		t.Error("a record one microsecond off this process's start was taken for this process")
	}
}

// A PID nothing holds is unknown, not an answer: zero reaches
// `startTimeContradicts` as silence, so the holder is assumed live.
func TestPIDStartCannotReadAProcessThatIsNotThere(t *testing.T) {
	if got := pidStart(-1); got != 0 {
		t.Errorf("pidStart(-1) = %d, want 0 (unknown)", got)
	}
}
