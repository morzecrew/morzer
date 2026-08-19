package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Per-runtime options, at the layer that carries them and refuses to read them.
// RFC 0023 §2.2 and decision 14.

// The fold that carried `runtime.project` into the legacy runtime's `project`
// option is gone with the block that fed it (decision 23).
//
// This asserts the removal rather than deleting the test, because the fold
// existed for a sharp reason -- a bundle built before `runtimes:` keeps the
// namespace its volumes are already in, and dropping the project renames every
// volume, network and container on the next apply. That reason did not stop
// being true; what changed is that such a bundle is now refused outright
// instead of being read and silently renamed, which is the safe direction and
// the one a vendor is told about.
func TestTheLegacyProjectIsNoLongerFoldedIntoAnOption(t *testing.T) {
	m := Manifest{
		Metadata: Metadata{Name: "demo"},
		Runtime: RuntimeSpec{
			Project: "myapp",
			Files:   []string{"compose.yaml"},
		},
	}

	assert.Empty(t, m.DeclaredRuntimes(),
		"the legacy block declares nothing, so there is no option to fold into")

	err := m.Validate()
	require.Error(t, err, "and the manifest carrying it is refused rather than read")
	assert.Contains(t, err.Error(), "options.project",
		"the refusal must name where a project goes now, or the migration "+
			"silently renames every volume the deployment owns")
}

func TestARuntimesReleaseInheritsNoProjectFromTheDeprecatedBlock(t *testing.T) {
	m := Manifest{
		Metadata: Metadata{Name: "demo"},
		Runtimes: Runtimes{"compose": {Files: []string{"compose.yaml"}}},
	}
	m.ApplyDefaults()

	declared := m.DeclaredRuntimes()
	assert.Empty(t, declared["compose"].Options,
		"a release that declared no options must not acquire one by defaulting")
}

// A half-finished migration: the files moved, the project stayed behind. It is
// refused rather than ignored, because ignoring it is what renames the volumes.
func TestAProjectLeftBesideRuntimesIsRefused(t *testing.T) {
	m := Manifest{
		APIVersion: APIVersionV1Alpha1,
		Kind:       KindApplicationRelease,
		Metadata:   Metadata{Name: "demo", Version: MustParseVersion("1.0.0")},
		Runtime:    RuntimeSpec{Project: "myapp"},
		Runtimes:   Runtimes{"compose": {Files: []string{"compose.yaml"}}},
		Images:     map[string]ImageSpec{"app": {Ref: "example.test/app@sha256:" + strings.Repeat("a", 64)}},
	}
	m.ApplyDefaults()

	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "runtime.project")
	// It names where the value goes. A refusal that only says "not here"
	// leaves a vendor to guess, and the guess that costs them is deleting it.
	assert.Contains(t, err.Error(), "runtimes.compose.options.project")
}

func TestARuntimeOptionIsBoundedInShapeAndNotInMeaning(t *testing.T) {
	valid := map[string]map[string]string{
		"a name":            {"project": "myapp"},
		"digits and dashes": {"project": "my-app-2"},
		"underscored key":   {"unit_prefix": "demo"},
		// Meaningless to Compose and accepted here: which keys exist is
		// the adapter's answer, and a list up here would be the runtime
		// catalogue decision 7 exists to prevent.
		"a key no runtime has": {"wharrgarbl": "x"},
	}
	for name, options := range valid {
		t.Run(name, func(t *testing.T) {
			m := manifestWithOptions(options)
			require.NoError(t, m.Validate())
		})
	}

	invalid := map[string]map[string]string{
		"an upper-case key":         {"Project": "x"},
		"a key with a hyphen":       {"unit-prefix": "x"},
		"an empty key":              {"": "x"},
		"a newline in the value":    {"project": "myapp\nUnit=evil"},
		"an escape in the value":    {"project": "myapp\x1b[31m"},
		"a value that is too long":  {"project": strings.Repeat("a", 201)},
		"a key that is too long":    {strings.Repeat("k", 33): "x"},
		"a carriage return sneaked": {"project": "myapp\rmore"},
	}
	for name, options := range invalid {
		t.Run(name, func(t *testing.T) {
			m := manifestWithOptions(options)
			err := m.Validate()
			require.Error(t, err, "the manifest is release-supplied input and this reaches argv")
		})
	}
}

// manifestWithOptions is the smallest valid manifest carrying one runtime's
// options, so a table asserts the option rules and nothing else.
func manifestWithOptions(options map[string]string) Manifest {
	m := Manifest{
		APIVersion: APIVersionV1Alpha1,
		Kind:       KindApplicationRelease,
		Metadata:   Metadata{Name: "demo", Version: MustParseVersion("1.0.0")},
		Runtimes: Runtimes{"compose": {
			Files:   []string{"compose.yaml"},
			Options: options,
		}},
		Images: map[string]ImageSpec{"app": {Ref: "example.test/app@sha256:" + strings.Repeat("a", 64)}},
	}
	m.ApplyDefaults()
	return m
}

// The recorded copy gets the same grammar, for the reason the backup schedule
// does: installation state is a file an operator can edit, and these values are
// handed to something that puts them in argv.
func TestAnInstallationRefusesAMalformedRecordedOption(t *testing.T) {
	base := func(options map[string]string) Installation {
		return Installation{
			SchemaVersion:  InstallationSchemaVersion,
			ID:             "inst_01",
			Product:        "demo",
			RuntimeOptions: options,
		}
	}

	require.NoError(t, base(map[string]string{"project": "myapp"}).Validate())
	require.NoError(t, base(map[string]string{}).Validate(),
		"declaring nothing is a fact about the installation, not an error")
	require.NoError(t, base(nil).Validate(),
		"nil is every installation created before schema 10")

	require.Error(t, base(map[string]string{"project": "myapp\x1b[2J"}).Validate())
	require.Error(t, base(map[string]string{"Project": "myapp"}).Validate())
}
