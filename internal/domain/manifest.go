package domain

import (
	"encoding/json"
	"errors"
	"fmt"
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

	Metadata      Metadata                 `yaml:"metadata" json:"metadata"`
	Providers     Providers                `yaml:"providers" json:"providers"`
	Runtime       RuntimeSpec              `yaml:"runtime" json:"runtime"`
	Requirements  Requirements             `yaml:"requirements" json:"requirements"`
	Parameters    map[string]ParameterSpec `yaml:"parameters" json:"parameters,omitempty"`
	Images        map[string]string        `yaml:"images" json:"images"`
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

	Health        HealthSpec                `yaml:"health" json:"health"`
	Compatibility Compatibility             `yaml:"compatibility" json:"compatibility"`
	Retention     Retention                 `yaml:"retention" json:"retention"`
	Extensions    map[string]map[string]any `yaml:"extensions" json:"extensions,omitempty"`
}

type Metadata struct {
	Name        string  `yaml:"name" json:"name"`
	Version     Version `yaml:"version" json:"version"`
	Description string  `yaml:"description" json:"description,omitempty"`
	Vendor      string  `yaml:"vendor" json:"vendor,omitempty"`
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
// and becomes port 8. Left alone for the same reason as ByteSize -- a
// zero-padded port is not a real spelling, and refusing integers would undo
// exactly what this type exists for -- and pinned by test so it is recorded
// rather than discovered.
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
	if m.Providers.Runtime.Name == "" {
		m.Providers.Runtime.Name = "compose"
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
	}

	// providers
	if m.Providers.Runtime.Name == "" {
		v.add("providers.runtime.name", "is required")
	}

	// runtime
	if len(m.Runtime.Files) == 0 {
		v.add("runtime.files", "must list at least one compose file")
	}
	for i, f := range m.Runtime.Files {
		v.checkRelPath(fmt.Sprintf("runtime.files[%d]", i), f)
	}
	for profile, files := range m.Runtime.Profiles {
		if len(files) == 0 {
			v.add("runtime.profiles."+profile, "must list at least one compose file")
		}
		for i, f := range files {
			v.checkRelPath(fmt.Sprintf("runtime.profiles.%s[%d]", profile, i), f)
		}
	}

	// parameters
	ValidateParameters(m.Parameters, &v)

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

	// images -- the pinning rule
	if len(m.Images) == 0 {
		v.add("images", "must declare at least one image")
	}
	for name, ref := range m.Images {
		if !digestRef.MatchString(ref) {
			v.add("images."+name, "must be pinned by digest (name@sha256:...), got %q", ref)
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
	}

	// compatibility
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

// ImageRefs returns image references in a stable order, so pull plans and
// dry-run output do not shuffle between runs.
func (m *Manifest) ImageRefs() []string {
	names := make([]string, 0, len(m.Images))
	for name := range m.Images {
		names = append(names, name)
	}
	sort.Strings(names)
	refs := make([]string, 0, len(names))
	for _, n := range names {
		refs = append(refs, m.Images[n])
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
