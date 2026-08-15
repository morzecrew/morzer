package suite

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/supervisor/systemd"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// Who decides whether a generated unit is enabled (RFC 0030 row 1).
//
// Driven through the real systemd adapter with a scripted runner, because the
// property is *which `systemctl` commands are issued* and nothing above the
// adapter can see those. A fake supervisor would answer whatever it was written
// to answer, which is how the behaviour these tests pin went unnoticed while
// the suite was green: §3.1 measured it by reading argv, and so does this.

// installedMachine installs the unit set the way `init` does, and returns the
// scripted runner together with a reader for everything issued *after* the
// setup.
//
// The setup deliberately uses EnableAll: it stands in for `init`, and a setup
// that installed with the reconciliation's own scope would be a test whose
// premise is the thing under test.
func installedMachine(t *testing.T, d *ops.Deps) (*exec.Scripted, func() []string) {
	t.Helper()
	ctx := context.Background()

	runner := exec.NewScripted()
	real := systemd.New(runner, systemd.WithUnitDir(t.TempDir()))
	d.Supervisor = availableSupervisor{Supervisor: real}

	inst, err := d.State.LoadInstallation(ctx)
	require.NoError(t, err)
	units, err := d.Supervisor.Units(ports.UnitParams{Product: inst.Product})
	require.NoError(t, err)
	require.NoError(t, d.Supervisor.InstallUnits(ctx, units, ports.EnableAll))

	installed := len(runner.Calls())
	require.NotZero(t, installed, "the setup issued no systemctl at all")

	// Everything issued from here on, which is what each test is about.
	return runner, func() []string {
		var out []string
		for _, c := range runner.Calls()[installed:] {
			out = append(out, strings.Join(c.Argv, " "))
		}
		return out
	}
}

// An unrelated setting change no longer re-enables the units.
//
// This is §3.1's measurement, inverted. `morzer config set update.check=true`
// issued `systemctl enable demo.service` and `enable demo-backup.timer` on
// every run, so an operator's `systemctl disable --now demo-backup.timer` held
// until the next setting change and was then undone with no message.
//
// The assertion is on the absence of the command rather than on a unit's state,
// because the absence is the whole property: the manager cannot leave a
// decision standing by *asking* systemd about it, only by not overruling it.
func TestASettingChangeDoesNotReEnableTheUnits(t *testing.T) {
	h := newHarness(t)
	h.install()
	_, since := installedMachine(t, h.Deps)

	_, err := ops.SetSettings(context.Background(), h.Deps, ops.SetSettingsOptions{
		Set: map[string]string{"update.check": "true"},
	})
	require.NoError(t, err)

	issued := since()
	for _, line := range issued {
		assert.NotContains(t, line, "enable demo.service",
			"a setting change re-asserted enablement: %v", issued)
		assert.NotContains(t, line, "enable demo-backup.timer",
			"a setting change re-asserted enablement: %v", issued)
	}

	// It did still reconcile, or this test would pass on a build where the
	// reconciliation never ran at all.
	assert.Contains(t, strings.Join(issued, "\n"), "daemon-reload",
		"the reconciliation did not run, so the absence above proves nothing")
}

// A unit the setting change *creates* is still enabled.
//
// Configuring a channel is what brings the update pair into existence, and a
// timer that is installed, wanted, and never switched on is a worse outcome
// than the one row 1 set out to fix. `disable` is durable; "never enabled in
// the first place" is not a decision anybody made.
func TestASettingChangeEnablesAUnitItCreates(t *testing.T) {
	h := newHarness(t)
	h.install()
	_, since := installedMachine(t, h.Deps)

	_, err := ops.SetSettings(context.Background(), h.Deps, ops.SetSettingsOptions{
		Set: map[string]string{
			"update.channel": "oci://registry.example/demo/bundle:stable",
			"update.check":   "true",
		},
	})
	require.NoError(t, err)

	issued := strings.Join(since(), "\n")
	assert.Contains(t, issued, "enable demo-update.timer",
		"the timer this change created was installed and never switched on")
	assert.NotContains(t, issued, "enable demo-backup.timer",
		"the units that already existed were re-enabled")
	// The oneshot beside it, which must never be enabled: doing so runs it
	// at every boot.
	assert.NotContains(t, issued, "enable demo-update.service",
		"a oneshot service was enabled, which runs it at every boot")
}

// `init --repair --install-units` is the one command that re-asserts it.
//
// Row 1 does not make enablement unreachable; it moves it to a command somebody
// runs *because* they found the machine wrong. That is also the remedy `doctor`
// prints beside `<unit>: not enabled`, so the warning is clearable rather than
// permanent -- which is what stops it becoming a warning nobody reads.
func TestARepairReAssertsEnablement(t *testing.T) {
	requireSOPS(t)
	ctx := context.Background()

	_, recoveryPub := generateRecoveryKey(t)
	h := initOriginMachine(t, ctx, recoveryPub)
	_, since := installedMachine(t, h.Deps)

	_, err := ops.Init(ctx, h.Deps, ops.InitOptions{
		Product: "demo", Repair: true, RecoveryRecipient: recoveryPub,
		InstallUnits: true,
	})
	require.NoError(t, err)

	issued := strings.Join(since(), "\n")
	assert.Contains(t, issued, "enable demo-backup.timer",
		"a repair left the timer switched off, so nothing can undo a mistaken disable")
	assert.Contains(t, issued, "enable demo.service",
		"a repair left the product's own service switched off")
}
