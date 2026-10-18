package cli

import (
	"context"

	"github.com/morzecrew/morzer/internal/adapters/notify/webhook"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// buildNotifiers resolves the configured notify targets into a fan-out.
//
// Returns nil when nothing is configured, which keeps Deps.Notifier nil and
// leaves the no-op path in ops.notify exercised on every installation that has
// not asked to be told anything.
//
// A target that cannot be resolved is dropped with a warning rather than
// failing the command. The contract is that notification never changes an
// operation's outcome, and refusing to run `apply` because a webhook secret was
// deleted would break that at the least convenient moment -- an operator's
// deployment must not depend on their alerting being healthy.
func (a *App) buildNotifiers(ctx context.Context, inst domain.Installation, secrets ops.SecretLoader) ports.Notifier {
	if !inst.Notify.HasTargets() {
		return nil
	}

	var out ports.Notifiers
	for _, cfg := range inst.Notify.Targets {
		n, err := a.buildNotifier(ctx, cfg, secrets)
		if err != nil {
			// Nil-checked because this runs during wiring, and a
			// caller that assembles Deps without a logger must not
			// crash on the path that exists to *tolerate* failure.
			if a.log != nil {
				a.log.Warn("a notify target was dropped",
					"target", cfg.Label(), "error", domain.AsError(err).Message)
			}
			continue
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// buildNotifier builds one target from what ops.ResolveNotifyTarget read, the
// resolution `notify target add` and `doctor` ask too, so the command refuses
// exactly the targets this would drop.
func (a *App) buildNotifier(
	ctx context.Context, cfg domain.NotifyTargetConfig, secrets ops.SecretLoader,
) (ports.Notifier, error) {
	endpoint, err := ops.ResolveNotifyTarget(ctx, secrets, cfg)
	if err != nil {
		return nil, err
	}
	return webhook.New(webhook.Options{
		Name:     cfg.Label(),
		URL:      endpoint.URL,
		Header:   endpoint.Header,
		Value:    endpoint.Value,
		MinLevel: cfg.MinLevel,
	})
}
