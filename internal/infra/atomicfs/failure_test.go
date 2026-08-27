package atomicfs_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
)

// This package is where "extraction cannot escape its root" and "a rendered
// secret is 0400" are actually enforced, so its failure paths are not
// incidental — they are the paths that run when something is wrong, which is
// the only time enforcement matters.
//
// Failures are provoked for real rather than injected: an unwritable directory,
// a file where a directory belongs, a mode nobody can read. That reaches most
// of what a mock would, without an abstraction between the code and the
// filesystem it is the point of this package to use carefully.

// unwritable makes a directory read-only for the duration of a test.
//
// Skipped as root, which ignores permission bits: a test that silently proves
// nothing is worse than one that does not run.
func unwritable(t *testing.T, dir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("cannot make %s read-only: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

func TestWriteFileReportsAnUnwritableDirectory(t *testing.T) {
	dir := t.TempDir()
	unwritable(t, dir)

	err := atomicfs.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600)
	if err == nil {
		t.Fatal("writing into a read-only directory succeeded")
	}
	// Named, not a bare syscall error: an operator reading this has to know
	// which path the manager could not write.
	if !strings.Contains(domain.AsError(err).Message, dir) {
		t.Errorf("the error does not name the directory: %s", domain.AsError(err).Message)
	}
}

func TestWriteFileReportsAPathBlockedByAFile(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The parent of the target is a regular file, so creating the directory
	// tree must fail rather than silently doing something else.
	if err := atomicfs.WriteFile(filepath.Join(blocker, "child"), []byte("x"), 0o600); err == nil {
		t.Fatal("writing through a regular file succeeded")
	}
}

func TestMkdirAllReportsAPathBlockedByAFile(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := atomicfs.MkdirAll(filepath.Join(blocker, "sub"), 0o755); err == nil {
		t.Fatal("creating a directory under a regular file succeeded")
	}
}

// TestMkdirExactSetsTheModeEvenWhenTheDirectoryExists pins the behaviour
// `init --repair` depends on: a directory that exists with the wrong
// permissions is corrected, not left.
func TestMkdirExactSetsTheModeEvenWhenTheDirectoryExists(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}

	if err := atomicfs.MkdirExact(dir, 0o700); err != nil {
		t.Fatalf("MkdirExact on an existing directory: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("mode is %04o, want 0700 — a too-permissive directory was left as it was", got)
	}
}

// The containment rule, asserted through the function production actually
// calls.
//
// It used to be asserted through ReadFileIn, which nothing outside this test
// ever called: the rule was proved on a path no operator could reach while
// WriteFileIn -- the one that renders a secret into a release's directory --
// had no test of its own. Same rule, same cleanRel, now guarded where it runs.
func TestWriteFileInStaysInsideItsRoot(t *testing.T) {
	dir := t.TempDir()

	root, err := atomicfs.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	if err := atomicfs.WriteFileIn(root, "present", []byte("hello"), 0o600); err != nil {
		t.Fatalf("writing a file inside the root: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "present"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Errorf("read %q, want %q", data, "hello")
	}

	for _, escape := range []string{"../outside", "/etc/passwd", ""} {
		err := atomicfs.WriteFileIn(root, escape, []byte("hello"), 0o600)
		if err == nil {
			t.Errorf("writing %q from inside a root succeeded", escape)
			continue
		}
		if !errors.Is(err, domain.ErrPathEscape) {
			t.Errorf("writing %q gave %v, want ErrPathEscape", escape, err)
		}
	}

	// And the refusal is a refusal, not a rename: nothing landed beside the
	// root under the name the escape asked for.
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "outside")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("an escaping write left something outside the root: %v", err)
	}
}

func TestOpenRootRefusesWhatIsNotADirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := atomicfs.OpenRoot(file); err == nil {
		t.Fatal("opening a regular file as a root succeeded")
	}
	if _, err := atomicfs.OpenRoot(filepath.Join(file, "nope")); err == nil {
		t.Fatal("opening a nonexistent root succeeded")
	}
}

func TestReadSymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "current")

	// Absent is not an error: no release is installed yet, and the caller
	// distinguishes "" from a target.
	target, err := atomicfs.ReadSymlink(link)
	if err != nil {
		t.Fatalf("reading an absent symlink: %v", err)
	}
	if target != "" {
		t.Errorf("absent symlink read as %q, want empty", target)
	}

	if err := os.Symlink("/somewhere/1.2.0", link); err != nil {
		t.Fatal(err)
	}
	if target, err = atomicfs.ReadSymlink(link); err != nil || target != "/somewhere/1.2.0" {
		t.Errorf("read %q, %v; want the target", target, err)
	}

	// A regular file is not a symlink, and must be reported rather than
	// read as one.
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := atomicfs.ReadSymlink(plain); err == nil {
		t.Error("reading a regular file as a symlink succeeded")
	}
}

// TestCheckModeReportsRatherThanFails pins the contract doctor depends on: a
// wrong mode is a finding it can print, not an error that aborts the run.
func TestCheckModeReportsRatherThanFails(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, nil, 0o640); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name       string
		path       string
		want       fs.FileMode
		ok         bool
		detailHas  string
		wantsError bool
	}{
		{"a matching mode", file, 0o640, true, "", false},
		{"a mode that differs", file, 0o600, false, "0640", false},
		{"a file where a directory belongs", file, fs.ModeDir | 0o755, false, "not a directory", false},
		{"a path that is absent", filepath.Join(dir, "gone"), 0o600, false, "does not exist", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, detail, err := atomicfs.CheckMode(tc.path, tc.want)
			if (err != nil) != tc.wantsError {
				t.Fatalf("error = %v, want error: %v", err, tc.wantsError)
			}
			if ok != tc.ok {
				t.Errorf("ok = %v, want %v (detail %q)", ok, tc.ok, detail)
			}
			if tc.detailHas != "" && !strings.Contains(detail, tc.detailHas) {
				t.Errorf("detail %q does not mention %q", detail, tc.detailHas)
			}
		})
	}
}

func TestReplaceSymlinkIsAtomicAndRepeatable(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "current")

	if err := atomicfs.ReplaceSymlink("/a", link); err != nil {
		t.Fatalf("first link: %v", err)
	}
	// Replacing an existing link is the release-pointer swap, and has to
	// work without an intermediate state where the link is absent.
	if err := atomicfs.ReplaceSymlink("/b", link); err != nil {
		t.Fatalf("replacing: %v", err)
	}

	target, err := atomicfs.ReadSymlink(link)
	if err != nil || target != "/b" {
		t.Errorf("link points at %q, %v; want /b", target, err)
	}

	// The parent is created, not required: the release pointer is written
	// during init, before anything guarantees its directory exists.
	deep := filepath.Join(dir, "missing", "l")
	if err := atomicfs.ReplaceSymlink("/a", deep); err != nil {
		t.Errorf("linking into a directory that does not exist yet: %v", err)
	}
	if target, _ := atomicfs.ReadSymlink(deep); target != "/a" {
		t.Errorf("the created link points at %q, want /a", target)
	}

	// A parent that cannot be created is still an error.
	blocked := filepath.Join(dir, "plainfile")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atomicfs.ReplaceSymlink("/a", filepath.Join(blocked, "l")); err == nil {
		t.Error("linking under a regular file succeeded")
	}
}

func TestExistsDistinguishesAbsenceFromFailure(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if ok, err := atomicfs.Exists(file); !ok || err != nil {
		t.Errorf("Exists(present) = %v, %v", ok, err)
	}
	if ok, err := atomicfs.Exists(filepath.Join(dir, "absent")); ok || err != nil {
		t.Errorf("Exists(absent) = %v, %v; absence is not an error", ok, err)
	}
}

func TestRemoveAllToleratesAbsence(t *testing.T) {
	if err := atomicfs.RemoveAll(filepath.Join(t.TempDir(), "never-existed")); err != nil {
		t.Errorf("removing something absent: %v", err)
	}
}

// TestRemoveWithOverwriteClearsEveryFile is the erasure path for rendered
// secrets. What it is worth depends on the filesystem, which the function's own
// doc comment is explicit about; what is testable is that every regular file
// under the tree is overwritten and then gone.
func TestRemoveWithOverwriteClearsEveryFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session")
	nested := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(dir, "one"),
		filepath.Join(nested, "two"),
	} {
		if err := os.WriteFile(p, []byte("s3cr3t"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := atomicfs.RemoveWithOverwrite(dir); err != nil {
		t.Fatalf("RemoveWithOverwrite: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the directory survived: %v", err)
	}

	// Absence is tolerated, because this runs from a defer that cannot know
	// whether the session directory was ever created.
	if err := atomicfs.RemoveWithOverwrite(dir); err != nil {
		t.Errorf("removing something already gone: %v", err)
	}
}

// A parent directory WriteFile has to create takes its mode from the file:
// a 0640 state file lands in a 0750 directory, a 0600 backup manifest in a
// 0700 one. The flat 0755 this replaces widened the manager's own tree.
func TestWriteFileDerivesParentDirModeFromTheFile(t *testing.T) {
	root := t.TempDir()

	cases := map[fs.FileMode]fs.FileMode{
		0o640: 0o750,
		0o600: 0o700,
		0o644: 0o755,
	}
	for fileMode, wantDir := range cases {
		dir := filepath.Join(root, fmt.Sprintf("m%04o", fileMode))
		path := filepath.Join(dir, "nested", "file.yaml")
		if err := atomicfs.WriteFile(path, []byte("x\n"), fileMode); err != nil {
			t.Fatal(err)
		}
		for _, created := range []string{dir, filepath.Join(dir, "nested")} {
			info, err := os.Stat(created)
			if err != nil {
				t.Fatal(err)
			}
			if perm := info.Mode().Perm(); perm != wantDir {
				t.Errorf("a %04o file's parent %s has mode %04o, want %04o",
					fileMode, created, perm, wantDir)
			}
		}
	}
}
