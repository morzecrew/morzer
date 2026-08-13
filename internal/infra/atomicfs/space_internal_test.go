package atomicfs

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The free-space arithmetic, which nothing exercised.
//
// RFC 0029 §5.1 says site 1 is "verified by existing space tests on Linux".
// There were none: `checkFreeSpace` is tested through an injected fake, the
// lifecycle layer injects `Deps.FreeSpace`, and the real function -- the one
// whose multiplication differs by platform -- was never run by a test at all.
// A per-OS split of an untested expression is a per-OS split of an unverified
// one, so this is the test that had to exist before the split meant anything.
//
// One file, no build tag: `Bavail` is `uint64` everywhere and `Bsize` is
// `int64` on Linux and `uint32` on darwin, so untyped constants in a struct
// literal compile as either.

func TestAvailableBytesMultipliesBlocksByBlockSize(t *testing.T) {
	stat := syscall.Statfs_t{Bavail: 10, Bsize: 4096}

	if got := availableBytes(&stat); got != 40960 {
		t.Errorf("availableBytes = %d, want %d", got, 40960)
	}
}

// Bavail, never Bfree: the difference is the blocks reserved for root, and
// reporting those as available lets an unprivileged operation pass its check
// and then fail on ENOSPC -- which for a backup means failing partway through
// writing the safety net.
//
// The two fields are set apart here so a swap cannot pass.
func TestAvailableBytesReadsTheUnprivilegedFigure(t *testing.T) {
	stat := syscall.Statfs_t{Bavail: 2, Bsize: 512}
	stat.Bfree = 1000

	if got := availableBytes(&stat); got != 1024 {
		t.Errorf("availableBytes = %d, want %d (it read Bfree, not Bavail)", got, 1024)
	}
}

func TestFreeSpaceReportsSomethingForARealDirectory(t *testing.T) {
	got, err := FreeSpace(t.TempDir())
	if err != nil {
		t.Fatalf("FreeSpace on a temp dir: %v", err)
	}
	if got <= 0 {
		t.Errorf("FreeSpace = %d, want a positive figure", got)
	}
}

// A path that does not exist yet is the fresh-install case: the directory is
// about to be created on the filesystem its nearest existing ancestor is on,
// so that is the one to measure.
func TestFreeSpaceMeasuresTheFilesystemAPathWillBeCreatedOn(t *testing.T) {
	root := t.TempDir()

	parent, err := FreeSpace(root)
	if err != nil {
		t.Fatal(err)
	}
	child, err := FreeSpace(filepath.Join(root, "not", "created", "yet"))
	if err != nil {
		t.Fatalf("FreeSpace on an absent path: %v", err)
	}

	// The same filesystem, so the same figure -- give or take whatever the
	// machine did in between, which is why this is a ratio rather than an
	// equality.
	if child <= 0 || child < parent/2 || child > parent*2 {
		t.Errorf("FreeSpace walked up to a different filesystem: %d vs %d", child, parent)
	}
}

// And it refuses rather than walking up when the failure is not "absent".
//
// This is the case the walk-up exists to *not* swallow: a directory that
// exists and cannot be searched would otherwise be answered with an ancestor's
// free space, which is a wrong number reported as a right one -- and the space
// check that reads it would then pass a backup onto a disk it never measured.
func TestFreeSpaceRefusesAFailureThatIsNotAbsence(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root searches any directory, so EACCES cannot be staged")
	}

	sealed := filepath.Join(t.TempDir(), "sealed")
	if err := os.Mkdir(sealed, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sealed, 0o700) })

	if _, err := FreeSpace(filepath.Join(sealed, "inside")); err == nil {
		t.Error("an unsearchable directory was answered with an ancestor's free space")
	}
}
