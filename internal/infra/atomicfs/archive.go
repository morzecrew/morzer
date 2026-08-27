package atomicfs

import (
	"archive/tar"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/klauspost/compress/zstd"

	"github.com/morzecrew/morzer/internal/domain"
)

// TarZstExtensions are the archive suffixes recognised as a release bundle.
//
// Deliberately short. Every additional container format is another parser
// reading attacker-influenced bytes, and one is enough: zstd compresses a
// bundle of text and small binaries about as well as anything and decompresses
// faster than all of them.
var TarZstExtensions = []string{".tar.zst", ".tzst"}

// IsTarZst reports whether a path names a zstd-compressed tar archive.
func IsTarZst(path string) bool {
	lower := strings.ToLower(path)
	return slices.ContainsFunc(TarZstExtensions, func(ext string) bool {
		return strings.HasSuffix(lower, ext)
	})
}

// decoderMaxMemory bounds the zstd window a single frame may ask for.
//
// The format lets an archive declare its own window size, so without a cap a
// 200-byte file can make the decoder allocate gigabytes before a single byte is
// extracted -- a denial of service that never reaches the size limits below,
// because nothing has been written yet.
const decoderMaxMemory = 64 << 20

// ExtractTarZst extracts a zstd-compressed tar archive into dst.
//
// This is the largest attack surface in the manager: it reads a format chosen
// by whoever produced the bundle and writes files onto the host as root. Every
// rule CopyTree applies to a directory applies here, enforced by the same
// ExtractLimits and the same rejection of anything that is not a regular file
// or a directory -- restating them would mean two places to review and one of
// them going stale.
//
// The rules, and why each is not merely defensive:
//
//   - Extraction goes through an os.Root on dst, so `../etc/passwd` fails at
//     the syscall rather than at a string check somebody has to get right.
//   - Limits are enforced *during* the copy, not after. A decompression bomb
//     has to be refused while it is being written; discovering it once the disk
//     is full is discovering it too late.
//   - A declared entry size is a claim, not a fact. Copies are bounded by a
//     limited reader, so an archive whose header lies about a file's length
//     cannot write more than the limit permits.
//   - Symlinks, hardlinks, device nodes, FIFOs and sockets are refused rather
//     than skipped. A bundle containing one is broken or hostile, and
//     extracting "most of" it would produce a release that differs from the one
//     that was verified.
func ExtractTarZst(src, dst string, limits ExtractLimits) error {
	f, err := os.Open(src)
	if err != nil {
		return domain.ValidationError(err, "cannot open the archive %s", src)
	}
	defer func() { _ = f.Close() }()

	zr, err := zstd.NewReader(f, zstd.WithDecoderMaxMemory(decoderMaxMemory))
	if err != nil {
		return domain.ValidationError(err, "%s is not a valid zstd archive", src).
			WithHint("release archives are tar.zst; check the file was downloaded completely")
	}
	defer zr.Close()

	if err := MkdirAll(dst, 0o755); err != nil {
		return err
	}
	root, err := OpenRoot(dst)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	tr := tar.NewReader(zr)

	// The budget is read from the stream before the rest of it is
	// committed to. It cannot be read from the extracted tree, because
	// extracting the tree is what it bounds.
	limits, err = negotiateLimits(tr, root, dst, src, limits)
	if err != nil {
		return err
	}

	return extractTar(tr, root, src, limits)
}

// maxManifestSize bounds the one entry read before any limit is settled.
//
// A manifest is a few kilobytes; a megabyte is four hundred times generous.
// The bound exists because this read happens *before* the negotiated ceiling
// is known, so without it an archive could declare its budget in a file large
// enough to be the attack.
const maxManifestSize = 1 << 20

// negotiateLimits reads the archive's first entry and settles the ceiling.
//
// Three rules, and each of them is the answer to a way of getting this wrong:
//
//   - The first entry must be the one the caller named. RFC 0014 makes
//     `manifest.yaml` a property of the archive format precisely so this read
//     is possible, and an archive that does not honour it is **refused** rather
//     than quietly given the default ceiling. A fallback would make the
//     ordering advisory, and an advisory guarantee is one that decays.
//   - A declared budget may only *lower* the ceiling, never raise it past the
//     hard caps. The declaration is made by the same unverified bytes the
//     guard bounds.
//   - No declaration means the default ceiling, not "unbounded". A missing
//     field must never be the permissive reading of anything gating untrusted
//     bytes.
//
// The manifest entry is written out here as well as read, because the stream
// moves forward: rewinding a decompressor to re-read the first entry means
// decompressing it twice, and the caller wants that file on disk regardless.
func negotiateLimits(
	tr *tar.Reader, root *os.Root, dst, src string, limits ExtractLimits,
) (ExtractLimits, error) {
	if limits.FirstEntry == "" || limits.Budget == nil {
		return limits, nil
	}

	hdr, err := tr.Next()
	if errors.Is(err, io.EOF) {
		return limits, domain.ValidationError(nil, "%s contains no entries", src)
	}
	if err != nil {
		return limits, domain.ValidationError(err, "cannot read %s", src).
			WithHint("the archive is truncated or corrupt")
	}

	rel, err := archiveEntryPath(hdr.Name)
	if err != nil {
		return limits, err
	}
	if rel != limits.FirstEntry {
		// hdr.Name rather than the cleaned path: `tar -C dir .` -- the
		// commonest way to produce a wrong archive -- opens with "./",
		// which cleans to the empty string. Reporting that reads as
		// though the archive has a nameless first entry.
		return limits, domain.ValidationError(domain.ErrValidation,
			"%s does not begin with %s (its first entry is %q)",
			src, limits.FirstEntry, hdr.Name).
			WithHint("a release archive states its size in the manifest, which has to " +
				"arrive before the bytes it bounds. Repack it with " +
				"`morzer release archive`")
	}
	if hdr.Typeflag != tar.TypeReg {
		return limits, domain.ValidationError(domain.ErrValidation,
			"%s in %s is not a regular file", limits.FirstEntry, src)
	}
	if hdr.Size > maxManifestSize {
		return limits, domain.ValidationError(domain.ErrValidation,
			"%s in %s declares %d bytes, which is not a manifest",
			limits.FirstEntry, src, hdr.Size)
	}

	manifest, err := io.ReadAll(io.LimitReader(tr, maxManifestSize))
	if err != nil {
		return limits, domain.ValidationError(err, "cannot read %s from %s",
			limits.FirstEntry, src)
	}

	declared, err := limits.Budget(manifest)
	if err != nil {
		return limits, err
	}

	limits = clampToBudget(limits, declared)
	// Against the *effective* ceiling rather than the declaration: a bundle
	// declaring more than the hard cap can still only write up to the cap,
	// and demanding the declared figure be free would refuse an install that
	// would have succeeded.
	if err := checkFreeSpace(dst, limits, declared); err != nil {
		return limits, err
	}

	// Charged before it is written, so a declaration too small to hold its
	// own manifest is refused with nothing on disk.
	limits, err = chargeForManifest(limits, int64(len(manifest)))
	if err != nil {
		return limits, err
	}

	if err := writeManifestEntry(root, rel, manifest, hdr); err != nil {
		return limits, err
	}

	return limits, nil
}

// chargeForManifest deducts the already-extracted manifest from the budget.
//
// The subtlety that makes this worth its own function: the extractor reads a
// **non-positive limit as no limit at all**, so a subtraction that lands on
// zero converts an exhausted budget into an unlimited one. A bundle declaring
// one megabyte with a one-megabyte manifest would then have extracted every
// remaining entry unbounded -- twenty thousand of them, at the per-file
// ceiling, which is four orders of magnitude past what it declared.
//
// So an exhausted budget is not represented at all: a declaration that does not
// leave room for the rest of the archive is refused outright. That is the same
// rule the budget already carries -- an archive exceeding its own declaration
// is refused -- applied to the one entry that had to be read before the rule
// could be enforced.
func chargeForManifest(limits ExtractLimits, size int64) (ExtractLimits, error) {
	if limits.MaxTotalSize > 0 {
		if size >= limits.MaxTotalSize {
			return limits, domain.ValidationError(domain.ErrValidation,
				"the manifest alone is %s and the bundle declares %s in total",
				domain.ByteSize(size), domain.ByteSize(limits.MaxTotalSize)).
				WithHint("bundle.uncompressed_size is what the whole archive expands to, " +
					"including the manifest")
		}
		limits.MaxTotalSize -= size
	}
	if limits.MaxEntries > 0 {
		if limits.MaxEntries <= 1 {
			return limits, domain.ValidationError(domain.ErrValidation,
				"the archive may hold %d entries and the manifest is one of them",
				limits.MaxEntries)
		}
		limits.MaxEntries--
	}
	return limits, nil
}

// writeManifestEntry writes the one entry that was read before the limits were
// settled.
//
// Deliberately not WriteFileIn, which finishes with a directory fsync whose
// failure it propagates. That is right where it is used -- a state file whose
// directory entry never landed is a file that never existed -- and wrong here:
// every *other* entry in this archive is written by extractFile, which fsyncs
// the file and not its directory. Using the stricter helper for exactly one
// entry made a filesystem that cannot fsync a directory reject an otherwise
// valid bundle, and made the manifest the odd one out in its own extraction.
func writeManifestEntry(root *os.Root, rel string, data []byte, hdr *tar.Header) error {
	mode := normalizeArchiveMode(hdr.FileInfo().Mode())

	out, err := root.OpenFile(rel, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return domain.ValidationError(err, "cannot create %q from the archive", rel)
	}
	defer func() { _ = out.Close() }()

	if _, err := out.Write(data); err != nil {
		return domain.ValidationError(err, "cannot write %q from the archive", rel)
	}
	if err := out.Chmod(mode); err != nil {
		return domain.Internal(err, "cannot set mode on %q", rel)
	}
	// The same reason extractFile gives: the digest is verified against
	// what the page cache holds, and a power cut after promotion would
	// otherwise leave `current` durably pointing at a truncated file the
	// verification blessed.
	if err := out.Sync(); err != nil {
		return domain.Internal(err, "cannot flush %q to disk", rel)
	}
	if err := out.Close(); err != nil {
		return domain.Internal(err, "cannot finish writing %q", rel)
	}
	return nil
}

// clampToBudget applies a declared size to the limits.
//
// min in both directions: a declaration larger than the hard cap is cut down to
// it, and one smaller than the default is honoured as the stricter bound the
// vendor asked for -- an archive that exceeds its own declaration is refused,
// which is the other half of the budget being meaningful.
func clampToBudget(limits ExtractLimits, declared int64) ExtractLimits {
	if declared <= 0 {
		return limits
	}
	limits.MaxTotalSize = min(declared, int64(HardMaxTotalSize))
	// A single file cannot exceed the whole archive, and an image layout is
	// usually one dominant blob rather than an even spread -- 110 M of
	// postgres's 115 M is a single layer -- so the per-file bound has to
	// track the total rather than stay at its default.
	limits.MaxFileSize = min(limits.MaxTotalSize, int64(HardMaxFileSize))
	return limits
}

// checkFreeSpace refuses a bundle the disk cannot hold, before writing any of
// it.
//
// Only against a declared budget: without one the default ceiling applies, and
// demanding two gigabytes free to extract a two-megabyte bundle would refuse
// every ordinary install on a small disk. A clean refusal beats a full
// filesystem, but only where there is a claim to check.
func checkFreeSpace(dst string, limits ExtractLimits, declared int64) error {
	if declared <= 0 {
		return nil
	}
	free, err := freeSpace(dst)
	if err != nil {
		// Not fatal: a filesystem whose free space cannot be read is not
		// evidence of a full one, and the extraction limits still bound
		// what gets written.
		return nil
	}
	if free < limits.MaxTotalSize {
		return domain.ValidationError(domain.ErrValidation,
			"the bundle needs up to %s and %s is free on %s",
			domain.ByteSize(limits.MaxTotalSize), domain.ByteSize(free), dst).
			WithHint("free some space, or install to a filesystem that has room")
	}
	return nil
}

// freeSpace is FreeSpace unless a test replaced it.
//
// Injectable for the reason Deps.FreeSpace already records: a check whose
// verdict depends on the host's real disk is a check whose test passes or fails
// on which machine ran it.
var freeSpace = FreeSpace

func extractTar(tr *tar.Reader, root *os.Root, src string, limits ExtractLimits) error {
	var entries int
	var total int64

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return domain.ValidationError(err, "cannot read %s", src).
				WithHint("the archive is truncated or corrupt")
		}

		entries++
		if limits.MaxEntries > 0 && entries > limits.MaxEntries {
			return domain.ValidationError(nil,
				"archive exceeds the entry limit of %d files", limits.MaxEntries).
				WithHint("this bundle is unusually large; verify it came from the vendor you expect")
		}

		rel, err := archiveEntryPath(hdr.Name)
		if err != nil {
			return err
		}
		if rel == "" {
			continue // the archive's own root entry
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := mkdirAllIn(root, rel, 0o755); err != nil {
				return err
			}

		case tar.TypeReg:
			written, err := extractFile(tr, root, rel, hdr, limits, total)
			if err != nil {
				return err
			}
			total += written

		case tar.TypeSymlink, tar.TypeLink:
			// Named separately from the catch-all because it is the
			// one an attacker actually reaches for: a link to
			// /etc/shadow that a later entry then writes through.
			return domain.ValidationError(nil,
				"archive contains a link at %q, which is not allowed", rel).
				WithHint("release bundles must contain only regular files and directories")

		default:
			return domain.ValidationError(nil,
				"archive contains a non-regular entry at %q (tar type %q), which is not allowed",
				rel, string(hdr.Typeflag))
		}
	}
}

// extractFile writes one entry, bounded twice over.
func extractFile(
	tr *tar.Reader,
	root *os.Root,
	rel string,
	hdr *tar.Header,
	limits ExtractLimits,
	totalSoFar int64,
) (int64, error) {
	// The declared size is checked first because it is free, and refusing a
	// 4 GiB file before reading any of it beats refusing it after.
	if limits.MaxFileSize > 0 && hdr.Size > limits.MaxFileSize {
		return 0, domain.ValidationError(nil,
			"archive entry %q declares %d bytes, over the per-file limit of %d",
			rel, hdr.Size, limits.MaxFileSize)
	}

	if dir := path.Dir(rel); dir != "." {
		if err := mkdirAllIn(root, dir, 0o755); err != nil {
			return 0, err
		}
	}

	mode := normalizeArchiveMode(hdr.FileInfo().Mode())
	out, err := root.OpenFile(rel, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return 0, domain.ValidationError(err, "cannot create %q from the archive", rel)
	}
	defer func() { _ = out.Close() }()

	// The bound the declared size cannot lie past: whichever of the
	// per-file and remaining-total limits is tighter, plus one byte so
	// exceeding it is detectable rather than merely reaching it.
	remaining := int64(-1)
	if limits.MaxFileSize > 0 {
		remaining = limits.MaxFileSize
	}
	if limits.MaxTotalSize > 0 {
		left := limits.MaxTotalSize - totalSoFar
		if remaining < 0 || left < remaining {
			remaining = left
		}
	}

	var reader io.Reader = tr
	if remaining >= 0 {
		reader = io.LimitReader(tr, remaining+1)
	}

	written, err := io.Copy(out, reader)
	if err != nil {
		return 0, domain.ValidationError(err, "cannot write %q from the archive", rel)
	}
	if remaining >= 0 && written > remaining {
		// Reached only by an archive that is lying, decompressing to
		// far more than it claims, or both.
		return 0, domain.ValidationError(nil,
			"archive exceeds its size limits while extracting %q", rel).
			WithHint("the archive expands to more than %d bytes, which no release bundle should",
				limits.MaxTotalSize)
	}
	if err := out.Chmod(mode); err != nil {
		return 0, domain.Internal(err, "cannot set mode on %q", rel)
	}
	// Fsync before the tree is promoted: the digest is verified against
	// what the page cache holds, and a power cut after promotion would
	// otherwise leave `current` durably pointing at truncated files the
	// verification blessed. Close is checked for the same reason -- it is
	// the last write error this file can report.
	if err := out.Sync(); err != nil {
		return 0, domain.Internal(err, "cannot flush %q to disk", rel)
	}
	if err := out.Close(); err != nil {
		return 0, domain.Internal(err, "cannot finish writing %q", rel)
	}
	return written, nil
}

// archiveEntryPath validates and normalises an entry name.
//
// os.Root would refuse an escaping path at the syscall, and does; this runs
// first so the operator gets "the archive contains a path that escapes it"
// rather than a permission error from deep inside the extractor.
func archiveEntryPath(name string) (string, error) {
	clean := path.Clean(strings.TrimPrefix(filepath.ToSlash(name), "./"))

	switch {
	case clean == "." || clean == "/":
		return "", nil
	case path.IsAbs(clean):
		return "", domain.ValidationError(domain.ErrPathEscape,
			"archive contains an absolute path %q", name).
			WithHint("entries must be relative to the bundle root")
	case clean == ".." || strings.HasPrefix(clean, "../"):
		return "", domain.ValidationError(domain.ErrPathEscape,
			"archive contains a path that escapes the bundle: %q", name).
			WithHint("this archive is malformed or hostile; do not install it")
	case strings.ContainsRune(clean, 0):
		return "", domain.ValidationError(domain.ErrPathEscape,
			"archive contains a path with a null byte")
	}
	return clean, nil
}

// normalizeArchiveMode reduces a recorded mode to one of two values.
//
// Only the executable bit survives, which is the only permission the content
// digest records and the only one a release depends on: a hook must be
// runnable, and nothing else in a bundle cares. Normalising also means an
// archive cannot ship a world-writable file, or a setuid one -- Perm() already
// drops setuid, and collapsing the rest removes the question entirely.
func normalizeArchiveMode(mode fs.FileMode) fs.FileMode {
	if mode.Perm()&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

// ReadFirstArchiveEntry returns the bytes of an archive's first entry, refusing
// unless it is the file the caller named.
//
// A release archive's first entry is `manifest.yaml` by construction (RFC 0014
// decision 2), which is what makes this possible at all: the manifest can be
// read without extracting the archive, without a temporary directory, and
// without decompressing past a few kilobytes.
//
// Refuses rather than searching, for the reason negotiateLimits refuses: the
// ordering is a guarantee the format makes, and a reader that falls back to
// scanning turns it into a convention. The two readers of the first entry then
// disagree about what an archive has to be, and the one that is lenient is the
// one that lets a non-conforming archive through to the one that is not.
//
// Bounded by maxManifestSize, because this read happens before anything has
// established what the archive is.
func ReadFirstArchiveEntry(src, want string) ([]byte, error) {
	f, err := os.Open(src)
	if err != nil {
		return nil, domain.ValidationError(err, "cannot open the archive %s", src)
	}
	defer func() { _ = f.Close() }()

	zr, err := zstd.NewReader(f, zstd.WithDecoderMaxMemory(decoderMaxMemory))
	if err != nil {
		return nil, domain.ValidationError(err, "%s is not a valid zstd archive", src).
			WithHint("release archives are tar.zst; check the file was downloaded completely")
	}
	defer zr.Close()

	tr := tar.NewReader(zr)
	hdr, err := tr.Next()
	if errors.Is(err, io.EOF) {
		return nil, domain.ValidationError(nil, "%s contains no entries", src)
	}
	if err != nil {
		return nil, domain.ValidationError(err, "cannot read %s", src).
			WithHint("the archive is truncated or corrupt")
	}

	rel, err := archiveEntryPath(hdr.Name)
	if err != nil {
		return nil, err
	}
	if rel != want {
		return nil, domain.ValidationError(nil,
			"%s does not begin with %s (its first entry is %q)", src, want, hdr.Name).
			WithHint("a release archive states its size in the manifest, which has to " +
				"arrive before the bytes it bounds. Repack it with `morzer release archive`")
	}

	data, err := io.ReadAll(io.LimitReader(tr, maxManifestSize+1))
	if err != nil {
		return nil, domain.ValidationError(err, "cannot read %s from %s", want, src)
	}
	if int64(len(data)) > maxManifestSize {
		return nil, domain.ValidationError(nil,
			"%s in %s is larger than %d bytes", want, src, maxManifestSize)
	}
	return data, nil
}
