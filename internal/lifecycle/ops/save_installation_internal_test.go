package ops

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// The order the two records are written in, which decides which way they can
// disagree when one write fails.
//
// `saveInstallation` writes both: installation.yaml, the operator-facing
// report, and the JSON state the manager actually reads. Its own comment says
// why there is one writer -- "two of them is how the file an operator looks at
// stops matching the deployment they are looking at" -- and the order is what
// makes that true or not.
//
// `doctor` reads the report back and reports the two disagreeing, so neither
// order is silent. What the order decides is which artifact is the wrong one:
// a report written first can run *ahead* of state and describe an installation
// that was never recorded, and `hookbackup` copies that file into backups.

// refusingState accepts every read and refuses to record an installation.
type refusingState struct {
	ports.StateStore
	err error
}

func (s *refusingState) SaveInstallation(context.Context, domain.Installation) error {
	return s.err
}

// acceptingState records what it was given and nothing else.
type acceptingState struct {
	ports.StateStore
	got domain.Installation
}

func (s *acceptingState) SaveInstallation(_ context.Context, i domain.Installation) error {
	s.got = i
	return nil
}

func TestNoReportIsWrittenForAStateWriteThatFailed(t *testing.T) {
	d := &Deps{
		State: &refusingState{err: errors.New("state store refused the write")},
		Paths: domain.PathsUnder(t.TempDir(), "demo"),
	}

	err := d.saveInstallation(context.Background(),
		domain.Installation{ID: "inst-1", Product: "demo"})

	require.Error(t, err, "a refused state write must be reported as a failure")

	_, statErr := os.Stat(d.Paths.InstallationFile())
	assert.True(t, os.IsNotExist(statErr),
		"a report describes an installation the manager never recorded: %s exists "+
			"after the state write that was supposed to create it failed",
		d.Paths.InstallationFile())
}

// The stronger half: a machine that already has a good report keeps it.
//
// Overwriting it with the failed change is worse than creating one, because
// what is destroyed is an accurate record of the installation as it actually
// stands -- and a backup taken afterwards carries the fiction rather than the
// fact.
func TestAFailedStateWriteLeavesAnExistingReportAlone(t *testing.T) {
	d := &Deps{
		State: &refusingState{err: errors.New("state store refused the write")},
		Paths: domain.PathsUnder(t.TempDir(), "demo"),
	}
	require.NoError(t, os.MkdirAll(d.Paths.EtcDir, 0o750))
	const previous = "# the installation as it actually stands\nid: inst-1\n"
	require.NoError(t, os.WriteFile(d.Paths.InstallationFile(), []byte(previous), 0o640))

	err := d.saveInstallation(context.Background(),
		domain.Installation{ID: "inst-1", Product: "demo", Profile: "changed"})

	require.Error(t, err)

	after, readErr := os.ReadFile(d.Paths.InstallationFile())
	require.NoError(t, readErr)
	assert.Equal(t, previous, string(after),
		"the report was replaced with a change the state store refused")
}

// The happy path still writes both, so the ordering fix cannot be mistaken for
// dropping the report altogether.
func TestASuccessfulSaveWritesBothRecords(t *testing.T) {
	store := &acceptingState{}
	d := &Deps{State: store, Paths: domain.PathsUnder(t.TempDir(), "demo")}

	inst := domain.Installation{ID: "inst-1", Product: "demo"}
	require.NoError(t, d.saveInstallation(context.Background(), inst))

	assert.Equal(t, inst, store.got, "the state store records what it was given")

	raw, err := os.ReadFile(d.Paths.InstallationFile())
	require.NoError(t, err, "the operator's report is still written")
	assert.Contains(t, string(raw), "inst-1")
	assert.True(t, strings.HasPrefix(string(raw), "# Managed by morzer."),
		"the report keeps its header saying what the file is")
}
