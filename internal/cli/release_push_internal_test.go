package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/release"
)

// TestThePushedArchiveIsTheCheckedOne. The checks run on what was extracted,
// and the upload reads an archive again; reading the vendor's path twice would
// let a file replaced in between be published under the checked content digest.
// So the archive is copied once, privately, and both read the copy.
func TestThePushedArchiveIsTheCheckedOne(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "demo-1.2.0.tar.zst")
	if err := release.WriteArchive(filepath.Join(wd, "..", "..", "testdata", "bundle"), archive, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	checked, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}

	_, pushed, cleanup, err := openArchive(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	if pushed == archive {
		t.Fatal("the archive to push is the vendor's path, read a second time")
	}
	if filepath.Base(pushed) != filepath.Base(archive) {
		t.Errorf("the copy is named %s; the layer's title is the archive's file name", filepath.Base(pushed))
	}

	if err := os.WriteFile(archive, []byte("replaced after the checks"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(pushed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, checked) {
		t.Error("replacing the archive after the checks changed what would be pushed")
	}

	cleanup()
	if _, err := os.Stat(pushed); !os.IsNotExist(err) {
		t.Errorf("the private copy outlived the push: %v", err)
	}
}
