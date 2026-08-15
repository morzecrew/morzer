package contract

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/ports"
)

// SupervisorHarness is one ports.Supervisor implementation under test.
//
// The port has a real adapter and an in-memory fake, and RFC 0030 row 1 made
// the difference between them load-bearing: the whole design is that an
// existing unit's enablement survives a reconciliation, and every operation
// test that asserts anything about it asserts it against the fake. A fake that
// re-enabled on every install -- which is exactly what this one did -- would
// agree with a production path that does not, in the direction that hides the
// defect.
//
// So the rules live here and both implementations answer them.
type SupervisorHarness struct {
	Supervisor ports.Supervisor

	// Enabled reports the units this implementation currently treats as
	// enabled.
	//
	// Supplied by the harness because the two observe it differently and
	// neither can be asked the other's way: the fake holds a state map, and
	// the systemd adapter with a scripted runner holds no state at all --
	// what it has is the argv it issued, which is what §3.1 measured and
	// the only thing a host without a live daemon can be asked.
	Enabled func() []string
}

// RunSupervisorSuite drives the enablement rules of RFC 0030 §8.1.
//
// Deliberately not the whole port. Unit *contents* and unit *existence* are
// reconciled identically by both implementations and are covered where they
// belong; what is here is the part where an implementation can silently
// disagree with the design and make every test above it read as passing.
func RunSupervisorSuite(t *testing.T, name string, newHarness func(t *testing.T) SupervisorHarness) {
	t.Helper()

	units := []ports.Unit{
		{Name: "demo.service", Contents: []byte("[Unit]\n"), Enable: true},
		{Name: "demo-backup.service", Contents: []byte("[Unit]\n"), Enable: false},
		{Name: "demo-backup.timer", Contents: []byte("[Timer]\n"), Enable: true},
	}

	t.Run(name+"/a first install enables what asks and nothing else", func(t *testing.T) {
		h := newHarness(t)
		require.NoError(t, h.Supervisor.InstallUnits(context.Background(), units, ports.EnableNew))

		got := h.Enabled()
		assert.ElementsMatch(t, []string{"demo.service", "demo-backup.timer"}, got)
		// The oneshot, said as its own assertion because the reason is
		// specific: enabling it runs a backup at every boot.
		assert.NotContains(t, got, "demo-backup.service")
	})

	t.Run(name+"/a reconciliation leaves a disabled unit disabled", func(t *testing.T) {
		h := newHarness(t)
		ctx := context.Background()
		require.NoError(t, h.Supervisor.InstallUnits(ctx, units, ports.EnableNew))

		// The operator, with their own init system.
		require.NoError(t, h.Supervisor.Disable(ctx, "demo-backup.timer"))
		require.NotContains(t, h.Enabled(), "demo-backup.timer",
			"the harness cannot observe a disable, so the rest proves nothing")

		require.NoError(t, h.Supervisor.InstallUnits(ctx, units, ports.EnableNew))
		assert.NotContains(t, h.Enabled(), "demo-backup.timer",
			"a reconciliation re-enabled a unit the operator switched off")
		assert.Contains(t, h.Enabled(), "demo.service",
			"a reconciliation switched off a unit nobody touched")
	})

	t.Run(name+"/a repair re-enables it", func(t *testing.T) {
		h := newHarness(t)
		ctx := context.Background()
		require.NoError(t, h.Supervisor.InstallUnits(ctx, units, ports.EnableNew))
		require.NoError(t, h.Supervisor.Disable(ctx, "demo-backup.timer"))

		require.NoError(t, h.Supervisor.InstallUnits(ctx, units, ports.EnableAll))
		assert.Contains(t, h.Enabled(), "demo-backup.timer",
			"a repair left the timer off, so nothing can undo a mistaken disable")
		assert.NotContains(t, h.Enabled(), "demo-backup.service",
			"a repair enabled the oneshot, which runs it at every boot")
	})

	t.Run(name+"/a unit added later is enabled in either scope", func(t *testing.T) {
		for _, scope := range []ports.EnableScope{ports.EnableNew, ports.EnableAll} {
			h := newHarness(t)
			ctx := context.Background()
			require.NoError(t, h.Supervisor.InstallUnits(ctx, units, scope))

			grown := append(slices.Clone(units),
				ports.Unit{Name: "demo-update.timer", Contents: []byte("[Timer]\n"), Enable: true})
			require.NoError(t, h.Supervisor.InstallUnits(ctx, grown, scope))

			assert.Contains(t, h.Enabled(), "demo-update.timer",
				"a newly created timer was installed and never switched on")
		}
	})

	t.Run(name+"/starting and stopping do not decide enablement", func(t *testing.T) {
		h := newHarness(t)
		ctx := context.Background()
		require.NoError(t, h.Supervisor.InstallUnits(ctx, units, ports.EnableNew))
		require.NoError(t, h.Supervisor.Disable(ctx, "demo-backup.timer"))

		// `systemctl stop` and `systemctl start` are about whether a unit
		// is running now; `enable` and `disable` are about whether it
		// comes back at boot. Conflating them makes a disabled unit
		// enabled again the moment anything stops it -- and the reason
		// this matters here is that RemoveUnits stops before it disables,
		// so the conflation was invisible in the one place it ran.
		require.NoError(t, h.Supervisor.Stop(ctx, "demo-backup.timer"))
		assert.NotContains(t, h.Enabled(), "demo-backup.timer",
			"stopping a unit enabled it")

		require.NoError(t, h.Supervisor.Start(ctx, "demo-backup.timer"))
		assert.NotContains(t, h.Enabled(), "demo-backup.timer",
			"starting a unit enabled it, which is what `--now` is for")
	})

	t.Run(name+"/an install never disables", func(t *testing.T) {
		h := newHarness(t)
		ctx := context.Background()
		require.NoError(t, h.Supervisor.InstallUnits(ctx, units, ports.EnableNew))

		// Somebody enabled the oneshot by hand. It is wrong, and it is
		// still not an install's business to correct: removal is what
		// RemoveUnits is for, and switching a unit off is the operator's.
		require.NoError(t, h.Supervisor.Enable(ctx, "demo-backup.service"))

		for _, scope := range []ports.EnableScope{ports.EnableNew, ports.EnableAll} {
			require.NoError(t, h.Supervisor.InstallUnits(ctx, units, scope))
			assert.Contains(t, h.Enabled(), "demo-backup.service",
				"an install disabled a unit rather than reporting it")
		}
	})
}
