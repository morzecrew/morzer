package ops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
)

// capture is a Notifier that records what it was handed.
type capture struct {
	got []events.Event
	err error
}

func (c *capture) Name() string { return "capture" }
func (c *capture) Notify(_ context.Context, ev events.Event) error {
	c.got = append(c.got, ev)
	return c.err
}

// TestNotifyFinishedCoversEveryOutcome.
//
// The call sites all used to sit after `if runErr != nil { return }`, so only
// successes were ever reported -- the half nobody needs. The failure rows are
// the point; the dry-run row is the state this wrapper was not tested against
// when it was written, and it announced that somebody had looked at a plan.
func TestNotifyFinishedCoversEveryOutcome(t *testing.T) {
	cases := []struct {
		name        string
		rec         domain.OperationRecord
		runErr      error
		wantSent    bool
		wantStatus  domain.OperationStatus
		wantFailure bool
	}{
		{
			name:       "success is reported",
			rec:        domain.OperationRecord{Status: domain.StatusSucceeded},
			wantSent:   true,
			wantStatus: domain.StatusSucceeded,
		},
		{
			name:        "failure is reported, which it never used to be",
			rec:         domain.OperationRecord{Status: domain.StatusFailed},
			runErr:      errors.New("the migration hook exited 1"),
			wantSent:    true,
			wantStatus:  domain.StatusFailed,
			wantFailure: true,
		},
		{
			name:        "a compensated update keeps its own status",
			rec:         domain.OperationRecord{Status: domain.StatusCompensated},
			runErr:      errors.New("health checks never passed"),
			wantSent:    true,
			wantStatus:  domain.StatusCompensated,
			wantFailure: true,
		},
		{
			name:        "a record with no status still reports failed",
			rec:         domain.OperationRecord{Status: domain.StatusRunning},
			runErr:      errors.New("interrupted before the engine wrote one"),
			wantSent:    true,
			wantStatus:  domain.StatusFailed,
			wantFailure: true,
		},
		{
			name:     "a dry run is not an outcome",
			rec:      domain.OperationRecord{Status: domain.StatusSucceeded, DryRun: true},
			wantSent: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &capture{}
			d := &Deps{Notifier: c}
			d.notifyFinished(context.Background(), "op_1", domain.OpTypeApply, tc.rec, tc.runErr)

			if got := len(c.got) == 1; got != tc.wantSent {
				t.Fatalf("sent = %t, want %t", got, tc.wantSent)
			}
			if !tc.wantSent {
				return
			}
			ev := c.got[0]
			if ev.Status != string(tc.wantStatus) {
				t.Errorf("status = %q, want %q", ev.Status, tc.wantStatus)
			}
			if (ev.Err != nil) != tc.wantFailure {
				t.Errorf("error carried = %t, want %t", ev.Err != nil, tc.wantFailure)
			}
		})
	}
}

// TestNotifyDoesNotOutliveCancellation.
//
// The deadline was originally taken on a detached context so a failed
// operation's message would still go out. It also meant Ctrl-C left the CLI
// apparently wedged for the whole deadline while posting to an endpoint nobody
// was waiting for -- a worse failure than a missing message about a run the
// operator watched themselves interrupt.
func TestNotifyDoesNotOutliveCancellation(t *testing.T) {
	blocked := make(chan struct{})
	d := &Deps{Notifier: notifierFunc(func(ctx context.Context, _ events.Event) error {
		<-ctx.Done()
		close(blocked)
		return ctx.Err()
	})}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		d.notify(ctx, events.Event{Kind: events.KindOperationFinished})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("notify outlived a cancelled operation")
	}
	<-blocked
}

type notifierFunc func(context.Context, events.Event) error

func (f notifierFunc) Name() string { return "fn" }
func (f notifierFunc) Notify(ctx context.Context, ev events.Event) error {
	return f(ctx, ev)
}
