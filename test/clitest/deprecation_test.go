package clitest_test

import (
	"path/filepath"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/test/clitest"
)

// A vendor's CI is one of the two places a deprecation warning can still change
// a bundle: here, before it is published, and at the moment an operator chooses
// to install it. Every other command meets the same manifest with no choice
// available, which is why `release.Load` refuses to be the place this happens.

// The example bundle is written the old way -- deliberately, since it is the
// fixture for the deprecated read path -- so `verify` has something to warn
// about that a released bundle really looks like.
func TestReleaseVerifyWarnsAboutTheDeprecatedRuntimeBlock(t *testing.T) {
	r := clitest.New(t)

	r.Run("release", "verify", r.Bundle).ExitCode(0).
		StderrContains("`runtime` is deprecated", domain.FieldRemovalRelease, "runtimes.compose")
}

// A warning, not a failure. `verify` answers "is this bundle installable", and
// a deprecated field still is until the release that stops reading it -- a
// vendor who wants the clock enforced has their own CI's exit code to spend.
func TestADeprecatedFieldDoesNotFailVerification(t *testing.T) {
	r := clitest.New(t)

	r.Run("release", "verify", r.Bundle, "--render-check").ExitCode(0).
		StdoutContains("demo 1.2.0")
}

// The other of the two moments: an operator choosing this bundle. They cannot
// edit it, but they can decline it and ask their vendor for one written the new
// way -- which is the whole reason the warning is here and not on every command
// that later reads the same manifest.
//
// `init` specifically. The api_version warning lived on the update path alone,
// so a first install said nothing at all; found while adding the field warning
// beside it, and this is the assertion that keeps both on this path.
func TestAFirstInstallWarnsAboutADeprecatedBundle(t *testing.T) {
	r := clitest.New(t)

	r.Run("init",
		"--release", r.Bundle,
		"--profile", "embedded",
		"--domain", "demo.example",
		"--no-recovery-recipient",
		"--install-units=false",
	).ExitCode(0).
		StderrContains("`runtime` is deprecated", domain.FieldRemovalRelease)
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
func TestAPlannedInstallWarnsAboutADeprecatedBundle(t *testing.T) {
	r := clitest.New(t)

	r.Run("init",
		"--release", r.Bundle,
		"--profile", "embedded",
		"--domain", "demo.example",
		"--no-recovery-recipient",
		"--install-units=false",
		"--dry-run",
	).ExitCode(0).
		StderrContains("`runtime` is deprecated", domain.FieldRemovalRelease)
}

// The warning is one sentence and has to read like one.
//
// `FieldDeprecation.Message()` already opens with the field name -- "`runtime`
// is deprecated ..." -- and the caller prepended "this bundle uses", composing
// "this bundle uses `runtime` is deprecated": two verbs in one clause. `release
// verify` prints the same message bare and always read correctly, so the defect
// was in the join rather than in the sentence.
//
// Asserted on a real install rather than a plan, deliberately. The plan is where
// the warning was missing entirely, so a grammar assertion there would have
// passed by finding no warning at all -- which is the shape of a test that
// guards nothing.
func TestTheDeprecationWarningIsOneSentence(t *testing.T) {
	r := clitest.New(t)

	r.Run("init",
		"--release", r.Bundle,
		"--profile", "embedded",
		"--domain", "demo.example",
		"--no-recovery-recipient",
		"--install-units=false",
	).ExitCode(0).
		StderrContains("`runtime` is deprecated").
		NoOutputContains("uses `runtime` is deprecated")
}

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
