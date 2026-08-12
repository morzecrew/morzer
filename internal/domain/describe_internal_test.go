package domain

import (
	"reflect"
	"testing"
)

// TestAFieldNobodyAccountedForIsReported.
//
// The detector, exercised. TestEveryInstallationFieldIsAccounted asserts that
// no Installation field is unaccounted for, which is true today and would stay
// true if the code that finds them were deleted — a mutation that removed the
// branch survived the whole suite. So the branch is driven here against names
// it must object to, rather than only against the real types where there is
// nothing to object to.
func TestAFieldNobodyAccountedForIsReported(t *testing.T) {
	carried, excluded, unaccounted := accountFields(
		[]string{"Product", "CreatedAt", "SomethingNewNobodyClassified"},
		[]string{"Product"},
		map[string]string{"CreatedAt": "history, not a choice"},
	)

	if !reflect.DeepEqual(carried, []string{"Product"}) {
		t.Errorf("carried: %v", carried)
	}
	if excluded["CreatedAt"] == "" {
		t.Error("an excluded field lost its reason")
	}
	if !reflect.DeepEqual(unaccounted, []string{"SomethingNewNobodyClassified"}) {
		t.Errorf("the unaccounted field was not reported: %v", unaccounted)
	}
}
