package suite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// `support redact --check` answers about a file the operator holds.
//
// RFC 0024 decision 7 grades this LOCKED and says it ships alongside the
// bundle, which the phasing section contradicts by listing it as P5. The self-
// audit resolved that in the LOCKED row's favour: the archive is safe by
// construction and the thing somebody pastes into a chat window is not.
func TestRedactCheckFindsASecretInAFileTheOperatorHolds(t *testing.T) {
	h := newHarness(t)
	h.install()
	holdSecret(h)

	paste := filepath.Join(t.TempDir(), "paste.txt")
	require.NoError(t, os.WriteFile(paste,
		[]byte("DB_URL=postgres://app:"+leaked+"@db/app\nand again "+leaked+"\n"), 0o600))

	report, err := ops.SupportRedactCheck(context.Background(), h.Deps, paste)
	require.NoError(t, err)
	require.True(t, report.Armed)
	require.Equal(t, 2, report.Redactions)

	// And it changed nothing: the operator was about to send this file, and
	// a check that rewrote it would have destroyed the evidence.
	after, err := os.ReadFile(paste)
	require.NoError(t, err)
	require.Contains(t, string(after), leaked)
}

// A file with nothing in it reports zero, and the report says the check ran.
//
// `Armed` is what separates "checked and found nothing" from "never checked",
// and a caller reading the count alone cannot tell them apart -- which is the
// reading that sends somebody to paste a file with confidence.
func TestRedactCheckSeparatesCleanFromUnchecked(t *testing.T) {
	h := newHarness(t)
	h.install()
	holdSecret(h)

	paste := filepath.Join(t.TempDir(), "clean.txt")
	require.NoError(t, os.WriteFile(paste, []byte("nothing to see here\n"), 0o600))

	clean, err := ops.SupportRedactCheck(context.Background(), h.Deps, paste)
	require.NoError(t, err)
	require.True(t, clean.Armed)
	require.Zero(t, clean.Redactions)

	h.Secrets.Fail = map[string]error{"Load": errors.New("the sops key is missing")}
	unchecked, err := ops.SupportRedactCheck(context.Background(), h.Deps, paste)
	require.NoError(t, err)
	require.False(t, unchecked.Armed,
		"an unarmed check reports zero redactions and must not also report that it ran")
	require.Zero(t, unchecked.Redactions)
}

// A directory and an oversized file are refused rather than read.
func TestRedactCheckRefusesWhatItCannotHonestlyAnswer(t *testing.T) {
	h := newHarness(t)
	h.install()

	dir := t.TempDir()
	_, err := ops.SupportRedactCheck(context.Background(), h.Deps, dir)
	require.Error(t, err)
	require.Contains(t, err.Error(), "directory")

	_, err = ops.SupportRedactCheck(context.Background(), h.Deps,
		filepath.Join(dir, "nothing-here.txt"))
	require.Error(t, err)
}

// A deployment that wrote nothing is an answer, not a missing file.
//
// Every other component states its gap in meta.json, and logs are the one place
// a missing file is most suspicious -- so "there was no output" has to be said
// rather than left as an absence a reader has to interpret.
func TestADeploymentThatLoggedNothingSaysSoRatherThanGoingQuiet(t *testing.T) {
	h := newHarness(t)
	h.install()
	holdSecret(h)
	h.Runtime.LogOutput = ""

	report, err := ops.SupportBundle(context.Background(), h.Deps,
		ops.SupportOptions{Dir: t.TempDir()})
	require.NoError(t, err)

	var reason string
	for _, o := range report.Omitted {
		if o.Name == "logs/" {
			reason = o.Reason
		}
	}
	require.NotEmpty(t, reason,
		"no log files and no explanation: a reader cannot tell an empty deployment "+
			"from a component that failed")
	require.Contains(t, reason, "no log output")
}

// manager.json carries the build, not only the version.
//
// It is the archive's statement about which redaction logic ran (§12 A2), and
// "1.0.0" cannot distinguish a release binary from a patched host.
func TestTheArchiveNamesTheBuildThatWroteIt(t *testing.T) {
	h := newHarness(t)
	h.install()

	report, err := ops.SupportBundle(context.Background(), h.Deps, ops.SupportOptions{
		Dir:   t.TempDir(),
		Build: ops.SupportBuild{Commit: "abc1234", Date: "2026-08-13T17:00:00Z"},
	})
	require.NoError(t, err)

	manager := archiveEntries(t, report.Path)["manager.json"]
	require.Contains(t, manager, "abc1234")
	require.Contains(t, manager, "2026-08-13T17:00:00Z")
	require.Contains(t, manager, "1.0.0")
}

// A file past the bound is refused rather than read whole.
//
// The file is an operator's own paste, so its size is not something the manager
// controls, and reading it entire is what makes the answer exact -- a secret
// split across a chunk boundary is the case a streaming reader gets wrong. The
// bound is what keeps "exact" from meaning "read whatever you are pointed at".
//
// Sparse, so the test costs an inode rather than 32MiB.
func TestRedactCheckRefusesAFilePastTheBound(t *testing.T) {
	h := newHarness(t)
	h.install()

	huge := filepath.Join(t.TempDir(), "enormous.log")
	f, err := os.Create(huge)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(33<<20))
	require.NoError(t, f.Close())

	_, err = ops.SupportRedactCheck(context.Background(), h.Deps, huge)
	require.Error(t, err)
	require.Contains(t, err.Error(), "past the")
}

// A service louder than the byte bound is cut, and the file says so at the top.
//
// At the top because somebody opening it reads the first line before deciding
// the incident started there. The banner names the byte bound alone: the line
// bound is applied by the runtime as a tail, so a stream that hit *that* arrives
// already short and this code cannot tell it from a service that said less.
func TestALoudServiceIsTruncatedAndTheFileSaysSo(t *testing.T) {
	h := newHarness(t)
	h.install()
	holdSecret(h)

	var b strings.Builder
	line := strings.Repeat("x", 512)
	for b.Len() < (2 << 20) {
		b.WriteString("web-1| " + line + "\n")
	}
	h.Runtime.LogOutput = b.String()

	report, err := ops.SupportBundle(context.Background(), h.Deps,
		ops.SupportOptions{Dir: t.TempDir()})
	require.NoError(t, err)

	var captured string
	for name, body := range archiveEntries(t, report.Path) {
		if strings.HasPrefix(name, "logs/") {
			captured = body
		}
	}
	require.NotEmpty(t, captured, "no log file was produced")
	require.Less(t, len(captured), 2<<20, "the byte bound did not cut anything")
	require.True(t, strings.HasPrefix(captured, "[truncated"),
		"the truncation is not announced at the top of the file: %.80s", captured)
	require.Contains(t, captured, "bytes")
}
