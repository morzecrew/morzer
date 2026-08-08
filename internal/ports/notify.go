package ports

import (
	"context"
	"sync"

	"github.com/morzecrew/morzer/internal/events"
)

// Notifier delivers operation events somewhere outside the machine: a
// webhook, a chat channel, an alerting system.
//
// A notifier failure never affects the operation outcome. The lifecycle layer
// logs it and moves on -- an update must not fail because a webhook endpoint
// was down, and an operator must not learn about a Slack outage by way of a
// rolled-back deployment.
type Notifier interface {
	// Name identifies the notifier in logs.
	Name() string

	Notify(ctx context.Context, ev events.Event) error
}

// Notifiers fans out to several notifiers, collecting nothing. It exists so
// the lifecycle layer holds one Notifier regardless of how many are
// configured.
type Notifiers []Notifier

func (n Notifiers) Name() string { return "multi" }

// Notify delivers to every target concurrently.
//
// Concurrent rather than sequential, and the reason is the deadline above it:
// the lifecycle layer bounds the whole fan-out, because what an operator waits
// for is the sum. Delivering in sequence under one shared deadline means a slow
// first target consumes the budget and every later one is skipped -- so the
// bound would have quietly broken the contract that each configured target
// receives each event.
//
// Fan-out makes the total the slowest target rather than the sum of all of
// them, so both properties hold at once.
func (n Notifiers) Notify(ctx context.Context, ev events.Event) error {
	var wg sync.WaitGroup
	for _, one := range n {
		wg.Add(1)
		go func(target Notifier) {
			defer wg.Done()
			// Errors are deliberately dropped here rather than
			// joined: the caller cannot act on them, and the
			// contract says they never change the outcome. The
			// adapter logs its own failures.
			_ = target.Notify(ctx, ev)
		}(one)
	}
	wg.Wait()
	return nil
}
