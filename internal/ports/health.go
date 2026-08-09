package ports

import (
	"context"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
)

// HealthProber runs the health checks a release declares. It is separate from
// Runtime because a container reporting "running" and an application
// reporting "ready" are different claims, and `apply` needs the second one.
type HealthProber interface {
	// Type is the check type this prober handles: http, tcp, command.
	Type() domain.HealthCheckType

	// Check runs one probe once. A failing probe is a HealthResult with
	// OK false, not an error; an error means the probe could not be run
	// at all.
	Check(ctx context.Context, spec CheckSpec) (HealthResult, error)
}

// HealthWaiter runs a set of checks. It is a separate interface from
// HealthProber because the polling policy belongs to the lifecycle layer, not
// to each transport.
type HealthWaiter interface {
	// WaitReady polls until every check passes or the context expires.
	// This is what `apply` uses: it wants a desired state.
	WaitReady(ctx context.Context, specs []CheckSpec) ([]HealthResult, error)

	// CheckOnce runs every check exactly once. This is what `status` and
	// `doctor` use: they report the current state rather than waiting for
	// a better one, and a diagnostic that blocks for two minutes waiting
	// for health is a diagnostic nobody runs.
	CheckOnce(ctx context.Context, specs []CheckSpec) ([]HealthResult, error)
}

// CheckSpec is one health check, resolved from the manifest with the release
// root attached so command checks can find their executable.
type CheckSpec struct {
	Check domain.HealthCheck

	// WorkingDir is the release root -- command checks run from there.
	WorkingDir string

	// Env is the hook ABI environment, passed to command checks.
	Env map[string]string
}

func (c CheckSpec) Name() string { return c.Check.Name }

func (c CheckSpec) Timeout() time.Duration {
	return c.Check.Timeout.Or(domain.DefaultHealthTimeout)
}

// StartPeriod is how long this check may keep failing before the failure means
// anything. Zero means the vendor declared none, and the waiter keeps trying
// for as long as the operation allows.
func (c CheckSpec) StartPeriod() time.Duration { return c.Check.StartPeriod.Duration() }

// HealthResult is the outcome of one probe.
type HealthResult struct {
	Name     string        `json:"name"`
	OK       bool          `json:"ok"`
	Message  string        `json:"message,omitempty"`
	Duration time.Duration `json:"-"`

	// Attempts is how many times the probe ran before settling. A check
	// that passed on the twelfth try is worth knowing about.
	Attempts int `json:"attempts,omitempty"`
}
