package ports

import (
	"context"
	"errors"
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

	// Notify delivers one event. Its error names the target, because the
	// fan-out below joins several and the log line is the only place an
	// operator learns which one failed.
	Notify(ctx context.Context, ev events.Event) error
}

// DeliveryError is one target's failure to take an event (RFC 0043 decision
// 10).
//
// Resendable says whether sending the same event again could succeed: a target
// that was down, or one whose credential or URL can still be fixed. A target
// that refused the content itself is final. Read with errors.As, never from the
// message, which is written for an operator.
type DeliveryError struct {
	Target     string
	Resendable bool
	Err        error
}

func (e *DeliveryError) Error() string { return e.Err.Error() }
func (e *DeliveryError) Unwrap() error { return e.Err }

// Notifiers fans out to several notifiers and joins their failures. It exists
// so the lifecycle layer holds one Notifier regardless of how many are
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
//
// Every failure is returned, joined, for the caller to log; none changes the
// outcome. They were dropped here on the grounds that the adapter logs its
// own, which the webhook never did, so a failed delivery was logged nowhere
// (RFC 0043 decision 9).
func (n Notifiers) Notify(ctx context.Context, ev events.Event) error {
	var wg sync.WaitGroup
	errs := make([]error, len(n))
	for i, one := range n {
		wg.Add(1)
		go func(target Notifier) {
			defer wg.Done()
			errs[i] = target.Notify(ctx, ev)
		}(one)
	}
	wg.Wait()
	return errors.Join(errs...)
}
