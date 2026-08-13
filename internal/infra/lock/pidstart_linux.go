package lock

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// pidStart reads the kernel's start time for a PID, in clock ticks since boot.
//
// Field 22 of /proc/<pid>/stat, counted after the comm field rather than by
// splitting the whole line: comm is the executable name in parentheses and may
// contain spaces, which is what breaks the naive split.
//
// Zero when it cannot be read -- a kernel without procfs, a PID that has
// already gone. Callers treat zero as "unknown" and fall back.
func pidStart(pid int) uint64 {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	return parsePIDStart(data)
}

// parsePIDStart pulls field 22 out of a /proc/<pid>/stat line.
//
// Split from the read so the parsing is testable without a process to point
// at: the shapes that break a naive reader -- a comm with a space in it, a
// comm with a parenthesis in it, a truncated line -- are the ones no live
// process on the machine running the tests is likely to have.
//
// Zero for anything it cannot read, which callers treat as "unknown".
func parsePIDStart(data []byte) uint64 {
	// Last ')' rather than first: comm is the executable name in parentheses
	// and may itself contain one, so scanning forward finds the wrong end.
	commEnd := bytes.LastIndexByte(data, ')')
	if commEnd < 0 || commEnd+2 >= len(data) {
		return 0
	}
	// After "(comm) " the fields are state (3) onward, so the start time --
	// field 22 -- is the 20th of what remains.
	fields := strings.Fields(string(data[commEnd+2:]))
	const startTimeOffset = 19
	if len(fields) <= startTimeOffset {
		return 0
	}
	v, err := strconv.ParseUint(fields[startTimeOffset], 10, 64)
	if err != nil {
		return 0
	}
	return v
}
