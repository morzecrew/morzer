package release_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/adapters/verify/checksum"
	"github.com/morzecrew/morzer/internal/release"
)

// A bundle must be exactly what it says it is, in both directions. Each
// direction catches a different mistake, and only one of them is obvious.

// TestABundledImageMissingFromTheLayoutIsRefused.
//
// The obvious direction: a release that promises to carry its own image and
// does not will fail to install on a machine with no registry access, which is
// the one case the whole feature exists for -- and it fails there, after the
// customer has taken the bundle offline.
func TestABundledImageMissingFromTheLayoutIsRefused(t *testing.T) {
	dir := bundleWithLayout(t, nil)

	_, err := release.Load(dir)
	if err == nil {
		t.Fatal("a bundle promising an image it does not carry was accepted")
	}
	// The vendor's next move is to pack the image or to unmark it, and
	// both need to know which image -- a message about a missing index.json
	// says which file is absent without saying which decision expected it.
	if !strings.Contains(err.Error(), `"app"`) {
		t.Errorf("the refusal does not name the image: %v", err)
	}
	if !strings.Contains(err.Error(), "from: bundle") {
		t.Errorf("the refusal does not name the declaration that expected it: %v", err)
	}
}

// TestALayoutThatOmitsOneDeclaredImageIsRefused.
//
// The likely real failure, and distinct from having no layout at all: a pack
// that copied three images of four leaves a layout that exists, looks right,
// and is missing the one image the install will need. Found by a sabotage that
// survived -- the no-layout test above never reaches this comparison.
func TestALayoutThatOmitsOneDeclaredImageIsRefused(t *testing.T) {
	dir := bundleWithLayout(t, []string{"sha256:" + strings.Repeat("c", 64)})

	_, err := release.Load(dir)
	if err == nil {
		t.Fatal("a layout missing a declared image was accepted")
	}
	if !strings.Contains(err.Error(), "images.app") {
		t.Errorf("the refusal does not name the image: %v", err)
	}
	if !strings.Contains(err.Error(), appDigest) {
		t.Errorf("the refusal does not name the digest that is missing: %v", err)
	}
}

// TestALayoutEntryNoManifestNamesIsRefused.
//
// The less obvious direction, and the reason it matters: those bytes are
// covered by the signature and shipped to a customer, so "the manifest says
// nothing about them" is the problem rather than the excuse.
func TestALayoutEntryNoManifestNamesIsRefused(t *testing.T) {
	dir := bundleWithLayout(t, []string{
		appDigest, "sha256:" + strings.Repeat("b", 64),
	})

	_, err := release.Load(dir)
	if err == nil {
		t.Fatal("a layout carrying an undeclared image was accepted")
	}
	if !strings.Contains(err.Error(), "no image in the manifest names") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}
}

// TestAMatchingLayoutLoads is the positive half, without which every refusal
// above could be satisfied by refusing everything.
func TestAMatchingLayoutLoads(t *testing.T) {
	dir := bundleWithLayout(t, []string{appDigest})

	rel, err := release.Load(dir)
	if err != nil {
		t.Fatalf("a bundle whose layout matches its manifest was refused: %v", err)
	}
	if got := rel.Manifest.BundledImages(); len(got) != 1 || got[0] != "app" {
		t.Errorf("BundledImages() = %v, want [app]", got)
	}
}

// TestALayoutNobodyDeclaredIsRefused.
//
// A layout with no image marked `from: bundle` is bytes in the artefact that
// nothing explains -- signed, shipped, and unaccounted for.
func TestALayoutNobodyDeclaredIsRefused(t *testing.T) {
	dir := bundle(t, nil)
	writeLayout(t, dir, []string{appDigest})
	sumTree(t, dir)

	_, err := release.Load(dir)
	if err == nil {
		t.Fatal("an undeclared image layout was accepted")
	}
	if !strings.Contains(err.Error(), "no image is marked") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}
}

// TestAnImagesDirectoryThatIsNotALayoutIsLeftAlone.
//
// `images/` is not a name this project reserved. A bundle shipping icons there
// is doing nothing wrong, and refusing it would be the manager claiming a
// directory it never asked for.
func TestAnImagesDirectoryThatIsNotALayoutIsLeftAlone(t *testing.T) {
	dir := bundle(t, nil)
	if err := os.MkdirAll(filepath.Join(dir, "images"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "images", "logo.svg"), []byte("<svg/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	sumTree(t, dir)

	if _, err := release.Load(dir); err != nil {
		t.Fatalf("a bundle with an ordinary images/ directory was refused: %v", err)
	}
}

// TestAnIndexNamingABlobTheBundleDoesNotCarryIsRefused.
//
// Without it a bundle passes `verify` and fails at install, on the machine with
// no registry to fall back to — which is the one case the whole feature exists
// for.
func TestAnIndexNamingABlobTheBundleDoesNotCarryIsRefused(t *testing.T) {
	dir := bundleWithLayout(t, []string{appDigest})

	blob := filepath.Join(dir, "images", "blobs", "sha256",
		strings.TrimPrefix(appDigest, "sha256:"))
	if err := os.Remove(blob); err != nil {
		t.Fatal(err)
	}
	sumTree(t, dir)

	_, err := release.Load(dir)
	if err == nil {
		t.Fatal("a layout whose index outruns its blobs was accepted")
	}
	if !strings.Contains(err.Error(), "does not carry it") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
}

// TestAnIndexEntryThatIsNotADigestIsRefused, rather than compared as a string.
func TestAnIndexEntryThatIsNotADigestIsRefused(t *testing.T) {
	dir := bundleWithLayout(t, []string{appDigest})

	index := filepath.Join(dir, "images", "index.json")
	if err := os.WriteFile(index, []byte(
		`{"schemaVersion":2,"manifests":[{"mediaType":"x","digest":"not-a-digest","size":2}]}`,
	), 0o644); err != nil {
		t.Fatal(err)
	}
	sumTree(t, dir)

	_, err := release.Load(dir)
	if err == nil {
		t.Fatal("an index entry that is not a digest was accepted")
	}
	if !strings.Contains(err.Error(), "not a digest") {
		t.Errorf("the refusal does not name the problem: %v", err)
	}
}

// TestAMalformedIndexIsRefusedRatherThanIgnored.
func TestAMalformedIndexIsRefusedRatherThanIgnored(t *testing.T) {
	dir := bundleWithLayout(t, []string{appDigest})
	index := filepath.Join(dir, "images", "index.json")
	if err := os.WriteFile(index, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	sumTree(t, dir)

	_, err := release.Load(dir)
	if err == nil {
		t.Fatal("an unreadable image index was accepted")
	}
	if !strings.Contains(err.Error(), "OCI image index") {
		t.Errorf("the refusal does not name what could not be read: %v", err)
	}
}

// appDigest is the digest the fixture's bundled image is pinned to.
const appDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000001"

// bundleWithLayout marks the example bundle's `app` image as bundled and writes
// a layout carrying the given digests. A nil slice writes no layout at all.
func bundleWithLayout(t *testing.T, digests []string) string {
	t.Helper()

	dir := bundle(t, func(dir string) {
		manifest := filepath.Join(dir, release.ManifestFileName)
		data, err := os.ReadFile(manifest)
		if err != nil {
			t.Fatal(err)
		}
		// The fixture pins `app` to this digest already, so marking it
		// bundled is the only edit -- and the mapping spelling is what
		// is under test.
		old := "  app: registry.example/demo/app@" + appDigest + "\n"
		bundled := "  app:\n    ref: registry.example/demo/app@" + appDigest +
			"\n    from: bundle\n"
		if !strings.Contains(string(data), old) {
			t.Fatalf("the fixture no longer pins app to %s", appDigest)
		}
		if err := os.WriteFile(manifest,
			[]byte(strings.Replace(string(data), old, bundled, 1)), 0o644); err != nil {
			t.Fatal(err)
		}
	})

	if digests != nil {
		writeLayout(t, dir, digests)
	}
	sumTree(t, dir)
	return dir
}

// writeLayout writes a minimal OCI image layout naming the given digests.
//
// Minimal on purpose: what the verification reads is `index.json`'s digests and
// the `oci-layout` marker, so a fixture carrying real blobs would be asserting
// something this code does not look at.
func writeLayout(t *testing.T, dir string, digests []string) {
	t.Helper()

	images := filepath.Join(dir, "images")
	if err := os.MkdirAll(filepath.Join(images, "blobs", "sha256"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(images, "oci-layout"),
		[]byte(`{"imageLayoutVersion":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	type descriptor struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
	}
	index := struct {
		SchemaVersion int          `json:"schemaVersion"`
		Manifests     []descriptor `json:"manifests"`
	}{SchemaVersion: 2}
	for _, d := range digests {
		index.Manifests = append(index.Manifests, descriptor{
			MediaType: "application/vnd.oci.image.manifest.v1+json",
			Digest:    d,
			Size:      2,
		})
		// A blob per entry, so the tree looks like a layout rather than
		// an index pointing at nothing.
		blob := filepath.Join(images, "blobs", "sha256", strings.TrimPrefix(d, "sha256:"))
		if err := os.WriteFile(blob, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	data, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(images, "index.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// sumTree writes a checksum list over the fixture, so the bundle stays one the
// verifier accepts for reasons other than the one under test.
func sumTree(t *testing.T, dir string) {
	t.Helper()
	if err := release.WriteSums(dir); err != nil {
		t.Fatal(err)
	}
}

// TestImageBlobsAreCoveredByTheChecksumList.
//
// No new verification mechanism: image blobs are ordinary files, so the rule
// that an unlisted file fails closed already covers them. Asserted because
// "already covered" is the kind of claim that stops being true when someone
// adds an exclusion for a large directory.
func TestImageBlobsAreCoveredByTheChecksumList(t *testing.T) {
	dir := bundleWithLayout(t, []string{appDigest})

	data, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	blob := "images/blobs/sha256/" + strings.TrimPrefix(appDigest, "sha256:")
	for _, want := range []string{"images/index.json", "images/oci-layout", blob} {
		if !strings.Contains(string(data), want) {
			t.Errorf("%s is not listed in SHA256SUMS", want)
		}
	}

	// A tampered blob fails, which is the property the listing exists for
	// rather than the listing itself.
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(blob)),
		[]byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checksum.VerifySumsFile(dir); err == nil {
		t.Error("a tampered image blob passed verification")
	}
}

// TestAnUnlistedImageBlobFailsClosed.
//
// The other half, and the one an attacker actually reaches for: adding a file
// rather than editing one. The signature still covers SHA256SUMS and verifies,
// every listed file still matches -- and the added blob is the payload. The
// completeness rule is what refuses it, and image blobs are exactly the files
// most likely to be excused from it one day for being large.
func TestAnUnlistedImageBlobFailsClosed(t *testing.T) {
	dir := bundleWithLayout(t, []string{appDigest})

	added := filepath.Join(dir, "images", "blobs", "sha256", strings.Repeat("e", 64))
	if err := os.WriteFile(added, []byte("added after signing"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := checksum.VerifySumsFile(dir)
	if err == nil {
		t.Fatal("a blob the checksum list does not cover passed verification")
	}
	if !strings.Contains(err.Error(), "not listed") {
		t.Errorf("the refusal does not name the rule: %v", err)
	}
}
