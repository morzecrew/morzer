package suite

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	signer "github.com/morzecrew/morzer/internal/adapters/sign/minisign"
	"github.com/morzecrew/morzer/internal/adapters/target"
	"github.com/morzecrew/morzer/internal/adapters/target/localdir"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// The fleet read model (RFC 0026 P1 and P2).
//
// The feature's failure mode is a table that looks complete and is not. So most
// of what follows is about what the reader *refuses* to say, and it asserts
// that by reading the published bytes rather than by reading the code that
// wrote them.

// fleetHarness is a machine that can sign and can reach a target.
func fleetHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.Deps.Signer = signer.New(h.Paths.SigningKeyFile(), "demo")
	h.Deps.Checker = signer.NewChecker()
	return h
}

// withFleetTarget wires the production registry and a directory target.
func (h *harness) withFleetTarget(t *testing.T) (inst domain.Installation, offsite string) {
	t.Helper()

	registry, err := target.NewRegistry(localdir.New())
	require.NoError(t, err)
	h.Deps.Targets = registry
	h.Deps.Objects = registry

	offsite = filepath.Join(t.TempDir(), "offsite")

	inst = signingInstallation(t, h)
	inst.Backup.Targets = []domain.BackupTargetConfig{{URL: "file://" + offsite}}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	return inst, offsite
}

// publishedRow reads back what a publish put on a target.
func publishedRow(t *testing.T, offsite string, inst domain.Installation) (domain.FleetRow, []byte) {
	t.Helper()

	key, err := domain.FleetKey(inst.Product, inst.ID)
	require.NoError(t, err)

	body, err := os.ReadFile(filepath.Join(offsite, filepath.FromSlash(key)))
	require.NoError(t, err)

	var row domain.FleetRow
	require.NoError(t, json.Unmarshal(body, &row))
	return row, body
}

// A publish puts a row and its signature at the key the design names.
func TestPublishingWritesTheRowAndItsSignature(t *testing.T) {
	h := fleetHarness(t)
	inst, offsite := h.withFleetTarget(t)

	result, err := ops.FleetPublish(context.Background(), h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	report, ok := result.Data.(ops.FleetPublishReport)
	require.True(t, ok)
	require.Len(t, report.Targets, 1)
	require.True(t, report.Targets[0].Published, "the row did not reach the target")
	require.True(t, report.Signed, "the row was published unsigned by a machine with a key")

	row, _ := publishedRow(t, offsite, inst)
	assert.Equal(t, domain.FleetSchemaVersion, row.Schema)
	assert.Equal(t, "demo", row.Product)
	assert.Equal(t, inst.ID, row.InstallationID)
	assert.Equal(t, "1.2.0", row.Version)
	assert.Equal(t, domain.FleetBound, row.Bound)

	key, err := domain.FleetKey(inst.Product, inst.ID)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(offsite, filepath.FromSlash(key))+".minisig")
}

// The signature is over the bytes as published, not over a re-serialisation.
//
// RFC 0026 §3.6 turns on this: a signature over "the JSON" is a signature over
// whichever spelling of it the verifier reproduces, so it would need a
// canonical form both ends implement identically. Asserting it here means a
// change to how the row is marshalled -- indentation, key order, a trailing
// newline -- cannot silently invalidate every signature in a fleet.
func TestTheSignatureCoversTheBytesAsPublished(t *testing.T) {
	h := fleetHarness(t)
	inst, offsite := h.withFleetTarget(t)

	_, err := ops.FleetPublish(context.Background(), h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	key, err := domain.FleetKey(inst.Product, inst.ID)
	require.NoError(t, err)
	path := filepath.Join(offsite, filepath.FromSlash(key))

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	sig, err := os.ReadFile(path + ".minisig")
	require.NoError(t, err)

	require.True(t, h.Deps.Checker.Check(body, sig, inst.Signing.PublicKey),
		"the published signature does not verify against the published bytes")

	// And the negative, so the assertion above is not merely a checker that
	// says yes: one byte different and it must fail.
	require.False(t, h.Deps.Checker.Check(append(body, ' '), sig, inst.Signing.PublicKey))
}

// Nothing this payload refuses reaches the bucket.
//
// Asserted against the published bytes with markers seeded into the real
// values, rather than against the list of fields the builder sets. The second
// would be an intent-guard: it would pass on the day something serialised the
// whole installation into the row by accident.
func TestARowCarriesNoParameterValueOrSecret(t *testing.T) {
	h := fleetHarness(t)
	inst, offsite := h.withFleetTarget(t)

	inst.Parameters = map[string]string{"http_port": "PARAM-VALUE-MUST-NOT-TRAVEL-8443"}
	inst.Domains = []string{"HOSTNAME-MUST-NOT-TRAVEL.example"}
	inst.AttestationSalt = "SALT-MUST-NOT-TRAVEL-0123456789abcdef"
	inst.Backup.Targets = []domain.BackupTargetConfig{{URL: "file://" + offsite}}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	h.Secrets.Seed(map[string]string{"db_password": "SECRET-MUST-NOT-TRAVEL-hunter2"})

	_, err := ops.FleetPublish(context.Background(), h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	_, body := publishedRow(t, offsite, inst)
	for _, needle := range []string{
		"PARAM-VALUE-MUST-NOT-TRAVEL",
		"HOSTNAME-MUST-NOT-TRAVEL",
		"SALT-MUST-NOT-TRAVEL",
		"SECRET-MUST-NOT-TRAVEL",
	} {
		assert.NotContainsf(t, string(body), needle,
			"%s reached a document published to a shared target", needle)
	}
}

// A count that could not be taken is published as absent, not as zero.
//
// The end-to-end half of the domain test: a runtime that will not answer must
// not make a deployment look like one whose services are all stopped, and this
// asserts it in the bytes that leave the machine.
func TestAnUnreachableRuntimePublishesNoCountAtAll(t *testing.T) {
	h := fleetHarness(t)
	inst, offsite := h.withFleetTarget(t)

	h.Runtime.Fail = map[string]error{"Status": assert.AnError}

	_, err := ops.FleetPublish(context.Background(), h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err, "a runtime that will not answer must not fail the publish")

	row, body := publishedRow(t, offsite, inst)
	assert.Nil(t, row.Health.Running, "a count was published for a runtime that did not answer")
	assert.Nil(t, row.Health.Services)
	assert.NotEmpty(t, row.Health.Problem, "the row does not say why there is no count")
	assert.Contains(t, string(body), `"running": null`)
}

// A newer row is not replaced by an older one.
//
// The key is stable and every write replaces in place, so without the
// read-before-write a slow publisher finishing after a fast one silently
// installs stale state as current -- which is what a timer beside a manual run
// produces.
func TestAPublishDeclinesToReplaceANewerRow(t *testing.T) {
	h := fleetHarness(t)
	_, offsite := h.withFleetTarget(t)
	ctx := context.Background()

	// The future, published first.
	future := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	h.Deps.Now = func() time.Time { return future }
	_, err := ops.FleetPublish(ctx, h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	// Then a publisher whose clock -- or whose read of the world -- is
	// older.
	h.Deps.Now = func() time.Time { return future.Add(-time.Hour) }
	result, err := ops.FleetPublish(ctx, h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	report, ok := result.Data.(ops.FleetPublishReport)
	require.True(t, ok)
	require.Len(t, report.Targets, 1)
	assert.False(t, report.Targets[0].Published)
	assert.Contains(t, report.Targets[0].Declined, "newer row")

	// And the row on the target is still the newer one.
	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	row, _ := publishedRow(t, offsite, inst)
	assert.Equal(t, future, row.PublishedAt.UTC())
}

// --force replaces it, because an operator must have a way out.
func TestForceReplacesARowThePublisherWouldHaveDeclined(t *testing.T) {
	h := fleetHarness(t)
	_, offsite := h.withFleetTarget(t)
	ctx := context.Background()

	future := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	h.Deps.Now = func() time.Time { return future }
	_, err := ops.FleetPublish(ctx, h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	older := future.Add(-time.Hour)
	h.Deps.Now = func() time.Time { return older }
	result, err := ops.FleetPublish(ctx, h.Deps, ops.FleetPublishOptions{
		TargetOptions: ops.TargetOptions{Options: ops.Options{Force: true}},
	})
	require.NoError(t, err)

	report, ok := result.Data.(ops.FleetPublishReport)
	require.True(t, ok)
	assert.True(t, report.Targets[0].Published)
	assert.NotEmpty(t, report.Targets[0].Unchecked,
		"--force replaced a row without recording that nothing was compared")

	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	row, _ := publishedRow(t, offsite, inst)
	assert.Equal(t, older, row.PublishedAt.UTC())
}

// A dry run writes nothing and still shows the document.
func TestADryRunPublishesNothingAndPrintsTheRow(t *testing.T) {
	h := fleetHarness(t)
	_, offsite := h.withFleetTarget(t)

	result, err := ops.FleetPublish(context.Background(), h.Deps, ops.FleetPublishOptions{
		TargetOptions: ops.TargetOptions{Options: ops.Options{DryRun: true}},
	})
	require.NoError(t, err)

	report, ok := result.Data.(ops.FleetPublishReport)
	require.True(t, ok)
	assert.Equal(t, domain.FleetSchemaVersion, report.Row.Schema,
		"--dry-run described the row instead of producing it")
	assert.NoDirExists(t, filepath.Join(offsite, "fleet"))
}

// The round trip: publish, then read it back.
func TestFleetListReadsBackWhatWasPublished(t *testing.T) {
	h := fleetHarness(t)
	inst, _ := h.withFleetTarget(t)
	ctx := context.Background()

	_, err := ops.FleetPublish(ctx, h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	report, err := ops.FleetList(ctx, h.Deps, ops.FleetListOptions{})
	require.NoError(t, err)

	require.Len(t, report.Rows, 1)
	row := report.Rows[0]
	require.Empty(t, row.Problem)
	require.NotNil(t, row.Row)
	assert.Equal(t, "demo", row.Product)
	assert.Equal(t, inst.ID, row.InstallationID)
	assert.Equal(t, "1.2.0", row.Row.Version)
	assert.Zero(t, report.Problems())
}

// The reader never says a row is verified, whatever it found.
//
// This is the test RFC 0026 §8 makes P2 conditional on. The row's own key
// checks out perfectly -- that is exactly the point -- and the reader must
// still report only that a signature is *there*, because the machine
// overwriting its neighbour's row rewrites payload, key and signature together.
// A reader that graduated to "verified" here would reintroduce the defect
// decision 6b removed, as a phase boundary.
func TestTheReaderNeverClaimsARowIsVerified(t *testing.T) {
	h := fleetHarness(t)
	h.withFleetTarget(t)
	ctx := context.Background()

	_, err := ops.FleetPublish(ctx, h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	report, err := ops.FleetList(ctx, h.Deps, ops.FleetListOptions{})
	require.NoError(t, err)

	require.Len(t, report.Rows, 1)
	assert.Equal(t, ops.FleetSigned, report.Rows[0].Signature)

	// Not a value this vocabulary has, and the assertion is on the whole
	// type rather than on one row: a third state would have to be added
	// deliberately.
	for _, forbidden := range []ops.FleetSignature{"verified", "valid", "trusted"} {
		assert.NotEqual(t, forbidden, report.Rows[0].Signature)
	}
}

// And it says so, on every run, in the report itself.
//
// Not in the documentation: an operator reading a complete-looking table is not
// reading the documentation. §8 permits this phase to ship without a roster
// only because the reader states both limitations, and the two are stated
// together because they have one cause.
func TestTheReaderStatesWhatItCannotDo(t *testing.T) {
	h := fleetHarness(t)
	h.withFleetTarget(t)
	ctx := context.Background()

	_, err := ops.FleetPublish(ctx, h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	report, err := ops.FleetList(ctx, h.Deps, ops.FleetListOptions{})
	require.NoError(t, err)

	require.NotEmpty(t, report.Limitations,
		"the reader presented a table with no statement of what it could not see")

	joined := strings.ToLower(strings.Join(report.Limitations, " "))
	assert.Contains(t, joined, "roster")
	assert.Contains(t, joined, "authenticated")
	assert.Contains(t, joined, "absent")
}

// A row nobody can read is a row, not an omission.
//
// Decision 4, asserted against the three ways a row goes bad: bytes that are
// not JSON, a document from a manager this one is too old to read, and a row
// published at a key naming a different installation.
func TestAnUnreadableRowIsShownCarryingItsProblem(t *testing.T) {
	h := fleetHarness(t)
	_, offsite := h.withFleetTarget(t)
	ctx := context.Background()

	write := func(product, id string, body []byte) {
		key, err := domain.FleetKey(product, id)
		require.NoError(t, err)
		path := filepath.Join(offsite, filepath.FromSlash(key))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, body, 0o644))
	}

	write("garbage", "inst_A", []byte("this is not JSON at all"))
	write("future", "inst_B", []byte(`{"schema":99,"product":"future",`+
		`"installation_id":"inst_B","published_at":"2026-08-13T10:00:00Z"}`))
	write("impostor", "inst_C", []byte(`{"schema":1,"product":"somebodyelse",`+
		`"installation_id":"inst_C","published_at":"2026-08-13T10:00:00Z"}`))

	report, err := ops.FleetList(ctx, h.Deps, ops.FleetListOptions{})
	require.NoError(t, err)

	problems := map[string]string{}
	for _, row := range report.Rows {
		if row.Problem != "" {
			problems[row.Product] = row.Problem
		}
	}

	require.Len(t, problems, 3, "a row that could not be read was dropped instead of shown")
	assert.Contains(t, problems["garbage"], "not a fleet row")
	assert.Contains(t, problems["future"], "newer manager")
	assert.Contains(t, problems["impostor"], "not the installation whose key it is at")
	assert.Equal(t, 3, report.Problems())
}

// A key that climbs out of the prefix is a finding, never a fetch.
//
// The keys come out of a listing of a bucket several machines write to, so this
// layer must not depend on being saved by the transport underneath it.
func TestAKeyThatEscapesThePrefixIsAFinding(t *testing.T) {
	h := fleetHarness(t)
	_, offsite := h.withFleetTarget(t)

	// Written under the prefix by hand, at a depth FleetKey would never
	// have produced.
	path := filepath.Join(offsite, "fleet", "demo", "inst_A", "nested", "status.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(`{"schema":1}`), 0o644))

	report, err := ops.FleetList(context.Background(), h.Deps, ops.FleetListOptions{})
	require.NoError(t, err)

	require.Len(t, report.Rows, 1)
	assert.Nil(t, report.Rows[0].Row, "a key that is not a row's key was fetched anyway")
	assert.Contains(t, report.Rows[0].Problem, "not a fleet row's key")
}

// A signature with no row beside it is worth a line of its own.
func TestASignatureWithNoRowIsReported(t *testing.T) {
	h := fleetHarness(t)
	_, offsite := h.withFleetTarget(t)

	path := filepath.Join(offsite, "fleet", "demo", "inst_A", "status.json.minisig")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("untrusted comment: x\n"), 0o644))

	report, err := ops.FleetList(context.Background(), h.Deps, ops.FleetListOptions{})
	require.NoError(t, err)

	require.Len(t, report.Rows, 1)
	assert.Contains(t, report.Rows[0].Problem, "a signature with no row")
}

// Staleness is judged against a threshold the report states.
func TestAStaleRowIsMarkedAndTheThresholdIsStated(t *testing.T) {
	h := fleetHarness(t)
	h.withFleetTarget(t)
	ctx := context.Background()

	published := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	_, err := ops.FleetPublish(ctx, h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	// Two days later, read with the default threshold.
	h.Deps.Now = func() time.Time { return published.Add(48 * time.Hour) }
	report, err := ops.FleetList(ctx, h.Deps, ops.FleetListOptions{})
	require.NoError(t, err)

	require.Len(t, report.Rows, 1)
	assert.True(t, report.Rows[0].Stale)
	assert.Equal(t, 1, report.Stale())
	assert.NotEmpty(t, report.StaleAfter,
		"a staleness verdict was rendered without saying what it was judged against")

	// A negative threshold judges nothing, which a reader who publishes
	// weekly must be able to say.
	report, err = ops.FleetList(ctx, h.Deps, ops.FleetListOptions{StaleAfter: -1})
	require.NoError(t, err)
	assert.False(t, report.Rows[0].Stale)
	assert.Empty(t, report.StaleAfter)

	// Staleness is not a problem: it is a judgement against a threshold,
	// and a machine deliberately published weekly must not fail a check
	// that defaults to a day.
	assert.Zero(t, report.Problems())
}

// An installation with no targets publishes nowhere, and says so rather than
// failing.
func TestPublishingWithNoTargetsIsRefusedWithAdvice(t *testing.T) {
	h := newHarness(t)
	registry, err := target.NewRegistry(localdir.New())
	require.NoError(t, err)
	h.Deps.Targets = registry
	h.Deps.Objects = registry
	h.install()

	_, err = ops.FleetPublish(context.Background(), h.Deps, ops.FleetPublishOptions{})
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Message, "no backup targets")
}

// A machine that has never minted a key publishes anyway, unsigned.
//
// Withholding the row would hide the installations with the least evidence,
// which is the wrong half of a fleet to go quiet.
func TestAMachineWithNoKeyPublishesAnUnsignedRow(t *testing.T) {
	h := newHarness(t)

	registry, err := target.NewRegistry(localdir.New())
	require.NoError(t, err)
	h.Deps.Targets = registry
	h.Deps.Objects = registry
	h.Deps.Signer = nil

	offsite := filepath.Join(t.TempDir(), "offsite")
	inst := h.install()
	inst.Backup.Targets = []domain.BackupTargetConfig{{URL: "file://" + offsite}}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	result, err := ops.FleetPublish(context.Background(), h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	report, ok := result.Data.(ops.FleetPublishReport)
	require.True(t, ok)
	require.True(t, report.Targets[0].Published)
	assert.False(t, report.Signed)

	list, err := ops.FleetList(context.Background(), h.Deps, ops.FleetListOptions{})
	require.NoError(t, err)
	require.Len(t, list.Rows, 1)
	assert.Equal(t, ops.FleetUnsigned, list.Rows[0].Signature)
}

// A dry run mints nothing.
//
// `--dry-run` is documented as "plan only, make no changes", and the row names
// the key that will sign it -- so resolving that key through EnsureKey made a
// planning command generate cryptographic material on a machine that had never
// signed. The file is the identity every later signature is attributed to, and
// creating one is not a plan.
func TestADryRunDoesNotMintASigningKey(t *testing.T) {
	h := newHarness(t)

	registry, err := target.NewRegistry(localdir.New())
	require.NoError(t, err)
	h.Deps.Targets = registry
	h.Deps.Objects = registry
	h.Deps.Signer = signer.New(h.Paths.SigningKeyFile(), "demo")

	offsite := filepath.Join(t.TempDir(), "offsite")
	inst := h.install()
	inst.Backup.Targets = []domain.BackupTargetConfig{{URL: "file://" + offsite}}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	require.NoFileExists(t, h.Paths.SigningKeyFile(),
		"this machine already has a key, so the test proves nothing")

	result, err := ops.FleetPublish(context.Background(), h.Deps, ops.FleetPublishOptions{
		TargetOptions: ops.TargetOptions{Options: ops.Options{DryRun: true}},
	})
	require.NoError(t, err)

	assert.NoFileExists(t, h.Paths.SigningKeyFile(),
		"a --dry-run publish minted this machine's signing identity")

	// And the report says so. Without this the fix could report `signed`
	// from a key it declined to create, which is the plan describing an
	// outcome the real run would not produce.
	report, ok := result.Data.(ops.FleetPublishReport)
	require.True(t, ok)
	assert.False(t, report.Signed,
		"the plan says the row would be signed, on a machine with no key to sign it")
	assert.Empty(t, report.Row.SigningKey)
}

// failingSignatureStore is a target that accepts the row and refuses the
// signature.
//
// It exists to assert an ordering that nothing else can see: which of the two
// objects is written first. A transport that never fails gives the same result
// either way.
type failingSignatureStore struct {
	ports.ObjectStore
	err error
}

func (s failingSignatureStore) PutObject(
	ctx context.Context, ref ports.TargetRef, key string, data []byte,
) error {
	if strings.HasSuffix(key, ".minisig") {
		return s.err
	}
	return s.ObjectStore.PutObject(ctx, ref, key, data)
}

// The row goes first, so an interrupted publish leaves something readable.
//
// The same rule that puts a backup's manifest last, and the same reasoning: a
// row nobody can check is honest, and a signature over bytes that are not there
// is not. It is worth pinning because the wrong order is invisible on a healthy
// target and produces, on an unhealthy one, exactly the state `fleet ls` reports
// as "a signature with no row beside it".
func TestTheRowIsWrittenBeforeItsSignature(t *testing.T) {
	h := fleetHarness(t)
	inst, offsite := h.withFleetTarget(t)

	h.Deps.Objects = failingSignatureStore{
		ObjectStore: h.Deps.Objects,
		err:         assert.AnError,
	}

	result, err := ops.FleetPublish(context.Background(), h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	report, ok := result.Data.(ops.FleetPublishReport)
	require.True(t, ok)
	require.NotEmpty(t, report.Targets[0].Error,
		"a signature that did not reach the target was reported as a clean publish")

	key, err := domain.FleetKey(inst.Product, inst.ID)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(offsite, filepath.FromSlash(key)),
		"the signature was written before the row, so an interrupted publish "+
			"leaves a signature over bytes that are not there")
	assert.NoFileExists(t, filepath.Join(offsite, filepath.FromSlash(key))+".minisig")
}

// A configuration target that cannot be read is not drift.
//
// An absent file is drift -- the release renders something and the machine has
// nothing. A file that cannot be *read* is a permission fault, and counting it
// would publish "1 target differs" for a machine where nothing changed at all,
// on the row an operator scans to decide which machine to go and look at.
func TestAnUnreadableConfigurationTargetIsNotCountedAsDrift(t *testing.T) {
	h := fleetHarness(t)
	inst, offsite := h.withFleetTarget(t)
	ctx := context.Background()

	// Applied first, so the rendered files exist and there is genuinely no
	// drift to find -- otherwise a count of one could come from anywhere.
	_, err := ops.Apply(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)

	_, err = ops.FleetPublish(ctx, h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)
	before, _ := publishedRow(t, offsite, inst)
	require.NotNil(t, before.Drift.Targets)
	require.Zero(t, *before.Drift.Targets, "this deployment has drifted, so the test proves nothing")
	require.Empty(t, before.Drift.Problem)

	// A directory where a file belongs: os.ReadFile returns EISDIR, which is
	// not fs.ErrNotExist. A permission bit would make this test pass for the
	// wrong reason under `sudo just ci`, where root reads a 0000 file.
	target := filepath.Join(h.Root, "etc", "demo", "application.yaml")
	require.NoError(t, os.Remove(target))
	require.NoError(t, os.Mkdir(target, 0o755))

	h.Deps.Now = func() time.Time { return time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC) }
	_, err = ops.FleetPublish(ctx, h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	after, _ := publishedRow(t, offsite, inst)
	require.NotNil(t, after.Drift.Targets)
	assert.Zero(t, *after.Drift.Targets,
		"a configuration target that could not be read was counted as drift")
	assert.Contains(t, after.Drift.Problem, "could not be read",
		"the row does not say a target was left out of the count")
}

// escSeq is the escape character, built rather than written so this file holds
// no control characters of its own.
var escSeq = string(rune(27))

// Text a hostile machine put in a row does not reach the terminal.
//
// The mirror of the rule the payload already followed on the way out, and the
// half that was missing. A row is read off a target several machines can write
// to, so its strings are chosen by whoever holds one of those credentials --
// and `encoding/json` refusing a *raw* control byte is not the check: the
// escaped spelling below is legal JSON and decodes to the same character.
func TestHostileTextInARowNeverReachesTheReader(t *testing.T) {
	h := fleetHarness(t)
	_, offsite := h.withFleetTarget(t)

	dir := filepath.Join(offsite, "fleet", "demo", "inst_HOSTILE")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	body := `{"schema":1,"product":"demo","installation_id":"inst_HOSTILE",` +
		`"version":"1.0.0\u001b[2J\u001b[1;1HALL SYSTEMS NOMINAL",` +
		`"manager_version":"` + strings.Repeat("A", 5000) + `",` +
		`"health":{"services":null,"running":null,"problem":"\u001b[31mred"},` +
		`"drift":{"targets":null},` +
		`"published_at":"2026-08-03T11:00:00Z"}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "status.json"), []byte(body), 0o644))

	report, err := ops.FleetList(context.Background(), h.Deps, ops.FleetListOptions{})
	require.NoError(t, err)
	require.Len(t, report.Rows, 1)

	row := report.Rows[0].Row
	require.NotNil(t, row, "the row was refused for another reason: %s", report.Rows[0].Problem)

	assert.NotContains(t, row.Version, escSeq,
		"an escape sequence from a published row reached the reader")
	assert.NotContains(t, row.Health.Problem, escSeq)
	assert.LessOrEqual(t, len(row.ManagerVersion), domain.MaxAttestedText+len("… [truncated]"),
		"unbounded remote text reached the reader")
}

// The same for a key, which is the path where there is no row to sanitise.
//
// A key that will not parse produces a status carrying the key itself, and the
// table prints it in both the name column and the diagnostics. The key came out
// of somebody else's listing.
func TestAHostileKeyNeverReachesTheReader(t *testing.T) {
	h := fleetHarness(t)
	_, offsite := h.withFleetTarget(t)

	dir := filepath.Join(offsite, "fleet", "demo"+escSeq+"[2Jgotcha")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "status.json"), []byte("{}"), 0o644))

	report, err := ops.FleetList(context.Background(), h.Deps, ops.FleetListOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, report.Rows)

	for _, r := range report.Rows {
		assert.NotContains(t, r.Key, escSeq,
			"an escape sequence from a remote key reached the reader")
		assert.NotContains(t, r.Problem, escSeq)
	}
}

// Sanitising must not become a way past the identity check.
//
// The row's product is compared with the key's *before* the row is bounded. The
// other order would let a row naming `demo` plus a trailing escape become plain
// `demo` and be accepted at a key it does not belong at -- turning the one
// integrity check this phase has into a formality.
func TestBoundingDoesNotLetARowClaimAnotherKey(t *testing.T) {
	h := fleetHarness(t)
	_, offsite := h.withFleetTarget(t)

	dir := filepath.Join(offsite, "fleet", "demo", "inst_SNEAKY")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	body := `{"schema":1,"product":"demo\u001b","installation_id":"inst_SNEAKY",` +
		`"health":{"services":null,"running":null},"drift":{"targets":null},` +
		`"published_at":"2026-08-03T11:00:00Z"}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "status.json"), []byte(body), 0o644))

	report, err := ops.FleetList(context.Background(), h.Deps, ops.FleetListOptions{})
	require.NoError(t, err)
	require.Len(t, report.Rows, 1)

	assert.Nil(t, report.Rows[0].Row,
		"a row whose product only matches once sanitised was accepted at this key")
	assert.Contains(t, report.Rows[0].Problem, "not the installation whose key it is at")
}

// A neighbouring prefix is not this namespace.
//
// A listing prefix is a string match, so asking for `fleet` is answered with
// `fleet-old/...` too. Those became rows carrying "not a fleet row's key", and
// a problem row makes `fleet ls` exit non-zero -- so an unrelated directory on
// a shared target turned a healthy fleet into a failing command.
func TestANeighbouringPrefixIsNotTheFleet(t *testing.T) {
	h := fleetHarness(t)
	_, offsite := h.withFleetTarget(t)
	ctx := context.Background()

	_, err := ops.FleetPublish(ctx, h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	for _, stray := range []string{"fleet-old/demo/notes.txt", "fleetsomething/x.json"} {
		path := filepath.Join(offsite, filepath.FromSlash(stray))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte("not ours"), 0o644))
	}

	report, err := ops.FleetList(ctx, h.Deps, ops.FleetListOptions{})
	require.NoError(t, err)

	require.Len(t, report.Rows, 1, "a neighbouring prefix leaked into the fleet listing")
	assert.Zero(t, report.Problems(),
		"an unrelated directory on the target made `fleet ls` report a problem")
}

// A flooded prefix is bounded, and the bound is reported.
//
// The per-object cap bounds each fetch and not the number of them, and the
// number is chosen by whoever can write to the prefix. A silent truncation
// would be worse than the flood: a shorter table that looks complete is exactly
// what this design is written against.
func TestAFloodedPrefixIsBoundedAndSaysSo(t *testing.T) {
	h := fleetHarness(t)
	_, offsite := h.withFleetTarget(t)

	for i := range ops.MaxFleetRows + 20 {
		dir := filepath.Join(offsite, "fleet", "flood", fmt.Sprintf("inst_%04d", i))
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "status.json"),
			[]byte(`{"schema":1}`), 0o644))
	}

	report, err := ops.FleetList(context.Background(), h.Deps, ops.FleetListOptions{})
	require.NoError(t, err)

	assert.LessOrEqual(t, len(report.Rows), ops.MaxFleetRows+1,
		"the listing fetched every key a writer chose to create")

	var truncated bool
	for _, r := range report.Rows {
		if strings.Contains(r.Problem, "listing is incomplete") {
			truncated = true
		}
	}
	assert.True(t, truncated, "the listing was cut short without saying so")
}

// A row that reached the target is reported as published, even when its
// signature did not.
//
// `Published` says the row reached the target. Collapsing it with "everything
// worked" left the report saying `published: false` about an object sitting on
// the target, which a --json consumer reads as "nothing is there" and an
// operator reads as a row to go and re-send.
func TestAPartialPublishReportsTheRowAsPublished(t *testing.T) {
	h := fleetHarness(t)
	inst, offsite := h.withFleetTarget(t)

	h.Deps.Objects = failingSignatureStore{ObjectStore: h.Deps.Objects, err: assert.AnError}

	result, err := ops.FleetPublish(context.Background(), h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	report, ok := result.Data.(ops.FleetPublishReport)
	require.True(t, ok)
	require.Len(t, report.Targets, 1)

	assert.True(t, report.Targets[0].Published,
		"the row is on the target and the report says it is not")
	assert.NotEmpty(t, report.Targets[0].Error,
		"the signature failure was swallowed along with it")
	assert.Contains(t, result.Summary, "signature did not",
		"the summary counts a partial publish as a target that did not answer")

	key, err := domain.FleetKey(inst.Product, inst.ID)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(offsite, filepath.FromSlash(key)))
}

// The overwrite, as far as this phase can play it out.
//
// RFC 0026 §6 owes P3 the full scenario, which needs the roster. What is
// testable now is the half that does not: a second installation, with its own
// valid signing key, writing its own genuinely signed row over the first's key.
// The reader must keep it as a problem row rather than showing it as the first
// installation's status.
func TestAnotherInstallationsRowAtThisKeyIsAProblem(t *testing.T) {
	h := fleetHarness(t)
	victim, offsite := h.withFleetTarget(t)
	ctx := context.Background()

	_, err := ops.FleetPublish(ctx, h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	victimKey, err := domain.FleetKey(victim.Product, victim.ID)
	require.NoError(t, err)

	// A second machine, with a key of its own, publishing its own row.
	attacker := fleetHarness(t)
	attackerInst, attackerSite := attacker.withFleetTarget(t)

	// A different installation, which the shared harness does not give for
	// free: both machines are seeded with the same id, and a test in which
	// the two keys coincide would prove nothing about an overwrite.
	attackerInst.ID = "inst_01ATTACKERINSTALLATION"
	require.NoError(t, attacker.Deps.State.SaveInstallation(ctx, attackerInst))

	_, err = ops.FleetPublish(ctx, attacker.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	attackerKey, err := domain.FleetKey(attackerInst.Product, attackerInst.ID)
	require.NoError(t, err)
	require.NotEqual(t, victimKey, attackerKey, "both harnesses produced the same key")

	// Payload, embedded public key and detached signature, all replaced
	// together -- which is what makes the row verify perfectly against
	// itself, and why the row's own key can never be the anchor.
	for _, ext := range []string{"", ".minisig"} {
		src := filepath.Join(attackerSite, filepath.FromSlash(attackerKey)) + ext
		dst := filepath.Join(offsite, filepath.FromSlash(victimKey)) + ext
		data, readErr := os.ReadFile(src)
		require.NoError(t, readErr)
		require.NoError(t, os.WriteFile(dst, data, 0o644))
	}

	report, err := ops.FleetList(ctx, h.Deps, ops.FleetListOptions{})
	require.NoError(t, err)
	require.Len(t, report.Rows, 1)

	assert.Nil(t, report.Rows[0].Row,
		"another installation's row was displayed as this installation's status")
	assert.Contains(t, report.Rows[0].Problem, "not the installation whose key it is at")
	assert.Equal(t, 1, report.Problems())
}

// failingJournalStore is a state store whose journal cannot be read.
//
// A double rather than a filesystem trick: `os.Open` succeeds on a directory
// and `chmod 000` does nothing under root, so both of the obvious ways to make
// the journal unreadable pass for the wrong reason on some machine. This fails
// exactly the call under test and leaves every other read working, which is
// also the real shape of the fault -- installation state is readable, the
// journal is not.
type failingJournalStore struct {
	ports.StateStore
	err error
}

func (s failingJournalStore) UnfinishedOperations(context.Context) ([]domain.OperationRecord, error) {
	return nil, s.err
}

// A journal that cannot be read is not an attention count of zero.
//
// `Attention` is an int and cannot spell "I could not look" the way the service
// counts can, so a failed read published a confident zero -- on the machine
// most likely to have something flagged. The row says it in the one field that
// exists for saying things, and only when the runtime has not already claimed
// it: two explanations on one line would bury the first.
func TestAnUnreadableJournalIsNotZeroAttention(t *testing.T) {
	h := fleetHarness(t)
	inst, offsite := h.withFleetTarget(t)

	h.Deps.State = failingJournalStore{StateStore: h.Deps.State, err: assert.AnError}

	_, err := ops.FleetPublish(context.Background(), h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err, "an unreadable journal must not fail the publish")

	row, _ := publishedRow(t, offsite, inst)
	assert.Zero(t, row.Health.Attention)
	assert.Contains(t, row.Health.Problem, "journal",
		"a journal that could not be read published as a confident attention count of zero")
}
