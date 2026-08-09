package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/goccy/go-yaml"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/agecrypt"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
)

// Reading an export out of a backup happens on a machine that has nothing:
// no installation, no secret store, no backup engine — the engine needs an
// installation and a current release, and a rebuilt host has neither. So this
// reads the backup directory directly, which is also what makes it usable at
// the only moment it matters.

// ExportFromBackupOptions selects which backup's identity to read.
type ExportFromBackupOptions struct {
	// BackupID names a backup. Empty means the newest.
	//
	// Empty is the ordinary case and the safe one: identity and data are
	// separate choices, and staleness in identity only ever loses
	// information, so the newest export is strictly the most complete.
	BackupID string

	// IdentityFile is the offline recovery key. The export component is
	// encrypted to the recovery recipients alone, so this machine's own key
	// — if it even has one — cannot open it.
	IdentityFile string
}

// BackupExport is where an export came from, so the caller can say so.
type BackupExport struct {
	Export domain.InstallationExport

	// Backup is the backup the export was read out of.
	Backup ports.BackupManifest

	// Newest is the newest backup that carries an export, when it is not
	// the one used. Zero otherwise.
	Newest ports.BackupManifest
}

// FromNewest reports whether the export came from the newest backup carrying
// one.
func (b BackupExport) FromNewest() bool { return b.Newest.ID == "" }

// ExportFromBackup reads an installation export out of a local backup.
//
// This is the claim RFC 0017 exists to make true: an operator with a recovery
// key and a backup can rebuild a machine, without the export that nothing
// schedules and no check reports on.
func ExportFromBackup(
	paths domain.Paths, opts ExportFromBackupOptions,
) (BackupExport, error) {
	if strings.TrimSpace(opts.IdentityFile) == "" {
		return BackupExport{}, domain.Usage("an offline recovery identity is required").
			WithHint("pass --identity <file>, the private key printed by " +
				"`morzer secret recipients generate-recovery-key`. The export " +
				"inside a backup is encrypted to the recovery keys alone")
	}

	manifests, err := readBackupManifests(paths.BackupsDir())
	if err != nil {
		return BackupExport{}, err
	}
	if len(manifests) == 0 {
		// The product is named in the remedy because it decides the
		// directory that was just searched. On a rebuilt host nothing
		// knows what the product is yet -- the identity that says so is
		// what this is trying to read -- so an operator who omitted
		// --product gets a path that looks plausible and is empty.
		return BackupExport{}, domain.BackupError(domain.ErrNotFound,
			"no backups were found in %s", paths.BackupsDir()).
			WithHint("if this machine is being rebuilt, pass --product <name>: that " +
				"directory is derived from it, and nothing here knows the product " +
				"until an identity has been read. Otherwise fetch a backup with " +
				"`morzer backup fetch --target <url>`, or recover identity from a " +
				"file with `morzer installation import <export>`")
	}

	chosen, newest, err := selectBackup(manifests, opts.BackupID)
	if err != nil {
		return BackupExport{}, err
	}

	export, err := readExportComponent(paths.BackupsDir(), chosen, opts.IdentityFile)
	if err != nil {
		return BackupExport{}, err
	}

	out := BackupExport{Export: export, Backup: chosen}
	if newest.ID != chosen.ID {
		out.Newest = newest
	}
	return out, nil
}

// readBackupManifests reads every backup header under dir.
//
// Only the headers: `backup.json` is the one file in a backup that is not
// encrypted, precisely so a machine with no key can still see what it has.
func readBackupManifests(dir string) ([]ports.BackupManifest, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, domain.BackupError(err, "cannot read the backup store at %s", dir)
	}

	var out []ports.BackupManifest
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasSuffix(entry.Name(), ports.FetchStagingSuffix) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name(), ports.BackupManifestFileName))
		if err != nil {
			// A directory with no header is not a backup: a
			// half-finished fetch, or something an operator put
			// there. Skipped rather than refused, because one
			// unreadable neighbour must not make the others
			// unreachable during a recovery.
			continue
		}
		var m ports.BackupManifest
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		// The id has to be the directory it was read from.
		//
		// `backup.json` is unsigned plaintext -- deliberately, so a
		// machine with no key can still enumerate what it has -- and
		// every step after this uses m.ID as a directory name. Without
		// this, a manifest can name a different backup: the reader then
		// opens a component from somewhere else while reporting the
		// metadata of the one it was asked for.
		//
		// Skipped rather than refused, like an unreadable neighbour:
		// one bad directory must not make the rest unreachable during
		// a recovery.
		if m.ID != entry.Name() {
			continue
		}
		out = append(out, m)
	}

	// Newest first, and by recorded time rather than by name: an id is a
	// timestamp today and need not stay one.
	sort.SliceStable(out, func(i, j int) bool {
		return out[j].CreatedAt.Before(out[i].CreatedAt.Time)
	})
	return out, nil
}

// selectBackup picks the backup to read identity from, and the newest one that
// carries an export.
func selectBackup(
	manifests []ports.BackupManifest, id string,
) (chosen, newest ports.BackupManifest, err error) {
	for _, m := range manifests {
		if hasExportComponent(m) {
			newest = m
			break
		}
	}

	if strings.TrimSpace(id) == "" {
		if newest.ID == "" {
			return ports.BackupManifest{}, ports.BackupManifest{}, noExportAnywhere(manifests)
		}
		return newest, newest, nil
	}

	for _, m := range manifests {
		if m.ID != id {
			continue
		}
		if !hasExportComponent(m) {
			return ports.BackupManifest{}, ports.BackupManifest{}, noExportIn(m)
		}
		return m, newest, nil
	}
	return ports.BackupManifest{}, ports.BackupManifest{}, domain.BackupError(domain.ErrNotFound,
		"no backup %s", id).
		WithHint("`morzer backup list` shows what is here")
}

func hasExportComponent(m ports.BackupManifest) bool {
	_, ok := exportRecord(m)
	return ok
}

// noExportIn explains a backup that carries no identity, and the two reasons
// differ in what the operator should do next.
//
// Telling someone whose installation has no recovery recipient to "take a new
// backup" prescribes the action that reproduces the failure exactly: the next
// backup omits the component for the same reason. So the two cases get
// different messages, and neither of them is "take a new backup" unless that
// would actually help.
func noExportIn(m ports.BackupManifest) error {
	if m.SchemaVersion < exportComponentSchemaVersion {
		return domain.BackupError(domain.ErrNotFound,
			"backup %s predates installation exports in backups", m.ID).
			WithHint("take a new backup on a machine that still runs, or recover " +
				"identity from a file with `morzer installation import <export>`")
	}
	return domain.BackupError(domain.ErrNotFound,
		"backup %s carries no installation export, because the installation that "+
			"took it has no recovery recipient", m.ID).
		WithHint("an export is encrypted to the recovery keys alone, and that " +
			"installation had none to encrypt to. Configure one with `morzer secret " +
			"recipients add`, then take a new backup — or recover identity from a " +
			"file with `morzer installation import <export>`")
}

// noExportAnywhere explains a store in which nothing carries identity.
//
// It answers with the *newest* backup's reason rather than a message covering
// both, because the two remedies point in opposite directions and an operator
// given both has been told to guess. The newest is the right one to explain:
// it is the one whose condition the next backup will reproduce.
func noExportAnywhere(manifests []ports.BackupManifest) error {
	if len(manifests) == 0 {
		return domain.BackupError(domain.ErrNotFound, "no backups carry an installation export")
	}
	err := domain.AsError(noExportIn(manifests[0]))
	return domain.BackupError(domain.ErrNotFound,
		"none of the %d backup(s) here carries an installation export -- %s",
		len(manifests), err.Message).
		WithHint("%s", err.Hint)
}

// exportComponentSchemaVersion is the backup schema that first carried an
// export.
//
// Spelled here rather than imported from the engine, because this reads
// backups the engine cannot open and must not depend on it. The two are pinned
// together by a test.
const exportComponentSchemaVersion = 4

// readExportComponent decrypts and parses the export inside one backup.
func readExportComponent(
	backupsDir string, m ports.BackupManifest, identityFile string,
) (domain.InstallationExport, error) {
	record, ok := exportRecord(m)
	if !ok {
		return domain.InstallationExport{}, noExportIn(m)
	}

	// A digest is required, not merely compared when present.
	//
	// `backup.json` is unsigned plaintext, so a guard that only runs when
	// a field is populated is a guard an attacker switches off by deleting
	// the field. Every export this manager writes carries a digest, so
	// refusing one without costs nothing and makes the binding below
	// mandatory rather than advisory.
	if record.SHA256 == "" {
		return domain.InstallationExport{}, domain.BackupError(domain.ErrValidation,
			"the installation export in backup %s records no digest", m.ID).
			WithHint("every export this manager writes is recorded with one, so a " +
				"component without a digest did not come from here and nothing " +
				"binds it to this backup")
	}

	// Opened through a root at the backup store, so neither the id nor the
	// component path can leave it. Both are attacker-influenced strings out
	// of that same unsigned manifest -- an id of `../..` with a matching
	// path escapes a plain filepath.Join -- and the remote side already
	// refuses exactly this through safeDestination.
	root, err := os.OpenRoot(backupsDir)
	if err != nil {
		return domain.InstallationExport{}, domain.BackupError(err,
			"cannot open the backup store at %s", backupsDir)
	}
	defer func() { _ = root.Close() }()

	in, err := root.Open(path.Join(m.ID, record.Path))
	if err != nil {
		return domain.InstallationExport{}, domain.BackupError(err,
			"cannot read the installation export in backup %s", m.ID)
	}
	defer func() { _ = in.Close() }()

	// The digest, against the backup's own manifest.
	//
	// A named file is not a bound file: nothing in "read export.yaml.age
	// out of backup X" establishes that the bytes belong to backup X, and
	// an attacker with write access to a backup store -- or a botched sync
	// -- can put another installation's identity there. The digest is over
	// the *stored* bytes, so this needs no key and runs before anything is
	// decrypted.
	sum, err := digestReader(in)
	if err != nil {
		return domain.InstallationExport{}, domain.BackupError(err,
			"cannot read the installation export in backup %s", m.ID)
	}
	if !atomicfs.SameDigest(sum, record.SHA256) {
		return domain.InstallationExport{}, domain.BackupError(domain.ErrDigestMismatch,
			"the installation export in backup %s is not the one its manifest records",
			m.ID).
			WithHint("backup.json says %s and the file hashes to %s. This backup has "+
				"been altered or assembled from parts of others; do not import "+
				"identity from it", record.SHA256, sum)
	}
	if _, err := in.Seek(0, io.SeekStart); err != nil {
		return domain.InstallationExport{}, domain.BackupError(err,
			"cannot re-read the installation export in backup %s", m.ID)
	}

	// An unencrypted export is refused rather than read.
	//
	// Nothing produces one -- the component has existed only since backups
	// began encrypting everything but their manifest -- so this costs
	// nothing today and closes the one shape where an attacker with write
	// access to a backup store could hand back an identity the recovery key
	// never had to open. The key is what anchors this whole path; a
	// component that does not need it is not on it.
	if record.Encryption == ports.EncryptionNone {
		return domain.InstallationExport{}, domain.BackupError(domain.ErrValidation,
			"the installation export in backup %s is not encrypted", m.ID).
			WithHint("an export component is always encrypted to the recovery keys; " +
				"one that is not did not come from this manager")
	}

	var plain bytes.Buffer
	if err := agecrypt.Decrypt(&plain, in, identityFile); err != nil {
		return domain.InstallationExport{}, domain.BackupError(err,
			"cannot decrypt the installation export in backup %s", m.ID).
			WithHint("the export is encrypted to this installation's recovery keys " +
				"alone — not to the machine that took the backup. --identity must " +
				"be the offline recovery key")
	}

	var export domain.InstallationExport
	if err := yaml.Unmarshal(plain.Bytes(), &export); err != nil {
		return domain.InstallationExport{}, domain.ValidationError(err,
			"the installation export in backup %s is not a valid export document", m.ID)
	}
	if err := export.Validate(); err != nil {
		return domain.InstallationExport{}, err
	}
	return export, nil
}

// digestReader hashes a stream, so the containment check and the digest check
// share one opened file rather than agreeing about a path.
func digestReader(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// DescribeBackupExport says which backup the identity came from, and what that
// choice implies.
//
// Printed rather than merely returned because the pairing it warns about is
// invisible otherwise: identity and data are separate choices by design, and
// the cost of that design is that an operator can pair a new identity with old
// data and have every compatibility check pass.
func DescribeBackupExport(b BackupExport) []string {
	var lines []string
	lines = append(lines, fmt.Sprintf("identity read from backup %s, taken %s",
		b.Backup.ID, b.Backup.CreatedAt.UTC().Format("2006-01-02 15:04:05 UTC")))

	if !b.FromNewest() {
		lines = append(lines, fmt.Sprintf(
			"this is NOT the newest backup carrying an identity: %s is, taken %s. "+
				"Secrets rotated since %s are not in what you just imported",
			b.Newest.ID,
			b.Newest.CreatedAt.UTC().Format("2006-01-02 15:04:05 UTC"),
			b.Backup.CreatedAt.UTC().Format("2006-01-02")))
	}

	lines = append(lines, "restoring data from a backup older than this one may bring "+
		"back data that predates these secrets; restore from this backup, or from a "+
		"newer one, unless you know otherwise")
	return lines
}

// ExportFromRemoteBackup reads an installation export off a backup target
// without downloading the backup.
//
// This is the path the recovery story actually takes: the machine is gone, the
// backups are in a bucket, and the operator has an access key and a recovery
// key. Transferring the archive to read four kilobytes of it would cost an
// hour at the worst possible moment.
func ExportFromRemoteBackup(
	ctx context.Context, d *Deps, opts TargetOptions, backupID, identityFile string,
) (BackupExport, error) {
	if strings.TrimSpace(identityFile) == "" {
		return BackupExport{}, domain.Usage("an offline recovery identity is required").
			WithHint("pass --identity <file>; the export inside a backup is " +
				"encrypted to the recovery keys alone")
	}

	targets, err := d.targetsFor(ctx, opts)
	if err != nil {
		return BackupExport{}, err
	}

	// Only the manifests cross the network here, which is what `backup list
	// --remote` already costs: they are the one plaintext file in a backup,
	// so this works from a machine that has lost every key it ever had.
	// Every replica of a backup is kept, not the first one seen.
	//
	// The same id on two targets is normally the same bytes -- a push is
	// idempotent and a backup is immutable -- but "normally" is doing work
	// there. Bit rot, a half-restored bucket or a tampered replica all
	// produce a copy that lists fine and fails to read, and taking the
	// first and stopping would mean a recovery that fails while an intact
	// copy sits on the next target. This is the one code path where that
	// trade is obviously wrong.
	var (
		manifests []ports.BackupManifest
		replicas  = map[string][]ports.RemoteRef{}
	)
	for _, target := range targets {
		found, listErr := d.Targets.List(ctx, target)
		if listErr != nil {
			return BackupExport{}, listErr
		}
		for _, m := range found {
			if _, seen := replicas[m.ID]; !seen {
				manifests = append(manifests, m)
			}
			replicas[m.ID] = append(replicas[m.ID], ports.RemoteRef{Target: target, ID: m.ID})
		}
	}
	if len(manifests) == 0 {
		return BackupExport{}, domain.BackupError(domain.ErrNotFound,
			"no backups were found on the target").
			WithHint("`morzer backup list --remote --target <url>` shows what is there")
	}
	sort.SliceStable(manifests, func(i, j int) bool {
		return manifests[j].CreatedAt.Before(manifests[i].CreatedAt.Time)
	})

	chosen, newest, err := selectBackup(manifests, backupID)
	if err != nil {
		return BackupExport{}, err
	}

	record, ok := exportRecord(chosen)
	if !ok {
		return BackupExport{}, noExportIn(chosen)
	}

	// A directory shaped like the local backup store, so the same reader
	// checks the same things. The digest binding in particular is not
	// optional here: a named remote key returns whatever sits at it, and an
	// attacker with write access to the bucket can put another
	// installation's identity under that name.
	export, err := readExportFromReplicas(
		ctx, d, replicas[chosen.ID], chosen, record.Path, identityFile)
	if err != nil {
		return BackupExport{}, err
	}

	out := BackupExport{Export: export, Backup: chosen}
	if newest.ID != chosen.ID {
		out.Newest = newest
	}
	return out, nil
}

// readExportFromReplicas tries each copy of a backup until one yields an
// export, and reports every failure if none does.
//
// A recovery is the wrong moment to be told about one bad copy and left to
// discover that another target holds a good one.
func readExportFromReplicas(
	ctx context.Context,
	d *Deps,
	refs []ports.RemoteRef,
	chosen ports.BackupManifest,
	componentPath string,
	identityFile string,
) (domain.InstallationExport, error) {
	var failures []string

	for _, ref := range refs {
		export, err := readExportFromReplica(ctx, d, ref, chosen, componentPath, identityFile)
		if err == nil {
			return export, nil
		}
		failures = append(failures,
			ref.Target.String()+": "+domain.AsError(err).Message)
	}

	if len(failures) == 1 {
		return domain.InstallationExport{}, domain.BackupError(domain.ErrValidation,
			"cannot read the installation export in backup %s -- %s",
			chosen.ID, failures[0])
	}
	return domain.InstallationExport{}, domain.BackupError(domain.ErrValidation,
		"no copy of backup %s yielded a usable installation export:\n  - %s",
		chosen.ID, strings.Join(failures, "\n  - ")).
		WithHint("every target holding this backup was tried")
}

// readExportFromReplica fetches and reads one copy.
func readExportFromReplica(
	ctx context.Context,
	d *Deps,
	ref ports.RemoteRef,
	chosen ports.BackupManifest,
	componentPath string,
	identityFile string,
) (domain.InstallationExport, error) {
	// A directory shaped like the local backup store, so the same reader
	// checks the same things -- the digest binding above all, which is not
	// optional here: a named remote key returns whatever sits at it, and an
	// attacker with write access to the bucket can put another
	// installation's identity under that name.
	staging, err := os.MkdirTemp("", "morzer-export-")
	if err != nil {
		return domain.InstallationExport{}, domain.Internal(err,
			"cannot create a staging directory")
	}
	defer func() { _ = atomicfs.RemoveAll(staging) }()

	if err := d.Targets.FetchFile(ctx, ref, componentPath,
		filepath.Join(staging, chosen.ID)); err != nil {
		return domain.InstallationExport{}, err
	}

	// Re-read from what was fetched rather than trusting the listing: the
	// manifest that binds the component is the one that travelled with it.
	fetched, err := readBackupManifests(staging)
	if err != nil {
		return domain.InstallationExport{}, err
	}
	if len(fetched) != 1 || fetched[0].ID != chosen.ID {
		return domain.InstallationExport{}, domain.BackupError(domain.ErrValidation,
			"the target returned a manifest for a different backup than %s", chosen.ID)
	}

	return readExportComponent(staging, fetched[0], identityFile)
}

// exportRecord finds the export component in a manifest.
func exportRecord(m ports.BackupManifest) (ports.ComponentRecord, bool) {
	for _, c := range m.Components {
		if c.Component == ports.ComponentExport {
			return c, true
		}
	}
	return ports.ComponentRecord{}, false
}
