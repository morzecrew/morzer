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

// The legacy block declares nothing, and that is the removal (decision 23).
//
// Asserted on DeclaredRuntimes rather than only on Validate because they are
// different claims: Validate refusing is what a vendor meets, and this is what
// stops the block having an effect on any path that never validated.
func TestALegacyRuntimeBlockDeclaresNothing(t *testing.T) {
	m := validManifest()
	m.Runtimes = nil
	m.Runtime.Files = []string{"compose.yaml"}
	m.Runtime.Project = "legacy"

	assert.Empty(t, m.DeclaredRuntimes(),
		"`runtime:` stopped being read in 0.3.0; folding it in is what "+
			"decision 23 removed")
}

// And the manifest carrying it is refused, naming what to write instead.
func TestALegacyRuntimeBlockIsRefusedAndNamesTheMigration(t *testing.T) {
	for name, mutate := range map[string]func(*Manifest){
		"files":        func(m *Manifest) { m.Runtime.Files = []string{"compose.yaml"} },
		"project only": func(m *Manifest) { m.Runtime.Project = "legacy" },
		"profiles":     func(m *Manifest) { m.Runtime.Profiles = map[string][]string{"ha": {"x.yaml"}} },
	} {
		t.Run(name, func(t *testing.T) {
			m := validManifest()
			m.Runtimes = Runtimes{"compose": {Files: []string{"compose.yaml"}}}
			mutate(&m)

			err := m.Validate()

			require.Error(t, err, "a manifest carrying `runtime:` must be refused")
			assert.Contains(t, err.Error(), "is no longer read",
				"the refusal must say the block is gone, not merely that it is wrong")
			assert.Contains(t, err.Error(), "runtimes.compose",
				"and must name the spelling to migrate to")
		})
	}
}

func TestARuntimesMapDeclaresItsOwnKeys(t *testing.T) {
	m := validManifest()
	m.Runtime = RuntimeSpec{}
	m.Runtimes = Runtimes{
		"quadlet": {Files: []string{"app.container"}},
		"compose": {Files: []string{"compose.yaml"}},
	}

	declared := m.DeclaredRuntimes()

	// Sorted, so every message that lists runtimes lists them alike.
	assert.Equal(t, []string{"compose", "quadlet"}, declared.Names())
}

// Both blocks was its own refusal until decision 23; it is now one case of the
// legacy block being refused at all, and the message names the migration
// rather than the collision. Kept as a test because a vendor mid-migration is
// the likeliest writer of this manifest.
func TestDeclaringBothBlocksIsRefused(t *testing.T) {
	m := validManifest()
	m.Runtime.Files = []string{"compose.yaml"}
	m.Runtimes = Runtimes{"compose": {Files: []string{"other.yaml"}}}

	err := m.Validate()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "is no longer read",
		"a half-migrated manifest is told the old block is gone, not that it collided")
}

// The path escape that motivated making DeclaredRuntimes derived is now
// unreachable, and this pins that it is unreachable for the right reason.
//
// The original defect: ApplyDefaults normalised the legacy block into a field,
// so Validate called on its own saw an empty map, checked no paths, and a
// `runtime.files` entry of `/etc/passwd` passed. Decision 23 closes it from the
// other end -- the block is refused before any path in it is worth checking --
// which is strictly safer than checking the paths of a block being read.
//
// Kept rather than deleted because "the escape is refused" and "the escape is
// not looked at" are the same outcome only while the refusal holds. If the
// block ever became readable again, this test is what fails.
func TestALegacyBlockCarryingAPathEscapeIsRefusedBeforeThePathMatters(t *testing.T) {
	for _, path := range []string{"/etc/passwd", "../outside/compose.yaml", "a/../../escape.yaml"} {
		t.Run(path, func(t *testing.T) {
			m := validManifest()
			m.Runtimes = nil
			m.Runtime.Files = []string{path}

			err := m.Validate()

			require.Error(t, err, "%q must be rejected", path)
			assert.Contains(t, err.Error(), "is no longer read",
				"refused for carrying the block at all, which does not "+
					"depend on anything being noticed about the path")
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

// A manifest declaring nothing is told about the one spelling there is. This
// used to name both, because with nothing declared there was no signal for
// which the author was writing; naming `runtime.files` now would send them to
// the block the refusal exists to move them off.
func TestDeclaringNoRuntimeNamesTheOnlySpelling(t *testing.T) {
	m := validManifest()
	m.Runtime = RuntimeSpec{}
	m.Runtimes = nil

	err := m.Validate()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "must declare at least one runtime")
	assert.NotContains(t, err.Error(), "runtime.files",
		"the deprecated block is gone; pointing a vendor at it is pointing "+
			"them at the thing they would then have to migrate off")
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

// ApplyDefaults must not fill RuntimeSpec.Project on a release that never
// wrote a `runtime:` block, and this is what says so.
//
// Load-bearing since decision 23, and more so than when it was written. The
// refusal for a legacy block now reads *every* field of it, Project included,
// because a project-only block is the half-finished migration and still
// decides a namespace. So a defaulter that filled Project unconditionally
// would no longer merely look like "declared both" -- it would refuse every
// manifest in existence, including the ones written entirely in `runtimes:`.
func TestARuntimesOnlyReleaseSurvivesDefaultingAndValidation(t *testing.T) {
	m := validManifest()
	m.Runtime = RuntimeSpec{}
	m.Runtimes = Runtimes{"compose": {Files: []string{"compose.yaml"}}}

	m.ApplyDefaults()
	require.Empty(t, m.Runtime.Project,
		"a release on the new spelling must not inherit a grouping name from the deprecated block")

	require.NoError(t, m.Validate(), "a defaulted project must not read as a declared legacy block")

	assert.Equal(t, []string{"compose"}, m.DeclaredRuntimes().Names())
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
