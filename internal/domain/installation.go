package domain

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
)

// InstallationSchemaVersion is the second of the three versioned contracts:
// what the manager can migrate. Bumped only when the on-disk shape changes in
// a way a previous manager would misread.
// Bumped to 2 when `settings` became `parameters`. encoding/json ignores
// unknown fields, so without the bump an older manager would read a newer
// state file, see no `settings`, and silently run the whole deployment on
// default parameters -- a wrong port rather than a refusal.
//
// Bumped to 3 when `backup.targets` arrived, for exactly the same reason and a
// worse consequence: an older manager reads the state, sees no targets, takes a
// backup, reports success, and leaves it on the machine the operator configured
// a target to survive. A refusal naming the manager version is the only honest
// answer, because the failure is otherwise invisible until the disaster.
//
// Bumped to 4 when `notify.targets` arrived, which is the same shape once more:
// an older manager sees no targets, runs an operation, reports success, and the
// operator who configured a way to be told is never told. Schema 4 names this
// shape and no other -- a second field set under one version would let a
// manager implementing only one of them rewrite the state and silently drop
// the other's fields.
//
// Bumped to 5 when `mode` arrived, and this one is about the *write* path
// rather than the read. Reading is the safe direction: an older binary that
// sees no mode treats the machine as production, which is stricter. Writing is
// not -- `config set` rewrites the state, unknown fields are dropped on the way
// through, and a dev sandbox touched once by an older binary would silently
// present as production ever after. Refusing a state file from the future is
// what prevents that, and it needs a version of its own: two field sets both
// called schema 4 would let a manager implementing only one of them rewrite a
// file written by the other, dropping fields it had never heard of.
// Bumped to 6 when `signing` arrived (RFC 0028). The read direction is safe --
// an older binary that sees no signing block behaves as it always did -- and
// the bump is for the write path, the same shape as 5: `config set` rewrites
// the state, unknown fields are dropped on the way through, and one pass by an
// older binary would silently discard the public key and the whole succession
// record. Losing `previous_keys` is the expensive half: it is the only thing
// that lets a verifier say "signed by a predecessor of this installation"
// rather than "unknown signer", and nothing regenerates it.
// Bumped to 7 when `policy.backup_schedule` arrived, and for the write path
// again: an older manager's `config set` rewrites the whole state, drops the
// field on the way through, and the next reconciliation renders the default --
// which is the exact defect persisting the schedule was meant to fix, arriving
// from the other direction.
// Bumped to 8 for `policy.skip_scheduled_backups` (RFC 0030 row 4). The write
// path again, and this one is the sharpest of the four: *without* the bump an
// older manager drops the field on its next `config set`, and its reconciliation
// does not merely render a default -- it installs a backup timer on a machine
// that declared it wanted none, on hardware the operator may have arranged to
// snapshot at the storage layer.
//
// What the bump actually does, in both directions, is refuse. `Validate`
// rejects a schema from the future, so an older manager does not read this
// state and ignore the field: it declines the installation and says which
// manager version wrote it. That is the intended outcome and it is worth being
// exact about, because "an older binary just ignores the new field" is what the
// paragraphs above describe happening *without* a bump, and it is the reading
// somebody troubleshooting a rollback will otherwise take from them.
// Bumped to 9 for `runtime` (RFC 0023 P2), and this one is the read direction
// rather than the write — the first of the bumps that is. An older manager
// reading a state file that names a runtime it has never heard of does not
// merely lose a field: it has exactly one adapter, so it would take an
// installation created against Quadlet and drive it with Compose. That is not
// a wrong default or a dropped setting, it is the manager operating the wrong
// substrate while reporting success, which is the failure decision 5 exists to
// refuse. Refusing the state file is the only answer that holds, and the
// refusal is what the bump buys.
// Bumped to 10 for `runtime_options`, and the read direction again -- the
// second bump that is, for a sharper version of schema 9's reason. The options
// decide what a runtime names durable things: under Compose the project is the
// prefix on every volume, network and container. An older manager reading a
// state file that records them does not merely lose a field, it operates the
// deployment under whatever the release currently says -- and if that differs,
// Compose creates a fresh, empty set of volumes beside the real ones and brings
// the product up against them, reporting success. Measured:
// `--project-name alpha` resolves a volume named `alpha_data` and `beta`
// resolves `beta_data`. Refusing the file is the only answer that keeps the
// data attached to the deployment.
const InstallationSchemaVersion = 10

// Signing is this installation's signing identity: the public half of the key
// the machine signs its own statements with, and the keys it used to.
//
// State rather than only a file on disk, because it has to reach `status
// --json`, the export and an attestation without any of them opening a key
// file (RFC 0028 decision 6).
//
// **Every field here may legitimately be empty.** An installation that has
// never signed anything has no key, and RFC 0028 decision 9 makes that the
// normal state of every machine that existed before schema 6: the migration
// bumps the number and mints nothing. A consumer that treats PublicKey as
// always populated produces an artifact claiming a signer that is the empty
// string.
type Signing struct {
	// PublicKey is the minisign public key line for the current key, as
	// `minisign -P` accepts it. Empty means this machine has never minted
	// one, which is not an error.
	PublicKey string `yaml:"public_key" json:"public_key,omitempty"`

	// PreviousKeys are the keys this installation signed with before,
	// newest first.
	PreviousKeys []RetiredKey `yaml:"previous_keys" json:"previous_keys,omitempty"`
}

// RetiredKey is a public key this installation used to sign with.
type RetiredKey struct {
	// Key is the retired public key, in the same form as
	// Signing.PublicKey.
	Key string `yaml:"key" json:"key"`

	// RetiredAt is when this machine stopped signing with the key.
	//
	// **History for an operator reading their own timeline, and
	// deliberately not a check.** A verifier must not reject a signature
	// for being dated after this: the date would come from the artifact,
	// and the artifact is what a forger writes. Enforcing it would stop
	// only an attacker who neglected to set a timestamp -- a defence in
	// appearance and not in fact. RFC 0028 §5.3 and decision 11.
	RetiredAt Time `yaml:"retired_at" json:"retired_at,omitempty"`

	// Reason is why the key was retired: RetiredByRebuild or
	// RetiredByRotation.
	Reason RetirementReason `yaml:"reason" json:"reason,omitempty"`
}

// RetirementReason says why a key stopped being this machine's.
type RetirementReason string

const (
	// RetiredByRebuild is a key inherited from the installation an export
	// came from. The predecessor is a *different machine* that held the
	// same installation identity; its private key did not travel.
	RetiredByRebuild RetirementReason = "rebuild"

	// RetiredByRotation is a key this machine replaced deliberately. P2 of
	// RFC 0028 -- named here so the field has both its values from the
	// start and a reader is not left wondering what else can appear.
	RetiredByRotation RetirementReason = "rotation"
)

// Valid reports whether a retirement reason is one this manager writes.
func (r RetirementReason) Valid() bool {
	return r == RetiredByRebuild || r == RetiredByRotation
}

// HasKey reports whether this installation has a current signing key.
//
// The predicate exists so consumers ask the question in one place rather than
// each comparing against "" and each deciding what an empty string means.
func (s Signing) HasKey() bool { return strings.TrimSpace(s.PublicKey) != "" }

// Mode declares what a machine is for.
//
// Absent means production. That is the rule SkipBackupBeforeUpdate learned the
// hard way, quoted in Policy below: the one place a missing value decides
// something is the one place it must not decide the dangerous thing.
type Mode string

// ModeDev marks a sandbox: a machine whose data is disposable and whose whole
// purpose is rehearsing what will happen to a real one.
const ModeDev Mode = "dev"

// Modes is every value `mode` may take, for validation and for error messages.
//
// Production is spelled by absence rather than by a value, so it is not in this
// list -- a machine that says nothing about what it is gets the strict
// treatment.
var Modes = []Mode{ModeDev}

// ParseMode reads a mode an operator typed.
//
// "production" is accepted and means the absent value. Absence is how production
// is *stored* -- that is what makes a hand-edited file default to the strict
// reading -- but refusing the word at a flag would be a manager insisting an
// operator spell the safe choice by saying nothing, and `import --mode
// production` from a dev export is a request that has to be understood before it
// can be refused for the right reason.
func ParseMode(s string) (Mode, error) {
	switch trimmed := Mode(strings.TrimSpace(s)); trimmed {
	case "", "production":
		return "", nil
	default:
		if trimmed.Valid() {
			return trimmed, nil
		}
		return "", Usage("unknown mode %q", s).
			WithHint("modes: %s, or production (the default)", joinModes())
	}
}

// Valid reports whether a non-empty mode is one this manager knows.
func (m Mode) Valid() bool { return slices.Contains(Modes, m) }

func joinModes() string {
	out := make([]string, len(Modes))
	for i, m := range Modes {
		out[i] = string(m)
	}
	return strings.Join(out, ", ")
}

// Describe renders a mode for an operator, including the one spelled by
// absence.
//
// One implementation, because two of them is how a refusal comes to say
// "mode is fixed: this one is " with nothing after the colon.
func (m Mode) Describe() string {
	if m == "" {
		return "production"
	}
	return string(m)
}

// IsDev reports whether this installation is a sandbox.
//
// Asked as a question about the installation rather than by comparing the field
// at each call site, so "absent means production" is stated once. A call site
// spelling it `inst.Mode != "production"` would read as correct and invert the
// default for every value nobody thought of.
func (i Installation) IsDev() bool { return i.Mode == ModeDev }

// RuntimeName is the runtime this installation runs on.
//
// Empty means an installation created before the field existed, and those
// machines ran the legacy runtime because it was the only one. Read through
// this rather than off the field, so the pre-schema-9 case is answered in one
// place instead of at every call site — the shape where one caller forgets and
// resolves a runtime named "" against a release that declares two.
func (i Installation) RuntimeName() string {
	if i.Runtime == "" {
		return LegacyRuntimeName
	}
	return i.Runtime
}

// Installation is the machine-specific state of one deployment. It is the
// only place operator intent is recorded; everything else is derived from it
// plus the release.
type Installation struct {
	SchemaVersion int `yaml:"schema_version" json:"schema_version"`

	// ID identifies this deployment across backups and restores. It is
	// generated once at init and never changes -- restore confirms against
	// it, so a typo here would defeat that check.
	ID string `yaml:"id" json:"id"`

	Product   string `yaml:"product" json:"product"`
	CreatedAt Time   `yaml:"created_at" json:"created_at"`

	// Mode declares this machine a sandbox. Absent means production.
	//
	// On the installation rather than in Policy, and deliberately: Policy is
	// what `morzer config` may change, and this may not be. It is fixed when
	// the installation is created and never transitions -- not one-way, *no*
	// way. Both directions are dangerous in different shapes: production →
	// dev puts real data under relaxed rules immediately, and dev →
	// production presents untrusted history as trustworthy and surfaces
	// during an incident, when someone discovers that `previous` was pruned
	// and no pre-update backup was ever taken.
	//
	// The claim is about the manager's own surfaces. `mode` is a field in a
	// JSON file and root can edit it; defending one boolean against an
	// operator who can equally edit the recipient list, the backup targets
	// or the installation id would be defending the wrong thing.
	Mode Mode `yaml:"mode" json:"mode,omitempty"`

	// Profile selects the deployment topology from runtime.profiles.
	Profile string `yaml:"profile" json:"profile,omitempty"`

	// Runtime is which runtime this installation was created against, and
	// it never transitions (RFC 0023 decision 3).
	//
	// Not a setting. The state directory records volume names, image
	// references and -- once a second adapter exists -- unit names, all of
	// which are runtime-specific, so changing this is a migration of
	// everything the manager knows rather than an edit. `config` may not
	// touch it, for the same reason and by the same mechanism as Mode.
	//
	// Empty on every installation created before schema 9, which is read as
	// the legacy runtime: those machines were created when there was one,
	// and it was that one. Deliberately not defaulted on write -- an empty
	// value means "predates the field", and filling it in would erase the
	// distinction on the first `config set`.
	//
	// A field of its own rather than `Providers.Runtime`, which was the
	// obvious home and is why that field is now gone. It was declared,
	// serialised, and never written or read by anything, and it was
	// documented four incompatible ways -- "declared by the release
	// manifest", "from the flags", "which adapters to use", and excluded
	// from the describe document as the release's to declare. Recording the
	// runtime there would have given it a fifth meaning that was also the
	// only real one, and an older manager reading it would have found a
	// name it understood and no reason to stop. RFC 0023 decision 11
	// settled that; wave 36 removed the field the decision was avoiding.
	Runtime string `yaml:"runtime" json:"runtime,omitempty"`

	// RuntimeOptions is what the runtime was told when this installation was
	// created -- the manifest's per-runtime options, verbatim and
	// uninterpreted.
	//
	// Recorded because these name things that outlive an operation. The
	// compose runtime's `project` is the prefix on every volume, network and
	// container it creates, so a release that changes it points the
	// deployment at storage that does not exist yet, and the old data stays
	// on the disk with nothing referring to it. The manager cannot tell
	// which options are like that -- only the adapter knows what any of them
	// mean -- so it treats all of them as durable and refuses a change,
	// which fails safe in the direction that keeps data attached.
	//
	// **No `omitempty`, deliberately, and it is load-bearing.** An empty map
	// means "this release declared no options", and nil means "created
	// before the field existed". Those lead to different answers: a release
	// that now sets `project` is a change from the first and an unknown
	// baseline from the second. omitempty would serialise both as absent and
	// erase the distinction on the first write.
	RuntimeOptions map[string]string `yaml:"runtime_options" json:"runtime_options"`

	// Domains are the public names the product is served under. The first
	// is canonical and becomes the public URL in `status`.
	Domains []string `yaml:"domains" json:"domains,omitempty"`

	Policy Policy `yaml:"policy" json:"policy"`

	// Parameters are the operator's choices among what the release
	// declares, stored as written and validated against the declaration on
	// load. Storing the raw text means a release that changes a
	// parameter's type surfaces a validation error rather than silently
	// reinterpreting the value.
	//
	// This replaces the `settings` free-form map, which reached the
	// template context but had no writer, no schema, no documentation and
	// no test -- it was never settable, so nothing can depend on it.
	Parameters map[string]string `yaml:"parameters" json:"parameters,omitempty"`

	// Update is how this deployment learns that a release exists.
	Update UpdateConfig `yaml:"update" json:"update,omitempty"`

	// Notify is where this deployment reports outcomes.
	//
	// In the installation for the same reason Backup is: a vendor cannot
	// know whether their customer runs Slack, a webhook receiver or
	// nothing, and a manifest that named one would be a vendor deciding
	// where an operator's alerts go.
	Notify NotifyConfig `yaml:"notify" json:"notify,omitempty"`

	// Backup is where this deployment's backups go besides this disk.
	//
	// In the installation rather than the release manifest, and deliberately:
	// a vendor cannot know whether their customer has a second VM, a NAS or a
	// bucket, and a manifest that named one would be a vendor deciding where
	// an operator's data is kept.
	Backup BackupConfig `yaml:"backup" json:"backup,omitempty"`

	// Signing is this machine's signing identity. Every field in it may be
	// empty on a real installation -- see Signing.
	Signing Signing `yaml:"signing" json:"signing,omitempty"`

	// AttestationSalt makes the attestation's rendered-configuration digest
	// resistant to being brute-forced back to its inputs.
	//
	// Minted at `init` and preserved across a rebuild by `installation
	// import`, because a re-minted salt breaks the chain continuity on
	// exactly the machine that most needs it -- the same failure RFC 0017
	// found for the installation id (RFC 0025 decision 10).
	//
	// The consequence is deliberate and worth stating where the field is:
	// the digest detects drift on **one machine over time**, which is the
	// audit question, and is not comparable across machines. An unsalted
	// digest would be comparable and would also be brute-forceable, since
	// the input is a handful of ports and booleans.
	AttestationSalt string `yaml:"attestation_salt" json:"attestation_salt,omitempty"`
}

// UpdateConfig is the operator's arrangement for learning about releases.
type UpdateConfig struct {
	// Check enables *unprompted* update checking -- the `doctor` check and
	// the `status` line. Default false, and absent means false.
	//
	// Off by default because a check contacts the vendor's registry, which
	// reveals an IP, a timestamp and by inference an installed version. For
	// a product whose customers chose self-hosting, turning that on for
	// them would be a phone-home nobody agreed to.
	//
	// It does *not* gate `morzer update --check`. An operator typing that
	// command is the consent, and refusing a direct instruction because a
	// persisted flag is false would be the manager arguing with the person
	// running it. See CheckAllowed.
	Check bool `yaml:"check" json:"check,omitempty"`

	// Channel is a mutable reference this deployment follows.
	//
	// A different operation from checking, not a spelling of it: a check
	// enumerates version tags and picks the highest admissible one, which is
	// what an operator wants from a stable repository. A channel is one
	// reference that moves -- `oci://registry.example/demo/bundle:dev` --
	// and enumeration structurally cannot follow one, because the tag is not
	// a version and the versions behind it are not tags anybody lists.
	//
	// Empty means no channel, which is the default and the only state a
	// machine reaches without someone saying otherwise.
	Channel string `yaml:"channel" json:"channel,omitempty"`

	// AutoApply installs what the channel offers, without a human.
	//
	// Only what passes the gate: the release must declare that a failure
	// cannot end needing a database restore, and the installation must
	// require signatures. Everything else is fetched, staged and notified
	// rather than silently skipped -- see domain.AssessUnattended.
	//
	// Absent means off, and the refusal that enforces the signature
	// requirement lives in Installation.Validate, so it fires wherever the
	// state is written rather than at the tick that would have acted.
	AutoApply bool `yaml:"auto_apply" json:"auto_apply,omitempty"`
}

// FollowsChannel reports whether a channel is configured.
func (u UpdateConfig) FollowsChannel() bool { return strings.TrimSpace(u.Channel) != "" }

// CheckAllowed reports whether an update check may contact the registry.
//
// explicit is true when an operator asked for one by name. The distinction is
// the whole of the phone-home policy: unprompted paths honour the setting,
// and a typed command is its own authorisation.
func (u UpdateConfig) CheckAllowed(explicit bool) bool { return explicit || u.Check }

// NotifyConfig is where operation outcomes are reported off the machine.
type NotifyConfig struct {
	// Targets each receive every forwarded event. A failure to deliver
	// never changes an operation's outcome -- see ports.Notifier.
	Targets []NotifyTargetConfig `yaml:"targets" json:"targets,omitempty"`
}

// HasTargets reports whether anything is configured to be told.
func (n NotifyConfig) HasTargets() bool { return len(n.Targets) > 0 }

// NotifyTargetConfig is one endpoint that receives events.
//
// The shape mirrors BackupTargetConfig, with one addition it needs and backup
// targets do not: a target's endpoint may *be* its credential. A Slack or Teams
// incoming-webhook URL is a bearer token spelled as a path, so putting it in
// URL would leak it into this file, into `doctor` output, and into an
// installation export beside a recovery key.
type NotifyTargetConfig struct {
	// Name identifies the target in diagnostics. Required when URLSecret is
	// used, because there is then nothing else safe to print.
	Name string `yaml:"name,omitempty" json:"name,omitempty"`

	// URL is the endpoint, when the endpoint carries no credential.
	// Exclusive with URLSecret.
	URL string `yaml:"url,omitempty" json:"url,omitempty"`

	// URLSecret names a secret holding the whole endpoint URL, for
	// services that spell the credential as a path. Exclusive with URL.
	URLSecret string `yaml:"url_secret,omitempty" json:"url_secret,omitempty"`

	// Credentials names a secret holding a credential document, whose
	// header value is sent with the request. A name rather than the value,
	// for the reason BackupTargetConfig.Credentials records.
	Credentials string `yaml:"credentials,omitempty" json:"credentials,omitempty"`

	// MinLevel is the lowest check severity this target receives, one of
	// "warn" or "error". Empty means "error".
	//
	// Per target and defaulting to the quiet end because warnings fire on
	// every `doctor` run until they are fixed, and deduplication is
	// deliberately out of scope. An operator who wants rotation reminders
	// asks for them on the target that should carry them.
	MinLevel string `yaml:"min_level,omitempty" json:"min_level,omitempty"`
}

// Endpoint reports how this target's URL is obtained, and validates that
// exactly one of the two forms is used.
func (t NotifyTargetConfig) Endpoint() (secretName string, direct string, err error) {
	hasURL := strings.TrimSpace(t.URL) != ""
	hasSecret := strings.TrimSpace(t.URLSecret) != ""

	switch {
	case hasURL && hasSecret:
		return "", "", ValidationError(nil,
			"a notify target sets both url and url_secret").
			WithHint("use url_secret when the URL itself is a credential, url otherwise")
	case hasSecret:
		// Trimmed, not raw. The presence check above trims, so
		// `url_secret: " chat "` passed validation and was then looked
		// up under a name with spaces in it -- a target silently
		// dropped for a secret that exists.
		return strings.TrimSpace(t.URLSecret), "", nil
	case hasURL:
		return "", strings.TrimSpace(t.URL), nil
	default:
		return "", "", ValidationError(nil,
			"a notify target sets neither url nor url_secret")
	}
}

// Validate reports what is wrong with this target, if anything.
//
// Checked when the installation is loaded or saved rather than only at wiring
// time. A target that fails to build is *dropped* with a log line, so an
// installation carrying a contradictory one would keep reporting itself
// healthy while the notifications an operator arranged silently never arrive.
func (t NotifyTargetConfig) Validate() error {
	if _, _, err := t.Endpoint(); err != nil {
		return err
	}
	if strings.TrimSpace(t.URLSecret) != "" && strings.TrimSpace(t.Name) == "" {
		return ValidationError(nil,
			"a notify target using url_secret must set a name").
			WithHint("diagnostics cannot print the URL in that case, so the " +
				"name is the only thing left to identify it by")
	}
	switch strings.ToLower(strings.TrimSpace(t.MinLevel)) {
	case "", "warn", "warning", "error":
	default:
		return ValidationError(nil,
			"notify target %q has min_level %q", t.Label(), t.MinLevel).
			WithHint("use warn or error; empty means error")
	}
	return nil
}

// Label is what diagnostics may print about this target.
//
// Never the URL when it came from a secret: the whole reason that form exists
// is that the URL is the credential.
func (t NotifyTargetConfig) Label() string {
	if t.Name != "" {
		return t.Name
	}
	if strings.TrimSpace(t.URLSecret) != "" {
		return "(url from secret " + t.URLSecret + ")"
	}
	return t.URL
}

// BackupConfig is the operator's backup arrangement beyond the local disk.
type BackupConfig struct {
	// Targets are pushed to, in order, after every backup is verified.
	//
	// Several are allowed and each is pushed to. An operator who configured
	// two targets wants two copies, so one of them failing fails the backup
	// -- see the push step, which is where that is enforced.
	Targets []BackupTargetConfig `yaml:"targets" json:"targets,omitempty"`
}

// BackupTargetConfig is one place backups are kept.
type BackupTargetConfig struct {
	// URL is where, as file://, ssh:// or s3://.
	URL string `yaml:"url" json:"url"`

	// Credentials names a secret holding the credential document for this
	// target. Empty for file://, which needs none.
	//
	// A name rather than the values: this file is a report an operator reads
	// and `morzer doctor` prints, and a bucket key in it would be a bucket
	// key in every support ticket. The values live in the encrypted secret
	// state, which is also what carries them into an export -- and therefore
	// to the rebuilt machine that has to reach the target to recover.
	Credentials string `yaml:"credentials,omitempty" json:"credentials,omitempty"`
}

// HasTargets reports whether any off-machine copy is configured.
func (b BackupConfig) HasTargets() bool { return len(b.Targets) > 0 }

// Policy is operator-set behaviour that overrides release defaults. These are
// decisions about *this machine*, so they live with the installation rather
// than the release.
type Policy struct {
	// RequireSignature refuses any bundle that is not signed. Checksum
	// verification always happens; this raises the bar.
	RequireSignature bool `yaml:"require_signature" json:"require_signature"`

	// SigningKeys are the minisign public keys a bundle's signature must
	// verify against.
	//
	// They live with the installation rather than with the release for the
	// obvious reason: a bundle naming the key that may sign it would be a
	// bundle authorising itself. This is the operator saying whose releases
	// this machine will install.
	SigningKeys []string `yaml:"signing_keys" json:"signing_keys,omitempty"`

	// RetainReleases and RetainBackups override manifest retention when
	// non-zero. An operator with a small disk should not have to edit a
	// vendor's manifest.
	RetainReleases int `yaml:"retain_releases" json:"retain_releases,omitempty"`
	RetainBackups  int `yaml:"retain_backups" json:"retain_backups,omitempty"`

	// SkipBackupBeforeUpdate turns off the pre-update backup for every
	// update on this installation.
	//
	// Named for the unsafe direction on purpose. It was BackupBeforeUpdate,
	// where the zero value -- a field absent from a hand-edited file, a
	// record written before the field existed -- meant "do not back up",
	// and the one place a missing bool decides something is the one place
	// it must not decide that. Now absence means the backup is taken.
	//
	// Recorded in the journal when it applies, so an incident review can
	// see the choice was made rather than defaulted into.
	SkipBackupBeforeUpdate bool `yaml:"skip_backup_before_update" json:"skip_backup_before_update,omitempty"`

	// StaleBackupAfter is when `doctor` starts warning that the last
	// backup is too old.
	//
	// Zero means unset, not "never warn", and every reader resolves it
	// through DefaultStaleBackupAfter. `status` used to read it directly
	// and skip the problem when it was zero, so the two commands gave
	// different answers about the same backup on any installation that
	// predates the field -- migrations do not fill defaults in.
	StaleBackupAfter Duration `yaml:"stale_backup_after" json:"stale_backup_after,omitempty"`

	// BackupSchedule is when scheduled backups run, as the supervisor's own
	// expression -- a systemd `OnCalendar` line. Empty takes the
	// supervisor's default.
	//
	// **Here rather than nowhere, which is where it used to live.** It
	// arrived as an `init` flag and was written straight into a unit file,
	// so nothing on the machine remembered it: every later reconciliation
	// rendered the default, and an unrelated `config set` silently moved an
	// operator's maintenance window. A value the manager re-renders has to
	// be a value the manager stores.
	//
	// In Policy because that is what Policy is -- the operator's
	// arrangement with the manager, as opposed to what the vendor
	// declared -- and it puts the schedule beside StaleBackupAfter, which
	// is the same kind of decision about the same subject. It also makes it
	// settable, which it never was.
	BackupSchedule string `yaml:"backup_schedule" json:"backup_schedule,omitempty"`

	// SkipScheduledBackups says this machine's backups are not this
	// manager's job (RFC 0030 row 4). The backup service and timer are then
	// not generated at all, and a reconciliation removes them.
	//
	// Named for the unsafe direction, as SkipBackupBeforeUpdate is, and for
	// the same reason: absence must mean the backups happen. A field missing
	// from a hand-edited file, or from a record written before it existed,
	// is a machine that gets a backup timer.
	//
	// **Not a second spelling of `systemctl disable`, which RFC 0030 row 1
	// made durable in the same change.** This decides whether the unit
	// exists; disabling decides whether an existing unit runs. An
	// installation that declares this is reproduced by `init --repair` and
	// travels in `installation describe`, which is what the host's own
	// tools cannot do -- and it is why answering row 1 first mattered:
	// while `disable` was being silently undone, this would have been a
	// second switch with different behaviour and no way to tell which was
	// in force.
	//
	// It does not mean "take no backups". `morzer backup` still works, and
	// a backup that exists is still expected to reach a target.
	SkipScheduledBackups bool `yaml:"skip_scheduled_backups" json:"skip_scheduled_backups,omitempty"`
}

// ValidateBackupSchedule refuses a schedule that could not safely be rendered.
//
// **Not a calendar parser.** Whether `Mon *-*-* 04:00:00` is a valid expression
// is the supervisor's question, and answering it here would mean this package
// carrying systemd's grammar and disagreeing with it at the first version that
// extends it. A wrong-but-well-formed expression is a unit systemd refuses to
// load, which is visible and local.
//
// What this refuses is the shape that is not a schedule at all. The value is
// rendered into `OnCalendar=` in a root-owned unit file, and it used to arrive
// only from argv at `init`. Persisting it means every later reconciliation
// reads it back out of the manager's state file instead -- so it is rendered
// again and again by a path that never revisits the door it came in through,
// and a value that got into that file by any means at all (an older binary, a
// restored or migrated state, an edit of the file itself) is rendered as it
// stands. A newline in it is a second directive in that unit, and `Unit=` in a
// [Timer] section names what the timer starts: a way to schedule something as
// root from a configuration field. Bounding it at the boundary is the same rule
// every other value that leaves this manager follows.
func ValidateBackupSchedule(raw string) error {
	// The raw value, before any trim. This guard runs on load and on save,
	// where `raw` *is* what gets rendered -- so trimming first and scanning
	// the copy asked the question about a string nobody stores. A leading
	// newline vanished into the trim, passed, and arrived at `OnCalendar=`
	// intact, where it opens a second directive: `Unit=` in a [Timer]
	// section names what the timer starts, so that is a way to schedule
	// something else as root from a hand-edited configuration field.
	//
	// The input doors trim before they call this, so an operator who pastes
	// a value with a stray newline at the end still gets a schedule rather
	// than a refusal. What arrives here untrimmed did not come through them.
	for _, r := range raw {
		if r == '\n' || r == '\r' || unicode.IsControl(r) {
			return ValidationError(nil, "a backup schedule is one line").
				WithHint("it is rendered into a systemd unit, where a second " +
					"line is a second directive; use an OnCalendar " +
					"expression such as `Mon *-*-* 04:00:00`")
		}
	}

	schedule := strings.TrimSpace(raw)
	if schedule == "" {
		return nil
	}
	if len(schedule) > maxBackupScheduleLen {
		return ValidationError(nil, "a backup schedule is at most %d characters",
			maxBackupScheduleLen).
			WithHint("OnCalendar expressions are short; " +
				"`morzer doctor` reports a unit systemd will not load")
	}
	return nil
}

// maxBackupScheduleLen bounds what reaches the unit file. Generous against any
// real expression and far short of a payload.
const maxBackupScheduleLen = 200

// DefaultStaleBackupAfter is how old the newest backup may be before it is
// worth telling somebody.
//
// Named rather than repeated, because it was repeated: `DefaultPolicy` wrote it
// and `doctor` fell back to it, and `status` had a third opinion -- no warning
// at all -- for any installation whose value was zero. One constant is what
// makes "the most recent backup is too old" one fact instead of three.
const DefaultStaleBackupAfter = 48 * time.Hour

// DefaultPolicy is what `init` writes. Safe defaults, explicitly opted out of
// rather than silently absent.
func DefaultPolicy() Policy {
	return Policy{
		RequireSignature: false,
		StaleBackupAfter: Duration(DefaultStaleBackupAfter),
	}
}

// PublicURL is the canonical address of the product, or empty when no domain
// was configured.
func (i Installation) PublicURL() string {
	if len(i.Domains) == 0 {
		return ""
	}
	d := i.Domains[0]
	if strings.Contains(d, "://") {
		return d
	}
	return "https://" + d
}

// RetentionReleases resolves the effective release retention: installation
// policy wins over the manifest, per the precedence rules.
func (i Installation) RetentionReleases(m Manifest) int {
	if i.Policy.RetainReleases > 0 {
		return i.Policy.RetainReleases
	}
	if m.Retention.Releases > 0 {
		return m.Retention.Releases
	}
	return DefaultRetentionReleases
}

// RetentionBackups resolves the effective backup retention.
func (i Installation) RetentionBackups(m Manifest) int {
	if i.Policy.RetainBackups > 0 {
		return i.Policy.RetainBackups
	}
	if m.Retention.Backups > 0 {
		return m.Retention.Backups
	}
	return DefaultRetentionBackups
}

// Validate checks the installation is internally consistent. Called after
// loading, so a hand-edited installation.yaml fails loudly at the start of an
// operation rather than halfway through it.
func (i Installation) Validate() error {
	var v validationErrors

	if i.SchemaVersion == 0 {
		v.add("schema_version", "is required")
	} else if i.SchemaVersion > InstallationSchemaVersion {
		return IncompatibleError(nil,
			"installation was written by a newer manager (schema %d, this manager reads %d)",
			i.SchemaVersion, InstallationSchemaVersion).
			WithHint("upgrade the manager; state migrations only run forward")
	}
	// Empty is valid and means "created before schema 9"; anything else has
	// to be a well-formed name. This is a grammar check, not an existence
	// check -- the domain cannot know which runtimes exist, and the one that
	// is well-formed and wrong is refused by the adapter before any operation
	// runs.
	//
	// It earns its place on a different argument than tidiness: this value is
	// read out of a file an operator may have hand-edited, and it is printed
	// back in error messages. A name carrying control characters is a terminal
	// escape in a diagnostic, which is the same shape as the bounds on fleet
	// rows and attested text.
	if i.Runtime != "" && !ValidRuntimeName(i.Runtime) {
		v.add("runtime", "is not a usable runtime name: lower-case letters, digits "+
			"and hyphens, starting with a letter, at most 32 characters")
	}
	// The same bounds the manifest puts on them, checked again here for the
	// same reason the backup schedule is: this file is hand-editable, and
	// the values reach an adapter's argv. A grammar, never a meaning -- what
	// the keys are for is the adapter's answer.
	for _, key := range sortedStringKeys(i.RuntimeOptions) {
		if !optionName.MatchString(key) {
			v.add("runtime_options",
				"%q is not a usable option name: lower-case letters, digits "+
					"and underscores, starting with a letter, at most 32 characters", key)
			continue
		}
		if err := ValidateSingleLine(i.RuntimeOptions[key], maxRuntimeOptionLen); err != nil {
			v.add("runtime_options."+key, "%s", AsError(err).Message)
		}
	}
	if i.ID == "" {
		v.add("id", "is required")
	}
	if i.Product == "" {
		v.add("product", "is required")
	} else if err := ValidateProductName(i.Product); err != nil {
		v.add("product", "%s", AsError(err).Message)
	}
	for idx, target := range i.Notify.Targets {
		if err := target.Validate(); err != nil {
			v.add(fmt.Sprintf("notify.targets[%d]", idx), "%s", AsError(err).Message)
		}
	}
	// Checked here as well as at `config set`, and this is the check that
	// matters. The setting path is one way in; the other is somebody
	// editing installation.yaml, which is the path the value's whole reason
	// for being bounded describes -- it is rendered into `OnCalendar=` in a
	// root-owned unit file, where a second line is a second directive. A
	// guard that only covered the command would have been a guard over the
	// door somebody was not coming through.
	if err := ValidateBackupSchedule(i.Policy.BackupSchedule); err != nil {
		v.add("policy.backup_schedule", "%s", AsError(err).Message)
	}

	// An unknown mode is refused rather than read as production. A typo --
	// `mode: development` -- would otherwise produce a machine that looks
	// like a sandbox in its own state file and behaves like a production
	// host, which is the one misreading with no visible symptom.
	if i.Mode != "" && !i.Mode.Valid() {
		v.add("mode", "unknown mode %q (%s, or absent for production)",
			i.Mode, joinModes())
	}

	// Unattended apply hands the vendor unattended root: hooks run as root,
	// and an update runs the target bundle's migrate hook. Refused where it
	// is written, in the same shape as require_signature above -- a machine
	// that accepts the setting and then refuses to act on every tick is
	// worse than one that refuses the setting.
	if i.Update.AutoApply && !i.Policy.RequireSignature {
		v.add("update.auto_apply",
			"needs policy.require_signature, because it hands the vendor "+
				"unattended root on this machine")
	}

	// The signing block is validated for shape and never for presence. An
	// installation with no key at all is every machine that predates schema
	// 6, and refusing those would refuse to load the state it is meant to
	// migrate (RFC 0028 §5.6). What is checked is that a *recorded*
	// predecessor is usable: an entry with no key names a signer nobody can
	// check against, and a verifier walking the list would report "unknown
	// signer" for a machine whose history is right there.
	for idx, prev := range i.Signing.PreviousKeys {
		field := fmt.Sprintf("signing.previous_keys[%d]", idx)
		if strings.TrimSpace(prev.Key) == "" {
			v.add(field+".key", "is required")
		}
		if prev.Reason != "" && !prev.Reason.Valid() {
			v.add(field+".reason", "unknown reason %q (%s or %s)",
				prev.Reason, RetiredByRebuild, RetiredByRotation)
		}
	}

	if i.Policy.RetainReleases < 0 {
		v.add("policy.retain_releases", "must not be negative")
	}
	if i.Policy.RetainBackups < 0 {
		v.add("policy.retain_backups", "must not be negative")
	}

	// An unsatisfiable policy is refused where it is written rather than
	// where it would be enforced. Requiring a signature with no key to check
	// it against would fail every operation with a message about bundles,
	// when the problem is one line of installation.yaml.
	if i.Policy.RequireSignature && len(i.Policy.SigningKeys) == 0 {
		v.add("policy.require_signature",
			"is set but policy.signing_keys is empty, so no bundle could ever satisfy it")
	}
	for n, key := range i.Policy.SigningKeys {
		if strings.TrimSpace(key) == "" {
			v.add(fmt.Sprintf("policy.signing_keys[%d]", n), "is empty")
		}
	}

	// A target is checked where it is written, not where it is used. The
	// alternative is a typo that surfaces during the nightly backup weeks
	// later, in the one operation whose whole purpose is to still work.
	seen := make(map[string]bool, len(i.Backup.Targets))
	for n, t := range i.Backup.Targets {
		field := fmt.Sprintf("backup.targets[%d]", n)

		parsed, err := ParseBackupTarget(t.URL)
		if err != nil {
			v.add(field+".url", "%s", AsError(err).Message)
			continue
		}
		if seen[parsed.Canonical()] {
			// Two identical targets would be pushed to twice and
			// pruned twice, and the second pass would report
			// removing what the first already removed.
			v.add(field+".url", "is listed twice")
		}
		seen[parsed.Canonical()] = true
	}

	return v.err()
}

// Time wraps time.Time to guarantee RFC3339 UTC in every serialised form.
// Mixed timezones in a journal make incident timelines needlessly hard to
// read, and the journal is the artifact an operator reaches for first.
type Time struct {
	time.Time
}

func NewTime(t time.Time) Time { return Time{t.UTC().Truncate(time.Second)} }

func (t Time) MarshalText() ([]byte, error) {
	if t.IsZero() {
		return []byte(""), nil
	}
	return []byte(t.UTC().Format(time.RFC3339)), nil
}

func (t *Time) UnmarshalText(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" {
		*t = Time{}
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return ValidationError(err, "invalid timestamp %q", s).
			WithHint("timestamps are RFC3339, e.g. 2026-08-03T00:00:00Z")
	}
	*t = Time{parsed.UTC()}
	return nil
}
