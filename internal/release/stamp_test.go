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

// TestStampingSurvivesACommentAtColumnZero.
//
// Found by audit, not by use: an earlier version treated any line starting in
// column zero as the end of the block it was scanning, so a vendor commenting
// between `name:` and `version:` at the left margin -- legal YAML, and a
// perfectly ordinary thing to write -- made stamping refuse a manifest it
// should have rewritten.
func TestStampingSurvivesACommentAtColumnZero(t *testing.T) {
	dir := bundle(t, func(dir string) {
		manifest := filepath.Join(dir, release.ManifestFileName)
		data, err := os.ReadFile(manifest)
		if err != nil {
			t.Fatal(err)
		}
		annotated := strings.Replace(string(data),
			"  name: demo\n",
			"  name: demo\n# The product name is load-bearing; see the docs.\n", 1)
		if annotated == string(data) {
			t.Fatal("the fixture no longer has the shape this annotates")
		}
		if err := os.WriteFile(manifest, []byte(annotated), 0o644); err != nil {
			t.Fatal(err)
		}
	})

	if err := release.Stamp(dir, mustVersion(t, "9.9.9")); err != nil {
		t.Fatalf("a manifest with a column-zero comment was refused: %v", err)
	}
	rel, err := release.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version().String() != "9.9.9" {
		t.Errorf("version = %s", rel.Version())
	}
}

// TestStampingRefusesWhenTheRewriteWouldLandTwice.
//
// A line-based rewrite cannot see YAML structure, so a block scalar inside
// `metadata` whose content happens to be a `version:` line looks exactly like
// the key. What catches it is the duplicate detection, not the re-parse: a
// manifest that has both is refused before anything is written, and one that
// has *only* the decoy has no real version at all, so the loader refuses it
// first for that. The re-parse behind them stays as defence in depth -- see
// Stamp -- and this test names the refusal that actually fires rather than
// accepting whichever came out.
func TestStampingRefusesWhenTheRewriteWouldLandTwice(t *testing.T) {
	dir := bundle(t, func(dir string) {
		manifest := filepath.Join(dir, release.ManifestFileName)
		data, err := os.ReadFile(manifest)
		if err != nil {
			t.Fatal(err)
		}
		// A description whose text contains a line that looks like the
		// key. Legal YAML, and a plausible thing for a vendor to write.
		trapped := strings.Replace(string(data),
			"  description: Demo self-hosted bundle used by the test suite\n",
			"  description: |\n    Demo bundle.\n    version: not-the-key\n", 1)
		if trapped == string(data) {
			t.Fatal("the fixture's description no longer has the shape this replaces")
		}
		if err := os.WriteFile(manifest, []byte(trapped), 0o644); err != nil {
			t.Fatal(err)
		}
	})

	err := release.Stamp(dir, mustVersion(t, "9.9.9"))
	if err == nil {
		t.Fatal("a rewrite that hit two lines was accepted")
	}
	if !strings.Contains(err.Error(), "twice") {
		t.Errorf("the refusal does not say two lines matched: %v", err)
	}

	// And nothing was written: the refusal has to come before the edit, or
	// a manifest is left with a version somewhere it does not belong.
	rel, loadErr := release.Load(dir)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if rel.Version().String() != "1.2.0" {
		t.Errorf("a refused stamp changed the manifest to %s", rel.Version())
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
