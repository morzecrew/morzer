package atomicfs

import (
	"errors"
	"testing"
)

// The extraction ceiling runs on bytes nobody has verified yet: extraction
// happens before the signature is checked, so these limits are the only thing
// between a hostile archive and the disk, and the signature cannot be the
// mitigation because it is checked afterwards.
//
// Which makes the direction of every rule here the whole point. A declaration
// read out of those same unverified bytes may only ever *lower* the ceiling.

func TestADeclaredBudgetOnlyEverLowersTheCeiling(t *testing.T) {
	base := DefaultExtractLimits()

	cases := []struct {
		name      string
		declared  int64
		wantTotal int64
		wantFile  int64
	}{
		{
			// The case the whole mechanism exists for: a bundle
			// carrying container images needs more than the default,
			// and gets it -- bounded.
			name:      "a realistic image bundle",
			declared:  12 << 30,
			wantTotal: 12 << 30,
			wantFile:  5 << 30, // the per-file hard cap
		},
		{
			// An attacker declaring whatever they need. The cap is
			// what makes the budget a bound rather than a request.
			name:      "a declaration far above the hard cap",
			declared:  500 << 30,
			wantTotal: HardMaxTotalSize,
			wantFile:  HardMaxFileSize,
		},
		{
			name:      "exactly the hard cap",
			declared:  HardMaxTotalSize,
			wantTotal: HardMaxTotalSize,
			wantFile:  HardMaxFileSize,
		},
		{
			// Stricter than the default, and honoured: an archive
			// that exceeds its own declaration is refused, which is
			// the other half of a budget meaning anything.
			name:      "a declaration below the default",
			declared:  1 << 20,
			wantTotal: 1 << 20,
			wantFile:  1 << 20,
		},
		{
			// Absent means the default, never "unbounded". A missing
			// field must not be the permissive reading of anything
			// that gates untrusted bytes.
			name:      "no declaration",
			declared:  0,
			wantTotal: base.MaxTotalSize,
			wantFile:  base.MaxFileSize,
		},
		{
			name:      "a negative declaration",
			declared:  -1,
			wantTotal: base.MaxTotalSize,
			wantFile:  base.MaxFileSize,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clampToBudget(base, tc.declared)
			if got.MaxTotalSize != tc.wantTotal {
				t.Errorf("total = %d, want %d", got.MaxTotalSize, tc.wantTotal)
			}
			if got.MaxFileSize != tc.wantFile {
				t.Errorf("per file = %d, want %d", got.MaxFileSize, tc.wantFile)
			}
			// Never raised past the cap, whatever was asked for.
			if got.MaxTotalSize > HardMaxTotalSize || got.MaxFileSize > HardMaxFileSize {
				t.Errorf("a declaration raised the ceiling past the hard cap: %+v", got)
			}
			// The entry count is not part of the trade: an OCI
			// layout is a handful of files per image, so it was
			// never the binding constraint.
			if got.MaxEntries != base.MaxEntries {
				t.Errorf("the entry limit moved to %d", got.MaxEntries)
			}
		})
	}
}

// TestTheFreeSpaceCheckRefusesBeforeAnythingIsWritten.
//
// A clean refusal beats a full filesystem. The disk reading is injected,
// because a check whose verdict depends on the host's real disk is a check
// whose test passes or fails on which machine ran it.
func TestTheFreeSpaceCheckRefusesBeforeAnythingIsWritten(t *testing.T) {
	restore := freeSpace
	t.Cleanup(func() { freeSpace = restore })

	limits := clampToBudget(DefaultExtractLimits(), 12<<30)

	freeSpace = func(string) (int64, error) { return 4 << 30, nil }
	err := checkFreeSpace("/somewhere", limits, 12<<30)
	if err == nil {
		t.Fatal("a bundle larger than the disk was accepted")
	}

	freeSpace = func(string) (int64, error) { return 40 << 30, nil }
	if err := checkFreeSpace("/somewhere", limits, 12<<30); err != nil {
		t.Errorf("a bundle that fits was refused: %v", err)
	}

	// A filesystem whose free space cannot be read is not evidence of a
	// full one, and the extraction limits still bound what gets written --
	// so this must not become a refusal on every machine with an unusual
	// mount.
	freeSpace = func(string) (int64, error) { return 0, errors.New("statfs: not supported") }
	if err := checkFreeSpace("/somewhere", limits, 12<<30); err != nil {
		t.Errorf("an unreadable filesystem became a refusal: %v", err)
	}

	// And with no declaration there is no claim to check: demanding two
	// gigabytes free to extract a two-megabyte bundle would refuse every
	// ordinary install on a small disk.
	freeSpace = func(string) (int64, error) { return 1 << 20, nil }
	if err := checkFreeSpace("/somewhere", DefaultExtractLimits(), 0); err != nil {
		t.Errorf("an undeclared bundle was measured against the default ceiling: %v", err)
	}
}
