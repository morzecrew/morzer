package blob

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// safeDestination is where a fetch decides what to write, driven by a manifest
// on a target -- which is a file this manager may not have written, because the
// whole premise of a target is that it is somewhere else.
//
// Tested here rather than through the port because the port cannot plant a
// hostile manifest on a target: a push refuses one, which is the other half of
// the defence and is covered by the contract suite. This half needs the
// function.
func TestSafeDestinationRefusesAnythingLeavingTheDestination(t *testing.T) {
	dest := filepath.Join("/var", "lib", "demo", "backups", "20260101T000000Z")

	for name, component := range map[string]string{
		"a parent reference":   "../../.ssh/authorized_keys",
		"one buried in a path": "nested/../../../etc/shadow",
		"a bare parent":        "..",
		"an absolute path":     "/etc/shadow",
		"an empty component":   "",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := safeDestination(dest, component)
			if err == nil {
				t.Fatalf("safeDestination(%q) = %q, and should have been refused: "+
					"whoever controls the target chooses where this machine writes",
					component, got)
			}
		})
	}
}

func TestSafeDestinationAcceptsWhatABackupActuallyContains(t *testing.T) {
	dest := filepath.Join("/var", "lib", "demo", "backups", "20260101T000000Z")

	for _, component := range []string{
		"database.sql.age",
		"secrets.sops.yaml.age",
		"files/uploads.tar.age", // a hook artifact in a subdirectory
		"./database.sql.age",    // harmless, and manifests in the wild have it
		// A backslash is a legal filename character on the only platform
		// this ships for, so this is one oddly-named file inside the
		// destination rather than an escape. Refusing it would refuse a
		// name a hook is entitled to produce.
		`weird\name.age`,
	} {
		got, err := safeDestination(dest, component)
		if err != nil {
			t.Fatalf("safeDestination(%q) was refused: %v", component, err)
		}
		if !strings.HasPrefix(got, dest+string(filepath.Separator)) {
			t.Errorf("safeDestination(%q) = %q, which is not under %q", component, got, dest)
		}
	}
}

// TestOnlyParentComponentsAreRefused. The rule every transport applies to a key
// out of a manifest, tested once, here, beside the contract it belongs to.
//
// It used to live twice -- once in the SFTP adapter and once in S3 -- and the
// two had already disagreed: a substring test for ".." rejected `notes..age` on
// one and accepted it on the other, so whether a backup could be restored
// depended on which transport had carried it.
func TestOnlyParentComponentsAreRefused(t *testing.T) {
	for key, want := range map[string]bool{
		"../.ssh/authorized_keys":     true,
		"../secrets":                  true,
		"id/../../etc/shadow":         true,
		"..":                          true,
		"notes..age":                  false,
		"database..dump":              false,
		"id/database.sql.age":         false,
		"id/nested..name/file.tar.gz": false,
	} {
		if got := HasParentComponent(key); got != want {
			t.Errorf("HasParentComponent(%q) = %v, want %v", key, got, want)
		}
	}
}

// TestListReportsATargetThatStoppedAnswering. A listing that dropped the
// manifests it could not read returned a short list and no error, and nothing
// downstream can tell a short list from a complete one: `backup list --remote`
// would hide a backup from the machine looking for it, and remote retention
// would prune from a view missing what it was counting.
func TestListReportsATargetThatStoppedAnswering(t *testing.T) {
	store := newMemStore()
	plantBackup(t, store, "20260101T000000Z", "20260101T000000Z")
	plantBackup(t, store, "20260102T000000Z", "20260102T000000Z")

	// The shape a connection dropped halfway through a listing has: the key
	// is there, reading it is not possible, and it is not "no such backup".
	store.getErr[path.Join("20260102T000000Z", ports.BackupManifestFileName)] =
		domain.BackupError(nil, "cannot reach the backup target")

	got, err := List(context.Background(), store)
	if err == nil {
		t.Fatalf("List returned %d backup(s) and no error after a manifest could not "+
			"be read; a short listing that reports success is what retention prunes from",
			len(got))
	}
}

// TestListSkipsAManifestThatWentDuringTheListing. The other half of the same
// rule: Remove deletes the manifest first, so a listing overlapping a removal
// sees a key whose object has already gone. That one is not a read failure and
// must not fail the listing.
func TestListSkipsAManifestThatWentDuringTheListing(t *testing.T) {
	store := newMemStore()
	plantBackup(t, store, "20260101T000000Z", "20260101T000000Z")
	plantBackup(t, store, "20260102T000000Z", "20260102T000000Z")
	store.getErr[path.Join("20260102T000000Z", ports.BackupManifestFileName)] = fs.ErrNotExist

	got, err := List(context.Background(), store)
	if err != nil {
		t.Fatalf("List failed because a manifest was removed while it ran: %v", err)
	}
	if len(got) != 1 || got[0].ID != "20260101T000000Z" {
		t.Fatalf("List = %v, want only 20260101T000000Z", ids(got))
	}
}

// TestListRefusesAManifestNamingAnotherBackup. The id in a listing is what
// every later operation resolves: retention removes it, fetch and verify read
// it. A manifest on a target is a file this manager may not have written, and
// one claiming another backup's id pointed all three at that other backup --
// so pruning the directory it sat in deleted a backup nobody asked it to touch.
func TestListRefusesAManifestNamingAnotherBackup(t *testing.T) {
	store := newMemStore()
	plantBackup(t, store, "20260101T000000Z", "20260101T000000Z")
	// A backup directory whose manifest names the one beside it.
	plantBackup(t, store, "20260102T000000Z", "20260101T000000Z")

	got, err := List(context.Background(), store)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, m := range got {
		if _, ok := store.objects[path.Join(m.ID, ports.BackupManifestFileName)]; !ok {
			t.Errorf("List reported id %q, which is not where the manifest was read from", m.ID)
		}
	}
	if len(got) != 1 {
		t.Fatalf("List = %v, want only the backup stored under its own id", ids(got))
	}
}

// TestListNamesABackupAfterTheDirectoryItIsIn. A manifest written before ids
// were recorded has none, and the directory is the only name it has.
func TestListNamesABackupAfterTheDirectoryItIsIn(t *testing.T) {
	store := newMemStore()
	plantBackup(t, store, "20260101T000000Z", "")

	got, err := List(context.Background(), store)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != "20260101T000000Z" {
		t.Fatalf("List = %v, want 20260101T000000Z", ids(got))
	}
}

// TestPushRefusesAComponentItCannotUpload. Dropping these silently published a
// manifest naming a component the push never uploaded: the backup listed on the
// target as if it were whole, and the failure waited for the fetch -- which is
// the run where finding out is too late.
func TestPushRefusesAComponentItCannotUpload(t *testing.T) {
	for name, componentPath := range map[string]string{
		"a component with no path":             "",
		"a component called like the manifest": ports.BackupManifestFileName,
	} {
		t.Run(name, func(t *testing.T) {
			local := writeLocalBackup(t, "20260101T000000Z", componentPath)
			store := newMemStore()

			_, err := Push(context.Background(), store, ports.TargetRef{},
				local, "20260101T000000Z")
			if err == nil {
				t.Fatalf("Push published a manifest naming a component at %q that it "+
					"did not upload; the target now holds a backup that lists and "+
					"cannot be fetched", componentPath)
			}
			if len(store.objects) != 0 {
				t.Errorf("Push refused the backup after writing %d object(s) to the "+
					"target; the refusal has to come before anything is published",
					len(store.objects))
			}
		})
	}
}

// memStore is the remote filesystem the choreography runs over, small enough
// for a test to plant exactly the state a target is in.
type memStore struct {
	objects map[string][]byte

	// getErr is what a read of one key fails with, which is the only way to
	// stage an unreachable target or a manifest that went mid-listing.
	getErr map[string]error
}

func newMemStore() *memStore {
	return &memStore{objects: map[string][]byte{}, getErr: map[string]error{}}
}

var _ Store = (*memStore)(nil)

func (m *memStore) Put(_ context.Context, key string, r io.Reader, _ int64) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.objects[key] = data
	return nil
}

func (m *memStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	if err, ok := m.getErr[key]; ok {
		return nil, err
	}
	data, ok := m.objects[key]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *memStore) Keys(_ context.Context, prefix string) ([]string, error) {
	var out []string
	for key := range m.objects {
		if strings.HasPrefix(key, prefix) {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (m *memStore) Delete(_ context.Context, key string) error {
	delete(m.objects, key)
	return nil
}

// plantBackup puts a manifest on the target under dir, recording id inside it.
// The two differ only for a backup this manager did not write.
func plantBackup(t *testing.T, s *memStore, dir, id string) {
	t.Helper()

	at, err := time.Parse("20060102T150405Z", dir)
	if err != nil {
		t.Fatalf("plantBackup: %v", err)
	}
	data, err := json.Marshal(ports.BackupManifest{
		SchemaVersion: 2,
		ID:            id,
		Product:       "demo",
		CreatedAt:     domain.NewTime(at),
	})
	if err != nil {
		t.Fatalf("plantBackup: %v", err)
	}
	s.objects[path.Join(dir, ports.BackupManifestFileName)] = data
}

// writeLocalBackup builds a backup directory whose manifest names one component
// at componentPath, with the file itself present under a name a push would
// accept.
func writeLocalBackup(t *testing.T, id, componentPath string) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("writeLocalBackup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "database.sql.age"),
		[]byte("ciphertext"), 0o600); err != nil {
		t.Fatalf("writeLocalBackup: %v", err)
	}

	at, err := time.Parse("20060102T150405Z", id)
	if err != nil {
		t.Fatalf("writeLocalBackup: %v", err)
	}
	data, err := json.Marshal(ports.BackupManifest{
		SchemaVersion: 2,
		ID:            id,
		Product:       "demo",
		CreatedAt:     domain.NewTime(at),
		Components: []ports.ComponentRecord{
			{Component: ports.ComponentDatabase, Path: componentPath, Size: 10},
		},
	})
	if err != nil {
		t.Fatalf("writeLocalBackup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ports.BackupManifestFileName), data, 0o600); err != nil {
		t.Fatalf("writeLocalBackup: %v", err)
	}
	return dir
}

func ids(manifests []ports.BackupManifest) []string {
	out := make([]string, len(manifests))
	for i, m := range manifests {
		out[i] = m.ID
	}
	return out
}
