package main

import (
	"strings"
	"testing"
)

// RFC 0023 decision 7a was OPEN because the draft assumed a mechanism that does
// not exist: it said the string `docker` was already forbidden above the
// adapters, and §2 measured that depguard is an import linter and cannot see a
// name. These tests are what makes the replacement real — decision 7a's
// "deliberately failing fixture", in both of the rule's halves.

// TestTheInventoryMatchesTheTree is the drift gate, and the reason the number
// in RFC 0023 P1 stays true after the wave that produced it.
//
// Both directions. A new leak fails because it is not listed; a *fixed* leak
// fails because its entry is stale — an allowlist checked one way only ever
// grows, and this list exists to shrink.
func TestTheInventoryMatchesTheTree(t *testing.T) {
	found, err := Check("../..")
	if err != nil {
		t.Fatal(err)
	}

	unexpected, stale := reconcile(found, allowed())

	for _, f := range unexpected {
		t.Errorf("not in the inventory: %s", f)
	}
	for _, s := range stale {
		t.Errorf("inventory entry describes something that no longer exists: %s\n"+
			"delete it — the number is meant to fall", s)
	}
}

// TestEveryEntryCarriesItsClassificationAndItsExit.
//
// The inventory's value is not the list of names, which a grep produces in a
// second. It is that each name was *decided about*: port-shaped means a rename,
// compose-shaped means it moves below the boundary, catalogue means it stays.
// An entry with no reason is a name somebody added to make the build pass.
func TestEveryEntryCarriesItsClassificationAndItsExit(t *testing.T) {
	for _, e := range inventory {
		switch e.Class {
		case PortShaped, ComposeShaped, Catalogue:
		default:
			t.Errorf("%s %s: unclassified (%q)", e.File, e.Symbol, e.Class)
		}
		if len(strings.TrimSpace(e.Why)) < 40 {
			t.Errorf("%s %s: no real reason given: %q", e.File, e.Symbol, e.Why)
		}
		// A catalogue entry is the one class with nothing to remove it:
		// a table of tool names grows when a runtime is added.
		if e.Class != Catalogue && strings.TrimSpace(e.Removes) == "" {
			t.Errorf("%s %s: classified %s with no exit — what takes it off the list?",
				e.File, e.Symbol, e.Class)
		}
	}
}

func TestAVocabularyLeakIsCaught(t *testing.T) {
	found, err := Check("testdata/leaky")
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"type ComposeStack",
		"field QuadletUnits",
		"field DockerHost",
		"func PodmanSocket",
		"name composeFiles", // `:=` is a declaration too

		// A checker that read only declarations would pass a file called
		// podman.go whose every symbol was neutrally named -- and the
		// loudest leak in the real tree is a file name, not a symbol.
		"file quadlet_helpers.go",
		"package composeui",
	}
	for _, w := range want {
		if !hasSymbol(found, w) {
			t.Errorf("the checker did not see %q", w)
		}
	}
}

// TestABranchOnRuntimeKindIsCaught is decision 7 itself.
//
// A conditional on runtime kind above the adapters is the abstraction failing in
// the one way that looks like progress, so it is the rule with no allowlist —
// asserted here by running the fixture through `reconcile`, which is what
// decides whether a finding can be silenced.
func TestABranchOnRuntimeKindIsCaught(t *testing.T) {
	found, err := Check("testdata/leaky")
	if err != nil {
		t.Fatal(err)
	}

	var branches []Finding
	for _, f := range found {
		if f.Rule == "branch" {
			branches = append(branches, f)
		}
	}
	if len(branches) != 2 {
		t.Fatalf("want the if and the case, got %d: %v", len(branches), branches)
	}

	// Handed an allowlist that *does* list every branch, by position and
	// symbol, exactly as a vocabulary entry would be listed. They must be
	// reported anyway.
	//
	// Against the real inventory this proves nothing: there is no branch
	// entry, so a finding lands in `unexpected` whether the rule refusing to
	// allowlist branches exists or not. Deleting that rule survived the
	// mutation sweep until this test handed reconcile the allowlist.
	silenced := map[string]Entry{}
	for _, b := range branches {
		silenced[b.File+"\x00"+b.Symbol] = Entry{
			File: b.File, Symbol: b.Symbol, Class: PortShaped,
			Why: "an entry somebody added to make the build pass", Removes: "n/a",
		}
	}

	unexpected, _ := reconcile(found, silenced)
	for _, b := range branches {
		if !containsFinding(unexpected, b) {
			t.Errorf("%s was allowlisted; a runtime branch must not be", b)
		}
	}
}

// TestProseIsNotABranch pins the precision the branch rule needs to be usable.
//
// Go spells string concatenation with the same node as comparison, so the first
// version of this rule reported every help string that mentioned Docker — nine
// of them, none a decision about anything. A rule that cries wolf on the
// command's own `--help` text is a rule somebody switches off.
func TestProseIsNotABranch(t *testing.T) {
	found, err := Check("testdata/clean")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found {
		t.Errorf("prose reported as a finding: %s", f)
	}
}

// TestAnAdapterMayNameItsRuntime.
//
// The boundary is the whole point: an adapter that could not say "quadlet"
// would be an adapter that could not do its job. If this fails, the rule has
// stopped being about the boundary and started being about the word.
func TestAnAdapterMayNameItsRuntime(t *testing.T) {
	found, err := Check("testdata/leaky")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found {
		if strings.Contains(f.File, "internal/adapters/") {
			t.Errorf("an adapter was reported: %s", f)
		}
	}
}

// TestAFixedLeakFailsUntilTheInventoryCatchesUp drives the stale direction,
// which the real tree cannot exercise: today every entry matches something.
func TestAFixedLeakFailsUntilTheInventoryCatchesUp(t *testing.T) {
	found, err := Check("testdata/clean") // nothing at all
	if err != nil {
		t.Fatal(err)
	}

	_, stale := reconcile(found, allowed())
	if len(stale) != len(inventory) {
		t.Fatalf("want every entry reported stale against an empty tree, got %d of %d",
			len(stale), len(inventory))
	}
}

// TestTheGateFailsAndSaysWhere is the end-to-end shape CI depends on.
//
// The rules were tested through `Check` and `reconcile`, which is the analysis.
// This is the part an engineer actually meets: a non-zero exit, and a message
// naming the file, the line and what to do. A checker that is right and
// illegible gets suppressed rather than obeyed.
func TestTheGateFailsAndSaysWhere(t *testing.T) {
	var out strings.Builder

	err := run("testdata/leaky", &out)
	if err == nil {
		t.Fatal("a tree full of leaks passed")
	}

	report := out.String() + err.Error()
	for _, want := range []string{
		"internal/ports/runtime.go", // the file
		"type ComposeStack",         // the symbol
		"quadlet",                   // the word that triggered it
		"inventory.go",              // where to record a decision
		"decision 7",                // and the one rule with no such option
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not mention %q:\n%s", want, report)
		}
	}
}

// TestTheGatePassesOnThisRepository is the same path over the real tree, which
// is what makes `just runtime-check` in CI a check rather than a hope.
func TestTheGatePassesOnThisRepository(t *testing.T) {
	var out strings.Builder
	if err := run("../..", &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "0 runtime branches") {
		t.Errorf("the summary does not report the branch count: %q", out.String())
	}
}

// TestTheInventoryPrintsAsATable.
//
// `just runtime-inventory` is P1's readable deliverable — the list, with each
// entry's class and its exit. It had no test at all, which coverage found and
// the mutation sweep could not: nothing I thought to break lived here.
func TestTheInventoryPrintsAsATable(t *testing.T) {
	var out strings.Builder
	printInventory(&out)
	printed := out.String()

	rows := strings.Count(printed, "\n| `")
	if rows != len(inventory) {
		t.Errorf("printed %d rows for %d entries", rows, len(inventory))
	}

	total := 0
	for _, c := range counts() {
		total += c.N
		if !strings.Contains(printed, string(c.Class)) {
			t.Errorf("the summary omits the %s class", c.Class)
		}
	}
	if total != len(inventory) {
		t.Errorf("the class counts total %d, the inventory holds %d — an entry is "+
			"classified as something the summary does not print", total, len(inventory))
	}
}

func hasSymbol(found []Finding, symbol string) bool {
	for _, f := range found {
		if f.Symbol == symbol {
			return true
		}
	}
	return false
}

func containsFinding(list []Finding, want Finding) bool {
	for _, f := range list {
		if f == want {
			return true
		}
	}
	return false
}
