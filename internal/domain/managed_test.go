package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

// What a release lets a desired-state document change (RFC 0057 §5.6).

func managedManifest(managed domain.ManagedSpec) *domain.Manifest {
	m := validManifestWithParameters(map[string]domain.ParameterSpec{
		"site": {Type: domain.ParamString},
	})
	m.Managed = &managed
	m.Compatibility.MinManagerVersion = domain.MustParseVersion(domain.DesiredStateMinManagerVersion)
	return m
}

// `managed` is refused by an older manager, which decodes the manifest
// strictly, so a release declaring it needs the manager that reads it.
func TestManagedNeedsTheFloor(t *testing.T) {
	m := managedManifest(domain.ManagedSpec{Parameters: []string{"site"}})
	assertNoManagedProblem(t, m)

	m.Compatibility.MinManagerVersion = domain.MustParseVersion("0.15.0")
	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be at least "+domain.DesiredStateMinManagerVersion+
		", because the release declares managed")

	plain := validManifestWithParameters(map[string]domain.ParameterSpec{"site": {Type: domain.ParamString}})
	if err := plain.Validate(); err != nil {
		assert.NotContains(t, err.Error(), "managed")
	}
}

func TestManagedNamesWhatTheReleaseDeclaresAndNothingMorzerForbids(t *testing.T) {
	for managed, want := range map[*domain.ManagedSpec]string{
		{Parameters: []string{"no_such"}}:                 `managed.parameters: names "no_such", which the release does not declare`,
		{Parameters: []string{"site", "site"}}:            `managed.parameters: names "site" twice`,
		{Settings: []string{"release.require_signature"}}: `managed.settings: names "release.require_signature"`,
		{Settings: []string{"sync.authority"}}:            `managed.settings: names "sync.authority"`,
		{Settings: []string{"update.auto_apply"}}:         `managed.settings: names "update.auto_apply"`,
		{Secrets: []string{"a", "a"}}:                     `managed.secrets: names "a" twice`,
	} {
		err := managedManifest(*managed).Validate()
		require.Error(t, err, "%+v", *managed)
		assert.Contains(t, err.Error(), want)
	}

	ok := managedManifest(domain.ManagedSpec{
		Parameters: []string{"site"},
		Secrets:    []string{"token"},
		Settings:   []string{"installation.profile", "installation.domains"},
	})
	assertNoManagedProblem(t, ok)
	assert.True(t, ok.Managed.Allows("parameters", "site"))
	assert.True(t, ok.Managed.Allows("settings", "installation.domains"))
	assert.False(t, ok.Managed.Allows("parameters", "other"))
	assert.False(t, ok.Managed.Allows("secrets", "site"))
}

// A managed secret is checked against the schema, which the manifest alone
// cannot see.
func TestManagedSecretsAreDeclaredByTheSchema(t *testing.T) {
	m := managedManifest(domain.ManagedSpec{Secrets: []string{"token", "missing"}})
	schema := domain.SecretSchema{Secrets: []domain.SecretDeclaration{{Name: "token"}}}
	assert.Equal(t, []string{"missing"}, m.ManagedSecretsUndeclared(schema))
}

// assertNoManagedProblem: the smallest manifest the helper builds fails on
// fields of its own, so only the messages about managed are read.
func assertNoManagedProblem(t *testing.T, m *domain.Manifest) {
	t.Helper()
	if err := m.Validate(); err != nil {
		assert.NotContains(t, err.Error(), "managed")
		assert.NotContains(t, err.Error(), "min_manager_version")
	}
}

// `published` names a task the release declares, one that runs with nothing
// passed to it, under the floor `managed` has (RFC 0057 §5.10).
func TestPublishedNamesARunnableTask(t *testing.T) {
	withTasks := func(published string) *domain.Manifest {
		m := managedManifest(domain.ManagedSpec{})
		m.Tasks = map[string]domain.TaskSpec{
			"entry": {Service: "app", Command: []string{"relay-entry"}},
			"load":  {Service: "app", Command: []string{"load"}, Inputs: map[string]domain.TaskInput{"dump": {}}},
			"dev":   {Service: "app", Command: []string{"seed"}, Modes: []string{"dev"}},
		}
		m.Published = &domain.PublishedSpec{Task: published}
		return m
	}
	problem := func(m *domain.Manifest) string {
		if err := m.Validate(); err != nil {
			return err.Error()
		}
		return ""
	}

	assert.NotContains(t, problem(withTasks("entry")), "published")
	assert.Contains(t, problem(withTasks("missing")), `published.task: names "missing", which the release does not declare`)
	assert.Contains(t, problem(withTasks("load")), `published.task: names "load", which takes inputs`)
	assert.Contains(t, problem(withTasks("dev")), `published.task: names "dev", which may not run on production machines`)
	assert.Contains(t, problem(withTasks("")), "published.task: is required")

	old := withTasks("entry")
	old.Compatibility.MinManagerVersion = domain.MustParseVersion("0.15.0")
	assert.Contains(t, problem(old), "because the release declares published")

	// published.refresh takes `publish` alone, under a floor of its own
	// (#353).
	refreshing := withTasks("entry")
	refreshing.Published.Refresh = domain.FactsRefreshPublish
	assert.Contains(t, problem(refreshing), "because the release declares published.refresh")
	refreshing.Compatibility.MinManagerVersion = domain.MustParseVersion(domain.FactsRefreshMinManagerVersion)
	assert.NotContains(t, problem(refreshing), "published")
	refreshing.Published.Refresh = "hourly"
	assert.Contains(t, problem(refreshing), `published.refresh: is "hourly"; the one value is "publish"`)
}

// An empty block is still a block: an older manager refuses `managed: {}` and
// `published: {}` as it refuses full ones, so the floor applies to both.
func TestAnEmptyBlockStillNeedsTheFloor(t *testing.T) {
	for name, set := range map[string]func(m *domain.Manifest){
		"managed":   func(m *domain.Manifest) { m.Managed = &domain.ManagedSpec{} },
		"published": func(m *domain.Manifest) { m.Published = &domain.PublishedSpec{} },
	} {
		m := validManifestWithParameters(map[string]domain.ParameterSpec{"site": {Type: domain.ParamString}})
		set(m)
		m.Compatibility.MinManagerVersion = domain.MustParseVersion("0.15.0")
		err := m.Validate()
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "because the release declares "+name, name)
	}
}
