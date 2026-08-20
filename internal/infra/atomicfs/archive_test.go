package atomicfs_test

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/morzecrew/morzer/internal/infra/atomicfs"
)

// Extraction is where a hostile bundle gets its one chance. Everything below
// is an archive built to trip exactly one refusal, because a single "malformed
// archive" fixture proves only that the first check works.
//
// Refused, never skipped: extracting the good half of a hostile archive
// produces a release that behaves differently from the one that was verified.
//
// **One refusal is not covered here.** `extractFile` bounds the copy as well as
// checking the declared size, because a tar header can claim one byte and the
// stream can carry a gigabyte. `archive/tar` will not write such an archive --
// it refuses with "write too long" -- so building the fixture would mean
// hand-rolling tar records, and the bound is left asserted by reading rather
// than by running. The declared-size check above it, and the total-size bound
// below it, are both covered.

// entry is one tar record, as unreasonable as the test needs it to be.
type entry struct {
	name string
	body string
	typ  byte
	mode int64
	link string
}

// archive builds a tar.zst in memory.
func archive(t *testing.T, entries ...entry) string {
	t.Helper()

	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		size := int64(len(e.body))
		hdr := &tar.Header{
			Name: e.name, Typeflag: typ, Mode: mode, Size: size, Linkname: e.link,
		}
		if typ != tar.TypeReg {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	return compress(t, raw.Bytes(), "bundle.tar.zst")
}

func compress(t *testing.T, data []byte, name string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := zstd.NewWriter(f)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAWellFormedArchiveExtracts(t *testing.T) {
	src := archive(t,
		entry{name: "manifest.yaml", body: "api_version: v1\n"},
		entry{name: "hooks", typ: tar.TypeDir},
		entry{name: "hooks/migrate", body: "#!/bin/sh\n", mode: 0o755},
	)
	dst := filepath.Join(t.TempDir(), "out")

	if err := atomicfs.ExtractTarZst(src, dst, atomicfs.DefaultExtractLimits()); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dst, "manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "api_version: v1\n" {
		t.Errorf("contents = %q", data)
	}

	info, err := os.Stat(filepath.Join(dst, "hooks", "migrate"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("the executable bit was lost: %04o", info.Mode().Perm())
	}
	// Normalised, not the archive's: a bundle claiming 0777 must not
	// produce a world-writable file under /opt.
	if info.Mode().Perm()&0o022 != 0 {
		t.Errorf("mode %04o is group- or world-writable", info.Mode().Perm())
	}
}

func TestArchiveModesAreNormalisedNotTrusted(t *testing.T) {
	src := archive(t,
		entry{name: "wide-open", body: "x", mode: 0o777},
		entry{name: "unreadable", body: "x", mode: 0o000},
	)
	dst := filepath.Join(t.TempDir(), "out")

	if err := atomicfs.ExtractTarZst(src, dst, atomicfs.DefaultExtractLimits()); err != nil {
		t.Fatal(err)
	}

	for name, wantExec := range map[string]bool{"wide-open": true, "unreadable": false} {
		info, err := os.Stat(filepath.Join(dst, name))
		if err != nil {
			t.Fatal(err)
		}
		perm := info.Mode().Perm()
		if perm&0o022 != 0 {
			t.Errorf("%s is mode %04o, which is writable by somebody else", name, perm)
		}
		if got := perm&0o100 != 0; got != wantExec {
			t.Errorf("%s: executable = %v, want %v (mode %04o)", name, got, wantExec, perm)
		}
		if perm&0o400 == 0 {
			t.Errorf("%s is mode %04o, which its own owner cannot read", name, perm)
		}
	}
}

// TestEveryWayAnArchiveCanBeHostileIsRefused. One archive per refusal: a
// single malformed fixture would prove only that the first check works.
func TestEveryWayAnArchiveCanBeHostileIsRefused(t *testing.T) {
	cases := map[string]struct {
		entries []entry
		limits  atomicfs.ExtractLimits
		want    string
	}{
		"a path that escapes the bundle": {
			[]entry{{name: "../../etc/cron.d/backdoor", body: "* * * * * root sh\n"}},
			atomicfs.DefaultExtractLimits(), "escapes the bundle",
		},
		"an absolute path": {
			[]entry{{name: "/etc/shadow", body: "x"}},
			atomicfs.DefaultExtractLimits(), "absolute path",
		},
		"a symlink": {
			[]entry{{name: "sneaky", typ: tar.TypeSymlink, link: "/etc/shadow"}},
			atomicfs.DefaultExtractLimits(), "link",
		},
		"a hard link": {
			[]entry{{name: "sneaky", typ: tar.TypeLink, link: "manifest.yaml"}},
			atomicfs.DefaultExtractLimits(), "link",
		},
		"a device node": {
			[]entry{{name: "dev/null", typ: tar.TypeChar}},
			atomicfs.DefaultExtractLimits(), "non-regular",
		},
		"a fifo": {
			[]entry{{name: "pipe", typ: tar.TypeFifo}},
			atomicfs.DefaultExtractLimits(), "non-regular",
		},
		"too many entries": {
			[]entry{{name: "a", body: "1"}, {name: "b", body: "2"}, {name: "c", body: "3"}},
			atomicfs.ExtractLimits{MaxEntries: 2}, "entry limit",
		},
		"one entry declaring more than the per-file limit": {
			[]entry{{name: "big", body: strings.Repeat("x", 100)}},
			atomicfs.ExtractLimits{MaxFileSize: 10}, "per-file limit",
		},
		"more in total than the limit allows": {
			[]entry{
				{name: "a", body: strings.Repeat("x", 60)},
				{name: "b", body: strings.Repeat("y", 60)},
			},
			atomicfs.ExtractLimits{MaxTotalSize: 100}, "",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			src := archive(t, tc.entries...)
			dst := filepath.Join(t.TempDir(), "out")

			err := atomicfs.ExtractTarZst(src, dst, tc.limits)
			if err == nil {
				t.Fatalf("%s was extracted", name)
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not say %q: %v", tc.want, err)
			}

			// And nothing escaped while it was being refused.
			if _, statErr := os.Stat("/etc/cron.d/backdoor"); statErr == nil {
				t.Fatal("the extractor wrote outside its destination")
			}
		})
	}
}

func TestArchivesThatAreNotArchives(t *testing.T) {
	dir := t.TempDir()

	notZstd := filepath.Join(dir, "bundle.tar.zst")
	if err := os.WriteFile(notZstd, []byte("this is not compressed"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Valid zstd, but the stream inside it is not a tar.
	notTar := compress(t, []byte("hello, not a tar"), "other.tar.zst")

	// A tar cut in half, which is what an interrupted download leaves.
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	if err := tw.WriteHeader(&tar.Header{
		Name: "manifest.yaml", Size: 1000, Mode: 0o644,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("short")); err != nil {
		t.Fatal(err)
	}
	truncated := compress(t, raw.Bytes(), "truncated.tar.zst")

	for name, src := range map[string]string{
		"not compressed at all": notZstd,
		"compressed, not a tar": notTar,
		"truncated mid-entry":   truncated,
	} {
		t.Run(name, func(t *testing.T) {
			err := atomicfs.ExtractTarZst(src, filepath.Join(t.TempDir(), "out"),
				atomicfs.DefaultExtractLimits())
			if err == nil {
				t.Fatalf("%s was extracted successfully", name)
			}
		})
	}
}

func TestExtractingSomethingThatIsNotThere(t *testing.T) {
	err := atomicfs.ExtractTarZst(filepath.Join(t.TempDir(), "gone.tar.zst"),
		filepath.Join(t.TempDir(), "out"), atomicfs.DefaultExtractLimits())
	if err == nil {
		t.Fatal("an archive that does not exist was extracted")
	}
}

func TestIsTarZst(t *testing.T) {
	for _, yes := range []string{"a.tar.zst", "/opt/x/bundle.tar.zst", "B.TAR.ZST"} {
		if !atomicfs.IsTarZst(yes) {
			t.Errorf("IsTarZst(%q) = false", yes)
		}
	}
	for _, no := range []string{"a.tar.gz", "a.tar", "a.zst", "bundle", "a.tar.zst.sig"} {
		if atomicfs.IsTarZst(no) {
			t.Errorf("IsTarZst(%q) = true", no)
		}
	}
}

// TestTheArchiveRootEntryIsSkippedNotRefused. `tar czf x .` writes a "./"
// entry, and refusing it would refuse every archive made the obvious way.
func TestTheArchiveRootEntryIsSkippedNotRefused(t *testing.T) {
	src := archive(t,
		entry{name: "./", typ: tar.TypeDir},
		entry{name: "./manifest.yaml", body: "api_version: v1\n"},
	)
	dst := filepath.Join(t.TempDir(), "out")

	if err := atomicfs.ExtractTarZst(src, dst, atomicfs.DefaultExtractLimits()); err != nil {
		t.Fatalf("an archive created by `tar czf x .` was refused: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "manifest.yaml")); err != nil {
		t.Errorf("the entry under ./ did not arrive: %v", err)
	}
}

// TestWriteTarZstRefusesWhatExtractionWouldRefuse.
//
// Two guards, and writing the test found they are not the one guard the code
// reads like. The containment guard fires first and on a different case than
// the format guard, so asserting one message for both would have pinned
// whichever happened to run.
//
// Tested here rather than through `release archive`, where neither is
// reachable: DigestTree refuses a non-regular file first, so a bundle carrying
// one never gets as far as being packed. The guards are backstops, and a test
// in the wrong package would be asserting something else's behaviour.
func TestWriteTarZstRefusesWhatExtractionWouldRefuse(t *testing.T) {
	// The containment guard: os.Root refuses a symlink that leaves the
	// tree at the syscall, before anything asks what kind of file it is.
	t.Run("a symlink out of the bundle", func(t *testing.T) {
		dir := t.TempDir()
		writeArchiveFixture(t, dir, "manifest.yaml")
		if err := os.Symlink("/etc/shadow", filepath.Join(dir, "link")); err != nil {
			t.Skipf("cannot create a symlink here: %v", err)
		}

		out := filepath.Join(t.TempDir(), "out.tar.zst")
		err := atomicfs.WriteTarZst(out, dir, []string{"manifest.yaml", "link"}, time.Unix(0, 0))
		if err == nil {
			t.Fatal("a symlink pointing out of the bundle was packed")
		}
		if !strings.Contains(err.Error(), "escapes") {
			t.Errorf("the refusal should name the containment failure: %v", err)
		}
		assertNoArchiveLeftBehind(t, out)
	})

	// The format guard: anything that opens but is not a regular file.
	// Extraction refuses these, so writing one produces an archive this
	// manager cannot install -- and the vendor finds out from a customer.
	t.Run("something that is not a regular file", func(t *testing.T) {
		dir := t.TempDir()
		writeArchiveFixture(t, dir, "manifest.yaml")
		if err := os.Mkdir(filepath.Join(dir, "compose"), 0o755); err != nil {
			t.Fatal(err)
		}

		out := filepath.Join(t.TempDir(), "out.tar.zst")
		err := atomicfs.WriteTarZst(out, dir, []string{"manifest.yaml", "compose"}, time.Unix(0, 0))
		if err == nil {
			t.Fatal("a directory was packed as a file entry")
		}
		if !strings.Contains(err.Error(), "regular file") {
			t.Errorf("the refusal should say why: %v", err)
		}
		assertNoArchiveLeftBehind(t, out)
	})
}

func writeArchiveFixture(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
		t.Fatal(err)
	}
}

// assertNoArchiveLeftBehind: a refused archive must leave neither a truncated
// file that looks finished nor the temporary it was streaming into.
func assertNoArchiveLeftBehind(t *testing.T, out string) {
	t.Helper()

	if _, err := os.Stat(out); err == nil {
		t.Error("a refused archive left a partial file in place")
	}
	entries, err := os.ReadDir(filepath.Dir(out))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".partial") {
			t.Errorf("a refused archive left its temporary file %s behind", e.Name())
		}
	}
}

// ReadFirstArchiveEntry's refusals, which are the whole of what it adds.
//
// Every branch here runs only when the input is malformed, so none of them is
// reached by a passing bundle — which is exactly why they need tests rather
// than the happy path does. A refusal that panics or returns a useless message
// is discovered by the first vendor with a truncated download.
func TestReadFirstArchiveEntryRefusals(t *testing.T) {
	// Not through the branch that names zstd, and that is worth pinning
	// rather than assuming. `zstd.NewReader` is lazy: it accepts the file
	// and the magic-number mismatch surfaces at the first read, so the
	// "not a valid zstd archive" message is unreachable for a file that
	// simply is not one. The refusal a vendor actually meets is the read's,
	// and it still has to be intelligible.
	t.Run("not a zstd archive", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "nope.tar.zst")
		if err := os.WriteFile(path, []byte("this is not compressed"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := atomicfs.ReadFirstArchiveEntry(path, "manifest.yaml")
		if err == nil {
			t.Fatal("a file that is not an archive must be refused")
		}
		if !strings.Contains(err.Error(), "cannot read") {
			t.Errorf("the refusal must name the file it could not read: %v", err)
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("and which file it was: %v", err)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		_, err := atomicfs.ReadFirstArchiveEntry(
			filepath.Join(t.TempDir(), "absent.tar.zst"), "manifest.yaml")
		if err == nil {
			t.Fatal("a path that is not there must be refused")
		}
	})

	t.Run("no entries at all", func(t *testing.T) {
		empty := t.TempDir()
		src := filepath.Join(empty, "src")
		if err := os.MkdirAll(src, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(empty, "empty.tar.zst")
		if err := atomicfs.WriteTarZst(path, src, nil, time.Unix(0, 0)); err != nil {
			t.Fatal(err)
		}
		_, err := atomicfs.ReadFirstArchiveEntry(path, "manifest.yaml")
		if err == nil {
			t.Fatal("an archive with no entries must be refused")
		}
		if !strings.Contains(err.Error(), "no entries") {
			t.Errorf("the refusal must name the emptiness: %v", err)
		}
	})

	t.Run("an entry larger than the bound", func(t *testing.T) {
		dir := t.TempDir()
		// The bound exists because this read happens before anything has
		// established what the archive is, so an archive could otherwise
		// declare its own budget in a file large enough to be the attack.
		big := make([]byte, (1<<20)+1)
		if err := os.WriteFile(filepath.Join(dir, "manifest.yaml"), big, 0o644); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "big.tar.zst")
		if err := atomicfs.WriteTarZst(path, dir, []string{"manifest.yaml"}, time.Unix(0, 0)); err != nil {
			t.Fatal(err)
		}
		_, err := atomicfs.ReadFirstArchiveEntry(path, "manifest.yaml")
		if err == nil {
			t.Fatal("an oversized first entry must be refused rather than read")
		}
		if !strings.Contains(err.Error(), "larger than") {
			t.Errorf("the refusal must say it was too large: %v", err)
		}
	})

	t.Run("the happy path still works", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte("api_version: v1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "ok.tar.zst")
		if err := atomicfs.WriteTarZst(path, dir, []string{"manifest.yaml"}, time.Unix(0, 0)); err != nil {
			t.Fatal(err)
		}
		data, err := atomicfs.ReadFirstArchiveEntry(path, "manifest.yaml")
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "api_version: v1\n" {
			t.Errorf("content = %q", data)
		}
	})
}
