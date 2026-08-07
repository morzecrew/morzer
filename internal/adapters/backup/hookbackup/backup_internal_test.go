package hookbackup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
)

// Two backups within the same second share a timestamp ID. The directory is
// the backup's identity, so a collision must allocate a new name rather than
// silently merging two backups into one directory -- the second run used to
// overwrite the first's manifest and components in place.
func TestABackupIDCollisionAllocatesANewDirectory(t *testing.T) {
	e := New(Config{
		Paths: domain.PathsUnder(t.TempDir(), "demo"),
		// The frozen clock every same-second pair of backups sees.
		NewID: func() string { return "20260807T120000Z" },
	})

	first, firstDir, err := e.createDir()
	if err != nil {
		t.Fatal(err)
	}
	second, secondDir, err := e.createDir()
	if err != nil {
		t.Fatal(err)
	}

	if first != "20260807T120000Z" {
		t.Errorf("first backup got id %q", first)
	}
	if second == first {
		t.Fatalf("two backups in the same second share the directory %q", secondDir)
	}
	for _, dir := range []string{firstDir, secondDir} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Errorf("%s has mode %04o, want 0700", dir, perm)
		}
	}

	// A third run keeps allocating rather than tripping over the suffix it
	// used last time.
	third, _, err := e.createDir()
	if err != nil {
		t.Fatal(err)
	}
	if third == first || third == second {
		t.Errorf("the third backup reused id %q", third)
	}
}

// A SIGKILL mid-restore strands decrypted plaintext in a .restore-* staging
// directory that only an in-process defer used to clean, and Prune never
// touches: the backup being restored is typically the newest. The sweep at
// the start of backup and restore operations is what bounds that exposure.
func TestOrphanedRestoreStagingIsSwept(t *testing.T) {
	e := New(Config{Paths: domain.PathsUnder(t.TempDir(), "demo")})

	backup := filepath.Join(e.paths.BackupsDir(), "20260807T000000Z")
	orphan := filepath.Join(backup, ".restore-abc123")
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	plaintext := filepath.Join(orphan, "database.dump")
	if err := os.WriteFile(plaintext, []byte("the entire database, decrypted"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := e.sweepStagedPlaintext(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("the orphaned plaintext staging directory survived the sweep")
	}
	if _, err := os.Stat(backup); err != nil {
		t.Error("the sweep must remove only the staging debris, not the backup itself")
	}
}

// One stuck removal must fail the operation -- proceeding would report success
// over decrypted product data -- but it must not stop the sweep: every sibling
// is still attempted, and the error names what remains.
func TestSweepAttemptsEveryDirectoryAndNamesTheStuck(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	e := New(Config{Paths: domain.PathsUnder(t.TempDir(), "demo")})

	stuck := filepath.Join(e.paths.BackupsDir(), "20260807T000001Z", ".restore-stuck")
	swept := filepath.Join(e.paths.BackupsDir(), "20260807T000002Z", ".restore-ok")
	for _, dir := range []string{stuck, swept} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// The parent loses the write bit, so the staging dir cannot be unlinked.
	parent := filepath.Dir(stuck)
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	err := e.sweepStagedPlaintext()
	if err == nil {
		t.Fatal("a sweep that left plaintext behind reported success")
	}
	if _, statErr := os.Stat(swept); !os.IsNotExist(statErr) {
		t.Error("the removable sibling was not swept")
	}
	if _, statErr := os.Stat(stuck); statErr != nil {
		t.Error("the stuck directory should still exist, that is the point of the error")
	}
}
