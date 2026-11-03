package suite

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// fakeManagerFeed serves one newest release, or an error, and counts the asks.
// Download copies a release's files from dir.
type fakeManagerFeed struct {
	newest string
	err    error
	asked  int
	dir    string
}

func (f *fakeManagerFeed) Newest(context.Context) (ports.ManagerRelease, error) {
	f.asked++
	if f.err != nil {
		return ports.ManagerRelease{}, f.err
	}
	return ports.ManagerRelease{
		Version: domain.MustParseVersion(f.newest),
		URL:     "https://github.com/morzecrew/morzer/releases/tag/v" + f.newest,
	}, nil
}

func (f *fakeManagerFeed) Download(_ context.Context, v domain.Version, dir string) error {
	if f.err != nil {
		return f.err
	}
	for _, name := range []string{ports.ManagerArchive(v), ports.SumsFileName, ports.SignatureFileName} {
		data, err := os.ReadFile(filepath.Join(f.dir, name))
		if err != nil {
			return domain.ValidationError(err, "the release has no %s", name)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// `manager.version` asks nobody until the installation consents with
// `manager.check`, then warns about a newer manager with the command that
// installs it over the binary the units run, and the scheduled run notifies it
// (RFC 0046 §5.1).
func TestTheManagerVersionCheckAsksOnlyWithConsent(t *testing.T) {
	h := appliedHarness(t)
	ctx := context.Background()
	feed := &fakeManagerFeed{newest: "0.12.0"}
	h.Deps.ManagerFeed = feed
	h.Deps.ManagerVersion = domain.MustParseVersion("0.11.0")
	h.Deps.ManagerPath = "/opt/morzer/bin/morzer"

	report, err := ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	got := findCheck(t, report, "manager.version")
	assert.Equal(t, "ok", got.Status)
	assert.Equal(t, "not checked: manager.check is off", got.Message)
	assert.Zero(t, feed.asked, "the feed was asked without consent")

	_, err = ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{Set: map[string]string{"manager.check": "true"}})
	require.NoError(t, err)

	report, err = ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	got = findCheck(t, report, "manager.version")
	assert.Equal(t, "warn", got.Status)
	assert.Equal(t, "0.11.0 is running and no longer supported; 0.12.0 is the newest release", got.Message)
	assert.Equal(t, "upgrade with install.sh --version 0.12.0 --require-signature --dir /opt/morzer/bin", got.Remedy)

	rec := &recordingNotifier{}
	h.Deps.Notifier = rec
	_, err = ops.DoctorUnattended(ctx, h.Deps)
	require.NoError(t, err)
	var named bool
	for _, ev := range rec.events {
		for _, c := range ev.Checks {
			named = named || (ev.Kind == events.KindDoctorFinished && c.ID == "manager.version")
		}
	}
	assert.True(t, named, "the scheduled doctor did not notify the newer manager")

	// A feed that cannot answer is never reported as up to date.
	feed.err = domain.ValidationError(nil, "cannot reach the release feed")
	report, err = ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	got = findCheck(t, report, "manager.version")
	assert.Equal(t, "warn", got.Status)
	assert.Equal(t, "cannot check for a newer manager: cannot reach the release feed", got.Message)

	_, err = ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{Unset: []string{"manager.check"}})
	require.NoError(t, err)
	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	assert.False(t, inst.Update.ManagerCheck, "unsetting left the consent recorded")
}

// What the check says, by where the running manager stands against the
// newest release (decisions 3 and 12).
func TestCheckManagerComparesWithTheNewestRelease(t *testing.T) {
	cases := map[string]struct {
		running, newest string
		want            ops.ManagerCheck
	}{
		"an older line": {"0.11.0", "0.12.0", ops.ManagerCheck{Outdated: true, Supported: false,
			Upgrade: "install.sh --version 0.12.0 --require-signature --dir /usr/local/bin"}},
		"an older patch on the newest line": {"0.12.0", "0.12.1", ops.ManagerCheck{Outdated: true, Supported: true,
			Upgrade: "install.sh --version 0.12.1 --require-signature --dir /usr/local/bin"}},
		"a prerelease of the newest": {"0.12.0-rc.1", "0.12.0", ops.ManagerCheck{Outdated: true, Supported: false,
			Upgrade: "install.sh --version 0.12.0 --require-signature --dir /usr/local/bin"}},
		"the newest":               {"0.12.0", "0.12.0", ops.ManagerCheck{Supported: true}},
		"ahead on the newest line": {"0.12.2", "0.12.1", ops.ManagerCheck{Supported: true}},
		"ahead of the newest line": {"0.13.0-rc.1", "0.12.0", ops.ManagerCheck{Supported: false}},
		"a dev build":              {"0.0.0", "0.12.0", ops.ManagerCheck{DevBuild: true}},
		"a snapshot build":         {"0.0.0-snapshot", "0.12.0", ops.ManagerCheck{DevBuild: true}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			d := &ops.Deps{
				ManagerFeed:    &fakeManagerFeed{newest: tc.newest},
				ManagerVersion: domain.MustParseVersion(tc.running),
				ManagerPath:    "/usr/local/bin/morzer",
			}
			got, err := ops.CheckManager(context.Background(), d)
			require.NoError(t, err)
			tc.want.Running, tc.want.Newest = tc.running, tc.newest
			tc.want.URL = "https://github.com/morzecrew/morzer/releases/tag/v" + tc.newest
			assert.Equal(t, tc.want, got)
		})
	}
}

// The summary never calls a build the feed does not list the newest release,
// and the upgrade command survives a directory a shell would split.
func TestTheManagerCheckSaysWhereTheBuildStands(t *testing.T) {
	check := func(running, newest, path string) ops.ManagerCheck {
		t.Helper()
		got, err := ops.CheckManager(context.Background(), &ops.Deps{
			ManagerFeed:    &fakeManagerFeed{newest: newest},
			ManagerVersion: domain.MustParseVersion(running),
			ManagerPath:    path,
		})
		require.NoError(t, err)
		return got
	}

	assert.Equal(t, "0.12.0 is the newest release", check("0.12.0", "0.12.0", "").Summary())
	assert.Equal(t, "0.13.0-rc.1 is ahead of the newest release, 0.12.0",
		check("0.13.0-rc.1", "0.12.0", "").Summary())
	assert.Equal(t, "install.sh --version 0.12.0 --require-signature --dir '/opt/my tools'",
		check("0.11.0", "0.12.0", "/opt/my tools/morzer").Upgrade)
	assert.Equal(t, "install.sh --version 0.12.0 --require-signature",
		check("0.11.0", "0.12.0", "").Upgrade, "no path, no --dir")
}
