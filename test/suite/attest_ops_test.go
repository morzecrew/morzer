package suite

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
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
		assert.NotContains(t, withoutDigests(string(body)), "9000",
			"a parameter value reached the attestation")

		// It moves no version, so it joins no chain -- exactly like an
		// apply, and for the same reason.
		assert.Empty(t, stmt.Predicate.Release.FromVersion)
	}
	assert.True(t, found, "no statement records the config operation")
}

// A state-read failure must cost the statement a field, not the statement.
//
// The release record is read for one thing -- the scheme the installed release
// came from. Swallowing the error turned a blip into a silent gap in the record
// of one of the five operations RFC 0025 requires one for, and told nobody.
func TestAConfigChangeIsAttestedEvenWhenTheReleaseRecordCannotBeRead(t *testing.T) {
	h := verifyHarness(t)
	attested(t, h)

	before := len(statementsOnDisk(t, h))

	// The read fails only for the attestation's own lookup, which is the
	// last one the operation makes. Failing every read would refuse the
	// operation at its first step and prove nothing about what happens
	// afterwards; deleting state does not fail the read at all, which is
	// how an earlier version of this test passed while asserting nothing.
	failing := &releaseReadFailsLate{StateStore: h.Deps.State, after: 1}
	h.Deps.State = failing

	_, err := ops.ConfigSet(context.Background(), h.Deps, ops.ConfigSetOptions{
		Set: map[string]string{"http_port": "9000"},
	})
	require.NoError(t, err, "the operation itself must be unaffected")
	require.Positive(t, failing.refused, "the read never failed, so this proves nothing")

	files := statementsOnDisk(t, h)
	require.Greater(t, len(files), before,
		"a state-read failure discarded the statement instead of a field of it")

	// And what it cost is one field, named rather than guessed at.
	//
	// Found by kind rather than by taking the last file: the listing is
	// filename-sorted and ids are ULIDs with a random tail, so "the last
	// one" is not "the one this operation wrote".
	var config domain.Statement
	for _, file := range files {
		body, err := os.ReadFile(file)
		require.NoError(t, err)
		var stmt domain.Statement
		require.NoError(t, json.Unmarshal(body, &stmt))
		if stmt.Predicate.Operation.Kind == string(domain.OpTypeConfig) {
			config = stmt
		}
	}
	require.NotEmpty(t, config.Predicate.Operation.ID, "no statement records the config operation")
	assert.Empty(t, config.Predicate.Release.SourceScheme,
		"the scheme was reported despite the read that would have supplied it failing")
}

// releaseReadFailsLate lets the first n reads of the release record through and
// refuses the rest.
type releaseReadFailsLate struct {
	ports.StateStore
	after   int
	seen    int
	refused int
}

func (s *releaseReadFailsLate) CurrentRelease(ctx context.Context) (domain.ReleaseRecord, error) {
	s.seen++
	if s.seen > s.after {
		s.refused++
		return domain.ReleaseRecord{}, errors.New("the release record cannot be read")
	}
	return s.StateStore.CurrentRelease(ctx)
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
//
// `Signed` says a signature is *there*. Whether it checks out is `attest
// verify`'s answer, and a listing that implied it had checked would be the
// overclaim the whole format is built to refuse.
func TestAttestLogListsWhatEachOperationRecorded(t *testing.T) {
	h := verifyHarness(t)
	attested(t, h)

	_, err := ops.ConfigSet(context.Background(), h.Deps, ops.ConfigSetOptions{
		Set: map[string]string{"http_port": "9000"},
	})
	require.NoError(t, err)

	entries, err := ops.AttestLog(context.Background(), h.Deps, ops.VerifyOptions{})
	require.NoError(t, err)
	require.Len(t, entries, 2)

	kinds := map[string]bool{}
	for _, e := range entries {
		kinds[e.Kind] = true
		assert.True(t, e.Signed, "this machine has a key, so its statements are signed")
		assert.Equal(t, string(domain.StatusSucceeded), e.Outcome)
		assert.NotEmpty(t, e.Operation)
		assert.False(t, e.Started.IsZero())
	}
	assert.True(t, kinds[string(domain.OpTypeApply)])
	assert.True(t, kinds[string(domain.OpTypeConfig)])
}

// Something unparseable among the audit records is itself worth seeing, and it
// goes last.
//
// Reported rather than skipped, because a directory of records containing a
// file that is not one is a finding -- and last, because a listing whose first
// row is a broken file is a listing an operator stops reading. Last falls out
// of the ordering rather than being a case of its own: a file that did not
// parse has no time, and newest-first puts the zero time at the end.
func TestAttestLogReportsAFileThatIsNotAStatement(t *testing.T) {
	h := verifyHarness(t)
	attested(t, h)

	require.NoError(t, os.WriteFile(
		filepath.Join(h.Paths.AttestationsDir(), "aaa-not-a-statement.json"),
		[]byte("{this is not json"), 0o644))

	entries, err := ops.AttestLog(context.Background(), h.Deps, ops.VerifyOptions{})
	require.NoError(t, err)
	require.Len(t, entries, 2)

	assert.Empty(t, entries[0].Unreadable, "a broken file led the listing")
	assert.NotEmpty(t, entries[1].Unreadable,
		"a file that is not a statement was hidden from the record")
}

// Two operations in the same second are still ordered newest first.
//
// Not an edge case: `domain.Time` truncates to the second, so a script that
// applies and then sets a parameter produces exactly this. Left to the
// filename order the pair would print oldest first -- a listing that says
// "newest first" and is backwards inside every second, which is the resolution
// most operators work at.
//
// The refinement is the operation id: ULIDs carry the mint time in their first
// 48 bits, so descending id order is time order at millisecond resolution.
// `--against-live` compares against the *latest* successful statement, and a
// second-resolution tie must not send it to an older one.
//
// `domain.Time` truncates to the second, so an update and the apply that
// follows it share a timestamp. A strict "is after" keeps whichever arrived
// first, and they arrive in filename order — oldest first. The deployment
// would then be checked against a statement a later operation had already
// superseded, and every difference that operation made would be reported as
// though somebody had made it by hand.
func TestAgainstLiveComparesAgainstTheLatestOfATiedPair(t *testing.T) {
	h := verifyHarness(t)
	attested(t, h)

	// A second statement for the same deployment, tied to the first's
	// second and later by id. Both are honest records of what is running,
	// so the comparison must come out clean either way -- what is asserted
	// is *which* one was used.
	existing := statementsOnDisk(t, h)
	require.Len(t, existing, 1)

	body, err := os.ReadFile(existing[0])
	require.NoError(t, err)
	var stmt domain.Statement
	require.NoError(t, json.Unmarshal(body, &stmt))

	// An id above the first's, minted from the same instant.
	later := "op_ZZZZZZZZZZZZZZZZZZZZZZZZZZ"
	stmt.Predicate.Operation.ID = later
	reissued, err := json.MarshalIndent(stmt, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(h.Paths.AttestationsDir(), later+".json"), reissued, 0o644))

	report, err := ops.AttestVerify(context.Background(), h.Deps,
		ops.VerifyOptions{AgainstLive: true})
	require.NoError(t, err)

	require.True(t, report.LiveChecked)
	assert.Equal(t, later, report.LiveAgainst,
		"the deployment was compared against the older statement of a tied pair")
}

// Written rather than driven, because the tie has to be certain. Running two
// real operations lands them in the same second most of the time, and a test
// whose precondition is a coin flip on a second boundary proves nothing on the
// runs where the coin lands the other way.
func TestAttestLogOrdersTwoOperationsFromTheSameSecond(t *testing.T) {
	dir := t.TempDir()
	instant := domain.NewTime(time.Date(2026, 8, 13, 9, 30, 0, 0, time.UTC))

	// Ids in mint order: a ULID's first 48 bits are its millisecond
	// timestamp, so the later operation's id sorts higher.
	for _, s := range []struct {
		id   string
		kind domain.OperationType
	}{
		{"op_01K2Z9X7QK8V3H4M5N6P7R8S9T", domain.OpTypeApply},
		{"op_01K2ZB4M8QF0R7V3X5Y6Z7A8Q9", domain.OpTypeConfig},
	} {
		stmt := domain.Attest(domain.OperationRecord{
			ID:        s.id,
			Type:      s.kind,
			Status:    domain.StatusSucceeded,
			StartedAt: instant,
		}, domain.AttestationInputs{})

		body, err := json.MarshalIndent(stmt, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, s.id+".json"), body, 0o644))
	}

	// Nil deps on purpose, and it is an assertion rather than a shortcut:
	// `attest log <path>` has to work on an auditor's laptop, which is not
	// an installation. A future edit that reaches for state here panics in
	// this test, which is exactly the signal wanted.
	entries, err := ops.AttestLog(context.Background(), nil, ops.VerifyOptions{Path: dir})
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, entries[0].Started, entries[1].Started, "the two did not tie")

	assert.Equal(t, string(domain.OpTypeConfig), entries[0].Kind,
		"the later operation of a tied pair printed second, so a listing that "+
			"says newest first is backwards inside every second")
	assert.Equal(t, string(domain.OpTypeApply), entries[1].Kind)
}
