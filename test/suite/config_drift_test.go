package suite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/test/fakes"
)

// Where the configuration files on disk differ from what the installed release
// renders, shown on the machine before an `apply` replaces them (RFC 0044).

func applicationTarget(h *harness) string {
	return filepath.Join(h.Root, "etc", "demo", "application.yaml")
}

func handEdit(t *testing.T, h *harness, line string) {
	t.Helper()
	f, err := os.OpenFile(applicationTarget(h), os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString(line + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

func appliedHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	return h
}

// A hand edit is shown with its diff and counted, `doctor` warns naming the
// file, and once `apply` has replaced it neither reports anything.
func TestAHandEditIsShownUntilApplyReplacesIt(t *testing.T) {
	h := appliedHarness(t)
	ctx := context.Background()

	clean, err := ops.ConfigDiff(ctx, h.Deps)
	require.NoError(t, err)
	require.Empty(t, clean.Drift, "this deployment has drifted, so the test proves nothing")
	assert.Contains(t, clean.Summary(), "no drift")

	handEdit(t, h, "log_level: debug")

	report, err := ops.ConfigDiff(ctx, h.Deps)
	require.NoError(t, err)
	require.Len(t, report.Drift, 1)
	assert.Equal(t, applicationTarget(h), report.Drift[0].Target)
	assert.Contains(t, report.Drift[0].Diff, "- log_level: debug")
	assert.True(t, report.Redacted)
	assert.Equal(t, 1, report.Targets)
	assert.Contains(t, report.Text(), "- log_level: debug")
	assert.Contains(t, report.Summary(), "1 of 1 configuration file differs from what 1.2.0 renders")

	doctor, err := ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	check := findCheck(t, doctor, "config.drift")
	assert.Equal(t, "warn", check.Status)
	assert.Contains(t, check.Message, applicationTarget(h))
	assert.Contains(t, check.Remedy, "morzer config diff")

	applyBaseline(t, h)
	report, err = ops.ConfigDiff(ctx, h.Deps)
	require.NoError(t, err)
	assert.Empty(t, report.Drift)
	doctor, err = ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	assert.Equal(t, "ok", findCheck(t, doctor, "config.drift").Status)
}

// An absent file is drift. A file that cannot be read is listed apart, never
// counted, and does not make `doctor` warn.
func TestAnAbsentFileIsDriftAndAnUnreadableOneIsNot(t *testing.T) {
	h := appliedHarness(t)
	ctx := context.Background()

	require.NoError(t, os.Remove(applicationTarget(h)))
	report, err := ops.ConfigDiff(ctx, h.Deps)
	require.NoError(t, err)
	require.Len(t, report.Drift, 1, "an absent file was not counted")

	// A directory where the file belongs: EISDIR, which root cannot read
	// either, where a 0000 file would read under `sudo`.
	require.NoError(t, os.Mkdir(applicationTarget(h), 0o755))
	report, err = ops.ConfigDiff(ctx, h.Deps)
	require.NoError(t, err)
	assert.Empty(t, report.Drift, "an unreadable file was counted as drift")
	require.Len(t, report.Unreadable, 1)
	assert.Equal(t, applicationTarget(h), report.Unreadable[0].Target)
	assert.Contains(t, report.Summary(), "1 could not be read")

	doctor, err := ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	check := findCheck(t, doctor, "config.drift")
	assert.Equal(t, "ok", check.Status)
	assert.Contains(t, check.Message, applicationTarget(h))
}

// A value pasted into a file is scrubbed before the diff is shown. With the
// secret state undecryptable, only the file names are shown, and the report
// says so.
func TestAPastedSecretIsScrubbedFromTheDiff(t *testing.T) {
	h := appliedHarness(t)
	ctx := context.Background()
	handEdit(t, h, "password: a-real-database-password")

	report, err := ops.ConfigDiff(ctx, h.Deps)
	require.NoError(t, err)
	require.Len(t, report.Drift, 1)
	assert.NotContains(t, report.Drift[0].Diff, "a-real-database-password")
	assert.Contains(t, report.Drift[0].Diff, "- password: [redacted]")

	h.Secrets.Fail = map[string]error{"Load": domain.SecretsError(errInjected, "cannot decrypt")}
	report, err = ops.ConfigDiff(ctx, h.Deps)
	require.NoError(t, err)
	assert.False(t, report.Redacted)
	require.Len(t, report.Drift, 1)
	assert.Equal(t, applicationTarget(h), report.Drift[0].Target)
	assert.Empty(t, report.Drift[0].Diff, "an unscrubbed diff was carried")
	assert.NotContains(t, report.Text(), "a-real-database-password")
	assert.Contains(t, report.Text(), applicationTarget(h))
	assert.Contains(t, report.Summary(), "not shown")
}

// With no release installed `config diff` fails as `config list` does, and the
// check says it did not run.
func TestDriftNeedsAnInstalledRelease(t *testing.T) {
	h := newHarness(t)
	h.install()
	ctx := context.Background()
	require.NoError(t, h.Deps.State.SetCurrentRelease(ctx, domain.ReleaseRecord{}))

	_, listErr := ops.ConfigList(ctx, h.Deps)
	require.Error(t, listErr)
	_, err := ops.ConfigDiff(ctx, h.Deps)
	require.Error(t, err)
	assert.Equal(t, domain.AsError(listErr).Message, domain.AsError(err).Message)

	doctor, err := ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	check := findCheck(t, doctor, "config.drift")
	assert.Equal(t, "ok", check.Status)
	assert.Contains(t, check.Message, "not checked")
}

// A plan shows the diff `config diff` shows, scrubbed the same way.
func TestAPlanShowsWhatConfigDiffShows(t *testing.T) {
	h := appliedHarness(t)
	ctx := context.Background()
	handEdit(t, h, "password: a-real-database-password")

	report, err := ops.ConfigDiff(ctx, h.Deps)
	require.NoError(t, err)

	_, err = ops.Apply(ctx, h.Deps, ops.Options{DryRun: true})
	require.NoError(t, err)
	plans := h.Events.OfKind(events.KindPlan)
	require.NotEmpty(t, plans)
	var diff string
	for _, step := range plans[len(plans)-1].Plan {
		if step.ID == "render-configuration" {
			diff = step.Diff
		}
	}
	assert.Equal(t, report.Text(), diff)
	assert.NotContains(t, diff, "a-real-database-password")
}

// An unattended tick leaves the release staged while a file differs and says
// why. An interactive update goes ahead: its plan already shows the diff.
func TestDriftHoldsAnUnattendedUpdate(t *testing.T) {
	h, _, _, _ := followingHarnessWith(t, recoverableUpdate)
	h.setHookEnv()
	applyBaseline(t, h)
	armAutoApply(t, h)
	ctx := context.Background()
	handEdit(t, h, "log_level: debug")

	res, err := ops.RunUnattended(ctx, h.Deps, ops.UnattendedOptions{})
	require.NoError(t, err, "a held tick must exit 0")
	assert.False(t, res.Applied, "the tick replaced a hand-edited file")
	assert.Contains(t, res.Assessment.Why(), "1 configuration file differs")
	assert.Contains(t, res.Assessment.Why(), "morzer config diff")
	candidate, err := h.Deps.State.UpdateCandidate(ctx)
	require.NoError(t, err)
	require.True(t, candidate.IsStaged(), "the release was not left staged")

	_, err = ops.Update(ctx, h.Deps, ops.UpdateOptions{To: candidate.Version.String()})
	require.NoError(t, err, "drift held an interactive update")
	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, candidate.Version, current.Version)
	assert.False(t, strings.Contains(readFile(t, applicationTarget(h)), "log_level: debug"))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

// A tick that cannot compare the files holds as well: it cannot tell that no
// edit would be lost.
func TestATickThatCannotCompareHolds(t *testing.T) {
	h, _, _, _ := followingHarnessWith(t, recoverableUpdate)
	h.setHookEnv()
	applyBaseline(t, h)
	armAutoApply(t, h)
	h.Deps.Renderer = &fakes.Renderer{Err: domain.ValidationError(errInjected, "template refused")}

	res, err := ops.RunUnattended(context.Background(), h.Deps, ops.UnattendedOptions{})
	require.NoError(t, err)
	assert.False(t, res.Applied, "the tick installed over files it could not compare")
	assert.Contains(t, res.Assessment.Why(), "could not be compared")
}
