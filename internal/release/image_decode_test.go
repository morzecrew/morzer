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
runtimes: {compose: {options: {project: demo}, files: [compose/compose.yaml]}}
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
	// Every image is still in ImageRefs, bundled or not: until ingest
	// lands, a bundled image is fetched from its registry like any other,
	// because nothing else brings it onto the machine.
	if got := m.ImageRefs(); len(got) != 4 {
		t.Errorf("ImageRefs() has %d entries, want 4: %v", len(got), got)
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
runtimes: {compose: {options: {project: demo}, files: [compose/compose.yaml]}}
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
runtimes: {compose: {options: {project: demo}, files: [compose/compose.yaml]}}
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
runtimes: {compose: {options: {project: demo}, files: [compose/compose.yaml]}}
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

// TestADeclaredSizeThatCannotBeReadIsRefused.
//
// Absent means the default ceiling; unreadable would mean extracting under that
// ceiling on the strength of a declaration nobody could parse. The distinction
// matters because this is the one field that gates untrusted bytes, and it is
// read before the signature is checked.
func TestADeclaredSizeThatCannotBeReadIsRefused(t *testing.T) {
	// `12 GB` and `12GB` are legitimate decimal sizes and stay accepted;
	// what is refused is a value ByteSize cannot read at all.
	refused := []string{"twelve gigabytes", "-1GiB", "GiB", "12 gigs"}
	for _, raw := range refused {
		manifest := "api_version: selfhost/v1alpha1\nbundle:\n  uncompressed_size: " + raw + "\n"
		if _, err := release.DeclaredBundleSize([]byte(manifest)); err == nil {
			t.Errorf("%q was read as no declaration at all", raw)
		}
	}

	// Absent is still absent, and still the default ceiling.
	size, err := release.DeclaredBundleSize([]byte("api_version: selfhost/v1alpha1\n"))
	if err != nil || size != 0 {
		t.Errorf("an absent declaration gave (%d, %v), want (0, nil)", size, err)
	}

	// And YAML that does not parse is left to the strict decode, which
	// reports it with a position once the archive is on disk. Failing here
	// would replace a good diagnostic with a worse one.
	if _, err := release.DeclaredBundleSize([]byte("{not yaml")); err != nil {
		t.Errorf("unparseable YAML was refused here rather than by the decoder: %v", err)
	}

	// The ordinary case still works.
	size, err = release.DeclaredBundleSize(
		[]byte("bundle:\n  uncompressed_size: 12GiB\n"))
	if err != nil {
		t.Fatal(err)
	}
	if size != 12<<30 {
		t.Errorf("12GiB read as %d", size)
	}
}
