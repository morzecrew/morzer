package webhook_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/adapters/notify/webhook"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/ports"
)

// answering serves the statuses in order, the last one from then on, and
// records each request's Idempotency-Key and the id in its body.
type answering struct {
	mu       sync.Mutex
	statuses []int
	keys     []string
	bodyIDs  []string
}

func (a *answering) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var ev events.Event
	_ = json.Unmarshal(body, &ev)

	a.mu.Lock()
	defer a.mu.Unlock()
	a.keys = append(a.keys, r.Header.Get("Idempotency-Key"))
	a.bodyIDs = append(a.bodyIDs, ev.ID)
	status := a.statuses[0]
	if len(a.statuses) > 1 {
		a.statuses = a.statuses[1:]
	}
	w.WriteHeader(status)
}

func (a *answering) requests() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.keys)
}

func notifierFor(t *testing.T, h http.Handler) *webhook.Notifier {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	n, err := webhook.New(webhook.Options{
		Name: "chat", URL: srv.URL, Client: srv.Client(), RetryAfter: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func identified() events.Event {
	ev := finished()
	ev.ID = "evt_01TESTEVENT"
	return ev
}

func deliveryError(t *testing.T, err error) *ports.DeliveryError {
	t.Helper()
	var de *ports.DeliveryError
	if !errors.As(err, &de) {
		t.Fatalf("the failure is not a DeliveryError: %v", err)
	}
	if de.Target != "chat" {
		t.Errorf("target = %q", de.Target)
	}
	return de
}

// A target that blinks gets the event on the second attempt, with the same id
// in the header and the body both times.
func TestATargetThatBlinksGetsOneRetry(t *testing.T) {
	a := &answering{statuses: []int{http.StatusServiceUnavailable, http.StatusOK}}
	if err := notifierFor(t, a).Notify(context.Background(), identified()); err != nil {
		t.Fatalf("a 503 then a 200 was reported as a failure: %v", err)
	}
	if a.requests() != 2 {
		t.Fatalf("requests = %d, want 2", a.requests())
	}
	for i := range a.keys {
		if a.keys[i] != "evt_01TESTEVENT" || a.bodyIDs[i] != "evt_01TESTEVENT" {
			t.Errorf("attempt %d carried key %q and body id %q", i+1, a.keys[i], a.bodyIDs[i])
		}
	}
}

// Which answers are tried again at once, and which a later send could get past.
// A redirect is an answer, because the client refuses to follow one, and a fixed
// URL can still deliver it.
func TestWhatIsRetriedAndWhatIsResendable(t *testing.T) {
	for _, tc := range []struct {
		status     int
		requests   int
		resendable bool
	}{
		{http.StatusInternalServerError, 2, true},
		{http.StatusBadGateway, 2, true},
		{http.StatusTooManyRequests, 2, true},
		{http.StatusRequestTimeout, 2, true},
		{http.StatusFound, 1, true},
		{http.StatusUnauthorized, 1, true},
		{http.StatusForbidden, 1, true},
		{http.StatusNotFound, 1, true},
		{http.StatusBadRequest, 1, false},
		{http.StatusUnprocessableEntity, 1, false},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			a := &answering{statuses: []int{tc.status}}
			err := notifierFor(t, a).Notify(context.Background(), identified())
			if a.requests() != tc.requests {
				t.Errorf("requests = %d, want %d", a.requests(), tc.requests)
			}
			if got := deliveryError(t, err).Resendable; got != tc.resendable {
				t.Errorf("resendable = %t, want %t", got, tc.resendable)
			}
		})
	}
}

type unreachable struct {
	mu    sync.Mutex
	calls int
}

func (u *unreachable) RoundTrip(*http.Request) (*http.Response, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls++
	return nil, errors.New("connection refused")
}

// No answer at all is retried and resendable: the target may be back in a
// second, or at the next send.
func TestAnUnreachableTargetIsRetriedOnce(t *testing.T) {
	transport := &unreachable{}
	n, err := webhook.New(webhook.Options{
		Name: "chat", URL: "https://hooks.example/x",
		Client: &http.Client{Transport: transport}, RetryAfter: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !deliveryError(t, n.Notify(context.Background(), identified())).Resendable {
		t.Error("an unreachable target was reported final")
	}
	if transport.calls != 2 {
		t.Errorf("attempts = %d, want 2", transport.calls)
	}
}

// The retry waits inside the caller's deadline and never past it, so the
// longest an operation can wait is unchanged.
func TestTheRetryKeepsInsideTheDeadline(t *testing.T) {
	a := &answering{statuses: []int{http.StatusServiceUnavailable, http.StatusOK}}
	srv := httptest.NewTLSServer(a)
	defer srv.Close()
	n, err := webhook.New(webhook.Options{
		Name: "chat", URL: srv.URL, Client: srv.Client(), RetryAfter: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = n.Notify(ctx, identified())
	if waited := time.Since(start); waited > 2*time.Second {
		t.Fatalf("Notify waited %s past a 200ms deadline", waited)
	}
	if !deliveryError(t, err).Resendable {
		t.Error("a 503 cut short by the deadline was reported final")
	}
	if a.requests() != 1 {
		t.Errorf("requests = %d, want 1", a.requests())
	}
}
