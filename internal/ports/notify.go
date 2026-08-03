package ports

import (
	"context"

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

func (n Notifiers) Notify(ctx context.Context, ev events.Event) error {
	for _, one := range n {
		// Errors are deliberately dropped here rather than joined: the
		// caller cannot act on them, and the contract says they never
		// change the outcome. The adapter logs its own failures.
		_ = one.Notify(ctx, ev)
	}
	return nil
}
