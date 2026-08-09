package release_test

import (
	"archive/tar"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/release"
)

// The archive's entry order is a property of the release format, not of this
// writer: a reader sizes its extraction budget from a declaration it takes out
// of the tar stream before extracting the rest, which works only while the
// manifest arrives first. That makes these the tests whose removal would be
// least noticed and most expensive.

// TestTheManifestIsTheFirstArchiveEntry is the guarantee another RFC rests on.
func TestTheManifestIsTheFirstArchiveEntry(t *testing.T) {
	dir := bundle(t, nil)

	out := filepath.Join(t.TempDir(), "demo.tar.zst")
	if err := release.WriteArchive(dir, out, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}

	names := archiveNames(t, out)
	if len(names) == 0 {
		t.Fatal("the archive is empty")
	}
	if names[0] != release.ManifestFileName {
		t.Errorf("first entry is %q, want %q", names[0], release.ManifestFileName)
	}
}

// TestArchiveEntriesAreRankedThenSorted asserts the order directly rather than
// inferring it from determinism.
//
// A determinism test alone passes on any machine whose readdir happens to be
// stable, which is most of them -- which is exactly how an unordered writer
// would reach the one vendor whose filesystem is not.
func TestArchiveEntriesAreRankedThenSorted(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"images/zebra.tar",
		"b.txt",
		"SHA256SUMS.minisig",
		"compose/compose.yaml",
		"images/aardvark.tar",
		"VERSION",
		"a.txt",
		"SHA256SUMS",
		"manifest.yaml",
	} {
		writeFixture(t, dir, name, name)
	}

	got, err := release.ArchiveEntries(dir)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		// The manifest first, so the budget is readable before
		// anything large is extracted.
		"manifest.yaml",
		"VERSION",
		// The integrity evidence ahead of the bytes it covers.
		"SHA256SUMS",
		"SHA256SUMS.minisig",
		// Content, sorted -- not in the order the directory
		// happened to be walked.
		"a.txt",
		"b.txt",
		"compose/compose.yaml",
		// Images last: the large content, extracted under a budget
		// that is by now known.
		"images/aardvark.tar",
		"images/zebra.tar",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("entry order:\ngot:\n  %s\nwant:\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// TestTwoArchivesOfOneTreeAreByteIdentical, and a changed SOURCE_DATE_EPOCH
// changes them.
//
// Both halves. Without the first, a writer that stamped the wall clock would
// pass; without the second, one that ignored the variable entirely would --
// and "deterministic" would mean "constant", which is a different and useless
// property.
func TestTwoArchivesOfOneTreeAreByteIdentical(t *testing.T) {
	dir := bundle(t, nil)
	work := t.TempDir()

	write := func(name string, epoch string) []byte {
		t.Helper()
		t.Setenv(release.SourceDateEpochEnv, epoch)
		modTime, err := release.ArchiveModTime(time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(work, name)
		if err := release.WriteArchive(dir, out, modTime); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	first := write("a.tar.zst", "1700000000")
	second := write("b.tar.zst", "1700000000")
	if string(first) != string(second) {
		t.Error("two archives of one tree differ, so the archive is not reproducible")
	}

	later := write("c.tar.zst", "1800000000")
	if string(later) == string(first) {
		t.Error("changing SOURCE_DATE_EPOCH changed nothing, so the timestamp is not recorded")
	}
}

// TestAnArchiveRoundTripsIntoALoadableRelease is the end of the chain: what
// `archive` writes is what the transports already read.
func TestAnArchiveRoundTripsIntoALoadableRelease(t *testing.T) {
	dir := bundle(t, nil)

	out := filepath.Join(t.TempDir(), "demo.tar.zst")
	if err := release.WriteArchive(dir, out, time.Unix(1700000000, 0)); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "unpacked")
	if err := atomicfs.ExtractTarZst(out, dst, atomicfs.DefaultExtractLimits()); err != nil {
		t.Fatalf("the archive this tool wrote cannot be extracted: %v", err)
	}

	// Load is the strong assertion: it checks the manifest, that VERSION
	// agrees with it, that every declared file exists, and that every
	// declared hook is executable -- so a writer that dropped the
	// executable bit fails here rather than during someone's `apply`.
	rel, err := release.Load(dst)
	if err != nil {
		t.Fatalf("the round-tripped bundle does not load: %v", err)
	}
	if rel.Version().String() != "1.2.0" {
		t.Errorf("version = %s", rel.Version())
	}

	// The documented claim, asserted rather than assumed: "a bundle and its
	// archive produce the same content digest, so a digest recorded from
	// the directory verifies the archive and vice versa". It survives
	// normalisation because the digest covers contents, paths and the
	// executable bit -- not ownership, not the other permission bits, and
	// not timestamps, all three of which this writer rewrites.
	source, err := release.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !atomicfs.SameDigest(source.Digest, rel.Digest) {
		t.Errorf("packing changed the release's identity:\n  directory %s\n  archive   %s",
			source.Digest, rel.Digest)
	}
}

// TestSourceDateEpochIsRefusedRatherThanIgnored.
//
// A pipeline that sets the variable is asking for a specific timestamp.
// Substituting a different one silently produces an archive that is
// reproducible by accident, which is the failure mode this whole property
// exists to remove.
func TestSourceDateEpochIsRefusedRatherThanIgnored(t *testing.T) {
	t.Setenv(release.SourceDateEpochEnv, "yesterday")

	if _, err := release.ArchiveModTime(time.Time{}); err == nil {
		t.Fatal("a SOURCE_DATE_EPOCH that is not a timestamp must be refused")
	}
}

// TestArchiveModTimeFallsBackToTheEpoch, not to now.
//
// A wall-clock default is what makes an archive differ from itself, and it
// looks like a real build date while being nothing of the sort.
func TestArchiveModTimeFallsBackToTheEpoch(t *testing.T) {
	// Empty rather than removed: LookupEnv reports it as set, so this also
	// pins that a variable exported as "" is treated as absent rather than
	// parsed as a timestamp and refused.
	t.Setenv(release.SourceDateEpochEnv, "")

	got, err := release.ArchiveModTime(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(time.Unix(0, 0).UTC()) {
		t.Errorf("mod time = %s, want the epoch", got)
	}

	// And the caller's suggestion wins over the epoch, which is the step a
	// simplifying refactor drops: it is the only one of the three with no
	// test of its own to fail.
	commit := time.Unix(1700000000, 0).UTC()
	got, err = release.ArchiveModTime(commit)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(commit) {
		t.Errorf("mod time = %s, want the supplied %s", got, commit)
	}
}

// archiveNames reads the entry names out of a tar.zst, in stream order.
func archiveNames(t *testing.T, path string) []string {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	zr, err := zstd.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()

	var names []string
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		// Distinguished from the end of the stream: a truncated archive
		// would otherwise return a short list and be reported as an
		// ordering failure, which is a confusing way to learn that the
		// writer produced something unreadable.
		if err != nil {
			t.Fatalf("cannot read the archive: %v", err)
		}
		names = append(names, hdr.Name)
	}
	return names
}

func writeFixture(t *testing.T, dir, rel, content string) {
	t.Helper()

	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
