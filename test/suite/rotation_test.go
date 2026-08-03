package suite

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// The example bundle declares a rotation period on db_password (2160h, ninety
// days) and none on the others. That asymmetry is what these exercise: a period
// is the release author's recommendation, and a secret without one is a secret
// nobody has an opinion about.

// doctorCheck runs doctor and returns one result by id.
func doctorCheck(t *testing.T, h *harness, id string) events.CheckResult {
	t.Helper()

	report, err := ops.Doctor(context.Background(), h.Deps)
	require.NoError(t, err)

	for _, r := range report.Results {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("doctor ran no check with id %q", id)
	return events.CheckResult{}
}

// ageSecret backdates a secret's last-changed timestamp.
func ageSecret(h *harness, name string, age time.Duration) {
	h.Secrets.SetChanged(name, h.Deps.Now().Add(-age))
}

func TestRotationIsReportedOnlyPastThePeriod(t *testing.T) {
	const period = 2160 * time.Hour // what the example bundle declares

	cases := []struct {
		name   string
		age    time.Duration
		status events.CheckStatus
	}{
		{"fresh", time.Hour, events.CheckOK},
		{"one hour inside the period", period - time.Hour, events.CheckOK},
		// The boundary is `age > period`, so exactly at it is still
		// within. A check that fired on the ninetieth day would be
		// telling an operator they are late on the day they are due.
		{"exactly at the period", period, events.CheckOK},
		{"one hour past", period + time.Hour, events.CheckWarn},
		{"long past", period * 3, events.CheckWarn},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.install()
			h.Secrets.Seed(map[string]string{
				"db_password": "value", "session_key": "value",
			})
			ageSecret(h, "db_password", tc.age)

			result := doctorCheck(t, h, "secrets.rotation")
			assert.Equal(t, tc.status, result.Status, "%s: %s", tc.name, result.Message)
		})
	}
}

func TestRotationNeverFailsDoctor(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.Secrets.Seed(map[string]string{"db_password": "value", "session_key": "value"})
	ageSecret(h, "db_password", 10000*time.Hour)

	// A warning, never a failure. Asserted on the check rather than on the
	// whole report, because the harness has other checks failing for
	// unrelated reasons -- no containers are running -- and a test that
	// depended on all of them passing would be testing the harness.
	result := doctorCheck(t, h, "secrets.rotation")
	assert.Equal(t, events.CheckWarn, result.Status)
	assert.NotEqual(t, events.CheckFail, result.Status)

	// And the property that makes that meaningful: a run whose worst result
	// is a warning exits zero. `doctor`'s exit code is what a monitoring
	// system pages on, and paging over a release author's recommendation is
	// how a team learns to ignore the whole signal.
	warned := ops.DoctorReport{Worst: events.CheckWarn}
	assert.Equal(t, domain.ExitSuccess, warned.ExitCode())

	failed := ops.DoctorReport{Worst: events.CheckFail}
	assert.Equal(t, domain.ExitPreflight, failed.ExitCode())
}

func TestRotationSaysNothingAboutSecretsWithNoDeclaredPeriod(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.Secrets.Seed(map[string]string{"db_password": "value", "session_key": "value"})

	// session_key has no rotation_period in the bundle. Ageing it a decade
	// must produce no opinion at all: inventing a default policy for a
	// vendor who declined to state one would be inventing their opinion.
	ageSecret(h, "session_key", 90000*time.Hour)

	result := doctorCheck(t, h, "secrets.rotation")
	assert.Equal(t, events.CheckOK, result.Status)
	assert.NotContains(t, result.Message, "session_key")
}

func TestRotationRemedyNamesTheCommandThatWorks(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.Secrets.Seed(map[string]string{"db_password": "value", "session_key": "value"})
	ageSecret(h, "db_password", 10000*time.Hour)

	result := doctorCheck(t, h, "secrets.rotation")
	require.Equal(t, events.CheckWarn, result.Status)

	// db_password declares a password generator, so `rotate` can produce a
	// new value. For a secret with no generator the remedy is `secret set`,
	// and pointing at `rotate` there would be pointing at a command that
	// fails.
	assert.Contains(t, result.Remedy, "morzer secret rotate db_password")
}

// TestSecretsStorageCheckIsCoherentEitherWay asserts what holds on every
// machine, rather than what happens to hold on this one.
//
// Whether a temp directory is tmpfs varies by host -- it is on most Linux
// desktops and is not on a GitHub runner's workspace -- so a test that expected
// one answer would pass or fail by accident. What must hold everywhere is that
// the check never refuses to operate, and that when it does warn it says what
// to do.
func TestSecretsStorageCheckIsCoherentEitherWay(t *testing.T) {
	h := newHarness(t)
	h.install()

	result := doctorCheck(t, h, "secrets.ephemeral-storage")

	require.NotEqual(t, events.CheckFail, result.Status,
		"a container with no tmpfs mounted is a legitimate way to run this; "+
			"refusing to operate would help nobody")

	if result.Status == events.CheckWarn {
		assert.Contains(t, result.Message, "not tmpfs")
		assert.Contains(t, result.Remedy, "tmpfs")
	}
	t.Logf("%s: %s", result.Status, result.Message)
}
