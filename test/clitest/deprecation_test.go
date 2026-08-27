package clitest_test

import (
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/release"

	"github.com/morzecrew/morzer/test/clitest"
)

// `runtime:` stopped being read in 0.3.0 (RFC 0023 decision 23), so what these
// assert is a refusal where they used to assert a warning. The distinction is
// the whole of that decision: a warning offers a grace period, and there was
// never a released manager that could read both spellings, so there was no
// grace period to offer.

// A vendor's CI is where this has to land, and it now lands as a failure.
func TestReleaseVerifyRefusesTheDeprecatedRuntimeBlock(t *testing.T) {
	r := clitest.New(t)

	r.Run("release", "verify", r.LegacyBundle()).ExitCode(2).
		StderrContains("is no longer read", "runtimes.compose")
}

// The current spelling verifies, which is the other half of the same claim: the
// refusal names a block, not any bundle that happens to reach it.
func TestTheCurrentSpellingVerifies(t *testing.T) {
	r := clitest.New(t)

	r.Run("release", "verify", r.Bundle, "--render-check").ExitCode(0).
		StdoutContains("demo 1.2.0")
}

// The operator's side. They cannot edit the bundle, so the refusal has to name
// what their vendor must change -- an error that only says "invalid" leaves
// them with nothing to ask for.
func TestAFirstInstallRefusesADeprecatedBundle(t *testing.T) {
	r := clitest.New(t)
	bundle := r.LegacyBundle()

	// The path is asserted, not incidental. `ParseManifest` prefixes the
	// source so an author with several bundles open knows which one is
	// being complained about, and until this line nothing checked that any
	// path was named at all: replacing the source with an empty string
	// degrades every refusal to `error: : manifest is invalid:` and passed
	// the whole suite. Found by sabotage while fixing the plan's half of
	// the same claim.
	r.Run("init",
		"--release", bundle,
		"--profile", "embedded",
		"--domain", "demo.example",
		"--no-recovery-recipient",
		"--install-units=false",
	).ExitCode(2).
		StderrContains("is no longer read", "runtimes.compose", bundle)
}

// The update path is asserted in the suite, where an update runs to completion
// against a fake runtime; see TestAnUpdateWarnsAboutADeprecatedBundle.

// The ratchet this wave turns. A project that warns about a field its own
// scaffold writes has deprecated nothing, and the scaffold emitted `runtime:`
// until this wave -- so every bundle authored with the documented starting
// point was born on the spelling the manager was about to stop reading.
//
// Asserted against the generator's real output rather than by reading the
// template, so a later edit that reintroduces the block fails here.
func TestTheScaffoldWritesNothingItWillWarnAbout(t *testing.T) {
	r := clitest.New(t)
	dir := filepath.Join(t.TempDir(), "my-product")

	r.Run("release", "new", dir, "--vendor", "example").ExitCode(0)

	r.Run("release", "verify", dir, "--render-check").ExitCode(0).
		NoOutputContains("deprecated")
}

// The plan is the moment the warning is worth most, and it was the one moment
// that never carried it.
//
// An operator running `init --dry-run` is deciding whether to install this
// bundle at all -- which is exactly the choice the deprecation exists to inform
// -- and the warning lived inside the staging step, which a plan never runs. So
// the operator who looked before leaping was the only one not told.
func TestAPlannedInstallRefusesADeprecatedBundle(t *testing.T) {
	r := clitest.New(t)

	r.Run("init",
		"--release", r.LegacyBundle(),
		"--profile", "embedded",
		"--domain", "demo.example",
		"--no-recovery-recipient",
		"--install-units=false",
		"--dry-run",
	).ExitCode(2).
		StderrContains("is no longer read")
}

// The plan reads a copy, and the copy's path is no answer to "which file?".
//
// `checkPlannedRelease` stages the bundle into a temporary directory to read
// it, and `ParseManifest` prefixes whatever file it was handed -- so a refusal
// named `/tmp/morzer-plan-3095461798/manifest.yaml`, a directory the operator
// never chose and which is removed before they can go and look at it. That
// prefix exists so "an author with several bundles open knows which file is
// being complained about"; a temp path answers that question with a path they
// cannot place, which is worse than not prefixing at all.
//
// `--product` is what makes this reachable, and it is the whole reason the
// defect survived review: without it the CLI reads the manifest at the source
// to learn the product name and refuses there, naming the real path by
// accident. With it, the plan's copy is the only manifest anybody read.
func TestAPlannedRefusalNamesTheBundleTheOperatorPassed(t *testing.T) {
	r := clitest.New(t)
	bundle := r.LegacyBundle()

	r.Run("init",
		"--product", "demo",
		"--release", bundle,
		"--profile", "embedded",
		"--domain", "demo.example",
		"--no-recovery-recipient",
		"--install-units=false",
		"--dry-run",
	).ExitCode(2).
		StderrContains("is no longer read", bundle).
		NoOutputContains("morzer-plan-")
}

// The warning-is-one-sentence assertion that stood here has moved to
// TestTheDeprecationMachineryStillRendersASentenceThatCanBeActedOn in
// internal/domain. It drove the field-deprecation join through a real install,
// and no manifest produces a field deprecation any more -- so run here it would
// have passed by finding no warning at all, which is the shape of a test that
// guards nothing. The invariant it was really protecting is that the message is
// a whole sentence, and that is asserted where the sentence is built.

// A plan names what it is planning.
//
// The closing line read "installation  created for " -- two empty slots and a
// past-tense claim of a creation, printed directly beneath "nothing was
// changed". The product was never unknown: the CLI reads the manifest before the
// operation to build the managed paths, and passes the name in. The summary was
// reading the installation out of engine state instead, which no step had
// populated because a plan runs no steps.
func TestAPlanNamesTheProductItWouldCreate(t *testing.T) {
	r := clitest.New(t)

	res := r.Run("init",
		"--release", r.Bundle,
		"--profile", "embedded",
		"--domain", "demo.example",
		"--no-recovery-recipient",
		"--install-units=false",
		"--dry-run",
	).ExitCode(0)

	res.OutputContains("demo")
	// A plan has created nothing, so it must not say it has.
	res.NoOutputContains("created for")
}

// The same claim on the surface something parses.
//
// Asserted separately because the text summary and `data.product` are two
// different promises, and only the second is a contract: blanking the JSON field
// while leaving the sentence intact killed no test until this one existed. The
// empty installation id is asserted too -- a plan that reported one would be
// naming a record nobody wrote.
func TestAPlansJSONNamesTheProductAndNoInstallation(t *testing.T) {
	r := clitest.New(t)

	out := r.Run("init",
		"--release", r.Bundle,
		"--profile", "embedded",
		"--domain", "demo.example",
		"--no-recovery-recipient",
		"--install-units=false",
		"--dry-run",
		"--json",
	).ExitCode(0)

	out.FieldEquals("data.product", "demo")
	out.FieldEquals("data.installation_id", "")
	out.FieldEquals("ok", true)
}

// The same bundle, packed the way a vendor ships it, refused by the operation.
//
// Asserted on a real install rather than a plan, and that is the finding this
// test carries. **A plan does not validate the bundle it plans against**: with
// `--product` given, `init --dry-run` over this archive reports "would create
// an installation" for a release the very next command refuses. It is refused
// without `--product` only because the CLI then has to read the manifest to
// learn the name, and validation comes with the read -- an incidental
// mechanism, not a check.
//
// Measured both ways while writing this. Recorded rather than fixed here: a
// plan that validates is RFC 0001 decision 12's territory and its own change.
func TestAnInstallFromAnArchiveIsRefusedToo(t *testing.T) {
	r := clitest.New(t)

	// Packed directly rather than with `release archive`, which now refuses
	// the same bundle for the same reason -- so after the removal this
	// project can no longer produce a legacy archive through its own
	// commands, and a test for one has to build it.
	bundle := r.LegacyBundle()
	var entries []string
	if err := filepath.WalkDir(bundle, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		rel, relErr := filepath.Rel(bundle, path)
		if relErr != nil {
			return relErr
		}
		if rel == release.ManifestFileName {
			return nil // added first, below
		}
		entries = append(entries, rel)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// manifest.yaml first: a release archive states its size in the
	// manifest, so the manifest has to arrive before the bytes it bounds.
	entries = append([]string{release.ManifestFileName}, entries...)
	archive := filepath.Join(t.TempDir(), "demo-1.2.0.tar.zst")
	if err := atomicfs.WriteTarZst(archive, bundle, entries, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}

	r.Run("init",
		"--product", "demo",
		"--release", archive,
		"--profile", "embedded",
		"--domain", "demo.example",
		"--no-recovery-recipient",
		"--install-units=false",
	).ExitCode(11).
		StderrContains("is no longer read")
}

// `--repair` restores an installation that is already there, and both summaries
// called it a creation.
//
// The plan's was the worse of the two: "would create an installation" printed
// beside an empty installation id, for a record that exists. An operator reading
// a plan to check they are repairing the right machine is reading the one line
// that did not distinguish the two.
func TestAPlannedRepairSaysRepair(t *testing.T) {
	r := clitest.New(t)

	install := []string{"init",
		"--release", r.Bundle,
		"--profile", "embedded",
		"--domain", "demo.example",
		"--no-recovery-recipient",
		"--install-units=false",
	}
	r.Run(install...).ExitCode(0)

	plan := r.Run(append(append([]string{}, install...), "--repair", "--dry-run")...).ExitCode(0)
	plan.OutputContains("would repair")
	plan.NoOutputContains("would create")

	// The operation says it too: the plan and the run describe one act.
	done := r.Run(append(append([]string{}, install...), "--repair")...).ExitCode(0)
	done.OutputContains("repaired for demo")
	done.NoOutputContains("created for demo")
}

// A remote reference gets no warning, and that is the decision rather than an
// oversight.
//
// Reading the manifest means materialising the bundle, and for a registry that
// is a network pull -- a cost nobody asks a plan for, to phrase an advisory. So
// the plan declines, and this pins both halves of that: no warning, and no
// attempt to reach the registry (the reference below resolves nowhere, and the
// plan still succeeds promptly).
//
// It is a real gap and it is carried as one. The test exists so that closing it
// is a deliberate act rather than a silent one.
func TestAPlanOverARemoteReferenceDeclinesToWarn(t *testing.T) {
	r := clitest.New(t)

	res := r.Run("init",
		"--product", "demo",
		"--release", "oci://registry.invalid/demo:1.2.0",
		"--profile", "embedded",
		"--domain", "demo.example",
		"--no-recovery-recipient",
		"--install-units=false",
		"--dry-run",
	).ExitCode(0)

	res.OutputContains("would create an installation for demo")
	res.NoOutputContains("is deprecated")
}

// A plan refuses what the run would refuse, and D-055 is the gap it closes.
//
// `--product` is given deliberately. Without it the CLI reads the manifest to
// learn the product name, and validation arrives with the read -- so the plan
// refused for a reason that had nothing to do with checking anything, and
// supplying the name silently removed the only check there was.
func TestAPlanRefusesABundleTheRunWouldRefuse(t *testing.T) {
	r := clitest.New(t)

	res := r.Run("init",
		"--product", "demo",
		"--release", r.LegacyBundle(),
		"--profile", "embedded",
		"--domain", "demo.example",
		"--no-recovery-recipient",
		"--install-units=false",
		"--dry-run",
	).ExitCode(2)

	res.StderrContains("is no longer read")
	res.NoOutputContains("would create")
}

// A plan that could not check the bundle says so, on both surfaces.
//
// The decision not to fetch a remote reference is unchanged and still pinned by
// TestAPlanOverARemoteReferenceDeclinesToWarn. What changes is that silence no
// longer stands in for a clean bill of health: the plan printed its steps and
// "would create an installation" whether it had validated anything or not, and
// nothing in that output told the two apart.
func TestAPlanOverARemoteReferenceSaysItDidNotValidate(t *testing.T) {
	r := clitest.New(t)

	res := r.Run("init",
		"--product", "demo",
		"--release", "oci://registry.invalid/demo:1.2.0",
		"--profile", "embedded",
		"--domain", "demo.example",
		"--no-recovery-recipient",
		"--install-units=false",
		"--dry-run",
	).ExitCode(0)

	res.OutputContains("did not validate the bundle's manifest")
	// Still a plan, and still promptly: declining to look is not a refusal.
	res.OutputContains("would create an installation for demo")
}

// The same claim on the surface something parses.
//
// Asserted separately from the sentence for the reason the sibling test above
// gives: the text and the field are two different promises, and only the field
// is a contract.
func TestAPlansJSONSaysWhetherItValidatedTheManifest(t *testing.T) {
	r := clitest.New(t)

	base := []string{"init",
		"--product", "demo",
		"--profile", "embedded",
		"--domain", "demo.example",
		"--no-recovery-recipient",
		"--install-units=false",
		"--dry-run", "--json",
	}

	local := r.Run(append(append([]string{}, base...), "--release", r.Bundle)...).ExitCode(0)
	local.FieldEquals("data.manifest_validated", true)

	remote := r.Run(append(append([]string{}, base...),
		"--release", "oci://registry.invalid/demo:1.2.0")...).ExitCode(0)
	remote.FieldEquals("data.manifest_validated", false)
}

// A plan refuses a `--set` the manifest does not declare.
//
// The same gap as D-055 arriving through the parameters rather than the
// manifest: `stage-release` rejects an undeclared assignment before the release
// is adopted, and a plan that does not was approving an invocation the run
// refuses. Both checks are pure functions over the manifest the plan has
// already loaded and the flags it was handed, so this costs no extra read.
func TestAPlanRefusesAnUndeclaredParameter(t *testing.T) {
	r := clitest.New(t)

	res := r.Run("init",
		"--product", "demo",
		"--release", r.Bundle,
		"--profile", "embedded",
		"--domain", "demo.example",
		"--no-recovery-recipient",
		"--install-units=false",
		"--set", "nosuchparameter=1",
		"--dry-run",
	).ExitCode(2)

	res.NoOutputContains("would create")
}

// A plan refuses a declaration the manifest gives no default and the operator
// gives no value.
//
// The other half of what `stage-release` checks before adopting a release, and
// the half a sabotage sweep found untested: the example bundle gives every
// parameter a default, so nothing in the suite reached `MissingValues` until a
// plan started calling it.
func TestAPlanRefusesAMissingRequiredParameter(t *testing.T) {
	r := clitest.New(t)
	bundle := r.BundleWithARequiredParameter()

	base := []string{"init",
		"--product", "demo",
		"--release", bundle,
		"--profile", "embedded",
		"--domain", "demo.example",
		"--no-recovery-recipient",
		"--install-units=false",
		"--dry-run",
	}

	res := r.Run(base...).ExitCode(2)
	res.StderrContains("declares no default for admin_email")
	res.NoOutputContains("would create")

	// Supplied, the same plan goes through: the check is the missing value,
	// not the declaration.
	r.Run(append(append([]string{}, base...),
		"--set", "admin_email=ops@demo.example")...).ExitCode(0).
		OutputContains("would create an installation for demo")
}
