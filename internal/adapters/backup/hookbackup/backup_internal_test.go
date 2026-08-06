package hookbackup

import (
	"os"
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
