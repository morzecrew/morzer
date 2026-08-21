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

// TestNoExclusionNamesAFieldThatIsGone.
//
// The other direction, and it was missing. accountFields ranges over the
// installation's fields and looks each one up in the reasons map; nothing
// ranged over the map. So a field *added* and forgotten failed the build, and a
// reason left behind for a field that no longer exists sat there indefinitely,
// documenting a decision about a document that had moved on.
//
// Found by removing `Providers` in wave 36 rather than by adding anything:
// every earlier change to Installation moved in the direction the check already
// covered. Its sibling table in sandbox_test.go has asserted both directions
// all along, which is what made the asymmetry visible once one of them was
// asked a question the other had already answered.
func TestNoExclusionNamesAFieldThatIsGone(t *testing.T) {
	real := map[string]bool{}
	for _, name := range fieldNames(reflect.TypeFor[Installation]()) {
		real[name] = true
	}

	for field := range installationFieldsNotDescribed {
		if !real[field] {
			t.Errorf("installationFieldsNotDescribed excludes %q, which is not an "+
				"Installation field; the exclusion is describing a struct that "+
				"has moved on", field)
		}
	}
}
