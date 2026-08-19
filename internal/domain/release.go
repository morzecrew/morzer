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

// RuntimeFilePaths resolves one runtime's declared files for a profile to
// absolute paths.
//
// Named for the runtime rather than for Compose (RFC 0023 §2.1 inventoried the
// old name as a leak), and taking the runtime as an argument rather than
// reading it from anywhere: which runtime this installation uses is the
// caller's fact, and a function that looked it up would be this layer deciding
// a runtime, which is what decision 7 forbids.
//
// An undeclared runtime refuses and names what the release does declare. That
// is decision 5 at the point a caller would otherwise get an empty file list
// and deploy nothing while reporting success.
func (r Release) RuntimeFilePaths(runtime, profile string) ([]string, error) {
	declared := r.Manifest.DeclaredRuntimes()
	decl, ok := declared[runtime]
	if !ok {
		return nil, ValidationError(nil,
			"this release does not support the %s runtime", runtime).
			WithHint("it declares: %s", strings.Join(declared.Names(), ", "))
	}
	rels, err := decl.FilesFor(profile)
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

	// UpstreamDigest is what the source said its artefact was when this
	// release was fetched -- the registry's manifest digest, not the
	// bundle's content digest.
	//
	// It exists so a channel poll can tell "the tag still points at what is
	// installed" from "the tag moved" without downloading anything. Without
	// it, every tick would re-fetch the bundle to compute a content digest
	// and discover it already had it.
	//
	// Empty for a release installed from a path, from a source that cannot
	// peek, or before this was recorded. Empty means "unknown", and a poll
	// treats unknown as changed exactly once, which costs one fetch and then
	// records the answer.
	UpstreamDigest string `json:"upstream_digest,omitempty"`
}

func (r ReleaseRecord) IsZero() bool { return r.Name == "" && r.Version.IsZero() }

func (r ReleaseRecord) String() string {
	if r.IsZero() {
		return "none"
	}
	return r.Name + " " + r.Version.String()
}

// UpdateCandidateSchemaVersion versions the candidate pointer.
//
// Its own version rather than the installation's: this file is written by a
// poll and read by `status`, and an older manager that has never heard of it
// simply does not read it. Nothing derived from it changes what is deployed.
const UpdateCandidateSchemaVersion = 1

// UpdateCandidate is what a followed channel last pointed at, and what became
// of it.
//
// One record for both outcomes -- staged, or refused -- because the poll needs
// to remember the refusals too. A tag republished with a version that is already
// installed is refused by the never-republish rule, and a poll that recorded
// only successes would re-download that bundle on every tick, forever, to reach
// the same refusal.
//
// Not part of the installation state: this is derived, disposable, and rebuilt
// by the next poll. Losing it costs one fetch.
type UpdateCandidate struct {
	SchemaVersion int `json:"schema_version"`

	// SourceRef is the channel this came from, redacted of credentials.
	SourceRef string `json:"source_ref"`

	// UpstreamDigest is what the channel pointed at. This is the field the
	// poll compares; everything else is for the operator.
	UpstreamDigest string `json:"upstream_digest"`

	SeenAt Time `json:"seen_at"`

	// Name, Version, Digest and Root describe the bundle, and are set only
	// when it was actually staged.
	Name    string  `json:"name,omitempty"`
	Version Version `json:"version,omitempty"`
	Digest  string  `json:"digest,omitempty"`
	Root    string  `json:"root,omitempty"`

	// Refused says why this candidate was not staged, in the operator's
	// words. Empty when it was.
	Refused string `json:"refused,omitempty"`
}

func (c UpdateCandidate) IsZero() bool { return c.UpstreamDigest == "" }

// IsStaged reports whether the bundle is on this machine, verified and ready to
// install.
//
// Both halves are required. A root without the refusal cleared would be a
// half-written record, and a refusal with a root is a bundle that was fetched
// and then found unacceptable -- neither is something to offer an operator as
// installable.
func (c UpdateCandidate) IsStaged() bool { return c.Root != "" && c.Refused == "" }

func (c UpdateCandidate) String() string {
	switch {
	case c.IsZero():
		return "none"
	case c.IsStaged():
		return c.Name + " " + c.Version.String()
	default:
		return "refused: " + c.Refused
	}
}
