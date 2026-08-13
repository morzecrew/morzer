package domain

import (
	"encoding/json"
	"regexp"
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

// imageName is the permitted shape of an image's key in the manifest.
//
// The same rule parameters already carry, and for the same reason: the key
// becomes the tail of `<PRODUCT>_IMAGE_<NAME>`, which a Compose file
// interpolates. Without it the key was unconstrained while the variable name
// was normalised -- upper-cased with `-` and `.` folded to `_` -- so `web-ui`
// and `web.ui` produced the same variable, one pinned reference silently
// overwrote the other, and which one survived depended on Go's randomised map
// iteration. A release whose image is chosen by map order is a release that
// runs different bytes on different days.
//
// Hyphens are allowed and dots are not, rather than the reverse: `web-ui` is
// the spelling image names actually use, and permitting exactly one of the two
// makes the normalisation injective again.
var imageName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

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

// Digest is the digest this reference pins, and false when it pins none.
//
// One definition, because three appeared: the packer compared it against what a
// registry served, the layout check compared it against what index.json
// carried, and the OCI source split it to address a repository. Two of those
// cut at the first "@" and one at the last -- indistinguishable for every legal
// reference and not the kind of agreement worth leaving to coincidence.
//
// Cut at the last "@": a repository name may not contain one, so any earlier
// occurrence is part of something malformed, and taking the last leaves the
// digest whole for the parse that follows.
//
// Unobservable through a validated manifest -- `digestRef` refuses any "@"
// before the digest, so first and last are the same for everything that reaches
// here. This is exported on a domain type and callable from anywhere, so the
// choice is pinned by its own test rather than by whoever happens to call it.
func (s ImageSpec) Digest() (string, bool) {
	at := strings.LastIndex(s.Ref, "@")
	if at <= 0 || at == len(s.Ref)-1 {
		return "", false
	}
	return s.Ref[at+1:], true
}

// localAliasPrefix marks a tag as one the manager made rather than one a
// vendor published.
//
// It is a tag and not a digest reference because the daemon offers nothing
// else. Measured on Docker 29.6.2: `docker tag` answers "refusing to create a
// tag with a digest reference", and a digest reference naming a repository the
// daemon never pulled from is absent from its reference store, so
// `docker image inspect registry.example/demo/app@sha256:...` reports no such
// image however the bytes arrived. RFC 0011 decision 19 is that measurement.
const localAliasPrefix = "morzer-"

// LocalAlias is the name a bundled image answers to on this machine.
//
// `<repo>:morzer-sha256-<hex>`, and every part of that is load-bearing. The
// repository is the vendor's, so an operator reading `docker images` sees
// which product the image belongs to. The digest is the one the manifest pins,
// so the alias is *derived* rather than invented: identical on every apply,
// which is what keeps the rendered configuration -- and therefore `diff` and
// `status` -- from reporting a change on every run. And the prefix says who
// made it, because it is a tag the vendor never published and an operator will
// eventually find it and wonder.
//
// False when the reference pins no digest, which a validated manifest cannot
// produce: the pinning rule refuses an unpinned reference first. Stated
// anyway, because this is exported on a domain type and reachable from
// anywhere.
//
// The identity is unaffected. Ref stays what the manifest pinned, what the
// signature covers, and what `release show` reports; this is only the name the
// daemon can be made to answer to.
func (s ImageSpec) LocalAlias() (string, bool) {
	digest, ok := s.Digest()
	if !ok {
		return "", false
	}
	// "sha256:abc" becomes "sha256-abc": a tag may not contain a colon,
	// which is the character separating it from the repository.
	return RepositoryOf(s.Ref) + ":" +
		localAliasPrefix + strings.ReplaceAll(digest, ":", "-"), true
}

// RepositoryOf is a reference with its digest and its tag removed.
//
// One definition, for the same reason Digest and ShortImageRef have one: the
// alias builder needs it to append a tag, and the ingest needs it to address
// the loopback server, and two implementations of "where does the tag end"
// agree right up until one of them is edited.
//
// The subtlety both need is the same. A reference may carry a tag *and* a
// digest -- `postgres:17@sha256:…` is legal and the pinning rule permits it --
// while a colon may equally be a registry's port. The tag is whatever follows
// a colon in the last path segment; a colon before the last slash is a port.
func RepositoryOf(ref string) string {
	repo := ref
	if at := strings.LastIndex(repo, "@"); at > 0 {
		repo = repo[:at]
	}

	segment := repo
	if slash := strings.LastIndex(repo, "/"); slash >= 0 {
		segment = repo[slash+1:]
	}
	if colon := strings.LastIndex(segment, ":"); colon >= 0 {
		repo = repo[:len(repo)-len(segment)+colon]
	}
	return repo
}

// RuntimeRef is the reference the runtime is handed for this image.
//
// The alias for a bundled image, Ref for every other -- and the distinction
// exists because the daemon cannot be made to resolve a bundled image by the
// reference its manifest pins. Compose has to receive a name that resolves, so
// for a bundled image this is the only candidate there is.
//
// Falls back to Ref when no alias can be built, which means the reference pins
// no digest and the manifest would not have validated. Falling back rather
// than returning an error keeps the Compose environment builder total; what
// the deployment then does with an unpinned reference is what it did before
// bundling existed.
func (s ImageSpec) RuntimeRef() string {
	if !s.Bundled() {
		return s.Ref
	}
	if alias, ok := s.LocalAlias(); ok {
		return alias
	}
	return s.Ref
}

// ShortImageRef is a reference with its digest dropped, for a message.
//
// A digest is 71 characters, and every message that carries one already names
// the image beside it -- so the digest is the part a reader skips to find the
// part that identifies anything. Three copies of this existed, one per layer,
// which is the arithmetic ImageSpec.Digest already ran once: they agreed by
// coincidence rather than by construction, and the next one would have been
// written by whoever needed it next.
//
// Cut at the first "@" rather than the last, unlike Digest, and deliberately:
// this answers "what should a human read", so for a malformed reference the
// least of it is the safest thing to print.
func ShortImageRef(ref string) string {
	if i := strings.Index(ref, "@"); i > 0 {
		return ref[:i]
	}
	return ref
}

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

// BundledImageRefs is the same set as BundledImages, by reference.
//
// The manifest's references, not the aliases: this is what ingest is asked to
// make present, and it is the identity the layout is addressed by.
func (m *Manifest) BundledImageRefs() []string {
	return m.imageRefs(func(spec ImageSpec) bool { return spec.Bundled() })
}

// PulledImageRefs is what a deployment fetches from a registry.
//
// Distinct from ImageRefs, which is what the release *consists of*: passing a
// bundled image to Pull would send the deployment to the vendor's registry for
// bytes the bundle already carries -- the one contact bundling exists to
// remove -- and it would ask for it under a reference that no longer resolves
// locally, so the pull would fail rather than merely be redundant.
func (m *Manifest) PulledImageRefs() []string {
	return m.imageRefs(func(spec ImageSpec) bool { return !spec.Bundled() })
}

// RuntimeImageRefs is what the daemon must be able to resolve.
//
// The alias for a bundled image and the manifest's reference for every other,
// which is the set Compose is handed and therefore the set whose absence is a
// converge failure. Asking about ImageRefs instead would report every bundled
// image as missing on a machine where ingest had just succeeded.
func (m *Manifest) RuntimeImageRefs() []string {
	names := sortedImageNames(m.Images)
	refs := make([]string, 0, len(names))
	for _, n := range names {
		refs = append(refs, m.Images[n].RuntimeRef())
	}
	return refs
}

func (m *Manifest) imageRefs(keep func(ImageSpec) bool) []string {
	var refs []string
	for _, n := range sortedImageNames(m.Images) {
		if spec := m.Images[n]; keep(spec) {
			refs = append(refs, spec.Ref)
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

// DigestFromRef recovers the content digest a reference names, in either of the
// two spellings this manager produces.
//
// `repo@sha256:<hex>` is the pinned form a manifest carries. `repo:morzer-sha256-<hex>`
// is the local alias the manager tags a bundled image with, because a daemon
// cannot resolve a digest reference for a repository it never pulled from --
// and the alias carries the digest precisely so the identity survives the
// rewriting.
//
// Empty when the reference names neither, which is an unpinned image: a
// manifest that never promised a digest, rather than evidence of one being
// swapped.
func DigestFromRef(ref string) string {
	if at := strings.LastIndex(ref, "@"); at > 0 && at < len(ref)-1 {
		return ref[at+1:]
	}

	colon := strings.LastIndex(ref, ":")
	if colon < 0 || colon == len(ref)-1 {
		return ""
	}
	tag := ref[colon+1:]
	if !strings.HasPrefix(tag, localAliasPrefix) {
		return ""
	}
	// `morzer-sha256-<hex>` back to `sha256:<hex>`.
	rest := strings.TrimPrefix(tag, localAliasPrefix)
	dash := strings.Index(rest, "-")
	if dash <= 0 || dash == len(rest)-1 {
		return ""
	}
	return rest[:dash] + ":" + rest[dash+1:]
}
