package main

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
)

// The page documenting `installation describe`, and the heading its exclusion
// table sits under.
const (
	describePage             = "reference/installation-commands.md"
	describeExclusionsAnchor = "#### What it leaves out, and why"
)

// checkDescribeExclusions holds the published exclusion table to what the code
// actually excludes.
//
// The table names the Installation fields `installation describe` leaves out of
// the document it writes, and it is a hand-written copy of
// `installationFieldsNotDescribed`. Measured 2026-08-21, before this check
// existed, the copy disagreed with the map in three of five rows: one had gone
// stale when a field was removed, and two had never been written down at all --
// one of them `attestation_salt`, whose reason for exclusion is that publishing
// it in a committed file makes an attestation digest brute-forceable.
//
// The page closed with "The list is not maintained by hand", which was true of
// the map and false of the copy. A claim like that is worse than no claim: it
// is why nobody re-read the table for the nine days it was wrong.
func checkDescribeExclusions(rep *report, root string) {
	rep.checks++

	path := filepath.Join(root, docsDir, filepath.FromSlash(describePage))
	body, err := os.ReadFile(path)
	if err != nil {
		rep.add("describe exclusions", "%s cannot be read: %v", describePage, err)
		return
	}

	documented, found := exclusionTable(string(body))
	if !found {
		rep.add("describe exclusions",
			"%s has no table under %q — the exclusions are published there, and a "+
				"check that cannot find the table is not a check",
			describePage, describeExclusionsAnchor)
		return
	}

	_, excluded, _ := domain.DescribedInstallationFields()
	serialised := installationFieldNames()

	want := map[string]bool{}
	for field := range excluded {
		name, ok := serialised[field]
		if !ok {
			// The map named something that is not a field. The domain's own
			// test refuses this; reaching it here means that test is gone.
			rep.add("describe exclusions",
				"installationFieldsNotDescribed excludes %q, which is not an "+
					"Installation field", field)
			continue
		}
		want[name] = true
	}

	for _, name := range sorted(want) {
		if !documented[name] {
			rep.add("describe exclusions",
				"%s excludes %q and %s does not list it — an operator reading "+
					"that page cannot tell the field is left out",
				"installation describe", name, describePage)
		}
	}
	for _, name := range sorted(documented) {
		if !want[name] {
			rep.add("describe exclusions",
				"%s lists %q as left out, and nothing excludes it — the table is "+
					"describing a struct that has moved on",
				describePage, name)
		}
	}
}

// exclusionTable reads the first-column entries of the table under the anchor.
//
// Reports whether it found the table at all, separately from finding it empty:
// a heading that has been renamed and a table that has lost its rows are
// different failures, and only one of them is silent.
func exclusionTable(body string) (map[string]bool, bool) {
	at := strings.Index(body, describeExclusionsAnchor)
	if at < 0 {
		return nil, false
	}

	names := map[string]bool{}
	seenRow := false
	for _, line := range strings.Split(body[at:], "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			if seenRow {
				break // prose after the table ends it
			}
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) == 0 {
			continue
		}
		first := strings.TrimSpace(cells[0])
		if first == "" || strings.Trim(first, "- :") == "" {
			continue // header separator
		}
		seenRow = true
		name := strings.Trim(first, "`")
		if name == "Not in the document" {
			continue // the header row
		}
		names[name] = true
	}
	return names, true
}

// installationFieldNames maps each Installation field to the name it is
// serialised under, which is the name the documentation uses because it is the
// name an operator sees in the file.
func installationFieldNames() map[string]string {
	t := reflect.TypeFor[domain.Installation]()
	out := make(map[string]string, t.NumField())
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("yaml")
		if tag == "" {
			tag = f.Tag.Get("json")
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			name = f.Name
		}
		out[f.Name] = name
	}
	return out
}

func sorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// excludedSerialisedNames is the exclusion set under the names the file uses.
// Exported to the test so its fixtures are built from the code rather than
// typed out, which would make the test a fourth copy of the same list.
func excludedSerialisedNames() map[string]bool {
	_, excluded, _ := domain.DescribedInstallationFields()
	serialised := installationFieldNames()
	out := make(map[string]bool, len(excluded))
	for field := range excluded {
		if name, ok := serialised[field]; ok {
			out[name] = true
		}
	}
	return out
}
