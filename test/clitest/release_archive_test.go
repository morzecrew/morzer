package clitest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/morzecrew/morzer/test/clitest"
)

// An archive is the artifact a customer receives, and every property it lacks
// at the moment it is written it lacks permanently. So the refusals matter more
// than the happy path: a tree summed after it changed, or signed before it
// changed, produces a bundle whose integrity evidence describes a different
// bundle -- and both are internally consistent, so nothing downstream can tell.

// TestReleaseArchiveRefusesAnUnsummedBundle.
//
// Archiving a tree with no SHA256SUMS produces a bundle the completeness rule
// cannot be applied to: the verifier fails closed on a file the list does not
// cover, and a list that does not exist covers nothing.
func TestReleaseArchiveRefusesAnUnsummedBundle(t *testing.T) {
	r := clitest.New(t)

	out := r.Run("release", "archive", r.Bundle)

	out.Failed().OutputContains("SHA256SUMS")
}

// TestReleaseArchiveRefusesWritingIntoTheBundle.
//
// The archive would become a file the bundle contains and its own SHA256SUMS
// does not list, so the next `verify` on that directory fails -- over a file
// this command created.
func TestReleaseArchiveRefusesWritingIntoTheBundle(t *testing.T) {
	r := clitest.New(t)

	out := r.Run("release", "archive", r.Bundle,
		"-o", filepath.Join(r.Bundle, "demo.tar.zst"))

	out.Failed().OutputContains("inside the bundle")
}

// TestBuildThenArchiveProducesAFetchableBundle is the vendor's path end to end,
// minus the signing step morzer deliberately does not perform.
//
// Driven through `release fetch` rather than by inspecting the tar, because
// what matters is not that a file appeared but that the transports already in
// the product accept what this wrote.
func TestBuildThenArchiveProducesAFetchableBundle(t *testing.T) {
	r := clitest.NewInstalled(t)
	source := r.BundleAt("1.3.0")

	r.Run("release", "build", source).ExitCode(0).OutputContains("1.3.0")

	dest := filepath.Join(t.TempDir(), "demo-1.3.0.tar.zst")
	r.Run("release", "archive", source, "-o", dest).ExitCode(0)

	r.Run("release", "fetch", dest).ExitCode(0).OutputContains("1.3.0")
}

// TestReleaseArchiveWarnsAboutAnUnsignedBundle rather than refusing one.
//
// Whether a signature is required is the operator's policy. A vendor whose
// customers do not require one is not misbehaving, so the tooling says
// something and proceeds -- which means "the vendor forgot to sign" is caught
// by the operator's policy rather than here, and that is the trade being made.
func TestReleaseArchiveWarnsAboutAnUnsignedBundle(t *testing.T) {
	r := clitest.New(t)
	r.Run("release", "build", r.Bundle).ExitCode(0)

	out := r.Run("release", "archive", r.Bundle, "-o",
		filepath.Join(t.TempDir(), "demo.tar.zst"))

	out.ExitCode(0).StderrContains("no SHA256SUMS.minisig")
}

// TestReleaseBuildRefusesToInvalidateASignature, and --force discards it.
//
// Both halves. Regenerating the list necessarily invalidates any signature over
// it, so a build that left one behind would produce a bundle whose signature
// does not verify -- failing on the customer's machine, for a reason the vendor
// cannot see from their own tree.
func TestReleaseBuildRefusesToInvalidateASignature(t *testing.T) {
	r := clitest.New(t)
	r.Run("release", "build", r.Bundle).ExitCode(0)

	signature := filepath.Join(r.Bundle, "SHA256SUMS.minisig")
	if err := os.WriteFile(signature, []byte("untrusted comment: fake\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r.Run("release", "build", r.Bundle).Failed().OutputContains("--force")

	// Forcing past it deletes the signature rather than building around
	// it: keeping one that no longer verifies is the artifact the refusal
	// exists to prevent.
	r.Run("release", "build", r.Bundle, "--force").ExitCode(0)
	if _, err := os.Stat(signature); err == nil {
		t.Error("--force left a signature over a checksum list it did not sign")
	}
}

// TestReleaseBuildRefusesABrokenBundleBeforeWritingAnything.
//
// A checksum list over a broken tree is evidence that the tree is exactly as
// broken as it is, signed and shipped. The refusal has to come before the
// write, not after it.
func TestReleaseBuildRefusesABrokenBundleBeforeWritingAnything(t *testing.T) {
	r := clitest.New(t)

	manifest := filepath.Join(r.Bundle, "manifest.yaml")
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, append(data, []byte("\nnonsense: true\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	r.Run("release", "build", r.Bundle).Failed().OutputContains("unknown field")

	if _, err := os.Stat(filepath.Join(r.Bundle, "SHA256SUMS")); err == nil {
		t.Error("a refused build wrote a checksum list anyway")
	}
}
