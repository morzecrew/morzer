package localdir

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The round trip is proved by the contract suite, which runs this adapter as
// the reference implementation of the port. What is here is the part a
// directory on a working disk never reaches.

// TestACancelledContextStopsNewWork, the same rule the SFTP adapter holds.
//
// A store that ignores the context runs past the deadline the engine set for
// it: a push that is meant to be abandoned goes on copying gigabytes onto a
// USB disk while the operation that owned it has already been reported as
// failed.
func TestACancelledContextStopsNewWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	store := &dirStore{root: t.TempDir(), target: New()}

	if err := store.Put(ctx, "id/db.age", strings.NewReader("x"), 1); !errors.Is(err, context.Canceled) {
		t.Errorf("Put under a cancelled context: %v", err)
	}
	if _, err := store.Get(ctx, "id/db.age"); !errors.Is(err, context.Canceled) {
		t.Errorf("Get under a cancelled context: %v", err)
	}
	if _, err := store.Keys(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Errorf("Keys under a cancelled context: %v", err)
	}
	if err := store.Delete(ctx, "id/db.age"); !errors.Is(err, context.Canceled) {
		t.Errorf("Delete under a cancelled context: %v", err)
	}

	// And nothing was written on the way to refusing.
	entries, err := os.ReadDir(store.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a cancelled push left %d entries behind", len(entries))
	}
}

// TestAMissingComponentIsReportedAsMissing, unwrapped, because blob tells "no
// such backup" from "the medium is gone" by asking errors.Is -- and answers an
// operator very differently depending on which it is.
func TestAMissingComponentIsReportedAsMissing(t *testing.T) {
	store := &dirStore{root: t.TempDir(), target: New()}

	_, err := store.Get(context.Background(), "20260101T000000Z/db.age")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a component that is not there reported %v, which reads as an "+
			"unreachable target", err)
	}
}

// TestGetRefusesASymlinkOutOfTheTarget.
//
// A target is somewhere this deployment does not control, so whoever owns the
// medium can point a component at /etc/shadow and have a fetch read it into the
// backup. The lexical check cannot see that; opening through os.Root can.
func TestGetRefusesASymlinkOutOfTheTarget(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("not yours"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "20260101T000000Z"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "20260101T000000Z", "db.age")); err != nil {
		t.Fatal(err)
	}

	store := &dirStore{root: root, target: New()}
	f, err := store.Get(context.Background(), "20260101T000000Z/db.age")
	if err == nil {
		_ = f.Close()
		t.Fatal("a component symlinked out of the target was opened")
	}
}
