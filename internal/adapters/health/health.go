// Package health implements the health probers a release can declare.
//
// A container reporting "running" and an application reporting "ready" are
// different claims. Compose can only make the first; these probes make the
// second, which is what `apply` actually needs before it calls an update
// successful.
package health

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// HTTPProber checks an HTTP endpoint.
type HTTPProber struct {
	client *http.Client
}

func NewHTTP() *HTTPProber {
	return &HTTPProber{
		client: &http.Client{
			// Redirects are not followed: a health endpoint that
			// redirects is misconfigured, and following one could
			// turn a local check into an outbound request.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

var _ ports.HealthProber = (*HTTPProber)(nil)

func (p *HTTPProber) Type() domain.HealthCheckType { return domain.HealthHTTP }

func (p *HTTPProber) Check(ctx context.Context, spec ports.CheckSpec) (ports.HealthResult, error) {
	started := time.Now()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, spec.Check.URL, nil)
	if err != nil {
		return ports.HealthResult{}, domain.ValidationError(err,
			"health check %q has an invalid url %q", spec.Name(), spec.Check.URL)
	}
	req.Header.Set("User-Agent", "morzer-health/1")

	resp, err := p.client.Do(req)
	if err != nil {
		// A connection failure is a failing check, not a broken
		// prober: the service simply is not up yet, which is the
		// normal state while waiting for readiness.
		return ports.HealthResult{
			Name:     spec.Name(),
			OK:       false,
			Message:  summariseHTTPError(err),
			Duration: time.Since(started),
		}, nil
	}
	defer func() { _ = resp.Body.Close() }()

	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	msg := fmt.Sprintf("HTTP %d", resp.StatusCode)
	if !ok {
		msg = fmt.Sprintf("HTTP %d from %s", resp.StatusCode, spec.Check.URL)
	}

	return ports.HealthResult{
		Name:     spec.Name(),
		OK:       ok,
		Message:  msg,
		Duration: time.Since(started),
	}, nil
}

// summariseHTTPError keeps the useful part of a transport error. Go's
// *url.Error wraps the full URL and a chain of syscall errors, which is a
// paragraph where a phrase will do.
func summariseHTTPError(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	}
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		if netErr.Op == "dial" {
			return "connection refused"
		}
		return netErr.Op + " failed"
	}
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i > 0 && i+2 < len(msg) {
		msg = msg[i+2:]
	}
	return msg
}

// TCPProber checks that a port accepts connections. Useful for services with
// no HTTP surface -- a database, a queue.
type TCPProber struct{}

func NewTCP() *TCPProber { return &TCPProber{} }

var _ ports.HealthProber = (*TCPProber)(nil)

func (p *TCPProber) Type() domain.HealthCheckType { return domain.HealthTCP }

func (p *TCPProber) Check(ctx context.Context, spec ports.CheckSpec) (ports.HealthResult, error) {
	started := time.Now()

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", spec.Check.Address)
	if err != nil {
		return ports.HealthResult{
			Name:     spec.Name(),
			OK:       false,
			Message:  summariseHTTPError(err),
			Duration: time.Since(started),
		}, nil
	}
	_ = conn.Close()

	return ports.HealthResult{
		Name:     spec.Name(),
		OK:       true,
		Message:  "accepting connections",
		Duration: time.Since(started),
	}, nil
}

// CommandProber runs an executable from the release.
//
// This is how a product answers questions the manager cannot: whether
// migrations are current, whether a replica has caught up. The command runs
// under the hook ABI, from the release root.
type CommandProber struct {
	runner exec.Runner
	redact []string
}

func NewCommand(runner exec.Runner, redact []string) *CommandProber {
	return &CommandProber{runner: runner, redact: redact}
}

var _ ports.HealthProber = (*CommandProber)(nil)

func (p *CommandProber) Type() domain.HealthCheckType { return domain.HealthCommand }

func (p *CommandProber) Check(ctx context.Context, spec ports.CheckSpec) (ports.HealthResult, error) {
	started := time.Now()

	if len(spec.Check.Command) == 0 {
		return ports.HealthResult{}, domain.ValidationError(nil,
			"health check %q declares no command", spec.Name())
	}

	argv := append([]string(nil), spec.Check.Command...)
	// The command is release-relative; resolving it against the release
	// root is what keeps a health check from executing something on PATH
	// that happens to share its name.
	if spec.WorkingDir != "" && !strings.HasPrefix(argv[0], "/") {
		argv[0] = spec.WorkingDir + "/" + argv[0]
	}

	res, err := p.runner.Run(ctx, exec.Command{
		Argv:          argv,
		Dir:           spec.WorkingDir,
		Env:           exec.BaseEnv(spec.Env),
		Timeout:       spec.Timeout(),
		Redact:        p.redact,
		CaptureOutput: true,
	})

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// A non-zero exit is a failing check, not a broken
			// prober.
			return ports.HealthResult{
				Name:     spec.Name(),
				OK:       false,
				Message:  checkMessage(res.Stdout, res.Stderr, exitErr.ExitCode),
				Duration: time.Since(started),
			}, nil
		}
		return ports.HealthResult{}, err
	}

	return ports.HealthResult{
		Name:     spec.Name(),
		OK:       true,
		Message:  firstLine(res.Stdout),
		Duration: time.Since(started),
	}, nil
}

func checkMessage(stdout, stderr string, code int) string {
	if s := firstLine(stderr); s != "" {
		return s
	}
	if s := firstLine(stdout); s != "" {
		return s
	}
	return fmt.Sprintf("exited with code %d", code)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// Waiter polls a set of checks until they all pass or the context expires.
//
// The polling policy lives here rather than in each prober because it is a
// lifecycle decision, not a transport one: how long to wait for readiness is
// the same question whether the probe is HTTP or a command.
type Waiter struct {
	probers map[domain.HealthCheckType]ports.HealthProber

	// interval is the gap between rounds. Fixed rather than exponential:
	// readiness usually arrives within seconds, and a backoff would turn a
	// service that took 30s to start into a 60s wait.
	interval time.Duration

	// clock is time.Now unless a test replaced it. Injected because the
	// start-period rule is about elapsed time, and a test that proved it by
	// sleeping for the period would be a test nobody runs.
	clock func() time.Time
}

func (w *Waiter) now() time.Time {
	if w.clock == nil {
		return time.Now()
	}
	return w.clock()
}

// withClock overrides the waiter's notion of now.
//
// Unexported: nothing in production sets it -- now() falls back to time.Now
// when it is nil -- and a clock only tests pass is a clock no test exercises as
// production leaves it.
func (w *Waiter) withClock(clock func() time.Time) *Waiter {
	w.clock = clock
	return w
}

func NewWaiter(probers ...ports.HealthProber) *Waiter {
	m := make(map[domain.HealthCheckType]ports.HealthProber, len(probers))
	for _, p := range probers {
		m[p.Type()] = p
	}
	return &Waiter{probers: m, interval: 2 * time.Second}
}

var _ ports.HealthWaiter = (*Waiter)(nil)

// WithInterval overrides the poll interval. Tests use it to avoid real waits.
func (w *Waiter) WithInterval(d time.Duration) *Waiter {
	w.interval = d
	return w
}

// CheckOnce runs every check exactly once, concurrently. This is what `doctor`
// and `status` use: they report the current state rather than waiting for a
// desired one.
func (w *Waiter) CheckOnce(ctx context.Context, specs []ports.CheckSpec) ([]ports.HealthResult, error) {
	results := make([]ports.HealthResult, len(specs))
	var wg sync.WaitGroup

	for i, spec := range specs {
		prober, ok := w.probers[spec.Check.Type]
		if !ok {
			results[i] = ports.HealthResult{
				Name:    spec.Name(),
				OK:      false,
				Message: fmt.Sprintf("no prober for check type %q", spec.Check.Type),
			}
			continue
		}

		wg.Add(1)
		go func(i int, spec ports.CheckSpec, prober ports.HealthProber) {
			defer wg.Done()

			probeCtx, cancel := context.WithTimeout(ctx, spec.Timeout())
			defer cancel()

			res, err := prober.Check(probeCtx, spec)
			if err != nil {
				// A prober that could not run at all is
				// reported as a failing check rather than
				// aborting the whole sweep: one broken check
				// must not hide the state of the others.
				res = ports.HealthResult{
					Name:    spec.Name(),
					OK:      false,
					Message: domain.AsError(err).Message,
				}
			}
			res.Attempts = 1
			results[i] = res
		}(i, spec, prober)
	}

	wg.Wait()
	return results, nil
}

// WaitReady polls until every check passes or the context expires.
//
// Checks that have already passed are not re-run: a database that came up is
// not going to un-come-up while the API finishes starting, and re-probing it
// every two seconds adds load precisely when the system is busiest.
func (w *Waiter) WaitReady(ctx context.Context, specs []ports.CheckSpec) ([]ports.HealthResult, error) {
	if len(specs) == 0 {
		return nil, nil
	}

	final := make([]ports.HealthResult, len(specs))
	passed := make([]bool, len(specs))
	attempts := make([]int, len(specs))

	started := w.now()
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		pending := make([]ports.CheckSpec, 0, len(specs))
		indices := make([]int, 0, len(specs))
		for i, spec := range specs {
			if !passed[i] {
				pending = append(pending, spec)
				indices = append(indices, i)
			}
		}
		if len(pending) == 0 {
			return final, nil
		}

		// The round is bounded by the start periods it is waiting on,
		// when every pending check declares one. Without this the
		// deadline is only *observed* between rounds, so a check with a
		// five-second period and a thirty-second probe timeout reports
		// its failure twenty-five seconds late -- the field promises to
		// tell an operator that a product is dead rather than slow, and
		// telling them half a minute after the fact is most of the way
		// back to not telling them.
		roundCtx, cancel := w.boundByStartPeriods(ctx, pending, started)
		round, err := w.CheckOnce(roundCtx, pending)
		cancel()
		if err != nil {
			return final, err
		}
		for j, res := range round {
			i := indices[j]
			attempts[i]++
			res.Attempts = attempts[i]
			final[i] = res
			if res.OK {
				passed[i] = true
			}
		}

		if allTrue(passed) {
			return final, nil
		}

		// A check that has been failing for longer than the vendor
		// said it may take to start is not slow, it is unhealthy.
		// Without this the two are the same observation, and a dead
		// product occupies the whole operation timeout before saying
		// so -- fifteen minutes of "waiting for health" that were
		// decidable after ninety seconds.
		//
		// Only when *every* still-failing check has outlived its own
		// declared period: one slow service must not condemn the
		// deployment while another is still legitimately starting.
		if overdue := overdueChecks(specs, passed, w.now().Sub(started)); overdue {
			return final, startPeriodError(specs, passed, final)
		}

		select {
		case <-ctx.Done():
			return final, timeoutError(specs, passed, final)
		case <-ticker.C:
		}
	}
}

// boundByStartPeriods caps a round at the last start-period deadline among the
// checks it is waiting on.
//
// Only when *every* pending check declares one: a check without a period must
// keep the round open for as long as the operation allows, and cutting it short
// because a sibling declared five seconds would turn one vendor's precision
// into every other check's timeout.
//
// The cap is the *latest* of the deadlines, not the earliest. Cutting at the
// earliest would abandon a check that is still legitimately inside its own
// longer period, which is the opposite of what the field is for.
func (w *Waiter) boundByStartPeriods(
	ctx context.Context, pending []ports.CheckSpec, started time.Time,
) (context.Context, context.CancelFunc) {
	var last time.Duration
	for _, spec := range pending {
		period := spec.StartPeriod()
		if period <= 0 {
			return ctx, func() {}
		}
		if period > last {
			last = period
		}
	}
	if last == 0 {
		return ctx, func() {}
	}

	remaining := last - w.now().Sub(started)
	if remaining <= 0 {
		// Already past every deadline: let the round run its normal
		// course rather than handing the probers a dead context, and
		// the overdue check below ends the wait when it returns.
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, remaining)
}

// overdueChecks reports whether every check still failing has outlived the
// start period its own manifest entry declared.
//
// False when any of them declared none, which is what keeps the previous
// behaviour for every manifest written before the field existed: no
// declaration means the waiter waits for as long as the operation allows.
func overdueChecks(specs []ports.CheckSpec, passed []bool, elapsed time.Duration) bool {
	any := false
	for i, spec := range specs {
		if passed[i] {
			continue
		}
		period := spec.StartPeriod()
		if period <= 0 || elapsed < period {
			return false
		}
		any = true
	}
	return any
}

// startPeriodError says the product had its grace period and did not use it.
//
// Deliberately worded differently from timeoutError: one means "we ran out of
// time", the other means "the vendor told us how long this takes and it took
// longer". An operator acts on those differently.
func startPeriodError(specs []ports.CheckSpec, passed []bool, results []ports.HealthResult) error {
	var failed []string
	for i, spec := range specs {
		if passed[i] {
			continue
		}
		msg := results[i].Message
		if msg == "" {
			msg = "no response"
		}
		failed = append(failed, fmt.Sprintf("%s (%s, start period %s)",
			spec.Name(), msg, spec.StartPeriod()))
	}

	return domain.HealthError(nil,
		"the product did not become healthy within the start period the release declares: %s",
		strings.Join(failed, ", ")).
		WithHint("this is longer than the vendor said startup takes, so it is a failure " +
			"rather than a slow boot; check service logs with `docker compose logs`")
}

func allTrue(bs []bool) bool { return !slices.Contains(bs, false) }

// timeoutError names exactly which checks never passed, and what they last
// said. "Health check failed" without that detail sends an operator to the
// logs to find out something the manager already knew.
func timeoutError(specs []ports.CheckSpec, passed []bool, results []ports.HealthResult) error {
	var failed []string
	for i, spec := range specs {
		if passed[i] {
			continue
		}
		msg := results[i].Message
		if msg == "" {
			msg = "no response"
		}
		failed = append(failed, fmt.Sprintf("%s (%s)", spec.Name(), msg))
	}

	return domain.HealthError(nil,
		"the product did not become healthy: %s", strings.Join(failed, ", ")).
		WithHint("check service logs with `docker compose logs`, " +
			"or raise the check timeout in the release manifest")
}
