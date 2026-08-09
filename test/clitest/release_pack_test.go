package clitest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/test/clitest"
)

// `pack` reaches a registry, so what can be exercised here is everything it
// decides *before* it does — which is where all four of its refusals live.

// TestReleasePackRefusesABundleWithNothingToPack.
//
// Silently succeeding would suggest there was something to pack, which is
// exactly the impression a vendor who mistyped `from: bundle` would take away.
func TestReleasePackRefusesABundleWithNothingToPack(t *testing.T) {
	r := clitest.New(t)

	r.Run("release", "pack", r.Bundle).
		Failed().OutputContains("from: bundle")
}

// TestReleasePackRefusesAPlatformItCannotRead, before it touches a registry —
// so a typo costs nothing but the message.
func TestReleasePackRefusesAPlatformItCannotRead(t *testing.T) {
	r := clitest.New(t)

	r.Run("release", "pack", r.Bundle, "--platform", "linux").
		Failed().OutputContains("platform")
}

// TestReleasePackRefusesToInvalidateASignature, and --force discards it.
//
// Packing rewrites the checksum list, so any signature over it stops covering
// the tree. Leaving one in place produces the wrongly-signed bundle the whole
// chain exists to prevent.
func TestReleasePackRefusesToInvalidateASignature(t *testing.T) {
	r := clitest.New(t)
	markBundled(t, r.Bundle)

	signature := filepath.Join(r.Bundle, "SHA256SUMS.minisig")
	if err := os.WriteFile(signature, []byte("untrusted comment: fake\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r.Run("release", "pack", r.Bundle).Failed().OutputContains("--force")

	// With --force the signature is gone before anything is copied: the
	// command still fails here, because there is no registry to reach, but
	// the refusal it fails with must not be the signature one.
	out := r.Run("release", "pack", r.Bundle, "--force")
	out.Failed()
	if strings.Contains(out.Stderr, "--force") {
		t.Errorf("--force did not get past the signature refusal:\n%s", out.Stderr)
	}
	if _, err := os.Stat(signature); err == nil {
		t.Error("--force left a signature over a checksum list it will not have signed")
	}
}

// TestReleasePackDryRunTouchesNothing.
func TestReleasePackDryRunTouchesNothing(t *testing.T) {
	r := clitest.New(t)
	markBundled(t, r.Bundle)

	r.Run("--dry-run", "release", "pack", r.Bundle).ExitCode(0).OutputContains("would copy")

	if _, err := os.Stat(filepath.Join(r.Bundle, "images")); err == nil {
		t.Error("a dry run created an image layout")
	}
}

// markBundled marks the fixture's `app` image as travelling in the bundle.
func markBundled(t *testing.T, dir string) {
	t.Helper()

	manifest := filepath.Join(dir, "manifest.yaml")
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	const digest = "@sha256:0000000000000000000000000000000000000000000000000000000000000001"
	old := "  app: registry.example/demo/app" + digest + "\n"
	if !strings.Contains(string(data), old) {
		t.Fatalf("the fixture no longer pins app to %s", digest)
	}
	replaced := strings.Replace(string(data), old,
		"  app:\n    ref: registry.example/demo/app"+digest+"\n    from: bundle\n", 1)
	if err := os.WriteFile(manifest, []byte(replaced), 0o644); err != nil {
		t.Fatal(err)
	}
}
