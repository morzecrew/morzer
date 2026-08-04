package health_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/adapters/health"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// The Waiter decides when an update is finished. Everything an operator sees
// after `morzer update` -- "healthy", or a refusal naming the service that
// never came up -- comes out of these two methods, and before this file
// neither had a test.

// scriptedProber answers a fixed sequence: the nth call gets the nth reply,
// and the last reply repeats. That is how a service that takes three rounds to
// come up is expressed without a real three-round wait.
type scriptedProber struct {
	kind    domain.HealthCheckType
	replies []ports.HealthResult
	err     error
	calls   atomic.Int32
}

func (p *scriptedProber) Type() domain.HealthCheckType { return p.kind }

func (p *scriptedProber) Check(ctx context.Context, spec ports.CheckSpec) (ports.HealthResult, error) {
	n := int(p.calls.Add(1))
	if p.err != nil {
		return ports.HealthResult{}, p.err
	}
	if n > len(p.replies) {
		n = len(p.replies)
	}
	res := p.replies[n-1]
	res.Name = spec.Name()
	return res, nil
}

func ok(message string) ports.HealthResult  { return ports.HealthResult{OK: true, Message: message} }
func bad(message string) ports.HealthResult { return ports.HealthResult{OK: false, Message: message} }

func checkSpec(name string, kind domain.HealthCheckType) ports.CheckSpec {
	return ports.CheckSpec{Check: domain.HealthCheck{Name: name, Type: kind}}
}

func TestCheckOnceRunsEveryCheckAndAttributesTheResults(t *testing.T) {
	up := &scriptedProber{kind: domain.HealthHTTP, replies: []ports.HealthResult{ok("ready")}}
	down := &scriptedProber{kind: domain.HealthTCP, replies: []ports.HealthResult{bad("refused")}}

	results, err := health.NewWaiter(up, down).CheckOnce(context.Background(), []ports.CheckSpec{
		checkSpec("api", domain.HealthHTTP),
		checkSpec("db", domain.HealthTCP),
	})
	if err != nil {
		t.Fatalf("CheckOnce must report per-check state, not fail: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("got %d results for 2 checks", len(results))
	}
	// Order is the caller's, not completion order: the probes run
	// concurrently and `status` prints them in manifest order.
	if results[0].Name != "api" || !results[0].OK {
		t.Errorf("the first result is not the first check: %+v", results[0])
	}
	if results[1].Name != "db" || results[1].OK {
		t.Errorf("the second result is not the second check: %+v", results[1])
	}
	for _, r := range results {
		if r.Attempts != 1 {
			t.Errorf("%s reports %d attempts from a single sweep", r.Name, r.Attempts)
		}
	}
}

// TestCheckOnceReportsACheckTypeNothingCanProbe is a release-authoring
// mistake. It has to name the type, because the operator's fix is in the
// manifest.
func TestCheckOnceReportsACheckTypeNothingCanProbe(t *testing.T) {
	waiter := health.NewWaiter(&scriptedProber{
		kind: domain.HealthHTTP, replies: []ports.HealthResult{ok("")},
	})

	results, err := waiter.CheckOnce(context.Background(),
		[]ports.CheckSpec{checkSpec("queue", domain.HealthCheckType("carrier-pigeon"))})
	if err != nil {
		t.Fatalf("an unprobeable check is a finding, not a failure of the sweep: %v", err)
	}
	if results[0].OK {
		t.Error("a check nothing can probe was reported passing")
	}
	if !strings.Contains(results[0].Message, "carrier-pigeon") {
		t.Errorf("message %q does not name the check type the manifest asked for",
			results[0].Message)
	}
}

// TestCheckOnceKeepsGoingWhenOneProberIsBroken is the property that makes
// `doctor` useful on a sick machine: one broken probe must not hide the state
// of the others.
func TestCheckOnceKeepsGoingWhenOneProberIsBroken(t *testing.T) {
	broken := &scriptedProber{
		kind: domain.HealthCommand,
		err:  domain.HealthError(errors.New("fork/exec: permission denied"), "the probe could not run"),
	}
	working := &scriptedProber{kind: domain.HealthHTTP, replies: []ports.HealthResult{ok("ready")}}

	results, err := health.NewWaiter(broken, working).CheckOnce(context.Background(),
		[]ports.CheckSpec{
			checkSpec("migrations", domain.HealthCommand),
			checkSpec("api", domain.HealthHTTP),
		})
	if err != nil {
		t.Fatalf("a prober that could not run must not abort the sweep: %v", err)
	}

	if results[0].OK {
		t.Error("a probe that never ran was reported passing")
	}
	if results[0].Message == "" {
		t.Error("a prober failure produced no message, so the report says nothing")
	}
	if !results[1].OK {
		t.Error("a working check was lost because a different one was broken")
	}
}

func TestWaitReadyOnNoChecksReturnsImmediately(t *testing.T) {
	// A release with no health checks is legitimate. Waiting for nothing
	// must not be an error and must not be a wait.
	results, err := health.NewWaiter().WaitReady(context.Background(), nil)
	if err != nil {
		t.Fatalf("waiting for no checks failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results for no checks", len(results))
	}
}

// TestWaitReadyPollsUntilTheServiceComesUp is the ordinary case: a database
// that is not listening for the first two rounds.
func TestWaitReadyPollsUntilTheServiceComesUp(t *testing.T) {
	prober := &scriptedProber{kind: domain.HealthTCP, replies: []ports.HealthResult{
		bad("connection refused"),
		bad("connection refused"),
		ok("accepting connections"),
	}}

	waiter := health.NewWaiter(prober).WithInterval(time.Millisecond)
	results, err := waiter.WaitReady(context.Background(),
		[]ports.CheckSpec{checkSpec("db", domain.HealthTCP)})
	if err != nil {
		t.Fatalf("a service that came up on the third round was reported failed: %v", err)
	}
	if !results[0].OK {
		t.Errorf("the final result is the failing one, not the passing one: %+v", results[0])
	}
	if results[0].Attempts != 3 {
		t.Errorf("Attempts = %d, want 3; the count is what `status` shows an "+
			"operator watching a slow start", results[0].Attempts)
	}
}

// TestWaitReadyStopsRe-probingWhatAlreadyPassed keeps a converged database
// from being polled every two seconds while the API finishes starting --
// precisely when the machine is busiest.
func TestWaitReadyStopsReprobingWhatAlreadyPassed(t *testing.T) {
	fast := &scriptedProber{kind: domain.HealthTCP, replies: []ports.HealthResult{ok("up")}}
	slow := &scriptedProber{kind: domain.HealthHTTP, replies: []ports.HealthResult{
		bad("503"), bad("503"), bad("503"), ok("ready"),
	}}

	waiter := health.NewWaiter(fast, slow).WithInterval(time.Millisecond)
	results, err := waiter.WaitReady(context.Background(), []ports.CheckSpec{
		checkSpec("db", domain.HealthTCP),
		checkSpec("api", domain.HealthHTTP),
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := fast.calls.Load(); got != 1 {
		t.Errorf("the database was probed %d times after passing on the first; "+
			"a converged service must not be re-probed", got)
	}
	if !results[0].OK || !results[1].OK {
		t.Errorf("not everything ended up healthy: %+v", results)
	}
	if results[0].Attempts != 1 {
		t.Errorf("the passing check reports %d attempts", results[0].Attempts)
	}
}

// TestWaitReadyTimesOutNamingWhatNeverCameUp is the message an operator reads
// after a failed update. "Health check failed" without the detail sends them to
// the logs for something the manager already knew.
func TestWaitReadyTimesOutNamingWhatNeverCameUp(t *testing.T) {
	up := &scriptedProber{kind: domain.HealthTCP, replies: []ports.HealthResult{ok("up")}}
	never := &scriptedProber{kind: domain.HealthHTTP, replies: []ports.HealthResult{bad("503 Service Unavailable")}}
	silent := &scriptedProber{kind: domain.HealthCommand, replies: []ports.HealthResult{bad("")}}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	waiter := health.NewWaiter(up, never, silent).WithInterval(time.Millisecond)
	results, err := waiter.WaitReady(ctx, []ports.CheckSpec{
		checkSpec("db", domain.HealthTCP),
		checkSpec("api", domain.HealthHTTP),
		checkSpec("migrations", domain.HealthCommand),
	})
	if err == nil {
		t.Fatal("a service that never came up was reported healthy")
	}

	de := domain.AsError(err)
	if de.Code != domain.CodeHealth {
		t.Errorf("exit code %v, want the health code: the exit status is how a "+
			"unit file distinguishes this from a crash", de.Code)
	}
	if !strings.Contains(de.Message, "api") {
		t.Errorf("message %q does not name the check that failed", de.Message)
	}
	if !strings.Contains(de.Message, "503") {
		t.Errorf("message %q drops what the check last said", de.Message)
	}
	if strings.Contains(de.Message, "db") {
		t.Errorf("message %q blames a check that passed", de.Message)
	}
	// A check that failed without saying anything still has to appear, or
	// the operator is told everything is fine except the ones they can see.
	if !strings.Contains(de.Message, "migrations") ||
		!strings.Contains(de.Message, "no response") {
		t.Errorf("message %q loses the silent failure", de.Message)
	}
	if de.Hint == "" {
		t.Error("the timeout gives the operator nowhere to go next")
	}

	// The partial results still travel, because `status` prints them.
	if len(results) != 3 || !results[0].OK {
		t.Errorf("the results from the last round were lost: %+v", results)
	}
}
