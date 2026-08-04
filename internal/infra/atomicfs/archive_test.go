package atomicfs_test

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
