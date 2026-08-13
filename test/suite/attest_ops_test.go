package suite

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// The rest of RFC 0025 §4.1's five operations, and the parts of `--against-live`
// that are about configuration rather than images.

// renderedConfigPath is where the fixture release renders its one config file.
func renderedConfigPath(h *harness) string {
	return filepath.Join(h.Root, "etc", "demo", "application.yaml")
}

// A configuration edited in place is the other half of what RFC 0025 §4.7
// promises `--against-live` catches, and it is the commoner hand-edit: opening
// the file the manager rendered is easier than pulling a different image.
//
// The digest is salted per installation (decision 4), so this can establish
// *that* the configuration differs and never how -- which is the trade the
// design made, and the reason the check has to exist rather than being replaced
// by a diff.
func TestAgainstLiveNoticesAConfigurationEditedByHand(t *testing.T) {
	h := verifyHarness(t)
	attested(t, h)

	// Clean first, or a failure below would prove nothing about the edit.
	clean, err := ops.AttestVerify(context.Background(), h.Deps,
		ops.VerifyOptions{AgainstLive: true})
	require.NoError(t, err)
	require.Empty(t, clean.Live,
		"the configuration the manager just rendered does not match its own statement")

	path := renderedConfigPath(h)
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(body, []byte("\ndebug: true\n")...), 0o640))

	report, err := ops.AttestVerify(context.Background(), h.Deps,
		ops.VerifyOptions{AgainstLive: true})
	require.NoError(t, err)

	require.NotEmpty(t, report.Live, "a configuration edited by hand went unnoticed")
	assert.Equal(t, "config", report.Live[0].Kind)
	assert.Positive(t, report.Problems())
}

// And the other direction: the configuration removed entirely is a different
// sentence, because it sends an operator somewhere else.
func TestAgainstLiveDistinguishesConfigurationThatIsGone(t *testing.T) {
	h := verifyHarness(t)
	attested(t, h)

	require.NoError(t, os.Remove(renderedConfigPath(h)))

	report, err := ops.AttestVerify(context.Background(), h.Deps,
		ops.VerifyOptions{AgainstLive: true})
	require.NoError(t, err)

	require.NotEmpty(t, report.Live)
	assert.Equal(t, "config", report.Live[0].Kind)
	assert.Contains(t, report.Live[0].Detail, "not on disk")
}

// `config` is the fifth of RFC 0025 §4.1's operations, and the one an audit
// reads most often: a parameter change alters what the deployment is without
// moving a version, so nothing else in the record shows it happened.
func TestAConfigChangeFilesAStatement(t *testing.T) {
	h := verifyHarness(t)
	attested(t, h)

	before := len(statementsOnDisk(t, h))

	_, err := ops.ConfigSet(context.Background(), h.Deps, ops.ConfigSetOptions{
		Set: map[string]string{"http_port": "9000"},
	})
	require.NoError(t, err)

	files := statementsOnDisk(t, h)
	require.Greater(t, len(files), before, "the config change filed no statement")

	var found bool
	for _, file := range files {
		body, err := os.ReadFile(file)
		require.NoError(t, err)
		var stmt domain.Statement
		require.NoError(t, json.Unmarshal(body, &stmt))
		if stmt.Predicate.Operation.Kind != string(domain.OpTypeConfig) {
			continue
		}
		found = true

		// Names, never values (decision 4). A statement that carried
		// `http_port: 9000` would publish a port to whoever the
		// document travels to.
		assert.Contains(t, stmt.Predicate.Config.ParameterNames, "http_port")
		assert.NotContains(t, string(body), "9000")

		// It moves no version, so it joins no chain -- exactly like an
		// apply, and for the same reason.
		assert.Empty(t, stmt.Predicate.Release.FromVersion)
	}
	assert.True(t, found, "no statement records the config operation")
}

// A refusal is not an operation, and must not leave a record of one.
//
// The lock is the path that reaches it. Every emission is placed *before* its
// operation returns an error, deliberately, so that failures are attested as
// well as successes -- and an operation the lock turned away never ran, so the
// record reaching the emission path carries no id at all. The statement went to
// `<attestations>/.json`, naming no operation, no kind and no outcome, and
// `attest verify` read it back as a statement because it is one. Every refusal
// overwrote the last.
//
// A busy lock is the routine way in: a systemd timer firing during a manual
// update hits it, so this is a file that would appear on machines nobody was
// doing anything unusual to.
func TestARefusedOperationFilesNoStatement(t *testing.T) {
	h := verifyHarness(t)
	attested(t, h)
	ctx := context.Background()

	before := statementsOnDisk(t, h)

	h.Locker.FailAcquire = true
	_, err := ops.Apply(ctx, h.Deps, ops.Options{})
	require.Error(t, err, "two operations ran against one installation at once")
	require.Equal(t, domain.CodeLocked, domain.AsError(err).Code)

	after := statementsOnDisk(t, h)
	assert.Equal(t, before, after, "an operation the lock refused filed a statement")
	for _, file := range after {
		assert.NotEqual(t, ".json", filepath.Base(file),
			"a statement was filed for an operation with no id")
	}
}

// `attest log` reads the record back without asking whether to believe it.
func TestAttestLogListsTheRecordNewestFirst(t *testing.T) {
	h := verifyHarness(t)
	attested(t, h)

	// The clock moves, because ordering by the time an operation started is
	// the claim under test and the harness's clock is otherwise frozen.
	// Without this the two statements share a timestamp, ids are ULIDs with
	// a random tail, and the assertion would be about the tiebreaker rather
	// than about the history.
	later := h.Deps.Now().Add(time.Hour)
	h.Deps.Now = func() time.Time { return later }

	_, err := ops.ConfigSet(context.Background(), h.Deps, ops.ConfigSetOptions{
		Set: map[string]string{"http_port": "9000"},
	})
	require.NoError(t, err)

	entries, err := ops.AttestLog(context.Background(), h.Deps, ops.VerifyOptions{})
	require.NoError(t, err)
	require.Len(t, entries, 2)

	assert.Equal(t, string(domain.OpTypeConfig), entries[0].Kind,
		"the newest operation must come first")
	assert.Equal(t, string(domain.OpTypeApply), entries[1].Kind)
	for _, e := range entries {
		assert.True(t, e.Signed, "this machine has a key, so its statements are signed")
		assert.Equal(t, string(domain.StatusSucceeded), e.Outcome)
	}
}
