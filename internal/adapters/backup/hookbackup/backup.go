// Package hookbackup implements ports.BackupEngine by coordinating the
// release's own backup and restore hooks.
//
// The manager does not know how to back up a PostgreSQL database, and should
// not: pg_dump, pgBackRest and WAL-G already do, and each product knows which
// one it wants. What the manager owns is the parts a hook cannot do for
// itself -- ordering, a self-describing manifest, checksums, retention, and
// the refusal to restore across installations without an explicit
// confirmation.
package hookbackup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/agecrypt"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
)

// Name is the provider name a manifest selects with providers.backup.name.
const Name = "hooks"

// ManifestFileName is the self-describing header inside every backup
// directory. The name is the port's, because a backup target reads it too.
const ManifestFileName = ports.BackupManifestFileName

// BackupManifestSchemaVersion versions that header.
//
// 2 records how each component is stored. A schema-1 backup holds plaintext
// artifacts and is still restorable: the component's Encryption field is empty
// and the restore uses the file as it finds it. That is the compatibility rule
// this project applies everywhere -- a new manager reads an old backup, an old
// manager refuses a new one clearly.
const BackupManifestSchemaVersion = 2

// Engine is the hook-coordinating backup engine.
type Engine struct {
	hooks ports.HookRunner

	// release supplies the backup and restore hooks.
	release domain.Release

	installation domain.Installation
	paths        domain.Paths

	managerVersion string
	newID          func() string
	now            func() time.Time

	// recipients answers who may read a backup. It is a function rather
	// than a list because the answer changes: `secret recipients add` is a
	// command, and a backup taken after it must be readable by the key it
	// added.
	recipients func(context.Context) ([]string, error)
}

// Config wires the engine. Every field is required except the clock and ID
// generator, which exist so tests get deterministic output.
type Config struct {
	Hooks          ports.HookRunner
	Release        domain.Release
	Installation   domain.Installation
	Paths          domain.Paths
	ManagerVersion string

	// Recipients supplies the age public keys a backup is encrypted to,
	// normally the secret store's own recipient list.
	//
	// Required. An engine that cannot answer it refuses to take a backup
	// rather than writing a plaintext one -- see Create.
	Recipients func(context.Context) ([]string, error)

	NewID func() string
	Now   func() time.Time
}

func New(cfg Config) *Engine {
	e := &Engine{
		hooks:          cfg.Hooks,
		release:        cfg.Release,
		installation:   cfg.Installation,
		paths:          cfg.Paths,
		managerVersion: cfg.ManagerVersion,
		newID:          cfg.NewID,
		now:            cfg.Now,
		recipients:     cfg.Recipients,
	}
	if e.now == nil {
		e.now = time.Now
	}
	if e.newID == nil {
		e.newID = func() string { return e.now().UTC().Format("20060102T150405Z") }
	}
	return e
}

var _ ports.BackupEngine = (*Engine)(nil)

// Create runs the release's backup hook and wraps its output in a manifest.
//
// The hook writes into a directory the manager creates and owns. Letting the
// hook choose the location would put retention and disk accounting outside the
// manager's reach, which is most of what it is here to provide.
func (e *Engine) Create(ctx context.Context, scope ports.Scope, labels map[string]string) (ports.BackupRef, error) {
	spec, ok := e.release.Manifest.Operation(domain.OpBackup)
	if !ok {
		return ports.BackupRef{}, domain.BackupError(domain.ErrUnsupported,
			"this release declares no backup operation").
			WithHint("add a `backup` entry under `operations` in the release manifest")
	}
	if spec.Kind != domain.OperationKindHook {
		return ports.BackupRef{}, domain.BackupError(domain.ErrUnsupported,
			"the backup operation must be a hook, not %q", spec.Kind)
	}

	id := e.newID()
	dir := filepath.Join(e.paths.BackupsDir(), id)

	// 0700: a backup contains the database and the encrypted secret file.
	// It is the single most sensitive directory the manager creates.
	if err := atomicfs.MkdirExact(dir, 0o700); err != nil {
		return ports.BackupRef{}, err
	}

	components := scope.Components
	if len(components) == 0 {
		components = ports.AllComponents
	}

	env := e.hookEnv(ports.PhaseBackup, dir)
	env.Extra = map[string]string{
		prefixed(e.installation.Product, "BACKUP_ID"):         id,
		prefixed(e.installation.Product, "BACKUP_COMPONENTS"): joinComponents(components),
		prefixed(e.installation.Product, "BACKUP_REASON"):     scope.Reason,
	}

	outcome, err := e.hooks.Run(ctx, e.release, spec.Command, env, spec.Timeout.Or(60*time.Minute))
	if err != nil {
		// A failed backup leaves nothing useful behind, and a partial
		// backup directory that looks like a backup is worse than no
		// backup at all -- someone will eventually try to restore it.
		_ = atomicfs.RemoveAll(dir)
		return ports.BackupRef{}, domain.BackupError(err, "the backup hook failed").
			WithHint("the product's backup hook reported a failure; its output is in the log")
	}

	// The manager copies the parts it owns; the hook is responsible only
	// for the product's own data.
	records, err := e.captureManagedComponents(dir, components)
	if err != nil {
		_ = atomicfs.RemoveAll(dir)
		return ports.BackupRef{}, err
	}

	hookRecords, err := recordArtifacts(dir, outcome.Result.Artifacts)
	if err != nil {
		_ = atomicfs.RemoveAll(dir)
		return ports.BackupRef{}, err
	}
	records = append(records, hookRecords...)

	// Encrypted last, so the hook and the copy above stay unchanged: they
	// write plaintext into a 0700 directory, and it is protected before the
	// backup is complete enough for anything to move it anywhere.
	//
	// Everything except backup.json, which stays readable so `backup list`
	// works on a machine whose key is gone -- and so an operator staring at
	// a directory of ciphertext can tell what it is.
	recipients, err := e.backupRecipients(ctx)
	if err != nil {
		_ = atomicfs.RemoveWithOverwrite(dir)
		return ports.BackupRef{}, err
	}
	if records, err = encryptComponents(dir, records, recipients); err != nil {
		_ = atomicfs.RemoveWithOverwrite(dir)
		return ports.BackupRef{}, err
	}

	manifest := ports.BackupManifest{
		SchemaVersion:  BackupManifestSchemaVersion,
		ID:             id,
		InstallationID: e.installation.ID,
		Product:        e.installation.Product,
		ReleaseVersion: e.release.Version(),
		SchemaAtBackup: outcome.Result.SchemaVersion,
		CreatedAt:      domain.NewTime(e.now()),
		Components:     records,
		Labels:         labels,
		ManagerVersion: e.managerVersion,
		Reason:         scope.Reason,
	}

	if err := writeManifest(dir, manifest); err != nil {
		_ = atomicfs.RemoveAll(dir)
		return ports.BackupRef{}, err
	}

	size, _ := atomicfs.DirSize(dir)
	return ports.BackupRef{ID: id, Path: dir, At: manifest.CreatedAt, Size: size}, nil
}

// captureManagedComponents copies the files the manager owns into the backup.
//
// The encrypted SOPS file is included; the rendered plaintext secrets under
// /run are not. A backup that carried decrypted credentials would turn every
// backup copy into a credential store, which is precisely what encrypting them
// at rest was meant to avoid.
func (e *Engine) captureManagedComponents(dir string, components []ports.Component) ([]ports.ComponentRecord, error) {
	var out []ports.ComponentRecord

	copyOne := func(component ports.Component, src, name string) error {
		if _, err := os.Stat(src); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // an absent optional file is not a failure
			}
			return domain.BackupError(err, "cannot read %s", src)
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return domain.BackupError(err, "cannot read %s", src)
		}
		dst := filepath.Join(dir, name)
		if err := atomicfs.WriteFile(dst, data, 0o600); err != nil {
			return err
		}
		sum, err := atomicfs.DigestFile(dst)
		if err != nil {
			return err
		}
		out = append(out, ports.ComponentRecord{
			Component: component, Path: name, Size: int64(len(data)), SHA256: sum,
		})
		return nil
	}

	for _, c := range components {
		var err error
		switch c {
		case ports.ComponentConfig:
			if err = copyOne(c, e.paths.InstallationFile(), "installation.yaml"); err != nil {
				return nil, err
			}
			err = copyOne(c, e.paths.ApplicationFile(), "application.yaml")
		case ports.ComponentSecrets:
			if err = copyOne(c, e.paths.SecretsFile(), "secrets.sops.yaml"); err != nil {
				return nil, err
			}
			// The recipient sidecar travels with the encrypted file:
			// without it a restore loses which key was the recovery
			// key.
			err = copyOne(c, filepath.Join(e.paths.EtcDir, "secrets.recipients.yaml"),
				"secrets.recipients.yaml")
		case ports.ComponentManifest:
			err = copyOne(c, filepath.Join(e.release.Root, "manifest.yaml"), "manifest.yaml")
		default:
			// database and files belong to the hook.
			continue
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// recordArtifacts checksums the files the hook reported.
func recordArtifacts(dir string, artifacts []ports.HookArtifact) ([]ports.ComponentRecord, error) {
	var out []ports.ComponentRecord

	for _, a := range artifacts {
		path := a.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		// A hook that reports an artifact outside the backup directory
		// has produced something the manager cannot manage: it would
		// not be pruned, moved, or restored with the rest.
		rel, err := filepath.Rel(dir, path)
		if err != nil || strings.HasPrefix(rel, "..") {
			return nil, domain.BackupError(nil,
				"the backup hook reported artifact %q outside the backup directory", a.Path).
				WithHint("hooks must write into the directory given in the BACKUP_DIR variable")
		}

		info, err := os.Stat(path)
		if err != nil {
			return nil, domain.BackupError(err,
				"the backup hook reported artifact %q but it does not exist", a.Path)
		}

		sum := a.SHA256
		if sum == "" {
			// The hook may checksum its own output; when it does
			// not, the manager does, because a backup manifest
			// without checksums cannot be verified.
			if sum, err = atomicfs.DigestFile(path); err != nil {
				return nil, err
			}
		}

		component := ports.ComponentDatabase
		if a.Name != "" && !strings.Contains(strings.ToLower(a.Name), "db") {
			component = ports.ComponentFiles
		}
		out = append(out, ports.ComponentRecord{
			Component: component, Path: rel, Size: info.Size(), SHA256: sum,
		})
	}
	return out, nil
}

// backupRecipients answers who may read this backup, and refuses to proceed
// without an answer.
//
// A plaintext backup is exactly the gap this closes, so falling back to one
// when the recipient list cannot be read would keep the gap alive and make it
// invisible: the operator would have a backup, it would restore, and it would
// be readable by anyone who found the file.
func (e *Engine) backupRecipients(ctx context.Context) ([]string, error) {
	if e.recipients == nil {
		return nil, domain.Internal(nil,
			"the backup engine was wired without a recipient source")
	}
	keys, err := e.recipients(ctx)
	if err != nil {
		return nil, domain.BackupError(err, "cannot determine who may read this backup")
	}
	if len(keys) == 0 {
		return nil, domain.BackupError(nil,
			"this installation has no recipients, so a backup could not be encrypted").
			WithHint("run `morzer secret recipients list` -- an installation always " +
				"has at least this machine's own key, so an empty list means the " +
				"secret state is unreadable and `morzer doctor` will say why")
	}
	return keys, nil
}

// encryptComponents replaces each recorded file with its encrypted form.
//
// The plaintext is overwritten before removal rather than merely unlinked.
// That is meaningful on tmpfs and very little else -- the backups directory is
// ordinarily not tmpfs -- but it costs one write and removes the easiest case.
func encryptComponents(
	dir string, records []ports.ComponentRecord, recipients []string,
) ([]ports.ComponentRecord, error) {
	out := make([]ports.ComponentRecord, 0, len(records))

	for _, rec := range records {
		plain := filepath.Join(dir, rec.Path)
		encrypted := plain + agecrypt.Extension

		if err := encryptFile(plain, encrypted, recipients); err != nil {
			return nil, err
		}
		if err := atomicfs.RemoveWithOverwrite(plain); err != nil {
			return nil, err
		}

		// The digest is of the stored bytes, so `backup verify` needs no
		// key. Tampering is caught by the decryption itself, which is
		// authenticated.
		sum, err := atomicfs.DigestFile(encrypted)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(encrypted)
		if err != nil {
			return nil, domain.BackupError(err, "cannot stat %s", encrypted)
		}

		rec.Path = rec.Path + agecrypt.Extension
		rec.SHA256 = sum
		rec.Size = info.Size()
		rec.Encryption = ports.EncryptionAge
		out = append(out, rec)
	}
	return out, nil
}

func encryptFile(src, dst string, recipients []string) error {
	in, err := os.Open(src)
	if err != nil {
		return domain.BackupError(err, "cannot read %s", src)
	}
	defer func() { _ = in.Close() }()

	// 0600 and created before anything is written: a backup artifact is
	// never briefly world-readable, even as ciphertext.
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return domain.BackupError(err, "cannot create %s", dst)
	}

	if err := agecrypt.Encrypt(out, in, recipients); err != nil {
		_ = out.Close()
		_ = atomicfs.RemoveAll(dst)
		return err
	}
	if err := out.Close(); err != nil {
		return domain.BackupError(err, "cannot finish writing %s", dst)
	}
	return nil
}

// List enumerates backups newest first.
func (e *Engine) List(ctx context.Context) ([]ports.BackupRef, error) {
	entries, err := os.ReadDir(e.paths.BackupsDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, domain.BackupError(err, "cannot list backups")
	}

	var out []ports.BackupRef
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(e.paths.BackupsDir(), entry.Name())
		manifest, err := readManifest(dir)
		if err != nil {
			// A directory without a readable manifest is not a
			// backup. Skipping it rather than failing keeps `backup
			// list` usable when one entry is damaged.
			continue
		}
		size, _ := atomicfs.DirSize(dir)
		out = append(out, ports.BackupRef{
			ID: manifest.ID, Path: dir, At: manifest.CreatedAt, Size: size,
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At.Time) })
	return out, nil
}

func (e *Engine) Inspect(ctx context.Context, ref ports.BackupRef) (ports.BackupManifest, error) {
	dir, err := e.resolve(ref)
	if err != nil {
		return ports.BackupManifest{}, err
	}
	return readManifest(dir)
}

// Verify re-reads the backup and checks every checksum. A backup that has
// never been verified is a hope, not a backup.
func (e *Engine) Verify(ctx context.Context, ref ports.BackupRef) error {
	dir, err := e.resolve(ref)
	if err != nil {
		return err
	}
	manifest, err := readManifest(dir)
	if err != nil {
		return err
	}

	var problems []string
	for _, c := range manifest.Components {
		path := filepath.Join(dir, c.Path)

		info, err := os.Stat(path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: missing", c.Path))
			continue
		}
		if c.Size > 0 && info.Size() != c.Size {
			problems = append(problems, fmt.Sprintf("%s: size is %d, manifest says %d",
				c.Path, info.Size(), c.Size))
			continue
		}
		if c.SHA256 == "" {
			continue
		}
		sum, err := atomicfs.DigestFile(path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: unreadable", c.Path))
			continue
		}
		if !atomicfs.SameDigest(sum, c.SHA256) {
			problems = append(problems, fmt.Sprintf("%s: checksum mismatch", c.Path))
		}
	}

	if len(problems) > 0 {
		return domain.BackupError(domain.ErrDigestMismatch,
			"backup %s failed verification:\n  - %s", manifest.ID, strings.Join(problems, "\n  - ")).
			WithHint("this backup cannot be trusted for a restore; take a fresh one")
	}
	return nil
}

// Restore runs the release's restore hook against a verified backup.
func (e *Engine) Restore(ctx context.Context, ref ports.BackupRef, opts ports.RestoreOptions) error {
	dir, err := e.resolve(ref)
	if err != nil {
		return err
	}
	manifest, err := readManifest(dir)
	if err != nil {
		return err
	}

	// Verify before restoring, always. Restoring a corrupt backup over a
	// working system is the worst outcome available here.
	if err := e.Verify(ctx, ports.BackupRef{ID: manifest.ID, Path: dir}); err != nil {
		return err
	}

	if opts.TargetInstallationID != "" && manifest.InstallationID != opts.TargetInstallationID && !opts.Force {
		return domain.BackupError(nil,
			"backup %s belongs to installation %s, but this machine is %s",
			manifest.ID, manifest.InstallationID, opts.TargetInstallationID).
			WithHint("if this machine is a rebuild of that one, run " +
				"`morzer installation import <export> --identity <key>` first, which " +
				"restores the original id. Otherwise pass --allow-cross-installation " +
				"to restore another deployment's data on purpose.")
	}

	spec, ok := e.release.Manifest.Operation(domain.OpRestore)
	if !ok {
		return domain.BackupError(domain.ErrUnsupported,
			"this release declares no restore operation")
	}

	// The hook ABI is a published contract: a restore hook reads
	// $BACKUP_DIR/database.sql, and it did so before backups were
	// encrypted. So the decryption happens here, into a staging directory
	// the hook is pointed at, and the hook itself is unchanged.
	staged, cleanup, err := e.stage(dir, manifest, opts)
	if err != nil {
		return err
	}
	// Unconditional, and overwriting: a failed restore must not leave a
	// decrypted database dump beside the encrypted one.
	defer cleanup()

	env := e.hookEnv(ports.PhaseRestore, staged)
	env.Extra = map[string]string{
		prefixed(e.installation.Product, "BACKUP_ID"):              manifest.ID,
		prefixed(e.installation.Product, "BACKUP_RELEASE_VERSION"): manifest.ReleaseVersion.String(),
		prefixed(e.installation.Product, "BACKUP_COMPONENTS"):      joinComponents(opts.Components),
	}

	if _, err := e.hooks.Run(ctx, e.release, spec.Command, env, spec.Timeout.Or(120*time.Minute)); err != nil {
		return domain.BackupError(err, "the restore hook failed").
			WithHint("the system may be partially restored; run `morzer doctor` before retrying")
	}
	return nil
}

// stage decrypts a backup into a directory the restore hook can read.
//
// A schema-1 backup has plaintext components and is used where it lies, which
// is what keeps a backup taken before this change restorable after it.
func (e *Engine) stage(
	dir string, manifest ports.BackupManifest, opts ports.RestoreOptions,
) (staged string, cleanup func(), err error) {
	var encrypted []ports.ComponentRecord
	for _, c := range manifest.Components {
		if c.Encryption == ports.EncryptionAge {
			encrypted = append(encrypted, c)
		}
	}
	if len(encrypted) == 0 {
		return dir, func() {}, nil
	}

	identity := opts.IdentityFile
	if identity == "" {
		identity = e.paths.AgeIdentityFile()
	}

	// Beside the backup rather than in /tmp: /tmp is frequently a different
	// filesystem, and a database dump is not something to copy twice.
	staged, err = os.MkdirTemp(dir, ".restore-")
	if err != nil {
		return "", func() {}, domain.BackupError(err,
			"cannot create a staging directory under %s", dir)
	}
	if err := os.Chmod(staged, 0o700); err != nil {
		return "", func() {}, domain.BackupError(err, "cannot secure the staging directory")
	}
	cleanup = func() { _ = atomicfs.RemoveWithOverwrite(staged) }

	for _, c := range encrypted {
		if err := decryptComponent(dir, staged, c, identity); err != nil {
			cleanup()
			return "", func() {}, err
		}
	}
	return staged, cleanup, nil
}

func decryptComponent(dir, staged string, c ports.ComponentRecord, identity string) error {
	in, err := os.Open(filepath.Join(dir, c.Path))
	if err != nil {
		return domain.BackupError(err, "cannot read %s from the backup", c.Path)
	}
	defer func() { _ = in.Close() }()

	// The stored name carries the .age suffix; the hook expects the name it
	// wrote.
	name := strings.TrimSuffix(c.Path, agecrypt.Extension)
	target := filepath.Join(staged, name)
	if dirPart := filepath.Dir(target); dirPart != staged {
		if err := atomicfs.MkdirAll(dirPart, 0o700); err != nil {
			return err
		}
	}

	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return domain.BackupError(err, "cannot create %s", name)
	}
	if err := agecrypt.Decrypt(out, in, identity); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return domain.BackupError(err, "cannot finish writing %s", name)
	}
	return nil
}

// Prune removes backups beyond the retention policy.
//
// The most recent backup is never pruned regardless of what the policy says.
// A policy of zero is a configuration mistake, and honouring it literally
// would delete the only copy of the data.
func (e *Engine) Prune(ctx context.Context, policy ports.RetentionPolicy) ([]ports.BackupRef, error) {
	refs, err := e.List(ctx)
	if err != nil {
		return nil, err
	}
	keep := policy.Keep
	if keep < 1 {
		keep = 1
	}
	if len(refs) <= keep {
		return nil, nil
	}

	exempt := make(map[string]bool, len(policy.KeepReasons))
	for _, r := range policy.KeepReasons {
		exempt[r] = true
	}

	var removed []ports.BackupRef
	// refs is newest-first, so everything past `keep` is a candidate.
	for _, ref := range refs[keep:] {
		if len(exempt) > 0 {
			if manifest, err := readManifest(ref.Path); err == nil && exempt[manifest.Reason] {
				continue
			}
		}
		if err := atomicfs.RemoveAll(ref.Path); err != nil {
			return removed, err
		}
		removed = append(removed, ref)
	}
	return removed, nil
}

// resolve turns a ref into a directory, accepting either a path or an ID.
func (e *Engine) resolve(ref ports.BackupRef) (string, error) {
	if ref.Path != "" {
		return ref.Path, nil
	}
	if ref.ID == "" {
		return "", domain.BackupError(nil, "no backup was specified")
	}
	dir := filepath.Join(e.paths.BackupsDir(), ref.ID)
	if _, err := os.Stat(dir); err != nil {
		return "", domain.BackupError(domain.ErrNotFound, "no backup with id %q", ref.ID).
			WithHint("run `morzer backup list` to see what is available")
	}
	return dir, nil
}

func (e *Engine) hookEnv(phase ports.HookPhase, backupDir string) ports.HookEnv {
	return ports.HookEnv{
		Product:        e.installation.Product,
		InstallationID: e.installation.ID,
		Phase:          phase,
		ReleaseVersion: e.release.Version(),
		ReleaseDir:     e.release.Root,
		DataDir:        e.paths.DataDir(),
		BackupDir:      backupDir,
		SecretsDir:     e.paths.SecretsRenderDir(),
		ConfigFile:     e.paths.ApplicationFile(),
		ComposeProject: e.release.Manifest.Runtime.Project,
	}
}

func writeManifest(dir string, m ports.BackupManifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return domain.Internal(err, "cannot serialise the backup manifest")
	}
	return atomicfs.WriteFile(filepath.Join(dir, ManifestFileName), append(data, '\n'), 0o600)
}

func readManifest(dir string) (ports.BackupManifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, ManifestFileName))
	if err != nil {
		return ports.BackupManifest{}, domain.BackupError(err,
			"%s has no readable backup manifest", dir).
			WithHint("a backup directory must contain %s", ManifestFileName)
	}
	var m ports.BackupManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return ports.BackupManifest{}, domain.BackupError(err,
			"the backup manifest in %s is not valid JSON", dir)
	}
	if m.SchemaVersion > BackupManifestSchemaVersion {
		return ports.BackupManifest{}, domain.BackupError(domain.ErrIncompatible,
			"backup %s was written by a newer manager (schema %d, this manager reads %d)",
			m.ID, m.SchemaVersion, BackupManifestSchemaVersion).
			WithHint("upgrade the manager before restoring this backup")
	}
	return m, nil
}

func joinComponents(cs []ports.Component) string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = string(c)
	}
	return strings.Join(out, ",")
}

// prefixed builds a product-namespaced environment variable name, matching the
// hook ABI's convention.
func prefixed(product, key string) string {
	p := strings.ToUpper(strings.ReplaceAll(product, "-", "_"))
	if p == "" {
		p = "PRODUCT"
	}
	return p + "_" + key
}
