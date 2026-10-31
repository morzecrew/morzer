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
		"multi-line describe":  {"seed", TaskSpec{Service: "app", Command: []string{"seed"}, Description: "a\nb"}, "must be one line, with no control characters"},
		"escape in describe":   {"seed", TaskSpec{Service: "app", Command: []string{"seed"}, Description: "\x1b[2Jfine"}, "must be one line, with no control characters"},
		"empty program":        {"seed", TaskSpec{Service: "app", Command: []string{""}}, "tasks.seed.command: must name a program first"},
		"blank program":        {"seed", TaskSpec{Service: "app", Command: []string{"  ", "x"}}, "tasks.seed.command: must name a program first"},
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

// A task with inputs, at the manager version that reads them (RFC 0041).
func manifestWithInputs(task TaskSpec) Manifest {
	m := manifestWithTask(task)
	m.Compatibility.MinManagerVersion = MustParseVersion(TaskInputsMinManagerVersion)
	return m
}

func TestAWellFormedTaskInputValidates(t *testing.T) {
	m := manifestWithInputs(TaskSpec{
		Service: "worker",
		Command: []string{"restore", "{inputs.snapshot}", "--meta={inputs.meta_file}"},
		Inputs: map[string]TaskInput{
			"snapshot":  {Description: "Catalogue snapshot (JSONL)", MaxSize: 2 * GiB},
			"meta_file": {},
		},
	})
	require.NoError(t, m.Validate())
}

func TestAMalformedTaskInputIsRefusedByField(t *testing.T) {
	cases := map[string]struct {
		task TaskSpec
		want string
	}{
		"upper-case name": {TaskSpec{Service: "w", Command: []string{"x"},
			Inputs: map[string]TaskInput{"Snap": {}}}, "tasks.seed.inputs.Snap: is not a valid input name"},
		"dashed name": {TaskSpec{Service: "w", Command: []string{"x"},
			Inputs: map[string]TaskInput{"snap-shot": {}}}, "tasks.seed.inputs.snap-shot: is not a valid input name"},
		"multi-line description": {TaskSpec{Service: "w", Command: []string{"x"},
			Inputs: map[string]TaskInput{"snap": {Description: "a\nb"}}}, "tasks.seed.inputs.snap.description"},
		"negative size": {TaskSpec{Service: "w", Command: []string{"x"},
			Inputs: map[string]TaskInput{"snap": {MaxSize: -1}}}, "tasks.seed.inputs.snap.max_size: must be positive"},
		"undeclared placeholder": {TaskSpec{Service: "w", Command: []string{"x", "{inputs.snap}"}},
			"names {inputs.snap}, which the task does not declare"},
		"unterminated placeholder": {TaskSpec{Service: "w", Command: []string{"x", "{inputs.snap"},
			Inputs: map[string]TaskInput{"snap": {}}}, "has an unterminated {inputs.<name>}"},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			m := manifestWithInputs(tc.task)
			err := m.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestTaskInputsNeedTheManagerThatReadsThem(t *testing.T) {
	m := manifestWithTask(TaskSpec{Service: "w", Command: []string{"x"},
		Inputs: map[string]TaskInput{"snap": {}}})

	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be at least "+TaskInputsMinManagerVersion+
		", because a task declares inputs")
}

// The placeholder is replaced inside an argument as well as as one, and the
// variable names the same path.
func TestThePlaceholderAndTheVariableNameTheSamePath(t *testing.T) {
	task := TaskSpec{Command: []string{"restore", "{inputs.snapshot}", "--meta={inputs.meta_file}", "plain"}}
	assert.Equal(t,
		[]string{"restore", "/morzer/inputs/snapshot", "--meta=/morzer/inputs/meta_file", "plain"},
		task.CommandWithInputs())
	assert.Equal(t, "MORZER_INPUT_META_FILE", TaskInputEnv("meta_file"))
	assert.Equal(t, []string{"restore"}, TaskSpec{Command: []string{"restore"}}.CommandWithInputs())
}

// `inputs: {}` is a field an older manager's strict decoding refuses as
// unknown, so it needs the floor as much as a populated map does.
func TestAnEmptyInputsMapStillNeedsTheManagerThatReadsIt(t *testing.T) {
	m := manifestWithTask(TaskSpec{Service: "w", Command: []string{"x"}, Inputs: map[string]TaskInput{}})
	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "because a task declares inputs")
}
