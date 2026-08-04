package clitest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/test/clitest"
)

// The release store is what `update --to` installs from and what `rollback`
// returns to, so what these assert is mostly about refusals: a bundle that
// does not verify must not land in it, and a prune must not remove the thing
// rollback needs.

func TestReleaseFetchPutsABundleInTheStoreWithoutActivatingIt(t *testing.T) {
	r := clitest.NewInstalled(t)

	out := r.Run("release", "fetch", r.BundleAt("1.3.0"))
	out.ExitCode(0).OutputContains("1.3.0")

	// In the store...
	stored := filepath.Join(r.Root, "opt", "demo", "releases", "1.3.0", "manifest.yaml")
	if _, err := os.Stat(stored); err != nil {
		t.Fatalf("the fetched release is not in the store: %v", err)
	}

	// ...and not current. `fetch` stages, `apply` activates; a fetch that
	// activated would make `release fetch` a deployment.
	current := r.Run("release", "show").ExitCode(0)
	current.OutputContains("1.2.0")
}

func TestReleaseFetchIsIdempotentForTheSameBundle(t *testing.T) {
	r := clitest.NewInstalled(t)
	bundle := r.BundleAt("1.3.0")

	r.Run("release", "fetch", bundle).ExitCode(0)
	again := r.Run("release", "fetch", bundle)

	again.ExitCode(0).OutputContains("already present")
}

// TestReleaseFetchRefusesTwoBundlesClaimingOneVersion is what content-addressed
// identity exists to catch.
func TestReleaseFetchRefusesTwoBundlesClaimingOneVersion(t *testing.T) {
	r := clitest.NewInstalled(t)

	first := r.BundleAt("1.3.0")
	r.Run("release", "fetch", first).ExitCode(0)

	// The same version, one byte different.
	second := r.BundleAt("1.3.0")
	extra := filepath.Join(second, "compose", "extra.yaml")
	if err := os.WriteFile(extra, []byte("# a different bundle\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := r.Run("release", "fetch", second)
	out.Failed()
	out.OutputContains("different digest")
	out.OutputContains("same version")
}

func TestReleaseFetchOfSomethingThatIsNotThere(t *testing.T) {
	r := clitest.NewInstalled(t)

	out := r.Run("release", "fetch", filepath.Join(t.TempDir(), "not-a-bundle"))
	out.Failed().OutputContains("not-a-bundle")
}

func TestReleaseList(t *testing.T) {
	r := clitest.NewInstalled(t).FetchReleases("1.3.0", "1.4.0")

	plain := r.Run("release", "list")
	plain.ExitCode(0)
	for _, want := range []string{"1.2.0", "1.3.0", "1.4.0"} {
		plain.OutputContains(want)
	}

	r.Run("release", "list", "--json").ExitCode(0).FieldLen("data", 3)
}

func TestReleaseShow(t *testing.T) {
	r := clitest.NewInstalled(t).FetchReleases("1.3.0")

	// No argument: the installed one.
	r.Run("release", "show").ExitCode(0).OutputContains("1.2.0")

	// A version in the store.
	r.Run("release", "show", "1.3.0").ExitCode(0).OutputContains("1.3.0")

	// A directory, which works on a machine with no installation at all.
	r.Run("release", "show", r.Bundle).ExitCode(0).OutputContains("demo")

	// A version nobody fetched.
	notThere := r.Run("release", "show", "9.9.9")
	notThere.Failed().OutputContains("not installed")
	notThere.OutputContains("release list")

	// Neither a path nor a version.
	r.Run("release", "show", "wednesday").Failed().
		OutputContains("neither a directory nor a version")
}

func TestReleaseVerify(t *testing.T) {
	r := clitest.New(t)

	good := r.Run("release", "verify", r.Bundle)
	good.ExitCode(0).OutputContains("sha256:")

	// A digest that does not match is the whole point of the command.
	bad := r.Run("release", "verify", r.Bundle,
		"--digest", "sha256:"+strings.Repeat("0", 64))
	bad.Failed().OutputContains("digest")
}

func TestReleaseVerifyOfSomethingThatIsNotABundle(t *testing.T) {
	r := clitest.New(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	r.Run("release", "verify", dir).Failed().
		OutputContains("manifest.yaml")
}

// TestReleasePruneNeverRemovesWhatRollbackNeeds is the guard that makes the
// command safe to put in a timer.
func TestReleasePruneNeverRemovesWhatRollbackNeeds(t *testing.T) {
	r := clitest.NewInstalled(t).FetchReleases("1.3.0", "1.4.0", "1.5.0")

	// Keep nothing beyond the active ones. The current release must
	// survive regardless.
	out := r.Run("release", "prune", "--keep", "1")
	out.ExitCode(0)

	list := r.Run("release", "list")
	list.ExitCode(0).OutputContains("1.2.0")
}

func TestReleasePruneDryRunRemovesNothing(t *testing.T) {
	r := clitest.NewInstalled(t).FetchReleases("1.3.0", "1.4.0", "1.5.0")

	before := storeEntries(t, r)

	out := r.Run("release", "prune", "--keep", "1", "--dry-run")
	out.ExitCode(0).OutputContains("would remove")

	if got := storeEntries(t, r); got != before {
		t.Errorf("a dry run removed releases: %d entries became %d", before, got)
	}
}

func TestReleasePruneWithNothingToDo(t *testing.T) {
	r := clitest.NewInstalled(t)

	r.Run("release", "prune").ExitCode(0).
		OutputContains("nothing to prune")
}

func TestReleasePruneJSON(t *testing.T) {
	r := clitest.NewInstalled(t).FetchReleases("1.3.0", "1.4.0", "1.5.0")

	out := r.Run("release", "prune", "--keep", "1", "--json").ExitCode(0)
	out.Field("data.removed")
	out.Field("data.retained")
}

func storeEntries(t *testing.T, r *clitest.Runner) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(r.Root, "opt", "demo", "releases"))
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}
