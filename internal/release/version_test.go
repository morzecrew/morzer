package release_test

import (
	"os/exec"
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
			name:     "the v prefix is dropped, so git tags work unchanged",
			describe: "v1.4.0-0-g3be286c",
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
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=t@example", "-c", "user.name=t", "commit", "-q",
			"--allow-empty", "-m", "first"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

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

// TestCommitTimeIsQuietOutsideARepository, because it feeds the archive's
// timestamp fallback where "not a repository" is an ordinary answer.
func TestCommitTimeIsQuietOutsideARepository(t *testing.T) {
	if got := release.CommitTime(t.TempDir()); !got.IsZero() {
		t.Errorf("CommitTime = %s outside a repository, want the zero time", got)
	}
}
