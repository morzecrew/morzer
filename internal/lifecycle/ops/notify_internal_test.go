package ops

import (
	"testing"

	"github.com/morzecrew/morzer/internal/events"
)

// TestEveryEventKindIsClassified.
//
// The allowlist decides what leaves the machine. Written as a map of only the
// forwarded kinds, a Kind added to the events package later would default to
// not-forwarded -- which is the safe direction, and indistinguishable from
// somebody having thought about it.
//
// So every kind carries an explicit entry and this fails until a new one does
// too. The assertion is against events.AllKinds rather than a list here,
// because a copy of the list is the thing that goes stale.
func TestEveryEventKindIsClassified(t *testing.T) {
	for _, k := range events.AllKinds {
		if _, ok := forwardedKinds[k]; !ok {
			t.Errorf("event kind %q is not classified in forwardedKinds: "+
				"decide whether it may leave the machine", k)
		}
	}
	if len(forwardedKinds) != len(events.AllKinds) {
		t.Errorf("forwardedKinds has %d entries for %d kinds: "+
			"an entry naming a kind that no longer exists is dead policy",
			len(forwardedKinds), len(events.AllKinds))
	}
}

// TestStepOutputNeverLeavesTheMachine is called out separately because it is
// the one entry whose flipping would be a security regression rather than a
// preference: it carries raw hook and compose output, and the "events carry no
// secrets" claim rests on a redaction handler that has been wrong once.
func TestStepOutputNeverLeavesTheMachine(t *testing.T) {
	if forwardedKinds[events.KindStepOutput] {
		t.Error("step.output must never be forwarded to a third party")
	}
}
