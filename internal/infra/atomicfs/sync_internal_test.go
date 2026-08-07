package atomicfs

import (
	"os"
	"path/filepath"
	"testing"
)

// missingAncestors feeds the ancestor-sync in WriteFile; its ordering is the
// crash-consistency contract: deepest first, ending with the first ancestor
// that already existed -- whose entry for the new chain is the one that must
// land -- and nothing at all when the directory is already there.
func TestMissingAncestorsListsTheChainDeepestFirst(t *testing.T) {
	root := t.TempDir()

	target := filepath.Join(root, "a", "b", "c")
	got := missingAncestors(target)

	want := []string{
		target,
		filepath.Join(root, "a", "b"),
		filepath.Join(root, "a"),
		root,
	}
	if len(got) != len(want) {
		t.Fatalf("chain is %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("chain[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestMissingAncestorsOfAnExistingDirIsEmpty(t *testing.T) {
	root := t.TempDir()
	if got := missingAncestors(root); len(got) != 0 {
		t.Fatalf("an existing directory produced a chain: %v", got)
	}
}

func TestSyncFileReportsAMissingPath(t *testing.T) {
	if err := SyncFile(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("flushing a file that does not exist reported success")
	}
}

func TestSyncFileFlushesAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact")
	if err := os.WriteFile(path, []byte("bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SyncFile(path); err != nil {
		t.Fatalf("flushing an existing file failed: %v", err)
	}
}

// SyncTree and SyncDir swallow failures by contract; what is pinned is that
// they cannot blow up an operation over a path that is gone.
func TestSyncHelpersTolerateMissingPaths(t *testing.T) {
	SyncDir(filepath.Join(t.TempDir(), "absent"))
	SyncTree(filepath.Join(t.TempDir(), "absent"))
}
