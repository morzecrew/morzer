// Package release loads release bundles from disk into domain types.
//
// It exists as a separate package because domain is restricted to stdlib and
// semver: a YAML decoder does not belong there. Domain owns the types and the
// validation rules; this package owns turning bytes into those types and
// turning decoder errors into messages a bundle author can act on.
package release

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
)

// ManifestFileName is the manifest at the root of every bundle.
const ManifestFileName = "manifest.yaml"

// VersionFileName is an optional convenience file. When present it must agree
// with the manifest: a bundle whose two version statements disagree is one
// nobody can reason about.
const VersionFileName = "VERSION"

// ReleaseNotesFileName is what `release new` writes and declares as
// `metadata.release_notes`.
//
// A convention for the author, not a fallback for the reader: Notes reads the
// declaration and nothing else, so this name has no meaning to a bundle that
// does not point at it.
const ReleaseNotesFileName = "RELEASE.md"

// Load reads and validates a release from an unpacked bundle directory.
//
// The digest is computed over the whole tree, so identity is content-based
// rather than trusting what the manifest claims about itself.
func Load(dir string) (domain.Release, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return domain.Release{}, domain.ValidationError(err, "cannot resolve %s", dir)
	}

	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return domain.Release{}, domain.ValidationError(domain.ErrReleaseNotFound,
				"no release bundle at %s", abs).
				WithHint("check the path, or run `morzer release list` to see installed releases")
		}
		return domain.Release{}, domain.ValidationError(err, "cannot read %s", abs)
	}
	if !info.IsDir() {
		return domain.Release{}, domain.ValidationError(nil,
			"%s is not a directory", abs).
			WithHint("point at an unpacked bundle directory containing %s", ManifestFileName)
	}

	manifest, err := LoadManifest(filepath.Join(abs, ManifestFileName))
	if err != nil {
		return domain.Release{}, err
	}

	if err := checkVersionFile(abs, manifest.Metadata.Version); err != nil {
		return domain.Release{}, err
	}

	digest, err := atomicfs.DigestTree(abs)
	if err != nil {
		return domain.Release{}, err
	}

	rel := domain.Release{Manifest: manifest, Root: abs, Digest: digest}

	if err := checkReferencedFiles(rel); err != nil {
		return domain.Release{}, err
	}

	// After the declared paths, because a bundle missing its templates is a
	// more basic problem than one whose image layout disagrees with its
	// manifest, and reporting the smaller problem first sends an author down
	// the wrong road.
	if err := checkBundledImages(rel); err != nil {
		return domain.Release{}, err
	}

	return rel, nil
}

// LoadManifest reads, decodes, defaults and validates one manifest file.
func LoadManifest(path string) (domain.Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return domain.Manifest{}, domain.ValidationError(domain.ErrReleaseNotFound,
				"no manifest at %s", path).
				WithHint("every release bundle must contain a %s at its root", ManifestFileName)
		}
		return domain.Manifest{}, domain.ValidationError(err, "cannot read %s", path)
	}
	return ParseManifest(data, path)
}

// managerVersion is the running manager's own version, recorded once at
// startup so ParseManifest can answer the question in checkManagerVersion.
//
// Package state rather than a parameter because Load and ParseManifest have
// dozens of call sites, almost none of which care -- and because this is a
// build-time constant of the process, not something that varies per call. Zero
// means unknown, which skips the check: the strict decode still runs, so an
// unset version costs the better error message and nothing else.
var managerVersion domain.Version

// SetManagerVersion records the running manager's version. Called once from
// the CLI at startup; tests set it directly.
func SetManagerVersion(v domain.Version) { managerVersion = v }

// ParseManifest decodes manifest bytes.
//
// Decoding is strict: an unknown field is an error, not a silently ignored
// key. A typo in a manifest field would otherwise mean the manager quietly
// uses a default while the author believes they configured something.
//
// That strictness has a cost the manifest's own compatibility block was meant
// to cover and cannot: `min_manager_version` is read by CheckUpgrade, which
// runs on an *already decoded* manifest, so a release using a field this
// manager predates fails here first and reports a typo. checkManagerVersion is
// the lenient pass that lets the release say what it actually needs.
func ParseManifest(data []byte, source string) (domain.Manifest, error) {
	var m domain.Manifest

	// Before the strict decode, or the unknown field wins the race and the
	// operator is told about a typo in a file they did not write.
	if err := checkManagerVersion(data, source); err != nil {
		return domain.Manifest{}, err
	}

	if err := yaml.UnmarshalWithOptions(data, &m,
		yaml.Strict(),
		yaml.DisallowUnknownField(),
		yaml.UseJSONUnmarshaler(),
	); err != nil {
		return domain.Manifest{}, decodeError(err, source, "manifest")
	}

	m.ApplyDefaults()

	if err := m.Validate(); err != nil {
		// Prefix the source so an author with several bundles open
		// knows which file is being complained about.
		e := domain.AsError(err)
		return domain.Manifest{}, domain.ValidationError(err, "%s: %s", source, e.Message)
	}

	// Deprecation is deliberately not checked here: it is a warning, not a
	// rejection -- the contract promises to read every published version
	// until it is explicitly withdrawn. The consumers that face an operator
	// surface it via Manifest.DeprecationWarning: update when it resolves a
	// bundle, and `release verify` for the vendor's own CI.

	return m, nil
}

// LoadSecretSchema reads the secret schema a manifest declares.
//
// A release without a secret schema is valid -- not every product has
// secrets -- and yields an empty schema rather than an error.
func LoadSecretSchema(rel domain.Release) (domain.SecretSchema, error) {
	if rel.Manifest.Secrets.Schema == "" {
		return domain.SecretSchema{}, nil
	}

	path, err := rel.Path(rel.Manifest.Secrets.Schema)
	if err != nil {
		return domain.SecretSchema{}, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return domain.SecretSchema{}, domain.ValidationError(domain.ErrNotFound,
				"the manifest declares secrets.schema %q but the file is missing",
				rel.Manifest.Secrets.Schema).
				WithHint("a declared-but-missing schema is a broken bundle; " +
					"remove the declaration or ship the file")
		}
		return domain.SecretSchema{}, domain.ValidationError(err, "cannot read %s", path)
	}

	var schema domain.SecretSchema
	if err := yaml.UnmarshalWithOptions(data, &schema,
		yaml.Strict(),
		yaml.DisallowUnknownField(),
		yaml.UseJSONUnmarshaler(),
	); err != nil {
		return domain.SecretSchema{}, decodeError(err, path, "secret schema")
	}

	if err := schema.Validate(); err != nil {
		e := domain.AsError(err)
		return domain.SecretSchema{}, domain.ValidationError(err, "%s: %s", path, e.Message)
	}
	return schema, nil
}

// DeclaredBundleSize reads `bundle.uncompressed_size` out of manifest bytes.
//
// Lenient about the *document*, strict about the *field*, and the split matters.
//
// This runs on the first entry of an archive that has not been verified, and it
// must succeed against a manifest written for a newer manager -- so YAML it
// cannot parse at all is left to the strict decode, which reports it properly,
// with a position, once the archive is on disk.
//
// But a `bundle.uncompressed_size` that is present and unreadable is refused
// rather than treated as absent. Absent means the default ceiling; unreadable
// would mean extracting under that ceiling on the strength of a declaration
// nobody could parse, which is the permissive reading of the one field that
// gates untrusted bytes.
func DeclaredBundleSize(manifest []byte) (int64, error) {
	var p struct {
		Bundle struct {
			UncompressedSize string `yaml:"uncompressed_size"`
		} `yaml:"bundle"`
	}
	if err := yaml.Unmarshal(manifest, &p); err != nil {
		return 0, nil
	}
	raw := strings.TrimSpace(p.Bundle.UncompressedSize)
	if raw == "" {
		return 0, nil
	}

	var size domain.ByteSize
	if err := size.UnmarshalText([]byte(raw)); err != nil {
		// A value that is there and unreadable is not the same as one
		// that is absent. Reading it as absent would extract under the
		// default ceiling on the strength of a declaration nobody could
		// parse, which is the permissive reading of a field that gates
		// untrusted bytes.
		return 0, domain.ValidationError(err,
			"the bundle declares an uncompressed size of %q, which is not a size", raw).
			WithHint("bundle.uncompressed_size looks like 12GiB or 512MiB")
	}
	if size < 0 {
		return 0, domain.ValidationError(domain.ErrValidation,
			"the bundle declares an uncompressed size of %s", size).
			WithHint("bundle.uncompressed_size is a size such as 12GiB")
	}
	return size.Bytes(), nil
}

// manifestPreamble is the little of a manifest that must be readable before
// the rest is judged.
//
// Every field is a string, deliberately: this decode has to succeed against a
// manifest written for a *newer* manager, so it must not depend on any
// UnmarshalYAML the current build happens to have. Parsing is done afterwards,
// by hand, on values this build understands.
type manifestPreamble struct {
	APIVersion    string `yaml:"api_version"`
	Compatibility struct {
		MinManagerVersion string `yaml:"min_manager_version"`
	} `yaml:"compatibility"`
}

// checkManagerVersion refuses a manifest that declares it needs a newer
// manager than this one, before strict decoding gets a chance to blame a typo.
//
// Everything it cannot answer, it declines to answer: unparseable YAML, an
// absent or malformed min_manager_version, an unknown manager version. In each
// case it returns nil and the strict decode reports whatever is really wrong,
// with the position information it is good at. The check only ever *replaces*
// a confusing error with a clear one -- it never rejects a manifest the strict
// pass would have accepted, which is what makes running it first safe.
func checkManagerVersion(data []byte, source string) error {
	if managerVersion.IsZero() {
		return nil
	}

	var p manifestPreamble
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil
	}
	if strings.TrimSpace(p.Compatibility.MinManagerVersion) == "" {
		return nil
	}
	required, err := domain.ParseVersion(p.Compatibility.MinManagerVersion)
	if err != nil {
		return nil
	}
	if !managerVersion.LessThan(required) {
		return nil
	}

	return domain.IncompatibleError(nil,
		"%s requires morzer %s or newer, and this is %s",
		source, required, managerVersion).
		WithHint("upgrade the manager, or install a release built for this version. " +
			"A newer release may use manifest fields this build does not know")
}

// decodeError turns a YAML decoder error into a domain error carrying line,
// column, and the offending source line.
//
// goccy/go-yaml is used specifically for this: its errors point at a position
// in the file, which is the difference between "fix line 34" and "something in
// this 200-line manifest is wrong".
func decodeError(err error, source, what string) error {
	formatted := yaml.FormatError(err, false, true)

	hint := "check the field names and indentation against the published schema"
	if strings.Contains(err.Error(), "unknown field") {
		hint = "unknown fields are rejected deliberately, so a typo cannot silently " +
			"fall back to a default. Vendor-specific keys belong under extensions.<namespace>."
	}

	return domain.ValidationError(err, "%s is not a valid %s:\n%s", source, what, indent(formatted, "  ")).
		WithHint("%s", hint)
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// checkVersionFile enforces that VERSION, when present, agrees with the
// manifest.
func checkVersionFile(dir string, manifestVersion domain.Version) error {
	data, err := os.ReadFile(filepath.Join(dir, VersionFileName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return domain.ValidationError(err, "cannot read %s", VersionFileName)
	}

	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return nil
	}
	fileVersion, err := domain.ParseVersion(raw)
	if err != nil {
		return domain.ValidationError(err, "%s contains %q, which is not a version", VersionFileName, raw)
	}
	if !fileVersion.Equal(manifestVersion) {
		return domain.ValidationError(nil,
			"%s says %s but the manifest says %s", VersionFileName, fileVersion, manifestVersion).
			WithHint("the two must agree; the manifest is authoritative, so fix %s", VersionFileName)
	}
	return nil
}

// checkReferencedFiles verifies that every path the manifest names actually
// exists.
//
// The spec's rule: a declared-but-missing hook is a release validation error,
// an undeclared missing hook is not. Catching this at load time means a broken
// bundle fails before the lock is taken, rather than three steps into an
// update.
func checkReferencedFiles(rel domain.Release) error {
	var missing []string

	check := func(field, relPath string) {
		if relPath == "" {
			return
		}
		abs, err := rel.Path(relPath)
		if err != nil {
			missing = append(missing, fmt.Sprintf("%s: %s", field, domain.AsError(err).Message))
			return
		}
		if _, err := os.Stat(abs); err != nil {
			missing = append(missing, fmt.Sprintf("%s: %s does not exist", field, relPath))
		}
	}

	// Declared like every other path a bundle ships, and checked like
	// every other one: a bundle promising release notes and shipping none
	// fails on the vendor's machine rather than showing an operator
	// nothing at the moment they were told to read something.
	check("metadata.release_notes", rel.Manifest.Metadata.ReleaseNotes)

	// Every declared runtime, not just the deprecated block. Walking only
	// `runtime:` meant a release using `runtimes:` had none of its files
	// checked at all -- so a missing one loaded clean and surfaced three
	// steps into a deployment, which is the failure this function exists to
	// move earlier.
	//
	// The field names follow the spelling the vendor used, for the same
	// reason Validate's do: naming `runtimes.compose.files` to somebody whose
	// manifest says `runtime.files` sends them looking for a block that is
	// not there.
	declared, fromLegacy := rel.Manifest.DeclaredRuntimes()
	for _, name := range declared.Names() {
		base := "runtimes." + name
		if fromLegacy {
			base = "runtime"
		}
		decl := declared[name]
		for i, f := range decl.Files {
			check(fmt.Sprintf("%s.files[%d]", base, i), f)
		}
		for profile, files := range decl.Profiles {
			for i, f := range files {
				check(fmt.Sprintf("%s.profiles.%s[%d]", base, profile, i), f)
			}
		}
	}
	for i, c := range rel.Manifest.Configuration {
		check(fmt.Sprintf("configuration[%d].template", i), c.Template)
	}
	check("secrets.schema", rel.Manifest.Secrets.Schema)

	for name, op := range rel.Manifest.Operations {
		if op.Kind == domain.OperationKindHook && len(op.Command) > 0 {
			check("operations."+name+".command", op.Command[0])
			checkExecutable(rel, op.Command[0], "operations."+name, &missing)
		}
	}
	for _, hc := range rel.Manifest.Health.Checks {
		if hc.Type == domain.HealthCommand && len(hc.Command) > 0 {
			check("health.checks."+hc.Name, hc.Command[0])
			checkExecutable(rel, hc.Command[0], "health.checks."+hc.Name, &missing)
		}
	}

	if len(missing) > 0 {
		return domain.ValidationError(domain.ErrNotFound,
			"the release manifest references files that are missing or unusable:\n  - %s",
			strings.Join(missing, "\n  - ")).
			WithHint("a declared hook or template must ship with the bundle")
	}
	return nil
}

// checkExecutable verifies a hook carries the executable bit. Discovering this
// at execution time would mean failing halfway through an operation for a
// reason that was knowable before it started.
func checkExecutable(rel domain.Release, relPath, field string, missing *[]string) {
	abs, err := rel.Path(relPath)
	if err != nil {
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		return // already reported as missing
	}
	if info.Mode().Perm()&0o111 == 0 {
		*missing = append(*missing,
			fmt.Sprintf("%s: %s is not executable (mode %04o)", field, relPath, info.Mode().Perm()))
	}
}
