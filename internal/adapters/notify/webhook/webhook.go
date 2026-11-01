// Package webhook delivers events to an HTTPS endpoint.
//
// One adapter rather than one per chat service: Slack, Teams and Discord all
// accept an incoming webhook, and the difference between them is the payload
// shape a receiver wants, not the transport. A service needing a particular
// JSON body gets a two-line receiver rather than a package here.
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/ports"
)

// DefaultTimeout bounds one request.
//
// Short on purpose. This runs inline in an operation, so the bound an operator
// experiences is latency added to every `apply` -- and a notifier that is slow
// is indistinguishable, from the operation's point of view, from one that is
// down.
const DefaultTimeout = 5 * time.Second

// DefaultRetryAfter is the pause before the one retry (RFC 0043 decision 1).
const DefaultRetryAfter = time.Second

// Notifier posts events to one endpoint.
type Notifier struct {
	name     string
	url      string
	header   string
	value    string
	minLevel events.Level
	client   *http.Client

	retryAfter time.Duration
}

var _ ports.Notifier = (*Notifier)(nil)

// Options is what constructing a notifier needs, with the credential already
// resolved: this package never learns that a secret store exists.
type Options struct {
	// Name identifies the target in logs. Never the URL when the URL came
	// from a secret -- that is the whole reason the form exists.
	Name string

	// URL is the resolved endpoint. Must be https.
	URL string

	// Header and Value are sent with the request when set, carrying
	// whatever authentication the receiver wants.
	Header string
	Value  string

	// MinLevel is the lowest check severity this target accepts. Empty
	// means error.
	MinLevel string

	// Client is injectable for tests. Nil takes a bounded default.
	Client *http.Client

	// RetryAfter is injectable for tests. Zero takes DefaultRetryAfter.
	RetryAfter time.Duration
}

// New builds a notifier, refusing an endpoint it should not talk to.
func New(opts Options) (*Notifier, error) {
	url := strings.TrimSpace(opts.URL)
	if url == "" {
		return nil, domain.ValidationError(nil, "a notify target has no URL")
	}
	// Refused at construction rather than at delivery: an operator who
	// configured plaintext should find out when they configure it, not
	// during the incident the notification exists for. Mirrors ParseRef's
	// refusal of plaintext release sources -- the payload describes a
	// deployment, and it travels to a third party.
	if !strings.HasPrefix(strings.ToLower(url), "https://") {
		return nil, domain.ValidationError(nil,
			"notify targets must be https, got %q", redactURL(url)).
			WithHint("the payload describes a deployment and crosses a network " +
				"you do not control")
	}

	level, err := parseLevel(opts.MinLevel)
	if err != nil {
		return nil, err
	}

	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	// Applied whoever built the client, not only the default one. The
	// guarantee is about where the payload and the auth header may travel,
	// so it must not depend on how the client arrived -- a caller-supplied
	// client with no policy of its own would otherwise follow a 302 to
	// plaintext.
	if client.CheckRedirect == nil {
		client.CheckRedirect = refuseRedirect
	}

	name := opts.Name
	if name == "" {
		name = "webhook"
	}
	retryAfter := opts.RetryAfter
	if retryAfter == 0 {
		retryAfter = DefaultRetryAfter
	}

	return &Notifier{
		name:       name,
		url:        url,
		header:     opts.Header,
		value:      opts.Value,
		minLevel:   level,
		client:     client,
		retryAfter: retryAfter,
	}, nil
}

// Name identifies this notifier in logs.
//
// Never the URL: a target configured through url_secret has a URL that *is* a
// credential, and a log line naming the notifier that just failed is exactly
// where it would surface.
func (n *Notifier) Name() string { return n.name }

// Notify posts one event, and once more after a pause when the answer could
// change in a second: no answer at all, a 408, a 429 or a 5xx (RFC 0043 §5.1).
// The retry waits inside the caller's deadline, which is unchanged, so the
// longest an operation can wait is too. A target that times out twice holds
// it for 11 seconds rather than 5, still inside that deadline.
//
// Returning an error is informational -- the lifecycle layer logs and drops it.
// See ports.Notifier. It is a *ports.DeliveryError.
func (n *Notifier) Notify(ctx context.Context, ev events.Event) error {
	if !n.accepts(ev) {
		return nil
	}

	// Failures before a request exists are final: the same event would
	// fail the same way.
	body, err := json.Marshal(ev)
	if err != nil {
		return n.failed(false, domain.Internal(err, "cannot serialise an event for %s", n.name))
	}

	status, err := n.post(ctx, body, ev.ID)
	if err != nil && retried(status) {
		select {
		case <-time.After(n.retryAfter):
			status, err = n.post(ctx, body, ev.ID)
		case <-ctx.Done():
		}
	}
	if err != nil {
		return n.failed(resendable(status), err)
	}
	return nil
}

// post makes one attempt, and reports the status it was answered with, or 0
// when there was no answer.
func (n *Notifier) post(ctx context.Context, body []byte, id string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, bytes.NewReader(body))
	if err != nil {
		return -1, domain.RuntimeError(err, "cannot build a request for %s", n.name)
	}
	req.Header.Set("Content-Type", "application/json")
	if id != "" {
		req.Header.Set("Idempotency-Key", id)
	}
	if n.header != "" {
		req.Header.Set(n.header, n.value)
	}

	resp, err := n.client.Do(req)
	if err != nil {
		// The URL is never in the message: net/http puts it in its own
		// error string, which is why the cause is not wrapped.
		return 0, domain.RuntimeError(nil, "cannot reach the notify target %s", n.name)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, domain.RuntimeError(nil,
			"the notify target %s answered %d", n.name, resp.StatusCode)
	}
	return resp.StatusCode, nil
}

func (n *Notifier) failed(resendable bool, err error) error {
	return &ports.DeliveryError{Target: n.name, Resendable: resendable, Err: err}
}

// retried is an answer that may change in a second: none (a refused
// connection or a timeout), a 408, a 429 or a 5xx. A status of 600 or more
// counts as a 5xx, as RFC 9110 §15 asks of a client.
func retried(status int) bool {
	return status == 0 || status == http.StatusRequestTimeout ||
		status == http.StatusTooManyRequests || status >= 500
}

// resendable is an answer a later send could get past (RFC 0043 decisions 1
// and 13): what is retried, a credential or a URL that can be fixed, and a
// redirect, which the client refuses to follow and so returns as the answer.
// Any other 4xx refuses the content itself.
func resendable(status int) bool {
	switch {
	case retried(status), status >= 300 && status < 400:
		return true
	default:
		return status == http.StatusUnauthorized || status == http.StatusForbidden ||
			status == http.StatusNotFound
	}
}

// accepts applies the per-target severity floor.
//
// Only check events carry a severity to compare. Everything that reaches here
// has already passed the kind allowlist in the lifecycle layer, so an event of
// any other forwarded kind is delivered.
func (n *Notifier) accepts(ev events.Event) bool {
	if ev.Kind != events.KindCheck {
		return true
	}
	return levelRank(ev.Level) >= levelRank(n.minLevel)
}

// refuseRedirect stops a 3xx from moving the request somewhere else.
//
// New requires https, and a redirect would undo that: an endpoint answering 302
// to an http:// location would receive the payload *and* the configured
// authentication header in the clear. Following redirects is a convenience the
// caller here does not need -- a webhook receiver that moves has been
// reconfigured, which is the operator's business rather than something to
// follow silently.
func refuseRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

func parseLevel(s string) (events.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "error":
		return events.LevelError, nil
	case "warn", "warning":
		return events.LevelWarn, nil
	default:
		return "", domain.ValidationError(nil,
			"min_level %q is not a severity", s).
			WithHint("use warn or error; empty means error")
	}
}

// levelRank orders severities so a floor can be compared.
//
// Anything below warn ranks below every floor, so an info or debug check is
// never forwarded whatever the target asked for. That is deliberate: the floor
// selects how noisy a target is, not whether it receives narration.
func levelRank(l events.Level) int {
	switch l {
	case events.LevelError:
		return 3
	case events.LevelWarn:
		return 2
	default:
		return 0
	}
}

// redactURL keeps a refused URL out of an error message.
//
// The refusal above fires on plaintext endpoints, and a plaintext endpoint is
// still capable of being a credential-in-a-path. Scheme and host are enough to
// act on.
func redactURL(raw string) string {
	i := strings.Index(raw, "://")
	if i < 0 {
		// No scheme at all, so nothing here is structure -- and the
		// value may have come from url_secret, where a malformed
		// "URL" is a bare token. Returning it whole would put a
		// credential into the refusal and from there into the log that
		// reports a dropped target.
		return "(malformed URL)"
	}
	rest := raw[i+3:]
	if j := strings.IndexAny(rest, "/?#"); j >= 0 {
		return fmt.Sprintf("%s://%s/…", raw[:i], rest[:j])
	}
	// Scheme and host only, with no path to hide.
	return raw
}
