package ui_test

import (
	"bytes"
	"errors"
	"strings"
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

// failing takes the first budget bytes of its first write and refuses the
// rest, then takes everything.
type failing struct {
	out    bytes.Buffer
	budget int
	failed bool
}

func (f *failing) Write(p []byte) (int, error) {
	if f.failed {
		return f.out.Write(p)
	}
	f.failed = true
	n := min(f.budget, len(p))
	f.out.Write(p[:n])
	return n, errors.New("disk full")
}

// A write that fails partway reports the bytes of p that went out, so a
// caller that writes p[n:] again ends with each line stamped once and nothing
// twice, wherever the failure fell.
func TestStampLinesRecoversFromAWriteThatFailedPartway(t *testing.T) {
	const stamp = "2026-11-19T14:21:05Z "
	now := func() time.Time { return time.Date(2026, 11, 19, 14, 21, 5, 0, time.UTC) }
	for _, c := range []struct {
		name   string
		write  string
		budget int
		n      int
	}{
		{"nothing went out", "ab\n", 0, 0},
		{"nothing of a line not yet ended went out", "ab", 0, 0},
		{"part of the time", "ab\n", 5, 0},
		{"the time and part of the line", "ab\n", len(stamp) + 1, 1},
		{"a line and part of the next time", "ab\ncd\n", len(stamp) + 3 + 5, 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			dest := &failing{budget: c.budget}
			w := ui.StampLines(dest, now)
			n, err := w.Write([]byte(c.write))
			if err == nil || n != c.n {
				t.Fatalf("Write = %d, %v; want %d and the error", n, err, c.n)
			}
			if n, err := w.Write([]byte(c.write)[n:]); err != nil || n != len(c.write)-c.n {
				t.Fatalf("retry = %d, %v", n, err)
			}
			want := strings.TrimSuffix(stamp+strings.ReplaceAll(c.write, "\n", "\n"+stamp), stamp)
			if dest.out.String() != want {
				t.Errorf("wrote %q, want %q", dest.out.String(), want)
			}
		})
	}
}
