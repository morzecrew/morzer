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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/target"
	"github.com/morzecrew/morzer/internal/adapters/target/localdir"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
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

// The timer arrives with the first target and leaves with the last.
//
// RFC 0026 P4. A unit set installed once at `init` could never do the second
// half: `init` runs before any target exists, so a machine that gains one a
// month later must gain the timer then -- and one that removes its last target
// must stop publishing on a schedule to somewhere its operator deliberately
// took away.
func TestTheFleetTimerFollowsTheTargets(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.Deps.Supervisor = h.Supervisor
	h.Supervisor.Present = true

	registry, err := target.NewRegistry(localdir.New())
	require.NoError(t, err)
	h.Deps.Targets = registry
	h.Deps.Objects = registry
	ctx := context.Background()

	// The machine manages units, which is what makes reconciliation its
	// business at all: `init --install-units=false` is a supported choice.
	units, err := h.Supervisor.Units(ports.UnitParams{Product: "demo"})
	require.NoError(t, err)
	require.NoError(t, h.Supervisor.InstallUnits(ctx, units))

	timer := "demo-fleet.timer"
	require.NotContains(t, h.Supervisor.Installed, timer,
		"a machine with no target was given a timer with nowhere to publish")

	offsite := filepath.Join(t.TempDir(), "offsite")
	require.NoError(t, os.MkdirAll(offsite, 0o755))

	_, err = ops.TargetAdd(ctx, h.Deps, ops.TargetAddOptions{URL: "file://" + offsite})
	require.NoError(t, err)
	assert.Contains(t, h.Supervisor.Installed, timer,
		"a target was added and nothing schedules a publish to it")

	_, err = ops.TargetRemove(ctx, h.Deps, ops.Options{}, "file://"+offsite)
	require.NoError(t, err)
	assert.NotContains(t, h.Supervisor.Installed, timer,
		"the timer outlived the target it was publishing to")
}

// The documented way to build a roster entry actually produces one.
//
// RFC 0026 §3.6 said the key is obtained from `morzer installation describe`,
// and it is not: that document is *desired state*, and a signing key is machine
// identity, which RFC 0027 excludes from it deliberately and RFC 0028 §5.3
// explains. Nothing else prints the key on its own.
//
// A dry-run publish does, and it is the better shape anyway: it mints nothing,
// and what it shows is exactly the row the roster describes. This pins the
// recipe the reference page tells an operator to run, so the page cannot go on
// naming a field that moved.
func TestADryRunPrintsWhatARosterEntryNeeds(t *testing.T) {
	h := fleetHarness(t)
	inst, _ := h.withFleetTarget(t)

	result, err := ops.FleetPublish(context.Background(), h.Deps, ops.FleetPublishOptions{
		TargetOptions: ops.TargetOptions{Options: ops.Options{DryRun: true}},
	})
	require.NoError(t, err)

	report, ok := result.Data.(ops.FleetPublishReport)
	require.True(t, ok)

	// The three fields of a roster entry, from one command.
	assert.Equal(t, inst.Product, report.Row.Product)
	assert.Equal(t, inst.ID, report.Row.InstallationID)
	assert.Equal(t, inst.Signing.PublicKey, report.Row.SigningKey,
		"the dry run does not print the key a roster entry has to bind")

	// And the entry it produces is one the roster accepts.
	roster := domain.FleetRoster{
		Schema: domain.FleetRosterSchemaVersion,
		Installations: []domain.FleetRosterEntry{{
			Product:   report.Row.Product,
			ID:        report.Row.InstallationID,
			PublicKey: report.Row.SigningKey,
		}},
	}
	require.NoError(t, roster.Validate(),
		"the entry the documented recipe produces is not a valid roster entry")
	assert.Empty(t, roster.Unkeyed())
}

// A timer that is installed and switched off publishes nothing, and says so
// nowhere.
//
// `systemctl disable demo-fleet.timer` leaves the unit loaded, so every check
// that asks only whether it is *there* passes while the schedule it exists for
// has stopped. The check calls itself "systemd units are installed and
// enabled"; until this test it verified the first word only, which is the
// worst arrangement available -- a machine that quietly stopped publishing,
// reported healthy by the command an operator runs to ask whether it is.
func TestDoctorReportsARequiredTimerThatIsSwitchedOff(t *testing.T) {
	h := newHarness(t)
	// A target, so this machine is one that should be publishing at all, and
	// a wired registry, so the reconciliation at the end is the real one.
	h.withTargets(t)
	h.Deps.Supervisor = h.Supervisor
	h.Supervisor.Present = true
	ctx := context.Background()

	units, err := h.Supervisor.Units(ports.UnitParams{Product: "demo", FleetTimer: true})
	require.NoError(t, err)
	require.NoError(t, h.Supervisor.InstallUnits(ctx, units))
	for _, u := range units {
		// As a reconciliation leaves them: loaded, and enabled exactly
		// where the supervisor asked for it.
		h.Supervisor.States[u.Name] = ports.UnitState{
			Name: u.Name, Loaded: true, Active: "active", Enabled: u.Enable,
		}
	}

	unitsCheck := func(t *testing.T) events.CheckResult {
		t.Helper()
		report, err := ops.Doctor(ctx, h.Deps)
		require.NoError(t, err)
		for _, c := range report.Results {
			if c.ID == "system.units" {
				return c
			}
		}
		t.Fatal("doctor ran no unit check")
		return events.CheckResult{}
	}

	got := unitsCheck(t)
	require.Equal(t, events.CheckOK, got.Status,
		"a correctly reconciled machine was reported as having a problem: %s", got.Message)

	require.NoError(t, h.Supervisor.Disable(ctx, "demo-fleet.timer"))

	got = unitsCheck(t)
	assert.Equal(t, events.CheckWarn, got.Status,
		"the fleet timer was switched off and doctor reported the units healthy")
	assert.Contains(t, got.Message, "demo-fleet.timer: not enabled")

	// The oneshot service beside it is deliberately never enabled -- enabling
	// it would run a publish at every boot -- so it must not be reported.
	assert.NotContains(t, got.Message, "demo-fleet.service: not enabled",
		"a unit the supervisor deliberately leaves disabled was reported as a problem")

	// And the remedy clears it. The comment on this check earns its place
	// only if the warning it now emits can be acted on: a warning that fires
	// forever with a fix that does not fix it is the thing that trains an
	// operator to stop reading the check. A reconciliation is what
	// `init --repair --install-units` performs, and it re-enables what the
	// supervisor asked to have enabled.
	second := filepath.Join(t.TempDir(), "second")
	require.NoError(t, os.MkdirAll(second, 0o755))
	_, err = ops.TargetAdd(ctx, h.Deps, ops.TargetAddOptions{URL: "file://" + second})
	require.NoError(t, err)

	got = unitsCheck(t)
	assert.Equal(t, events.CheckOK, got.Status,
		"a reconciliation did not re-enable the timer, so the warning cannot be cleared: %s",
		got.Message)
}

// Doctor asks about the units this installation should have, not every unit
// this supervisor could ever own.
//
// ManagedUnitNames is deliberately the superset -- it is what *removal* walks,
// so a machine that stopped following a channel still has its timer taken away.
// Checking against it reported the conditional pairs as "not installed" on
// every ordinary machine, on every run, with a remedy that could not clear
// them: a warning that always fires and cannot be fixed is a warning nobody
// reads on the run that meant something. Adding the fleet pair doubled it.
func TestDoctorDoesNotDemandUnitsThisMachineShouldNotHave(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.Deps.Supervisor = h.Supervisor
	h.Supervisor.Present = true
	ctx := context.Background()

	units, err := h.Supervisor.Units(ports.UnitParams{Product: "demo"})
	require.NoError(t, err)
	require.NoError(t, h.Supervisor.InstallUnits(ctx, units))
	for _, u := range units {
		// Enabled exactly where the supervisor asked for it, which is what
		// a reconciliation leaves behind. Marking everything loaded and
		// nothing enabled describes no machine that has ever existed.
		h.Supervisor.States[u.Name] = ports.UnitState{
			Name: u.Name, Loaded: true, Active: "active", Enabled: u.Enable,
		}
	}

	unitsCheck := func(t *testing.T) events.CheckResult {
		t.Helper()
		report, err := ops.Doctor(ctx, h.Deps)
		require.NoError(t, err)
		for _, c := range report.Results {
			if c.ID == "system.units" {
				return c
			}
		}
		t.Fatal("doctor ran no unit check")
		return events.CheckResult{}
	}

	got := unitsCheck(t)
	assert.Equal(t, events.CheckOK, got.Status,
		"a machine with no channel and no target was told four units are missing: %s", got.Message)

	// And a unit it *should* have and does not is still a finding, or the
	// fix above would have turned the check off rather than narrowed it.
	offsite := filepath.Join(t.TempDir(), "offsite")
	require.NoError(t, os.MkdirAll(offsite, 0o755))
	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Backup.Targets = []domain.BackupTargetConfig{{URL: "file://" + offsite}}
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

	got = unitsCheck(t)
	assert.Equal(t, events.CheckWarn, got.Status,
		"a target was configured and the missing fleet timer went unreported")
	assert.Contains(t, got.Message, "demo-fleet.timer")
}

// A flood cannot hide the fleet.
//
// Two bounds written apart, and their interaction is the defect. MaxFleetRows
// bounds what a writer with access to the prefix can make this reader do;
// absence is computed from the rows that came back. Left in listing order, a
// thousand junk keys push the real ones past the cap and the report says the
// whole roster is absent — turning a nuisance into twelve machines somebody
// gets out of bed for.
func TestAFloodCannotMakeTheFleetLookAbsent(t *testing.T) {
	h := fleetHarness(t)
	inst, offsite := h.withFleetTarget(t)
	ctx := context.Background()

	_, err := ops.FleetPublish(ctx, h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	// Junk that sorts before the real row on every transport that answers a
	// listing in lexical order, and enough of it to fill the cap twice.
	for i := range ops.MaxFleetRows + 20 {
		dir := filepath.Join(offsite, "fleet", "aaaa", fmt.Sprintf("inst_%05d", i))
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "status.json"),
			[]byte(`{"schema":1}`), 0o644))
	}

	report, err := ops.FleetList(ctx, h.Deps, ops.FleetListOptions{Roster: rosterFor(inst)})
	require.NoError(t, err)

	row := rowAt(t, report, inst.ID)
	assert.False(t, row.Absent,
		"a flood of junk keys pushed the fleet past the cap and reported it missing")
	assert.Equal(t, ops.FleetVerified, row.Signature,
		"the expected row was found but not read")
	assert.Zero(t, report.Absent())

	// And the flood is still reported, so the listing does not look whole.
	var truncated bool
	for _, r := range report.Rows {
		if strings.Contains(r.Problem, "listing is incomplete") {
			truncated = true
		}
	}
	assert.True(t, truncated, "the listing was cut short without saying so")
}

// A systemd that will not answer does not un-add the target.
//
// The reconciliation is derived state and the target is what was asked for, so
// an error here would describe an outcome that did not happen — and the repair
// it invites does not exist: re-running `backup target add` meets "already a
// backup target" and refuses before reaching the reconciliation, so the machine
// would be stuck exactly as it is with no command to type. `config set` fails
// on the same error deliberately, because a setting can simply be set again.
func TestATargetIsAddedEvenWhenTheTimerCannotBe(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.Deps.Supervisor = h.Supervisor
	h.Supervisor.Present = true

	registry, err := target.NewRegistry(localdir.New())
	require.NoError(t, err)
	h.Deps.Targets = registry
	h.Deps.Objects = registry
	ctx := context.Background()

	units, err := h.Supervisor.Units(ports.UnitParams{Product: "demo"})
	require.NoError(t, err)
	require.NoError(t, h.Supervisor.InstallUnits(ctx, units))

	offsite := filepath.Join(t.TempDir(), "offsite")
	require.NoError(t, os.MkdirAll(offsite, 0o755))
	h.Supervisor.Fail = map[string]error{"InstallUnits": errors.New("systemd is busy")}

	_, err = ops.TargetAdd(ctx, h.Deps, ops.TargetAddOptions{URL: "file://" + offsite})
	require.NoError(t, err, "a busy systemd made `backup target add` report a failure it did not have")

	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	require.Len(t, inst.Backup.Targets, 1, "the target the command reported adding is not there")

	// And re-running is the trap this shape avoids: it refuses, so a
	// command that had failed would have left nothing to type.
	_, err = ops.TargetAdd(ctx, h.Deps, ops.TargetAddOptions{URL: "file://" + offsite})
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Message, "already a backup target")
}

// failingSignatureFetch is a target whose signatures cannot be read back.
//
// The listing names the .minisig and the fetch does not produce it, which is a
// real shape -- a permission that covers the row and not its neighbour, an
// object removed between the listing and the read.
type failingSignatureFetch struct {
	ports.ObjectStore
	err error
}

func (s failingSignatureFetch) GetObject(
	ctx context.Context, ref ports.TargetRef, key string,
) ([]byte, error) {
	if strings.HasSuffix(key, ".minisig") {
		return nil, s.err
	}
	return s.ObjectStore.GetObject(ctx, ref, key)
}

// A signature that is there and cannot be read is not a verified row.
//
// The detection branch a sabotage sweep cannot find, because nothing in the
// design suggests mutating it: the row itself reads perfectly, so a verdict
// derived from "did the check pass" rather than "could the check run" would
// report this as unverifiable only by accident, or as verified by an early
// return nobody would think to write a test against.
func TestASignatureThatCannotBeReadIsNotAVerifiedRow(t *testing.T) {
	h := fleetHarness(t)
	inst, _ := h.withFleetTarget(t)
	ctx := context.Background()

	_, err := ops.FleetPublish(ctx, h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	h.Deps.Objects = failingSignatureFetch{ObjectStore: h.Deps.Objects, err: assert.AnError}

	report, err := ops.FleetList(ctx, h.Deps, ops.FleetListOptions{Roster: rosterFor(inst)})
	require.NoError(t, err)
	require.Len(t, report.Rows, 1)

	assert.Equal(t, ops.FleetUnverifiable, report.Rows[0].Signature)
	assert.NotEqual(t, ops.FleetVerified, report.Rows[0].Signature,
		"a row was verified against a signature this reader never read")
	assert.Contains(t, report.Rows[0].Problem, "could not be read")
	assert.Equal(t, 1, report.Problems())
}

// The sentences count correctly, which is the whole reason they are built
// rather than written.
//
// A report saying "the roster binds no key to a, b, so nothing published under
// it can be authenticated" is a report somebody stops trusting the details of,
// and the details are all this feature has.
//
// Both cases, and the singular is the one that needed finding: every test here
// happened to leave either none or *all* of the roster unkeyed, so the sentence
// an operator meets most often -- one machine added before its key was
// collected -- had no test at all. A sabotage sweep cannot report that; only
// coverage can, which is why both are run.
func TestTheReportCountsInPlural(t *testing.T) {
	entries := func(unkeyed int, inst domain.Installation) []domain.FleetRosterEntry {
		out := []domain.FleetRosterEntry{
			{Product: inst.Product, ID: inst.ID, PublicKey: inst.Signing.PublicKey},
		}
		for i := range unkeyed {
			out = append(out, domain.FleetRosterEntry{
				Product: "demo", ID: fmt.Sprintf("inst_0%dNOKEYCOLLECTEDYET", i),
			})
		}
		return out
	}

	cases := map[string]struct {
		unkeyed int
		says    string
	}{
		"one":  {unkeyed: 1, says: "under it can be authenticated"},
		"more": {unkeyed: 2, says: "under them can be authenticated"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := fleetHarness(t)
			inst, _ := h.withFleetTarget(t)
			ctx := context.Background()

			_, err := ops.FleetPublish(ctx, h.Deps, ops.FleetPublishOptions{})
			require.NoError(t, err)

			roster := domain.FleetRoster{
				Schema:        domain.FleetRosterSchemaVersion,
				Installations: entries(tc.unkeyed, inst),
			}
			require.NoError(t, roster.Validate())
			require.Len(t, roster.Unkeyed(), tc.unkeyed)

			report, err := ops.FleetList(ctx, h.Deps, ops.FleetListOptions{Roster: roster})
			require.NoError(t, err)

			joined := strings.Join(report.Limitations, " ")
			assert.Containsf(t, joined, tc.says,
				"%d unkeyed entries described as the wrong number: %s", tc.unkeyed, joined)
		})
	}
}
