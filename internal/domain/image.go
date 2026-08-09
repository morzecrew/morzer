package domain

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// ImageSource is where an image's bytes come from.
//
// Not where its *identity* comes from: the reference is the identity in both
// cases, and it is pinned by digest either way. This says only how the bytes
// arrive on the machine.
type ImageSource string

const (
	// ImageFromRegistry is the default: the runtime pulls it, with
	// whatever ambient credentials the host has.
	ImageFromRegistry ImageSource = "registry"

	// ImageFromBundle means the image travels inside the bundle, as an OCI
	// layout under images/, covered by the same SHA256SUMS and signature as
	// every other file.
	//
	// It exists because a vendor shipping a private image otherwise has to
	// hand the customer credentials for the registry it lives in, and cloud
	// registries frequently cannot express "this one customer may read
	// these three repositories".
	ImageFromBundle ImageSource = "bundle"
)

// ImageSources is every legal value, for validation and the generated schema.
var ImageSources = []ImageSource{ImageFromRegistry, ImageFromBundle}

// ImageSpec is one entry in a manifest's `images` map.
//
// It decodes from either spelling:
//
//	images:
//	  db: postgres@sha256:…                      # pulled
//	  app:
//	    ref: registry.example/demo/app@sha256:…
//	    from: bundle                             # travels in the bundle
//
// Both, because most images will never be bundled and making every one of them
// carry a `from:` key to say so is noise in the file a vendor reads most. The
// dual shape is not an invention either -- PortSpec already accepts an integer
// or a string, so the pattern and its tests exist.
type ImageSpec struct {
	// Ref is the image reference, pinned by digest. It is interpolated into
	// Compose as <PRODUCT>_IMAGE_<NAME>, so it must stay something the
	// daemon can resolve -- which is why `from` is a separate field rather
	// than a scheme on the reference the way transports spell it.
	Ref string `yaml:"ref" json:"ref"`

	// From is where the bytes come from. Empty means registry; see Source.
	From ImageSource `yaml:"from" json:"from,omitempty"`
}

// Source is From with the default applied.
//
// A method rather than a value written by ApplyDefaults, so "the vendor said
// registry" and "the vendor said nothing" stay distinguishable in the file and
// in `release show --json`, which would otherwise announce a field the manifest
// never declared.
func (s ImageSpec) Source() ImageSource {
	if s.From == "" {
		return ImageFromRegistry
	}
	return s.From
}

// Bundled reports whether this image travels inside the bundle.
func (s ImageSpec) Bundled() bool { return s.Source() == ImageFromBundle }

// UnmarshalYAML accepts the scalar and the mapping spelling.
func (s *ImageSpec) UnmarshalYAML(unmarshal func(any) error) error {
	var ref string
	if err := unmarshal(&ref); err == nil {
		*s = ImageSpec{Ref: ref}
		return nil
	}

	// A distinct type for the mapping arm, or unmarshal would recurse
	// through this method for ever.
	var mapping struct {
		Ref  string      `yaml:"ref"`
		From ImageSource `yaml:"from"`
	}
	if err := unmarshal(&mapping); err != nil {
		// The decoder's own error, not a replacement for it. Strict
		// decoding reaches inside this method, so a typo in `from`
		// arrives here as `unknown field "source"` with a line and a
		// column -- which is the whole reason this project decodes with
		// goccy. Substituting a generic sentence would throw away the
		// position and name nothing, on exactly the mistake a vendor is
		// most likely to make in a field with two spellings.
		return err
	}
	*s = ImageSpec{Ref: mapping.Ref, From: mapping.From}
	return nil
}

// MarshalJSON always writes the mapping form.
//
// One shape on the way out, whichever came in: the JSON envelope is read by
// scripts, and a field that is sometimes a string and sometimes an object is a
// field every consumer has to branch on.
func (s ImageSpec) MarshalJSON() ([]byte, error) {
	type alias ImageSpec
	return json.Marshal(alias(s))
}

func (s *ImageSpec) UnmarshalJSON(b []byte) error {
	var ref string
	if err := json.Unmarshal(b, &ref); err == nil {
		*s = ImageSpec{Ref: ref}
		return nil
	}
	type alias ImageSpec
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*s = ImageSpec(a)
	return nil
}

// BundledImages returns the images that travel inside the bundle, by name, in
// a stable order.
func (m *Manifest) BundledImages() []string {
	var names []string
	for name, spec := range m.Images {
		if spec.Bundled() {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// PulledImageRefs returns the references that will actually be fetched from a
// registry, in a stable order.
//
// Used by the checks that ask about registry reachability: a release that
// bundles everything should not warn about a registry it never contacts.
func (m *Manifest) PulledImageRefs() []string {
	var refs []string
	for _, name := range sortedImageNames(m.Images) {
		if !m.Images[name].Bundled() {
			refs = append(refs, m.Images[name].Ref)
		}
	}
	return refs
}

func sortedImageNames(images map[string]ImageSpec) []string {
	names := make([]string, 0, len(images))
	for name := range images {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func joinImageSources(vs []ImageSource) string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = strconv.Quote(string(v))
	}
	return strings.Join(out, ", ")
}
