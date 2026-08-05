package localdir

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// writeBackup lays down a backup directory the way the engine leaves one: the
// components, and the manifest that names them.
func writeBackup(t *testing.T, id string) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "backups", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte("ciphertext")
	if err := os.WriteFile(filepath.Join(dir, "database.sql.age"), body, 0o600); err != nil {
		t.Fatal(err)
	}

	data, err := json.Marshal(ports.BackupManifest{
		SchemaVersion: 2,
		ID:            id,
		Product:       "demo",
		CreatedAt:     domain.NewTime(time.Now().UTC()),
		Components: []ports.ComponentRecord{{
			Component: ports.ComponentDatabase,
			Path:      "database.sql.age",
			Size:      int64(len(body)),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ports.BackupManifestFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

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

// TestACopyAlreadyRunningIsAbandonedWhenTheOperationIs.
//
// The check at the top of Put only covers work that has not started. A push
// abandoned while a multi-gigabyte component is being written to a USB disk has
// to stop writing it, not finish and then be told it was cancelled.
func TestACopyAlreadyRunningIsAbandonedWhenTheOperationIs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := &dirStore{root: t.TempDir(), target: New()}
	r := &cancellingReader{cancel: cancel, chunks: 8}

	err := store.Put(ctx, "20260101T000000Z/database.sql.age", r, int64(r.chunks))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Put returned %v; the copy ran on after the operation was abandoned", err)
	}
	if r.reads > 2 {
		t.Errorf("the reader was read %d times after the cancellation", r.reads)
	}
	// And nothing half-written was left where a component belongs.
	if _, err := os.Stat(filepath.Join(store.root, "20260101T000000Z", "database.sql.age")); err == nil {
		t.Error("the abandoned copy was renamed into place anyway")
	}
}

// cancellingReader cancels the operation while the copy is in flight, then goes
// on offering bytes -- a stand-in for the component still arriving.
type cancellingReader struct {
	cancel context.CancelFunc
	chunks int
	reads  int
}

func (r *cancellingReader) Read(p []byte) (int, error) {
	r.reads++
	if r.chunks == 0 {
		return 0, io.EOF
	}
	r.chunks--
	r.cancel()
	p[0] = 'x'
	return 1, nil
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

// TestAWriteOrADeleteCannotBeSteeredOutOfTheTargetByASymlink, the same rule Get
// holds and for the same reason: the medium is not this deployment's to trust.
// A backup directory left behind as a link to /etc turns a push into a write
// over there and a prune into a deletion over there.
func TestAWriteOrADeleteCannotBeSteeredOutOfTheTargetByASymlink(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "20260101T000000Z")); err != nil {
		t.Fatal(err)
	}

	store := &dirStore{root: root, target: New()}

	if err := store.Put(ctx, "20260101T000000Z/database.sql.age",
		strings.NewReader("ciphertext"), 10); err == nil {
		t.Error("a push through a symlinked backup directory succeeded")
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the push wrote %d file(s) outside the target", len(entries))
	}

	// The delete side, which is the one retention runs unattended.
	victim := filepath.Join(outside, "database.sql.age")
	if err := os.WriteFile(victim, []byte("not yours"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = store.Delete(ctx, "20260101T000000Z/database.sql.age")
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("a prune deleted a file outside the target: %v", err)
	}
}

// TestASymlinkedTargetListsWhatWasPushedToIt.
//
// A symlink is an ordinary way to name a mounted medium, and writing through
// one always worked. Listing did not: the walk started at the link, saw
// something that was not a directory, and descended into nothing -- so the
// target accepted a backup and then reported itself empty.
func TestASymlinkedTargetListsWhatWasPushedToIt(t *testing.T) {
	ctx := context.Background()

	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "medium"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "offsite")
	if err := os.Symlink(filepath.Join(base, "medium"), link); err != nil {
		t.Fatal(err)
	}

	ref, err := ports.TargetURL("file://" + link)
	if err != nil {
		t.Fatal(err)
	}
	target := New()
	if _, err := target.Push(ctx, ref, writeBackup(t, "20260101T000000Z"), "20260101T000000Z"); err != nil {
		t.Fatalf("pushing to a symlinked target: %v", err)
	}

	manifests, err := target.List(ctx, ref)
	if err != nil {
		t.Fatalf("listing a symlinked target: %v", err)
	}
	if len(manifests) != 1 {
		t.Fatalf("the target listed %d backups after accepting one", len(manifests))
	}
}

// TestATargetInsideTheBackupItWouldHoldIsRefused.
//
// The refusal used to compare the backup directory with <root>/<id>, which does
// not exist yet on a first push -- so a target pointed at the backup itself was
// stat'd, found absent, accepted, and handed a nested copy of the backup inside
// the backup. An operator would be told they had an off-machine copy of
// something sitting on the same disk.
func TestATargetInsideTheBackupItWouldHoldIsRefused(t *testing.T) {
	const id = "20260101T000000Z"

	for name, inside := range map[string]string{
		"the backup directory itself": ".",
		"a directory under it":        "offsite",
	} {
		t.Run(name, func(t *testing.T) {
			local := writeBackup(t, id)
			before, err := os.ReadDir(local)
			if err != nil {
				t.Fatal(err)
			}

			ref, err := ports.TargetURL("file://" + filepath.Join(local, inside))
			if err != nil {
				t.Fatal(err)
			}

			_, err = New().Push(context.Background(), ref, local, id)
			if err == nil {
				t.Fatal("a target inside the backup directory was accepted")
			}
			if hint := domain.AsError(err).Hint; !strings.Contains(hint, "disk failing") {
				t.Errorf("the refusal reads as something else: %v (hint %q)", err, hint)
			}

			after, err := os.ReadDir(local)
			if err != nil {
				t.Fatal(err)
			}
			if len(after) != len(before) {
				t.Errorf("the refused push left %d entries in the backup directory, was %d",
					len(after), len(before))
			}
		})
	}
}
