package clitest_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/state"
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

// TestReleaseVerifyRefusesATemplateThatCannotParse.
//
// The command's own help calls it "the command a bundle vendor runs in their
// own CI", and before RFC 0013 P1 it printed `bundle is valid` for a bundle
// whose template could not render: it validated the manifest, checked that
// every referenced file *existed*, computed the digest, and never parsed one.
// The failure then arrived during an operator's `apply`, part-way through a
// journaled operation, on a machine belonging to someone who did not write it.
//
// Both halves are asserted. Without the second, the gate could refuse every
// bundle and still look correct here.
func TestReleaseVerifyRefusesATemplateThatCannotParse(t *testing.T) {
	r := clitest.New(t)

	tmpl := filepath.Join(r.Bundle, "templates", "application.yaml.tmpl")
	original, err := os.ReadFile(tmpl)
	if err != nil {
		t.Fatalf("cannot read the example template: %v", err)
	}

	// An unterminated action. A parse failure needs no installation, no
	// parameters and no network, which is what makes checking it safe in
	// the path a vendor runs on every commit.
	broken := "server:\n  url: {{ .Installation.URL\n"
	if err := os.WriteFile(tmpl, []byte(broken), 0o600); err != nil {
		t.Fatalf("cannot write the broken template: %v", err)
	}

	r.Run("release", "verify", r.Bundle).Failed().
		OutputContains("does not parse")

	if err := os.WriteFile(tmpl, original, 0o600); err != nil {
		t.Fatalf("cannot restore the template: %v", err)
	}
	r.Run("release", "verify", r.Bundle).ExitCode(0).
		OutputContains("bundle is valid")
}

// TestPruneNeverRemovesAStagedCandidate.
//
// Staging ahead of the operator's decision is most of what makes channel
// following worth having, and a retention pass that pruned the candidate it had
// just fetched would make it pointless -- the decision would still be waiting,
// with the bundle gone.
//
// The candidate is written through the real state store rather than by hand, so
// a change to how it is stored breaks this test rather than leaving it passing
// against a shape nothing produces.
func TestPruneNeverRemovesAStagedCandidate(t *testing.T) {
	r := clitest.NewInstalled(t).FetchReleases("1.3.0", "1.4.0", "1.5.0")
	ctx := context.Background()

	store := state.New(domain.PathsUnder(r.Root, "demo"))
	require.NoError(t, store.SetUpdateCandidate(ctx, domain.UpdateCandidate{
		SchemaVersion:  domain.UpdateCandidateSchemaVersion,
		SourceRef:      "oci://registry.example/demo/bundle:stable",
		UpstreamDigest: "sha256:" + strings.Repeat("a", 64),
		Name:           "demo",
		Version:        domain.MustParseVersion("1.3.0"),
		Root:           filepath.Join(r.Root, "opt", "demo", "releases", "1.3.0"),
	}))

	// 1.3.0 is the oldest inactive release, so it is the first thing a
	// prune with no room reaches -- which is what makes it the one worth
	// asserting. A candidate that retention would have kept anyway proves
	// nothing about the exemption.
	r.Run("release", "prune", "--keep", "1").ExitCode(0).OutputContains("1.4.0")

	r.Run("release", "list").ExitCode(0).OutputContains("1.3.0")
}

// TestRetentionCountsOnlyTheReleasesItMayRemove.
//
// `--keep` is documented in three places -- the flag's own help, the manifest
// reference and the retention policy -- as the number of *non-active* releases
// to retain. The pass counted the exempt ones toward it, so every role a release
// held ate one of the slots the operator asked for: on a machine with a current
// and a previous release, `--keep 1` retained nothing at all.
//
// The staged candidate is what makes this assertable without an update: it is an
// exempt release sorting *above* the inactive ones, which is the arrangement the
// miscount needs. On a real machine that arrangement is just "current is the
// newest release", which is every machine.
func TestRetentionCountsOnlyTheReleasesItMayRemove(t *testing.T) {
	r := clitest.NewInstalled(t).FetchReleases("1.3.0", "1.4.0", "1.5.0")
	ctx := context.Background()

	store := state.New(domain.PathsUnder(r.Root, "demo"))
	require.NoError(t, store.SetUpdateCandidate(ctx, domain.UpdateCandidate{
		SchemaVersion:  domain.UpdateCandidateSchemaVersion,
		SourceRef:      "oci://registry.example/demo/bundle:stable",
		UpstreamDigest: "sha256:" + strings.Repeat("b", 64),
		Name:           "demo",
		Version:        domain.MustParseVersion("1.5.0"),
		Root:           filepath.Join(r.Root, "opt", "demo", "releases", "1.5.0"),
	}))

	// Exempt: 1.2.0 current and 1.5.0 staged. Inactive: 1.4.0 and 1.3.0, of
	// which `--keep 1` retains the newer.
	out := r.Run("release", "prune", "--keep", "1").ExitCode(0)
	out.OutputContains("1.3.0")

	list := r.Run("release", "list").ExitCode(0)
	list.OutputContains("1.4.0")
	// And the reason 1.5.0 survived is visible, rather than leaving an
	// operator arguing with the retention policy about a release they can
	// see and cannot remove. The marker and the word, because the marker
	// alone is a character an operator has to look up.
	list.SaysAll("+ 1.5.0", "staged")
}

// TestRenderCheckCatchesWhatParsingCannot is the pair that keeps the two modes
// from collapsing into one.
//
// A template naming a secret the schema does not declare parses perfectly: it is
// a function call with a string argument, and nothing at parse time knows what
// the bundle declares. It fails the moment it is rendered. Without both halves
// asserted here, `--render-check` could be an alias for the parse pass and
// nothing would notice -- which is the flag's entire justification gone.
func TestRenderCheckCatchesWhatParsingCannot(t *testing.T) {
	r := clitest.New(t)

	tmpl := filepath.Join(r.Bundle, "templates", "application.yaml.tmpl")
	original, err := os.ReadFile(tmpl)
	if err != nil {
		t.Fatalf("cannot read the example template: %v", err)
	}

	// Valid syntax, undeclared secret. This is the shape of a real typo:
	// the schema calls it db_password.
	broken := "secrets:\n  db: {{ secretFile .Secrets \"db_passwrod\" }}\n"
	if err := os.WriteFile(tmpl, []byte(broken), 0o600); err != nil {
		t.Fatalf("cannot write the template: %v", err)
	}

	r.Run("release", "verify", r.Bundle).ExitCode(0).
		OutputContains("bundle is valid")

	failed := r.Run("release", "verify", r.Bundle, "--render-check").Failed()
	failed.OutputContains("does not render")
	// And names the secret, because "does not render" alone tells an author
	// which template failed and nothing about why.
	failed.OutputContains("db_passwrod")

	// A correct bundle still passes both, so the new gate cannot start
	// refusing what it is supposed to bless.
	if err := os.WriteFile(tmpl, original, 0o600); err != nil {
		t.Fatalf("cannot restore the template: %v", err)
	}
	r.Run("release", "verify", r.Bundle).ExitCode(0)
	r.Run("release", "verify", r.Bundle, "--render-check").ExitCode(0).
		OutputContains("synthetic context")
}

// TestRenderCheckIsOptIn.
//
// Decision 12 is that it stays opt-in permanently, so the default path must not
// acquire it by accident. A bundle that fails the render check and passes plain
// `verify` is the assertion; the reverse -- a flag that silently became the
// default -- is what it catches.
func TestRenderCheckIsOptIn(t *testing.T) {
	r := clitest.New(t)

	tmpl := filepath.Join(r.Bundle, "templates", "application.yaml.tmpl")
	// `.Installation.Urrl` is a field that does not exist on the type: it
	// parses, and rendering it is an error rather than an empty string
	// because the renderer runs with missingkey=error.
	if err := os.WriteFile(tmpl,
		[]byte("url: {{ .Installation.Urrl }}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	r.Run("release", "verify", r.Bundle).ExitCode(0)
	r.Run("release", "verify", r.Bundle, "--render-check").Failed()
}

// TestUpdateCheckRefusesRatherThanReportingUpToDate.
//
// The command's value is that it never answers a question nobody asked. A
// freshly installed machine has no recorded release source -- `init` installs
// from a path -- so the check must say so rather than report that nothing newer
// exists.
func TestUpdateCheckRefusesRatherThanReportingUpToDate(t *testing.T) {
	r := clitest.NewInstalled(t)

	res := r.Run("update", "--check")
	res.Failed()
	out := res.Stdout + res.Stderr
	if strings.Contains(out, "nothing newer is offered") {
		t.Error("a check that could not run reported that nothing newer exists")
	}

	// --to names something already in the store; --check asks the source
	// what it offers. Silently ignoring one of them is the failure.
	r.Run("update", "--check", "--to", "1.3.0").Failed().
		OutputContains("alternatives")
}

// TestACandidateRecordThatCannotBeReadStopsThePruneAndShowsUpInStatus.
//
// The three role lookups that decide which releases are exempt used to discard
// their errors, so a read that failed answered "not current, not previous, not
// staged" -- and a retention pass acting on that answer removes the release a
// poll had just fetched, or worse. Refusing costs an operator a retry; the other
// way costs them the bundle, and on a failed read of the *current* release it
// costs them the deployment.
//
// The pair is asserted together because they are one decision: the record is
// authoritative while it exists, so nothing may quietly treat an unreadable one
// as an absent one -- and `status` is where an operator finds out why the prune
// refused.
func TestACandidateRecordThatCannotBeReadStopsThePruneAndShowsUpInStatus(t *testing.T) {
	r := clitest.NewInstalled(t).FetchReleases("1.3.0", "1.4.0", "1.5.0")

	paths := domain.PathsUnder(r.Root, "demo")
	require.NoError(t, os.WriteFile(paths.UpdateCandidateFile(),
		[]byte("{\"version\": \n"), 0o640))

	r.Run("release", "prune", "--keep", "1").Failed().
		OutputContains("update candidate")

	if got := storeEntries(t, r); got != 4 {
		t.Errorf("the prune removed %d release(s) it could not prove were unstaged", 4-got)
	}

	r.Run("status").ExitCode(0).OutputContains("cannot read the update candidate")
}

// TestReleaseFetchReportsWhatItFetched.
//
// `finish` is what publishes JSON, and it publishes the result's own data --
// so a payload assigned to the app beside it was overwritten by this call's
// empty one, and `release fetch --json` answered with a null data field. The
// command exists to be scripted: whatever fetched the bundle is the thing that
// needs its version and its root.
//
// Both runs are asserted, because the already-present branch is a second call
// with a second chance to drop the payload -- and it is the one a re-run hits.
func TestReleaseFetchReportsWhatItFetched(t *testing.T) {
	r := clitest.NewInstalled(t)
	bundle := r.BundleAt("1.3.0")

	first := r.Run("release", "fetch", bundle, "--json").ExitCode(0)
	first.FieldEquals("data.version", "1.3.0")
	first.Field("data.digest")
	first.Field("data.root")

	again := r.Run("release", "fetch", bundle, "--json").ExitCode(0)
	again.FieldEquals("data.version", "1.3.0")
}

// TestRetentionFallsBackThroughThePolicyNotPastIt.
//
// When no release is current, or the current one's manifest will not load,
// there is no vendor declaration to read -- and the fallback returned the
// built-in default directly, discarding `policy.retain_releases`. The
// precedence rule is that the operator's policy wins over the vendor's
// declaration, so losing the operator's number in the case where the vendor has
// none is precisely backwards.
//
// A broken current release is the reachable way there: a partial fetch, an
// interrupted apply, a half-restored root. That is also when an operator is most
// likely to be reclaiming disk.
func TestRetentionFallsBackThroughThePolicyNotPastIt(t *testing.T) {
	r := clitest.NewInstalled(t).FetchReleases("1.3.0", "1.4.0", "1.5.0")
	ctx := context.Background()

	store := state.New(domain.PathsUnder(r.Root, "demo"))
	inst, err := store.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Policy.RetainReleases = 1
	require.NoError(t, store.SaveInstallation(ctx, inst))

	require.NoError(t, os.Remove(filepath.Join(
		r.Root, "opt", "demo", "releases", "1.2.0", "manifest.yaml")))

	// No --keep: the number comes from the policy, which is the whole point.
	out := r.Run("release", "prune").ExitCode(0)
	out.OutputContains("1.3.0")
	out.OutputContains("1.4.0")

	r.Run("release", "list").ExitCode(0).OutputContains("1.5.0")
}
