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

// ReleaseNotesFileName is rendered by `release show` when present.
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

// ParseManifest decodes manifest bytes.
//
// Decoding is strict: an unknown field is an error, not a silently ignored
// key. A typo in a manifest field would otherwise mean the manager quietly
// uses a default while the author believes they configured something.
func ParseManifest(data []byte, source string) (domain.Manifest, error) {
	var m domain.Manifest

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

// LoadSecretSchema reads templates/secrets.yaml from a release.
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

	for i, f := range rel.Manifest.Runtime.Files {
		check(fmt.Sprintf("runtime.files[%d]", i), f)
	}
	for profile, files := range rel.Manifest.Runtime.Profiles {
		for i, f := range files {
			check(fmt.Sprintf("runtime.profiles.%s[%d]", profile, i), f)
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
