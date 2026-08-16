package ops

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/test/fakes"
)

// Which runtime an installation is fixed to, at the moment it is fixed.
// RFC 0023 decisions 3, 5 and 12.

func releaseDeclaring(runtimes domain.Runtimes) domain.Release {
	return domain.Release{
		Manifest: domain.Manifest{Runtimes: runtimes},
		Root:     "/opt/demo/releases/1",
	}
}

func TestASingleDeclaredRuntimeIsWhatTheInstallationIsFixedTo(t *testing.T) {
	got, err := runtimeForRelease(releaseDeclaring(domain.Runtimes{
		"quadlet": {Files: []string{"app.container"}},
	}))

	require.NoError(t, err)
	assert.Equal(t, "quadlet", got,
		"the recorded runtime is the one the release declares, not the one the manager happens to have")
}

// Decision 12. The manager cannot yet tell which of two declared runtimes it is
// able to drive, and the two ways to find out are both barred: a branch on a
// runtime's name above internal/adapters, which decision 7 forbids, or a name
// injected at the composition root that every test would set and no test would
// exercise as production leaves it. So it refuses.
//
// The refusal is asserted rather than the absence of a crash, because the
// failure this prevents is silent: picking one of the two and installing with
// it would produce a working machine running a substrate the operator never
// chose, and nothing downstream would ever contradict it.
func TestAReleaseDeclaringSeveralRuntimesIsRefusedRatherThanPickedFrom(t *testing.T) {
	got, err := runtimeForRelease(releaseDeclaring(domain.Runtimes{
		"compose": {Files: []string{"compose.yaml"}},
		"quadlet": {Files: []string{"app.container"}},
	}))

	require.Error(t, err)
	assert.Empty(t, got, "a refusal must not also return a runtime somebody might record")
	assert.Contains(t, err.Error(), "cannot yet choose between them")

	var domErr *domain.Error
	require.ErrorAs(t, err, &domErr)
	assert.Contains(t, domErr.Hint, "compose")
	assert.Contains(t, domErr.Hint, "quadlet")
	assert.Contains(t, domErr.Hint, "P3",
		"a refusal that expires should say what it is waiting for")
}

// A release declaring nothing at all records nothing, and
// Installation.RuntimeName reads that as the legacy runtime. Recording a
// default here instead would make "created before the field" and "chose the
// incumbent" the same value for ever after.
func TestAReleaseDeclaringNoRuntimeRecordsNothing(t *testing.T) {
	got, err := runtimeForRelease(releaseDeclaring(nil))

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
	steps := initSteps(&Deps{}, InitOptions{Product: "demo", ReleasePath: "bundle.tar.zst"})

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
	steps := initSteps(&Deps{}, InitOptions{Product: "demo"})

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

	_, err := d.runtimeConfig(rel, inst, "")

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

	cfg, err := d.runtimeConfig(rel, inst, "")

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
