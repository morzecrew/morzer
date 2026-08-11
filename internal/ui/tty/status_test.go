package tty_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/ui/theme"
	"github.com/morzecrew/morzer/internal/ui/tty"
	"github.com/morzecrew/morzer/internal/ui/views"
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
	tm := teatest.NewTestModel(t, tty.NewWatchModel(context.Background(), tty.WatchOptions[ops.Status]{
		Body:     views.StatusDoc,
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
	tm := teatest.NewTestModel(t, tty.NewWatchModel(context.Background(), tty.WatchOptions[ops.Status]{
		Body:     views.StatusDoc,
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

// TestAWatchThatGivesUpCarriesTheReasonOut is `stats --watch`'s half of the
// contract, and the half `status --watch` deliberately does not have.
//
// A watch that stayed on screen redrawing an error would exit 0 whenever the
// operator eventually pressed `q`, which is the wrong answer for a sampler
// pointed at a daemon that has gone. `status` sets no limit for the opposite
// reason: it is what somebody leaves running while a machine comes back, and
// one that quit during the reboot would go dark at the moment it was being
// watched for.
func TestAWatchThatGivesUpCarriesTheReasonOut(t *testing.T) {
	gone := errors.New("cannot reach the docker daemon")

	model := tty.NewWatchModel(context.Background(), tty.WatchOptions[[]ports.ServiceStats]{
		Theme:    theme.New(false, false),
		Interval: 10 * time.Millisecond,
		Subject:  "statistics",
		Refresh: func(context.Context) ([]ports.ServiceStats, error) {
			return nil, gone
		},
		Body:              views.StatsDoc,
		StopAfterFailures: 2,
	})

	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(100, 40))

	// It ends on its own: no key is sent, so a watch that kept redrawing
	// would hang here rather than pass.
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))

	final := tm.FinalModel(t)
	err, ok := final.(interface{ Fatal() error })
	if !ok {
		t.Fatalf("the watch model does not report why it stopped: %T", final)
	}
	if !errors.Is(err.Fatal(), gone) {
		t.Errorf("the watch stopped and reported %v, want the daemon's own error", err.Fatal())
	}
}

// TestAStatusWatchNeverGivesUp is the other side of the same switch.
func TestAStatusWatchNeverGivesUp(t *testing.T) {
	model := tty.NewWatchModel(context.Background(), tty.WatchOptions[ops.Status]{
		Theme:    theme.New(false, false),
		Interval: 10 * time.Millisecond,
		Subject:  "status",
		Refresh: func(context.Context) (ops.Status, error) {
			return ops.Status{}, errors.New("cannot reach the docker daemon")
		},
		Body: views.StatusDoc,
		// No StopAfterFailures, which is the decision under test.
	})

	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(100, 40))
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(stripANSI(string(b)), "cannot reach the docker daemon")
	}, teatest.WithDuration(5*time.Second))

	// Still up after several failed reads. The operator ends it.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))

	final := tm.FinalModel(t)
	if reporter, ok := final.(interface{ Fatal() error }); ok && reporter.Fatal() != nil {
		t.Errorf("a status watch gave up on its own: %v", reporter.Fatal())
	}
}

// TestTheFirstFetchIsGuardedLikeEveryOther.
//
// The in-flight guard exists so a runtime that is already struggling does not
// receive a second query while it owes an answer to the first. `Init` starts a
// fetch without claiming the guard, so the window between the first read and
// the first tick is one interval wide and unprotected — which is exactly the
// window a slow daemon sits in.
func TestTheFirstFetchIsGuardedLikeEveryOther(t *testing.T) {
	var inFlight, overlapped, calls atomic.Int32

	model := tty.NewWatchModel(context.Background(), tty.WatchOptions[ops.Status]{
		Theme: theme.New(false, false),
		// Well inside the first read, so a tick lands while it is still
		// out. This is the arrangement, not a race to be lucky in.
		Interval: 10 * time.Millisecond,
		Refresh: func(context.Context) (ops.Status, error) {
			calls.Add(1)
			if inFlight.Add(1) > 1 {
				overlapped.Add(1)
			}
			defer inFlight.Add(-1)
			time.Sleep(300 * time.Millisecond)
			return deployment(), nil
		},
		Body: views.StatusDoc,
	})

	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(100, 40))
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(stripANSI(string(b)), "1.3.0")
	}, teatest.WithDuration(5*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))

	if n := overlapped.Load(); n != 0 {
		t.Errorf("%d refresh(es) started while another was in flight, out of %d: "+
			"the guard did not cover the first one", n, calls.Load())
	}
}
