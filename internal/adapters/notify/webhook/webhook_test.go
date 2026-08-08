package webhook_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/adapters/notify/webhook"
	"github.com/morzecrew/morzer/internal/events"
)

// TestPlaintextIsRefusedWhenConfigured.
//
// At construction rather than at delivery: an operator who configured a
// plaintext endpoint should find out when they configure it, not during the
// incident the notification exists for.
func TestPlaintextIsRefusedWhenConfigured(t *testing.T) {
	_, err := webhook.New(webhook.Options{URL: "http://hooks.example/T000/B111/secret"})
	if err == nil {
		t.Fatal("a plaintext notify target must be refused")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("the refusal should name the requirement: %v", err)
	}
	// A plaintext URL can still be a credential-in-a-path, so the message
	// must not quote the whole thing back.
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("the refused URL's path leaked into the message: %v", err)
	}
}

// TestADownEndpointDoesNotFailTheOperation is the port's central promise, and
// it was untestable until an adapter existed to fail.
func TestADownEndpointDoesNotFailTheOperation(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "computer says no", http.StatusInternalServerError)
	}))
	defer srv.Close()

	n, err := webhook.New(webhook.Options{URL: srv.URL, Client: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}

	// Notify reports the failure to its caller, which logs and drops it --
	// ops.notify is where the "never changes the outcome" half lives. What
	// matters here is that it returns rather than panicking or blocking.
	if err := n.Notify(context.Background(), finished()); err == nil {
		t.Error("a 500 should be reported to the caller")
	}
}

// TestMinLevelGatesChecksInBothDirections.
//
// Both halves. With only the first, a target that dropped everything would
// pass; with only the second, so would one that forwarded everything and made
// the default meaningless.
func TestMinLevelGatesChecksInBothDirections(t *testing.T) {
	cases := []struct {
		name     string
		minLevel string
		level    events.Level
		want     bool
	}{
		{"default drops a warning", "", events.LevelWarn, false},
		{"default takes an error", "", events.LevelError, true},
		{"warn takes a warning", "warn", events.LevelWarn, true},
		{"warn takes an error", "warn", events.LevelError, true},
		{"info is never forwarded", "warn", events.LevelInfo, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got atomic.Int32
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got.Add(1)
			}))
			defer srv.Close()

			n, err := webhook.New(webhook.Options{
				URL: srv.URL, Client: srv.Client(), MinLevel: tc.minLevel,
			})
			if err != nil {
				t.Fatal(err)
			}

			ev := events.Event{Kind: events.KindCheck, Level: tc.level}
			if err := n.Notify(context.Background(), ev); err != nil {
				t.Fatalf("delivery failed: %v", err)
			}

			delivered := got.Load() == 1
			if delivered != tc.want {
				t.Errorf("delivered = %t, want %t", delivered, tc.want)
			}
		})
	}
}

// TestAnOutcomeIsDeliveredWithItsCredential.
//
// The header comes from the resolved credential rather than from the
// installation file, which is what keeps a token out of every support ticket.
func TestAnOutcomeIsDeliveredWithItsCredential(t *testing.T) {
	type received struct {
		auth string
		body []byte
	}
	got := make(chan received, 1)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- received{auth: r.Header.Get("Authorization"), body: body}
	}))
	defer srv.Close()

	n, err := webhook.New(webhook.Options{
		URL: srv.URL, Client: srv.Client(),
		Header: "Authorization", Value: "Bearer hunter2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Notify(context.Background(), finished()); err != nil {
		t.Fatal(err)
	}

	select {
	case r := <-got:
		if r.auth != "Bearer hunter2" {
			t.Errorf("Authorization = %q", r.auth)
		}
		var ev events.Event
		if err := json.Unmarshal(r.body, &ev); err != nil {
			t.Fatalf("the payload is not an event: %v", err)
		}
		if ev.Kind != events.KindOperationFinished {
			t.Errorf("kind = %q", ev.Kind)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was delivered")
	}
}

// TestTheNameNeverLeaksTheURL.
//
// Name() reaches log lines, and a target configured through url_secret has a
// URL that *is* the credential. The one place a failing notifier is named is
// exactly where it would surface.
func TestTheNameNeverLeaksTheURL(t *testing.T) {
	n, err := webhook.New(webhook.Options{
		Name: "chat",
		URL:  "https://hooks.example/services/T000/B111/XXXXsecretXXXX",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(n.Name(), "secret") || strings.Contains(n.Name(), "hooks.example") {
		t.Errorf("Name() leaks the endpoint: %q", n.Name())
	}
}

func finished() events.Event {
	return events.Event{Kind: events.KindOperationFinished, Status: "succeeded"}
}

// TestARedirectDoesNotCarryTheHeaderToPlaintext.
//
// New requires https, and a redirect would undo that: an endpoint answering 302
// with an http:// location would receive the payload *and* the configured
// authentication header in the clear.
//
// The client here is the httptest server's own, which has no redirect policy of
// its own -- so this fails unless New installs one regardless of who built the
// client.
func TestARedirectDoesNotCarryTheHeaderToPlaintext(t *testing.T) {
	var leaked atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Add(1)
		}
	}))
	defer plain.Close()

	redirector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL, http.StatusFound)
	}))
	defer redirector.Close()

	client := redirector.Client()
	if client.CheckRedirect != nil {
		t.Fatal("the fixture must start with no redirect policy, or this proves nothing")
	}

	n, err := webhook.New(webhook.Options{
		URL: redirector.URL, Client: client,
		Header: "Authorization", Value: "Bearer hunter2",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = n.Notify(context.Background(), finished())

	if leaked.Load() != 0 {
		t.Error("the authentication header reached a plaintext endpoint through a redirect")
	}
}

// TestAMalformedSecretURLIsNotEchoed.
//
// When the URL came from url_secret, a malformed value is a bare credential.
// The refusal is logged by the caller, so echoing the value would put the
// secret in the log line that reports the dropped target.
func TestAMalformedSecretURLIsNotEchoed(t *testing.T) {
	_, err := webhook.New(webhook.Options{URL: "xoxb-9999-super-secret-token"})
	if err == nil {
		t.Fatal("a value that is not a URL must be refused")
	}
	if strings.Contains(err.Error(), "xoxb") || strings.Contains(err.Error(), "secret-token") {
		t.Errorf("the refusal echoed the credential: %v", err)
	}
}
