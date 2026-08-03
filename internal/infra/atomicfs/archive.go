package atomicfs

import (
	"archive/tar"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
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
	for _, ext := range TarZstExtensions {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
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

	return extractTar(tar.NewReader(zr), root, src, limits)
}

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
