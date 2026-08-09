package ops

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/lifecycle/engine"
	"github.com/morzecrew/morzer/internal/ports"
)

// ExportOptions configures `installation export`.
type ExportOptions struct {
	Options

	// Path is the file to write. It is not defaulted: an export is a file
	// an operator has to deliberately put somewhere off this machine, and a
	// default location would be one they forget to move.
	Path string
}

// Export writes everything a rebuilt machine needs to become this one.
//
// Read-only, so it takes no lock and writes no journal entry -- for the same
// reason `status` does not. It is also the only operation an operator can run
// safely at any time, which matters: an export is worth nothing if taking one
// is something they hesitate over.
//
// What it produces is not a backup. It carries identity and secrets, never
// data; see domain.InstallationExport.
func Export(ctx context.Context, d *Deps, opts ExportOptions) (Result, error) {
	if strings.TrimSpace(opts.Path) == "" {
		return Result{}, domain.Usage("no destination given").
			WithHint("run `morzer installation export <path>`")
	}

	exists, err := atomicfs.Exists(opts.Path)
	if err != nil {
		return Result{}, err
	}
	if exists && !opts.Force {
		return Result{}, domain.Usage("%s already exists", opts.Path).
			WithHint("choose another path, or pass --force to overwrite it")
	}

	export, err := BuildExport(ctx, d)
	if err != nil {
		return Result{}, err
	}
	inst := export.Installation

	data, err := yaml.Marshal(export)
	if err != nil {
		return Result{}, domain.Internal(err, "cannot serialise the installation export")
	}

	header := "# morzer installation export.\n" +
		"# Contains the installation identity and the ENCRYPTED secret state.\n" +
		"# It carries no application data -- that is what `morzer backup` is for.\n" +
		"# Store it somewhere the machine it came from cannot reach.\n"

	if opts.DryRun {
		return Result{
			Summary: fmt.Sprintf("would write an export of %s to %s", inst.ID, opts.Path),
			Data:    exportSummary(export, opts.Path),
		}, nil
	}

	// 0600: the ciphertext is useless without a key, but the installation
	// record inside names domains, policy and the layout of a production
	// deployment. There is no reason for it to be world-readable.
	if err := atomicfs.WriteFile(opts.Path, append([]byte(header), data...), 0o600); err != nil {
		return Result{}, err
	}

	return Result{
		Summary: fmt.Sprintf("exported installation %s to %s", inst.ID, opts.Path),
		Data:    exportSummary(export, opts.Path),
	}, nil
}

func exportSummary(e domain.InstallationExport, path string) map[string]any {
	kinds := make([]string, 0, len(e.Secrets.Recipients))
	for _, r := range e.Secrets.Recipients {
		kinds = append(kinds, r.Kind)
	}
	out := map[string]any{
		"path":            path,
		"installation_id": e.Installation.ID,
		"product":         e.Installation.Product,
		"recipients":      kinds,
	}
	if !e.Release.IsZero() {
		out["release"] = e.Release
	}
	return out
}

// BuildExport assembles the document `installation export` writes.
//
// Exported because a backup carries the same document, and RFC 0017 decision 2
// makes that the *same* document rather than a second one that resembles it.
// Two producers of a recovery payload is the situation that RFC was written to
// fix: the backup used to copy the operator-facing `installation.yaml`, which
// this codebase already ships a `doctor` check for because it drifts from the
// authoritative state.
//
// So there is one builder, and the equivalence is pinned by a test that
// decrypts a backup's component and compares it against this function's output
// on the same machine at the same moment.
//
// It validates before returning. An export that only turns out to be unusable
// during a recovery is worse than no export at all, because the operator
// stopped looking for alternatives — and a backup is the artifact least likely
// to be checked before it is needed.
func BuildExport(ctx context.Context, d *Deps) (domain.InstallationExport, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return domain.InstallationExport{}, err
	}

	store, err := d.recoverableSecrets()
	if err != nil {
		return domain.InstallationExport{}, err
	}

	state, err := store.ExportState(ctx)
	if err != nil {
		return domain.InstallationExport{}, err
	}

	recipients, err := store.Recipients(ctx)
	if err != nil {
		return domain.InstallationExport{}, err
	}

	export := domain.InstallationExport{
		APIVersion:     domain.APIVersionV1Alpha1,
		Kind:           domain.KindInstallationExport,
		ExportedAt:     domain.NewTime(d.now()),
		ManagerVersion: d.ManagerVersion,
		SourceHost:     hostname(),
		Installation:   inst,
		Secrets: domain.ExportedSecrets{
			State:      string(state),
			Recipients: exportRecipients(recipients),
		},
		Release: currentExportedRelease(ctx, d),
	}

	if err := export.Validate(); err != nil {
		return domain.InstallationExport{}, err
	}
	return export, nil
}

// ExportForBackup builds the export a backup carries, or reports that this
// installation has none to carry.
//
// The second return is not an error because it is not a failure. An
// installation created with `--no-recovery-recipient` has no offline key to
// encrypt an identity bundle to, and RFC 0017 decision 11 says such a backup
// gets no export component rather than one encrypted to the machine's own key
// -- which would be readable by exactly the key that dies with the machine,
// the appearance of a recovery path with none of the substance.
//
// The check is made on the recipients, before the document is assembled,
// because BuildExport *refuses* an export whose only recipient is the
// exporting machine. Letting that refusal reach the backup engine would turn a
// deliberate operator choice into a failed backup, on every backup, for ever.
//
// A secret provider that cannot export at all is the same shape of answer: it
// is a property of the configuration rather than a fault, and a deployment
// using one still takes backups.
func ExportForBackup(ctx context.Context, d *Deps) (domain.InstallationExport, bool, error) {
	store, err := d.recoverableSecrets()
	if err != nil {
		return domain.InstallationExport{}, false, nil
	}

	recipients, err := store.Recipients(ctx)
	if err != nil {
		return domain.InstallationExport{}, false, err
	}
	if !hasRecoveryRecipient(recipients) {
		return domain.InstallationExport{}, false, nil
	}

	export, err := BuildExport(ctx, d)
	if err != nil {
		return domain.InstallationExport{}, false, err
	}
	return export, true, nil
}

func hasRecoveryRecipient(in []ports.Recipient) bool {
	for _, r := range in {
		if r.Kind == ports.RecipientRecovery {
			return true
		}
	}
	return false
}

// exportRecipients converts port recipients into the document's own
// vocabulary. The kinds are identical strings; the conversion exists because
// domain cannot import ports.
func exportRecipients(in []ports.Recipient) []domain.ExportedRecipient {
	out := make([]domain.ExportedRecipient, 0, len(in))
	for _, r := range in {
		out = append(out, domain.ExportedRecipient{
			PublicKey: r.PublicKey,
			Kind:      string(r.Kind),
			Comment:   r.Comment,
		})
	}
	return out
}

// currentExportedRelease records what was running, tolerating nothing being
// installed. An installation with no release is a legitimate state -- `init`
// without `--release` produces one -- and is not a reason to refuse an export.
func currentExportedRelease(ctx context.Context, d *Deps) domain.ExportedRelease {
	current, err := d.State.CurrentRelease(ctx)
	if err != nil || current.IsZero() {
		return domain.ExportedRelease{}
	}
	return domain.ExportedRelease{
		Name:    current.Name,
		Version: current.Version,
		Digest:  current.Digest,
	}
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return name
}

// LoadExport reads and validates an export file.
//
// Exported because the CLI needs the product name before it can build the
// managed paths: every directory the manager touches derives from it, and on a
// rebuilt machine the export is the only place it is written down.
func LoadExport(path string) (domain.InstallationExport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return domain.InstallationExport{}, domain.ValidationError(err,
			"cannot read the export at %s", path)
	}

	var export domain.InstallationExport
	if err := yaml.Unmarshal(data, &export); err != nil {
		return domain.InstallationExport{}, domain.ValidationError(err,
			"%s is not a valid installation export", path).
			WithHint("it should begin with `api_version: %s`", domain.APIVersionV1Alpha1)
	}
	if err := export.Validate(); err != nil {
		return domain.InstallationExport{}, err
	}
	return export, nil
}

// ImportOptions configures `installation import`.
type ImportOptions struct {
	Options

	// SourcePath is where the export came from, for messages.
	SourcePath string

	// Export is the parsed document. The CLI loads it before this runs,
	// because the product name inside it determines every managed path.
	Export domain.InstallationExport

	// IdentityFile is the offline key that can decrypt the export. There is
	// no default: the whole point is that this key was not on the machine
	// that was lost, so it cannot be somewhere the manager already looks.
	IdentityFile string
}

// Import rebuilds a machine from an export and an offline recovery key.
//
// It restores the installation's original ID. That looks wrong and is
// deliberate: backups are stamped with it and `restore` checks against it, so a
// rebuilt machine with a fresh ID could not restore its own backups -- which is
// the entire point of having got this far. The consequence is stated back to
// the operator: the source machine must be decommissioned, because two live
// machines sharing an installation ID is a genuine problem.
//
// It leaves the release uninstalled. `update` and `apply` follow, then
// `restore`. Bundles are not carried in an export -- they are content-addressed
// and fetchable -- and pretending otherwise would make an export enormous for
// no gain.
func Import(ctx context.Context, d *Deps, opts ImportOptions) (Result, error) {
	export := opts.Export
	if err := export.Validate(); err != nil {
		return Result{}, err
	}

	store, err := d.recoverableSecrets()
	if err != nil {
		return Result{}, err
	}

	exists, err := d.State.InstallationExists(ctx)
	if err != nil {
		return Result{}, err
	}
	if exists && !opts.Force {
		return Result{}, domain.InstallationError(domain.ErrAlreadyInstalled,
			"an installation already exists at %s", d.Paths.EtcDir).
			WithHint("importing would replace its identity and secret state; " +
				"pass --force if that is what you mean")
	}

	// The recovery identity is checked against the export's recipients
	// before anything is created. The alternative is discovering, after the
	// directories and a new machine key exist, that the key in the
	// operator's hand cannot open the file -- at the worst possible moment,
	// with the least useful error.
	recoveryStore := store.WithIdentity(opts.IdentityFile)
	recoveryKey, err := recoveryStore.IdentityPublicKey(ctx)
	if err != nil {
		return Result{}, domain.SecretsError(err,
			"cannot use the recovery identity at %s", opts.IdentityFile).
			WithHint("pass --identity <file>, pointing at the private key " +
				"printed by `morzer secret recipients generate-recovery-key`")
	}
	if err := checkRecoveryKey(export, recoveryKey, opts.IdentityFile); err != nil {
		return Result{}, err
	}

	if opts.DryRun {
		return Result{
			Summary: fmt.Sprintf("would import installation %s (%s) from %s",
				export.Installation.ID, export.Installation.Product, opts.SourcePath),
			Data: importSummary(export, recoveryKey, ""),
		}, nil
	}

	opID := d.newOpID()
	op := engine.Operation{
		ID:          opID,
		Type:        domain.OpTypeImport,
		Description: "import installation " + export.Installation.ID,
		Steps:       importSteps(d, export, opts, recoveryStore),
		Flags:       importFlags(opts),
	}

	var result engine.Result
	runErr := d.withLock(ctx, opID, domain.OpTypeImport, opts.Options, func(ctx context.Context) error {
		var err error
		result, err = d.Engine.Run(ctx, op, d.engineOptions(opts.Options, export.Installation.ID, nil))
		return err
	})

	out := Result{Record: result.Record}
	if runErr != nil {
		return out, runErr
	}

	machineKey, _ := d.Secrets.IdentityPublicKey(ctx)
	out.Summary = fmt.Sprintf("imported installation %s for %s",
		export.Installation.ID, export.Installation.Product)
	out.Data = importSummary(export, recoveryKey, machineKey)
	return out, nil
}

func importFlags(opts ImportOptions) map[string]string {
	flags := map[string]string{"source": opts.SourcePath}
	if opts.Force {
		// Recorded because it means an existing installation's identity
		// was replaced, which an incident review will want to see.
		flags["force"] = "true"
	}
	return flags
}

func importSummary(e domain.InstallationExport, recoveryKey, machineKey string) map[string]any {
	out := map[string]any{
		"installation_id": e.Installation.ID,
		"product":         e.Installation.Product,
		"recovery_key":    recoveryKey,
		"source_host":     e.SourceHost,
		"exported_at":     e.ExportedAt,
	}
	if machineKey != "" {
		out["machine_key"] = machineKey
	}
	if !e.Release.IsZero() {
		out["release"] = e.Release
	}
	return out
}

// checkRecoveryKey refuses an identity the export was not encrypted for.
func checkRecoveryKey(export domain.InstallationExport, key, path string) error {
	for _, r := range export.Secrets.Recipients {
		if strings.TrimSpace(r.PublicKey) == strings.TrimSpace(key) {
			return nil
		}
	}

	known := make([]string, 0, len(export.Secrets.Recipients))
	for _, r := range export.Secrets.Recipients {
		known = append(known, fmt.Sprintf("%s (%s)", r.PublicKey, r.Kind))
	}
	return domain.SecretsError(nil,
		"the identity at %s is not a recipient of this export", path).
		WithHint("its public key is %s; the export can be opened by: %s",
			key, strings.Join(known, ", "))
}

// recoverableSecrets asserts the configured provider supports recovery.
//
// A provider that cannot be re-opened under another identity cannot participate
// in export or import at all, and saying so by name is better than failing
// three steps in with a message about age keys.
func (d *Deps) recoverableSecrets() (ports.RecoverableSecretStore, error) {
	store, ok := d.Secrets.(ports.RecoverableSecretStore)
	if !ok {
		return nil, domain.Usage(
			"the configured secret provider does not support export or import").
			WithHint("this needs a provider whose state can be re-opened with an " +
				"offline key; sops-age is the one that can")
	}
	return store, nil
}

// importSteps rebuilds the machine.
//
// The order is the design: directories, then the installation record, then the
// state the recovery key can still read, then a new machine key, and only then
// the re-encryption that grants this host access and revokes the old one's.
// Doing the re-encryption earlier would mean re-encrypting for a machine key
// that does not exist yet.
func importSteps(
	d *Deps,
	export domain.InstallationExport,
	opts ImportOptions,
	recoveryStore ports.SecretStore,
) []engine.Step {
	return []engine.Step{
		stepCreateDirectories(d),
		stepWriteImportedInstallation(d, export),
		stepRestoreSecretState(d, export),
		stepCreateIdentity(d),
		stepAdoptSecretState(d, export, opts, recoveryStore),
	}
}

// stepWriteImportedInstallation writes the exported installation verbatim.
func stepWriteImportedInstallation(d *Deps, export domain.InstallationExport) engine.Step {
	return engine.Step{
		ID:          "write-installation",
		Description: "restore installation " + export.Installation.ID,
		Idempotent:  true,
		OnFailure:   engine.Compensate,
		Timeout:     time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			inst := export.Installation
			// The ID and CreatedAt come across untouched. Everything
			// that keys on them -- backups above all -- keeps
			// working, which is the reason to import rather than
			// re-init.
			data, err := yaml.Marshal(inst)
			if err != nil {
				return domain.Internal(err, "cannot serialise the installation")
			}
			header := "# Managed by morzer. Restored by `installation import`.\n" +
				"# The installation ID is the original one, so existing backups remain restorable.\n"
			if err := atomicfs.WriteFile(d.Paths.InstallationFile(),
				append([]byte(header), data...), 0o640); err != nil {
				return err
			}
			st.Set(engine.KeyInstallation, inst)
			st.Detail("id %s", inst.ID)
			return d.State.SaveInstallation(ctx, inst)
		},
		Compensate: func(ctx context.Context, st *engine.State) error {
			_ = os.Remove(d.Paths.InstallationFile())
			_ = os.Remove(d.Paths.InstallationState())
			return nil
		},
	}
}

// stepRestoreSecretState writes the exported ciphertext into place.
//
// Compensating by deleting it is safe in a way that deleting secret state
// almost never is: the export still holds every byte, so the worst case is
// running the import again.
func stepRestoreSecretState(d *Deps, export domain.InstallationExport) engine.Step {
	return engine.Step{
		ID:          "restore-secret-state",
		Description: "restore encrypted secret state",
		Idempotent:  true,
		OnFailure:   engine.Compensate,
		Timeout:     time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			store, err := d.recoverableSecrets()
			if err != nil {
				return err
			}
			if err := store.ImportState(ctx, []byte(export.Secrets.State)); err != nil {
				return err
			}
			st.Detail("%d recipient(s) from the export", len(export.Secrets.Recipients))
			return nil
		},
		Compensate: func(ctx context.Context, st *engine.State) error {
			_ = os.Remove(d.Paths.SecretsFile())
			return nil
		},
	}
}

// stepAdoptSecretState re-encrypts for this machine and revokes the old one.
//
// This is the step the whole feature exists for. It reads through the offline
// recovery identity -- the only key that can still open the file -- and writes
// back a recipient set built around a key that did not exist five seconds ago.
//
// The old machine's key is dropped rather than kept. A decommissioned host must
// not retain the ability to decrypt: if it is being replaced because it was
// compromised, keeping its key would make the rebuild ceremonial.
func stepAdoptSecretState(
	d *Deps,
	export domain.InstallationExport,
	opts ImportOptions,
	recoveryStore ports.SecretStore,
) engine.Step {
	return engine.Step{
		ID:          "adopt-secret-state",
		Description: "re-encrypt for this machine",
		Idempotent:  true,
		OnFailure:   engine.Compensate,
		Timeout:     5 * time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			machineKey, err := d.Secrets.IdentityPublicKey(ctx)
			if err != nil {
				return err
			}

			next := []ports.Recipient{{
				PublicKey: machineKey,
				Kind:      ports.RecipientMachine,
				Comment:   "machine key created by `installation import`",
			}}
			for _, r := range export.NonMachineRecipients() {
				next = append(next, ports.Recipient{
					PublicKey: r.PublicKey,
					Kind:      ports.RecipientKind(r.Kind),
					Comment:   r.Comment,
				})
			}

			// Through the recovery identity: this host's brand-new
			// key is not yet a recipient, so its own store cannot
			// read the file it is about to rewrite.
			if err := recoveryStore.ReencryptFor(ctx, next); err != nil {
				return err
			}

			st.Detail("%d recipient(s), machine key %s", len(next), machineKey)
			if dropped := droppedMachineKeys(export, machineKey); len(dropped) > 0 {
				st.Warn("revoked the previous machine key %s; that host can no longer decrypt",
					strings.Join(dropped, ", "))
			}
			return nil
		},
		Verify: func(ctx context.Context, st *engine.State) error {
			// The claim is that *this* machine can now read the
			// state. Nothing short of reading it proves that, and a
			// failure here is far cheaper now than at the next
			// `apply`.
			if _, err := d.Secrets.Load(ctx); err != nil {
				return domain.SecretsError(err,
					"the secret state is still not readable by this machine").
					WithHint("re-run `morzer installation import %s --identity %s --force`",
						opts.SourcePath, opts.IdentityFile)
			}
			return nil
		},
	}
}

// droppedMachineKeys names the machine keys the import revoked.
func droppedMachineKeys(export domain.InstallationExport, machineKey string) []string {
	var out []string
	for _, r := range export.Secrets.Recipients {
		if r.Kind == domain.RecipientKindMachine && r.PublicKey != machineKey {
			out = append(out, r.PublicKey)
		}
	}
	return out
}
