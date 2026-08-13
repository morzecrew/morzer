package suite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// Redaction under test (RFC 0024 P3).
//
// This is the phase that decides whether the feature is safe, and §9 says why
// the tests are here rather than left to the redactor's own suite: the redactor
// is tested against the routes a *log line* takes, and this archive is built
// from files written months earlier by code paths nobody was watching.
//
// The secret shapes below are 0008's, the ones the handler was caught missing:
// a `fmt.Stringer`, a struct, an `error` wrapping a value. They are driven
// through the journal rather than through a logger, because the journal is what
// the archive carries and the journal is written by a different path.

const leaked = "s3cr3t-p4ssw0rd-value-that-must-not-travel"

// stringerSecret is 0008's first shape: a value whose String method is the only
// way its contents ever appear.
type stringerSecret struct{ value string }

func (s stringerSecret) String() string { return "conn=" + s.value }

// structSecret is the second: a struct with no String method at all, which
// reaches a log line through %v and one level of reflection.
type structSecret struct {
	User     string
	Password string
}

// Every shape 0008 found, seeded into the journal and then collected.
//
// The journal is the sharp case for this feature. `logging`'s own
// `TestRegisteringAfterWithIsAKnownLimit` pins that the handler redacts at
// capture time and keeps the clear copy -- so a message written before its
// secret was registered is on disk in the clear, and no amount of correctness in
// the handler helps an archive assembled a month later. The bundle answers by
// scrubbing at collection time, and this is what says so.
func TestEverySecretShapeIsScrubbedOnItsWayIntoTheArchive(t *testing.T) {
	shapes := map[string]string{
		"a bare string": leaked,
		"a Stringer":    stringerSecret{value: leaked}.String(),
		"a struct rendered by %v": fmt.Sprintf("%v",
			structSecret{User: "app", Password: leaked}),
		"an error wrapping a value": fmt.Errorf(
			"connect: %w", errors.New("auth failed for "+leaked)).Error(),
	}

	for name, text := range shapes {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.install()
			holdSecret(h)

			// Written before the redactor has ever heard of the
			// value, which is the ordinary case: the journal
			// outlives every process that appended to it.
			seedJournal(t, h, text)

			report, err := ops.SupportBundle(context.Background(), h.Deps,
				ops.SupportOptions{Dir: t.TempDir()})
			require.NoError(t, err)

			journal := archiveEntries(t, report.Path)["journal.jsonl"]
			require.NotEmpty(t, journal, "the journal is not in the archive")
			require.NotContainsf(t, journal, leaked,
				"a secret reached the archive through %s", name)
			require.Contains(t, journal, domain.Redacted,
				"the value is gone with no marker, so it may have been dropped "+
					"rather than scrubbed")
		})
	}
}

// The count in meta.json is the number a reviewer looks at first, so it has to
// be a count of what was removed rather than a flag.
func TestTheRedactionCountSaysHowMuchWasRemoved(t *testing.T) {
	h := newHarness(t)
	h.install()
	holdSecret(h)

	// Twice in one message: an operator reading "1" where two copies had to
	// go would believe they had seen the extent of it.
	seedJournal(t, h, "retrying "+leaked+" then "+leaked)

	report, err := ops.SupportBundle(context.Background(), h.Deps,
		ops.SupportOptions{Dir: t.TempDir()})
	require.NoError(t, err)

	var journal ops.SupportEntry
	for _, e := range report.Entries {
		if e.Name == "journal.jsonl" {
			journal = e
		}
	}
	require.Equal(t, 2, journal.Redactions)

	// And the archive's own index carries the same number, because a
	// recipient has the file and not the terminal output.
	var meta struct {
		Entries []ops.SupportEntry `json:"entries"`
	}
	require.NoError(t, json.Unmarshal(
		[]byte(archiveEntries(t, report.Path)["meta.json"]), &meta))

	found := false
	for _, e := range meta.Entries {
		if e.Name == "journal.jsonl" {
			require.Equal(t, 2, e.Redactions)
			found = true
		}
	}
	require.True(t, found, "meta.json does not mention the journal")
}

// Container logs are collected, per service, and scrubbed.
func TestContainerLogsAreCollectedPerServiceAndScrubbed(t *testing.T) {
	h := newHarness(t)
	h.install()
	holdSecret(h)

	h.Runtime.LogOutput = "web-1| starting up\n" +
		"web-1| connecting with " + leaked + "\n" +
		"db-1| ready to accept connections\n"

	report, err := ops.SupportBundle(context.Background(), h.Deps,
		ops.SupportOptions{Dir: t.TempDir()})
	require.NoError(t, err)

	entries := archiveEntries(t, report.Path)

	var logFiles []string
	for name := range entries {
		if strings.HasPrefix(name, "logs/") {
			logFiles = append(logFiles, name)
			require.NotContainsf(t, entries[name], leaked,
				"%s carries an unredacted secret", name)
		}
	}
	require.NotEmpty(t, logFiles, "no container logs were collected")
}

// Logs are omitted entirely when the secret values cannot be loaded.
//
// This is where the archive and `morzer logs` part company. 0021 prints an
// unfiltered stream and says so, because an operator reading their own terminal
// can decide what to do with what they see. This artifact is read by somebody
// else, later, who has the file and the count in `meta.json` and nothing else --
// and decision 5 already refuses a flag that turns redaction off, so shipping
// unredactable bytes by default would be that flag with no way to turn it off.
//
// Everything else still ships: one component that cannot be made safe must not
// cost an operator the archive.
func TestUnredactableLogsAreOmittedRatherThanShipped(t *testing.T) {
	h := newHarness(t)
	h.install()

	h.Runtime.LogOutput = "web-1| connecting with " + leaked + "\n"
	h.Secrets.Fail = map[string]error{"Load": errors.New("the sops key is missing")}

	report, err := ops.SupportBundle(context.Background(), h.Deps,
		ops.SupportOptions{Dir: t.TempDir()})
	require.NoError(t, err, "one unsafe component must not cost the whole archive")

	entries := archiveEntries(t, report.Path)
	for name, body := range entries {
		require.NotContainsf(t, body, leaked, "%s carries an unredacted secret", name)
		require.NotContainsf(t, name, "logs/",
			"logs were collected although nothing could be scrubbed from them")
	}

	var omitted []string
	for _, o := range report.Omitted {
		omitted = append(omitted, o.Name)
	}
	require.Contains(t, omitted, "logs/")
	require.Contains(t, entries, "journal.jsonl", "the rest of the archive is missing")
}

// holdSecret makes the leaked value one of this installation's own secrets.
//
// Without it these tests would assert nothing: the redactor scrubs what it has
// been told about, and a value no installation holds is a value it has no
// reason to know. The scenario being tested is the real one -- the journal
// recorded a secret this machine still has -- rather than "the redactor removes
// arbitrary strings", which it does not and should not.
func holdSecret(h *harness) {
	h.Secrets.Seed(map[string]string{"db_password": leaked})
}

// seedJournal appends one operation record carrying text in a step message.
//
// Through the state store rather than through a logger, because that is the
// path the journal is actually written by, and a test that seeded it through
// slog would be asserting against a file the product does not produce.
func seedJournal(t *testing.T, h *harness, text string) {
	t.Helper()

	require.NoError(t, h.Deps.State.AppendOperation(context.Background(), domain.OperationRecord{
		ID:        "op_01SEEDEDFORREDACTION",
		Type:      domain.OpTypeApply,
		Status:    domain.StatusFailed,
		StartedAt: domain.NewTime(h.Deps.Now()),
		Steps: []domain.StepRecord{{
			ID:      "start-services",
			Status:  domain.StepFailed,
			Message: text,
		}},
	}))
}

// `--no-logs` removes the component and says it did.
//
// The distinction from decision 5's refused `--raw` is worth keeping straight:
// this removes a component, `--raw` would have removed the filter from one. An
// operator who knows their product logs request bodies knows something the
// manager cannot, and every value of this flag is safe.
func TestNoLogsRemovesTheComponentAndRecordsIt(t *testing.T) {
	h := newHarness(t)
	h.install()
	holdSecret(h)
	h.Runtime.LogOutput = "web-1| starting up\n"

	report, err := ops.SupportBundle(context.Background(), h.Deps,
		ops.SupportOptions{Dir: t.TempDir(), NoLogs: true})
	require.NoError(t, err)

	for name := range archiveEntries(t, report.Path) {
		require.NotContains(t, name, "logs/", "--no-logs still collected logs")
	}

	var reasons []string
	for _, o := range report.Omitted {
		if o.Name == "logs/" {
			reasons = append(reasons, o.Reason)
		}
	}
	require.Len(t, reasons, 1, "the archive does not record that logs were left out")
	require.Contains(t, reasons[0], "--no-logs")
}
