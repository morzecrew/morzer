package ops

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/goccy/go-yaml"
	"golang.org/x/net/http/httpguts"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/preflight"
	"github.com/morzecrew/morzer/internal/ports"
)

// Where outcomes are reported changes here after `init`, the way backup targets
// do (RFC 0015 decision 2). Like a setting, a change converges nothing, so it
// takes the deployment lock and writes the state, and is not an engine
// operation.

// SecretLoader is the only thing resolving a notify target needs from the
// secret store: it reads two values and never writes.
type SecretLoader interface {
	Load(ctx context.Context) (domain.SecretSet, error)
}

// NotifyEndpoint is a notify target with its secrets read: what a notifier is
// built from.
type NotifyEndpoint struct {
	URL    string
	Header string
	Value  string
}

// notifyCredentials is the document a notify target's credential secret holds.
//
// One secret holding one document rather than one field, for the reason
// ParseTargetCredentials records: a header name and its value must rotate
// together.
type notifyCredentials struct {
	Header string `yaml:"header"`
	Value  string `yaml:"value"`
}

// ResolveNotifyTarget reads a target's secrets and refuses what a notifier would
// not be built from.
//
// The one place both the wiring and `notify target add` ask, so the command
// refuses exactly the targets the wiring would drop. Its errors never quote a
// value read from a secret: a URL from url_secret is the credential.
func ResolveNotifyTarget(
	ctx context.Context, secrets SecretLoader, cfg domain.NotifyTargetConfig,
) (NotifyEndpoint, error) {
	if err := cfg.Validate(); err != nil {
		return NotifyEndpoint{}, err
	}
	secretName, direct, err := cfg.Endpoint()
	if err != nil {
		return NotifyEndpoint{}, err
	}

	var out NotifyEndpoint
	if secretName != "" {
		if out.URL, err = readNotifySecret(ctx, secrets, secretName); err != nil {
			return NotifyEndpoint{}, err
		}
		// Never quoted: a URL kept in a secret is the credential.
		shown := fmt.Sprintf("the URL in the secret %q", secretName)
		if err := checkNotifyURL(out.URL, shown); err != nil {
			return NotifyEndpoint{}, err
		}
	} else {
		out.URL = direct
		if err := checkNotifyURL(out.URL, ports.RedactRefCredentials(direct)); err != nil {
			return NotifyEndpoint{}, err
		}
	}

	if cfg.Credentials == "" {
		return out, nil
	}
	raw, err := readNotifySecret(ctx, secrets, cfg.Credentials)
	if err != nil {
		return NotifyEndpoint{}, err
	}
	var creds notifyCredentials
	// The cause is never wrapped with the value: a YAML decoder quotes the
	// line it failed on, and that line is the credential.
	if err := yaml.Unmarshal([]byte(raw), &creds); err != nil {
		return NotifyEndpoint{}, domain.ValidationError(nil,
			"the secret %q is not a notify credential document", cfg.Credentials).
			WithHint("it is a small YAML document with `header` and `value`")
	}
	// Both, or neither is any use. A document with an empty header or value
	// builds a notifier that sends no authentication at all, so the endpoint
	// rejects every delivery while the installation looks correctly
	// configured -- a notification channel that is silently off.
	if strings.TrimSpace(creds.Header) == "" || strings.TrimSpace(creds.Value) == "" {
		return NotifyEndpoint{}, domain.ValidationError(nil,
			"the secret %q sets no header and value", cfg.Credentials).
			WithHint("it is a small YAML document with both, e.g. " +
				"`header: Authorization` and `value: Bearer …`")
	}
	// The same silence by another route: net/http refuses such a header on
	// every request, so each delivery fails and is dropped. Neither is quoted,
	// because the value is the credential.
	if !httpguts.ValidHeaderFieldName(creds.Header) || !httpguts.ValidHeaderFieldValue(creds.Value) {
		return NotifyEndpoint{}, domain.ValidationError(nil,
			"the secret %q is not a valid HTTP header", cfg.Credentials).
			WithHint("a header name is one token, such as `Authorization`, and " +
				"its value is one line")
	}
	// The webhook sends each event's id in this header (RFC 0043 decision 3),
	// and a credential naming it would replace every id with one value, so a
	// receiver dropping duplicates would drop every event after the first.
	if strings.EqualFold(creds.Header, "Idempotency-Key") {
		return NotifyEndpoint{}, domain.ValidationError(nil,
			"the secret %q names the header Idempotency-Key, which carries each event's id", cfg.Credentials).
			WithHint("name the header the receiver authenticates with, such as `Authorization`")
	}
	out.Header, out.Value = creds.Header, creds.Value
	return out, nil
}

// checkNotifyURL refuses an endpoint a notifier must not talk to. shown is what
// the refusal may print.
func checkNotifyURL(raw, shown string) error {
	u, err := url.Parse(raw)
	// Hostname, not Host: `https://:443/hook` has a port and nowhere to send.
	if err != nil || u.Hostname() == "" {
		return domain.Usage("%s is not a URL", shown).
			WithHint("write https://hooks.example/morzer")
	}
	// Decision 3: the payload describes a deployment and crosses a network
	// the operator does not control.
	if u.Scheme != "https" {
		return domain.Usage("notify targets must be https, and %s is not", shown).
			WithHint("the payload describes a deployment and crosses a network you do not control")
	}
	return nil
}

// readNotifySecret reads one secret's plaintext.
func readNotifySecret(ctx context.Context, secrets SecretLoader, name string) (string, error) {
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

// NotifyTargetAddOptions configures adding a notify target.
type NotifyTargetAddOptions struct {
	Options
	Target domain.NotifyTargetConfig
}

// NotifyTargetAdd records a new place operation outcomes are reported.
//
// Nothing is sent. A webhook has no read-only request, and a probe would be a
// delivery its receiver acts on, of a kind decision 4 does not forward. What the
// wiring would refuse -- plaintext, a secret that is not set, a credential
// document missing a half -- is refused here instead, at the terminal where the
// mistake was made rather than in a log line nobody reads.
func NotifyTargetAdd(ctx context.Context, d *Deps, opts NotifyTargetAddOptions) (Result, error) {
	cfg := opts.Target
	cfg.Name = strings.TrimSpace(cfg.Name)
	cfg.URL = strings.TrimSpace(cfg.URL)
	cfg.URLSecret = strings.TrimSpace(cfg.URLSecret)
	cfg.Credentials = strings.TrimSpace(cfg.Credentials)
	cfg.MinLevel = strings.ToLower(strings.TrimSpace(cfg.MinLevel))

	if _, err := ResolveNotifyTarget(ctx, d.Secrets, cfg); err != nil {
		return Result{}, err
	}
	// Refused here and not in ResolveNotifyTarget, which the wiring also
	// asks: a target already carrying one keeps working, and only a new one
	// is kept from writing a password into installation.yaml, which `doctor`
	// prints and every backup copies.
	if u, err := url.Parse(cfg.URL); err == nil && u.User != nil {
		return Result{}, domain.Usage("%s carries a credential, and a url is written to installation.yaml",
			ports.RedactRefCredentials(cfg.URL)).
			WithHint("put the whole URL in a secret and use --url-secret, " +
				"or send the credential as a header with --credentials")
	}

	err := d.withLock(ctx, d.newOpID(), domain.OpTypeConfig, opts.Options, func(ctx context.Context) error {
		current, err := d.loadInstallation(ctx)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(current.Notify.Targets, sameNotifyTarget(cfg)) {
			return domain.Usage("%s is already a notify target", cfg.Label()).
				WithHint("run `morzer notify target list` to see them all")
		}
		if opts.DryRun {
			return nil
		}
		current.Notify.Targets = append(current.Notify.Targets, cfg)
		if err := d.saveInstallation(ctx, current); err != nil {
			return err
		}
		d.refreshNotifyUnits(ctx, current)
		return nil
	})
	switch {
	case err != nil:
		return Result{}, err
	case opts.DryRun:
		return Result{Summary: "would add notify target " + cfg.Label(), Data: cfg}, nil
	}
	return Result{
		Summary: fmt.Sprintf("notify target %s added; the next operation's outcome is reported there", cfg.Label()),
		Data:    cfg,
	}, nil
}

// sameNotifyTarget matches a target that would deliver to the same place, or
// that the same name would identify: two targets reading one endpoint deliver
// every event twice, and `remove` could not tell two of one name apart.
func sameNotifyTarget(cfg domain.NotifyTargetConfig) func(domain.NotifyTargetConfig) bool {
	return func(existing domain.NotifyTargetConfig) bool {
		same := func(a, b string) bool {
			a = strings.TrimSpace(a)
			return a != "" && a == strings.TrimSpace(b)
		}
		return same(cfg.Name, existing.Name) || same(cfg.URL, existing.URL) ||
			same(cfg.URLSecret, existing.URLSecret)
	}
}

// NotifyTargetRemove stops reporting to a target, named by its name or its url.
//
// The name, because a target using url_secret has no URL that may be printed or
// typed (decision 10).
func NotifyTargetRemove(ctx context.Context, d *Deps, opts Options, which string) (Result, error) {
	which = strings.TrimSpace(which)
	var removed domain.NotifyTargetConfig
	err := d.withLock(ctx, d.newOpID(), domain.OpTypeConfig, opts, func(ctx context.Context) error {
		current, err := d.loadInstallation(ctx)
		if err != nil {
			return err
		}
		idx := slices.IndexFunc(current.Notify.Targets, func(t domain.NotifyTargetConfig) bool {
			return which != "" && (strings.TrimSpace(t.Name) == which || strings.TrimSpace(t.URL) == which)
		})
		if idx < 0 {
			// What was typed may be a URL with a password in it.
			e := domain.Usage("%s is not a notify target of this installation",
				ports.RedactRefCredentials(which))
			if !current.Notify.HasTargets() {
				return e.WithHint("this installation reports to no notify target")
			}
			return e.WithHint("run `morzer notify target list` to see them")
		}
		removed = current.Notify.Targets[idx]
		if opts.DryRun {
			return nil
		}
		current.Notify.Targets = slices.Delete(slices.Clone(current.Notify.Targets), idx, idx+1)
		if err := d.saveInstallation(ctx, current); err != nil {
			return err
		}
		// What it had still to accept goes with it (RFC 0043 §5.2).
		if err := d.forgetTarget(removed.Label()); err != nil {
			d.warnf("the target was removed and its undelivered notifications were not: %s",
				domain.AsError(err).Message)
		}
		d.refreshNotifyUnits(ctx, current)
		return nil
	})
	switch {
	case err != nil:
		return Result{}, err
	case opts.DryRun:
		return Result{Summary: "would remove notify target " + removed.Label()}, nil
	}
	return Result{Summary: "notify target " + removed.Label() + " removed"}, nil
}

// refreshNotifyUnits reconciles the doctor timer, which exists while a target
// does (RFC 0043 decision 4). A failure does not fail the change, for the
// reason a backup target's does not: the target is on disk, and re-running the
// command would meet "already a notify target".
func (d *Deps) refreshNotifyUnits(ctx context.Context, inst domain.Installation) {
	if err := d.refreshUnits(ctx, inst); err != nil {
		d.warnf("the notify targets were changed and the doctor timer was not reconciled: %s; "+
			"`morzer doctor` reports it until something reconciles the units",
			domain.AsError(err).Message)
	}
}

// NotifyTarget is one configured target as `notify target list` reports it:
// what may be printed, never a value read from a secret.
type NotifyTarget struct {
	Target string `json:"target"`

	// URLSecret names the secret holding the URL, when the URL is one.
	URLSecret   string `json:"url_secret,omitempty"`
	Credentials string `json:"credentials,omitempty"`
	MinLevel    string `json:"min_level"`
}

// NotifyTargetList reads the configured targets. Read-only: no lock.
func NotifyTargetList(ctx context.Context, d *Deps) ([]NotifyTarget, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return nil, err
	}
	return notifyTargets(inst), nil
}

func notifyTargets(inst domain.Installation) []NotifyTarget {
	out := make([]NotifyTarget, 0, len(inst.Notify.Targets))
	for _, t := range inst.Notify.Targets {
		level := strings.ToLower(strings.TrimSpace(t.MinLevel))
		if level == "" {
			level = "error"
		}
		out = append(out, NotifyTarget{
			Target:      t.Label(),
			URLSecret:   t.URLSecret,
			Credentials: t.Credentials,
			MinLevel:    level,
		})
	}
	return out
}

// checkNotifyTargets reports a target the wiring would drop.
//
// A target that does not resolve is dropped with a log line (decision 12 keeps
// the notifier nil rather than failing the command), so a deleted secret
// silences notifications with nothing to show for it. A warning rather than a
// failure: notification never changes an operation's outcome, and a failing
// check would turn a monitor watching doctor's exit code red over the alerting
// rather than the deployment.
func (d *Deps) checkNotifyTargets(inst domain.Installation) preflight.Check {
	return preflight.Check{
		ID:          "notify.target-resolves",
		Category:    preflight.CategoryConfig,
		Description: "every notify target can be reported to",
		Run: func(ctx context.Context) events.CheckResult {
			var problems []string
			for _, t := range inst.Notify.Targets {
				if _, err := ResolveNotifyTarget(ctx, d.Secrets, t); err != nil {
					problems = append(problems, fmt.Sprintf("%s (%s)",
						t.Label(), domain.AsError(err).Message))
				}
			}
			if len(problems) > 0 {
				return preflight.Warn(
					"nothing is reported to it until this is fixed; "+
						"`morzer notify target remove` and `add` it again once it is",
					"dropped: %s", strings.Join(problems, "; "))
			}
			return preflight.OK("%s", describeNotifyCount(len(inst.Notify.Targets)))
		},
	}
}

func describeNotifyCount(n int) string {
	if n == 1 {
		return "1 notify target"
	}
	return fmt.Sprintf("%d notify targets", n)
}
