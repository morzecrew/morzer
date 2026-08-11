package tty_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/ui/theme"
	"github.com/morzecrew/morzer/internal/ui/tty"
)

// deployment is a machine with something wrong with it: a service down, a
// failing health check, an unacknowledged intervention and a warning. Every
// field a renderer could quietly drop is populated.
func deployment() ops.Status {
	return ops.Status{
		Product:        "demo",
		InstallationID: "inst_01J8ZP",
		Profile:        "embedded",
		PublicURL:      "https://demo.example",
		CurrentRelease: &domain.ReleaseRecord{
			Version: domain.MustParseVersion("1.3.0"),
		},
		PreviousRelease: &domain.ReleaseRecord{
			Version: domain.MustParseVersion("1.2.0"),
		},
		Services: []ports.ServiceState{
			{Name: "app", State: "running", Health: ports.HealthHealthy},
			{Name: "db", State: "exited (137)"},
		},
		Health: []ports.HealthResult{
			{Name: "http", OK: false, Message: "connection refused"},
		},
		LastBackup: &ops.BackupSummary{ID: "bk_01J8ZN", Age: "3h"},
		LastOperation: &domain.OperationRecord{
			ID: "op_01J8ZQ", Type: domain.OpTypeUpdate,
			Status: domain.StatusManualIntervention,
		},
		NeedsAttention: []domain.OperationRecord{{
			ID: "op_01J8ZQ", Type: domain.OpTypeUpdate,
			Error: domain.HealthError(nil, "api did not become healthy").
				WithHint("check `docker compose logs api`"),
		}},
		Problems: []string{"the release store holds 6 releases"},
	}
}

// TestWatchKeepsTheLastGoodStatusWhenARefreshFails pins the behaviour that
// makes a watch usable during an incident: the runtime becoming briefly
// unreachable is worth showing, but blanking the table over it throws away the
// information the operator is sitting there watching for.
func TestWatchKeepsTheLastGoodStatusWhenARefreshFails(t *testing.T) {
	n := 0
	tm := teatest.NewTestModel(t, tty.NewWatchModel(context.Background(), tty.WatchOptions{
		Theme:    theme.New(false, false),
		Interval: 20 * time.Millisecond,
		Refresh: func(context.Context) (ops.Status, error) {
			// The first read succeeds; every one after it fails,
			// which is what a runtime going away looks like.
			if n++; n == 1 {
				return deployment(), nil
			}
			return ops.Status{}, errors.New("cannot reach the docker daemon")
		},
	}), teatest.WithInitialTermSize(100, 40))

	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(stripANSI(string(b)), "cannot reach the docker daemon")
	}, teatest.WithDuration(5*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))

	// Asserted on the final frame, not on the accumulated output: the
	// stream still holds the earlier frame that did show the release, so
	// searching it would pass even if the view had blanked itself.
	frame := stripANSI(tm.FinalModel(t).View())
	if !strings.Contains(frame, "cannot reach the docker daemon") {
		t.Errorf("the failure is not on screen:\n%s", frame)
	}
	if !strings.Contains(frame, "1.3.0") {
		t.Errorf("a failed refresh threw away the last good status:\n%s", frame)
	}
}

func TestWatchQuitsOnQ(t *testing.T) {
	tm := teatest.NewTestModel(t, tty.NewWatchModel(context.Background(), tty.WatchOptions{
		Theme:    theme.New(false, false),
		Interval: time.Hour, // only the initial fetch
		Refresh:  func(context.Context) (ops.Status, error) { return deployment(), nil },
	}), teatest.WithInitialTermSize(100, 40))

	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(stripANSI(string(b)), "demo")
	}, teatest.WithDuration(5*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
}
