package installer

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// `.github/scripts/publish-install-script.sh` driven against a real git remote.
//
// The script is what puts `install.sh` at the documentation site's root, which
// is the URL the README and the installation page tell people to curl. It ran
// on the 0.1.0 release, reported "install.sh at the site root is already these
// bytes", exited 0 — and published nothing, because `git diff` says nothing at
// all about an untracked path. The site returned 404 for the whole of the first
// release.
//
// Nothing exercised it because the case that failed is the *first* run, and by
// the time anybody would have written a test the file would already exist. So
// these tests build the empty root every time.

// publishRepo is a bare "origin" with a gh-pages branch, plus a working clone
// to run the script from. It stands in for the repository the workflow has:
// the script fetches origin, worktrees the branch, and pushes back.
type publishRepo struct {
	t      *testing.T
	origin string
	work   string
}

func newPublishRepo(t *testing.T) *publishRepo {
	t.Helper()
	root := t.TempDir()
	r := &publishRepo{
		t:      t,
		origin: filepath.Join(root, "origin.git"),
		work:   filepath.Join(root, "work"),
	}

	r.run(root, "git", "init", "--bare", "-b", "main", r.origin)
	r.run(root, "git", "clone", "-q", r.origin, r.work)
	r.git("config", "user.email", "test@example.invalid")
	r.git("config", "user.name", "test")

	// gh-pages as mike leaves it: an index and a version directory, and no
	// install.sh. This is the state the first release publishes into.
	r.git("checkout", "-q", "-b", "gh-pages")
	r.write("index.html", "<html>site</html>")
	r.write("versions.json", "[]")
	r.git("add", "-A")
	r.git("commit", "-qm", "mike")
	r.git("push", "-q", "-u", "origin", "gh-pages")
	return r
}

func (r *publishRepo) run(dir, name string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (r *publishRepo) git(args ...string) string { return r.run(r.work, "git", args...) }

func (r *publishRepo) write(name, body string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.work, name), []byte(body), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// publish runs the real script from the working clone.
func (r *publishRepo) publish() string {
	r.t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", ".github", "scripts", "publish-install-script.sh"))
	if err != nil {
		r.t.Fatal(err)
	}
	return r.run(r.work, "bash", script)
}

// published reads install.sh back out of the branch on the remote, which is
// what the site actually serves.
func (r *publishRepo) published() (string, bool) {
	r.t.Helper()
	cmd := exec.Command("git", "show", "origin/gh-pages:install.sh")
	cmd.Dir = r.work
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

// head is the branch's tip on the remote, read from the remote itself.
//
// Not a commit count out of the working clone: the script fetches with
// `--depth 1`, so after it has run the clone's history is shallow and counting
// says 1 however many commits the branch really has.
func (r *publishRepo) head() string {
	out := r.run(r.work, "git", "ls-remote", r.origin, "refs/heads/gh-pages")
	return strings.Fields(out)[0]
}

// rewriteRootFromElsewhere changes the branch through a second clone, the way
// a docs deploy does -- without disturbing the working clone this script runs
// from, whose tree holds the untracked source file.
func (r *publishRepo) rewriteRootFromElsewhere(name, body string) {
	r.t.Helper()
	other := filepath.Join(r.t.TempDir(), "other")
	r.run(filepath.Dir(other), "git", "clone", "-q", "-b", "gh-pages", r.origin, other)
	r.run(other, "git", "config", "user.email", "deploy@example.invalid")
	r.run(other, "git", "config", "user.name", "deploy")
	if err := os.WriteFile(filepath.Join(other, name), []byte(body), 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.run(other, "git", "add", "-A")
	r.run(other, "git", "commit", "-qm", "a deploy rewrote the root")
	r.run(other, "git", "push", "-q")
}

// TestTheFirstPublicationActuallyPublishes is the regression.
//
// An empty root, one run, and the file has to be on the branch afterwards.
// Before the fix this passed its exit code and published nothing.
func TestTheFirstPublicationActuallyPublishes(t *testing.T) {
	r := newPublishRepo(t)
	r.write("install.sh", "#!/bin/sh\necho one\n")

	out := r.publish()

	body, ok := r.published()
	if !ok {
		t.Fatalf("install.sh is not on the branch after publishing:\n%s", out)
	}
	if body != "#!/bin/sh\necho one\n" {
		t.Errorf("published bytes are not the source's: %q", body)
	}
	if strings.Contains(out, "already these bytes") {
		t.Errorf("it claimed the file was already published, and it did not exist:\n%s", out)
	}
}

// TestPublishingIsExecutable: the site serves it and people pipe it to sh, but
// a checkout of the branch should be runnable too, and git records the bit.
func TestThePublishedScriptKeepsItsExecutableBit(t *testing.T) {
	r := newPublishRepo(t)
	r.write("install.sh", "#!/bin/sh\necho one\n")
	r.publish()

	mode := strings.TrimSpace(r.run(r.work, "git", "ls-tree", "origin/gh-pages", "install.sh"))
	if !strings.HasPrefix(mode, "100755") {
		t.Errorf("published mode is not executable: %q", mode)
	}
}

// TestPublishingTwiceCommitsOnce keeps the fix from becoming a commit on every
// docs release. The step runs on each one deliberately, because mike owns the
// root and a sibling file surviving is its behaviour rather than its promise.
func TestPublishingTwiceCommitsOnce(t *testing.T) {
	r := newPublishRepo(t)
	r.write("install.sh", "#!/bin/sh\necho one\n")
	r.publish()
	first := r.head()

	out := r.publish()

	if got := r.head(); got != first {
		t.Errorf("a second identical publication moved the branch: %s then %s", first, got)
	}
	if !strings.Contains(out, "already these bytes") {
		t.Errorf("it did not report the file as unchanged:\n%s", out)
	}
}

// TestARewrittenRootIsRepublished is the case the step exists for: a docs
// deploy that removed or replaced the file at the root.
func TestARewrittenRootIsRepublished(t *testing.T) {
	r := newPublishRepo(t)
	r.write("install.sh", "#!/bin/sh\necho one\n")
	r.publish()

	// Something else rewrote the root's copy.
	r.rewriteRootFromElsewhere("install.sh", "#!/bin/sh\necho stale\n")

	// The source is unchanged; publishing must restore it.
	r.publish()

	body, ok := r.published()
	if !ok {
		t.Fatal("install.sh vanished")
	}
	if body != "#!/bin/sh\necho one\n" {
		t.Errorf("the rewritten copy was not replaced: %q", body)
	}
}
