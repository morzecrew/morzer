package domain

import (
	"path/filepath"
	"strings"
)

// Release is an immutable version of the product: a manifest plus the
// directory holding its Compose files, templates and hooks.
//
// Identity is (name, version) *plus* Digest. The same version appearing with
// a different digest is an error, not a warning -- that is the whole basis on
// which rollback and reproducibility rest.
type Release struct {
	Manifest Manifest `json:"manifest"`

	// Root is the absolute path of the unpacked release directory.
	Root string `json:"root"`

	// Digest is the content digest of the bundle, "sha256:...".
	Digest string `json:"digest"`
}

func (r Release) Name() string     { return r.Manifest.Metadata.Name }
func (r Release) Version() Version { return r.Manifest.Metadata.Version }

func (r Release) String() string {
	return r.Name() + " " + r.Version().String()
}

// Path resolves a bundle-relative path against the release root. It refuses
// to produce anything outside the root: the manifest is release-supplied
// input, and a path escaping the root would let a bundle read or execute
// arbitrary host files.
//
// This is a defence in depth. Extraction and rendering additionally use
// os.Root so containment is enforced by the kernel, not by string inspection.
func (r Release) Path(rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", ValidationError(ErrPathEscape, "path %q must be relative to the release root", rel)
	}
	joined := filepath.Join(r.Root, rel)
	cleanRoot := filepath.Clean(r.Root)
	if joined != cleanRoot && !strings.HasPrefix(joined, cleanRoot+string(filepath.Separator)) {
		return "", ValidationError(ErrPathEscape, "path %q escapes the release root", rel)
	}
	return joined, nil
}

// ComposeFilePaths resolves the Compose files for a profile to absolute paths.
func (r Release) ComposeFilePaths(profile string) ([]string, error) {
	rels, err := r.Manifest.Runtime.ComposeFiles(profile)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rels))
	for _, rel := range rels {
		abs, err := r.Path(rel)
		if err != nil {
			return nil, err
		}
		out = append(out, abs)
	}
	return out, nil
}

// ReleaseRecord is the persisted pointer to an installed release. It is
// deliberately small: the full manifest is re-read from disk, but the record
// alone must be enough for `status` to answer without touching /opt.
type ReleaseRecord struct {
	SchemaVersion int     `json:"schema_version"`
	Name          string  `json:"name"`
	Version       Version `json:"version"`
	Digest        string  `json:"digest"`
	Root          string  `json:"root"`
	InstalledAt   Time    `json:"installed_at"`
	OperationID   string  `json:"operation_id,omitempty"`

	// SchemaAtInstall is the database schema version this release migrated
	// to. Rollback needs it, and re-deriving it later would mean running
	// the product's migration tooling just to ask a question.
	SchemaAtInstall int `json:"schema_at_install,omitempty"`

	// SourceRef is where this release came from, so `update --check` has
	// something to query and the doctor check can run unattended.
	//
	// Recorded when a release becomes *current*, never when a candidate is
	// staged: a staged release is not the installed one, and checking the
	// candidate's source while the old release is still running would
	// report on a release nobody is using. Empty for anything installed
	// before this was recorded, and for a source that cannot enumerate.
	SourceRef string `json:"source_ref,omitempty"`
}

func (r ReleaseRecord) IsZero() bool { return r.Name == "" && r.Version.IsZero() }

func (r ReleaseRecord) String() string {
	if r.IsZero() {
		return "none"
	}
	return r.Name + " " + r.Version.String()
}
