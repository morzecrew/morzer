package clitest_test

import (
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
