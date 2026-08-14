package suite

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// The roster (RFC 0026 P3).
//
// Two answers, one file. Which installations are absent -- an object that was
// never written cannot announce itself -- and which key may speak for one,
// because a row carrying the key that verifies it verifies itself and
// authenticates nothing against the only attacker this design has.
//
// The test the RFC makes decision 6b conditional on is
// TestARowSignedByAnotherMachinesKeyIsNamed. A verifier anchored in the row
// passes that scenario, which is what makes it the one worth writing first.

// rosterFor builds a roster naming these installations with their real keys.
func rosterFor(insts ...domain.Installation) domain.FleetRoster {
	roster := domain.FleetRoster{Schema: domain.FleetRosterSchemaVersion}
	for _, inst := range insts {
		roster.Installations = append(roster.Installations, domain.FleetRosterEntry{
			Product:   inst.Product,
			ID:        inst.ID,
			PublicKey: inst.Signing.PublicKey,
		})
	}
	return roster
}

// rowAt finds one installation's line in a report.
func rowAt(t *testing.T, report ops.FleetReport, id string) ops.FleetRowStatus {
	t.Helper()
	for _, row := range report.Rows {
		if row.InstallationID == id {
			return row
		}
	}
	t.Fatalf("no row for %s in %d row(s)", id, len(report.Rows))
	return ops.FleetRowStatus{}
}

// A roster turns "a signature is there" into "this installation signed it".
func TestARosterAuthenticatesARow(t *testing.T) {
	h := fleetHarness(t)
	inst, _ := h.withFleetTarget(t)
	ctx := context.Background()

	_, err := ops.FleetPublish(ctx, h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	report, err := ops.FleetList(ctx, h.Deps, ops.FleetListOptions{Roster: rosterFor(inst)})
	require.NoError(t, err)

	require.Len(t, report.Rows, 1)
	row := report.Rows[0]
	assert.Equal(t, ops.FleetVerified, row.Signature)
	assert.True(t, row.Expected)
	assert.Empty(t, row.Problem)
	assert.NotNil(t, row.Row)
	assert.Zero(t, report.Problems())
	assert.Equal(t, 1, report.Expected)

	// And the same target, read without the roster, says only that a
	// signature is there -- so the verdict above came from the roster and
	// not from something the row carries.
	without, err := ops.FleetList(ctx, h.Deps, ops.FleetListOptions{})
	require.NoError(t, err)
	assert.Equal(t, ops.FleetSigned, without.Rows[0].Signature)
}

// **The overwrite, played out.** RFC 0026 §6's P3 test, and the one decision 6b
// lives or dies by.
//
// A second installation with a perfectly valid signing key of its own writes a
// row claiming to be the first, signs it with its own key, and carries that key
// in the payload -- which is exactly what a machine with write access to a
// shared prefix can do, and exactly what makes the row verify against itself. A
// verifier that trusted the row's own key would call this verified. This one
// names it.
func TestARowSignedByAnotherMachinesKeyIsNamed(t *testing.T) {
	h := fleetHarness(t)
	victim, offsite := h.withFleetTarget(t)
	ctx := context.Background()

	_, err := ops.FleetPublish(ctx, h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	// A second machine, with its own key. Nothing about it is forged: it is
	// a legitimate member of the fleet, which is the point.
	attacker := fleetHarness(t)
	attackerInst, _ := attacker.withFleetTarget(t)
	require.NotEqual(t, victim.Signing.PublicKey, attackerInst.Signing.PublicKey,
		"both harnesses minted the same key, so this proves nothing")

	// The row it writes claims the victim's identity, so the key-versus-row
	// check this reader already had cannot catch it. Only the roster can.
	forged := domain.NewFleetRow(domain.FleetRowInputs{
		Installation: domain.Installation{
			Product: victim.Product,
			ID:      victim.ID,
			Signing: domain.Signing{PublicKey: attackerInst.Signing.PublicKey},
		},
		Version:     "9.9.9-owned",
		PublishedAt: domain.NewTime(time.Now()),
	})
	body, err := json.MarshalIndent(forged, "", "  ")
	require.NoError(t, err)
	body = append(body, '\n')

	sig, err := attacker.Deps.Signer.Sign(ctx, body, "morzer fleet demo forged")
	require.NoError(t, err)

	victimKey, err := domain.FleetKey(victim.Product, victim.ID)
	require.NoError(t, err)
	path := filepath.Join(offsite, filepath.FromSlash(victimKey))
	require.NoError(t, os.WriteFile(path, body, 0o644))
	require.NoError(t, os.WriteFile(path+".minisig", sig.Encoded, 0o644))

	// The row verifies perfectly against the key it carries. That is the
	// premise of the whole decision, so it is asserted rather than assumed.
	require.True(t, h.Deps.Checker.Check(body, sig.Encoded, attackerInst.Signing.PublicKey),
		"the forged row does not even verify against its own key")

	report, err := ops.FleetList(ctx, h.Deps, ops.FleetListOptions{Roster: rosterFor(victim)})
	require.NoError(t, err)
	require.Len(t, report.Rows, 1)

	row := report.Rows[0]
	assert.Equal(t, ops.FleetSignedByAnotherKey, row.Signature)
	assert.NotEqual(t, ops.FleetVerified, row.Signature,
		"a row was verified against the key it carries, which authenticates nothing")
	assert.Contains(t, row.Problem, "the roster does not name")
	assert.Equal(t, 1, report.Problems())

	// And nothing the impostor wrote is rendered. A caption beside
	// `9.9.9-owned` would be the caption doing the work.
	assert.Nil(t, row.Row,
		"the payload of a row that failed verification was displayed anyway")
}

// The installation that stopped publishing is the row you actually want.
//
// Without a roster it is structurally invisible: listing a prefix shows exactly
// the population that is fine.
func TestAnExpectedInstallationThatPublishedNothingIsARow(t *testing.T) {
	h := fleetHarness(t)
	inst, _ := h.withFleetTarget(t)
	ctx := context.Background()

	_, err := ops.FleetPublish(ctx, h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	roster := rosterFor(inst)
	roster.Installations = append(roster.Installations, domain.FleetRosterEntry{
		Product:   "demo",
		ID:        "inst_01GONEQUIETQUIETQUIETQUIET",
		PublicKey: "RWQfaKe0000000000000000000000000000000000000000000000",
	})

	report, err := ops.FleetList(ctx, h.Deps, ops.FleetListOptions{Roster: roster})
	require.NoError(t, err)

	require.Len(t, report.Rows, 2, "the absent installation was not a row")
	gone := rowAt(t, report, "inst_01GONEQUIETQUIETQUIETQUIET")
	assert.True(t, gone.Absent)
	assert.True(t, gone.Expected)
	assert.Nil(t, gone.Row)
	assert.Contains(t, gone.Problem, "no target holds a row")
	assert.Equal(t, "fleet/demo/inst_01GONEQUIETQUIETQUIETQUIET/status.json", gone.Key,
		"the absent row does not say where to go and look")

	assert.Equal(t, 1, report.Absent())
	assert.Equal(t, 1, report.Problems(),
		"an installation that stopped publishing did not reach the exit status")

	// The same target with no roster shows one row and no problem, which is
	// the failure mode this phase removes.
	blind, err := ops.FleetList(ctx, h.Deps, ops.FleetListOptions{})
	require.NoError(t, err)
	assert.Len(t, blind.Rows, 1)
	assert.Zero(t, blind.Problems())
}

// Stripping a signature must not be a way out of being checked.
//
// The downgrade: an attacker who cannot forge a signature can delete one. If a
// missing signature read as the ordinary unsigned state, removing the .minisig
// beside a forged row would be enough to escape the roster entirely.
func TestARowThatShouldBeSignedAndIsNotIsAFinding(t *testing.T) {
	h := fleetHarness(t)
	inst, offsite := h.withFleetTarget(t)
	ctx := context.Background()

	_, err := ops.FleetPublish(ctx, h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	key, err := domain.FleetKey(inst.Product, inst.ID)
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(offsite, filepath.FromSlash(key))+".minisig"))

	report, err := ops.FleetList(ctx, h.Deps, ops.FleetListOptions{Roster: rosterFor(inst)})
	require.NoError(t, err)
	require.Len(t, report.Rows, 1)

	assert.Equal(t, ops.FleetMissingSignature, report.Rows[0].Signature)
	assert.NotEqual(t, ops.FleetUnsigned, report.Rows[0].Signature,
		"a stripped signature was reported as the ordinary state of a machine with no key")
	assert.Equal(t, 1, report.Problems())
}

// Bytes nobody's key accounts for are unverifiable, not merely signed.
func TestATamperedRowIsUnverifiable(t *testing.T) {
	h := fleetHarness(t)
	inst, offsite := h.withFleetTarget(t)
	ctx := context.Background()

	_, err := ops.FleetPublish(ctx, h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	key, err := domain.FleetKey(inst.Product, inst.ID)
	require.NoError(t, err)
	path := filepath.Join(offsite, filepath.FromSlash(key))

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	// Still valid JSON and still this installation's row: only the version
	// moved, which is exactly the edit somebody would make.
	tampered := strings.Replace(string(body), `"1.2.0"`, `"9.9.9"`, 1)
	require.NotEqual(t, string(body), tampered, "the edit changed nothing")
	require.NoError(t, os.WriteFile(path, []byte(tampered), 0o644))

	report, err := ops.FleetList(ctx, h.Deps, ops.FleetListOptions{Roster: rosterFor(inst)})
	require.NoError(t, err)
	require.Len(t, report.Rows, 1)

	assert.Equal(t, ops.FleetUnverifiable, report.Rows[0].Signature)
	assert.Nil(t, report.Rows[0].Row, "the tampered payload was rendered")
	assert.Equal(t, 1, report.Problems())
}

// A roster that binds no key still reports absence, and says what it cannot do.
//
// The reason the key is optional: requiring it would put twelve public keys
// between an operator and the answer they came for.
func TestARosterWithoutKeysStillFindsTheMissingMachine(t *testing.T) {
	h := fleetHarness(t)
	inst, _ := h.withFleetTarget(t)
	ctx := context.Background()

	_, err := ops.FleetPublish(ctx, h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	roster := domain.FleetRoster{
		Schema: domain.FleetRosterSchemaVersion,
		Installations: []domain.FleetRosterEntry{
			{Product: inst.Product, ID: inst.ID},
			{Product: "demo", ID: "inst_01NEVERPUBLISHEDANYTHING"},
		},
	}
	require.NoError(t, roster.Validate())

	report, err := ops.FleetList(ctx, h.Deps, ops.FleetListOptions{Roster: roster})
	require.NoError(t, err)

	assert.Equal(t, 1, report.Absent(), "absence needs no key and was not reported")
	assert.Equal(t, ops.FleetSigned, rowAt(t, report, inst.ID).Signature,
		"a row was verified against a roster entry that binds no key")

	joined := strings.Join(report.Limitations, " ")
	assert.Contains(t, joined, "binds no key",
		"the reader did not say which installations it could not authenticate")
	assert.Contains(t, joined, inst.ID)
}

// A row from an installation the roster does not name is shown and not failed.
//
// A roster covering three of twelve machines is a legitimate way to adopt this.
// Reporting the other nine as findings would make it unusable on the way in --
// the roster's contract is "these must be here", not "no others may be".
func TestARowTheRosterDoesNotNameIsShownAndNotFailed(t *testing.T) {
	h := fleetHarness(t)
	inst, _ := h.withFleetTarget(t)
	ctx := context.Background()

	_, err := ops.FleetPublish(ctx, h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	roster := domain.FleetRoster{
		Schema: domain.FleetRosterSchemaVersion,
		Installations: []domain.FleetRosterEntry{
			{Product: "web", ID: "inst_01SOMEBODYELSE", PublicKey: "RWQfaKe"},
		},
	}
	require.NoError(t, roster.Validate())

	report, err := ops.FleetList(ctx, h.Deps, ops.FleetListOptions{Roster: roster})
	require.NoError(t, err)

	published := rowAt(t, report, inst.ID)
	assert.False(t, published.Expected, "a row outside the roster was marked expected")
	assert.Empty(t, published.Problem, "an unrostered row was reported as a finding")
	assert.Equal(t, ops.FleetSigned, published.Signature,
		"a row was judged against a roster entry belonging to somebody else")

	// The one thing that *is* a finding here is the machine the roster
	// expects and this target does not hold.
	assert.Equal(t, 1, report.Absent())
	assert.Equal(t, 1, report.Problems())
}

// A build with no signature checker accuses nobody.
//
// Reporting every row as unverifiable because this reader cannot check would be
// the reader blaming a fleet for what is missing from itself.
func TestAReaderThatCannotCheckSignaturesSaysSoInsteadOfAccusing(t *testing.T) {
	h := fleetHarness(t)
	inst, _ := h.withFleetTarget(t)
	ctx := context.Background()

	_, err := ops.FleetPublish(ctx, h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	h.Deps.Checker = nil

	report, err := ops.FleetList(ctx, h.Deps, ops.FleetListOptions{Roster: rosterFor(inst)})
	require.NoError(t, err)
	require.Len(t, report.Rows, 1)

	assert.Equal(t, ops.FleetSigned, report.Rows[0].Signature)
	assert.Zero(t, report.Problems())
	assert.Contains(t, strings.Join(report.Limitations, " "), "cannot check signatures")
}

// The reader states its limits with a roster too, in a different sentence.
//
// A2's discipline does not lapse once an anchor exists: the anchor is a file
// the operator maintains, and a reader that stopped saying so would be
// presenting its own input back as evidence.
func TestTheReaderStatesWhatARosterDoesAndDoesNotBuy(t *testing.T) {
	h := fleetHarness(t)
	inst, _ := h.withFleetTarget(t)
	ctx := context.Background()

	_, err := ops.FleetPublish(ctx, h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	report, err := ops.FleetList(ctx, h.Deps, ops.FleetListOptions{Roster: rosterFor(inst)})
	require.NoError(t, err)

	require.NotEmpty(t, report.Limitations,
		"a reader with a roster presented its table with no statement at all")
	joined := strings.ToLower(strings.Join(report.Limitations, " "))
	assert.Contains(t, joined, "roster you supplied")
	assert.NotContains(t, joined, "no roster was given")
}

// Reading a roster refuses what an operator actually mistypes.
func TestReadingARosterRefusesTheFileRatherThanTheFleet(t *testing.T) {
	good := "schema: 1\ninstallations:\n  - product: demo\n    id: inst_01A\n    key: RWQfaKe\n"

	roster, err := ops.ParseFleetRoster(good)
	require.NoError(t, err)
	require.Len(t, roster.Installations, 1)
	assert.Equal(t, "RWQfaKe", roster.Installations[0].PublicKey)

	cases := map[string]string{
		// The singular is the mistake somebody makes once; it parses
		// into an empty roster, which reports either the whole fleet
		// absent or nothing absent at all depending on which key was
		// misspelt.
		"a misspelt list key": "schema: 1\ninstallation:\n  - product: demo\n    id: inst_01A\n",
		"a misspelt entry field": "schema: 1\ninstallations:\n  - product: demo\n" +
			"    installation_id: inst_01A\n",
		"nothing at all":  "",
		"not YAML at all": "schema: 1\ninstallations: [\n",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ops.ParseFleetRoster(raw)
			require.Error(t, err, "a roster that says nothing was accepted as one")
		})
	}
}
