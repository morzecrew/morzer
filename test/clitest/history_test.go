package clitest_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/state"
	"github.com/morzecrew/morzer/test/clitest"
)

func TestHistoryListsTheJournalNewestFirst(t *testing.T) {
	r := clitest.NewInstalled(t)

	// Written through the state store as the operations would leave them, so
	// the journal holds kinds an install with no Docker cannot produce. Each
	// one later than init's own record.
	ctx := context.Background()
	store := state.New(domain.PathsUnder(r.Root, "demo"))
	start := time.Now().Add(time.Hour)
	at := func(minutes int) domain.Time { return domain.NewTime(start.Add(time.Duration(minutes) * time.Minute)) }
	for _, rec := range []domain.OperationRecord{{
		ID: "01OPAPPLYDRY", Type: domain.OpTypeApply, Status: domain.StatusSucceeded, DryRun: true,
		StartedAt: at(0), FinishedAt: at(1), ManagerVersion: "0.11.0",
	}, {
		ID: "01OPUPDATE", Type: domain.OpTypeUpdate, Status: domain.StatusFailed,
		From: domain.MustParseVersion("1.2.0"), To: domain.MustParseVersion("1.3.0"),
		StartedAt: at(2), FinishedAt: at(4), ManagerVersion: "0.11.0",
		Flags: map[string]string{"skip_backup": "true"},
		Steps: []domain.StepRecord{
			{ID: "pull-images", Status: domain.StepSucceeded, DurationMS: 1500},
			{ID: "start-services", Status: domain.StepFailed, DurationMS: 30000, Error: "web did not start"},
		},
		Error: &domain.Error{Message: "web did not start", Hint: "read `morzer logs web`"},
	}, {
		ID: "01OPBACKUP", Type: domain.OpTypeBackup, Status: domain.StatusSucceeded,
		StartedAt: at(5), FinishedAt: at(6), ManagerVersion: "0.11.0",
	}} {
		require.NoError(t, store.AppendOperation(ctx, rec))
	}

	list := r.Run("history").ExitCode(0)
	list.OutputContains("4 operation(s), newest first", "apply (dry run)", "1.2.0 -> 1.3.0", "init")
	backup, update, dry := strings.Index(list.Stdout, "01OPBACKUP"), strings.Index(list.Stdout, "01OPUPDATE"),
		strings.Index(list.Stdout, "01OPAPPLYDRY")
	require.True(t, backup >= 0 && backup < update && update < dry, "newest first:\n%s", list.Stdout)

	r.Run("history", "--limit", "2").ExitCode(0).
		OutputContains("2 of 4 operations", "--limit 0").NoOutputContains("01OPAPPLYDRY")
	r.Run("history", "--json").ExitCode(0).
		FieldLen("data.operations", 4).FieldEquals("data.total", float64(4)).
		FieldEquals("data.operations.0.id", "01OPBACKUP")

	r.Run("history", "01OPUPDATE").ExitCode(0).OutputContains(
		"Operation 01OPUPDATE", "1.2.0 -> 1.3.0", "skip_backup", "start-services", "30s",
		"web did not start", "read `morzer logs web`")
	r.Run("history", "01OPUPDATE", "--json").ExitCode(0).
		FieldEquals("data.status", "failed").FieldLen("data.steps", 2)

	r.Run("history", "01OPNOPE").ExitCode(domain.ExitUsage).
		OutputContains(`no operation with id "01OPNOPE"`, "morzer history")
	r.Run("history", "--limit", "-1").ExitCode(domain.ExitUsage).OutputContains("--limit")

	r.Run("__complete", "history", "").ExitCode(0).
		OutputContains("01OPUPDATE\tupdate failed", "01OPBACKUP\tbackup succeeded", ":4")
	r.Run("__complete", "history", "01OPUPDATE", "").ExitCode(0).NoOutputContains("01OPBACKUP")
}

func TestHistoryOnAnInstallationWithOnlyItsInit(t *testing.T) {
	r := clitest.NewInstalled(t)

	r.Run("history").ExitCode(0).OutputContains("1 operation(s)", "init", "succeeded")
}
