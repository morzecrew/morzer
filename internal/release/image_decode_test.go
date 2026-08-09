package release_test

import (
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/release"
)

// Decode tests go through YAML rather than through the struct, deliberately.
//
// The scalar suite's gap that let an unquoted `mode` decode to the wrong
// permission for months is recent enough to be worth not repeating: a type with
// two spellings is exactly where a test that constructs the Go value proves
// nothing about the file a vendor writes.

func TestImagesDecodeInBothSpellings(t *testing.T) {
	const digest = "@sha256:0000000000000000000000000000000000000000000000000000000000000001"

	manifest := `api_version: selfhost/v1alpha1
kind: application-release
metadata: {name: demo, version: 1.0.0}
runtime: {project: demo, files: [compose/compose.yaml]}
images:
  db: registry.example/demo/db` + digest + `
  app:
    ref: registry.example/demo/app` + digest + `
    from: bundle
  api:
    ref: registry.example/demo/api` + digest + `
    from: registry
  bare:
    ref: registry.example/demo/bare` + digest + `
`

	m, err := release.ParseManifest([]byte(manifest), "manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct {
		source  domain.ImageSource
		bundled bool
	}{
		// The scalar spelling: most images are never bundled, and making
		// each of them carry a `from:` to say so is noise.
		"db":   {domain.ImageFromRegistry, false},
		"app":  {domain.ImageFromBundle, true},
		"api":  {domain.ImageFromRegistry, false},
		"bare": {domain.ImageFromRegistry, false},
	}
	for name, want := range cases {
		spec, ok := m.Images[name]
		if !ok {
			t.Fatalf("%s did not decode at all", name)
		}
		if spec.Source() != want.source {
			t.Errorf("%s source = %q, want %q", name, spec.Source(), want.source)
		}
		if spec.Bundled() != want.bundled {
			t.Errorf("%s bundled = %t, want %t", name, spec.Bundled(), want.bundled)
		}
		if !strings.Contains(spec.Ref, "@sha256:") {
			t.Errorf("%s ref = %q, which is not the pinned reference", name, spec.Ref)
		}
	}

	// "the vendor said registry" and "the vendor said nothing" stay
	// distinguishable, which is what keeps `release show --json` from
	// announcing a field the manifest never declared.
	if m.Images["bare"].From != "" {
		t.Errorf("an unstated source was materialised as %q", m.Images["bare"].From)
	}
	if m.Images["api"].From != domain.ImageFromRegistry {
		t.Errorf("an explicit source was lost: %q", m.Images["api"].From)
	}

	if got := m.BundledImages(); len(got) != 1 || got[0] != "app" {
		t.Errorf("BundledImages() = %v, want [app]", got)
	}
	// The pulled set is what registry-reachability checks ask about: a
	// release that bundles everything must not warn about a registry it
	// never contacts.
	if got := m.PulledImageRefs(); len(got) != 3 {
		t.Errorf("PulledImageRefs() has %d entries, want 3: %v", len(got), got)
	}
}

// TestAnUnknownImageSourceIsRefused rather than defaulted.
//
// Both plausible typos -- `bundled`, and `from` under the wrong image -- fail
// towards a release the vendor believes ships its own bytes and does not, which
// surfaces as a credential failure on a customer's machine.
func TestAnUnknownImageSourceIsRefused(t *testing.T) {
	manifest := `api_version: selfhost/v1alpha1
kind: application-release
metadata: {name: demo, version: 1.0.0}
runtime: {project: demo, files: [compose/compose.yaml]}
images:
  app:
    ref: registry.example/demo/app@sha256:0000000000000000000000000000000000000000000000000000000000000001
    from: bundled
`
	_, err := release.ParseManifest([]byte(manifest), "manifest.yaml")
	if err == nil {
		t.Fatal("an unknown image source was accepted")
	}
	if !strings.Contains(err.Error(), "images.app.from") {
		t.Errorf("the refusal does not name the field: %v", err)
	}
}

// TestAnImageMappingWithNoRefIsRefused, by the pinning rule that already
// exists -- an absent reference is not a pinned one.
func TestAnImageMappingWithNoRefIsRefused(t *testing.T) {
	manifest := `api_version: selfhost/v1alpha1
kind: application-release
metadata: {name: demo, version: 1.0.0}
runtime: {project: demo, files: [compose/compose.yaml]}
images:
  app:
    from: bundle
`
	_, err := release.ParseManifest([]byte(manifest), "manifest.yaml")
	if err == nil {
		t.Fatal("an image with no reference was accepted")
	}
	if !strings.Contains(err.Error(), "pinned by digest") {
		t.Errorf("the refusal does not name the rule: %v", err)
	}
}

// TestAnUnknownKeyInAnImageMappingIsRefused.
//
// Strict decoding is recursive, and this type has its own UnmarshalYAML --
// which is exactly where strictness is most easily dropped, because the method
// decodes into a struct of its own.
func TestAnUnknownKeyInAnImageMappingIsRefused(t *testing.T) {
	manifest := `api_version: selfhost/v1alpha1
kind: application-release
metadata: {name: demo, version: 1.0.0}
runtime: {project: demo, files: [compose/compose.yaml]}
images:
  app:
    ref: registry.example/demo/app@sha256:0000000000000000000000000000000000000000000000000000000000000001
    source: bundle
`
	_, err := release.ParseManifest([]byte(manifest), "manifest.yaml")
	if err == nil {
		t.Fatal("an unknown key inside an image mapping was accepted, so a typo " +
			"in `from` silently leaves the image pulled")
	}
	// And the decoder's own diagnostic survives, rather than being replaced
	// by a generic sentence about the two spellings. This type decodes
	// through its own UnmarshalYAML, which is exactly where a position is
	// most easily thrown away -- on the mistake a vendor is most likely to
	// make in a field that has two shapes.
	if !strings.Contains(err.Error(), `unknown field "source"`) {
		t.Errorf("the refusal does not name the unknown key: %v", err)
	}
	if !strings.Contains(err.Error(), "[8:5]") {
		t.Errorf("the refusal lost the line and column: %v", err)
	}
}
