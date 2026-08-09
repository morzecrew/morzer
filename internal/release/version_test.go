package release_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/release"
)

// mustVersion parses a version or fails the test.
func mustVersion(t *testing.T, s string) domain.Version {
	t.Helper()
	v, err := domain.ParseVersion(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// The scheme has one job: a development build must sort *after* the release it
// comes after and *before* the next one, and two builds of different content
// must never be the same version. Every row below is one way to get that wrong.

func TestTheVersionScheme(t *testing.T) {
	cases := []struct {
		name       string
		describe   string
		allowDirty bool
		want       string
		wantErr    string
	}{
		{
			name:     "on a tag, the tag verbatim",
			describe: "v1.4.0-0-g3be286c",
			want:     "1.4.0",
		},
		{
			// The same distance-0 case without the prefix, so the
			// row earns its place: `v` is dropped either way.
			name:     "the v prefix is optional, so git tags work unchanged",
			describe: "1.4.0-0-g3be286c",
			want:     "1.4.0",
		},
		{
			// The patch is bumped because 1.4.0-dev.7 sorts *below*
			// 1.4.0 -- a development build named after the tag it
			// follows would sort behind the release it comes after.
			name:     "past a tag, the next patch as a prerelease",
			describe: "v1.4.0-7-g3be286c",
			want:     "1.4.1-dev.7.g3be286c",
		},
		{
			name:     "a tag without the v prefix",
			describe: "1.4.0-7-g3be286c",
			want:     "1.4.1-dev.7.g3be286c",
		},
		{
			// A build stamping a version that names a commit it is
			// not is a lie the content digest cannot catch.
			name:     "a dirty tree is refused",
			describe: "v1.4.0-7-g3be286c-dirty",
			wantErr:  "uncommitted",
		},
		{
			name:       "and allowed, marked, and sorting after the clean build",
			describe:   "v1.4.0-7-g3be286c-dirty",
			allowDirty: true,
			want:       "1.4.1-dev.7.g3be286c.dirty",
		},
		{
			// Not the tag verbatim: the tree carries content the tag
			// does not, and `1.4.0-dirty` would sort *below* the tag
			// it is ahead of.
			name:       "dirty on the tag itself takes the development shape",
			describe:   "v1.4.0-0-g3be286c-dirty",
			allowDirty: true,
			want:       "1.4.1-dev.0.g3be286c.dirty",
		},
		{
			// Is the next thing after 1.4.0-rc.1 another rc, or
			// 1.4.0? A wrong guess sorts wrongly, which is the one
			// failure this scheme exists to prevent.
			name:     "a prerelease tag has no unambiguous successor",
			describe: "v1.4.0-rc.1-7-g3be286c",
			wantErr:  "prerelease",
		},
		{
			name:     "a tag carrying build metadata is refused",
			describe: "v1.4.0+build.7-7-g3be286c",
			wantErr:  "build metadata",
		},
		{
			name:     "output that is not a describe",
			describe: "1.4.0",
			wantErr:  "git describe",
		},
		{
			name:     "a tag that is not a version",
			describe: "release-candidate-7-g3be286c",
			wantErr:  "not a semantic version",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			described, err := release.ParseDescribe(tc.describe)
			var got string
			if err == nil {
				var version domain.Version
				version, err = described.Version(tc.allowDirty)
				got = version.String()
			}

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("got %s, want a refusal mentioning %q", got, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("refusal is %q, want it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("version = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestADevelopmentBuildSortsBetweenItsNeighbours is the property the scheme
// exists for, asserted as ordering rather than as a string.
//
// The table above pins the spelling; a spelling that is right and an ordering
// that is wrong would pass it.
func TestADevelopmentBuildSortsBetweenItsNeighbours(t *testing.T) {
	version := func(describe string, allowDirty bool) string {
		t.Helper()
		d, err := release.ParseDescribe(describe)
		if err != nil {
			t.Fatal(err)
		}
		v, err := d.Version(allowDirty)
		if err != nil {
			t.Fatal(err)
		}
		return v.String()
	}

	tag := mustVersion(t, "1.4.0")
	next := mustVersion(t, "1.4.1")
	dev := mustVersion(t, version("v1.4.0-7-g3be286c", false))
	dirty := mustVersion(t, version("v1.4.0-7-g3be286c-dirty", true))

	if !dev.GreaterThan(tag) {
		t.Errorf("%s does not sort after the tag %s it follows", dev, tag)
	}
	if !dev.LessThan(next) {
		t.Errorf("%s does not sort before the release %s it precedes", dev, next)
	}
	if !dirty.GreaterThan(dev) {
		t.Errorf("%s does not sort after the clean build %s at the same commit", dirty, dev)
	}
}

// TestGitWithNoReachableTagFailsRatherThanDefaulting.
//
// Shallow checkouts are the default in the most popular CI system in the world,
// and they fetch no tags. Defaulting past that would stamp a plausible wrong
// version -- most likely 0.0.0 -- onto a real release.
func TestGitWithNoReachableTagFailsRatherThanDefaulting(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "first")

	_, err := release.DescribeRepository(dir)
	if err == nil {
		t.Fatal("a repository with no tags must be refused, not defaulted around")
	}
	// The hint carries the actionable half, and the usual cause of this
	// failure is a checkout that fetched no tags rather than a missing tag.
	if hint := domain.AsError(err).Hint; !strings.Contains(hint, "fetch-depth") {
		t.Errorf("the refusal should point at the usual cause, hint was %q (%v)", hint, err)
	}
}

// TestDescribeAgainstARealRepository.
//
// The table above exercises the scheme against strings; this exercises the half
// that talks to git, which patch coverage found was reached only by its failure
// case. `--version-from-git` is the whole point of the phase, and nothing ran
// it against a repository that has a tag.
func TestDescribeAgainstARealRepository(t *testing.T) {
	dir := newRepository(t)

	described, err := release.DescribeRepository(dir)
	if err != nil {
		t.Fatal(err)
	}
	if described.Tag.String() != "1.4.0" {
		t.Errorf("tag = %s, want 1.4.0", described.Tag)
	}
	if described.Distance != 2 {
		t.Errorf("distance = %d, want 2", described.Distance)
	}
	if described.SHA == "" {
		t.Error("no commit sha, so two branches at the same distance would collide")
	}
	if described.Dirty {
		t.Error("a clean tree was reported dirty")
	}
	// The commit date is what the archive's timestamp falls back to, so a
	// zero here silently turns every archive into an epoch-stamped one.
	if described.CommitTime.IsZero() {
		t.Error("no commit date")
	}

	version, err := described.Version(false)
	if err != nil {
		t.Fatal(err)
	}
	want := "1.4.1-dev.2.g" + described.SHA
	if version.String() != want {
		t.Errorf("version = %s, want %s", version, want)
	}

	// And a dirty tree is refused, through the real `--dirty` flag rather
	// than a hand-written string -- which is the half that would break if
	// git's spelling ever changed.
	if err := os.WriteFile(filepath.Join(dir, "first"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}

	dirty, err := release.DescribeRepository(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !dirty.Dirty {
		t.Fatal("a modified work tree was not reported dirty")
	}
	if _, err := dirty.Version(false); err == nil {
		t.Error("a dirty tree must be refused without --allow-dirty")
	}
}

// TestAnUntrackedFileMakesTheTreeDirty.
//
// `git describe --dirty` reports only tracked modifications, and a bundle is
// archived from the filesystem rather than from the index -- so without a
// status check a brand-new file is packed, summed and signed while the version
// names the commit as though it described the tree. Reproduced before the fix:
// `git describe --tags --long --dirty` returned `v1.4.0-0-g…` with an
// untracked file sitting beside it.
func TestAnUntrackedFileMakesTheTreeDirty(t *testing.T) {
	dir := newRepository(t)

	clean, err := release.DescribeRepository(dir)
	if err != nil {
		t.Fatal(err)
	}
	if clean.Dirty {
		t.Fatal("a clean checkout was reported dirty")
	}

	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	described, err := release.DescribeRepository(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !described.Dirty {
		t.Fatal("an untracked file left the tree looking clean, so a version " +
			"could name a commit that does not contain what was packed")
	}
	if _, err := described.Version(false); err == nil {
		t.Error("the version must be refused without --allow-dirty")
	}

	// An ignored file is not dirtiness: a vendor who gitignores their
	// build output has said what they meant, and refusing over it would
	// make --version-from-git unusable in the loop it exists for.
	if err := os.Remove(filepath.Join(dir, "untracked.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("build-output\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", ".gitignore")
	gitIn(t, dir, "commit", "-q", "-m", "ignore build output")
	if err := os.WriteFile(filepath.Join(dir, "build-output"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	ignored, err := release.DescribeRepository(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ignored.Dirty {
		t.Error("an ignored file was treated as uncommitted work")
	}
}

// newRepository builds a repository two commits past the tag v1.4.0.
func newRepository(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	commit := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, dir, "add", ".")
		gitIn(t, dir, "commit", "-q", "-m", name)
	}
	commit("first")
	gitIn(t, dir, "tag", "v1.4.0")
	commit("second")
	commit("third")
	return dir
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	// Identity and signing are forced off: a developer's global config that
	// signs every commit would make this fail for a reason unrelated to
	// what it tests.
	full := append([]string{
		"-C", dir,
		"-c", "user.email=audit@example",
		"-c", "user.name=audit",
		"-c", "commit.gpgsign=false",
	}, args...)
	if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestCommitTimeIsQuietOutsideARepository, because it feeds the archive's
// timestamp fallback where "not a repository" is an ordinary answer.
func TestCommitTimeIsQuietOutsideARepository(t *testing.T) {
	if got := release.CommitTime(t.TempDir()); !got.IsZero() {
		t.Errorf("CommitTime = %s outside a repository, want the zero time", got)
	}
}
