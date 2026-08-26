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

// An apply that failed does not print a sentence saying it applied.
//
// The same defect as the tense, one axis over: `applySummary` asked what the
// steps did and never what the operation did, so a rolled-back run printed
// "demo 1.2.0 applied" between "earlier changes were rolled back" and the error
// explaining why. `updateSummary` has always checked the status; this is the
// half of that pair that never did.
//
// The apply here fails at pull-images for want of a container runtime, which is
// what makes the assertion cheap: any failure past the first step reaches the
// summary.
func TestAFailedApplyDoesNotSayItApplied(t *testing.T) {
	r := clitest.NewInstalled(t)

	res := r.Run("apply")

	res.ExitCode(11)
	res.NoOutputContains("demo 1.2.0 applied")
	res.NoOutputContains("is already applied")
}
