package ops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/lifecycle/engine"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
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

	// Mode declares this machine a sandbox. Empty means production.
	//
	// Only settable here and at `import`, because those are the two ways an
	// installation is created and mode is fixed at creation -- see
	// domain.Installation.Mode. There is no command that changes it later,
	// deliberately and in both directions.
	Mode domain.Mode

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

	// Parameters are `--set name=value` assignments, validated against the
	// release's declarations before anything is created. A typo fails
	// before a directory exists rather than after a deployment is running
	// on a default the operator did not intend.
	Parameters map[string]string
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

	// A parameter can only be checked against a release that declares it,
	// and the whole value of declaring one is that setting it wrong is
	// caught. Accepting `--set` unverified would be a free-form map with a
	// flag in front of it.
	if len(opts.Parameters) > 0 && opts.ReleasePath == "" {
		return Result{}, domain.Usage("--set needs a release to validate against").
			WithHint("pass --release <bundle>, whose manifest declares which " +
				"parameters exist and what they accept")
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

	// A plan gets its deprecation warning here, from the bundle at its
	// source, because the one inside stepStageRelease reads the staged copy
	// and a plan stages nothing.
	//
	// Deliberately not moved out of the step for the real path. There it
	// reads the bundle *after* verification, and a bundle whose signature
	// does not check out should not have produced advice about its fields
	// on the way to being refused. A plan has no verified copy to read and
	// is already a statement about the source (RFC 0001 decision 12), so
	// the two paths read different copies for the same reason.
	if opts.DryRun && opts.ReleasePath != "" {
		d.warnPlannedDeprecations(opts.ReleasePath)
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

	// A plan populated no state, so there is no installation to read: every
	// step reported what it would do and none of them ran. Reading engine
	// state anyway is what printed "installation  created for " -- two empty
	// slots and a creation claimed in the past tense, directly beneath the
	// line saying nothing was changed.
	//
	// The product was never unknown. The CLI resolves it before the
	// operation, from --product or from the manifest at the bundle's source,
	// because every managed path derives from it -- so it arrives here in
	// opts and the plan can say whose installation it is describing.
	if opts.DryRun {
		out.Summary = fmt.Sprintf("would create an installation for %s", opts.Product)
		out.Data = map[string]any{
			"installation_id": "",
			"product":         opts.Product,
			"etc_dir":         d.Paths.EtcDir,
		}
		return out, nil
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
		// Before the installation is written, because the write records
		// the public half. The other order would leave state claiming
		// no key on a machine that has one.
		stepCreateSigningKey(d),
	}

	// Staging puts the bundle on disk and the release into the engine's
	// state, and it runs *before* the installation is written.
	//
	// That order used to be the other way round, and the reason it changed
	// is worth keeping: the installation records which runtime it is fixed
	// to, and the only place that fact exists is the release's manifest. A
	// write that ran first could not read it, so every installation recorded
	// an empty runtime -- which `Installation.RuntimeName` reads as the
	// legacy one, quietly resolving a release declared for another runtime
	// against the wrong adapter.
	//
	// Nothing is lost by the swap. The old order was justified by a `--set`
	// the manifest does not declare failing during staging, so the engine
	// unwound the installation the previous step had written; staging first
	// means there is no installation to unwind, which is the same outcome
	// reached earlier.
	if opts.ReleasePath != "" {
		steps = append(steps, stepStageRelease(d, opts))
	}

	steps = append(steps, stepWriteInstallation(d, opts))

	if opts.ReleasePath != "" {
		// Ingest reads the release back out of engine state, because
		// this list is built before anything has been resolved.
		steps = append(steps, stepIngestImages(d, stagedRelease()))
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

// stepCreateSigningKey mints the machine's signing identity.
//
// Beside stepCreateIdentity and not inside it: one key encrypts and one signs,
// they live in different directories, and a step that did both would report a
// single outcome for two things that fail for different reasons.
//
// Not compensable, for the same reason the age identity is not. Deleting a
// signing key is not as catastrophic -- old signatures stay verifiable against
// the recorded public key, and only the ability to make new ones is lost (RFC
// 0028 decision 7) -- but a compensation that removed it would still be
// automatic destruction of key material in response to an unrelated failure
// later in the operation.
//
// Idempotent through the port: EnsureKey reads before it mints, so this step
// running a second time on a machine that already signed returns the same key
// rather than orphaning every artifact that machine has emitted.
func stepCreateSigningKey(d *Deps) engine.Step {
	return engine.Step{
		ID:          "create-signing-key",
		Description: "create machine signing key",
		Idempotent:  true,
		OnFailure:   engine.Abort,
		Timeout:     time.Minute,
		Check: func(ctx context.Context, st *engine.State) (bool, error) {
			// No signer configured is "nothing to do", not a
			// failure. A build or a test that signs nothing must
			// still be able to create an installation -- and an
			// installation without a signing key is a state this
			// design already has to support, because every machine
			// that reaches schema 6 by migration is in it (RFC 0028
			// decision 9). Refusing here would make the absent case
			// unreachable through the one path that creates it.
			if d.Signer == nil {
				return true, nil
			}

			// Deliberately *not* "the key file exists". Execute is
			// what publishes the public key for the installation
			// record, so skipping it on file-existence alone left
			// `init --repair` unable to record a key that is on
			// disk and missing from state -- which is exactly the
			// state `doctor` reports with the remedy "run `morzer
			// init --repair` to record it". The remedy did not
			// work, and the warning survived the repair.
			//
			// EnsureKey reads before it mints, so running Execute
			// against an existing key is safe and returns that
			// same key. Never reporting done is the simpler
			// correct answer than a Check that has to know what
			// state records.
			return false, nil
		},
		Execute: func(ctx context.Context, st *engine.State) error {
			key, err := d.Signer.EnsureKey(ctx)
			if err != nil {
				return err
			}
			// Into engine state so stepWriteInstallation records it,
			// rather than each of them asking the port separately
			// and risking two answers.
			st.Set(engine.KeySigningKey, key.Line)
			st.Detail("key %s", key.KeyID)
			return nil
		},
		Verify: func(ctx context.Context, st *engine.State) error {
			if d.Signer == nil {
				return nil
			}
			ok, detail, err := atomicfs.CheckMode(d.Paths.SigningKeyFile(), 0o400)
			if err != nil {
				return err
			}
			if !ok {
				return domain.SecretsError(nil,
					"the signing key at %s is not 0400: %s",
					d.Paths.SigningKeyFile(), detail)
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
			inst, err := d.buildInstallation(ctx, st, opts)
			if err != nil {
				return err
			}
			st.Set(engine.KeyInstallation, inst)

			return d.saveInstallation(ctx, inst)
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
func (d *Deps) buildInstallation(ctx context.Context, st *engine.State, opts InitOptions) (domain.Installation, error) {
	// Trimmed once, here, because both branches below store it and the
	// validator refuses what it is given rather than a tidied copy. An
	// untrimmed value means the guard checked one string and the unit file
	// received another; a whitespace-only one is *non-empty*, so it slips
	// past the supervisor's fallback to the nightly default and renders an
	// `OnCalendar=` with nothing after it. `config set` has always trimmed.
	backupSchedule := strings.TrimSpace(opts.BackupSchedule)

	inst := domain.Installation{
		SchemaVersion: domain.InstallationSchemaVersion,
		Product:       opts.Product,
		CreatedAt:     domain.NewTime(d.now()),
		Mode:          opts.Mode,
		Profile:       opts.Profile,
		Domains:       opts.Domains,
		Policy:        domain.DefaultPolicy(),
		Parameters:    opts.Parameters,
	}
	runtime, err := d.runtimeForNewInstallation(st)
	if err != nil {
		return domain.Installation{}, err
	}
	inst.Runtime = runtime
	// What the runtime was told, recorded beside which runtime it is. These
	// name the volumes, so a later release that changes them is a release
	// pointing this deployment at storage it has never written to -- and the
	// refusal that catches it needs a baseline, which is this.
	//
	// An empty map when the release declared none, never nil: empty is the
	// claim "nothing was declared", which is a fact about this installation,
	// and nil means "created before schema 10", which is a fact about the
	// manager that wrote it.
	inst.RuntimeOptions = runtimeOptionsFor(st, runtime)

	inst.Policy.RequireSignature = opts.RequireSignature
	inst.Policy.SigningKeys = opts.SigningKeys
	inst.Policy.BackupSchedule = backupSchedule

	// The key the minting step just produced, so state records what is
	// actually on disk rather than what a second call to the port would
	// return. Empty when this init did not mint one, which is why the
	// repair branch below preserves whatever was already recorded.
	if key := engine.MustGet[string](st, engine.KeySigningKey); key != "" {
		inst.Signing.PublicKey = key
	}

	salt, err := newAttestationSalt()
	if err != nil {
		return domain.Installation{}, err
	}
	inst.AttestationSalt = salt

	if existing, err := d.State.LoadInstallation(ctx); err == nil && existing.ID != "" {
		inst.ID = existing.ID
		inst.CreatedAt = existing.CreatedAt

		// The runtime is carried, never rebuilt (RFC 0023 decision 3).
		//
		// Rebuilding it from the release looks harmless and is the
		// transition the decision forbids, arriving by the back door: a
		// vendor who moved from one runtime to another between releases
		// would have `init --repair` silently re-point an installation
		// whose volumes and image references belong to the old one.
		// Carried even when empty, because empty is what a machine
		// created before schema 9 records and rewriting it would erase
		// the difference between "predates the field" and "chose this".
		inst.Runtime = existing.Runtime

		// And what it was told, carried for the same reason and with a
		// sharper consequence. Rebuilding the options from the release
		// during a repair would adopt a renamed project in the one
		// command an operator runs *because* something is already
		// wrong, and the deployment would come up against empty
		// volumes with the real ones still on the disk.
		inst.RuntimeOptions = existing.RuntimeOptions

		// A repair keeps the signing identity and the salt. Both are
		// carried rather than regenerated for the same reason the
		// installation id is: re-minting the salt breaks the digest
		// chain on the machine that most needs it, and re-recording a
		// key would let `init --repair` disagree with the key file
		// this run did not touch.
		//
		// PreviousKeys always, PublicKey only when this run did not
		// mint -- an operator repairing a machine whose key file was
		// lost gets the new key recorded, not the stale one.
		inst.Signing.PreviousKeys = existing.Signing.PreviousKeys
		if inst.Signing.PublicKey == "" {
			inst.Signing.PublicKey = existing.Signing.PublicKey
		}
		if existing.AttestationSalt != "" {
			inst.AttestationSalt = existing.AttestationSalt
		}
		if opts.Profile == "" {
			inst.Profile = existing.Profile
		}
		if len(opts.Domains) == 0 {
			inst.Domains = existing.Domains
		}
		inst.Policy = existing.Policy
		if backupSchedule != "" {
			// The one field of Policy this command can set, so it
			// outranks the carried block when it was given -- the
			// same rule as Profile and Domains above. Tested on the
			// trimmed value, so `--backup-schedule "   "` is "not
			// given" rather than an instruction to blank the window
			// an operator already had.
			inst.Policy.BackupSchedule = backupSchedule
		}

		// Everything an operator arranged *after* `init`, carried
		// because this command did not create it and has no business
		// removing it.
		//
		// It used to rebuild all three from a fresh struct, so a repair
		// -- run precisely because something was already wrong --
		// silently dropped the backup targets, the notification targets
		// and the update channel. An operator found out at the next
		// backup, during a recovery, or never. `--repair` re-creates a
		// layout; it is not a second `init`, and the fields below are the
		// difference.
		inst.Update = existing.Update
		inst.Notify = existing.Notify
		inst.Backup = existing.Backup

		// A repair inherits the mode rather than resetting it. `init
		// --repair` on a sandbox is re-creating directories, not
		// deciding what the machine is for -- and the state store
		// refuses the change anyway, so without this an operator
		// repairing a sandbox would meet a refusal about modes when
		// they asked about a missing directory.
		if opts.Mode == "" {
			inst.Mode = existing.Mode
		}
	} else {
		inst.ID = NewOperationID(d.now())
	}

	return inst, nil
}

// newAttestationSalt mints the per-installation salt for the attestation's
// rendered-configuration digest.
//
// 32 bytes from crypto/rand, hex. The digest it salts is over a handful of
// ports, hostnames and booleans, so an unsalted one would be brute-forceable
// back to its inputs by anybody holding an attestation -- which is the point of
// RFC 0025 decision 4, and the reason parameter *values* never appear in the
// document at all.
func newAttestationSalt() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", domain.Internal(err, "cannot generate an attestation salt")
	}
	return hex.EncodeToString(b[:]), nil
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
			d.warnDeprecations(rel.Manifest)

			// Before the release is adopted: a `--set` the manifest
			// does not declare fails here, and no installation has
			// been written yet -- so there is nothing configured
			// with a value that decides nothing, and nothing for the
			// engine to unwind.
			if _, err := domain.ResolveParameters(rel.Manifest.Parameters, opts.Parameters); err != nil {
				return err
			}

			// A declaration with no default is the manifest saying
			// "you must choose this", and `init` is the one command
			// that can be told. Refused here rather than inside
			// ResolveParameters, which every later operation calls:
			// an apply reading months-old state cannot supply a
			// value, and taking a deployment down over a knob
			// nobody touched would be worse than the empty string.
			if missing := domain.MissingValues(rel.Manifest.Parameters, opts.Parameters); len(missing) > 0 {
				return domain.Usage(
					"release %s declares no default for %s",
					rel.Version(), strings.Join(missing, ", ")).
					WithHint("pass --set %s=<value> (repeat --set for several)", missing[0])
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

// runtimeForNewInstallation decides which runtime this installation is fixed
// to, and refuses rather than guessing (RFC 0023 decisions 3 and 5).
//
// The rule is deliberately narrow while there is one adapter. A release
// declaring exactly one runtime fixes the installation to it. A release
// declaring several is refused, because choosing between them means knowing
// which this manager can actually drive, and the only honest answers to that
// today are a branch on a runtime's name -- which decision 7 forbids and
// `tools/runtimecheck` fails the build over -- or a name injected at the
// composition root that every test would set and no test would exercise as
// production leaves it. Refusing costs a bundle nobody ships yet; either
// alternative costs the architecture test this RFC exists to run.
//
// An installation created with no release at all records nothing, and
// Installation.RuntimeName reads that as the legacy runtime.
func (d *Deps) runtimeForNewInstallation(st *engine.State) (string, error) {
	rel, ok := st.Get(engine.KeyRelease)
	if !ok {
		return "", nil
	}
	r, ok := rel.(domain.Release)
	if !ok {
		return "", nil
	}
	return runtimeForRelease(r)
}

// runtimeOptionsFor reads the staged release's options for one runtime.
//
// Always non-nil for an installation this manager creates, including when the
// release declares nothing: see the field's own documentation for why the empty
// map and the absent one have to stay distinguishable.
func runtimeOptionsFor(st *engine.State, runtime string) map[string]string {
	options := map[string]string{}

	rel, ok := st.Get(engine.KeyRelease)
	if !ok {
		return options
	}
	r, ok := rel.(domain.Release)
	if !ok {
		return options
	}
	declared, _ := r.Manifest.DeclaredRuntimes()
	for key, value := range declared[runtime].Options {
		options[key] = value
	}
	return options
}

// runtimeForRelease is the decision itself, split from the state plumbing above
// so it can be tested without an engine run.
//
// Not a stylistic split: `engine.State` has no exported constructor, so a test
// of the rule would otherwise have to drive a whole operation to reach three
// lines of `switch`. A decision that can only be exercised through the machinery
// around it is a decision that gets exercised once, by the happy path.
func runtimeForRelease(r domain.Release) (string, error) {
	declared, _ := r.Manifest.DeclaredRuntimes()
	names := declared.Names()
	switch len(names) {
	case 0:
		return "", nil
	case 1:
		return names[0], nil
	default:
		return "", domain.ValidationError(nil,
			"this release declares %d runtimes and the manager cannot yet choose between them",
			len(names)).
			WithHint("it declares: %s — installing a release that declares more than one "+
				"arrives with the second runtime adapter (RFC 0023 P3)",
				strings.Join(names, ", "))
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
			// Through the same computation the reconciliation uses,
			// on the installation this operation just wrote. Two
			// spellings of "which units does this machine want" is
			// how `init --repair` came to install a set that did not
			// match what a later `config set` would install -- and
			// how the remedy `doctor` prints came to be one that
			// could not clear the warning it printed.
			inst := engine.MustGet[domain.Installation](st, engine.KeyInstallation)
			units, err := d.Supervisor.Units(d.unitParams(inst))
			if err != nil {
				return err
			}
			// EnableAll, unlike the reconciliation, and the pair is
			// the whole of RFC 0030 row 1. `init` has nothing to
			// overrule; `init --repair` is a command somebody runs
			// *because* they found this machine wrong, so reversing
			// a `systemctl disable` is a legitimate thing for it to
			// do -- and the only place it happens.
			if err := d.Supervisor.InstallUnits(ctx, units, ports.EnableAll); err != nil {
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
