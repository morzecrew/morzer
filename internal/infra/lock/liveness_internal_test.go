package lock

import "testing"

// The PID-reuse guard's decision, driven directly.
//
// RFC 0029 §8 requires this of P1, and names why: on darwin `pidStart` cannot
// answer at all (`pidstart_darwin.go` returns zero by construction), so the
// question "what does the guard do when the start time is unknown" stops being
// hypothetical the moment the tree compiles for macOS.
//
// The answer has to be **assume the holder is live**, and the asymmetry is the
// whole point. Treating unknown as "gone" would let a second operation take a
// lock somebody is holding: two deployments against one installation, which is
// the single thing this guard exists to prevent. Treating unknown as "live"
// costs an operator a wait for a lock that was already free.
func TestAnUnknownStartTimeIsNotEvidenceTheHolderIsGone(t *testing.T) {
	for name, c := range map[string]struct {
		recorded, live uint64
		want           bool
	}{
		// The one case that is evidence: both known, and different.
		"a different process wearing the same PID": {recorded: 111, live: 222, want: true},

		"the same process":                {recorded: 111, live: 111, want: false},
		"this platform cannot report one": {recorded: 111, live: 0, want: false},
		"the record predates the field":   {recorded: 0, live: 222, want: false},
		"neither side is known":           {recorded: 0, live: 0, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := startTimeContradicts(c.recorded, c.live); got != c.want {
				t.Errorf("startTimeContradicts(%d, %d) = %v, want %v",
					c.recorded, c.live, got, c.want)
			}
		})
	}
}

// And the darwin implementation is that platform, so its answer is pinned where
// it can be read next to the rule above rather than inferred from a file this
// build does not compile.
//
// Asserted through the same function the guard calls, with the zero
// `pidstart_darwin.go` returns: a machine that cannot report a start time must
// never have its lock treated as stale.
func TestTheDarwinStubCannotMakeALockLookStale(t *testing.T) {
	const anyRecordedStartTime = 987654

	if startTimeContradicts(anyRecordedStartTime, 0) {
		t.Error("a platform that cannot report a start time made the lock look stale, " +
			"which is how two deployments end up running at once")
	}
}
