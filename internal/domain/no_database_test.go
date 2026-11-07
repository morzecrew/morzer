package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A release declaring it keeps no database, at the manager version that reads
// the field (RFC 0016 decision 33).
func manifestWithNoDatabase() Manifest {
	m := validManifest()
	m.Compatibility.MinManagerVersion = MustParseVersion(NoDatabaseMinManagerVersion)
	m.Compatibility.Database = DatabaseNone
	return m
}

func TestAReleaseMayDeclareItKeepsNoDatabase(t *testing.T) {
	m := manifestWithNoDatabase()
	require.NoError(t, m.Validate())
}

// Anything that says the release has a database contradicts the declaration,
// and which one the vendor meant is theirs to say.
func TestNoDatabaseIsRefusedBesideWhatSaysThereIsOne(t *testing.T) {
	cases := map[string]struct {
		edit  func(*Manifest)
		field string
	}{
		"a schema minimum":  {func(m *Manifest) { m.Compatibility.DatabaseSchemaMin = 1 }, "compatibility.database_schema_min"},
		"a schema maximum":  {func(m *Manifest) { m.Compatibility.DatabaseSchemaMax = 1 }, "compatibility.database_schema_max"},
		"a schema produced": {func(m *Manifest) { m.Compatibility.DatabaseSchemaProduces = 1 }, "compatibility.database_schema_produces"},
		"a migrate step": {func(m *Manifest) {
			m.Operations = map[string]OperationSteps{OpMigrate: {{Kind: OperationKindHook, Command: []string{"hooks/migrate"}}}}
		}, "operations.migrate"},
		"an older manager": {func(m *Manifest) { m.Compatibility.MinManagerVersion = MustParseVersion("0.12.0") }, "compatibility.min_manager_version"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := manifestWithNoDatabase()
			tc.edit(&m)
			err := m.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.field)
		})
	}
}

func TestADatabaseDeclarationOtherThanNoneIsRefused(t *testing.T) {
	m := manifestWithNoDatabase()
	m.Compatibility.Database = "postgres"
	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `compatibility.database: must be none, or absent, and is "postgres"`)
}
