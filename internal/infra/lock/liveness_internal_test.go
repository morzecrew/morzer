package lock

import "testing"

// The PID-reuse guard's decision, driven directly.
//
// RFC 0029 §8 requires this of P1, and names why: `pidStart` cannot always
// answer -- a PID that has gone, a kernel without procfs -- so the question
// "what does the guard do when the start time is unknown" is never
// hypothetical.
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

		"the same process":              {recorded: 111, live: 111, want: false},
		"the live start cannot be read": {recorded: 111, live: 0, want: false},
		"the record predates the field": {recorded: 0, live: 222, want: false},
		"neither side is known":         {recorded: 0, live: 0, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := startTimeContradicts(c.recorded, c.live); got != c.want {
				t.Errorf("startTimeContradicts(%d, %d) = %v, want %v",
					c.recorded, c.live, got, c.want)
			}
		})
	}
}
