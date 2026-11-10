package suite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/target"
	"github.com/morzecrew/morzer/internal/adapters/target/localdir"
	"github.com/morzecrew/morzer/internal/adapters/verify/minisign"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// The sync settings (RFC 0057 §5.5): local and root-only, and refused when
// they would follow documents with nothing to check them against.

const (
	authorityKey  = "RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3"
	authorityKey2 = "RWT6zgAAAAAAAXRoaXMga2V5IGlzIGEgZml4dHVyZSBub3QgYSByZWFs"
)

func setSettings(t *testing.T, h *harness, set map[string]string) error {
	t.Helper()
	_, err := ops.SetSettings(context.Background(), h.Deps, ops.SetSettingsOptions{Set: set})
	return err
}

func TestSyncIsRefusedWithNothingToFollow(t *testing.T) {
	h := newHarness(t)
	h.install()

	err := setSettings(t, h, map[string]string{"sync.enabled": "true"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sync.enabled needs sync.authority")

	err = setSettings(t, h, map[string]string{"sync.enabled": "true", "sync.authority": authorityKey})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sync.enabled needs sync.target")

	// One command: sync.enabled sorts before the target, so the rule is
	// asked of the whole result.
	require.NoError(t, setSettings(t, h, map[string]string{
		"sync.enabled": "true", "sync.authority": authorityKey, "sync.target": "file:///srv/state",
	}))
	assert.True(t, installation(t, h).Sync.Enabled)

	// Nor can the authority be taken away from under it.
	_, err = ops.SetSettings(context.Background(), h.Deps, ops.SetSettingsOptions{Unset: []string{"sync.authority"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sync.enabled needs sync.authority")
}

// A new authority starts a watermark of its own, and the change is in the
// journal: it hands the machine to whoever holds the new key (decisions 4,
// 27).
func TestANewAuthorityClearsTheWatermarkAndIsJournalled(t *testing.T) {
	h := newHarness(t)
	inst := h.install()
	inst.Sync = domain.SyncConfig{
		Authority: authorityKey,
		Applied:   &domain.SyncDocument{Serial: 99999999999999, AuthorityKeyID: "OLD"},
		Refused:   &domain.SyncDocument{Serial: 99999999999999, AuthorityKeyID: "OLD"},
	}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	require.NoError(t, setSettings(t, h, map[string]string{"sync.authority": authorityKey2}))
	after := installation(t, h).Sync
	assert.Equal(t, authorityKey2, after.Authority)
	assert.Nil(t, after.Applied, "the old authority's high serial would refuse every one of the new one's")
	assert.Nil(t, after.Refused)

	records := configRecords(t, h)
	require.Len(t, records, 1, "changing the authority was not journalled")
	assert.Equal(t, authorityKey2, records[0].Flags["sync.authority"])

	// The same key again changes nothing and keeps the watermark.
	inst = installation(t, h)
	inst.Sync.Applied = &domain.SyncDocument{Serial: 5, AuthorityKeyID: "NEW"}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))
	require.NoError(t, setSettings(t, h, map[string]string{"sync.authority": authorityKey2}))
	assert.NotNil(t, installation(t, h).Sync.Applied)
}

func TestSyncSettingsRefuseWhatTheyCannotUse(t *testing.T) {
	h := newHarness(t)
	h.install()
	registry, err := target.NewRegistry(localdir.New())
	require.NoError(t, err)
	h.Deps.Targets = registry
	for name, value := range map[string]string{
		"sync.interval":    "30s",
		"sync.target":      "gopher://example",
		"sync.credentials": "bad name",
	} {
		assert.Error(t, setSettings(t, h, map[string]string{name: value}), "%s=%s was accepted", name, value)
	}

	require.NoError(t, setSettings(t, h, map[string]string{
		"sync.interval": "20m", "sync.credentials": "sync_s3",
	}))
	got := installation(t, h).Sync
	assert.Equal(t, "20m0s", got.Interval)

	// Whole seconds, which is what the timer ticks in.
	require.NoError(t, setSettings(t, h, map[string]string{"sync.interval": "1m0.4s"}))
	assert.Equal(t, "1m0s", installation(t, h).Sync.Interval)
	assert.Equal(t, "sync_s3", got.Credentials)
}

// A document can never touch them: they are not among the settings morzer
// lets a release make manageable.
func TestNoSyncSettingIsManageable(t *testing.T) {
	for _, name := range []string{"sync.target", "sync.credentials", "sync.authority",
		"sync.interval", "sync.enabled"} {
		assert.False(t, domain.IsManageableSetting(name), name)
	}
}

// The sync timer is installed while sync is on and taken away when it is
// turned off, by the setting change itself.
func TestTheSyncTimerFollowsTheSwitch(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.Deps.Supervisor = h.Supervisor
	h.Supervisor.Present = true
	registry, err := target.NewRegistry(localdir.New())
	require.NoError(t, err)
	h.Deps.Targets = registry
	ctx := context.Background()
	units, err := h.Supervisor.Units(ports.UnitParams{Product: "demo"})
	require.NoError(t, err)
	require.NoError(t, h.Supervisor.InstallUnits(ctx, units, ports.EnableAll))

	timer := "demo-sync.timer"
	require.NotContains(t, h.Supervisor.Installed, timer)
	require.NoError(t, setSettings(t, h, map[string]string{
		"sync.target":    "file://" + t.TempDir(),
		"sync.authority": authorityKey,
		"sync.enabled":   "true",
	}))
	assert.Contains(t, h.Supervisor.Installed, timer, "sync was turned on and nothing schedules it")

	require.NoError(t, setSettings(t, h, map[string]string{"sync.enabled": "false"}))
	assert.NotContains(t, h.Supervisor.Installed, timer, "sync was turned off and its timer stayed")
}

// `init` records the target and the authority to follow, checked as `config
// set` checks them, and leaves sync off: the target's credential may not be a
// secret yet.
func TestInitRecordsWhatToFollow(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	realIdentity(t, h)
	h.Deps.Verifier = minisign.New()
	_, err := ops.Init(ctx, h.Deps, ops.InitOptions{
		Product: "demo", NoRecoveryKey: true, SyncAuthority: "not-a-key",
	})
	require.Error(t, err)
	exists, err := h.Deps.State.InstallationExists(ctx)
	require.NoError(t, err)
	assert.False(t, exists, "a malformed authority was refused after the installation existed")

	_, err = ops.Init(ctx, h.Deps, ops.InitOptions{
		Product: "demo", NoRecoveryKey: true, SyncTarget: "file:///srv/state", SyncAuthority: " " + authorityKey + "\n",
	})
	require.NoError(t, err)
	got := installation(t, h).Sync
	assert.Equal(t, "file:///srv/state", got.Target)
	assert.Equal(t, authorityKey, got.Authority)
	assert.False(t, got.Enabled)
}
