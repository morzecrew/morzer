package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A manifest declaring tasks, at the manager version that reads them (RFC 0035).
func manifestWithTask(task TaskSpec) Manifest {
	m := validManifest()
	m.Compatibility.MinManagerVersion = MustParseVersion(TasksMinManagerVersion)
	m.Tasks = map[string]TaskSpec{"seed": task}
	m.ApplyDefaults()
	return m
}

func TestAWellFormedTaskValidatesAndGetsTheOperationTimeout(t *testing.T) {
	m := manifestWithTask(TaskSpec{Service: "app", Command: []string{"seed"}, Modes: []string{"dev"}})
	require.NoError(t, m.Validate())
	assert.Equal(t, Duration(DefaultOperationTimeout), m.Tasks["seed"].Timeout)
}

func TestAMalformedTaskIsRefusedByField(t *testing.T) {
	cases := map[string]struct {
		name string
		task TaskSpec
		want string
	}{
		"no service":           {"seed", TaskSpec{Command: []string{"seed"}}, "tasks.seed.service: is required"},
		"no command":           {"seed", TaskSpec{Service: "app"}, "tasks.seed.command: is required"},
		"unknown mode":         {"seed", TaskSpec{Service: "app", Command: []string{"seed"}, Modes: []string{"staging"}}, `got "staging"`},
		"an empty mode":        {"seed", TaskSpec{Service: "app", Command: []string{"seed"}, Modes: []string{""}}, `got ""`},
		"a padded mode":        {"seed", TaskSpec{Service: "app", Command: []string{"seed"}, Modes: []string{" dev"}}, `got " dev"`},
		"multi-line describe":  {"seed", TaskSpec{Service: "app", Command: []string{"seed"}, Description: "a\nb"}, "must be a single line"},
		"negative timeout":     {"seed", TaskSpec{Service: "app", Command: []string{"seed"}, Timeout: -1}, "tasks.seed.timeout: must be positive"},
		"upper-case name":      {"Seed", TaskSpec{Service: "app", Command: []string{"seed"}}, "tasks.Seed: is not a valid task name"},
		"name with whitespace": {"re index", TaskSpec{Service: "app", Command: []string{"x"}}, "is not a valid task name"},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			m := manifestWithTask(TaskSpec{})
			m.Tasks = map[string]TaskSpec{tc.name: tc.task}
			err := m.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// An older manager cannot decode `tasks:` at all, so the bundle has to say
// which manager it needs, or its customer is told about a typo.
func TestTasksNeedTheManagerThatReadsThem(t *testing.T) {
	m := manifestWithTask(TaskSpec{Service: "app", Command: []string{"seed"}})
	m.Compatibility.MinManagerVersion = MustParseVersion("0.7.0")

	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "compatibility.min_manager_version: must be at least "+
		TasksMinManagerVersion+", because the release declares tasks")
}

func TestATaskIsAllowedOnTheModesItLists(t *testing.T) {
	everywhere := TaskSpec{}
	assert.True(t, everywhere.AllowedOn(""), "no modes means every mode, production included")
	assert.True(t, everywhere.AllowedOn(ModeDev))

	sandbox := TaskSpec{Modes: []string{"dev"}}
	assert.False(t, sandbox.AllowedOn(""), "a dev-only task ran on production")
	assert.True(t, sandbox.AllowedOn(ModeDev))

	production := TaskSpec{Modes: []string{"production"}}
	assert.True(t, production.AllowedOn(""), "production is spelled by absence and listed by name")
	assert.False(t, production.AllowedOn(ModeDev))
}

func TestTaskNamesAreSorted(t *testing.T) {
	m := Manifest{Tasks: map[string]TaskSpec{"seed": {}, "reindex": {}, "audit": {}}}
	assert.Equal(t, []string{"audit", "reindex", "seed"}, m.TaskNames())
}
