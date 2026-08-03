package ops

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/lifecycle/engine"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"

	"github.com/goccy/go-yaml"
)

// InitOptions is what `init` needs to create an installation. Every field has
// a flag equivalent, so the whole command is scriptable without a TTY.
type InitOptions struct {
	Options

	// Product names the installation. When a release bundle is given it
	// defaults to the manifest's metadata.name.
	Product string

	// ReleasePath is an optional bundle to stage during init. Without it,
	// `init` creates the installation and `update` installs a release
	// later.
	ReleasePath string

	Profile string
	Domains []string

	// RecoveryRecipient is the offline age public key. Mandatory unless
	// explicitly waived: a machine that loses its identity with no second
	// recipient has lost its secrets permanently, and the moment to notice
	// that is now, not during a recovery.
	RecoveryRecipient string
	NoRecoveryKey     bool

	// InstallUnits controls systemd unit installation.
	InstallUnits   bool
	BackupSchedule string

	// GenerateSecrets creates every secret the release schema declares a
	// generator for.
	GenerateSecrets bool

	// RequireSignature and SigningKeys are this machine's verification
	// policy. They are set here rather than left to a later edit of
	// installation.yaml because the release staged during init is verified
	// against them too -- a policy that only took effect from the second
	// release would be a policy with a hole in it.
	RequireSignature bool
	SigningKeys      []string

	// Repair re-creates missing directories on an existing installation
	// instead of refusing.
	Repair bool

	Settings map[string]any
}

// Init creates a new installation.
//
// It does not start the product: `apply` follows. Separating them means a
// half-finished install leaves a machine with directories and keys, not with a
// partially-running deployment.
func Init(ctx context.Context, d *Deps, opts InitOptions) (Result, error) {
	if err := domain.ValidateProductName(opts.Product); err != nil {
		return Result{}, err
	}

	exists, err := d.State.InstallationExists(ctx)
	if err != nil {
		return Result{}, err
	}
	if exists && !opts.Repair {
		return Result{}, domain.InstallationError(domain.ErrAlreadyInstalled,
			"an installation already exists at %s", d.Paths.EtcDir).
			WithHint("use `morzer init --repair` to restore missing directories, " +
				"or `morzer update` to install a new release")
	}

	// An unsatisfiable policy is refused before anything is created, with
	// the same message Installation.Validate would give later. Discovering
	// it after the directories and the machine key exist would mean
	// unwinding a half-built installation over one missing flag.
	if opts.RequireSignature && len(opts.SigningKeys) == 0 {
		return Result{}, domain.Usage(
			"--require-signature needs at least one --signing-key").
			WithHint("no bundle could satisfy the policy otherwise; " +
				"pass the vendor's minisign public key")
	}

	if !opts.NoRecoveryKey && opts.RecoveryRecipient == "" {
		return Result{}, domain.Usage("a recovery recipient is required").
			WithHint("pass --recovery-recipient <age1...>, or --no-recovery-recipient " +
				"if you accept that losing this machine means losing its secrets")
	}
	if opts.RecoveryRecipient != "" {
		// Validated before anything is created: an unusable key
		// discovered after the identity exists would leave a
		// half-configured installation.
		if err := d.Secrets.ValidateRecipient(opts.RecoveryRecipient); err != nil {
			return Result{}, err
		}
	}

	opID := d.newOpID()

	op := engine.Operation{
		ID:          opID,
		Type:        domain.OpTypeInit,
		Description: "initialise " + opts.Product,
		Steps:       initSteps(d, opts),
		Flags:       initFlags(opts),
	}

	var result engine.Result
	runErr := d.withLock(ctx, opID, domain.OpTypeInit, opts.Options, func(ctx context.Context) error {
		var err error
		result, err = d.Engine.Run(ctx, op, d.engineOptions(opts.Options, "", nil))
		return err
	})

	out := Result{Record: result.Record}
	if runErr != nil {
		return out, runErr
	}

	inst := engine.MustGet[domain.Installation](result.State, engine.KeyInstallation)
	out.Summary = fmt.Sprintf("installation %s created for %s", inst.ID, inst.Product)
	out.Data = map[string]any{
		"installation_id": inst.ID,
		"product":         inst.Product,
		"etc_dir":         d.Paths.EtcDir,
	}
	return out, nil
}

func initFlags(opts InitOptions) map[string]string {
	flags := map[string]string{}
	if opts.NoRecoveryKey {
		// Recorded because it is the choice most likely to be regretted
		// during a recovery, and the journal is where that gets traced.
		flags["no_recovery_recipient"] = "true"
	}
	if opts.Repair {
		flags["repair"] = "true"
	}
	if len(flags) == 0 {
		return nil
	}
	return flags
}

func initSteps(d *Deps, opts InitOptions) []engine.Step {
	steps := []engine.Step{
		stepCreateDirectories(d),
		stepCreateIdentity(d),
		stepWriteInstallation(d, opts),
	}

	if opts.ReleasePath != "" {
		steps = append(steps, stepStageRelease(d, opts))
	}

	steps = append(steps,
		stepInitSecrets(d, opts),
		stepAddRecoveryRecipient(d, opts),
	)

	if opts.InstallUnits {
		steps = append(steps, stepInstallUnits(d, opts))
	}
	return steps
}

// stepCreateDirectories builds the managed layout with the right permissions.
func stepCreateDirectories(d *Deps) engine.Step {
	return engine.Step{
		ID:          "create-directories",
		Description: "create system directories",
		Idempotent:  true,
		OnFailure:   engine.Abort,
		Timeout:     time.Minute,
		Check: func(ctx context.Context, st *engine.State) (bool, error) {
			for _, dir := range d.Paths.ManagedDirs() {
				info, err := os.Stat(dir.Path)
				if err != nil || !info.IsDir() {
					return false, nil
				}
				if uint32(info.Mode().Perm()) != dir.Mode {
					return false, nil
				}
			}
			return true, nil
		},
		Execute: func(ctx context.Context, st *engine.State) error {
			for _, dir := range d.Paths.ManagedDirs() {
				// MkdirExact, not MkdirAll: the modes are a
				// security property. The 0700 secret directory
				// created under a permissive umask would be a
				// 0755 secret directory.
				if err := atomicfs.MkdirExact(dir.Path, os.FileMode(dir.Mode)); err != nil {
					return err
				}
			}
			st.Detail("%d directories", len(d.Paths.ManagedDirs()))
			return nil
		},
	}
}

// stepCreateIdentity generates the machine's age key.
//
// Not compensable on purpose: deleting an identity is how secret state becomes
// permanently unreadable, and a compensation that did it automatically would
// be a footgun aimed at the one file that cannot be regenerated.
func stepCreateIdentity(d *Deps) engine.Step {
	return engine.Step{
		ID:          "create-identity",
		Description: "create machine age identity",
		Idempotent:  true,
		OnFailure:   engine.Abort,
		Timeout:     time.Minute,
		Check: func(ctx context.Context, st *engine.State) (bool, error) {
			return atomicfs.Exists(d.Paths.AgeIdentityFile())
		},
		Execute: func(ctx context.Context, st *engine.State) error {
			pub, err := d.Secrets.EnsureIdentity(ctx)
			if err != nil {
				return err
			}
			st.Detail("public key %s", pub)
			return nil
		},
		Verify: func(ctx context.Context, st *engine.State) error {
			ok, detail, err := atomicfs.CheckMode(d.Paths.AgeIdentityFile(), 0o400)
			if err != nil {
				return err
			}
			if !ok {
				return domain.SecretsError(nil,
					"the age identity at %s is not 0400: %s", d.Paths.AgeIdentityFile(), detail)
			}
			return nil
		},
	}
}

// stepWriteInstallation writes installation.yaml and the state file.
func stepWriteInstallation(d *Deps, opts InitOptions) engine.Step {
	return engine.Step{
		ID:          "write-installation",
		Description: "write installation configuration",
		Idempotent:  true,
		OnFailure:   engine.Compensate,
		Timeout:     time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			inst, err := d.buildInstallation(ctx, opts)
			if err != nil {
				return err
			}
			st.Set(engine.KeyInstallation, inst)

			// installation.yaml is the operator-facing file; the
			// JSON state file is the manager's. Both are written so
			// a hand edit to the YAML is visible, and so the manager
			// never has to parse YAML on a hot path.
			data, err := yaml.Marshal(inst)
			if err != nil {
				return domain.Internal(err, "cannot serialise the installation")
			}
			header := "# Managed by morzer. Edit with care.\n" +
				"# Values here override release defaults; see `morzer status --json` for effective values.\n"
			if err := atomicfs.WriteFile(d.Paths.InstallationFile(), append([]byte(header), data...), 0o640); err != nil {
				return err
			}
			return d.State.SaveInstallation(ctx, inst)
		},
		Compensate: func(ctx context.Context, st *engine.State) error {
			_ = os.Remove(d.Paths.InstallationFile())
			_ = os.Remove(d.Paths.InstallationState())
			return nil
		},
	}
}

// buildInstallation constructs the installation, reusing the existing ID on a
// repair.
//
// Regenerating the ID would be silently destructive: backups are stamped with
// it, and restore checks against it, so a new ID would make every existing
// backup look like it belongs to a different machine.
func (d *Deps) buildInstallation(ctx context.Context, opts InitOptions) (domain.Installation, error) {
	inst := domain.Installation{
		SchemaVersion: domain.InstallationSchemaVersion,
		Product:       opts.Product,
		CreatedAt:     domain.NewTime(d.now()),
		Profile:       opts.Profile,
		Domains:       opts.Domains,
		Policy:        domain.DefaultPolicy(),
		Settings:      opts.Settings,
	}
	inst.Policy.RequireSignature = opts.RequireSignature
	inst.Policy.SigningKeys = opts.SigningKeys

	if existing, err := d.State.LoadInstallation(ctx); err == nil && existing.ID != "" {
		inst.ID = existing.ID
		inst.CreatedAt = existing.CreatedAt
		if opts.Profile == "" {
			inst.Profile = existing.Profile
		}
		if len(opts.Domains) == 0 {
			inst.Domains = existing.Domains
		}
		inst.Policy = existing.Policy
	} else {
		inst.ID = NewOperationID(d.now())
	}

	return inst, nil
}

// stepStageRelease copies a bundle into the release store.
func stepStageRelease(d *Deps, opts InitOptions) engine.Step {
	return engine.Step{
		ID:          "stage-release",
		Description: "stage release bundle",
		Idempotent:  true,
		OnFailure:   engine.Compensate,
		Timeout:     10 * time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			ref, err := ports.ParseRef(opts.ReleasePath)
			if err != nil {
				return err
			}

			resolved, err := d.Source.Resolve(ctx, ref)
			if err != nil {
				return err
			}

			dest := d.Paths.ReleaseDir(resolved.Version.String())
			if _, err := d.Source.Fetch(ctx, ref, dest); err != nil {
				return err
			}

			// Verified before it is trusted for anything: hooks run
			// only from a verified release.
			if d.Verifier != nil {
				expect := ports.Expectation{
					Digest:     resolved.Digest,
					Required:   opts.RequireSignature,
					PublicKeys: opts.SigningKeys,
				}
				if err := d.Verifier.Verify(ctx, ports.BundlePath(dest), expect); err != nil {
					return err
				}
			}

			rel, err := release.Load(dest)
			if err != nil {
				return err
			}
			st.Set(engine.KeyRelease, rel)

			if err := atomicfs.ReplaceSymlink(dest, d.Paths.CurrentLink()); err != nil {
				return err
			}
			return d.State.SetCurrentRelease(ctx, domain.ReleaseRecord{
				SchemaVersion: domain.InstallationSchemaVersion,
				Name:          rel.Name(),
				Version:       rel.Version(),
				Digest:        rel.Digest,
				Root:          dest,
				InstalledAt:   domain.NewTime(d.now()),
				OperationID:   st.OpID,
			})
		},
		Compensate: func(ctx context.Context, st *engine.State) error {
			rel, ok := st.Get(engine.KeyRelease)
			if !ok {
				return nil
			}
			if r, ok := rel.(domain.Release); ok {
				return atomicfs.RemoveAll(r.Root)
			}
			return nil
		},
	}
}

// stepInitSecrets creates the encrypted secret state and generates what it
// can.
func stepInitSecrets(d *Deps, opts InitOptions) engine.Step {
	return engine.Step{
		ID:          "init-secrets",
		Description: "create secret state",
		Idempotent:  true,
		OnFailure:   engine.Compensate,
		Timeout:     5 * time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			schema, err := d.secretSchema(st)
			if err != nil {
				return err
			}
			if len(schema.Secrets) == 0 {
				st.Detail("the release declares no secrets")
				return nil
			}

			existing, err := d.Secrets.Load(ctx)
			if err != nil {
				return err
			}

			generated := 0
			var manual []string
			for _, decl := range schema.Secrets {
				if existing.Has(decl.Name) {
					continue
				}
				if !decl.Generator.Auto() {
					if decl.Required {
						manual = append(manual, decl.Name)
					}
					continue
				}
				if !opts.GenerateSecrets {
					manual = append(manual, decl.Name)
					continue
				}
				if err := d.Secrets.Generate(ctx, decl.Name,
					ports.GenSpec{Generator: decl.Generator}); err != nil {
					return err
				}
				generated++
			}

			st.Detail("%d generated", generated)
			if len(manual) > 0 {
				// A warning, not a failure: `init` creating the
				// installation and `apply` refusing to start
				// without the secrets is the right division.
				st.Warn("secret(s) still to be set before `apply`: %v", manual)
			}
			return nil
		},
	}
}

// secretSchema resolves the release's secret schema, tolerating an
// installation created without a release.
func (d *Deps) secretSchema(st *engine.State) (domain.SecretSchema, error) {
	rel, ok := st.Get(engine.KeyRelease)
	if !ok {
		return domain.SecretSchema{}, nil
	}
	r, ok := rel.(domain.Release)
	if !ok {
		return domain.SecretSchema{}, nil
	}
	return release.LoadSecretSchema(r)
}

// stepAddRecoveryRecipient encrypts the state for the offline key.
func stepAddRecoveryRecipient(d *Deps, opts InitOptions) engine.Step {
	return engine.Step{
		ID:          "add-recovery-recipient",
		Description: "add offline recovery recipient",
		Idempotent:  true,
		OnFailure:   engine.Compensate,
		Timeout:     time.Minute,
		Check: func(ctx context.Context, st *engine.State) (bool, error) {
			if opts.RecoveryRecipient == "" {
				return true, nil
			}
			recipients, err := d.Secrets.Recipients(ctx)
			if err != nil {
				return false, err
			}
			for _, r := range recipients {
				if r.PublicKey == opts.RecoveryRecipient {
					return true, nil
				}
			}
			return false, nil
		},
		Execute: func(ctx context.Context, st *engine.State) error {
			if opts.RecoveryRecipient == "" {
				st.Warn("no recovery recipient: if this machine is lost, its secrets are lost with it")
				return nil
			}

			initialised, err := d.Secrets.Initialized(ctx)
			if err != nil {
				return err
			}
			if !initialised {
				// Nothing to encrypt for yet. The recipient is
				// added when the first secret is written, which
				// reuses the existing recipient list.
				st.Detail("deferred until the first secret is written")
				return nil
			}

			return d.Secrets.AddRecipient(ctx, ports.Recipient{
				PublicKey: opts.RecoveryRecipient,
				Kind:      ports.RecipientRecovery,
				Comment:   "offline recovery key added at init",
			})
		},
	}
}

// stepInstallUnits writes the systemd units.
func stepInstallUnits(d *Deps, opts InitOptions) engine.Step {
	return engine.Step{
		ID:          "install-units",
		Description: "install systemd units",
		Idempotent:  true,
		OnFailure:   engine.Compensate,
		Timeout:     2 * time.Minute,
		Check: func(ctx context.Context, st *engine.State) (bool, error) {
			if d.Supervisor == nil || !d.Supervisor.Available(ctx) {
				// No systemd is not an error: containers and
				// minimal images are legitimate hosts.
				return true, nil
			}
			return false, nil
		},
		Execute: func(ctx context.Context, st *engine.State) error {
			units, err := d.Supervisor.Units(ports.UnitParams{
				Product:        opts.Product,
				ManagerPath:    d.ManagerPath,
				ConfigPath:     d.Paths.InstallationFile(),
				BackupSchedule: opts.BackupSchedule,
			})
			if err != nil {
				return err
			}
			if err := d.Supervisor.InstallUnits(ctx, units); err != nil {
				return err
			}
			st.Detail("%d unit(s)", len(units))
			return nil
		},
		Compensate: func(ctx context.Context, st *engine.State) error {
			if d.Supervisor == nil {
				return nil
			}
			return d.Supervisor.RemoveUnits(ctx, d.Supervisor.ManagedUnitNames(opts.Product))
		},
	}
}
