package release

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
)

// ArchiveExtension is the suffix `release archive` writes and every transport
// reads. One container format, deliberately -- see atomicfs.TarZstExtensions.
const ArchiveExtension = ".tar.zst"

// ImagesDirName is where a bundle carries container images it ships itself.
//
// Named here because the archive's entry order puts it last, which is a rule
// this package enforces today for a consumer that arrives with RFC 0011.
const ImagesDirName = "images"

// SourceDateEpochEnv is the reproducible-builds convention for pinning the
// timestamps an artifact records.
const SourceDateEpochEnv = "SOURCE_DATE_EPOCH"

// Entry ranks. The order is a property of the release archive format rather
// than an implementation detail, because a reader depends on it: RFC 0011 sizes
// its extraction budget from a declaration it reads out of the tar stream
// before extracting the rest, which works only if the manifest arrives first.
//
// Ranks order the four groups; within a group, entries are sorted
// lexicographically by their slash-separated relative path. The ranks serve the
// budget read and the sort serves reproducibility -- directory traversal order
// is unspecified by every filesystem, so without the sort two archives of one
// tree could differ in entry order alone.
const (
	rankManifest = iota
	rankVersion
	rankIntegrity
	rankContent
	rankImages
)

func entryRank(rel string) int {
	switch {
	case rel == ManifestFileName:
		return rankManifest
	case rel == VersionFileName:
		return rankVersion
	case rel == ports.SumsFileName, rel == ports.SignatureFileName:
		return rankIntegrity
	case rel == ImagesDirName || strings.HasPrefix(rel, ImagesDirName+"/"):
		return rankImages
	default:
		return rankContent
	}
}

// ArchiveEntries lists every file in a bundle directory, in the locked order.
//
// Files only: see atomicfs.WriteTarZst for why directory entries are not
// emitted. Nothing is excluded -- an archive missing a file the bundle contains
// would unpack into a tree whose SHA256SUMS is incomplete, which is the failure
// the completeness rule exists to catch.
func ArchiveEntries(dir string) ([]string, error) {
	var entries []string

	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		entries = append(entries, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, domain.ValidationError(err, "cannot read the bundle at %s", dir)
	}
	if len(entries) == 0 {
		return nil, domain.ValidationError(domain.ErrNotFound, "%s contains no files", dir)
	}

	sort.Slice(entries, func(i, j int) bool {
		ri, rj := entryRank(entries[i]), entryRank(entries[j])
		if ri != rj {
			return ri < rj
		}
		return entries[i] < entries[j]
	})
	return entries, nil
}

// ArchiveName is the file `release archive` writes when given no -o.
//
// Guessable by a human and scriptable without a lookup, which is what a vendor
// wiring an upload step needs.
func ArchiveName(m domain.Manifest) string {
	return fmt.Sprintf("%s-%s%s", m.Metadata.Name, m.Metadata.Version, ArchiveExtension)
}

// WriteArchive packs a verified bundle directory into out.
//
// It does not verify: the caller does that first, because refusing to archive
// an unsummed or mismatched tree is a decision about what a release is, and it
// belongs beside the other refusals rather than buried in a writer.
func WriteArchive(dir, out string, modTime time.Time) error {
	entries, err := ArchiveEntries(dir)
	if err != nil {
		return err
	}
	return atomicfs.WriteTarZst(out, dir, entries, modTime)
}

// ArchiveModTime resolves the single timestamp every archive entry records.
//
// SOURCE_DATE_EPOCH wins where it is expressed, because that is the convention
// a reproducible-builds pipeline already sets and this tool has no business
// overriding it. Otherwise the caller's suggestion is used -- `archive` passes
// the commit date of the repository the bundle sits in, when there is one --
// and otherwise the epoch, which is a timestamp that is obviously not a build
// time rather than one that looks like a real date and is not.
func ArchiveModTime(fallback time.Time) (time.Time, error) {
	raw, ok := os.LookupEnv(SourceDateEpochEnv)
	if !ok || strings.TrimSpace(raw) == "" {
		if fallback.IsZero() {
			return time.Unix(0, 0).UTC(), nil
		}
		return fallback.UTC(), nil
	}

	seconds, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		// Refused rather than ignored. A pipeline that sets this
		// variable is asking for a specific timestamp, and silently
		// substituting a different one produces an archive that is
		// reproducible by accident and wrong on purpose.
		return time.Time{}, domain.Usage("%s is %q, which is not a Unix timestamp",
			SourceDateEpochEnv, raw).
			WithHint("it is a count of seconds since 1970-01-01, e.g. the output of " +
				"`git log -1 --format=%%ct`")
	}
	return time.Unix(seconds, 0).UTC(), nil
}
