package release_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/adapters/verify/checksum"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

// The checksum list is what a signature covers, and the verifier fails closed
// on a file the list does not mention. So the property that matters is
// completeness, and it is asserted by counting rather than by sampling -- the
// documented publishing procedure prescribed a manual cross-check of exactly
// this, which is documentation admitting the step is easy to get wrong.

// TestSumsCoverEveryFileExactlyOnce.
func TestSumsCoverEveryFileExactlyOnce(t *testing.T) {
	dir := bundle(t, nil)

	if err := release.WriteSums(dir); err != nil {
		t.Fatal(err)
	}

	listed := sumsPaths(t, dir)
	var present []string
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		// The list cannot list itself, and the signature over it is
		// what makes the list trustworthy.
		if rel == ports.SumsFileName || rel == ports.SignatureFileName {
			return nil
		}
		present = append(present, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(listed) != len(present) {
		t.Errorf("%s lists %d files and the bundle contains %d\n listed: %v\npresent: %v",
			ports.SumsFileName, len(listed), len(present), listed, present)
	}
	seen := map[string]int{}
	for _, p := range listed {
		seen[p]++
	}
	for _, p := range present {
		if seen[p] != 1 {
			t.Errorf("%s appears %d times in %s, want once", p, seen[p], ports.SumsFileName)
		}
	}
}

// TestAGeneratedSumsFileSatisfiesTheVerifier closes the loop between the two
// halves of the chain.
//
// The writer and the checker are different code in different packages, and the
// rule they must agree on -- which files the list exempts -- is spelled in
// both. A disagreement makes every bundle this tool produces fail the tool's
// own verification, so it is worth one assertion rather than a shared comment.
func TestAGeneratedSumsFileSatisfiesTheVerifier(t *testing.T) {
	dir := bundle(t, nil)

	if err := release.WriteSums(dir); err != nil {
		t.Fatal(err)
	}
	if err := checksum.VerifySumsFile(dir); err != nil {
		t.Fatalf("a freshly written checksum list does not verify: %v", err)
	}

	// And a file added afterwards is caught, so the assertion above is
	// about completeness rather than about an empty list passing.
	if err := os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checksum.VerifySumsFile(dir); err == nil {
		t.Error("a file the list does not cover must fail verification")
	}
}

// TestSumsCanBeRegeneratedOverThemselves is the ordinary vendor loop -- build,
// edit, build again -- and it had no direct test.
//
// Found by a sabotage that survived: dropping the exemption for SHA256SUMS
// itself passed every test in this file, because each one sums a tree that has
// none yet. The rule is only reachable on the *second* build, where a list that
// named itself would carry its own previous digest and fail verification
// immediately.
func TestSumsCanBeRegeneratedOverThemselves(t *testing.T) {
	dir := bundle(t, nil)

	if err := release.WriteSums(dir); err != nil {
		t.Fatal(err)
	}
	first := sumsPaths(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := release.WriteSums(dir); err != nil {
		t.Fatalf("a second build over an already-summed tree failed: %v", err)
	}

	second := sumsPaths(t, dir)
	for _, p := range second {
		if p == ports.SumsFileName {
			t.Fatalf("%s lists itself, so its own digest is stale the moment it is written",
				ports.SumsFileName)
		}
	}
	if len(second) != len(first)+1 {
		t.Errorf("the rebuilt list has %d entries, want %d", len(second), len(first)+1)
	}
	if err := checksum.VerifySumsFile(dir); err != nil {
		t.Fatalf("a regenerated checksum list does not verify: %v", err)
	}
}

// TestSumsLineOrderMatchesTheArchiveOrder.
//
// Two orderings of one tree would make the list's bytes depend on how the
// directory happened to be walked, which is the same defect the archive's
// canonical order exists to remove -- and this file is inside the archive.
func TestSumsLineOrderMatchesTheArchiveOrder(t *testing.T) {
	dir := bundle(t, nil)

	if err := release.WriteSums(dir); err != nil {
		t.Fatal(err)
	}

	entries, err := release.ArchiveEntries(dir)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, e := range entries {
		if e == ports.SumsFileName || e == ports.SignatureFileName {
			continue
		}
		want = append(want, e)
	}

	got := sumsPaths(t, dir)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("checksum line order:\ngot:\n  %s\nwant:\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// sumsPaths reads the paths named by a bundle's checksum list, in file order.
func sumsPaths(t *testing.T, dir string) []string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(dir, ports.SumsFileName))
	if err != nil {
		t.Fatal(err)
	}

	var paths []string
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("malformed checksum line %q", line)
		}
		if len(fields[0]) != 64 {
			t.Errorf("checksum %q is not bare hex, so `sha256sum -c` cannot read it", fields[0])
		}
		paths = append(paths, fields[1])
	}
	return paths
}
