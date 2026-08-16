package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The runtime dimension, RFC 0023 P2.
//
// What is being pinned here is mostly *refusals*: the manifest gained a second
// way to say which files a release ships, and the expensive failures are the
// ones where the manager picks one of two answers and proceeds. Every test
// below that asserts an error asserts which error, because "it failed" is
// satisfied by a validator that fails for the wrong reason.

func TestALegacyRuntimeBlockDeclaresTheLegacyRuntime(t *testing.T) {
	m := validManifest()
	m.Runtimes = nil
	m.Runtime.Files = []string{"compose.yaml"}

	declared, fromLegacy := m.DeclaredRuntimes()

	require.True(t, fromLegacy, "a release using the old block must be reported as doing so")
	assert.Equal(t, []string{LegacyRuntimeName}, declared.Names())
	assert.Equal(t, []string{"compose.yaml"}, declared[LegacyRuntimeName].Files)
}

func TestARuntimesMapDeclaresItsOwnKeys(t *testing.T) {
	m := validManifest()
	m.Runtime = RuntimeSpec{}
	m.Runtimes = Runtimes{
		"quadlet": {Files: []string{"app.container"}},
		"compose": {Files: []string{"compose.yaml"}},
	}

	declared, fromLegacy := m.DeclaredRuntimes()

	assert.False(t, fromLegacy)
	// Sorted, so every message that lists runtimes lists them alike.
	assert.Equal(t, []string{"compose", "quadlet"}, declared.Names())
}

// Both blocks is a refusal rather than a merge. Merging would pick a winner
// the vendor never nominated, and the losing block is a topology somebody
// wrote on purpose.
func TestDeclaringBothBlocksIsRefused(t *testing.T) {
	m := validManifest()
	m.Runtime.Files = []string{"compose.yaml"}
	m.Runtimes = Runtimes{"compose": {Files: []string{"other.yaml"}}}

	err := m.Validate()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be used together with the deprecated `runtime:` block",
		"the refusal must say which two things collided")
}

// The regression that made DeclaredRuntimes derived rather than stored.
//
// ApplyDefaults used to normalise the legacy block into a field. That made it
// a snapshot: Validate called on its own saw an empty map and checked no
// paths, so an escaping path in the legacy block passed. This asserts the
// check holds without ApplyDefaults having run, which is the only version of
// it that is a check.
func TestLegacyRuntimePathsAreCheckedWithoutApplyingDefaults(t *testing.T) {
	for _, path := range []string{"/etc/passwd", "../outside/compose.yaml", "a/../../escape.yaml"} {
		t.Run(path, func(t *testing.T) {
			m := validManifest()
			m.Runtimes = nil
			m.Runtime.Files = []string{path}

			err := m.Validate()

			require.Error(t, err, "%q must be rejected", path)
			assert.Contains(t, err.Error(), "runtime.files",
				"the refusal must name the field the vendor actually wrote")
		})
	}
}

func TestRuntimesMapPathsAreCheckedAndNamedByRuntime(t *testing.T) {
	m := validManifest()
	m.Runtime = RuntimeSpec{}
	m.Runtimes = Runtimes{"quadlet": {Files: []string{"/etc/passwd"}}}

	err := m.Validate()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "runtimes.quadlet.files",
		"a vendor must be able to search their own file for the field named")
}

// A manifest declaring nothing has no signal for which spelling its author is
// using, so the refusal names both. The per-runtime messages that would name
// the right one never run when there is nothing to iterate.
func TestDeclaringNoRuntimeNamesBothSpellings(t *testing.T) {
	m := validManifest()
	m.Runtime = RuntimeSpec{}
	m.Runtimes = nil

	err := m.Validate()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "runtimes")
	assert.Contains(t, err.Error(), "runtime.files")
}

// `providers.runtime.name` is no longer a hardcoded "compose". It is derived
// from what the release declares, and only when there is one thing to derive
// it from -- decision 8, and the death of §2.1's second expensive leak.
func TestTheProviderNameIsDerivedFromASingleDeclaredRuntime(t *testing.T) {
	m := validManifest()
	m.Runtime = RuntimeSpec{}
	m.Providers.Runtime.Name = ""
	m.Runtimes = Runtimes{"quadlet": {Files: []string{"app.container"}}}

	m.ApplyDefaults()

	assert.Equal(t, "quadlet", m.Providers.Runtime.Name,
		"a single-runtime release still fills the field, and not with the incumbent's name")
}

func TestTheProviderNameStaysEmptyForATwoRuntimeRelease(t *testing.T) {
	m := validManifest()
	m.Runtime = RuntimeSpec{}
	m.Providers.Runtime.Name = ""
	m.Runtimes = Runtimes{
		"compose": {Files: []string{"compose.yaml"}},
		"quadlet": {Files: []string{"app.container"}},
	}

	m.ApplyDefaults()

	assert.Empty(t, m.Providers.Runtime.Name,
		"there is no one value the field could take that would not be a lie about the other runtime")
}

// Decision 5 at the point a caller would otherwise receive an empty file list
// and deploy nothing while reporting success.
func TestResolvingAnUndeclaredRuntimeRefusesAndSaysWhatIsDeclared(t *testing.T) {
	m := validManifest()
	m.Runtime = RuntimeSpec{}
	m.Runtimes = Runtimes{"compose": {Files: []string{"compose.yaml"}}}
	rel := Release{Manifest: m, Root: "/opt/demo/releases/1"}

	_, err := rel.RuntimeFilePaths("quadlet", "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support the quadlet runtime")
	// The list lands in the hint, which is where the operator reads it --
	// asserting it against Error() would pass on a hint that was never set.
	var domErr *Error
	require.ErrorAs(t, err, &domErr)
	assert.Contains(t, domErr.Hint, "compose", "the refusal must name what the release does declare")
}

func TestResolvingADeclaredRuntimeReturnsItsFiles(t *testing.T) {
	m := validManifest()
	m.Runtime = RuntimeSpec{}
	m.Runtimes = Runtimes{"quadlet": {Files: []string{"app.container"}}}
	rel := Release{Manifest: m, Root: "/opt/demo/releases/1"}

	paths, err := rel.RuntimeFilePaths("quadlet", "")

	require.NoError(t, err)
	require.Len(t, paths, 1)
	assert.Contains(t, paths[0], "app.container")
}

// An unknown profile refuses rather than falling back to base, under the new
// declaration exactly as under the old one.
func TestAnUnknownProfileRefusesUnderARuntimeDeclaration(t *testing.T) {
	decl := RuntimeDecl{
		Files:    []string{"compose.yaml"},
		Profiles: map[string][]string{"prod": {"compose.prod.yaml"}},
	}

	_, err := decl.FilesFor("staging")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown deployment profile")
	var domErr *Error
	require.ErrorAs(t, err, &domErr)
	assert.Contains(t, domErr.Hint, "prod", "the refusal must list what is declared")
}

func TestAProfileFileAlreadyListedIsNotPassedTwice(t *testing.T) {
	decl := RuntimeDecl{
		Files:    []string{"compose.yaml", "shared.yaml"},
		Profiles: map[string][]string{"prod": {"shared.yaml", "compose.prod.yaml"}},
	}

	files, err := decl.FilesFor("prod")

	require.NoError(t, err)
	assert.Equal(t, []string{"compose.yaml", "shared.yaml", "compose.prod.yaml"}, files,
		"a file in both lists would otherwise be merged with itself")
}

// An installation written before schema 9 has no runtime recorded, and it ran
// the only runtime there was. Read in one place so no call site has to
// remember, because the one that forgot would resolve "" against a release.
func TestAnInstallationWithNoRecordedRuntimeReadsAsTheLegacyOne(t *testing.T) {
	assert.Equal(t, LegacyRuntimeName, Installation{}.RuntimeName())
	assert.Equal(t, "quadlet", Installation{Runtime: "quadlet"}.RuntimeName())
}

// ApplyDefaults fills RuntimeSpec.Project from the product name unconditionally,
// including for a release that never wrote a `runtime:` block. That makes the
// legacy struct non-zero on a manifest whose author only used `runtimes:` --
// which would read as "declared both" and refuse the release, if isZero looked
// at Project. It looks at Files and Profiles, and this is what says so.
func TestARuntimesOnlyReleaseSurvivesDefaultingAndValidation(t *testing.T) {
	m := validManifest()
	m.Runtime = RuntimeSpec{}
	m.Runtimes = Runtimes{"compose": {Files: []string{"compose.yaml"}}}

	m.ApplyDefaults()
	require.Empty(t, m.Runtime.Project,
		"a release on the new spelling must not inherit a grouping name from the deprecated block")

	require.NoError(t, m.Validate(), "a defaulted project must not read as a declared legacy block")

	declared, fromLegacy := m.DeclaredRuntimes()
	assert.False(t, fromLegacy)
	assert.Equal(t, []string{"compose"}, declared.Names())
}

// A two-runtime manifest must survive its own defaulting.
//
// ApplyDefaults leaves `providers.runtime.name` empty for a release declaring
// two runtimes, and Validate required it unconditionally -- so the shape
// decision 8 settled could not validate, and the `init` refusal that is
// supposed to meet it was unreachable. The earlier test proved only that the
// field stays empty, which is true of a manifest nothing can load.
func TestATwoRuntimeManifestValidatesAfterItsOwnDefaulting(t *testing.T) {
	m := validManifest()
	m.Runtime = RuntimeSpec{}
	m.Providers.Runtime.Name = ""
	m.Runtimes = Runtimes{
		"compose": {Files: []string{"compose.yaml"}},
		"quadlet": {Files: []string{"app.container"}},
	}

	m.ApplyDefaults()

	require.NoError(t, m.Validate(),
		"a release declaring two runtimes must be loadable, or decision 8's shape does not exist")
	assert.Empty(t, m.Providers.Runtime.Name)
}

// A single-runtime release still has to name its provider, because there the
// field means what it always meant and a missing one is a vendor's omission.
func TestASingleRuntimeManifestStillRequiresTheProviderName(t *testing.T) {
	m := validManifest()
	m.Runtime = RuntimeSpec{}
	m.Providers.Runtime.Name = ""
	m.Runtimes = Runtimes{"compose": {Files: []string{"compose.yaml"}}}

	// Deliberately not ApplyDefaults: that would derive the name. This is
	// the manifest as a vendor could hand it to `release verify`.
	err := m.Validate()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "providers.runtime.name")
}

// An empty runtime name resolves to the legacy runtime three layers later, so
// a release declaring something else installs as Compose with every message
// agreeing. Refused where the vendor can see the key.
func TestARuntimeWithAnEmptyNameIsRefused(t *testing.T) {
	for _, name := range []string{"", "   "} {
		m := validManifest()
		m.Runtime = RuntimeSpec{}
		m.Providers.Runtime.Name = "compose"
		m.Runtimes = Runtimes{name: {Files: []string{"compose.yaml"}}}

		err := m.Validate()

		require.Error(t, err, "a runtime named %q must be refused", name)
		assert.Contains(t, err.Error(), "empty name")
	}
}

// The grammar of a runtime name, and the line it deliberately does not cross.
//
// Two checks exist because two different things can be wrong and only one is
// knowable here. A name that is malformed is refused by the domain; a name that
// is well-formed and simply is not this manager's runtime is refused by the
// adapter, which is the only thing that knows. A list of known names at this
// layer would be the runtime catalogue above `internal/adapters` that decision
// 7 exists to prevent.
func TestARuntimeNameMustBeWellFormed(t *testing.T) {
	valid := []string{"compose", "quadlet", "podman-compose", "k3s", "a", "x9"}
	for _, name := range valid {
		assert.True(t, ValidRuntimeName(name), "%q is a usable runtime name", name)
	}

	invalid := map[string]string{
		"empty":           "",
		"whitespace":      "   ",
		"padded":          " compose",
		"trailing space":  "compose ",
		"capitalised":     "Compose",
		"leading digit":   "9lives",
		"leading hyphen":  "-compose",
		"underscore":      "podman_compose",
		"path traversal":  "../etc",
		"terminal escape": "compose\x1b[31m",
		"newline":         "compose\nX",
		"absurdly long":   "a123456789012345678901234567890123",
	}
	for why, name := range invalid {
		assert.False(t, ValidRuntimeName(name), "%s: %q must be refused", why, name)
	}
}

// The state file is read from disk and its runtime is printed back in error
// messages, so a name carrying a terminal escape is a diagnostic that moves the
// cursor. Same shape as the bounds on fleet rows and attested text.
func TestAnInstallationRefusesAMalformedRecordedRuntime(t *testing.T) {
	base := func() Installation {
		return Installation{
			SchemaVersion: InstallationSchemaVersion,
			ID:            "11111111-1111-1111-1111-111111111111",
			Product:       "demo",
			CreatedAt:     NewTime(time.Now()),
		}
	}

	i := base()
	i.Runtime = "compose\x1b[31m"
	err := i.Validate()
	require.Error(t, err, "a runtime name carrying a terminal escape must be refused")
	assert.Contains(t, err.Error(), "runtime")

	// Empty stays valid: it is what every installation created before schema
	// 9 records, and RuntimeName reads it as the legacy runtime.
	ok := base()
	ok.Runtime = ""
	assert.NoError(t, ok.Validate())

	// And a well-formed name this manager may not be able to drive is *not*
	// the domain's to refuse. That is the adapter's, and refusing it here
	// would need a list of known runtimes above internal/adapters.
	unknown := base()
	unknown.Runtime = "quadlt"
	assert.NoError(t, unknown.Validate(),
		"a typo that is well-formed is caught by the adapter mismatch, not here")
}
