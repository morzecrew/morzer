package ops

import (
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
)

// TestUpdateCheckPermission is the whole of the phone-home policy.
//
// Three paths, and the natural reading is wrong in one of them: an operator
// typing `morzer update --check` is the consent, so refusing that because a
// persisted flag is false would be the manager arguing with a direct
// instruction. The unprompted paths -- doctor, status, a timer -- are where the
// setting has to hold.
func TestUpdateCheckPermission(t *testing.T) {
	cases := []struct {
		name     string
		setting  bool
		explicit bool
		want     bool
	}{
		{"unprompted and unset stays off", false, false, false},
		{"unprompted and enabled runs", true, false, true},
		{"explicit runs even when unset", false, true, true},
		{"explicit runs when enabled", true, true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := domain.UpdateConfig{Check: tc.setting}
			if got := cfg.CheckAllowed(tc.explicit); got != tc.want {
				t.Errorf("CheckAllowed(%t) = %t, want %t", tc.explicit, got, tc.want)
			}
		})
	}
}

// TestAbsentMeansOff. A zero UpdateConfig -- a hand-edited file, a record
// written before the field existed -- must not contact anything.
func TestAbsentMeansOff(t *testing.T) {
	var zero domain.UpdateConfig
	if zero.CheckAllowed(false) {
		t.Error("an absent setting enabled an unprompted registry query")
	}
}
