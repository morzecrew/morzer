package views_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/internal/ui/theme"
)

// `task list` says which tasks this machine may run, so the refusal `task run`
// would give is visible before anybody types it.
func TestTheTaskListSaysWhatMayRunHere(t *testing.T) {
	tasks := []ops.TaskEntry{
		{Name: "reindex", Service: "app", Allowed: true, Description: "Rebuild the search index"},
		{Name: "seed", Service: "app", Modes: []string{"dev"}, Args: true},
	}
	t.Setenv("COLUMNS", "200")
	for _, mode := range []ui.Mode{ui.ModePlain, ui.ModeRich} {
		var b bytes.Buffer
		require.NoError(t, ui.Render(&b, mode, theme.New(false, true), tasks))
		got := flatten(b.String())

		assert.Contains(t, got, "reindex app any yes no Rebuild the search index",
			"a task listing no modes runs anywhere and takes no arguments")
		assert.Contains(t, got, "seed app dev no yes",
			"a dev-only task on this machine read as allowed")
	}
}

// A task's inputs are named in the table and described under it, with the
// --input an operator types.
func TestTheTaskListDescribesEachInput(t *testing.T) {
	tasks := []ops.TaskEntry{
		{Name: "reindex", Service: "app", Allowed: true},
		{Name: "restore", Service: "worker", Allowed: true, Inputs: []ops.TaskInputEntry{
			{Name: "snapshot", Description: "Catalogue snapshot (JSONL)", MaxSize: 2 << 30},
		}},
	}
	t.Setenv("COLUMNS", "200")
	var b bytes.Buffer
	require.NoError(t, ui.Render(&b, ui.ModePlain, theme.New(false, true), tasks))
	got := flatten(b.String())

	assert.Contains(t, got, "restore worker any yes no snapshot")
	assert.Contains(t, got, "restore --input snapshot=<path>: Catalogue snapshot (JSONL) (at most 2GiB)")
}
