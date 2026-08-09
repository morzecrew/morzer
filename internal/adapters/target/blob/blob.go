// Package blob holds the choreography every backup target shares, over the
// smallest remote filesystem a target actually needs.
//
// The three adapters differ only in how bytes move: a directory copy, an SFTP
// stream, a bucket PUT. Everything that makes a pushed backup *correct* is the
// same in all three, and it is the part that is easy to get subtly wrong:
//
//   - the manifest is written last, so a push interrupted halfway leaves a
//     directory that List does not report and nobody can restore;
//   - the manifest is deleted first, so a removal interrupted halfway leaves
//     the same;
//   - a re-push of the same backup overwrites rather than duplicating, because
//     retention counts backups and a second copy under a second name would
//     make the count wrong.
//
// Writing that three times would be three chances to get it wrong, and the
// contract suite would only catch it on whichever adapter a developer
// remembered to run.
package blob

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
)

// Store is the minimal remote filesystem a backup target needs.
//
// Keys are slash-separated and relative to the target's own root, so an adapter
// joins them onto a directory or a bucket prefix as it likes. Nothing here
// knows about backups.
type Store interface {
	// Put writes size bytes from r at key, replacing whatever was there.
	Put(ctx context.Context, key string, r io.Reader, size int64) error

	// Get opens key for reading. A key that is not there must report an
	// error satisfying errors.Is(err, fs.ErrNotExist), because "no such
	// backup" and "the bucket is unreachable" send an operator to
	// different places.
	Get(ctx context.Context, key string) (io.ReadCloser, error)

	// Keys lists everything under prefix, recursively, as keys relative to
	// the store root rather than to the prefix.
	Keys(ctx context.Context, prefix string) ([]string, error)

	// Delete removes key. Removing something already absent is not an
	// error: Remove is retried after a partial failure.
	Delete(ctx context.Context, key string) error
}

// HasParentComponent reports whether any element of a key is "..".
//
// Every store resolves keys that arrived in a manifest, and a manifest on a
// target is a file this manager may not have written: `../../.ssh/authorized_keys`
// as a component name must not be a way for whoever controls the target to
// decide what a fetch reads or a removal deletes.
//
// It lives beside the contract rather than once per transport because the two
// copies had already disagreed once. A substring test for ".." rejected
// `notes..age` on one adapter and accepted it on another, which made a backup
// restorable or not depending on which transport had happened to carry it.
func HasParentComponent(key string) bool {
	for _, part := range strings.Split(path.Clean(key), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

// Push copies a local backup directory to the store.
//
// Two rules, and both of them matter more than the transport underneath.
//
// The manifest goes last, so a push interrupted halfway leaves a directory that
// List does not report and nobody can restore.
//
// What goes at all is decided by the manifest, not by walking the directory. A
// backup directory can hold files the backup does not contain: an interrupted
// restore leaves a `.restore-*` staging directory of *decrypted* components
// beside the encrypted ones, and a push that copied everything it found would
// take a plaintext database dump to a bucket. Naming the components is the same
// rule Fetch follows, and it fails closed in both directions.
func Push(ctx context.Context, s Store, ref ports.TargetRef, localDir, id string) (ports.RemoteRef, error) {
	if id == "" {
		return ports.RemoteRef{}, domain.Internal(nil, "a backup was pushed with no id")
	}

	manifestPath := filepath.Join(localDir, ports.BackupManifestFileName)
	manifest, err := readLocalManifest(manifestPath)
	if err != nil {
		return ports.RemoteRef{}, err
	}

	names := make([]string, 0, len(manifest.Components))
	for _, c := range manifest.Components {
		// Refused rather than skipped. Skipping them published a
		// manifest naming a component the push had not uploaded: the
		// backup listed on the target as if it were whole and failed
		// at the fetch, which is the run where finding out is too
		// late.
		if c.Path == "" {
			return ports.RemoteRef{}, domain.BackupError(nil,
				"the backup names a component with no path").
				WithHint("this backup was not written by this manager; do not push it")
		}
		if c.Path == ports.BackupManifestFileName {
			return ports.RemoteRef{}, domain.BackupError(nil,
				"the backup names a component called %s, which is the manifest's own name",
				ports.BackupManifestFileName).
				WithHint("the component would be overwritten by the manifest; " +
					"check what the backup hook wrote")
		}
		names = append(names, c.Path)
	}
	// Sorted so a failed push fails at the same file twice, which is what
	// makes one reproducible from a log.
	sort.Strings(names)

	for _, name := range names {
		// The same guard Fetch applies, for the same reason and in the
		// other direction: a manifest is data, and a component path that
		// leaves the backup directory would read a file the backup does
		// not contain and send it somewhere else.
		local, err := safeDestination(localDir, name)
		if err != nil {
			return ports.RemoteRef{}, err
		}
		size, err := regularFileSize(local)
		if err != nil {
			return ports.RemoteRef{}, err
		}
		if err := putFile(ctx, s, path.Join(id, name), local, size); err != nil {
			return ports.RemoteRef{}, err
		}
	}

	size, err := regularFileSize(manifestPath)
	if err != nil {
		return ports.RemoteRef{}, err
	}
	if err := putFile(ctx, s, path.Join(id, ports.BackupManifestFileName), manifestPath, size); err != nil {
		return ports.RemoteRef{}, err
	}

	return ports.RemoteRef{Target: ref, ID: id}, nil
}

// List reads every manifest on the target, newest first.
//
// Only manifests are transferred. A directory without a manifest is skipped
// rather than reported: it is a push that was interrupted or a removal in
// flight, and `backup list` has to stay usable while either is happening.
//
// A manifest that is there and cannot be read is not the same thing, and used
// to be treated as if it were. A target that stopped answering halfway through
// a listing then produced a short list and no error, and nothing downstream can
// tell a short list from a complete one: `backup list --remote` hides backups
// on the machine most likely to be looking for them, `backup fetch` with no id
// picks "the newest" out of whatever happened to be readable, and remote
// retention prunes from a view missing the backups it was counting.
func List(ctx context.Context, s Store) ([]ports.BackupManifest, error) {
	keys, err := s.Keys(ctx, "")
	if err != nil {
		return nil, err
	}

	var out []ports.BackupManifest
	for _, key := range keys {
		id, name := path.Split(key)
		if name != ports.BackupManifestFileName {
			continue
		}
		id = strings.Trim(id, "/")
		if id == "" || strings.Contains(id, "/") {
			// A manifest at the root, or nested two deep, is not a
			// backup this manager wrote.
			continue
		}

		manifest, err := readManifest(ctx, s, key)
		if err != nil {
			// Not-found is the one case that is not a failure to
			// read: Remove deletes the manifest first, so a listing
			// that overlaps a removal sees a key whose object has
			// already gone.
			if errors.Is(err, domain.ErrNotFound) {
				continue
			}
			return nil, err
		}

		switch manifest.ID {
		case id:
		case "":
			manifest.ID = id
		default:
			// Skipped, because the id in a listing is what every
			// later operation resolves: retention removes it, fetch
			// and verify read it. Trusting an id that names a
			// different directory pointed all three at that other
			// backup -- so pruning this one deleted the other, which
			// is how retention takes a backup nobody asked it to
			// touch. A manifest this manager wrote always sits under
			// its own id.
			continue
		}
		out = append(out, manifest)
	}

	// Newest first, breaking ties by id descending.
	//
	// The tie-break is not cosmetic. Retention deletes everything past the
	// keep count, so an unstable order among equal timestamps means
	// retention picks a different backup to delete on every run -- and the
	// one it keeps is not necessarily the newest. Ids are timestamps, so
	// descending id is the same ordering by another route.
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt.Time) {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt.Time)
	})
	return out, nil
}

// Fetch copies one backup down into destDir.
//
// Driven by the manifest rather than by a listing: a file on the target that the
// manifest does not name was not part of the backup, and copying it down would
// hand a restore something nobody checksummed.
func Fetch(ctx context.Context, s Store, ref ports.RemoteRef, destDir string) error {
	manifestKey := path.Join(ref.ID, ports.BackupManifestFileName)

	manifest, err := readManifest(ctx, s, manifestKey)
	if err != nil {
		return err
	}

	if err := atomicfs.MkdirAll(destDir, 0o700); err != nil {
		return err
	}

	for _, c := range manifest.Components {
		// Checked here rather than only in each store's key resolution.
		//
		// The stores do refuse a `..` key, so this was never reachable --
		// but the guard that mattered was on the *read* side, and what a
		// hostile component path actually decides is where the **write**
		// goes. Depending on three adapters to each remember a rule that
		// protects a fourth one's caller is how the fourth adapter ships
		// without it.
		local, err := safeDestination(destDir, c.Path)
		if err != nil {
			return err
		}
		if err := getFile(ctx, s, path.Join(ref.ID, c.Path), local); err != nil {
			return err
		}
	}
	// Manifest last here too, so an interrupted fetch leaves a local
	// directory that `backup list` skips rather than one it offers.
	return getFile(ctx, s, manifestKey, filepath.Join(destDir, ports.BackupManifestFileName))
}

// FetchFile copies one named file out of a remote backup, with its manifest.
//
// The manifest is not an extra: it is what lets the caller tell whether the
// file it just fetched belongs to the backup it asked for. Fetching the file
// alone would be fetching whatever sits at that key.
// The manifest lands *first* here, which is the opposite of Fetch and for a
// reason worth stating: the caller needs it to decide whether the file it is
// about to receive belongs to this backup, and reading it off the target twice
// -- once to check, once to keep -- would make the byte count this method
// exists to minimise depend on how the check was implemented.
//
// The cost is that an interrupted FetchFile leaves a manifest with no
// component, so destDir must be staging the caller owns rather than the backup
// store. Fetch writes into the store and orders itself the other way for
// exactly that reason.
func FetchFile(ctx context.Context, s Store, ref ports.RemoteRef, name, destDir string) error {
	if err := atomicfs.MkdirAll(destDir, 0o700); err != nil {
		return err
	}

	// Read into memory under the same bound readManifest applies, then
	// written down. Fetching it to disk first would leave the size of a
	// hostile target's answer deciding how much of this machine's disk it
	// gets -- and the parse that follows would read all of it back.
	manifest, err := readManifest(ctx, s, path.Join(ref.ID, ports.BackupManifestFileName))
	if err != nil {
		return err
	}

	// Only a file the manifest names. A caller asking for anything else is
	// asking for a key rather than a component, and answering would make
	// this a general remote-read primitive that happens to be pointed at a
	// backup store.
	var known bool
	for _, c := range manifest.Components {
		if c.Path == name {
			known = true
			break
		}
	}
	if !known {
		return domain.BackupError(domain.ErrNotFound,
			"backup %s has no component %q", ref.ID, name)
	}

	local, err := safeDestination(destDir, name)
	if err != nil {
		return err
	}
	if err := getFile(ctx, s, path.Join(ref.ID, name), local); err != nil {
		return err
	}

	// The manifest is written from the bytes already validated as a
	// manifest, so the caller reads the same document this function
	// checked the component name against.
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return domain.Internal(err, "cannot serialise the backup manifest")
	}
	return atomicfs.WriteFile(
		filepath.Join(destDir, ports.BackupManifestFileName), encoded, 0o600)
}

// Verify reads a backup back off the target and checks its checksums.
//
// A full transfer, which is the honest cost of the claim: a backup nobody has
// read back is a hope, and that sentence does not stop being true because the
// backup is in a bucket. Nothing is written to disk -- each component is
// streamed through a digest and discarded -- so this costs bandwidth and no
// storage, and needs no key: the checksums are of the stored bytes.
func Verify(ctx context.Context, s Store, ref ports.RemoteRef) error {
	manifest, err := readManifest(ctx, s, path.Join(ref.ID, ports.BackupManifestFileName))
	if err != nil {
		return err
	}

	var problems []string
	for _, c := range manifest.Components {
		size, sum, err := digest(ctx, s, path.Join(ref.ID, c.Path))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				problems = append(problems, c.Path+": missing")
				continue
			}
			return err
		}
		switch {
		case c.Size > 0 && size != c.Size:
			problems = append(problems, fmt.Sprintf("%s: size is %d, manifest says %d",
				c.Path, size, c.Size))
		case c.SHA256 != "" && !atomicfs.SameDigest(sum, c.SHA256):
			problems = append(problems, c.Path+": checksum mismatch")
		}
	}

	if len(problems) > 0 {
		return domain.BackupError(domain.ErrDigestMismatch,
			"backup %s on %s failed verification:\n  - %s",
			manifest.ID, ref.Target, strings.Join(problems, "\n  - ")).
			WithHint("the copy on the target cannot be trusted for a restore; " +
				"push a fresh backup, and check the local copy too")
	}
	return nil
}

// digest streams one object through SHA-256 without keeping it.
func digest(ctx context.Context, s Store, key string) (int64, string, error) {
	r, err := s.Get(ctx, key)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = r.Close() }()

	h := sha256.New()
	size, err := io.Copy(h, r)
	if err != nil {
		return 0, "", domain.BackupError(err, "cannot read %s from the target", key)
	}
	return size, ports.DigestString("sha256", h.Sum(nil)), nil
}

// Remove deletes one backup, manifest first.
//
// The reverse of Push for the same reason: an interrupted removal must leave
// something nothing will restore from, rather than a backup missing one
// component that verification would only catch after the restore began.
func Remove(ctx context.Context, s Store, ref ports.RemoteRef) error {
	if ref.ID == "" {
		return domain.Internal(nil, "a backup removal named no backup")
	}

	if err := s.Delete(ctx, path.Join(ref.ID, ports.BackupManifestFileName)); err != nil {
		return err
	}

	keys, err := s.Keys(ctx, ref.ID+"/")
	if err != nil {
		return err
	}
	for _, key := range keys {
		if err := s.Delete(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

func readManifest(ctx context.Context, s Store, key string) (ports.BackupManifest, error) {
	r, err := s.Get(ctx, key)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ports.BackupManifest{}, domain.BackupError(domain.ErrNotFound,
				"there is no backup %s on this target", path.Dir(key)).
				WithHint("run `morzer backup list --target <url>` to see what is there")
		}
		return ports.BackupManifest{}, err
	}
	defer func() { _ = r.Close() }()

	// A manifest is kilobytes. The bound is here so a target that answers
	// with something else cannot be used to exhaust this machine's memory.
	data, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return ports.BackupManifest{}, domain.BackupError(err, "cannot read %s from the target", key)
	}

	var m ports.BackupManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return ports.BackupManifest{}, domain.BackupError(err,
			"%s on the target is not a valid backup manifest", key)
	}
	return m, nil
}

func putFile(ctx context.Context, s Store, key, localPath string, size int64) error {
	f, err := os.Open(localPath) //nolint:gosec // a path under the manager's own backup directory
	if err != nil {
		return domain.BackupError(err, "cannot read %s", localPath)
	}
	defer func() { _ = f.Close() }()

	if err := s.Put(ctx, key, f, size); err != nil {
		return err
	}
	return nil
}

func getFile(ctx context.Context, s Store, key, localPath string) error {
	r, err := s.Get(ctx, key)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return domain.BackupError(domain.ErrNotFound,
				"the backup on this target is missing %s", path.Base(key)).
				WithHint("the push that wrote it did not finish; take a fresh backup")
		}
		return err
	}
	defer func() { _ = r.Close() }()

	if dir := filepath.Dir(localPath); dir != "" {
		if err := atomicfs.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}

	// 0600 and created before anything is written: a fetched backup holds
	// the same ciphertext the local one does and is never briefly readable
	// by anyone else.
	f, err := os.OpenFile(localPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) //nolint:gosec // caller-owned destination
	if err != nil {
		return domain.BackupError(err, "cannot create %s", localPath)
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return domain.BackupError(err, "cannot write %s", localPath)
	}
	// Flushed before the close, the way localdir already flushes what it
	// writes. atomicfs holds this contract for the files it writes and leaves
	// file contents to whoever wrote them -- this is a writer, and the bytes
	// it is writing are the copy an operator fetched because they are about to
	// need it.
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return domain.BackupError(err, "cannot flush %s to disk", localPath)
	}
	return f.Close()
}

// safeDestination joins a component path onto a destination and refuses one
// that leaves it.
//
// A manifest on a target is a file this manager may not have written -- that is
// the entire premise of a target being somewhere else. Its component paths
// decide where a fetch writes, so `../../.ssh/authorized_keys` in one must not
// be a way for whoever controls the bucket to choose a path on this machine.
func safeDestination(destDir, component string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(component))
	if component == "" || filepath.IsAbs(clean) ||
		clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", domain.BackupError(nil,
			"the backup names a component outside the target: %q", component).
			WithHint("this backup was not written by this manager; do not restore from it")
	}
	return filepath.Join(destDir, clean), nil
}

// readLocalManifest reads the manifest of a backup about to be pushed.
func readLocalManifest(path string) (ports.BackupManifest, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the manager's own backup directory
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ports.BackupManifest{}, domain.BackupError(domain.ErrNotFound,
				"%s holds no %s, so it is not a backup",
				filepath.Dir(path), ports.BackupManifestFileName)
		}
		return ports.BackupManifest{}, domain.BackupError(err, "cannot read %s", path)
	}

	var m ports.BackupManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return ports.BackupManifest{}, domain.BackupError(err,
			"%s is not a valid backup manifest", path)
	}
	return m, nil
}

// regularFileSize stats a component and refuses anything that is not a plain
// file.
//
// A symlink among a backup's components points at something this manager did
// not write, and following it would copy a file from outside the backup onto a
// machine nobody meant to put it on.
func regularFileSize(path string) (int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, domain.BackupError(domain.ErrNotFound,
				"the backup names %s but it is not there", filepath.Base(path)).
				WithHint("run `morzer backup verify` on it before pushing")
		}
		return 0, domain.BackupError(err, "cannot stat %s", path)
	}
	if !info.Mode().IsRegular() {
		return 0, domain.BackupError(nil, "%s is not a regular file", path)
	}
	return info.Size(), nil
}
