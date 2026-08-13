package domain_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

// The fleet row is a published ABI (RFC 0026 §3.1), and these are the
// properties a reader on somebody else's laptop depends on.
//
// They are asserted rather than reviewed because the payload's mistakes are the
// quiet kind: a field that reads as a finding when it is an absence, a key
// built from a string nobody checked, a bound sentence that drifts away from
// the decision it exists to carry.

func fleetInstallation() domain.Installation {
	return domain.Installation{
		ID:      "op_01K2Z9QW8ERT6YH3VXNBM5CDFG",
		Product: "demo",
		Signing: domain.Signing{PublicKey: "RWQf6L?RANDOMLOOKINGKEY"},
	}
}

// Every row says what it is not.
//
// The bound is the one field that cannot be inferred from the others, and it
// carries decision 6b into the artifact: the key a row names is part of the
// row's claim, so checking a signature against it establishes nothing about who
// published. A reader that found the file without the RFC has only this
// sentence to go on.
func TestEveryRowCarriesTheBound(t *testing.T) {
	row := domain.NewFleetRow(domain.FleetRowInputs{
		Installation: fleetInstallation(),
		PublishedAt:  at("2026-08-13T10:00:00Z"),
	})

	require.Equal(t, domain.FleetBound, row.Bound)
	assert.Contains(t, strings.ToLower(row.Bound), "roster",
		"the bound does not name what a reader should anchor in instead")
	assert.Contains(t, strings.ToLower(row.Bound), "not who published",
		"the bound does not say what a signature over a row fails to prove")
}

// A count that could not be taken is absent, not zero.
//
// The failure this guards is specific and would be invisible in review: a fleet
// screen showing `0/0 running` for a machine whose daemon refused the
// connection reads exactly like a machine whose services are all down. One is
// the row an operator scrolls past; the other is the row they are looking for.
func TestACountThatCouldNotBeTakenIsAbsent(t *testing.T) {
	row := domain.NewFleetRow(domain.FleetRowInputs{
		Installation: fleetInstallation(),
		PublishedAt:  at("2026-08-13T10:00:00Z"),
		Health: domain.FleetHealth{
			Problem: "cannot reach the container runtime",
		},
		Drift: domain.FleetDrift{Problem: "no release is installed"},
	})

	require.Nil(t, row.Health.Services)
	require.Nil(t, row.Health.Running)
	require.Nil(t, row.Drift.Targets)

	body, err := json.Marshal(row)
	require.NoError(t, err)

	// null in the JSON, not 0 and not absent. A consumer reading
	// `.health.running` gets an unambiguous "no answer" rather than a
	// number nobody measured.
	assert.Contains(t, string(body), `"running":null`)
	assert.Contains(t, string(body), `"services":null`)
	assert.Contains(t, string(body), `"targets":null`)
}

// Zero really is zero when it was measured.
//
// The other half of the pointer, and the one that makes the test above mean
// something: a deployment with nothing running must publish that as a fact.
func TestAMeasuredZeroIsPublishedAsZero(t *testing.T) {
	none, three := 0, 3
	row := domain.NewFleetRow(domain.FleetRowInputs{
		Installation: fleetInstallation(),
		PublishedAt:  at("2026-08-13T10:00:00Z"),
		Health:       domain.FleetHealth{Services: &three, Running: &none},
	})

	body, err := json.Marshal(row)
	require.NoError(t, err)
	assert.Contains(t, string(body), `"running":0`)
	assert.Contains(t, string(body), `"services":3`)
}

// The row does not share memory with whatever built it.
//
// These constructors are value-to-value, and a shared pointer is that promise
// being false in the one case nobody tests -- a publisher that reuses its
// health struct across two installations would rewrite a document it had
// already built.
func TestARowDoesNotAliasItsInputs(t *testing.T) {
	running := 2
	in := domain.FleetRowInputs{
		Installation: fleetInstallation(),
		PublishedAt:  at("2026-08-13T10:00:00Z"),
		Health:       domain.FleetHealth{Running: &running},
	}
	row := domain.NewFleetRow(in)

	running = 99

	require.NotNil(t, row.Health.Running)
	assert.Equal(t, 2, *row.Health.Running)
}

// Free text is bounded, because a row travels to a shared bucket.
//
// The text is the manager's own today. That is exactly the argument for
// bounding it now: the same reasoning left an attestation carrying a hook's
// unbounded stderr until RFC 0025 §10 caught it, and a row is read in
// terminals and web views by people who did not write it.
func TestFreeTextInARowIsBounded(t *testing.T) {
	row := domain.NewFleetRow(domain.FleetRowInputs{
		Installation: fleetInstallation(),
		PublishedAt:  at("2026-08-13T10:00:00Z"),
		Health:       domain.FleetHealth{Problem: strings.Repeat("x", 5000)},
		LastOperation: &domain.FleetOperation{
			ID:      "op_1",
			Kind:    "update",
			Outcome: "failed\x1b[2Jcleared your screen",
			At:      at("2026-08-13T09:00:00Z"),
		},
	})

	assert.LessOrEqual(t, len(row.Health.Problem), domain.MaxAttestedText+len("… [truncated]"))
	require.NotNil(t, row.LastOperation)
	assert.NotContains(t, row.LastOperation.Outcome, "\x1b",
		"an escape sequence reached a document that is read in terminals")
}

// The key is built from checked components, in both directions.
func TestTheKeyRefusesWhatItWouldNotHaveWritten(t *testing.T) {
	key, err := domain.FleetKey("demo", "op_01K2Z9QW8ERT6YH3VXNBM5CDFG")
	require.NoError(t, err)
	assert.Equal(t, "fleet/demo/op_01K2Z9QW8ERT6YH3VXNBM5CDFG/status.json", key)

	// Both halves come out of installation.yaml, which an operator can
	// edit, and the result names an object on a bucket several machines
	// write to -- so one installation must not be able to choose what
	// another's row is called.
	for _, tc := range []struct{ product, id string }{
		{"../..", "op_1"},
		{"demo", "../../etc/passwd"},
		{"demo", ""},
		{"", "op_1"},
		{"demo", "."},
		{"demo", ".."},
		{"demo", ".hidden"},
		{"demo", "op_1/nested"},
		{"demo", strings.Repeat("a", 200)},
	} {
		_, err := domain.FleetKey(tc.product, tc.id)
		assert.Errorf(t, err, "FleetKey(%q, %q) was accepted", tc.product, tc.id)
	}
}

// Parsing a key back is the same check, so a listing cannot smuggle one in.
//
// `fleet ls` reads these out of somebody else's bucket. A key it accepts is a
// key it then fetches, so the parse has to refuse everything the writer would
// have refused -- and refuse it by rebuilding rather than by a second
// hand-written rule that could drift from the first.
func TestParsingAKeyRefusesWhatWritingOneWould(t *testing.T) {
	product, id, err := domain.ParseFleetKey("fleet/demo/op_01K2Z9/status.json")
	require.NoError(t, err)
	assert.Equal(t, "demo", product)
	assert.Equal(t, "op_01K2Z9", id)

	for _, key := range []string{
		"fleet/../../etc/passwd",
		"fleet/demo/op_1/nested/status.json",
		"fleet/demo/status.json",
		"fleet/demo/op_1/status.json.minisig",
		"attestations/demo/op_1/status.json",
		"fleet/demo/.hidden/status.json",
		"",
	} {
		_, _, err := domain.ParseFleetKey(key)
		assert.Errorf(t, err, "ParseFleetKey(%q) was accepted", key)
	}
}

// A row from a newer manager is refused whole, not read in part.
//
// The same rule LoadInstallation applies to a future installation, and for the
// same reason: the fields this manager recognises inside a document it does not
// understand make a row that looks complete. A fleet screen presenting that as
// fact is worse than one saying it cannot read the row.
func TestARowFromANewerManagerIsRefusedWhole(t *testing.T) {
	row := domain.FleetRow{
		Schema:         domain.FleetSchemaVersion + 1,
		Product:        "demo",
		InstallationID: "op_1",
		PublishedAt:    at("2026-08-13T10:00:00Z"),
	}

	err := row.Validate()
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Message, "newer manager")
}

// A row with no publish time is refused, because the alternative is worse.
//
// Staleness is computed from that field. A row without one has no age, and a
// reader that defaulted it to now would show the least trustworthy row in the
// fleet as the freshest.
func TestARowWithNoPublishTimeIsRefused(t *testing.T) {
	row := domain.FleetRow{
		Schema:         domain.FleetSchemaVersion,
		Product:        "demo",
		InstallationID: "op_1",
	}
	require.Error(t, row.Validate())
}

func TestARowThisManagerWroteValidates(t *testing.T) {
	row := domain.NewFleetRow(domain.FleetRowInputs{
		Installation: fleetInstallation(),
		PublishedAt:  at("2026-08-13T10:00:00Z"),
	})
	require.NoError(t, row.Validate())
}

// Staleness, including the two edges that decide whether a fleet screen
// flickers.
func TestStaleness(t *testing.T) {
	now := at("2026-08-13T12:00:00Z").Time
	row := func(published string) domain.FleetRow {
		return domain.FleetRow{PublishedAt: at(published)}
	}

	// Exactly at the threshold is not yet stale: an hourly publisher read
	// against a one-hour threshold would otherwise alternate on scheduler
	// jitter alone.
	assert.False(t, row("2026-08-13T11:00:00Z").Stale(now, time.Hour))
	assert.True(t, row("2026-08-13T10:59:59Z").Stale(now, time.Hour))

	// No threshold means nothing is stale, which is what a reader that was
	// not asked to judge should conclude.
	assert.False(t, row("2020-01-01T00:00:00Z").Stale(now, 0))

	// A row from the future keeps its negative age rather than being
	// clamped: a machine with a wrong clock is a finding, and zero would
	// present it as having published this instant.
	assert.Negative(t, row("2026-08-13T13:00:00Z").Age(now))
}
