package release

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/morzecrew/morzer/internal/domain"
)

// ImagesDirName is where a bundle carries the images it ships itself.
//
// One definition, used by two things that must agree: the archive writer ranks
// this directory last so the large content is extracted under a budget that is
// by then known, and the verification below reads the layout inside it.
const ImagesDirName = "images"

// ImageIndexFileName is the OCI layout's index, which names every image in it.
const ImageIndexFileName = "index.json"

// ImageLayoutMarkerFileName is the file that marks a directory as an OCI image
// layout. Its presence is what distinguishes a layout from a directory that
// happens to be called images/.
const ImageLayoutMarkerFileName = "oci-layout"

// ImageLayoutDigests reads the manifest digests an OCI layout carries.
//
// Only the digests: this reads the layout to answer "which images are in here",
// not to interpret them. Writing a layout needs a registry client; reading this
// much of one needs a JSON decode, and keeping the two apart is what lets the
// verification path stay free of a registry dependency it would only use to
// parse a file it can already read.
func ImageLayoutDigests(dir string) ([]string, error) {
	path := filepath.Join(dir, ImagesDirName, ImageIndexFileName)

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, domain.ValidationError(domain.ErrNotFound,
				"the bundle declares images it carries but has no %s/%s",
				ImagesDirName, ImageIndexFileName).
				WithHint("images marked `from: bundle` travel as an OCI layout under " +
					"images/; run `morzer release pack` to write one")
		}
		return nil, domain.ValidationError(err, "cannot read %s", path)
	}

	var index ocispec.Index
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, domain.ValidationError(err,
			"%s/%s is not a valid OCI image index", ImagesDirName, ImageIndexFileName)
	}

	digests := make([]string, 0, len(index.Manifests))
	for _, m := range index.Manifests {
		digests = append(digests, m.Digest.String())
	}
	sort.Strings(digests)
	return digests, nil
}

// checkBundledImages enforces that the manifest and the layout describe the
// same set of images, in both directions.
//
// Both directions, because each catches a different mistake. A manifest naming
// an image the layout does not carry is a release that will fail to install on
// a machine with no registry access -- the one case the whole feature exists
// for. A layout carrying an image no manifest names is a bundle whose contents
// nobody stated: extra bytes, signed, shipped, and unexplained.
//
// This is the completeness rule SHA256SUMS already holds over files, one level
// up: a bundle must be exactly what it says it is.
func checkBundledImages(rel domain.Release) error {
	bundled := rel.Manifest.BundledImages()
	layoutPresent, err := hasImageLayout(rel.Root)
	if err != nil {
		return err
	}

	switch {
	case len(bundled) == 0 && !layoutPresent:
		return nil

	case len(bundled) > 0 && !layoutPresent:
		// Named, not merely counted. The vendor's next move is to pack
		// these images or to unmark them, and both need the list -- a
		// message about a missing index.json says which file is absent
		// without saying which decision produced the expectation.
		return domain.ValidationError(domain.ErrNotFound,
			"the manifest marks %s `from: bundle`, but the bundle carries no %s/ layout",
			strings.Join(quoteAll(bundled), ", "), ImagesDirName).
			WithHint("run `morzer release pack` to copy them in, or drop " +
				"`from: bundle` to keep pulling them")

	case len(bundled) == 0 && layoutPresent:
		// A layout with nothing declaring it. Refused rather than
		// ignored: the files are covered by the signature and shipped
		// to a customer, so "the manifest says nothing about them" is
		// the problem rather than the excuse.
		return domain.ValidationError(domain.ErrValidation,
			"the bundle carries an %s/ layout but no image is marked `from: bundle`",
			ImagesDirName).
			WithHint("mark the images it carries, or remove the layout")
	}

	digests, err := ImageLayoutDigests(rel.Root)
	if err != nil {
		return err
	}
	inLayout := make(map[string]bool, len(digests))
	for _, d := range digests {
		inLayout[d] = true
	}

	var problems []string
	declared := make(map[string]bool, len(bundled))
	for _, name := range bundled {
		digest, ok := rel.Manifest.Images[name].Digest()
		if !ok {
			// Unreachable through Load: the pinning rule refuses an
			// unpinned reference first. Stated rather than assumed,
			// because this function is also reachable from `pack`.
			problems = append(problems,
				"images."+name+": the reference is not pinned by digest")
			continue
		}
		declared[digest] = true
		if !inLayout[digest] {
			problems = append(problems, "images."+name+
				": marked `from: bundle` but "+ImagesDirName+"/"+ImageIndexFileName+
				" does not carry "+digest)
		}
	}
	for _, digest := range digests {
		if !declared[digest] {
			problems = append(problems, ImagesDirName+"/"+ImageIndexFileName+
				": carries "+digest+", which no image in the manifest names")
		}
	}

	if len(problems) > 0 {
		return domain.ValidationError(domain.ErrValidation,
			"the bundle's images do not match what the manifest declares:\n  - %s",
			strings.Join(problems, "\n  - ")).
			WithHint("every image marked `from: bundle` must appear in the layout by " +
				"the digest the manifest pins, and the layout must carry nothing else")
	}
	return nil
}

// quoteAll quotes each name, so a list of one reads as a name rather than as
// prose that happens to contain a word.
func quoteAll(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = strconv.Quote(n)
	}
	return out
}

// hasImageLayout reports whether the bundle carries an OCI layout.
//
// Keyed on the `oci-layout` marker rather than on the directory existing: a
// bundle may legitimately ship an `images/` directory of its own -- icons, say
// -- and refusing that would be the manager claiming a directory name it never
// reserved.
func hasImageLayout(root string) (bool, error) {
	marker := filepath.Join(root, ImagesDirName, ImageLayoutMarkerFileName)
	switch _, err := os.Stat(marker); {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, domain.ValidationError(err, "cannot read %s", marker)
	}
}
