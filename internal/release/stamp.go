package release

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
)

// versionLine matches `  version: 1.2.0`, capturing the indentation and key so
// the rewrite preserves them, and any trailing comment so the rewrite preserves
// that too.
var versionLine = regexp.MustCompile(`^(\s+version:\s*)(\S+)(\s*(?:#.*)?)$`)

// Stamp writes a version into a bundle's manifest and VERSION file.
//
// Both, or the bundle stops loading: checkVersionFile refuses a disagreement
// between them. Stamping one and not the other is the single most likely defect
// in this file, which is why it has a test of its own.
//
// The manifest is edited line by line rather than decoded and re-encoded. A
// round trip through the YAML marshaller would discard every comment in the
// file and reorder the keys to struct order -- turning a vendor's annotated,
// hand-ordered manifest into machine output, on a command whose entire job was
// to change one field. The edit is verified afterwards by re-parsing, which is
// what makes a surgical rewrite safe rather than merely quick.
func Stamp(dir string, version domain.Version) error {
	if version.IsZero() {
		return domain.Internal(nil, "cannot stamp an unset version")
	}

	manifestPath := filepath.Join(dir, ManifestFileName)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return domain.ValidationError(err, "cannot read %s", manifestPath)
	}

	stamped, err := stampManifest(string(data), version)
	if err != nil {
		return err
	}
	if err := atomicfs.WriteFile(manifestPath, []byte(stamped), 0o644); err != nil {
		return err
	}

	// Re-read through the real loader. The rewrite is textual, so this is
	// the only thing standing between a clever regex and a manifest that no
	// longer parses -- and it also proves the value landed where it was
	// meant to rather than in some other `version:` key.
	m, err := LoadManifest(manifestPath)
	if err != nil {
		return domain.ValidationError(err,
			"stamping %s produced a manifest that no longer loads", version)
	}
	if !m.Metadata.Version.Equal(version) {
		return domain.Internal(nil,
			"stamping wrote %s but the manifest reads %s", version, m.Metadata.Version)
	}

	return atomicfs.WriteFile(
		filepath.Join(dir, VersionFileName), []byte(version.String()+"\n"), 0o644)
}

// stampManifest replaces metadata.version in a manifest's text.
//
// It looks only inside the top-level `metadata:` block, so a `version:` under
// `providers.runtime` or inside a vendor's `extensions` block is not the one
// rewritten. Anything it cannot locate unambiguously is refused rather than
// guessed at: a build that stamped the wrong key would produce a bundle that
// loads, verifies, and is not the version anybody asked for.
func stampManifest(text string, version domain.Version) (string, error) {
	lines := strings.Split(text, "\n")

	inMetadata := false
	replaced := -1
	for i, line := range lines {
		trimmed := strings.TrimRight(line, "\r")

		// A line starting in column zero ends whatever block was open.
		if trimmed != "" && !strings.HasPrefix(trimmed, " ") && !strings.HasPrefix(trimmed, "\t") {
			inMetadata = strings.HasPrefix(trimmed, "metadata:")
			continue
		}
		if !inMetadata {
			continue
		}
		m := versionLine.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		if replaced >= 0 {
			return "", domain.ValidationError(nil,
				"the manifest's metadata block declares version twice, on lines %d and %d",
				replaced+1, i+1)
		}
		lines[i] = m[1] + version.String() + m[3]
		replaced = i
	}

	if replaced < 0 {
		return "", domain.ValidationError(domain.ErrNotFound,
			"cannot find metadata.version in the manifest").
			WithHint("stamping rewrites the `version:` line inside the top-level " +
				"`metadata:` block; a flow-style mapping cannot be rewritten in place")
	}
	return strings.Join(lines, "\n"), nil
}
