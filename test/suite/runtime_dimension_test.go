package suite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// The runtime an installation is fixed to, at the two places RFC 0023 P2 left
// it unasserted: the second creation path, and the command an operator runs to
// find out whether this machine works.
//
// Both are the same shape of failure. The runtime does not transition
// (decision 3), so a machine that records the wrong one, or records none, is
// not a setting anybody can correct -- it is a rebuild.

func TestAnImportCarriesTheRecordedRuntime(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()

	recoveryPath, recoveryPub := generateRecoveryKey(t)
	origin := initOriginMachine(t, ctx, recoveryPub)

	originInst, err := origin.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, originInst.Runtime,
		"init must record the runtime, or there is nothing here for import to carry")

	exportPath := filepath.Join(t.TempDir(), "demo.export.yaml")
	_, err = ops.Export(ctx, origin.Deps, ops.ExportOptions{Path: exportPath})
	require.NoError(t, err)

	export, err := ops.LoadExport(exportPath)
	require.NoError(t, err)
	// Asserted on the document as well as on the rebuilt machine. An
	// export that dropped the field would still import perfectly cleanly,
	// because empty is a legal value that means "created before schema 9".
	assert.Equal(t, originInst.Runtime, export.Installation.Runtime,
		"the export document must carry the runtime; it is the only copy that leaves the machine")

	rebuilt := newMachine(t, t.TempDir())
	_, err = ops.Import(ctx, rebuilt.Deps, ops.ImportOptions{
		SourcePath:   exportPath,
		Export:       export,
		IdentityFile: recoveryPath,
	})
	require.NoError(t, err)

	rebuiltInst, err := rebuilt.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	// The raw field, deliberately not RuntimeName(): the accessor reads
	// empty as the legacy runtime, so it would answer "compose" for an
	// import that had dropped the value entirely -- which is the failure
	// this test exists to catch.
	assert.Equal(t, originInst.Runtime, rebuiltInst.Runtime,
		"the rebuilt machine must be fixed to the runtime the lost one was")
}

func TestAnImportRefusesARuntimeThisManagerCannotDrive(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	_, err := ops.Import(ctx, h.Deps, ops.ImportOptions{
		SourcePath: "irrelevant",
		Export:     exportOfAnInstallationOn("quadlet"),
		// Never reached: the refusal is about this manager rather than
		// about the export, so it arrives before the recovery key is
		// even looked at.
		IdentityFile: "/nonexistent",
	})
	require.Error(t, err)

	// Both names, because "unsupported runtime" sends an operator looking
	// for the wrong manager. What they need to know is which one this
	// binary is, and which one their export needs.
	assert.Contains(t, err.Error(), "quadlet")
	assert.Contains(t, err.Error(), "compose")
	assert.Contains(t, domain.AsError(err).Hint, "restore",
		"a refusal with no way forward is where a recovery stops")

	// Refused with nothing created, like every other refusal about what
	// this machine can be. A half-built machine is the worst outcome
	// available during a rebuild.
	assert.NoFileExists(t, h.Paths.InstallationFile())
	assert.NoFileExists(t, h.Paths.SecretsFile())
}

// A dry run refuses too. It plans an import that could never work otherwise,
// and an operator reading "would import installation ..." has been told the
// opposite of what the real run will say.
func TestAnImportDryRunRefusesTheSameRuntime(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	_, err := ops.Import(ctx, h.Deps, ops.ImportOptions{
		SourcePath:   "irrelevant",
		Export:       exportOfAnInstallationOn("quadlet"),
		IdentityFile: "/nonexistent",
		Options:      ops.Options{DryRun: true},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "quadlet")
}

// exportOfAnInstallationOn builds the smallest export that reaches the runtime
// refusal: valid enough to pass document validation, and nothing more.
func exportOfAnInstallationOn(runtime string) domain.InstallationExport {
	return domain.InstallationExport{
		APIVersion: domain.APIVersionV1Alpha1,
		Kind:       domain.KindInstallationExport,
		Installation: domain.Installation{
			SchemaVersion: domain.InstallationSchemaVersion,
			ID:            "inst_01FROMANOTHERMACHINE",
			Product:       "demo",
			Runtime:       runtime,
		},
		Secrets: domain.ExportedSecrets{
			State: "sops: {}",
			Recipients: []domain.ExportedRecipient{
				{PublicKey: "age1recovery", Kind: domain.RecipientKindRecovery},
			},
		},
	}
}

func TestDoctorNamesTheRuntimeAnInstallationIsFixedTo(t *testing.T) {
	h := newHarness(t)
	h.install()

	report, err := ops.Doctor(context.Background(), h.Deps)
	require.NoError(t, err)

	declared := findResult(t, report, "runtime.declared")
	assert.Equal(t, "ok", declared.Status)
	assert.Contains(t, declared.Message, "compose",
		"an operator comparing two machines needs the name, not a tick")
}

func TestDoctorRefusesAnInstallationThisManagerCannotDrive(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	inst := h.install()

	// The state of a machine whose export was imported by the wrong
	// manager, or whose installation file was hand-edited. Nothing else
	// changes: the release is fine, the secrets are fine, and every other
	// check passes.
	inst.Runtime = "quadlet"
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))

	report, err := ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)

	declared := findResult(t, report, "runtime.declared")
	assert.Equal(t, "fail", declared.Status)
	assert.Contains(t, declared.Message, "quadlet")
	assert.Contains(t, declared.Message, "compose")
	// The remedy is the whole point of reporting it here rather than
	// letting `apply` fail: there is no fix on this machine, and saying so
	// is what stops somebody editing the field back and losing a volume.
	assert.Contains(t, declared.Remedy, "does not transition")
}

// With no installation, `doctor` still says what `init` will need -- and which
// tools those are is the adapter's answer, not this layer's. RFC 0023 §2.2
// listed the hard-coded one as a leak no checker can see.
func TestDoctorAsksTheRuntimeWhichToolsInitWillNeed(t *testing.T) {
	h := newHarness(t)

	report, err := ops.Doctor(context.Background(), h.Deps)
	require.NoError(t, err)

	ids := make(map[string]bool, len(report.Results))
	for _, r := range report.Results {
		ids[r.ID] = true
	}
	for _, want := range h.Runtime.RequiredTools() {
		assert.True(t, ids["tools."+want],
			"the runtime asked for %q and doctor did not check it", want)
	}
	assert.True(t, ids["tools.sops"],
		"the secret provider is not the runtime and is still checked by name")
}

// An adapter that is not a separate binary -- one built in, or reached over a
// socket -- has no tool to name. The capability is optional so that it can say
// so, and `doctor` then checks nothing rather than checking a guess.
func TestARuntimeThatNamesNoToolsIsNotGuessedAt(t *testing.T) {
	h := newHarness(t)
	// Embedding the interface rather than the fake: only the interface's
	// own methods are promoted, so this value genuinely does not implement
	// the capability. Wrapping the concrete type would have promoted
	// RequiredTools and tested nothing.
	h.Deps.Runtime = runtimeWithoutTools{h.Runtime}

	report, err := ops.Doctor(context.Background(), h.Deps)
	require.NoError(t, err)

	for _, r := range report.Results {
		assert.NotContains(t, r.ID, "tools.docker",
			"a runtime that named no tools must not have one assumed for it")
	}
}

type runtimeWithoutTools struct{ ports.Runtime }
