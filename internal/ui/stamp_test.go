package ui_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/ui"
)

// Each line is stamped with the time it began, however the writes split it,
// and an empty line is left empty.
func TestStampLinesTimesEachLineWhenItBegins(t *testing.T) {
	var out bytes.Buffer
	clock := time.Date(2026, 11, 19, 17, 21, 5, 0, time.FixedZone("MSK", 3*3600))
	w := ui.StampLines(&out, func() time.Time { return clock })

	for _, write := range []struct {
		text  string
		after time.Duration
	}{
		{"[1/2] stage", time.Second},
		{" release\n\nerror: refused\n", time.Second},
		{"hint:  run it again\n", 0},
	} {
		if n, err := w.Write([]byte(write.text)); err != nil || n != len(write.text) {
			t.Fatalf("Write(%q) = %d, %v", write.text, n, err)
		}
		clock = clock.Add(write.after)
	}

	want := "2026-11-19T14:21:05Z [1/2] stage release\n" +
		"\n" +
		"2026-11-19T14:21:06Z error: refused\n" +
		"2026-11-19T14:21:07Z hint:  run it again\n"
	if out.String() != want {
		t.Errorf("stamped\n%s\nwant\n%s", out.String(), want)
	}
}
