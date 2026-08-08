package cli

import (
	"context"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/morzecrew/morzer/internal/adapters/notify/webhook"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// notifyCredentials is the document a notify target's credential secret holds.
//
// One secret holding one document rather than one field, for the reason
// ParseTargetCredentials records: a header name and its value must rotate
// together, and two secrets that must change at the same time is one chance to
// change only one of them.
type notifyCredentials struct {
	Header string `yaml:"header"`
	Value  string `yaml:"value"`
}

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
func (a *App) buildNotifiers(ctx context.Context, inst domain.Installation, secrets ports.SecretStore) ports.Notifier {
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

func (a *App) buildNotifier(
	ctx context.Context, cfg domain.NotifyTargetConfig, secrets ports.SecretStore,
) (ports.Notifier, error) {
	secretName, direct, err := cfg.Endpoint()
	if err != nil {
		return nil, err
	}

	url := direct
	if secretName != "" {
		url, err = readSecretValue(ctx, secrets, secretName)
		if err != nil {
			return nil, err
		}
	}

	opts := webhook.Options{
		Name:     cfg.Label(),
		URL:      url,
		MinLevel: cfg.MinLevel,
	}

	if cfg.Credentials != "" {
		raw, err := readSecretValue(ctx, secrets, cfg.Credentials)
		if err != nil {
			return nil, err
		}
		var creds notifyCredentials
		// The cause is never wrapped with the value: a YAML decoder
		// quotes the line it failed on, and that line is the credential.
		if err := yaml.Unmarshal([]byte(raw), &creds); err != nil {
			return nil, domain.ValidationError(nil,
				"the secret %q is not a notify credential document", cfg.Credentials).
				WithHint("it is a small YAML document with `header` and `value`")
		}
		// Both, or neither is any use. A document with an empty header
		// or an empty value builds a notifier that sends no
		// authentication at all, so the endpoint rejects every delivery
		// while the installation looks correctly configured -- a
		// notification channel that is silently off.
		if strings.TrimSpace(creds.Header) == "" || strings.TrimSpace(creds.Value) == "" {
			return nil, domain.ValidationError(nil,
				"the secret %q sets no header and value", cfg.Credentials).
				WithHint("it is a small YAML document with both, e.g. " +
					"`header: Authorization` and `value: Bearer …`")
		}
		opts.Header, opts.Value = creds.Header, creds.Value
	}

	return webhook.New(opts)
}

// readSecretValue reads one secret's plaintext.
func readSecretValue(ctx context.Context, secrets ports.SecretStore, name string) (string, error) {
	if secrets == nil {
		return "", domain.Internal(nil,
			"a notify target names a secret but no secret store was wired")
	}
	set, err := secrets.Load(ctx)
	if err != nil {
		return "", domain.ValidationError(err, "cannot read the secret %q", name)
	}
	secret, ok := set.Get(name)
	if !ok {
		return "", domain.ValidationError(domain.ErrNotFound,
			"a notify target names the secret %q, which is not set", name).
			WithHint("set it with `morzer secret set %s`", name)
	}
	return strings.TrimSpace(secret.Reveal()), nil
}
