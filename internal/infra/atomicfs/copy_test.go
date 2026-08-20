package atomicfs_test

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/morzecrew/morzer/internal/infra/atomicfs"
)

// CopyTree is how a release gets from wherever an operator put it into the
// immutable store under /opt. Everything it refuses is something a bundle
// could contain that a release must not: a symlink pointing at /etc/shadow, a
// device node, an entry count chosen to exhaust the inode table.
//
// Refused rather than skipped, throughout. Copying "most of" a hostile bundle
// produces a release that behaves differently from the one that was verified,
// which is the failure mode digest-pinning exists to prevent.

func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestCopyTreeCopiesAndPreservesTheExecutableBit(t *testing.T) {
	src := tree(t, map[string]string{
		"manifest.yaml":        "api_version: v1\n",
		"hooks/migrate":        "#!/bin/sh\n",
		"compose/compose.yaml": "services: {}\n",
	})
	if err := os.Chmod(filepath.Join(src, "hooks", "migrate"), 0o755); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "release")
	if err := atomicfs.CopyTree(src, dst, atomicfs.DefaultExtractLimits()); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"manifest.yaml", "hooks/migrate", "compose/compose.yaml"} {
		if _, err := os.Stat(filepath.Join(dst, name)); err != nil {
			t.Errorf("%s did not arrive: %v", name, err)
		}
	}

	// Whether a hook can run is part of what the bundle is, and the digest
	// covers it -- so a copy that lost the bit would produce a release that
	// no longer matches its own digest.
	info, err := os.Stat(filepath.Join(dst, "hooks", "migrate"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("the executable bit was lost: mode %04o", info.Mode().Perm())
	}
}

func TestCopyTreeRefusesEverythingThatIsNotAFileOrADirectory(t *testing.T) {
	t.Run("a symlink", func(t *testing.T) {
		src := tree(t, map[string]string{"manifest.yaml": "x"})
		if err := os.Symlink("/etc/shadow", filepath.Join(src, "sneaky")); err != nil {
			t.Skipf("cannot create a symlink here: %v", err)
		}

		err := atomicfs.CopyTree(src, filepath.Join(t.TempDir(), "out"),
			atomicfs.DefaultExtractLimits())
		if err == nil {
			t.Fatal("a bundle containing a symlink was copied")
		}
		if !strings.Contains(err.Error(), "symlink") {
			t.Errorf("the refusal does not say what it found: %v", err)
		}
		if !strings.Contains(err.Error(), "sneaky") {
			t.Errorf("the refusal does not name the entry: %v", err)
		}
	})

	t.Run("a fifo", func(t *testing.T) {
		src := tree(t, map[string]string{"manifest.yaml": "x"})
		fifo := filepath.Join(src, "pipe")
		if err := syscall.Mkfifo(fifo, 0o600); err != nil {
			t.Skipf("cannot create a fifo here: %v", err)
		}

		err := atomicfs.CopyTree(src, filepath.Join(t.TempDir(), "out"),
			atomicfs.DefaultExtractLimits())
		if err == nil {
			t.Fatal("a bundle containing a fifo was copied")
		}
		if !strings.Contains(err.Error(), "non-regular") {
			t.Errorf("the refusal does not say what it found: %v", err)
		}
	})
}

func TestCopyTreeEnforcesItsLimits(t *testing.T) {
	src := tree(t, map[string]string{
		"a.txt": strings.Repeat("a", 1000),
		"b.txt": strings.Repeat("b", 1000),
		"c.txt": strings.Repeat("c", 1000),
	})

	cases := map[string]struct {
		limits atomicfs.ExtractLimits
		want   string
	}{
		"too many entries": {
			atomicfs.ExtractLimits{MaxEntries: 2}, "entry limit",
		},
		"one file too large": {
			atomicfs.ExtractLimits{MaxFileSize: 500}, "per-file limit",
		},
		"too large in total": {
			atomicfs.ExtractLimits{MaxTotalSize: 1500}, "total size limit",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := atomicfs.CopyTree(src, filepath.Join(t.TempDir(), "out"), tc.limits)
			if err == nil {
				t.Fatalf("%s was allowed through", name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not say which limit: %v", err)
			}
		})
	}
}

// TestZeroLimitsMeanUnlimited, which is what the option struct's zero value has
// to mean for a caller that only wants to bound one dimension.
func TestZeroLimitsMeanUnlimited(t *testing.T) {
	src := tree(t, map[string]string{"a.txt": "hello", "b/c.txt": "world"})

	if err := atomicfs.CopyTree(src, filepath.Join(t.TempDir(), "out"),
		atomicfs.ExtractLimits{}); err != nil {
		t.Fatalf("a copy with no limits set was refused: %v", err)
	}
}

func TestCopyTreeRefusesASourceItCannotUse(t *testing.T) {
	t.Run("a source that is not there", func(t *testing.T) {
		err := atomicfs.CopyTree(filepath.Join(t.TempDir(), "gone"),
			filepath.Join(t.TempDir(), "out"), atomicfs.DefaultExtractLimits())
		if err == nil {
			t.Fatal("a source that does not exist was copied")
		}
		if !strings.Contains(err.Error(), "cannot read source") {
			t.Errorf("the refusal does not say what was wrong: %v", err)
		}
	})

	t.Run("a source that is a file", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "bundle.tar")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := atomicfs.CopyTree(file, filepath.Join(t.TempDir(), "out"),
			atomicfs.DefaultExtractLimits())
		if err == nil {
			t.Fatal("a file was copied as a directory")
		}
		if !strings.Contains(err.Error(), "not a directory") {
			t.Errorf("the refusal does not say what was wrong: %v", err)
		}
	})
}

// TestCopyTreeIntoSomewhereItCannotWrite is the full-disk and read-only-mount
// case, provoked with a permission that produces the same syscall error.
func TestCopyTreeIntoSomewhereItCannotWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}

	src := tree(t, map[string]string{"manifest.yaml": "x"})
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	err := atomicfs.CopyTree(src, filepath.Join(parent, "out"),
		atomicfs.DefaultExtractLimits())
	if err == nil {
		t.Fatal("a copy into an unwritable directory reported success")
	}
}

// TestCopyTreeCannotBeEscapedByASourcePath. The destination is opened as an
// os.Root and every entry is created through it, so even a source path that
// walks upward lands inside.
func TestCopyTreeStaysInsideItsDestination(t *testing.T) {
	src := tree(t, map[string]string{
		"a/b/c/deep.txt": "value",
		"manifest.yaml":  "x",
	})
	outer := t.TempDir()
	dst := filepath.Join(outer, "release")

	if err := atomicfs.CopyTree(src, dst, atomicfs.DefaultExtractLimits()); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dst, "a", "b", "c", "deep.txt")); err != nil {
		t.Errorf("a nested file did not arrive: %v", err)
	}
	entries, err := os.ReadDir(outer)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the copy wrote outside its destination: %v", entries)
	}
}

func TestDigestTree(t *testing.T) {
	content := map[string]string{"manifest.yaml": "a", "hooks/migrate": "b"}

	first, err := atomicfs.DigestTree(tree(t, content))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, "sha256:") {
		t.Errorf("digest = %q, want a sha256 reference", first)
	}

	// The same content on a different filesystem path must hash the same,
	// or a digest recorded from an unpacked bundle would never verify.
	second, err := atomicfs.DigestTree(tree(t, content))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("the same tree hashed differently in two places: %s vs %s", first, second)
	}
}

func TestDigestTreeCoversPathsModesAndContents(t *testing.T) {
	base := map[string]string{"a.txt": "hello", "b.txt": "world"}
	original, err := atomicfs.DigestTree(tree(t, base))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("changed content", func(t *testing.T) {
		got, err := atomicfs.DigestTree(tree(t, map[string]string{
			"a.txt": "hello!", "b.txt": "world",
		}))
		if err != nil {
			t.Fatal(err)
		}
		if got == original {
			t.Error("changing a byte did not change the digest")
		}
	})

	t.Run("moved file", func(t *testing.T) {
		// Moving a file changes the release even when no byte of content
		// does, so the path is part of the hash.
		got, err := atomicfs.DigestTree(tree(t, map[string]string{
			"sub/a.txt": "hello", "b.txt": "world",
		}))
		if err != nil {
			t.Fatal(err)
		}
		if got == original {
			t.Error("moving a file did not change the digest")
		}
	})

	t.Run("the executable bit", func(t *testing.T) {
		dir := tree(t, base)
		if err := os.Chmod(filepath.Join(dir, "a.txt"), 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := atomicfs.DigestTree(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got == original {
			t.Error("making a file executable did not change the digest, so a " +
				"bundle whose hook gained the ability to run would verify " +
				"against the old digest")
		}
	})

	t.Run("the group and world bits do not count", func(t *testing.T) {
		// They vary with the umask of whoever unpacked the bundle, and
		// folding them in would make the digest depend on the unpacking
		// environment rather than on the bundle.
		dir := tree(t, base)
		if err := os.Chmod(filepath.Join(dir, "a.txt"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := atomicfs.DigestTree(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got != original {
			t.Errorf("the group bits changed the digest: %s vs %s", got, original)
		}
	})

	t.Run("an ambiguous split", func(t *testing.T) {
		// Without a separator, a file "a" holding "bc" and a file "ab"
		// holding "c" would hash identically.
		one, err := atomicfs.DigestTree(tree(t, map[string]string{"a": "bc"}))
		if err != nil {
			t.Fatal(err)
		}
		two, err := atomicfs.DigestTree(tree(t, map[string]string{"ab": "c"}))
		if err != nil {
			t.Fatal(err)
		}
		if one == two {
			t.Error("two different trees hash identically")
		}
	})
}

func TestDigestTreeRefusesANonRegularFile(t *testing.T) {
	dir := tree(t, map[string]string{"a.txt": "x"})
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o600); err != nil {
		t.Skipf("cannot create a fifo here: %v", err)
	}

	if _, err := atomicfs.DigestTree(dir); err == nil {
		t.Fatal("a tree containing a fifo was digested, so what the digest " +
			"covers depends on what the filesystem happened to hold")
	}
}

func TestDigestFile(t *testing.T) {
	dir := tree(t, map[string]string{"a.txt": "hello"})

	got, err := atomicfs.DigestFile(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	// sha256("hello")
	const want = "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got != want {
		t.Errorf("DigestFile = %s, want %s", got, want)
	}

	if _, err := atomicfs.DigestFile(filepath.Join(dir, "gone")); err == nil {
		t.Error("a file that does not exist produced a digest")
	}
}

func TestSameDigestToleratesHowAChecksumWasPasted(t *testing.T) {
	const bare = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

	equal := [][2]string{
		{"sha256:" + bare, "sha256:" + bare},
		{"sha256:" + bare, bare},                                       // from `sha256sum`
		{"sha256:" + bare, "  SHA256:" + strings.ToUpper(bare) + "  "}, // pasted from a web page
	}
	for _, pair := range equal {
		if !atomicfs.SameDigest(pair[0], pair[1]) {
			t.Errorf("SameDigest(%q, %q) = false", pair[0], pair[1])
		}
	}

	different := [][2]string{
		{"sha256:" + bare, "sha256:" + strings.Repeat("0", 64)},
		{"", ""},          // two absent digests are not a match
		{"sha256:", bare}, // an empty digest matches nothing
		{"", bare},
	}
	for _, pair := range different {
		if atomicfs.SameDigest(pair[0], pair[1]) {
			t.Errorf("SameDigest(%q, %q) = true", pair[0], pair[1])
		}
	}
}

func TestFingerprintSecretIdentifiesWithoutRevealing(t *testing.T) {
	const secret = "a-real-database-password"

	got := atomicfs.FingerprintSecret(secret)
	if strings.Contains(got, secret) || len(got) != 12 {
		t.Errorf("fingerprint = %q", got)
	}
	if got != atomicfs.FingerprintSecret(secret) {
		t.Error("the fingerprint is not stable, so two installations cannot be compared")
	}
	if got == atomicfs.FingerprintSecret(secret+"!") {
		t.Error("two different secrets fingerprint the same")
	}
}

func TestDirSizeIgnoresWhatItCannotRead(t *testing.T) {
	dir := tree(t, map[string]string{"a.txt": "12345", "sub/b.txt": "123"})

	size, err := atomicfs.DirSize(dir)
	if err != nil {
		t.Fatal(err)
	}
	if size != 8 {
		t.Errorf("DirSize = %d, want 8", size)
	}

	// A diagnostic must not fail because one subtree is unreadable: this is
	// what `doctor` calls on a machine that is already misbehaving.
	if os.Geteuid() != 0 {
		if err := os.Chmod(filepath.Join(dir, "sub"), 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "sub"), 0o755) })

		if _, err := atomicfs.DirSize(dir); err != nil {
			t.Errorf("an unreadable subtree failed a diagnostic: %v", err)
		}
	}

	// And a path that is not there is zero, not a crash.
	if _, err := atomicfs.DirSize(filepath.Join(t.TempDir(), "gone")); err == nil {
		t.Log("DirSize on a missing path returns no error, which is acceptable " +
			"for a diagnostic")
	}
}

// TestAnInTreeSymlinkIsRefused: a link that resolves *inside* the bundle is
// refused like any other, and its target is not copied under its name.
//
// This is the observable refusal, and it is the walk that produces it here --
// the entry is a symlink before the walk sees it. The open-time descent that
// covers the *swapped after the walk* case cannot be reached this way without a
// race no test can reliably win, so it is exercised directly in
// copy_internal_test.go instead.
func TestAnInTreeSymlinkIsRefused(t *testing.T) {
	src := tree(t, map[string]string{
		"manifest.yaml": "x",
		"real.txt":      "the file the walk would have seen",
	})
	if err := os.Symlink("real.txt", filepath.Join(src, "alias.txt")); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "out")
	err := atomicfs.CopyTree(src, dst, atomicfs.DefaultExtractLimits())
	if err == nil {
		t.Fatal("a bundle containing an in-tree symlink was copied")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("the refusal does not say what it found: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dst, "alias.txt")); statErr == nil {
		t.Error("the symlink's target was copied under the link's name")
	}
}

// TestCopyTreeLeavesTheSourceTreeBehind covers the second surface the same leak
// reached: staging a local bundle.
//
// The published archive is the serious half, but a working copy staged from a
// directory put `.git` on the operator's disk too -- and spent the bundle's
// entry budget doing it, since an object store is exactly the thing with tens
// of thousands of files in it.
func TestCopyTreeLeavesTheSourceTreeBehind(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "staged")

	for _, rel := range []string{
		"manifest.yaml", "compose/compose.yaml", ".gitignore",
		".git/config", ".git/objects/ab/cdef", ".DS_Store",
	} {
		p := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(rel), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := atomicfs.CopyTree(src, dst, atomicfs.DefaultExtractLimits()); err != nil {
		t.Fatal(err)
	}

	for _, rel := range []string{"manifest.yaml", "compose/compose.yaml", ".gitignore"} {
		if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s belongs in the staged bundle: %v", rel, err)
		}
	}
	for _, rel := range []string{".git", ".git/config", ".DS_Store"} {
		if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("%s must not be staged (err=%v)", rel, err)
		}
	}
}
