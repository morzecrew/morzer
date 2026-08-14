package views_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ui/views"
)

// The fleet table's job is to be honest at a glance, and the two ways it can
// fail are both about a cell that reads as a measurement when it is not.
//
// `0/0 up` for a runtime that never answered looks exactly like a deployment
// whose services are all stopped. A missing row for an object that would not
// parse looks exactly like a machine nobody has installed. Both are asserted
// against the rendered text rather than against the struct, because the struct
// has the distinction and the question is whether the reader is shown it.

func fleetRow(product string) views.FleetRow {
	running, services, drift := 2, 2, 0
	return views.FleetRow{
		Target:         "s3://fleet.example/rows",
		Key:            "fleet/" + product + "/inst_01/status.json",
		Product:        product,
		InstallationID: "inst_01",
		Signature:      "signed",
		Age:            "12m0s ago",
		Row: &domain.FleetRow{
			Schema:         domain.FleetSchemaVersion,
			Product:        product,
			InstallationID: "inst_01",
			Version:        "1.4.0",
			Health:         domain.FleetHealth{Running: &running, Services: &services},
			Drift:          domain.FleetDrift{Targets: &drift},
		},
	}
}

// fleetMixed is one report holding every shape a row can take.
func fleetMixed() views.Fleet {
	down, three := 0, 3
	oneTarget := 1

	healthy := fleetRow("acme")

	stopped := fleetRow("beta")
	stopped.Row.Health = domain.FleetHealth{Running: &down, Services: &three}

	unchecked := fleetRow("gamma")
	unchecked.Row.Health = domain.FleetHealth{
		Problem: "cannot reach the container runtime",
	}
	unchecked.Row.Drift = domain.FleetDrift{Problem: "no release is installed"}
	unchecked.Age = "4h0m0s ago"
	unchecked.Stale = true

	sandbox := fleetRow("sandbox")
	sandbox.Row.Mode = domain.ModeDev
	sandbox.Signature = "unsigned"
	sandbox.Row.Drift = domain.FleetDrift{Targets: &oneTarget}
	sandbox.Row.Health.Attention = 1

	future := fleetRow("delta")
	future.Age = "in 3h0m0s"

	unreadable := views.FleetRow{
		Target:         "s3://fleet.example/rows",
		Key:            "fleet/epsilon/inst_09/status.json",
		Product:        "epsilon",
		InstallationID: "inst_09",
		Signature:      "signed",
		Problem:        "it was written by a newer manager (schema 2, this manager reads 1)",
	}

	return views.Fleet{
		Targets:    []string{"s3://fleet.example/rows"},
		StaleAfter: "24h0m0s",
		Rows: []views.FleetRow{
			healthy, stopped, unchecked, sandbox, future, unreadable,
		},
		Limitations: []string{
			"no roster was given, so no row below is authenticated and no absent " +
				"installation can be shown; both need the roster that binds an " +
				"installation id to a public key",
		},
	}
}

// fleetRostered is the same table read with a roster, which is the arrangement
// an operator is in when something has actually gone wrong.
func fleetRostered() views.Fleet {
	verified := fleetRow("acme")
	verified.Signature = "verified"
	verified.Expected = true

	overwritten := fleetRow("beta")
	overwritten.Signature = "signed-by-another-key"
	overwritten.Expected = true
	overwritten.Row = nil
	overwritten.Age = ""
	overwritten.Problem = "signed by a key the roster does not name"

	stranger := fleetRow("gamma")
	stranger.Signature = "signed"

	absent := views.FleetRow{
		Key:            "fleet/delta/inst_07/status.json",
		Product:        "delta",
		InstallationID: "inst_07",
		Expected:       true,
		Absent:         true,
		Problem:        "the roster expects this installation; no target holds a row",
	}

	return views.Fleet{
		Targets:    []string{"s3://fleet.example/rows"},
		Expected:   3,
		StaleAfter: "24h0m0s",
		Rows:       []views.FleetRow{verified, overwritten, stranger, absent},
		Limitations: []string{
			"every verdict below is against the roster you supplied: it is what " +
				"says which installations exist and which key each one signs " +
				"with, and nothing here can check that it is right",
		},
	}
}

// The row an operator came for is a row, and it is legible as one.
//
// An installation that stopped publishing is structurally invisible without a
// roster: listing a prefix shows exactly the population that is fine. So the
// rendering of the one line the roster buys is worth pinning here, where it
// meets a reader, and not only at the operation that computes it.
func TestAnAbsentInstallationIsRenderedAsALine(t *testing.T) {
	out := render(t, 100, fleetRostered())

	assert.Contains(t, out, "delta/inst_07",
		"an installation the roster expects and nothing published was not shown")
	assert.Contains(t, out, "the roster expects this installation; no target holds a row")
	assert.Contains(t, out, "the roster expects 3 installation(s); 2 published a row and 1 did not",
		"the table does not say how much of the expected fleet reported")

	// Not `unsigned`: there is no row, so nothing could have been signed,
	// and the ordinary state of a machine with no key is the most
	// reassuring possible spelling of the most alarming line in the table.
	assert.NotContains(t, out, "unsigned")
}

// The overwrite is loud, and the row it forged is not rendered.
func TestARowSignedByAnUnnamedKeyIsRenderedAsSuch(t *testing.T) {
	out := render(t, 100, fleetRostered())

	assert.Contains(t, out, "signed-by-another-key")
	assert.Contains(t, out, "roster does not name")
	assert.NotContains(t, out, "1.4.0\n  beta",
		"the payload of a row that failed verification was rendered")

	// And a row from an installation the roster says nothing about is
	// noted rather than failed: a roster covering three of twelve machines
	// is a legitimate way to adopt this.
	assert.Contains(t, out, "the roster does not name this installation")
}

// A runtime that did not answer must not render as a deployment that is down.
//
// The distinction the payload carries as an absent count rather than a zero,
// asserted where it actually matters: in the characters an operator reads.
func TestARuntimeThatDidNotAnswerDoesNotRenderAsZero(t *testing.T) {
	out := render(t, 100, fleetMixed())

	assert.Contains(t, out, "0/3 up", "a deployment that is down lost its count")
	assert.Contains(t, out, "not checked",
		"a runtime that did not answer rendered as a measurement")
	assert.NotContains(t, out, "0/0 up",
		"a runtime that did not answer rendered as a deployment with no services")
}

// A row nobody can read appears, with its reason.
func TestAnUnreadableRowIsRendered(t *testing.T) {
	out := render(t, 100, fleetMixed())

	assert.Contains(t, out, "epsilon",
		"a row that could not be read was dropped from the table")
	assert.Contains(t, out, "newer manager",
		"the row is listed without saying why it cannot be read")
}

// A row that *was* read still says why a measurement is missing.
//
// The publisher collected `health.problem` and `drift.problem` precisely so
// somebody could act on them. Rendering only `not checked` in the cell told an
// operator that a measurement was missing and not why -- which on a fleet
// screen means going to each machine to find out, and is the whole reason the
// row carries the sentence.
func TestTheReasonAMeasurementIsMissingIsRendered(t *testing.T) {
	out := render(t, 100, fleetMixed())

	assert.Contains(t, out, "cannot reach the container runtime",
		"the row says why health was not taken and the table does not")
	assert.Contains(t, out, "no release is installed",
		"the row says why drift was not measured and the table does not")
}

// The limitations are printed, every run.
//
// RFC 0026 §8 permits this phase to exist only because the reader states them.
// A table rendered without them is the complete-looking table the whole design
// is written against, so it is asserted here as well as at the operation.
func TestTheLimitationsAreRendered(t *testing.T) {
	out := render(t, 100, fleetMixed())

	assert.Contains(t, out, "roster")
	assert.Contains(t, out, "authenticated")
	assert.Contains(t, out, "24h0m0s",
		"a staleness verdict was rendered without the threshold it was judged against")
}

// A sandbox is marked, and a clock that is wrong is visible.
func TestASandboxAndAWrongClockAreVisible(t *testing.T) {
	out := render(t, 100, fleetMixed())

	assert.Contains(t, out, "sandbox (dev)",
		"a sandbox rendered exactly like a production machine")
	assert.Contains(t, out, "in 3h",
		"a row published in the future rendered as an ordinary age")
	assert.Contains(t, out, "need attention")
}

// An empty target says so rather than printing a bare table.
func TestAnEmptyFleetSaysSo(t *testing.T) {
	out := render(t, 100, views.Fleet{
		Targets:     []string{"s3://fleet.example/rows"},
		Limitations: []string{"no roster was given"},
	})
	require.Contains(t, out, "no rows have been published here")
}

// fleetFixtures pins the table at every width.
//
// One fixture rather than several, and deliberately a crowded one: the point of
// the golden file here is the *mixture* -- a measured zero beside an unmeasured
// one, a sandbox beside production, a row that would not parse beside five that
// did. Splitting them into tidy fixtures would render each case in isolation,
// which is the one arrangement an operator never sees.
func fleetFixtures() []fixture {
	return []fixture{{
		name:  "fleet",
		value: fleetMixed(),
		fields: []string{
			"acme", "0/3 up", "not checked", "sandbox (dev)", "in 3h",
			"epsilon", "newer manager", "roster", "24h0m0s",
		},
	}, {
		name:  "fleet-roster",
		value: fleetRostered(),
		fields: []string{
			"verified", "signed-by-another-key", "delta/inst_07",
			"no target holds a row", "roster expects 3",
		},
	}, {
		name:   "fleet-empty",
		value:  views.Fleet{Targets: []string{"s3://fleet.example/rows"}, Limitations: []string{"no roster was given"}},
		fields: []string{"no rows have been published here", "no roster"},
	}}
}
