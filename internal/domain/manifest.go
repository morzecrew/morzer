package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// APIVersion identifies the manifest schema. It is the first of the three
// independently versioned contracts in the spec: what the manager can read.
type APIVersion string

const (
	APIVersionV1Alpha1 APIVersion = "selfhost/v1alpha1"
)

// SupportedAPIVersions is embedded in `morzer version --json` so a bundle
// author can check compatibility without trial and error. Ordered oldest
// first.
var SupportedAPIVersions = []APIVersion{APIVersionV1Alpha1}

// DeprecatedAPIVersions maps a still-readable but stale api_version to the
// warning shown when it is encountered. The spec requires reading every
// previously published version until explicit deprecation; this is where that
// promise is kept.
var DeprecatedAPIVersions = map[APIVersion]string{}

// DeprecationWarning reports whether this manifest's api_version is
// deprecated, and the message to show the operator when it is.
//
// Computed on demand rather than at parse time so every consumer that meets a
// manifest -- update resolving a bundle, `release verify` in a vendor's CI --
// asks the same question of the same map, and none of them can forget to look
// at a field the loader forgot to fill.
func (m Manifest) DeprecationWarning() (string, bool) {
	warning, deprecated := DeprecatedAPIVersions[m.APIVersion]
	return warning, deprecated
}

const KindApplicationRelease = "application-release"

// Manifest is the typed release contract. Field order follows the spec's
// document order so the struct reads like the YAML it decodes.
type Manifest struct {
	APIVersion APIVersion `yaml:"api_version" json:"api_version"`
	Kind       string     `yaml:"kind" json:"kind"`

	Metadata  Metadata  `yaml:"metadata" json:"metadata"`
	Providers Providers `yaml:"providers" json:"providers"`

	// Runtime is the Compose-shaped block, and it is deprecated.
	//
	// Kept readable rather than replaced. RFC 0023 §4.1 specified a
	// replacement and argued it on the strength of landing "before the
	// first tag"; 0.1.0 and 0.2.0 are cut, and under strict decoding a
	// replacement makes `runtime:` an unknown field -- so every bundle
	// already built would stop parsing to buy a tidier surface.
	// DeclaredRuntimes folds it into Runtimes[LegacyRuntimeName] on read.
	//
	// Carries no `omitempty`, which would be the obvious way to stop a
	// `runtimes:`-only release announcing an empty `"runtime": {}`: it does
	// nothing on a struct field. Backup and Bundle below reach for
	// `omitzero` for exactly that reason, and this one does not follow them
	// because a deprecated block that disappears from `release show --json`
	// is harder to notice than one that shows up empty.
	Runtime RuntimeSpec `yaml:"runtime" json:"runtime"`

	// Runtimes is the runtime dimension: which runtimes this release
	// supports, and what each is declared with.
	//
	// A release declaring one installs only where that runtime is present,
	// and a release declaring two carries both sets with the vendor owning
	// their equivalence -- the manager asserts nothing about it and must
	// not pretend to (RFC 0023 decision 4).
	Runtimes      Runtimes                 `yaml:"runtimes" json:"runtimes,omitempty"`
	Requirements  Requirements             `yaml:"requirements" json:"requirements"`
	Parameters    map[string]ParameterSpec `yaml:"parameters" json:"parameters,omitempty"`
	Images        map[string]ImageSpec     `yaml:"images" json:"images"`
	Configuration []ConfigurationFile      `yaml:"configuration" json:"configuration"`
	Secrets       SecretsSpec              `yaml:"secrets" json:"secrets"`
	Operations    map[string]OperationSpec `yaml:"operations" json:"operations"`

	// omitzero rather than omitempty: omitempty does not omit an empty
	// struct, so every release -- including the ones written before this
	// section existed -- grew a `"backup": {}` in `release show --json`,
	// announcing a section the manifest never declared to whatever parses
	// that output. omitzero keeps the struct (a pointer would make
	// `m.Backup.Volumes` a nil dereference for every caller) and omits it
	// when the vendor declared nothing.
	Backup BackupSpec `yaml:"backup" json:"backup,omitzero"`

	// Bundle describes the *artefact*, not the host it installs on.
	//
	// A separate block from `requirements` deliberately: everything in that
	// one -- architectures, OS, tools, memory, disk -- says what the machine
	// must provide. This says what the bundle itself is, and a reader
	// looking for "how big is this thing" should not have to find it among
	// the host's obligations.
	//
	// omitzero for the same reason Backup carries it: an empty struct is not
	// omitted by omitempty, so every release without one would announce a
	// `"bundle": {}` in `release show --json`.
	Bundle BundleSpec `yaml:"bundle" json:"bundle,omitzero"`

	Health        HealthSpec                `yaml:"health" json:"health"`
	Compatibility Compatibility             `yaml:"compatibility" json:"compatibility"`
	Retention     Retention                 `yaml:"retention" json:"retention"`
	Extensions    map[string]map[string]any `yaml:"extensions" json:"extensions,omitempty"`
}

// DeclaredRuntimes returns the runtimes this release supports, and whether
// they came from the deprecated `runtime:` block.
//
// Derived on every call rather than normalised once into a field, and that is
// the whole point. The first version of this stored the synthesis in
// ApplyDefaults, which made it a snapshot: anything that touched `runtime:`
// afterwards was silently ignored, and `Validate` called without
// ApplyDefaults checked an empty map -- so a `runtime.files` entry of
// `/etc/passwd` passed validation. A path-escape check that holds only when
// another method ran first is not a check.
func (m Manifest) DeclaredRuntimes() (Runtimes, bool) {
	if len(m.Runtimes) > 0 {
		return m.Runtimes, false
	}
	if m.Runtime.isZero() {
		return nil, false
	}
	return Runtimes{LegacyRuntimeName: RuntimeDecl{
		Files:    m.Runtime.Files,
		Profiles: m.Runtime.Profiles,
	}}, true
}

type Metadata struct {
	Name        string  `yaml:"name" json:"name"`
	Version     Version `yaml:"version" json:"version"`
	Description string  `yaml:"description" json:"description,omitempty"`
	Vendor      string  `yaml:"vendor" json:"vendor,omitempty"`

	// ReleaseNotes is a bundle-relative path to what changed in this
	// release.
	//
	// Declared rather than found by convention, because every other path a
	// bundle ships is declared and existence-checked -- a declared-but-
	// missing file is a validation error. There is deliberately no fallback
	// to looking for RELEASE.md: a convention layered under a declaration
	// reintroduces exactly the ambiguity the declaration removes.
	ReleaseNotes string `yaml:"release_notes" json:"release_notes,omitempty"`

	// SupportURL is where an operator goes when something is wrong.
	//
	// The operator of a self-hosted product is not its vendor, and "where
	// do I get help" otherwise has no home. Surfaced by `status` and by a
	// failing `doctor` check -- deliberately not appended to every error
	// hint, which would put a vendor URL in every log line.
	SupportURL string `yaml:"support_url" json:"support_url,omitempty"`
}

// Provider selects a port implementation by name. New capabilities arrive as
// new provider names, never as changes to the core.
type Provider struct {
	Name    string     `yaml:"name" json:"name"`
	Version Constraint `yaml:"version" json:"version,omitempty"`
}

type Providers struct {
	Runtime Provider `yaml:"runtime" json:"runtime"`
	Secrets Provider `yaml:"secrets" json:"secrets"`
	Backup  Provider `yaml:"backup" json:"backup"`
	Health  Provider `yaml:"health" json:"health"`
}

type RuntimeSpec struct {
	Project  string              `yaml:"project" json:"project"`
	Files    []string            `yaml:"files" json:"files"`
	Profiles map[string][]string `yaml:"profiles" json:"profiles,omitempty"`
}

// ComposeFiles returns the file list for a deployment profile: the base files
// plus the profile's additions. An unknown profile is an error rather than a
// silent fallback to base -- deploying the wrong topology quietly is worse
// than refusing.
func (r RuntimeSpec) ComposeFiles(profile string) ([]string, error) {
	files := append([]string(nil), r.Files...)
	if profile == "" {
		return files, nil
	}
	extra, ok := r.Profiles[profile]
	if !ok {
		known := make([]string, 0, len(r.Profiles))
		for name := range r.Profiles {
			known = append(known, name)
		}
		sort.Strings(known)
		return nil, ValidationError(nil, "unknown deployment profile %q", profile).
			WithHint("profiles declared by this release: %s", strings.Join(known, ", "))
	}
	// A profile file already listed in `files` must not be passed twice:
	// Compose would merge it with itself and the operator would see
	// confusing duplicate-key diagnostics.
	for _, f := range extra {
		if !containsString(files, f) {
			files = append(files, f)
		}
	}
	return files, nil
}

// RuntimeDecl is what one runtime needs from the bundle: the files it is
// declared with, and the extra files each deployment profile adds.
//
// One `files` field, not a differently named key per runtime. RFC 0023 §4.1
// sketched `units:` for Quadlet beside `files:` for Compose, and that sketch
// cannot be validated here: deciding which key is legal means asking which
// runtime this is, and a branch on a runtime's name above `internal/adapters`
// is what decision 7 forbids and `tools/runtimecheck` fails the build over.
// What the files *mean* is the adapter's to know -- a `.container` unit and a
// `compose.yaml` are both paths into the bundle from here.
type RuntimeDecl struct {
	Files    []string            `yaml:"files" json:"files"`
	Profiles map[string][]string `yaml:"profiles" json:"profiles,omitempty"`
}

// FilesFor returns this runtime's files for a deployment profile.
//
// The same rule RuntimeSpec.ComposeFiles applies, and deliberately the same
// text: an unknown profile refuses rather than falling back to base, because
// deploying the wrong topology quietly is worse than stopping.
func (d RuntimeDecl) FilesFor(profile string) ([]string, error) {
	files := append([]string(nil), d.Files...)
	if profile == "" {
		return files, nil
	}
	extra, ok := d.Profiles[profile]
	if !ok {
		known := make([]string, 0, len(d.Profiles))
		for name := range d.Profiles {
			known = append(known, name)
		}
		sort.Strings(known)
		return nil, ValidationError(nil, "unknown deployment profile %q", profile).
			WithHint("profiles declared by this release: %s", strings.Join(known, ", "))
	}
	for _, f := range extra {
		if !containsString(files, f) {
			files = append(files, f)
		}
	}
	return files, nil
}

// LegacyRuntimeName is the runtime a manifest declares when it uses the old
// `runtime:` block and says nothing else.
//
// This is RFC 0023 §2.1's second expensive leak, and it is deliberately still
// here: `tools/runtimecheck` carries it in the inventory because the statement
// it makes is about history rather than about dispatch. A manifest written
// before `runtimes:` existed does declare Compose -- that is what the block
// meant -- and a manager that would not say so could not read a released
// bundle at all. It leaves when the legacy block does, and not before.
const LegacyRuntimeName = "compose"

// isZero reports whether the legacy block was written at all. Files rather
// than Project, because ApplyDefaults fills Project from the product name and
// so a defaulted manifest is never zero by that measure.
func (r RuntimeSpec) isZero() bool { return len(r.Files) == 0 && len(r.Profiles) == 0 }

// Runtimes maps a runtime's name to what that runtime is declared with.
//
// The keys are the declaration (RFC 0023 decision 8). `providers.runtime.name`
// is not the selector and cannot be: it is a single `Provider` beside
// `secrets` and `backup`, so it holds one value, and §4.1 requires a bundle to
// be able to declare two runtimes at once -- which decision 4's
// `--render-check` then renders both of.
type Runtimes map[string]RuntimeDecl

// Names returns the declared runtimes, sorted, so every message that lists
// them lists them in the same order.
func (r Runtimes) Names() []string {
	names := make([]string, 0, len(r))
	for name := range r {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type OSRequirement struct {
	ID      string     `yaml:"id" json:"id"`
	Version Constraint `yaml:"version" json:"version,omitempty"`
}

type Requirements struct {
	Architectures []string              `yaml:"architectures" json:"architectures,omitempty"`
	OS            []OSRequirement       `yaml:"os" json:"os,omitempty"`
	Tools         map[string]Constraint `yaml:"tools" json:"tools,omitempty"`
	Memory        ByteSize              `yaml:"memory" json:"memory,omitempty"`
	Disk          ByteSize              `yaml:"disk" json:"disk,omitempty"`

	// CPUs is the number of logical CPUs the release wants.
	//
	// Logical rather than physical cores, and a cgroup quota is honoured
	// where one is in force: those are the three things a machine can mean
	// by "how many CPUs", and the one that matters is how much parallelism
	// the product will actually get.
	CPUs int `yaml:"cpus" json:"cpus,omitempty"`

	// Ports are strings so one field covers both a literal `18080` and a
	// `"{{ .Parameters.http_port }}"`. Resolve them with
	// Manifest.ResolvePorts; reading them raw gives a template, not a
	// number.
	Ports []PortSpec `yaml:"ports" json:"ports,omitempty"`
}

// PortSpec is a port requirement: a literal, or a parameter reference.
//
// A distinct type rather than a bare string so YAML's `[18080]` decodes without
// quoting -- an existing manifest keeps working, and a vendor does not have to
// learn that a number must now be a string.
//
// Accepting the integer means accepting YAML's bases with it: `010` is octal
// and becomes port 8, `0x10` is hex and becomes port 16. Left alone for the
// same reason as ByteSize -- a zero-padded port is not a real spelling, and
// refusing integers would undo exactly what this type exists for -- and pinned
// by test so it is recorded rather than discovered.
type PortSpec string

func (p *PortSpec) UnmarshalYAML(unmarshal func(any) error) error {
	var n int
	if err := unmarshal(&n); err == nil {
		*p = PortSpec(strconv.Itoa(n))
		return nil
	}
	var s string
	if err := unmarshal(&s); err != nil {
		return fmt.Errorf("must be a port number or a {{ .Parameters.<name> }} reference")
	}
	*p = PortSpec(s)
	return nil
}

func (p PortSpec) MarshalJSON() ([]byte, error) { return json.Marshal(string(p)) }

func (p *PortSpec) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*p = PortSpec(s)
		return nil
	}
	var n int
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("must be a port number or a {{ .Parameters.<name> }} reference")
	}
	*p = PortSpec(strconv.Itoa(n))
	return nil
}

type ConfigurationFile struct {
	Template string   `yaml:"template" json:"template"`
	Target   string   `yaml:"target" json:"target"`
	Mode     FileMode `yaml:"mode" json:"mode,omitempty"`
}

type SecretsSpec struct {
	Source   string `yaml:"source" json:"source"`
	RenderTo string `yaml:"render_to" json:"render_to"`
	Schema   string `yaml:"schema" json:"schema"`
}

// OperationKind distinguishes how a manifest-declared operation is executed.
// The two kinds have different failure semantics, which is why they are not
// collapsed into one "command" field.
type OperationKind string

const (
	// OperationKindRuntimeService runs a one-shot container from the
	// Compose project -- it sees the application network and secrets.
	OperationKindRuntimeService OperationKind = "runtime-service"
	// OperationKindHook runs an executable from the release directory on
	// the host, under the hook ABI.
	OperationKindHook OperationKind = "hook"
)

type OperationSpec struct {
	Kind    OperationKind `yaml:"kind" json:"kind"`
	Service string        `yaml:"service" json:"service,omitempty"`
	Command []string      `yaml:"command" json:"command,omitempty"`
	Timeout Duration      `yaml:"timeout" json:"timeout,omitempty"`
}

// Named manifest operations the lifecycle layer looks up by convention.
const (
	OpMigrate   = "migrate"
	OpSmokeTest = "smoke_test"
	OpBackup    = "backup"
	OpRestore   = "restore"
	OpPreflight = "preflight"
)

// BackupSpec is what the release says about backing up storage the manager can
// reach without a client for whatever wrote it.
//
// It exists because the manager cannot answer the one question that decides
// whether a volume copy is worth anything: is it safe to read this while the
// product is writing to it. The vendor knows. Postgres' data directory is not;
// a directory of uploaded files that are written once and never modified is.
// So the vendor declares, the manager enforces, and the backup manifest
// records which claim was made -- the same shape as parameters, for the same
// reason.
type BackupSpec struct {
	// Volumes classifies the project's named volumes by the key they have
	// in the Compose `volumes:` block.
	//
	// Partial by design: a volume absent from this map is captured cold,
	// which is correct for every volume and slow for some. A vendor
	// declares only where they want something other than the safe default.
	Volumes map[string]VolumeSpec `yaml:"volumes" json:"volumes,omitempty"`
}

// VolumeSpec is one volume's declaration. Listing a volume here means saying
// something about it: the consistency is required, because the way to ask for
// the default is to leave the volume out.
type VolumeSpec struct {
	Consistency VolumeConsistency `yaml:"consistency" json:"consistency,omitempty"`
}

// VolumeConsistency is a vendor's claim about reading one volume live.
type VolumeConsistency string

const (
	// VolumeCold is the default and needs no declaration: the services
	// that mount the volume are stopped for the duration of the copy.
	VolumeCold VolumeConsistency = "cold"

	// VolumeHot claims the volume may be read while its services run.
	//
	// The vendor is promising that a crash-consistent copy of this volume
	// is a usable one -- true for write-once files, false for anything
	// with a write-ahead log or an index it rebuilds on start.
	VolumeHot VolumeConsistency = "hot"

	// VolumeExclude keeps the manager out of a volume entirely, which is
	// the expected declaration for a database's storage: the hook owns it,
	// and a second copy taken by other means invites somebody to restore
	// the wrong one.
	VolumeExclude VolumeConsistency = "exclude"
)

// VolumeConsistencies is every legal value, for validation and for the
// generated schema.
var VolumeConsistencies = []VolumeConsistency{VolumeCold, VolumeHot, VolumeExclude}

// Consistency resolves a volume's declared consistency, defaulting to cold.
//
// The default is the slow one on purpose. A manager that guessed `hot` for an
// undeclared volume would be making the vendor's claim on their behalf, and
// would be wrong exactly where it costs the most -- a database volume nobody
// remembered to exclude.
func (b BackupSpec) Consistency(volume string) VolumeConsistency {
	spec, ok := b.Volumes[volume]
	if !ok || spec.Consistency == "" {
		return VolumeCold
	}
	return spec.Consistency
}

// BundleSpec is what a release says about its own packaging.
type BundleSpec struct {
	// UncompressedSize is what the archive expands to, and it exists to
	// raise the extraction ceiling for a bundle carrying container images.
	//
	// It is read out of the tar stream *before the signature is checked*, so
	// it is attacker-controlled input in the strictest sense: it may only
	// ever **lower** the effective limit, never raise it above the hard cap.
	// A declaration is a request for a smaller budget than the manager
	// allows, not permission to exceed it, and an absent one means the
	// default ceiling rather than "unbounded" -- a missing field must never
	// be the permissive reading of anything that gates untrusted bytes.
	//
	// The clamp lives in the extractor, not in this validation. By the time
	// a manifest validates, the value has already been read from the stream
	// and used.
	UncompressedSize ByteSize `yaml:"uncompressed_size" json:"uncompressed_size,omitempty"`
}

type HealthSpec struct {
	Checks []HealthCheck `yaml:"checks" json:"checks,omitempty"`
}

type HealthCheckType string

const (
	HealthHTTP    HealthCheckType = "http"
	HealthTCP     HealthCheckType = "tcp"
	HealthCommand HealthCheckType = "command"
)

type HealthCheck struct {
	Name    string          `yaml:"name" json:"name"`
	Type    HealthCheckType `yaml:"type" json:"type"`
	URL     string          `yaml:"url" json:"url,omitempty"`
	Address string          `yaml:"address" json:"address,omitempty"`
	Command []string        `yaml:"command" json:"command,omitempty"`
	Timeout Duration        `yaml:"timeout" json:"timeout,omitempty"`

	// StartPeriod is how long this check may keep failing before the
	// failure means anything.
	//
	// Distinct from Timeout, which bounds a single attempt. Without it a
	// product with a slow first boot and a product that is dead are the
	// same observation, and the only lever is a timeout long enough to
	// delay noticing the second. Zero means the waiter keeps trying for as
	// long as the operation allows, which is what it has always done.
	StartPeriod Duration `yaml:"start_period" json:"start_period,omitempty"`
}

type Retention struct {
	Releases int `yaml:"releases" json:"releases"`
	Backups  int `yaml:"backups" json:"backups"`

	// declared records which fields the manifest actually named.
	//
	// Without it ApplyDefaults cannot tell "absent" from "written as 0",
	// and it runs before Validate -- so `retention: {releases: 0}` became 3
	// and the refusal Validate exists to give was never reached. A vendor
	// who wrote a zero meant something by it, and 3 is not it.
	declared struct{ releases, backups bool }
}

// UnmarshalYAML records presence alongside the values.
func (r *Retention) UnmarshalYAML(unmarshal func(any) error) error {
	var raw struct {
		Releases *int `yaml:"releases"`
		Backups  *int `yaml:"backups"`
	}
	if err := unmarshal(&raw); err != nil {
		return err
	}
	if raw.Releases != nil {
		r.Releases, r.declared.releases = *raw.Releases, true
	}
	if raw.Backups != nil {
		r.Backups, r.declared.backups = *raw.Backups, true
	}
	return nil
}

// Defaults for fields the spec gives a safe default. Applied after decoding
// so an absent field and an explicit zero stay distinguishable during
// validation.
const (
	DefaultRetentionReleases = 3
	DefaultRetentionBackups  = 7
	DefaultOperationTimeout  = 10 * time.Minute
	DefaultHealthTimeout     = 120 * time.Second
	DefaultConfigMode        = FileMode(0o640)
)

// ApplyDefaults fills in absent optional fields. Called by the loader before
// Validate, never by callers directly.
func (m *Manifest) ApplyDefaults() {
	// Absent, not merely zero: an explicit `releases: 0` is a value
	// Validate refuses, and defaulting it here would answer a vendor's
	// mistake by silently keeping three releases instead.
	if !m.Retention.declared.releases {
		m.Retention.Releases = DefaultRetentionReleases
	}
	if !m.Retention.declared.backups {
		m.Retention.Backups = DefaultRetentionBackups
	}
	// Derived, not stored: DeclaredRuntimes folds the legacy block in on
	// every call, so nothing here can go stale against a later edit.
	declared, _ := m.DeclaredRuntimes()
	if m.Providers.Runtime.Name == "" && len(declared) == 1 {
		// Derived rather than hardcoded. A single-runtime release
		// leaves the field meaning what it always meant; a
		// two-runtime release leaves it empty, because there is no
		// one value it could take that would not be a lie about the
		// other one.
		m.Providers.Runtime.Name = declared.Names()[0]
	}
	if m.Providers.Secrets.Name == "" {
		m.Providers.Secrets.Name = "sops-age"
	}
	if m.Providers.Backup.Name == "" {
		m.Providers.Backup.Name = "hooks"
	}
	if m.Runtime.Project == "" {
		m.Runtime.Project = m.Metadata.Name
	}
	for i := range m.Configuration {
		if m.Configuration[i].Mode == 0 {
			m.Configuration[i].Mode = DefaultConfigMode
		}
	}
	for name, op := range m.Operations {
		if op.Timeout == 0 {
			op.Timeout = Duration(DefaultOperationTimeout)
			m.Operations[name] = op
		}
	}
	for i := range m.Health.Checks {
		if m.Health.Checks[i].Timeout == 0 {
			m.Health.Checks[i].Timeout = Duration(DefaultHealthTimeout)
		}
	}
	if m.Secrets.RenderTo == "" && m.Secrets.Source != "" {
		m.Secrets.RenderTo = DefaultPaths(m.Metadata.Name).SecretsRenderDir()
	}
}

// digestRef matches an image reference pinned by digest. Anything else -- a
// bare tag, a floating `latest` -- is rejected: an unpinned image makes a
// release mutable, and a mutable release makes rollback meaningless.
var digestRef = regexp.MustCompile(`^[^\s@]+@sha256:[a-f0-9]{64}$`)

// runtimeName is the grammar of a runtime's name. It is deliberately not a list
// of them.
//
// What this can check and what it cannot is the whole point. It rejects a name
// that is empty, padded, capitalised, or carrying anything a terminal would
// interpret -- and that matters, because this value is read out of a file
// somebody may have hand-edited and then printed in error messages. It cannot
// reject `quadlt`, and nothing at this layer could: whether a runtime exists is
// not a fact the domain has, and a list of known names here would be the
// runtime catalogue above `internal/adapters` that decision 7 exists to
// prevent.
//
// The name that is well-formed and wrong is caught where the knowledge is: the
// adapter reports its own name, and an installation whose runtime disagrees is
// refused before any operation runs. Two checks, each at the layer that can
// actually make it.
//
// Shaped after productNamePattern, which answers the same question about a
// different identifier.
var runtimeName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// ValidRuntimeName reports whether a name is well-formed. Exported because both
// the manifest's keys and the installation's recorded runtime are the same kind
// of token and must agree about what one looks like -- two spellings of this
// rule would let a manifest declare a name the state could not record.
func ValidRuntimeName(name string) bool { return runtimeName.MatchString(name) }

// Validate checks every rule in the manifest contract and reports all
// violations at once. Bundle authors iterate faster when one run surfaces
// every problem instead of the first.
func (m *Manifest) Validate() error {
	var v validationErrors

	// api_version and kind gate everything else: if the schema is unknown,
	// field-level complaints would be noise.
	if m.APIVersion == "" {
		v.add("api_version", "is required")
	} else if !isSupportedAPIVersion(m.APIVersion) {
		return IncompatibleError(ErrUnknownAPIVersion, "manifest api_version %q is not supported", m.APIVersion).
			WithHint("this manager reads: %s. Upgrade the manager, or use a bundle built for this version.",
				joinAPIVersions(SupportedAPIVersions))
	}
	if m.Kind != KindApplicationRelease {
		v.add("kind", "must be %q, got %q", KindApplicationRelease, m.Kind)
	}

	// metadata
	if m.Metadata.Name == "" {
		v.add("metadata.name", "is required")
	} else if err := ValidateProductName(m.Metadata.Name); err != nil {
		v.add("metadata.name", "%s", AsError(err).Message)
	}
	if m.Metadata.Version.IsZero() {
		v.add("metadata.version", "is required and must be a semantic version")
	} else if meta := m.Metadata.Version.Metadata(); meta != "" {
		// Build metadata is a silent bypass of the guard that makes
		// version identity mean anything. String() retains it, so the
		// release store -- keyed by the string -- puts two
		// metadata-differing builds in different directories; Compare
		// ignores it, so the "already installed with a different
		// digest" check never compares them at all. Two different
		// bundles then claim one version and nothing notices.
		//
		// Constraints keep accepting metadata: `upgrade_from:
		// ">=1.0.0+build.7"` is a range, not an identity, and metadata
		// already decides nothing there.
		v.add("metadata.version", "must not carry build metadata, and this carries %q", "+"+meta)
	}
	if m.Metadata.ReleaseNotes != "" {
		v.checkRelPath("metadata.release_notes", m.Metadata.ReleaseNotes)
	}
	if u := m.Metadata.SupportURL; u != "" {
		// Parsed rather than prefix-matched. A prefix check accepts
		// `https://` and `https:///help`, which have no host to reach,
		// and it accepts a value carrying terminal escape sequences --
		// this string is printed by `status` and by a failing `doctor`,
		// so it reaches a terminal at the worst possible moment. Parsing
		// answers all three: url.Parse refuses ASCII control characters
		// outright, and scheme and host are then checkable facts rather
		// than the first eight bytes of a string.
		//
		// https only, matching every other URL this project accepts from
		// a bundle. A support link is shown to an operator who is
		// already in trouble, which is the worst moment to send them
		// somewhere over plaintext.
		switch parsed, err := url.Parse(u); {
		case err != nil:
			v.add("metadata.support_url", "is not a URL: %s", err)
		case parsed.Scheme != "https":
			v.add("metadata.support_url", "must be an https URL, got scheme %q", parsed.Scheme)
		case parsed.Host == "":
			v.add("metadata.support_url", "names no host, got %q", u)
		}
	}

	// Computed before the providers block below, which needs to know how
	// many runtimes are declared to know whether one name can be true.
	declaredRuntimes, fromLegacy := m.DeclaredRuntimes()

	// providers
	//
	// Required only where a single value can be true. ApplyDefaults derives
	// this field from a single declared runtime and deliberately leaves it
	// empty for a release declaring two, because no one name is honest about
	// the other -- so requiring it unconditionally made every two-runtime
	// manifest unvalidatable, and the shape decision 8 settled unreachable.
	// The field stays required for the single-runtime case, where it is what
	// it has always been.
	if m.Providers.Runtime.Name == "" && len(declaredRuntimes) != 2 {
		v.add("providers.runtime.name", "is required")
	}

	// runtimes
	//
	// ApplyDefaults has already folded a legacy `runtime:` block into the
	// map, so there is one shape to check. What it cannot fold is a
	// manifest carrying both: merging them would pick a winner the vendor
	// never nominated, and picking the wrong one deploys a topology nobody
	// asked for.
	if len(m.Runtimes) > 0 && !m.Runtime.isZero() {
		v.add("runtimes", "cannot be used together with the deprecated `runtime:` block; "+
			"move the files under `runtimes."+LegacyRuntimeName+".files` and delete `runtime:`")
	}
	if len(declaredRuntimes) == 0 {
		// Both spellings named, because with nothing declared there is
		// no signal for which one the vendor is writing -- and the
		// per-runtime messages below, which do name the right field,
		// never run for a manifest that declares nothing at all.
		v.add("runtimes", "must declare at least one runtime, each listing at least one file; "+
			"a release still using the deprecated `runtime:` block lists them under `runtime.files`")
	}
	for _, name := range declaredRuntimes.Names() {
		decl := declaredRuntimes[name]
		// An empty key is the sharp one. It validates, `runtimeForRelease`
		// records "", and `Installation.RuntimeName` reads "" as the
		// legacy runtime -- so a release that declared something else
		// entirely installs as Compose, and every later message agrees
		// with the wrong answer. Named as a key rather than a value,
		// because that is what the vendor has to go and find.
		if !ValidRuntimeName(name) {
			if strings.TrimSpace(name) == "" {
				v.add("runtimes", "declares a runtime with an empty name")
			} else {
				v.add("runtimes."+name,
					"is not a usable runtime name: lower-case letters, digits and "+
						"hyphens, starting with a letter, at most 32 characters")
			}
			continue
		}
		// The field path a vendor can search for in their own file.
		base := "runtimes." + name
		if fromLegacy {
			base = "runtime"
		}
		if len(decl.Files) == 0 {
			v.add(base+".files", "must list at least one file")
		}
		for i, f := range decl.Files {
			v.checkRelPath(fmt.Sprintf("%s.files[%d]", base, i), f)
		}
		for profile, files := range decl.Profiles {
			if len(files) == 0 {
				v.add(base+".profiles."+profile, "must list at least one file")
			}
			for i, f := range files {
				v.checkRelPath(fmt.Sprintf("%s.profiles.%s[%d]", base, profile, i), f)
			}
		}
	}

	// parameters
	ValidateParameters(m.Parameters, &v)

	if m.Bundle.UncompressedSize < 0 {
		v.add("bundle.uncompressed_size", "must not be negative, got %s",
			m.Bundle.UncompressedSize)
	}

	if m.Requirements.CPUs < 0 {
		v.add("requirements.cpus", "must not be negative, got %d", m.Requirements.CPUs)
	}

	// requirements.ports -- literal or a parameter reference, never junk
	for i, port := range m.Requirements.Ports {
		field := fmt.Sprintf("requirements.ports[%d]", i)
		text := strings.TrimSpace(string(port))
		switch {
		case text == "":
			v.add(field, "is empty")
		case strings.Contains(text, "{{"):
			// Resolvability is checked against the installation's
			// values; here only that it parses at all, so a
			// malformed template fails at `release verify` rather
			// than midway through an apply.
			empty := Parameters{}
			_, err := empty.Resolve(field, text)
			if errors.Is(err, ErrTemplateSyntax) {
				v.add(field, "%s", AsError(err).Message)
			}
		default:
			if n, err := strconv.Atoi(text); err != nil || n < 1 || n > 65535 {
				v.add(field, "must be a port number (1-65535) or a parameter reference, got %q", text)
			}
		}
	}

	// images -- the pinning rule, and where the bytes come from
	if len(m.Images) == 0 {
		v.add("images", "must declare at least one image")
	}
	for _, name := range sortedImageNames(m.Images) {
		spec := m.Images[name]
		field := "images." + name
		if !imageName.MatchString(name) {
			v.add(field, "must be lowercase letters, digits and hyphens, "+
				"starting and ending with a letter or digit")
			continue
		}
		if !digestRef.MatchString(spec.Ref) {
			v.add(field, "must be pinned by digest (name@sha256:...), got %q", spec.Ref)
		}
		// A misspelled source is refused rather than defaulted. Both
		// plausible typos -- `bundled`, and `from` under the wrong image
		// -- fail towards a release the vendor believes ships its own
		// bytes and does not, which surfaces as a credential failure on
		// a customer's machine.
		switch spec.From {
		case ImageFromRegistry, ImageFromBundle, "":
		default:
			v.add(field+".from", "must be %s, got %q",
				joinImageSources(ImageSources), spec.From)
		}
	}

	// configuration
	for i, c := range m.Configuration {
		field := fmt.Sprintf("configuration[%d]", i)
		v.checkRelPath(field+".template", c.Template)
		if c.Target == "" {
			v.add(field+".target", "is required")
		} else if !path.IsAbs(c.Target) {
			v.add(field+".target", "must be an absolute path, got %q", c.Target)
		}
	}

	// secrets
	if m.Secrets.Schema != "" {
		v.checkRelPath("secrets.schema", m.Secrets.Schema)
	}
	if m.Secrets.Source != "" && !path.IsAbs(m.Secrets.Source) {
		v.add("secrets.source", "must be an absolute path, got %q", m.Secrets.Source)
	}
	if m.Secrets.RenderTo != "" && !path.IsAbs(m.Secrets.RenderTo) {
		v.add("secrets.render_to", "must be an absolute path, got %q", m.Secrets.RenderTo)
	}

	// operations
	for name, op := range m.Operations {
		field := "operations." + name
		switch op.Kind {
		case OperationKindRuntimeService:
			if op.Service == "" {
				v.add(field+".service", "is required for kind %q", OperationKindRuntimeService)
			}
			if len(op.Command) > 0 {
				v.add(field+".command", "is not allowed for kind %q; use the service's own entrypoint",
					OperationKindRuntimeService)
			}
		case OperationKindHook:
			if len(op.Command) == 0 {
				v.add(field+".command", "is required for kind %q", OperationKindHook)
			} else {
				v.checkRelPath(field+".command[0]", op.Command[0])
			}
			if op.Service != "" {
				v.add(field+".service", "is not allowed for kind %q", OperationKindHook)
			}
		case "":
			v.add(field+".kind", "is required (%q or %q)", OperationKindRuntimeService, OperationKindHook)
		default:
			v.add(field+".kind", "must be %q or %q, got %q",
				OperationKindRuntimeService, OperationKindHook, op.Kind)
		}
		if op.Timeout < 0 {
			v.add(field+".timeout", "must be positive")
		}
	}

	// backup.volumes
	//
	// A misspelled consistency is refused here rather than defaulted. The
	// two plausible typos -- `warm`, and `hot` under the wrong volume name
	// -- both fail towards a backup the vendor believes is fast and
	// correct, and silently defaulting to cold would hide the first while
	// silently accepting the second.
	for name, spec := range m.Backup.Volumes {
		field := "backup.volumes." + name
		if name == "" {
			v.add("backup.volumes", "has an entry with an empty volume name")
		}
		switch spec.Consistency {
		case VolumeCold, VolumeHot, VolumeExclude:
		case "":
			// An entry that declares nothing is refused rather than
			// defaulted to cold, and the generated schema requires
			// the key for the same reason. Leaving a volume out of
			// this map is already how a vendor asks for the default,
			// so a volume listed with no consistency is a
			// half-finished edit or a value that templated away to
			// empty -- both leave the vendor believing they declared
			// something they did not.
			//
			// The schema said this from the start: its enum is the
			// three values, so `consistency: ""` failed in an editor
			// and passed here. A loader more permissive than the
			// published schema is the disagreement this repository
			// generates the schema to avoid, and the strict side is
			// the right one to settle on.
			v.add(field+".consistency", "is required (%s); omit the volume entirely for the default",
				joinConsistencies(VolumeConsistencies))
		default:
			v.add(field+".consistency", "must be %s, got %q",
				joinConsistencies(VolumeConsistencies), spec.Consistency)
		}
	}

	// health
	seenCheck := map[string]bool{}
	for i, c := range m.Health.Checks {
		field := fmt.Sprintf("health.checks[%d]", i)
		if c.Name == "" {
			v.add(field+".name", "is required")
		} else if seenCheck[c.Name] {
			v.add(field+".name", "duplicate health check name %q", c.Name)
		} else {
			seenCheck[c.Name] = true
		}
		switch c.Type {
		case HealthHTTP:
			if c.URL == "" {
				v.add(field+".url", "is required for type %q", HealthHTTP)
			}
		case HealthTCP:
			if c.Address == "" {
				v.add(field+".address", "is required for type %q", HealthTCP)
			}
		case HealthCommand:
			if len(c.Command) == 0 {
				v.add(field+".command", "is required for type %q", HealthCommand)
			} else {
				v.checkRelPath(field+".command[0]", c.Command[0])
			}
		case "":
			v.add(field+".type", "is required (%q, %q or %q)", HealthHTTP, HealthTCP, HealthCommand)
		default:
			v.add(field+".type", "unknown health check type %q", c.Type)
		}
		if c.StartPeriod < 0 {
			v.add(field+".start_period", "must not be negative")
		}
	}

	// compatibility
	//
	// Negative schema numbers are refused by name rather than ignored.
	// Every check that reads one is guarded by `> 0` so that an *absent*
	// declaration decides nothing -- which means a negative one decides
	// nothing either, while looking to a vendor like a declaration they
	// made. For database_schema_produces that is the difference between a
	// release being refused by the unattended gate and passing its schema
	// half without being looked at.
	for _, f := range []struct {
		name  string
		value int
	}{
		{"database_schema_min", m.Compatibility.DatabaseSchemaMin},
		{"database_schema_max", m.Compatibility.DatabaseSchemaMax},
		{"database_schema_produces", m.Compatibility.DatabaseSchemaProduces},
	} {
		if f.value < 0 {
			v.add("compatibility."+f.name,
				"must not be negative, and is %d", f.value)
		}
	}
	if m.Compatibility.DatabaseSchemaMin > 0 && m.Compatibility.DatabaseSchemaMax > 0 &&
		m.Compatibility.DatabaseSchemaMin > m.Compatibility.DatabaseSchemaMax {
		v.add("compatibility", "database_schema_min (%d) exceeds database_schema_max (%d)",
			m.Compatibility.DatabaseSchemaMin, m.Compatibility.DatabaseSchemaMax)
	}

	// retention
	if m.Retention.Releases < 1 {
		v.add("retention.releases", "must keep at least 1 release")
	}
	if m.Retention.Backups < 1 {
		v.add("retention.backups", "must keep at least 1 backup")
	}

	// extensions -- namespaced keys only, so a typo'd core field cannot
	// hide inside a vendor block
	for ns := range m.Extensions {
		if !strings.Contains(ns, ".") && !strings.Contains(ns, "/") {
			v.add("extensions."+ns, "must be namespaced, e.g. example.com/%s", ns)
		}
	}

	return v.err()
}

// Operation looks up a manifest-declared operation by name.
func (m *Manifest) Operation(name string) (OperationSpec, bool) {
	op, ok := m.Operations[name]
	return op, ok
}

// ImageRefs returns every image reference in a stable order, so pull plans and
// dry-run output do not shuffle between runs.
//
// Every image, bundled or not, under the reference the manifest pins: this
// answers "what does this release consist of". Two neighbouring questions have
// different answers -- what a deployment fetches from a registry is
// PulledImageRefs, and what the daemon must resolve is RuntimeImageRefs.
func (m *Manifest) ImageRefs() []string {
	names := sortedImageNames(m.Images)
	refs := make([]string, 0, len(names))
	for _, n := range names {
		refs = append(refs, m.Images[n].Ref)
	}
	return refs
}

func isSupportedAPIVersion(v APIVersion) bool {
	for _, s := range SupportedAPIVersions {
		if s == v {
			return true
		}
	}
	return false
}

func joinAPIVersions(vs []APIVersion) string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = string(v)
	}
	return strings.Join(out, ", ")
}

func joinConsistencies(vs []VolumeConsistency) string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = strconv.Quote(string(v))
	}
	return strings.Join(out, ", ")
}

func containsString(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// validationErrors accumulates field-level complaints so Validate can report
// every problem in one pass.
type validationErrors struct {
	problems []string
}

func (v *validationErrors) add(field, format string, args ...any) {
	v.problems = append(v.problems, fmt.Sprintf("%s: %s", field, fmt.Sprintf(format, args...)))
}

// checkRelPath enforces the manifest rule that every bundle-relative path
// stays inside the release root. This is a contract check, not a security
// boundary -- extraction and rendering enforce containment through os.Root.
func (v *validationErrors) checkRelPath(field, p string) {
	switch {
	case p == "":
		v.add(field, "is required")
	case path.IsAbs(p):
		v.add(field, "must be relative to the release root, got %q", p)
	case p != path.Clean(p):
		v.add(field, "must be a clean path, got %q (did you mean %q?)", p, path.Clean(p))
	case p == ".." || strings.HasPrefix(p, "../"):
		v.add(field, "must not escape the release root, got %q", p)
	}
}

func (v *validationErrors) err() error {
	if len(v.problems) == 0 {
		return nil
	}
	sort.Strings(v.problems)
	return ValidationError(nil, "manifest is invalid:\n  - %s", strings.Join(v.problems, "\n  - "))
}
