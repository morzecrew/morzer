package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
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

// RuntimesMinManagerVersion is the manager release that added `runtimes:`.
//
// A bundle written with it is refused outright by anything older, because
// strict decoding makes an unknown field fatal -- so a manifest using it has to
// declare this as its `compatibility.min_manager_version` or the vendor's
// customer gets a report about a typo instead of an upgrade requirement (RFC
// 0018 decision 1). Kept beside FieldRemovalRelease so the whole migration's
// clock reads in one place: the new spelling arrives here and the old one stops
// being read there.
const RuntimesMinManagerVersion = "0.3.0"

// FieldRemovalRelease is the release that stops reading the fields
// DeprecatedFields reports.
//
// A deprecation without one is a word in a document: it tells a vendor that
// something will happen and gives them nothing to plan against.
//
// **DeprecatedFields currently reports none**, so this names no live
// deprecation: `runtime:` was its only member and stopped being read in 0.3.0
// (RFC 0023 decision 23), which is one release earlier than this constant ever
// said, because the grace period it promised turned out never to have existed.
// The value is what the next deprecated field would target.
//
// That there is one value for all of them is a single-member design, and it is
// only visibly so now that the member is gone: two fields deprecated in
// different releases cannot share it. Left as it is deliberately -- the shape
// to choose is the next deprecation's to force, and guessing it with nothing to
// deprecate is how a mechanism gets built for a caller that never arrives.
const FieldRemovalRelease = "0.4.0"

// FieldDeprecation is a manifest field this manager still reads and will stop
// reading at FieldRemovalRelease.
//
// Separate from DeprecatedAPIVersions because a field cannot be a map key: an
// api_version is deprecated by its value, and a field is deprecated by being
// written at all, which only the manifest itself can answer.
type FieldDeprecation struct {
	// Field is spelled the way the vendor spelled it, for the same reason
	// Validate's paths are: naming `runtimes.compose.files` to somebody
	// whose manifest says `runtime.files` sends them looking for a block
	// that is not there.
	Field string

	// Replacement is what to write instead. Nothing here enforces that it
	// is set -- the type cannot -- but a deprecation naming no successor is
	// a complaint rather than an instruction, and the tests that read
	// Message assert it is there.
	Replacement string
}

// Message is the sentence shown to whoever met the manifest. One sentence,
// carrying all three things they need: what is deprecated, when it stops being
// read, and what to write instead.
func (f FieldDeprecation) Message() string {
	return fmt.Sprintf("`%s` is deprecated and will stop being read in %s; use %s",
		f.Field, FieldRemovalRelease, f.Replacement)
}

// DeprecatedFields reports the deprecated fields this manifest actually uses.
//
// **There are none.** `runtime:` was the only one and is no longer read at all
// (RFC 0023 decision 23) -- a field that is refused is not deprecated, it is
// gone, and reporting it here would offer a vendor a grace period the loader
// will not honour.
//
// The mechanism is kept rather than deleted with its member. It is RFC 0018's,
// it is the only field-level deprecation machinery this manifest has, and the
// next field to need one would otherwise rebuild it -- differently, which is
// how one manifest comes to have two ways of saying the same thing. What keeps
// it honest with nothing to report is the test that drives it with a synthetic
// deprecation; without that, an empty registry is an untested one.
func (m Manifest) DeprecatedFields() []FieldDeprecation {
	return nil
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

// DeclaredRuntimes returns the runtimes this release supports.
//
// It no longer reads the deprecated `runtime:` block: that block stopped being
// read in 0.3.0 (RFC 0023 decision 23), and Validate refuses a manifest
// carrying it, so a legacy bundle is answered by a refusal naming the
// migration rather than by a fold nothing announces.
//
// Derived on every call rather than normalised once into a field, and that is
// worth keeping now that there is nothing to fold. The first version of this
// stored the synthesis in ApplyDefaults, which made it a snapshot: anything
// that touched the block afterwards was silently ignored, and `Validate`
// called without ApplyDefaults checked an empty map -- so a `runtime.files`
// entry of `/etc/passwd` passed validation. A path-escape check that holds
// only when another method ran first is not a check.
func (m Manifest) DeclaredRuntimes() Runtimes {
	return m.Runtimes
}

// ProfileNames is every deployment profile this release declares, sorted and
// deduplicated across its runtimes.
//
// One implementation because there are three callers -- the `init` wizard's
// question, `release show`, and the synthetic profile `release verify
// --render-check` renders with -- and each of them read
// `Manifest.Runtime.Profiles` directly. That field stopped being read in 0.3.0
// (decision 23), so all three silently answered "no profiles" for every bundle
// written in the current spelling: an empty list is also what a release with no
// profiles looks like, so none of them failed, they just stopped being right.
//
// A union across runtimes rather than one runtime's, because a profile is the
// operator's choice of topology and the manifest is what says which exist. A
// release whose runtimes disagree about profiles is the vendor's to reconcile,
// and offering the smaller set would hide the disagreement rather than surface
// it.
func (m Manifest) ProfileNames() []string {
	seen := map[string]bool{}
	for _, decl := range m.DeclaredRuntimes() {
		for name := range decl.Profiles {
			seen[name] = true
		}
	}
	out := slices.Sorted(maps.Keys(seen))
	return out
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
		known := slices.Sorted(maps.Keys(r.Profiles))
		return nil, ValidationError(nil, "unknown deployment profile %q", profile).
			WithHint("profiles declared by this release: %s", strings.Join(known, ", "))
	}
	// A profile file already listed in `files` must not be passed twice:
	// Compose would merge it with itself and the operator would see
	// confusing duplicate-key diagnostics.
	for _, f := range extra {
		if !slices.Contains(files, f) {
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

	// Options are settings this runtime understands and the manager does
	// not. They are carried, bounded and compared here, and never
	// interpreted: what `project` means is Compose's business, and a
	// manager that knew would be branching on a runtime's name to find out
	// (decision 7).
	//
	// Opaque rather than a field per setting, and that is the whole design.
	// A `project:` key beside `files:` would be one runtime's vocabulary in
	// the shape every runtime shares -- exactly what decision 10 took
	// `units:` out of -- and Quadlet's equivalent question ("what do the
	// units get called") has a different answer with a different name.
	//
	// The manager bounds the *shape*: keys are identifiers, values are one
	// line and short, because these reach an adapter's argv. Whether a key
	// is one this runtime has heard of is the adapter's answer, given by
	// Validate, because nothing up here can know the list.
	Options map[string]string `yaml:"options,omitempty" json:"options,omitempty"`
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
		known := slices.Sorted(maps.Keys(d.Profiles))
		return nil, ValidationError(nil, "unknown deployment profile %q", profile).
			WithHint("profiles declared by this release: %s", strings.Join(known, ", "))
	}
	for _, f := range extra {
		if !slices.Contains(files, f) {
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

// isZero reports whether the legacy block was written at all.
//
// Files and profiles rather than every field: a manifest may set `project:`
// alone -- that was the only way to name one before `runtimes.<name>.options`
// existed -- and reading that as "the legacy block was written" would refuse a
// release that declares `runtimes:` and nothing else. What it costs is that
// `runtime: {project: x}` beside `runtimes:` is ignored rather than refused,
// which the both-declared message cannot help with because there is no file
// list to move.
// isAbsent reports whether the deprecated block was written at all.
//
// Every field, not only the ones that declare a runtime. There used to be an
// isZero beside this asking the narrower question -- files or profiles -- back
// when the block was read and the question was what it declared. Decision 23
// refuses what a vendor wrote instead, and a block carrying only `project`
// declares nothing and still decides the namespace every volume lives in.
func (r RuntimeSpec) isAbsent() bool {
	return r.Project == "" && len(r.Files) == 0 && len(r.Profiles) == 0
}

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
	names := slices.Sorted(maps.Keys(r))
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
	declared := m.DeclaredRuntimes()
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
	// The project is deliberately *not* defaulted here any more. It used to
	// become the product name for every manifest, including ones that never
	// wrote a `runtime:` block -- so a release on the new spelling was
	// silently grouped by a field on the deprecated one, and nothing said so.
	// What a runtime calls its grouping, and what it falls back to, is the
	// adapter's answer now; RuntimeConfig carries the product name so it has
	// something to fall back to.
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

// optionName is the grammar for a runtime option's key.
//
// Underscores rather than hyphens, matching parameter names: these are settings
// somebody writes in YAML beside parameters, and two spellings of "identifier"
// in one file is a rule nobody remembers. The manager checks the shape and
// stops -- which keys exist is the adapter's answer.
var optionName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// maxRuntimeOptionLen bounds a runtime option's value. Generous against any
// real setting -- a project name, a unit prefix -- and far short of a payload.
const maxRuntimeOptionLen = 200

// ValidateSingleLine refuses a value that could not safely be handed to
// something that renders or executes it.
//
// The rule every value leaving this manager follows, in one place: no newline,
// no carriage return, no control character, and a length bound. A newline in a
// value that reaches argv or a unit file is a second argument or a second
// directive, and the shape is identical whether the value came from a manifest,
// a hand-edited state file, or a restored backup.
//
// It says nothing about meaning. A well-formed value that is wrong is the
// adapter's refusal, not this one.
func ValidateSingleLine(raw string, max int) error {
	for _, r := range raw {
		if r == '\n' || r == '\r' || unicode.IsControl(r) {
			return ValidationError(nil, "must be one line, with no control characters")
		}
	}
	if len(raw) > max {
		return ValidationError(nil, "must be at most %d characters", max)
	}
	return nil
}

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
	declaredRuntimes := m.DeclaredRuntimes()

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
	// `runtime:` is no longer read (RFC 0023 decision 23), and a manifest
	// carrying it is refused rather than folded. This replaces two narrower
	// rules the removal subsumes -- one for a manifest carrying both
	// spellings, one for a `project` left behind by a half-finished
	// migration -- because with the block refused outright, both are the
	// same manifest seen from different sides.
	//
	// Refused rather than deleted from the struct. The manifest is
	// strict-decoded (`yaml.Strict()`, `yaml.DisallowUnknownField()` in
	// internal/release/load.go), so removing the field would answer a vendor
	// with "unknown field runtime" -- true, useless, and naming nothing they
	// can act on.
	//
	// Every field of the block, not only the ones isZero measures. A
	// project-only block declares no runtime and still decides the namespace
	// Compose puts volumes, networks and containers in; ignoring it renames
	// all of them and brings the deployment up against empty storage with
	// the old data unreferenced on the disk. Measured: `--project-name
	// alpha` resolves a volume named `alpha_data`, `beta` resolves
	// `beta_data`.
	if !m.Runtime.isAbsent() {
		v.add("runtime", "is no longer read: move the files under `runtimes."+
			LegacyRuntimeName+".files`, any `runtime.project` under `runtimes."+
			LegacyRuntimeName+".options.project`, and delete `runtime:`")
	}
	if len(declaredRuntimes) == 0 && m.Runtime.isAbsent() {
		// One spelling named, because there is only one. This used to
		// name the deprecated block too -- with nothing declared there
		// was no signal for which one the vendor was writing -- and
		// pointing anybody at `runtime.files` now sends them to a block
		// the refusal above exists to move them off.
		//
		// Silent when the legacy block is present, which is the case
		// that reads worst: a vendor who wrote `runtime:` declared a
		// runtime, and telling them they declared none is a second
		// error contradicting the first. The refusal above is the whole
		// answer, and it names the migration.
		v.add("runtimes", "must declare at least one runtime, each listing at least one file")
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
		// Always the new spelling: the old one is refused above, so no
		// manifest reaching here was written in it.
		base := "runtimes." + name
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
		for _, key := range sortedStringKeys(decl.Options) {
			field := base + ".options." + key
			if !optionName.MatchString(key) {
				v.add(base+".options",
					"%q is not a usable option name: lower-case letters, digits "+
						"and underscores, starting with a letter, at most 32 characters", key)
				continue
			}
			// The value's shape, never its meaning. It is handed to an
			// adapter that puts it in argv, so the bound is the same one
			// every value that leaves this manager gets: one line, no
			// control characters, and short enough not to be a payload.
			// Whether the adapter has heard of the key is its own answer,
			// given by Validate -- there is no list up here to check
			// against, and inventing one would be this layer deciding
			// what a runtime understands.
			if err := ValidateSingleLine(decl.Options[key], maxRuntimeOptionLen); err != nil {
				v.add(field, "%s", AsError(err).Message)
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

func isSupportedAPIVersion(v APIVersion) bool { return slices.Contains(SupportedAPIVersions, v) }

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
