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
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

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
//
// 3 adds volume components, and the bump is what makes an older manager refuse
// rather than half-restore one. An older manager reads a schema-3 backup, does
// not know what ComponentVolumes means, decrypts the tarballs into the staging
// directory and hands them to a restore hook that was never told about them --
// so the database comes back, the uploads do not, and nothing says so. A
// refusal naming the manager version is the only honest answer.
//
// 4 adds the export component and retires `secrets`. An older manager reading
// a schema-4 backup would find no `secrets.sops.yaml.age` where it expects one
// and an `export.yaml.age` it has no idea is the replacement -- so it would
// restore the data and silently drop the half of the backup that carries
// identity. Nothing is released, so this protects nobody today; it is here
// because it will matter after the first tag, and because the bump is free now
// and impossible later.
const BackupManifestSchemaVersion = 4

// ExportProvider builds the installation export a backup carries.
//
// The signature is the lifecycle layer's ops.BuildExport, supplied by the CLI
// where adapters are named. The engine takes a function rather than importing
// that package: an adapter reaching into the lifecycle layer would invert the
// dependency the whole architecture rests on, and the engine has no business
// knowing how an export is assembled -- only that it gets one.
// The boolean is "this installation has an export to carry", distinct from an
// error: an installation with no recovery recipient has made a choice, not hit
// a fault, and a backup for it succeeds without the component.
type ExportProvider func(context.Context) (domain.InstallationExport, bool, error)

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

	// freeSpace reports the bytes available where backups are written.
	// Injectable for the same reason as the clock: a space check whose
	// verdict depends on the host's real disk is a check whose test passes
	// or fails on which machine happened to run it.
	freeSpace func(string) (int64, error)

	// recipients answers who may read a backup. It is a function rather
	// than a list because the answer changes: `secret recipients add` is a
	// command, and a backup taken after it must be readable by the key it
	// added.
	recipients func(context.Context) ([]string, error)

	// export builds the identity document the backup carries. Nil means no
	// export component; see Config.Export.
	export ExportProvider

	// runtime reads the project's volumes. Nil in a build or a test that
	// wires none, in which case a backup covers what the hook produces and
	// nothing else -- exactly as it did before volumes existed.
	runtime ports.Runtime

	// runtimeConfig identifies the project whose volumes are read.
	runtimeConfig ports.RuntimeConfig

	// allowDowntime permits stopping services to read a volume that the
	// release has not declared safe to read live.
	//
	// On by default. An undeclared volume captured hot is a claim the
	// manager would be making on the vendor's behalf, so the choice is
	// between stopping and skipping -- and a backup that quietly skipped
	// the uploads is the failure this whole component exists to prevent.
	allowDowntime bool

	// stopTimeoutOverride replaces DefaultStopTimeout when non-zero.
	stopTimeoutOverride time.Duration
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

	// Export builds the InstallationExport a backup carries.
	//
	// A function rather than a document, for the same reason Recipients is
	// one: the export has to describe the machine at the moment the backup
	// is taken, not at the moment the engine was constructed.
	//
	// Optional. Without it a backup carries no export component, which is
	// what every test that does not care about recovery gets -- and what
	// makes the absence of the component a testable state rather than an
	// unreachable one.
	Export ExportProvider

	// Runtime reads and writes the project's volumes, and stops the
	// services that mount them.
	//
	// Optional: without it a backup covers what the hook produces and the
	// files the manager owns, which is what it covered before volumes were
	// a component. Volume capture additionally needs the runtime to
	// implement ports.VolumeInspector and ports.VolumeCapturer.
	Runtime ports.Runtime

	// RuntimeConfig identifies the Compose project. Required alongside
	// Runtime.
	RuntimeConfig ports.RuntimeConfig

	// AllowDowntime permits stopping services to read an undeclared
	// volume. See Engine.allowDowntime.
	AllowDowntime bool

	// StopTimeout is how long a service gets to shut down cleanly before it
	// is killed, when one is stopped to read a volume. Zero uses
	// DefaultStopTimeout.
	//
	// Injectable for the same reason as the clock: it is the dominant cost
	// of a container test, because a fixture whose PID 1 ignores SIGTERM
	// waits the whole period out every time.
	StopTimeout time.Duration

	NewID func() string
	Now   func() time.Time

	// FreeSpace reports the bytes available on the filesystem holding a
	// path. Injectable for the same reason as the clock; see
	// Engine.freeSpace. Zero uses the real filesystem.
	FreeSpace func(string) (int64, error)
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
		export:         cfg.Export,
		freeSpace:      cfg.FreeSpace,
		runtime:        cfg.Runtime,
		runtimeConfig:  cfg.RuntimeConfig,
		allowDowntime:  cfg.AllowDowntime,

		stopTimeoutOverride: cfg.StopTimeout,
	}
	if e.now == nil {
		e.now = time.Now
	}
	if e.freeSpace == nil {
		e.freeSpace = atomicfs.FreeSpace
	}
	if e.newID == nil {
		e.newID = func() string { return e.now().UTC().Format("20060102T150405Z") }
	}
	return e
}

var _ ports.BackupEngine = (*Engine)(nil)

// createDir allocates the backup's identity on disk.
//
// The ID has second granularity, so two backups started within one second
// share it -- and tolerating the existing directory would overwrite the first
// backup's manifest and components in place. A collision picks a suffixed name
// instead; the bound exists only so a filesystem that reports EEXIST for some
// other reason cannot loop this forever.
//
// 0700: a backup contains the database and the encrypted secret file. It is
// the single most sensitive directory the manager creates.
func (e *Engine) createDir() (id, dir string, err error) {
	id = e.newID()
	dir = filepath.Join(e.paths.BackupsDir(), id)
	for n := 2; ; n++ {
		err := atomicfs.MkdirFresh(dir, 0o700)
		if err == nil {
			return id, dir, nil
		}
		if !errors.Is(err, fs.ErrExist) || n > 10 {
			return "", "", err
		}
		id = e.newID() + "-" + strconv.Itoa(n)
		dir = filepath.Join(e.paths.BackupsDir(), id)
	}
}

// Create runs the release's backup hook and wraps its output in a manifest.
//
// The hook writes into a directory the manager creates and owns. Letting the
// hook choose the location would put retention and disk accounting outside the
// manager's reach, which is most of what it is here to provide.
func (e *Engine) Create(ctx context.Context, scope ports.Scope, labels map[string]string) (ports.BackupRef, error) {
	// First, before any refusal below: a machine whose release lost its
	// backup hook can still be carrying another release's restore debris.
	if err := e.sweepStagedPlaintext(); err != nil {
		return ports.BackupRef{}, err
	}
	e.sweepManifestlessDirectories()

	components := scope.Components
	if len(components) == 0 {
		components = ports.AllComponents
	}

	spec, hasHook := e.release.Manifest.Operation(domain.OpBackup)
	if hasHook && spec.Kind != domain.OperationKindHook {
		return ports.BackupRef{}, domain.BackupError(domain.ErrUnsupported,
			"the backup operation must be a hook, not %q", spec.Kind)
	}

	// A release with no backup hook can still produce a restorable backup,
	// as long as there is something to put in it. That is the whole point
	// of volumes being a component the manager can read for itself: an
	// operator running a vendor who never wrote the hook used to have
	// `morzer backup` and nothing behind it.
	//
	// Nothing to capture at all is still a refusal, because a directory
	// containing only a manifest is not a backup and somebody would
	// eventually try to restore it.
	_, canCapture := e.volumeSupport()
	wantVolumes := canCapture && componentSelected(components, ports.ComponentVolumes)
	if !hasHook && !wantVolumes {
		return ports.BackupRef{}, domain.BackupError(domain.ErrUnsupported,
			"this release declares no backup operation and no volumes are in scope").
			WithHint("add a `backup` entry under `operations` in the release manifest, " +
				"or include the `volumes` component so the manager captures the " +
				"project's named volumes itself")
	}

	id, dir, err := e.createDir()
	if err != nil {
		return ports.BackupRef{}, err
	}

	// A failed backup leaves nothing useful behind, and a partial backup
	// directory that looks like a backup is worse than no backup at all --
	// someone will eventually try to restore it.
	//
	// Overwriting, not just unlinking. What is in the directory at this
	// point is plaintext: the hook's database dump and the volume tarballs,
	// before encryption. The success path already overwrites exactly these
	// bytes as it encrypts each one, so unlinking them here would leave a
	// failed backup's product data more recoverable from free blocks than a
	// successful one's.
	fail := func(err error) (ports.BackupRef, error) {
		_ = atomicfs.RemoveWithOverwrite(dir)
		return ports.BackupRef{}, err
	}

	var records []ports.ComponentRecord
	var schemaAtBackup int

	// Decided before the hook runs, not after. Decision 8 promises that a
	// backup which will not fit is refused "before anything is written", and
	// the hook's database dump lands on the very disk the space check is
	// about: measuring afterwards meant an operator whose disk was too small
	// paid for a full pg_dump before being told so, and got the refusal with
	// the dump still occupying the space it was refused for.
	//
	// Only the decision moves. The copy itself stays after the hook, because
	// a cold capture stops services and the hook has to run against a stack
	// that is up.
	var capture volumeCapture
	if wantVolumes {
		planned, planErr := e.planVolumeCapture(ctx)
		if planErr != nil {
			return fail(planErr)
		}
		capture = planned
	}

	if hasHook {
		env := e.hookEnv(ports.PhaseBackup, dir)
		env.Extra = map[string]string{
			prefixed(e.installation.Product, "BACKUP_ID"):         id,
			prefixed(e.installation.Product, "BACKUP_COMPONENTS"): joinComponents(components),
			prefixed(e.installation.Product, "BACKUP_REASON"):     scope.Reason,
		}

		outcome, err := e.hooks.Run(ctx, e.release, spec.Command, env, spec.Timeout.Or(60*time.Minute))
		if err != nil {
			return fail(domain.BackupError(err, "the backup hook failed").
				WithHint("the product's backup hook reported a failure; " +
					"its output is in the log"))
		}
		schemaAtBackup = outcome.Result.SchemaVersion

		hookRecords, err := recordArtifacts(dir, outcome.Result.Artifacts)
		if err != nil {
			return fail(err)
		}
		records = append(records, hookRecords...)
	}

	// The copy, after the hook: a cold capture stops services and the hook
	// has to run against a stack that is up. The hook is authoritative for
	// anything with a transaction log; volumes cover what it does not.
	var uncaptured []ports.UncapturedVolume
	var capturedVolumes int
	if wantVolumes {
		// What the hook wrote goes into the space check the copy makes
		// for itself: it is on the disk now, it is sizeable now, and
		// encryption will duplicate it.
		volumeRecords, skipped, err := e.captureVolumes(ctx, dir, capture, largestComponent(records))
		if err != nil {
			return fail(err)
		}
		records = append(records, volumeRecords...)
		uncaptured = skipped
		capturedVolumes = len(volumeRecords)
	}

	// The refusal is about what was captured, not about what was intended.
	//
	// The gate above passes when volumes are merely *in scope*, which they
	// always are -- so a release with no hook whose project keeps its data
	// on bind mounts got as far as here and produced a backup holding the
	// configuration and nothing of the product. `backup list` offers that,
	// and somebody eventually restores it.
	if !hasHook && capturedVolumes == 0 {
		return fail(domain.BackupError(domain.ErrUnsupported,
			"this release declares no backup operation and the backup captured nothing").
			WithHint("add a `backup` entry under `operations` in the release " +
				"manifest, or move the product's data onto a named volume -- " +
				"bind mounts are never captured"))
	}

	// The manager copies the parts it owns; the hook is responsible only
	// for the product's own data.
	managed, err := e.captureManagedComponents(dir, components)
	if err != nil {
		return fail(err)
	}
	records = append(records, managed...)

	// The identity document, and the recipients it alone is encrypted to.
	// Written after the refusal above, so a backup that captured nothing
	// does not acquire an export on its way to being rejected.
	exportRecord, exportRecipients, err := e.captureExport(ctx, dir, components)
	if err != nil {
		return fail(err)
	}
	if exportRecord != nil {
		records = append(records, *exportRecord)
	}

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
	records, err = encryptComponents(dir, records, func(rec ports.ComponentRecord) []string {
		if rec.Component == ports.ComponentExport {
			return exportRecipients
		}
		return recipients
	})
	if err != nil {
		_ = atomicfs.RemoveWithOverwrite(dir)
		return ports.BackupRef{}, err
	}

	manifest := ports.BackupManifest{
		SchemaVersion:  BackupManifestSchemaVersion,
		ID:             id,
		InstallationID: e.installation.ID,
		Product:        e.installation.Product,
		ReleaseVersion: e.release.Version(),
		SchemaAtBackup: schemaAtBackup,
		CreatedAt:      domain.NewTime(e.now()),
		Components:     records,
		Labels:         labels,
		ManagerVersion: e.managerVersion,
		Reason:         scope.Reason,
		Uncaptured:     uncaptured,
	}

	// The components' directory entries become durable before the manifest
	// that names them does: a manifest that survives a crash must never
	// describe files that did not. The whole tree, because components nest
	// -- volumes/*.tar.age lives a level down, and a hook may have written
	// artifacts in subdirectories of its own -- and then the backup
	// directory's own entry in the store, without which the durable backup
	// is one `backup list` cannot see.
	atomicfs.SyncTree(dir)
	atomicfs.SyncDir(e.paths.BackupsDir())

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
			// Retired. The export component carries the same state
			// byte for byte and the recipient roles the sidecar
			// existed to preserve, so writing both would put the
			// secret state in one backup twice.
			//
			// Still accepted in a scope so an explicit
			// `--component secrets` is not an error on a manager
			// that no longer produces one; it simply contributes
			// nothing.
			continue
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

// ExportFileName is the identity document inside a backup, before encryption.
const ExportFileName = "export.yaml"

// captureExport writes the installation export into the backup.
//
// It returns the record and the recipients that record is encrypted to, which
// are *not* the backup's own: the export goes to the recovery keys alone, so
// the machine that wrote the backup cannot read the identity inside it.
// Compromising the live host then yields the data and not the ability to
// become the machine.
//
// A nil record means no export was written, and there are two honest reasons
// for that: no provider was wired, or the installation has no recovery
// recipient. The second is the interesting one. Falling back to the backup's
// full recipient list would produce an identity bundle readable by exactly the
// key that dies with the machine -- the appearance of a recovery path with
// none of the substance -- so the component is skipped and `--from-backup`
// says why rather than handing back something unusable.
func (e *Engine) captureExport(
	ctx context.Context, dir string, components []ports.Component,
) (*ports.ComponentRecord, []string, error) {
	if !containsComponent(components, ports.ComponentExport) || e.export == nil {
		return nil, nil, nil
	}

	export, ok, err := e.export(ctx)
	if err != nil {
		return nil, nil, domain.BackupError(err,
			"cannot assemble the installation export this backup carries")
	}
	if !ok {
		return nil, nil, nil
	}

	// Belt and braces against the provider and this engine disagreeing:
	// the provider decides whether there is an export to carry, and this
	// decides who can read it. If the second answer is empty the first was
	// wrong, and writing a component nobody can open is worse than writing
	// none.
	recipients := recoveryRecipients(export)
	if len(recipients) == 0 {
		return nil, nil, nil
	}

	data, err := yaml.Marshal(export)
	if err != nil {
		return nil, nil, domain.Internal(err, "cannot serialise the installation export")
	}

	dst := filepath.Join(dir, ExportFileName)
	if err := atomicfs.WriteFile(dst, data, 0o600); err != nil {
		return nil, nil, err
	}
	sum, err := atomicfs.DigestFile(dst)
	if err != nil {
		return nil, nil, err
	}

	return &ports.ComponentRecord{
		Component: ports.ComponentExport,
		Path:      ExportFileName,
		Size:      int64(len(data)),
		SHA256:    sum,
	}, recipients, nil
}

// recoveryRecipients is the offline keys an export names, and nothing else.
//
// Read off the export rather than asked of the secret store, because the
// export is the document being protected and its own recipient list is the
// authority on who the recovery keys are. Asking twice would admit an answer
// that disagrees with the file it guards.
func recoveryRecipients(export domain.InstallationExport) []string {
	var out []string
	for _, r := range export.Secrets.Recipients {
		if r.Kind == domain.RecipientKindRecovery && strings.TrimSpace(r.PublicKey) != "" {
			out = append(out, r.PublicKey)
		}
	}
	return out
}

func containsComponent(components []ports.Component, want ports.Component) bool {
	for _, c := range components {
		if c == want {
			return true
		}
	}
	return false
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

// largestComponent is the biggest single recorded file.
//
// The biggest, not the sum, and that is the whole point of it: encryptComponents
// works through the components one at a time, writing a ciphertext beside a
// plaintext and removing the plaintext before starting the next, so what a space
// check has to reserve on top of the bytes already there is one more copy of the
// largest -- never one more copy of all of them. Reserving the sum would refuse
// backups that fit.
//
// Only recorded components count. A file a hook wrote and did not report is
// never encrypted, so it never gets a second copy: it is already subtracted from
// the free space and adds nothing to the peak.
func largestComponent(records []ports.ComponentRecord) int64 {
	var largest int64
	for _, rec := range records {
		largest = max(largest, rec.Size)
	}
	return largest
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
// The recipient list is per record rather than per backup, which is RFC 0017
// decision 11 arriving in the one place it can be enforced. Everything a
// running machine may need to read again is encrypted to the deployment's full
// list; the export component is encrypted to the recovery keys alone. A single
// list for the whole backup could not express that, and the property it buys
// -- the machine cannot read its own identity bundle -- is the difference
// between compromising a host and inheriting it.
func encryptComponents(
	dir string,
	records []ports.ComponentRecord,
	recipientsFor func(ports.ComponentRecord) []string,
) ([]ports.ComponentRecord, error) {
	out := make([]ports.ComponentRecord, 0, len(records))

	for _, rec := range records {
		plain := filepath.Join(dir, rec.Path)
		encrypted := plain + agecrypt.Extension

		recipients := recipientsFor(rec)
		if len(recipients) == 0 {
			// Never a plaintext component. An empty list here would
			// mean a bug in the caller rather than a configuration
			// an operator chose -- the no-recovery-recipient case
			// is handled by not writing the component at all.
			return nil, domain.Internal(nil,
				"no recipients for the %s component of this backup", rec.Component)
		}
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
	// The ciphertext is the backup. The manifest naming it is fsynced on
	// write, so without this a power cut could leave a durable manifest
	// pointing at truncated components -- a backup that reported success
	// and fails at restore time, the one moment nothing can be done.
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return domain.BackupError(err, "cannot flush %s to disk", dst)
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
	if err := e.sweepStagedPlaintext(); err != nil {
		return err
	}

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

	// Before the gate below and before anything is staged, because that gate
	// counts volumes and the count is exactly what a damaged record makes a
	// lie: a manifest whose volume component names no volume would otherwise
	// read as a backup that simply has none, and the restore would run the
	// hook, return zero, and leave the volume as it found it.
	//
	// Only when volumes are in scope. Restoring the database alone out of a
	// backup whose volume metadata is damaged is a documented recovery path
	// -- and the one an operator reaches for *because* something is wrong
	// with the backup. Refusing it would take away the remedy on the
	// strength of a component they deliberately excluded.
	if componentSelected(opts.Components, ports.ComponentVolumes) {
		if err := manifest.CheckVolumeRecords(); err != nil {
			return err
		}
	}

	spec, hasHook := e.release.Manifest.Operation(domain.OpRestore)
	volumes := manifest.VolumeRecords()

	// A backup of nothing but volumes needs no restore hook, which is the
	// other half of letting a release without one still have backups.
	if !hasHook && len(volumes) == 0 {
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

	// Volumes before the hook. A volume that will not go back stops the
	// operation while the database is still the one that was there; once
	// the hook has begun overwriting a database, nothing can put it back.
	if err := e.restoreVolumes(ctx, staged, manifest, opts); err != nil {
		return err
	}

	if !hasHook {
		return nil
	}

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
		if c.Encryption != ports.EncryptionAge {
			continue
		}
		// Everything else is staged whatever the scope says, because the
		// hook ABI predates scoping and a hook that reads more than it
		// was told to would break. Volumes are new and no hook can be
		// reading them -- so a restore of the database alone does not
		// decrypt a hundred gigabytes of uploads it will not use.
		if c.Component == ports.ComponentVolumes &&
			!componentSelected(opts.Components, ports.ComponentVolumes) {
			continue
		}
		encrypted = append(encrypted, c)
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

// sweepStagedPlaintext removes restore staging directories a dead process
// left behind.
//
// stage decrypts into <backup>/.restore-* and cleans up only through an
// in-process defer, so a SIGKILL or power cut mid-restore strands the
// database dump and volume tarballs as plaintext beside the ciphertext --
// forever: Prune removes whole backups past retention, and the backup being
// restored is typically the newest, which is never pruned. Swept at the start
// of the operations that run under the deployment lock, with overwrite for
// the same reason a failed backup scrubs rather than unlinks: this is
// product data on free blocks otherwise.
//
// A removal failure fails the operation: proceeding would report success over
// decrypted product data the sweep just proved it cannot clean up. The parent
// directory is synced after each removal so the deletion itself survives a
// power cut rather than resurrecting the plaintext.
func (e *Engine) sweepStagedPlaintext() error {
	stale, _ := filepath.Glob(filepath.Join(e.paths.BackupsDir(), "*", ".restore-*"))
	var errs []error
	for _, dir := range stale {
		// Every directory is attempted: one stuck removal must not
		// leave its siblings unswept on top of failing the operation.
		if err := atomicfs.RemoveWithOverwrite(dir); err != nil {
			errs = append(errs, err)
			continue
		}
		atomicfs.SyncDir(filepath.Dir(dir))
	}
	if len(errs) > 0 {
		return domain.BackupError(errors.Join(errs...),
			"cannot remove stale restore staging under %s", e.paths.BackupsDir()).
			WithHint("it holds decrypted product data from an interrupted restore; " +
				"remove the .restore-* directories manually before continuing")
	}
	return nil
}

// sweepManifestlessDirectories reclaims backup directories that never got a
// manifest.
//
// The manifest is written last, on purpose: a directory holding components and
// no manifest is a backup that was interrupted, and treating it as one would
// mean restoring from something nobody finished writing. So `backup list` skips
// it -- which also means retention never counts it, never prunes it, and the
// components sit on the disk of the machine they were meant to protect until
// somebody notices. A power cut during a database dump can leave gigabytes.
//
// Best effort, and deliberately not an error: this runs at the start of a
// backup, and failing to tidy up must not stop the operator taking one. What
// cannot be removed is left for `doctor` to report as space that is not
// accounted for.
//
// The deployment lock serialises operations, so nothing here is racing a backup
// in flight -- and the directory that Create is *about* to write does not exist
// yet when this runs.
func (e *Engine) sweepManifestlessDirectories() {
	entries, err := os.ReadDir(e.paths.BackupsDir())
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// `backup fetch` writes into <id>.fetching and adds the manifest
		// last, and it runs outside the deployment lock -- so a
		// manifest-less directory under that name is not debris, it is a
		// download in progress, and erasing it would delete the recovery
		// an operator is in the middle of. A fetch cleans its own
		// staging before it starts and on the way out.
		if strings.HasSuffix(entry.Name(), ports.FetchStagingSuffix) {
			continue
		}
		dir := filepath.Join(e.paths.BackupsDir(), entry.Name())
		if _, err := os.Stat(filepath.Join(dir, ManifestFileName)); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			// Unreadable is not the same as absent, and a backup
			// whose manifest cannot be read is evidence rather than
			// debris.
			continue
		}
		// Overwritten before unlinking, exactly as Create's own failure
		// path does: what is in there is the hook's plaintext dump and
		// the volume tarballs of an interrupted backup, and unlinking
		// alone would leave that more recoverable from free blocks than
		// the path this one stands in for.
		if err := atomicfs.RemoveWithOverwrite(dir); err == nil {
			atomicfs.SyncDir(e.paths.BackupsDir())
		}
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

// Timeouts for the quiesce pair.
//
// Stopping is bounded because a container that ignores SIGTERM should not hold
// a nightly backup open indefinitely -- but generously, because a database
// being quiesced for a volume copy is exactly the process that should be given
// time to flush. Resuming is bounded separately and more generously still,
// because it runs on a detached context after something has already gone wrong.
const (
	DefaultStopTimeout = 2 * time.Minute
	resumeTimeout      = 10 * time.Minute
)

func (e *Engine) stopTimeout() time.Duration {
	if e.stopTimeoutOverride > 0 {
		return e.stopTimeoutOverride
	}
	return DefaultStopTimeout
}

// detach returns a bounded context that survives the cancellation of its
// parent, for the cleanup that has to happen precisely when the operation was
// interrupted.
func detach(ctx context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), limit)
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, domain.BackupError(err, "cannot stat %s", path)
	}
	return info.Size(), nil
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
