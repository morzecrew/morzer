package views_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui/views"
)

// A size an operator can weigh against an attachment limit.
//
// The unit boundaries are where this goes wrong silently: a 5MiB archive
// reported as "5 B" is a number somebody acts on, and the arithmetic that
// produces it is the kind nobody re-reads.
func TestASizeIsReadableAtEveryScale(t *testing.T) {
	for _, c := range []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1 << 20, "1.0 MiB"},
		{5 << 20, "5.0 MiB"},
		{1 << 30, "1.0 GiB"},
	} {
		out := render(t, 100, ops.SupportReport{
			Entries: []ops.SupportEntry{{Name: "journal.jsonl", Bytes: c.bytes}},
		})
		require.Containsf(t, out, c.want, "%d bytes did not render as %s:\n%s",
			c.bytes, c.want, out)
	}
}

// The plaintext warning is on every run, and an omission is shown as a gap.
//
// Both are the operator-facing half of decisions this feature rests on: the
// archive is readable by anyone who receives it, and a component that is missing
// is not a row with a zero in it.
func TestTheSupportViewShowsTheGapsAndTheWarning(t *testing.T) {
	out := render(t, 100, ops.SupportReport{
		Path: "/tmp/support-demo.tar.zst",
		Entries: []ops.SupportEntry{
			{Name: "journal.jsonl", Title: "The operation journal", Bytes: 2048, Redactions: 0},
		},
		Omitted: []ops.SupportOmission{
			{Name: "logs/", Reason: "the deployment produced no log output to capture"},
		},
	})

	require.Contains(t, out, "not encrypted",
		"an operator was not told the archive is readable by whoever receives it")
	require.Contains(t, out, "logs/", "a missing component is not shown as missing")
	require.Contains(t, out, "no log output")
	require.Contains(t, out, "/tmp/support-demo.tar.zst")

	// A zero redaction count is printed rather than left blank: an empty
	// cell reads as "not checked" on the one file where it matters.
	require.Regexp(t, `journal\.jsonl.*\b0\b`, flatten(out))
}

// A preview says it wrote nothing, in the title, where somebody looks first.
func TestThePreviewSaysItWroteNothing(t *testing.T) {
	out := render(t, 100, ops.SupportReport{
		Preview: true,
		Entries: []ops.SupportEntry{{Name: "meta.json", Bytes: 100}},
	})
	require.Contains(t, strings.ToLower(out), "nothing written")
}

// The check refuses to call a file clean, and separates that from not looking.
func TestTheRedactCheckViewNeverSaysClean(t *testing.T) {
	found := render(t, 100, ops.RedactCheckReport{
		Path: "/tmp/paste.txt", Armed: true, Redactions: 3,
	})
	require.Contains(t, found, "3 secret value(s)")
	require.Contains(t, found, "do not send this file")

	none := render(t, 100, ops.RedactCheckReport{Path: "/tmp/paste.txt", Armed: true})
	require.Contains(t, none, "no known secret")
	require.Contains(t, flatten(none), "not the same as clean")

	unchecked := render(t, 100, ops.RedactCheckReport{Path: "/tmp/paste.txt"})
	require.Contains(t, unchecked, "nothing could be checked")
	require.NotContains(t, unchecked, "no known secret",
		"a check that never ran reported the file as holding no known secret")
}

var _ = views.Version{}
