package release_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/release"
)

// TestStampingWritesBothFiles.
//
// The loader refuses a bundle whose VERSION and manifest disagree, so stamping
// one and not the other produces a bundle that no longer loads -- from the
// command whose whole job was to set the version. It is the single most likely
// defect in the stamper, which is why it is asserted through the loader rather
// than by reading the two files.
func TestStampingWritesBothFiles(t *testing.T) {
	dir := bundle(t, nil)

	if err := release.Stamp(dir, mustVersion(t, "1.4.1-dev.7.gabc1234")); err != nil {
		t.Fatal(err)
	}

	rel, err := release.Load(dir)
	if err != nil {
		t.Fatalf("the stamped bundle does not load: %v", err)
	}
	if rel.Version().String() != "1.4.1-dev.7.gabc1234" {
		t.Errorf("manifest version = %s", rel.Version())
	}

	raw, err := os.ReadFile(filepath.Join(dir, release.VersionFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != "1.4.1-dev.7.gabc1234" {
		t.Errorf("VERSION = %q", raw)
	}
}

// TestStampingKeepsTheManifestAVendorWrote.
//
// A round trip through the YAML marshaller would discard every comment and
// reorder the keys to struct order, turning a hand-written manifest into
// machine output on a command that was asked to change one field.
func TestStampingKeepsTheManifestAVendorWrote(t *testing.T) {
	dir := bundle(t, nil)
	manifest := filepath.Join(dir, release.ManifestFileName)

	before, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := release.Stamp(dir, mustVersion(t, "9.9.9")); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(after), "# Example release bundle exercised by the test suite.") {
		t.Error("stamping dropped the manifest's leading comment")
	}
	// Exactly one line differs, and it is the version.
	beforeLines, afterLines := strings.Split(string(before), "\n"), strings.Split(string(after), "\n")
	if len(beforeLines) != len(afterLines) {
		t.Fatalf("stamping changed the line count, %d -> %d", len(beforeLines), len(afterLines))
	}
	var changed []int
	for i := range beforeLines {
		if beforeLines[i] != afterLines[i] {
			changed = append(changed, i)
		}
	}
	if len(changed) != 1 {
		t.Fatalf("stamping changed %d lines, want 1", len(changed))
	}
	if !strings.Contains(afterLines[changed[0]], "version: 9.9.9") {
		t.Errorf("the changed line is %q", afterLines[changed[0]])
	}
}

// TestStampingDoesNotTouchAnotherVersionKey.
//
// The example manifest declares `providers.runtime: {name: compose, version:
// ">=2.30"}`. A stamper that rewrote the first `version:` it found would
// replace a provider constraint with a release version -- producing a bundle
// that loads, verifies, and refuses to run on every Docker in existence.
func TestStampingDoesNotTouchAnotherVersionKey(t *testing.T) {
	dir := bundle(t, nil)

	if err := release.Stamp(dir, mustVersion(t, "9.9.9")); err != nil {
		t.Fatal(err)
	}

	rel, err := release.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := rel.Manifest.Providers.Runtime.Version.String(); got != ">=2.30" {
		t.Errorf("providers.runtime.version = %q, want the constraint untouched", got)
	}
}

// TestStampingRefusesAManifestItCannotRewrite rather than guessing.
//
// A build that stamped the wrong key would produce a bundle that loads,
// verifies, and is not the version anybody asked for.
func TestStampingRefusesAManifestItCannotRewrite(t *testing.T) {
	dir := bundle(t, func(dir string) {
		manifest := filepath.Join(dir, release.ManifestFileName)
		data, err := os.ReadFile(manifest)
		if err != nil {
			t.Fatal(err)
		}
		// Flow style: legal YAML, and not a line this can rewrite.
		flow := strings.Replace(string(data),
			"metadata:\n  name: demo\n  version: 1.2.0",
			"metadata: {name: demo, version: 1.2.0}", 1)
		if flow == string(data) {
			t.Fatal("the fixture's metadata block no longer has the shape this rewrites")
		}
		if err := os.WriteFile(manifest, []byte(flow), 0o644); err != nil {
			t.Fatal(err)
		}
	})

	err := release.Stamp(dir, mustVersion(t, "9.9.9"))
	if err == nil {
		t.Fatal("a manifest whose version cannot be located must be refused")
	}
	if !strings.Contains(err.Error(), "metadata.version") {
		t.Errorf("the refusal should name what it looked for: %v", err)
	}
}
