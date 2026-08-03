package suite

import (
	"archive/tar"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
)

// Archive extraction is the largest attack surface in the manager: it parses a
// format chosen by whoever produced the bundle and writes files onto the host,
// as root, before anything in the bundle has been verified as safe to run.
//
// These fixtures are built rather than checked in. A malicious archive in the
// repository is a file every contributor's editor, indexer and antivirus will
// eventually open, and one whose contents nobody can review by reading them.
// Building each one in the test that uses it means the attack is written out in
// Go, next to the assertion about what must happen to it.

// tarEntry is one archive member, as hostile as it needs to be.
type tarEntry struct {
	Name     string
	Body     string
	Mode     int64
	Typeflag byte
	Linkname string
}

// writeArchive builds a tar.zst from explicit entries, applying no validation
// of its own -- an archive writer that refused to produce a hostile archive
// could not be used to test refusing one.
func writeArchive(t *testing.T, path string, entries []tarEntry) {
	t.Helper()

	f, err := os.Create(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()

	zw, err := zstd.NewWriter(f)
	require.NoError(t, err)
	tw := tar.NewWriter(zw)

	for _, e := range entries {
		mode := e.Mode
		if mode == 0 {
			mode = 0o644
		}
		typeflag := e.Typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		size := int64(len(e.Body))

		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name:     e.Name,
			Mode:     mode,
			Size:     size,
			Typeflag: typeflag,
			Linkname: e.Linkname,
		}))
		if typeflag == tar.TypeReg && e.Body != "" {
			_, err := tw.Write([]byte(e.Body))
			require.NoError(t, err)
		}
	}

	require.NoError(t, tw.Close())
	require.NoError(t, zw.Close())
}

// writeTarZst packs a directory into a tar.zst, the way a vendor's release
// pipeline would. Used by the release-source contract suite.
func writeTarZst(t *testing.T, srcDir, dest string) {
	t.Helper()

	f, err := os.Create(dest)
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()

	zw, err := zstd.NewWriter(f)
	require.NoError(t, err)
	tw := tar.NewWriter(zw)

	require.NoError(t, filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = src.Close() }()
		_, err = io.Copy(tw, src)
		return err
	}))

	require.NoError(t, tw.Close())
	require.NoError(t, zw.Close())
}

// extractFixture builds an archive and tries to extract it, returning the
// destination and whatever went wrong.
func extractFixture(t *testing.T, entries []tarEntry, limits atomicfs.ExtractLimits) (dest string, err error) {
	t.Helper()

	archive := filepath.Join(t.TempDir(), "hostile.tar.zst")
	writeArchive(t, archive, entries)
	dest = filepath.Join(t.TempDir(), "extracted")
	return dest, atomicfs.ExtractTarZst(archive, dest, limits)
}

func TestArchiveExtractionRefusesHostileEntries(t *testing.T) {
	limits := atomicfs.DefaultExtractLimits()

	t.Run("path traversal", func(t *testing.T) {
		dest, err := extractFixture(t, []tarEntry{
			{Name: "manifest.yaml", Body: "ok"},
			{Name: "../../../../etc/cron.d/pwned", Body: "* * * * * root sh -c evil"},
		}, limits)

		require.Error(t, err, "an entry escaping the destination must be refused")
		assert.True(t, errors.Is(err, domain.ErrPathEscape),
			"the refusal must be typed as a path escape, got: %v", err)

		// The point is not only that it errored. Nothing may have been
		// written outside the destination at any moment.
		assert.NoFileExists(t, filepath.Join(filepath.Dir(dest), "etc", "cron.d", "pwned"))
	})

	t.Run("absolute path", func(t *testing.T) {
		_, err := extractFixture(t, []tarEntry{
			{Name: "/etc/shadow", Body: "root::0:0:99999:7:::"},
		}, limits)

		require.Error(t, err)
		assert.True(t, errors.Is(err, domain.ErrPathEscape), "got: %v", err)
	})

	t.Run("symlink escape", func(t *testing.T) {
		// The classic two-step: plant a link pointing out of the
		// destination, then write "through" it with a later entry.
		_, err := extractFixture(t, []tarEntry{
			{Name: "manifest.yaml", Body: "ok"},
			{Name: "escape", Typeflag: tar.TypeSymlink, Linkname: "/etc"},
			{Name: "escape/cron.d/pwned", Body: "evil"},
		}, limits)

		require.Error(t, err, "a symlink in a bundle must be refused, not followed")
		assert.Contains(t, err.Error(), "link")
	})

	t.Run("hardlink", func(t *testing.T) {
		_, err := extractFixture(t, []tarEntry{
			{Name: "manifest.yaml", Body: "ok"},
			{Name: "link", Typeflag: tar.TypeLink, Linkname: "/etc/shadow"},
		}, limits)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "link")
	})

	t.Run("device node", func(t *testing.T) {
		// A bundle shipping /dev/sda would give a hook a raw disk to
		// write to, with none of the manager's containment applying.
		_, err := extractFixture(t, []tarEntry{
			{Name: "disk", Typeflag: tar.TypeBlock},
		}, limits)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "non-regular")
	})

	t.Run("fifo", func(t *testing.T) {
		// Not exotic: a FIFO where a config file is expected makes the
		// next reader block forever, which is a denial of service the
		// operator would diagnose as a hang.
		_, err := extractFixture(t, []tarEntry{
			{Name: "pipe", Typeflag: tar.TypeFifo},
		}, limits)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "non-regular")
	})

	t.Run("entry-count bomb", func(t *testing.T) {
		entries := make([]tarEntry, 0, 64)
		for i := range 64 {
			entries = append(entries, tarEntry{Name: "f" + itoa(i), Body: "x"})
		}

		_, err := extractFixture(t, entries, atomicfs.ExtractLimits{
			MaxEntries: 10, MaxTotalSize: 1 << 20, MaxFileSize: 1 << 20,
		})

		require.Error(t, err, "an archive with more entries than the limit must be refused")
		assert.Contains(t, err.Error(), "entry limit")
	})

	t.Run("declared size over the per-file limit", func(t *testing.T) {
		_, err := extractFixture(t, []tarEntry{
			{Name: "big", Body: strings.Repeat("A", 100)},
		}, atomicfs.ExtractLimits{MaxEntries: 10, MaxTotalSize: 1 << 20, MaxFileSize: 50})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "per-file limit")
	})

	t.Run("total size exceeded mid-extraction", func(t *testing.T) {
		// Each entry is under the per-file limit; together they are not.
		// The refusal has to come while writing, not from a tally taken
		// afterwards -- by then the disk is already full.
		entries := []tarEntry{
			{Name: "a", Body: strings.Repeat("A", 400)},
			{Name: "b", Body: strings.Repeat("B", 400)},
			{Name: "c", Body: strings.Repeat("C", 400)},
		}

		dest, err := extractFixture(t, entries, atomicfs.ExtractLimits{
			MaxEntries: 100, MaxTotalSize: 900, MaxFileSize: 500,
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "size limits")

		// The last entry must not have been written in full: the limit
		// stopped the copy rather than judging it after the fact.
		if info, statErr := os.Stat(filepath.Join(dest, "c")); statErr == nil {
			assert.Less(t, info.Size(), int64(400),
				"the copy must stop at the limit, not complete and then complain")
		}
	})

	t.Run("decompression bomb", func(t *testing.T) {
		// The attack the size limits exist for: a few kilobytes on the
		// wire that become gigabytes on disk. Eight megabytes of zeros
		// is the same shape at a size a test can afford.
		const bombSize = 8 << 20

		archive := filepath.Join(t.TempDir(), "bomb.tar.zst")
		writeArchive(t, archive, []tarEntry{
			{Name: "manifest.yaml", Body: "ok"},
			{Name: "bomb", Body: strings.Repeat("\x00", bombSize)},
		})

		packed, err := os.Stat(archive)
		require.NoError(t, err)
		require.Less(t, packed.Size(), int64(bombSize/100),
			"the fixture is only a bomb if it compresses hugely; it did not")

		dest := filepath.Join(t.TempDir(), "extracted")
		err = atomicfs.ExtractTarZst(archive, dest, atomicfs.ExtractLimits{
			MaxEntries: 100, MaxTotalSize: 1 << 20, MaxFileSize: 1 << 20,
		})
		require.Error(t, err, "an archive that expands past the limit must be refused")

		// Refused on what it expands to, not on what it weighs. A check
		// against the compressed size would let this through.
		total, err := atomicfs.DirSize(dest)
		require.NoError(t, err)
		assert.LessOrEqual(t, total, int64(1<<20)+1,
			"extraction must stop at the limit rather than run to completion")
	})

	t.Run("not a zstd archive at all", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bundle.tar.zst")
		require.NoError(t, os.WriteFile(path, []byte("this is not compressed"), 0o644))

		err := atomicfs.ExtractTarZst(path, filepath.Join(t.TempDir(), "out"), limits)
		require.Error(t, err, "a file that is not an archive must fail with a message about the archive")
	})

	t.Run("truncated archive", func(t *testing.T) {
		archive := filepath.Join(t.TempDir(), "bundle.tar.zst")
		writeArchive(t, archive, []tarEntry{{Name: "manifest.yaml", Body: strings.Repeat("x", 4096)}})

		data, err := os.ReadFile(archive)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(archive, data[:len(data)/2], 0o644))

		err = atomicfs.ExtractTarZst(archive, filepath.Join(t.TempDir(), "out"), limits)
		require.Error(t, err, "a half-downloaded archive must not extract as a valid bundle")
	})
}

func TestArchiveExtractionNormalisesModes(t *testing.T) {
	dest, err := extractFixture(t, []tarEntry{
		{Name: "hooks/migrate", Body: "#!/bin/sh\n", Mode: 0o777},
		{Name: "manifest.yaml", Body: "ok", Mode: 0o666},
	}, atomicfs.DefaultExtractLimits())
	require.NoError(t, err)

	// Executable survives, because whether a hook can run is part of what
	// the bundle is and is recorded in its digest.
	hook, err := os.Stat(filepath.Join(dest, "hooks", "migrate"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), hook.Mode().Perm(),
		"an executable entry stays executable, but not world-writable")

	// Everything else collapses to one value, so an archive cannot ship a
	// world-writable release file and nothing has to reason about which
	// permission bits a vendor happened to set.
	manifest, err := os.Stat(filepath.Join(dest, "manifest.yaml"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), manifest.Mode().Perm())
}

func TestArchiveAndDirectoryHashIdentically(t *testing.T) {
	bundle := testBundlePath(t)

	archive := filepath.Join(t.TempDir(), "bundle.tar.zst")
	writeTarZst(t, bundle, archive)

	extracted := filepath.Join(t.TempDir(), "extracted")
	require.NoError(t, atomicfs.ExtractTarZst(archive, extracted, atomicfs.DefaultExtractLimits()))

	want, err := atomicfs.DigestTree(bundle)
	require.NoError(t, err)
	got, err := atomicfs.DigestTree(extracted)
	require.NoError(t, err)

	// Without this, pinning a digest would mean pinning a transport too: an
	// operator who recorded a digest from an unpacked bundle could never
	// verify the archive their vendor publishes.
	assert.Equal(t, want, got,
		"a bundle and its archive must produce the same content digest")
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
