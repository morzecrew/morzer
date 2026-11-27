package ops

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/state"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/fakes"
)

// Which runtime an installation is fixed to, at the moment it is fixed.
// RFC 0023 decisions 3 and 5, and G5, which retired decision 12
// (logs/quadlet-adapter.md, "The wiring").

func releaseDeclaring(runtimes domain.Runtimes) domain.Release {
	return domain.Release{
		Manifest: domain.Manifest{Runtimes: runtimes},
		Root:     "/opt/demo/releases/1",
	}
}

func TestASingleDeclaredRuntimeIsWhatTheInstallationIsFixedTo(t *testing.T) {
	got, err := runtimeForRelease(releaseDeclaring(domain.Runtimes{
		"quadlet": {Files: []string{"app.container"}},
	}), "")

	require.NoError(t, err)
	assert.Equal(t, "quadlet", got,
		"the recorded runtime is the one the release declares, not the one the manager happens to have")
}

// G5. Of two declared runtimes the operator names one, and none is picked
// without: picking would produce a working machine on a substrate nobody chose,
// and since the runtime never transitions, nothing downstream would contradict
// it.
func TestAReleaseDeclaringSeveralRuntimesNeedsOneNamed(t *testing.T) {
	both := releaseDeclaring(domain.Runtimes{
		"compose": {Files: []string{"compose.yaml"}},
		"quadlet": {Files: []string{"app.container"}},
	})

	got, err := runtimeForRelease(both, "")
	require.Error(t, err)
	assert.Empty(t, got, "a refusal must not also return a runtime somebody might record")
	assert.Contains(t, err.Error(), "choose one with --runtime")
	var domErr *domain.Error
	require.ErrorAs(t, err, &domErr)
	assert.Contains(t, domErr.Hint, "compose, quadlet")

	got, err = runtimeForRelease(both, "quadlet")
	require.NoError(t, err)
	assert.Equal(t, "quadlet", got)
}

// A name the release does not declare is a typo or the wrong bundle, refused
// whatever the release declares, rather than recorded.
func TestARuntimeTheReleaseDoesNotDeclareIsRefused(t *testing.T) {
	_, err := runtimeForRelease(releaseDeclaring(domain.Runtimes{
		"compose": {Files: []string{"compose.yaml"}},
	}), "quadlet")
	assert.ErrorContains(t, err, "does not declare the quadlet runtime")

	legacy := (domain.Installation{}).RuntimeName()
	got, err := runtimeForRelease(releaseDeclaring(nil), legacy)
	require.NoError(t, err, "a release declaring nothing runs on the legacy runtime, so naming it is no conflict")
	assert.Empty(t, got, "and still records nothing")
	_, err = runtimeForRelease(releaseDeclaring(nil), "quadlet")
	assert.ErrorContains(t, err, "declares no runtime")
}

// The root wires every adapter this host can run, and the installation a
// command is on picks among them by the name it records (G5).
func TestTheWiredRuntimeIsTheOneTheInstallationRecords(t *testing.T) {
	ctx := context.Background()
	paths := domain.PathsUnder(t.TempDir(), "demo")
	for _, dir := range paths.ManagedDirs() {
		require.NoError(t, os.MkdirAll(dir.Path, os.FileMode(dir.Mode)))
	}
	first := &fakes.Runtime{RuntimeName: "compose"}
	second := &fakes.Runtime{RuntimeName: "quadlet"}
	d := &Deps{Paths: paths, State: state.New(paths), Runtime: first, Runtimes: []ports.Runtime{first, second}}

	d.SelectRuntime(ctx)
	assert.Same(t, first, d.Runtime, "with no installation the default stays")

	require.NoError(t, d.State.SaveInstallation(ctx, domain.Installation{
		SchemaVersion: domain.InstallationSchemaVersion, ID: "i", Product: "demo", Runtime: "quadlet",
	}))
	d.SelectRuntime(ctx)
	assert.Same(t, second, d.Runtime)
	_, ok := d.drivesRuntime(domain.Installation{Runtime: "quadlet"})
	assert.True(t, ok)
}

// A runtime this host has no adapter for is refused when the installation is
// created, not by the first operation after it.
func TestARuntimeWithNoAdapterHereIsRefusedWhenChosen(t *testing.T) {
	first := &fakes.Runtime{RuntimeName: "compose"}
	d := &Deps{Runtime: first, Runtimes: []ports.Runtime{first}}

	err := d.useRuntime("quadlet")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot drive the quadlet runtime on this host; it drives compose")
	assert.Same(t, first, d.Runtime)

	require.NoError(t, d.useRuntime(""), "nothing named is the legacy runtime")
	assert.NoError(t, (&Deps{}).useRuntime("quadlet"), "a Deps with no runtime refuses nothing")
}

// A release declaring nothing at all records nothing, and
// Installation.RuntimeName reads that as the legacy runtime. Recording a
// default here instead would make "created before the field" and "chose the
// incumbent" the same value for ever after.
func TestAReleaseDeclaringNoRuntimeRecordsNothing(t *testing.T) {
	got, err := runtimeForRelease(releaseDeclaring(nil), "")

	require.NoError(t, err)
	assert.Empty(t, got)
}

// The step order that makes the recorded runtime possible at all.
//
// `stepWriteInstallation` reads the release out of engine state, and only
// `stepStageRelease` puts it there. When the write ran first the release was
// never present, so every installation recorded an empty runtime -- which
// `Installation.RuntimeName` reads as the legacy one, resolving a release
// declared for another runtime against the wrong adapter, silently.
//
// Asserted on the step list rather than through a full init, because the defect
// was in the ordering and an end-to-end test that happened to pass would not say
// which order it passed under.
func TestStagingPrecedesTheInstallationWriteWhenThereIsARelease(t *testing.T) {
	steps := initSteps(&Deps{}, InitOptions{Product: "demo", ReleasePath: "bundle.tar.zst"}, releaseTrust{})

	stage, write := -1, -1
	for i, s := range steps {
		switch s.ID {
		case "stage-release":
			stage = i
		case "write-installation":
			write = i
		}
	}

	require.NotEqual(t, -1, stage, "the release is staged")
	require.NotEqual(t, -1, write, "the installation is written")
	assert.Less(t, stage, write,
		"the installation write reads the release from engine state, so staging must "+
			"have put it there first; the other order records an empty runtime for "+
			"every installation")
}

// And with no release there is nothing to stage, so the write still happens.
func TestTheInstallationIsStillWrittenWithoutARelease(t *testing.T) {
	steps := initSteps(&Deps{}, InitOptions{Product: "demo"}, releaseTrust{})

	var ids []string
	for _, s := range steps {
		ids = append(ids, s.ID)
	}
	assert.Contains(t, ids, "write-installation")
	assert.NotContains(t, ids, "stage-release")
}

// Decision 5, at the point every operation passes through.
//
// Without this, the recorded runtime is a label: `init` stores "quadlet", and
// every later operation hands `.container` files to the Compose adapter that
// happens to be wired. The deployment fails somewhere far away from the
// sentence that would have explained it.
func TestAnInstallationIsRefusedByAManagerBuiltForAnotherRuntime(t *testing.T) {
	d := &Deps{Runtime: &fakes.Runtime{RuntimeName: "compose"}}
	inst := domain.Installation{Runtime: "quadlet"}
	rel := domain.Release{
		Manifest: domain.Manifest{Runtimes: domain.Runtimes{
			"quadlet": {Files: []string{"app.container"}},
		}},
		Root: "/opt/demo/releases/1",
	}

	_, err := d.runtimeConfig(rel, inst, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "runs on the quadlet runtime")
	assert.Contains(t, err.Error(), "configured for compose")
}

// And the matching case still builds a configuration, so the refusal above is
// about disagreement rather than about the check being unconditional.
func TestAMatchingRuntimeStillBuildsAConfiguration(t *testing.T) {
	d := &Deps{Runtime: &fakes.Runtime{RuntimeName: "compose"}}
	inst := domain.Installation{Runtime: "compose", Product: "demo"}
	rel := domain.Release{
		Manifest: domain.Manifest{Runtimes: domain.Runtimes{
			"compose": {Files: []string{"compose.yaml"}},
		}},
		Root: "/opt/demo/releases/1",
	}

	cfg, err := d.runtimeConfig(rel, inst, nil)

	require.NoError(t, err)
	assert.Len(t, cfg.Files, 1)
}

// A Deps with no runtime wired cannot contradict an installation, and must not
// pretend to.
//
// The branch is defensive today -- every production wiring has an adapter --
// and it is asserted anyway, because the alternative reading of "no runtime" is
// "no runtime matches", which would make `installation import` refuse every
// export on a machine that has yet to be given one. That is a rebuild stopped
// by a nil check, and nothing else in the process would say so.
func TestNoRuntimeWiredIsNotAMismatch(t *testing.T) {
	d := &Deps{}
	have, ok := d.drivesRuntime(domain.Installation{Runtime: "quadlet"})

	assert.True(t, ok, "a manager with no adapter cannot disagree about the runtime")
	assert.Empty(t, have, "and it has no name to offer instead")
}

// The comparison is between the recorded value and the adapter's, with the
// pre-schema-9 reading applied to the record. An installation created before
// the field existed ran the legacy runtime, and a manager driving it is not a
// mismatch -- which is every installation on every machine upgrading to this
// version.
func TestAnInstallationFromBeforeTheFieldIsNotAMismatch(t *testing.T) {
	d := &Deps{Runtime: fakes.NewRuntime()}
	_, ok := d.drivesRuntime(domain.Installation{})

	assert.True(t, ok, "an empty runtime means the legacy one, which is what this manager drives")
}
