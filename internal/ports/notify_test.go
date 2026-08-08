package ports_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/ports"
)

type stubNotifier struct {
	name    string
	calls   *atomic.Int32
	err     error
	block   time.Duration
	entered chan struct{}
}

func (s stubNotifier) Name() string { return s.name }
func (s stubNotifier) Notify(ctx context.Context, _ events.Event) error {
	if s.entered != nil {
		s.entered <- struct{}{}
	}
	if s.block > 0 {
		select {
		case <-time.After(s.block):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if s.calls != nil {
		s.calls.Add(1)
	}
	return s.err
}

// TestNotifiersDeliversToEveryTargetDespiteFailures.
//
// The contract an operator configures against: two targets means two copies.
// A fan-out that stopped at the first error would silently make the second
// target decorative.
func TestNotifiersDeliversToEveryTargetDespiteFailures(t *testing.T) {
	var a, b, c atomic.Int32
	n := ports.Notifiers{
		stubNotifier{name: "a", calls: &a, err: errors.New("down")},
		stubNotifier{name: "b", calls: &b},
		stubNotifier{name: "c", calls: &c, err: errors.New("also down")},
	}

	if err := n.Notify(context.Background(), events.Event{Kind: events.KindOperationFinished}); err != nil {
		t.Errorf("the fan-out reported an error to its caller: %v", err)
	}
	for name, got := range map[string]int32{"a": a.Load(), "b": b.Load(), "c": c.Load()} {
		if got != 1 {
			t.Errorf("target %s received %d events, want 1", name, got)
		}
	}
}

// TestASlowTargetDoesNotStarveTheOthers.
//
// Delivery is bounded by one deadline covering the whole fan-out, because what
// an operator waits for is the sum. Sequentially, a slow first target would
// consume that budget and every later target would be skipped -- bounding the
// latency by breaking the contract above. Concurrency is what lets both hold.
func TestASlowTargetDoesNotStarveTheOthers(t *testing.T) {
	var fast atomic.Int32
	entered := make(chan struct{}, 1)

	n := ports.Notifiers{
		stubNotifier{name: "slow", block: 30 * time.Second, entered: entered},
		stubNotifier{name: "fast", calls: &fast},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() { _ = n.Notify(ctx, events.Event{Kind: events.KindOperationFinished}); close(done) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the fan-out did not return when its deadline expired")
	}
	<-entered

	if fast.Load() != 1 {
		t.Error("the fast target was starved by the slow one")
	}
}

// TestNotifiersOnNoTargets keeps the empty case decided rather than inherited.
func TestNotifiersOnNoTargets(t *testing.T) {
	var n ports.Notifiers
	if err := n.Notify(context.Background(), events.Event{}); err != nil {
		t.Errorf("an empty fan-out errored: %v", err)
	}
	if n.Name() != "multi" {
		t.Errorf("Name() = %q", n.Name())
	}
}
