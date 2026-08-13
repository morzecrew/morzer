package views_test

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/internal/ui/theme"
	"github.com/morzecrew/morzer/internal/ui/views"
)

// The widths every view is pinned at.
//
// 60 is a split pane and the width at which a table has to drop something; 80
// is the convention; 100 is the measure; 400 is the wide screen that started
// all of this, and the only reason it is in the list is that its rendering must
// be indistinguishable from 100's.
var widths = []int{60, 80, 100, 400}

var update = flag.Bool("update", false, "rewrite the golden files")

// render draws a report at one width, with colour off.
//
// Colour off because a golden file full of escape sequences is a golden file
// nobody reviews, and the thing being pinned here is layout. The styled path is
// covered by the mode-fidelity test, which asserts the two carry the same
// fields, and by the theme's own tests.
func render(t *testing.T, width int, value any) string {
	t.Helper()
	t.Setenv("COLUMNS", fmt.Sprint(width))

	var b bytes.Buffer
	require.NoError(t, ui.Render(&b, ui.ModeRich, theme.New(false, true), value))
	return b.String()
}

// golden compares against the committed rendering, or rewrites it under
// -update.
func golden(t *testing.T, name, got string) {
	t.Helper()

	path := filepath.Join("testdata", name+".txt")
	if *update {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(path, []byte(got), 0o600))
		return
	}

	want, err := os.ReadFile(path)
	require.NoError(t, err, "missing golden file; run `go test ./internal/ui/views -update`")
	require.Equal(t, string(want), got, "%s changed; review the diff and re-run with -update", path)
}

// TestGoldenRenders pins every view at every width.
//
// A golden file is the only test that catches "this still renders, and it looks
// wrong". The assertions below say what must be true; the file says what it
// looks like, and a diff in one is a conversation rather than a failure.
func TestGoldenRenders(t *testing.T) {
	for _, f := range fixtures() {
		for _, width := range widths {
			t.Run(fmt.Sprintf("%s/%d", f.name, width), func(t *testing.T) {
				golden(t, fmt.Sprintf("%s-%d", f.name, width), render(t, width, f.value))
			})
		}
	}
}

// TestNothingIsJustifiedToTheViewport is the regression test for the complaint
// that started RFC 0019.
//
// `doctor` computed `description = width - width/3 - 12`, so the description
// column *was* the terminal: a check and the sentence explaining it sat 20
// spaces apart at 100 columns, 87 at 200 and 207 at 380 — at opposite edges of
// a wide screen. The bug is not the size of any one gap, it is that the gap is a
// function of the screen, so that is what this asserts: whatever the widest gap
// in a rendering is, it must not grow when the screen does.
//
// Preferred over an absolute bound because a table's alignment padding is
// data-derived and legitimately exceeds any constant — one 30-character secret
// name beside a 3-character one is 27 spaces of gap that no rule should forbid.
// An absolute bound would have been deleted the first time somebody added a long
// name; this one fails only on the defect it names.
func TestNothingIsJustifiedToTheViewport(t *testing.T) {
	for _, f := range fixtures() {
		t.Run(f.name, func(t *testing.T) {
			base := widestGap(render(t, 100, f.value))
			for _, width := range []int{200, 400, 1000} {
				got := widestGap(render(t, width, f.value))
				require.LessOrEqual(t, got, base,
					"the widest gap grew from %d columns at 100 to %d at %d: "+
						"something is sized from the screen rather than the content",
					base, got, width)
			}
		})
	}
}

// TestWrappedTextFitsTheMeasure asserts that prose stops at the measure.
//
// Table rows are exempt and have to be: a table whose columns genuinely need
// 130 characters may use them, packed left, ending where its content ends — the
// cap is on the measure, not on the use of the screen. What is asserted here is
// everything else, which is the text an operator reads a line at a time.
func TestWrappedTextFitsTheMeasure(t *testing.T) {
	for _, f := range fixtures() {
		for _, width := range widths {
			if f.verbatim {
				continue
			}
			t.Run(fmt.Sprintf("%s/%d", f.name, width), func(t *testing.T) {
				measure := min(width, ui.MaxContentWidth)
				for _, line := range strings.Split(render(t, width, f.value), "\n") {
					if isTableRow(line) {
						continue
					}
					require.LessOrEqualf(t, ui.Width(line), measure,
						"a line runs past the measure:\n%q", line)
				}
			})
		}
	}
}

// TestPlainCarriesEveryFieldRichDoes is the mode-fidelity half that catches a
// styled view quietly dropping a column.
func TestPlainCarriesEveryFieldRichDoes(t *testing.T) {
	for _, f := range fixtures() {
		t.Run(f.name, func(t *testing.T) {
			t.Setenv("COLUMNS", "100")

			var rich, plain bytes.Buffer
			require.NoError(t, ui.Render(&rich, ui.ModeRich, theme.New(true, true), f.value))
			require.NoError(t, ui.Render(&plain, ui.ModePlain, nil, f.value))

			require.NotContains(t, plain.String(), "\x1b",
				"plain output carries an escape sequence")

			for _, word := range f.fields {
				require.Containsf(t, plain.String(), word, "plain dropped %q", word)
				require.Containsf(t, stripANSI(rich.String()), word, "rich dropped %q", word)
			}
		})
	}
}

// widestGap is the longest run of spaces between two pieces of content.
//
// Leading indentation is excluded because it is structure rather than distance,
// and trailing space cannot exist — the document trims it.
func widestGap(text string) int {
	widest := 0
	for _, line := range strings.Split(stripANSI(text), "\n") {
		trimmed := strings.TrimLeft(line, " ")
		run := 0
		for _, r := range trimmed {
			if r == ' ' {
				run++
				if run > widest {
					widest = run
				}
				continue
			}
			run = 0
		}
	}
	return widest
}

// isTableRow reports whether a line is part of a table.
//
// Tables here are drawn at an indent of four or more with two-space gutters;
// prose at that indent is a check message or a remedy, which never contains a
// run of two spaces. That is the distinguishing feature and it is why the
// components use one gutter constant.
func isTableRow(line string) bool {
	trimmed := strings.TrimLeft(stripANSI(line), " ")
	return strings.Contains(trimmed, "  ")
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// fixture is one report and the words its rendering must contain.
type fixture struct {
	name   string
	value  any
	fields []string

	// verbatim marks a report whose whole output is a value something
	// substitutes, so the measure does not apply to it. Declared per
	// fixture rather than inferred, because "this line may run past the
	// measure" is a decision about the report and not a property a test
	// should discover.
	verbatim bool
}

func fixtures() []fixture {
	all := statusFixtures()
	all = append(all, doctorFixtures()...)
	all = append(all, listFixtures()...)
	all = append(all, machineFixtures()...)
	all = append(all, inspectFixtures()...)
	all = append(all, calloutFixtures()...)
	all = append(all, fleetFixtures()...)
	return all
}

var _ = views.Verbose{}
