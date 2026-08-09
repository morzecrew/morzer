package atomicfs

import (
	"archive/tar"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/morzecrew/morzer/internal/domain"
)

// WriteTarZst writes the named files as a zstd-compressed tar, in the order
// given.
//
// It enumerates nothing and sorts nothing: the caller supplies the entry list
// and its order, because the order is a property of the release format rather
// than of tar. See release.ArchiveEntries, which is where the ranks live.
//
// Only regular files are written. Directory entries are omitted deliberately —
// ExtractTarZst creates a parent directory when it meets a file that needs one,
// and `sha256sum` does not list directories either, so emitting them would add
// entries that carry no content, appear in no checksum list, and have to be
// ordered against the files they contain.
//
// Every header is normalised so that two archives of the same tree are
// byte-identical:
//
//   - uid/gid 0 and empty owner names, so the vendor's account does not reach
//     the artifact.
//   - The mode is the executable bit and nothing else, matching what the
//     content digest records and what extraction restores. A vendor's umask is
//     therefore not an input to the archive.
//   - One mtime for every entry, chosen by the caller, and no atime or ctime.
//   - Compression runs single-threaded. The encoder's block splitting is
//     otherwise a function of how many cores the build machine has, which would
//     make "reproducible" mean "reproducible on this laptop".
func WriteTarZst(dst, srcDir string, entries []string, modTime time.Time) error {
	root, err := OpenRoot(srcDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	// Written beside the destination and renamed, so an interrupted archive
	// never leaves a truncated .tar.zst that looks finished.
	tmp := dst + ".partial" + randomSuffix()
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return domain.ValidationError(err, "cannot create %s", dst)
	}
	defer func() {
		_ = out.Close()
		_ = os.Remove(tmp)
	}()

	if err := writeArchiveStream(out, root, entries, modTime); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return domain.Internal(err, "cannot flush %s to disk", dst)
	}
	if err := out.Close(); err != nil {
		return domain.Internal(err, "cannot finish writing %s", dst)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return domain.Internal(err, "cannot move the archive into place at %s", dst)
	}
	SyncDir(filepath.Dir(dst))
	return nil
}

func writeArchiveStream(out io.Writer, root *os.Root, entries []string, modTime time.Time) error {
	zw, err := zstd.NewWriter(out, zstd.WithEncoderConcurrency(1))
	if err != nil {
		return domain.Internal(err, "cannot start the archive encoder")
	}
	defer func() { _ = zw.Close() }()

	tw := tar.NewWriter(zw)
	for _, rel := range entries {
		if err := writeArchiveEntry(tw, root, rel, modTime); err != nil {
			return err
		}
	}

	// Both closes are checked: the tar writer emits the end-of-archive
	// marker on Close and the zstd writer flushes its final frame, so an
	// error swallowed here is an archive that is silently truncated at the
	// tail -- the shape a reader reports as "corrupt" with no idea why.
	if err := tw.Close(); err != nil {
		return domain.Internal(err, "cannot finish the archive")
	}
	if err := zw.Close(); err != nil {
		return domain.Internal(err, "cannot finish compressing the archive")
	}
	return nil
}

func writeArchiveEntry(tw *tar.Writer, root *os.Root, rel string, modTime time.Time) error {
	f, err := root.Open(rel)
	if err != nil {
		return domain.ValidationError(err, "cannot read %s for the archive", rel)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return domain.ValidationError(err, "cannot read %s for the archive", rel)
	}
	if !info.Mode().IsRegular() {
		// Extraction refuses these, so writing one would produce an
		// archive this manager cannot install.
		return domain.ValidationError(nil,
			"%s is not a regular file, and a release bundle may contain only regular files", rel)
	}

	hdr := &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     filepath.ToSlash(rel),
		Size:     info.Size(),
		Mode:     int64(normalizeArchiveMode(info.Mode())),
		ModTime:  modTime.UTC().Truncate(time.Second),

		// Pinned rather than left to the writer's own selection, which
		// picks a format from the header's contents and could change
		// with a Go release -- turning a byte-identical archive into a
		// differently-encoded one for a tree nobody touched.
		Format: tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return domain.Internal(err, "cannot write the archive entry for %s", rel)
	}

	// CopyN rather than Copy: tar declared the size in the header, so a
	// file that grew or shrank since the Stat would desynchronise the
	// stream. Refusing beats writing an archive that unpacks wrongly.
	written, err := io.CopyN(tw, f, info.Size())
	if err != nil {
		return domain.ValidationError(err,
			"cannot read %s for the archive (it declared %d bytes and %d were read)",
			rel, info.Size(), written)
	}
	return nil
}
