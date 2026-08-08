package lock

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// statLine builds a /proc/<pid>/stat line with comm as the executable name and
// start as field 22, so each case below differs in exactly one thing.
//
// The fields after "(comm) " are state (3) onward, which puts the start time at
// index 19 of the remainder -- the arithmetic the parser depends on, spelled
// out here so a test failure points at the offset rather than at the fixture.
func statLine(pid int, comm string, start string) []byte {
	after := []string{
		"S", "1", "1", "1", "0", "-1", "4194560", // state .. flags
		"100", "0", "0", "0", "10", "5", "0", "0", // faults and times
		"20", "0", "1", "0", // priority, nice, threads, itrealvalue
		start, // field 22
		"0", "0", "0",
	}
	return []byte(fmt.Sprintf("%d (%s) %s\n", pid, comm, strings.Join(after, " ")))
}

// TestParsePIDStartSurvivesTheCommField.
//
// comm is whatever the executable was called, wrapped in parentheses, and the
// kernel does not escape it. A reader that splits the whole line on whitespace
// gets a different field 22 for `my prog` than for `bash`, and one that scans
// forward for ')' stops inside a name that contains one. Both produce a
// plausible number rather than an error, which is the dangerous shape: a wrong
// start time makes every live holder look recycled, and `status` starts
// reporting held locks as free.
func TestParsePIDStartSurvivesTheCommField(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		comm string
	}{
		{"an ordinary name", "bash"},
		{"a name with a space", "my prog"},
		{"a name with several spaces", "a b c d"},
		{"a name containing a closing paren", "weird)name"},
		{"a name that is only punctuation", "((("},
		{"an empty name", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := parsePIDStart(statLine(4242, c.comm, "987654"))
			if got != 987654 {
				t.Errorf("comm %q gave start %d, want 987654 -- field 22 was "+
					"read from the wrong offset", c.comm, got)
			}
		})
	}
}

// Anything unreadable is zero, which ownerAlive treats as "unknown" and falls
// back to the PID alone. Zero must never be a *parsed* value either, or the
// fallback would trigger on a process that genuinely started at boot tick 0.
func TestParsePIDStartRefusesWhatItCannotRead(t *testing.T) {
	t.Parallel()

	short := statLine(1, "bash", "987654")
	truncated := short[:len(short)/2]

	for _, c := range []struct {
		name string
		in   []byte
	}{
		{"empty", nil},
		{"no closing paren", []byte("1234 bash S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 0 0 0 0 99")},
		{"nothing after the comm", []byte("1234 (bash)")},
		{"too few fields", []byte("1234 (bash) S 1 1")},
		{"a truncated line", truncated},
		{"a non-numeric start time", statLine(1, "bash", "not-a-number")},
		{"a negative start time", statLine(1, "bash", "-1")},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := parsePIDStart(c.in); got != 0 {
				t.Errorf("got %d, want 0 -- an unreadable stat line must read "+
					"as unknown rather than as a start time", got)
			}
		})
	}
}

// And against the real thing, so the fixture above cannot drift away from the
// format the kernel actually writes.
func TestParsePIDStartAgreesWithProcfs(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", os.Getpid()))
	if err != nil {
		t.Skip("no procfs on this platform")
	}
	if got := parsePIDStart(data); got == 0 {
		t.Fatalf("this process's own stat line parsed as unknown:\n%s", data)
	}
}
