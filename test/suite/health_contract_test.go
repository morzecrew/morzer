package suite

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/adapters/health"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/contract"
	"github.com/morzecrew/morzer/test/fakes"
)

// The HealthWaiter is what decides an update finished. Its port promises one
// sentence -- "polls until every check passes or the context expires" -- which
// the real waiter kept and the fake did not: it probed once and gave up in a
// microsecond. Every operation test that drove a product to "never healthy" was
// therefore exercising something no implementation does.

func TestHealthWaiterContract_Fake(t *testing.T) {
	contract.RunHealthWaiterSuite(t, func(t *testing.T, s contract.HealthScenario) (ports.HealthWaiter, []ports.CheckSpec) {
		h := fakes.NewHealth()
		// Patience stays zero: this is the honest configuration, and a
		// suite run against a shortened one would prove nothing about
		// the fake the operation tests actually use.
		h.Interval = 5 * time.Millisecond

		switch s {
		case contract.HealthPasses:
			h.Healthy = true
		case contract.HealthPassesEventually:
			h.PassAfter = 3
		case contract.HealthNeverPasses:
			h.Healthy = false
		}

		return h, []ports.CheckSpec{
			{Check: domain.HealthCheck{Name: "api", Type: domain.HealthHTTP}},
		}
	})
}

// TestHealthWaiterContract_HTTP runs the same suite against the real waiter and
// the real HTTP prober, over a server that answers the way a service coming up
// answers. Hermetic: httptest, no Docker, no sleeps beyond the poll interval.
func TestHealthWaiterContract_HTTP(t *testing.T) {
	contract.RunHealthWaiterSuite(t, func(t *testing.T, s contract.HealthScenario) (ports.HealthWaiter, []ports.CheckSpec) {
		var requests atomic.Int32

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := requests.Add(1)
			switch {
			case s == contract.HealthNeverPasses:
				w.WriteHeader(http.StatusServiceUnavailable)
			case s == contract.HealthPassesEventually && n < 3:
				// What a service that is still starting says.
				w.WriteHeader(http.StatusServiceUnavailable)
			default:
				w.WriteHeader(http.StatusOK)
			}
			fmt.Fprintln(w, "probe", n)
		}))
		t.Cleanup(srv.Close)

		waiter := health.NewWaiter(health.NewHTTP()).WithInterval(5 * time.Millisecond)

		return waiter, []ports.CheckSpec{
			{Check: domain.HealthCheck{
				Name: "api",
				Type: domain.HealthHTTP,
				URL:  srv.URL + "/healthz",
			}},
		}
	})
}
