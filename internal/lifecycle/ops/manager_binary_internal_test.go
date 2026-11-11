package ops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/fakes"
)

// Units run the manager as root, so they never name a binary another user
// could replace. Under --root they are files nothing runs, and are not checked.
func TestUnitsNeverRunABinaryAnotherUserCanReplace(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root owns every file it creates")
	}
	own := filepath.Join(t.TempDir(), "morzer")
	require.NoError(t, os.WriteFile(own, []byte("#!/bin/sh\n"), 0o755)) //nolint:gosec // a stand-in binary

	production := &Deps{Paths: domain.DefaultPaths("demo"), ManagerPath: own, ManagerVersion: domain.MustParseVersion("0.16.0")}
	err := production.checkManagerBinary()
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrPreflight))
	assert.Contains(t, err.Error(), own)
	assert.Contains(t, domain.AsError(err).Hint, "sudo sh -s -- --version 0.16.0")

	// Both ways units are written: `init`'s step, and the reconciliation
	// every other operation runs. Neither writes one.
	supervisor := fakes.NewSupervisor()
	supervisor.Present = true
	supervisor.Installed["demo.service"] = []byte("[Unit]\n")
	production.Supervisor = supervisor
	require.ErrorIs(t, stepInstallUnits(production, "demo", ports.EnableAll).Execute(context.Background(), nil), domain.ErrPreflight)
	require.ErrorIs(t, production.refreshUnits(context.Background(), domain.Installation{Product: "demo"}), domain.ErrPreflight)
	assert.Equal(t, []byte("[Unit]\n"), supervisor.Installed["demo.service"], "a unit was rewritten")
	production.Supervisor = nil

	if _, statErr := os.Stat("/usr/bin/env"); statErr == nil {
		production.ManagerPath = "/usr/bin/env"
		assert.NoError(t, production.checkManagerBinary(), "a binary only root can replace was refused")
	}

	scratch := &Deps{Paths: domain.PathsUnder(t.TempDir(), "demo"), ManagerPath: own}
	assert.NoError(t, scratch.checkManagerBinary(), "units under --root were checked")
}

// `doctor` fails a machine whose units already run such a binary, and says how
// to move them to one only root can replace.
func TestDoctorFailsUnitsThatRunABinaryAnotherUserCanReplace(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root owns every file it creates")
	}
	own := filepath.Join(t.TempDir(), "morzer")
	require.NoError(t, os.WriteFile(own, []byte("#!/bin/sh\n"), 0o755)) //nolint:gosec // a stand-in binary
	supervisor := fakes.NewSupervisor()
	supervisor.Present = true
	d := &Deps{Paths: domain.DefaultPaths("demo"), Supervisor: supervisor, ManagerVersion: domain.MustParseVersion("0.16.0")}
	inst := domain.Installation{Product: "demo"}
	run := func() events.CheckResult { return d.checkUnitsManagerBinary(inst).Run(context.Background()) }

	supervisor.ManagerPaths = map[string][]string{"demo": {own}}
	result := run()
	assert.Equal(t, events.CheckFail, result.Status)
	assert.Contains(t, result.Message, own)
	assert.Contains(t, result.Remedy, "init --repair")
	assert.True(t, d.checkUnitsManagerBinary(inst).Fatal, "a warning would let it stand")

	if _, err := os.Stat("/usr/bin/env"); err == nil {
		supervisor.ManagerPaths = map[string][]string{"demo": {"/usr/bin/env"}}
		assert.Equal(t, events.CheckOK, run().Status)
	}

	supervisor.Fail = map[string]error{"UnitsManagerPaths": errors.New("unreadable")}
	assert.Equal(t, events.CheckFail, run().Status, "units nobody could read were passed")
	supervisor.Fail = nil

	d.Paths = domain.PathsUnder(t.TempDir(), "demo")
	supervisor.ManagerPaths = map[string][]string{"demo": {own}}
	assert.Equal(t, events.CheckOK, run().Status, "units under --root were checked")
}
