package clitest_test

import (
	"testing"

	"github.com/morzecrew/morzer/test/clitest"
)

// What a plan says it did, as opposed to what it says it would do.
//
// An earlier wave fixed this for `init`, where a plan printed "installation
// created for demo" — a creation claimed in the past tense, directly beneath
// the line saying nothing was changed. The fix grew `initVerb` and stopped
// there, and the identical defect stayed live in the two commands that share
// the same shape: one summary function, called on both paths, that never learns
// which one it is on.
//
// Nothing pinned either of them. That is why the sibling fix did not reach
// them, and it is why these tests assert both halves — the sentence a plan must
// print, and the sentence it must not.

func TestAPlannedApplySaysItWouldApply(t *testing.T) {
	r := clitest.NewInstalled(t)

	res := r.Run("apply", "--dry-run").ExitCode(0)

	res.OutputContains("would apply demo 1.2.0")
	res.NoOutputContains("demo 1.2.0 applied")
}

func TestAPlannedUpdateSaysItWouldUpdate(t *testing.T) {
	r := clitest.NewInstalled(t)

	res := r.Run("update", r.Bundle, "--dry-run").ExitCode(0)

	res.OutputContains("would update demo")
	res.NoOutputContains("updated demo from")
}
