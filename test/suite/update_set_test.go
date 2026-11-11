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
	"github.com/morzecrew/morzer/internal/ports"
)

// Values an update gives the release it installs (#346): a release that adds
// a parameter with no default can only be installed with one.

const adminEmail = "ops@example.test"

// upgradeRequiringAdminEmail is the 1.3.0 fixture with one more parameter,
// `admin_email`, that has no default.
func upgradeRequiringAdminEmail(t *testing.T, h *harness) string {
	t.Helper()
	src := stageUpgradeSource(t, h)
	requireAdminEmail(t, src)
	return src
}

func requireAdminEmail(t *testing.T, bundle string) {
	t.Helper()
	manifest := filepath.Join(bundle, "manifest.yaml")
	raw, err := os.ReadFile(manifest)
	require.NoError(t, err)
	patched := strings.Replace(string(raw), "parameters:\n",
		"parameters:\n  admin_email:\n    type: string\n    description: Who the product mails\n    services: [app]\n", 1)
	require.NotEqual(t, string(raw), patched)
	require.NoError(t, os.WriteFile(manifest, []byte(patched), 0o644))
}

// convergedHarness is a 1.2.0 deployment, converged, about to be updated.
func convergedHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	return h
}

// Without the value the update is refused before anything it would undo: no
// backup, no stop, no record, and the running release goes on serving.
func TestAnUpdateWithoutAValueTheReleaseNeedsIsRefusedBeforeAnythingStops(t *testing.T) {
	h := convergedHarness(t)
	src := upgradeRequiringAdminEmail(t, h)
	backups, updates, calls := countBackups(t, h), countUpdates(t, h), len(h.Runtime.Calls)

	_, err := ops.Update(context.Background(), h.Deps, ops.UpdateOptions{Ref: src})
	require.Error(t, err)
	e := domain.AsError(err)
	assert.Contains(t, e.Message, "declares no default for admin_email")
	assert.Contains(t, e.Hint, "--set admin_email=")

	assert.Equal(t, "1.2.0", currentVersion(t, h))
	assert.Equal(t, backups, countBackups(t, h), "a backup was taken for an update that was refused")
	assert.Equal(t, updates, countUpdates(t, h), "a refused update was journalled")
	assert.Len(t, h.Runtime.Calls, calls, "the runtime was touched by an update that was refused")
}

// With it the release is installed, and the value is recorded by name in the
// journal, never quoted.
func TestUpdateSetGivesTheReleaseItsValue(t *testing.T) {
	h := convergedHarness(t)
	// Migrating no further than 1.2.0 reads, so the rollback below is one.
	src := stageRecoverableUpgradeSource(t, h)
	requireAdminEmail(t, src)

	result, err := ops.Update(context.Background(), h.Deps, ops.UpdateOptions{
		Ref: src, Parameters: map[string]string{"admin_email": adminEmail},
	})
	require.NoError(t, err)
	assert.Equal(t, "1.3.0", currentVersion(t, h))
	assert.Equal(t, adminEmail, installation(t, h).Parameters["admin_email"])
	assert.Equal(t, "admin_email", result.Record.Flags["set"])
	cfg, _ := lastUp(t, h)
	assert.Equal(t, adminEmail, cfg.Env["DEMO_PARAM_ADMIN_EMAIL"], "the converge did not deliver the value")

	journal, err := os.ReadFile(h.Paths.JournalFile())
	require.NoError(t, err)
	assert.NotContains(t, string(journal), adminEmail, "a value reached the journal")

	// Rolling back drops it with the release that declared it.
	_, err = ops.Rollback(context.Background(), h.Deps, ops.RollbackOptions{})
	require.NoError(t, err)
	assert.NotContains(t, installation(t, h).Parameters, "admin_email",
		"a rollback kept a value only the release it left declared")
}

// A name the release being installed does not declare is a typo, as it is for
// `config set`.
func TestUpdateSetRefusesANameTheReleaseDoesNotDeclare(t *testing.T) {
	h := convergedHarness(t)
	src := upgradeRequiringAdminEmail(t, h)

	_, err := ops.Update(context.Background(), h.Deps, ops.UpdateOptions{
		Ref: src, Parameters: map[string]string{"admin_email": adminEmail, "admin_emial": adminEmail},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `declares no parameter "admin_emial"`)
	assert.Equal(t, "1.2.0", currentVersion(t, h))
	assert.NotContains(t, installation(t, h).Parameters, "admin_email")
}

// A value that does not parse is refused by name before anything runs.
func TestUpdateSetRefusesAValueOfTheWrongType(t *testing.T) {
	h := convergedHarness(t)
	src := upgradeRequiringAdminEmail(t, h)

	backups, calls := countBackups(t, h), len(h.Runtime.Calls)

	_, err := ops.Update(context.Background(), h.Deps, ops.UpdateOptions{
		Ref: src, Parameters: map[string]string{"admin_email": adminEmail, "http_port": "not-a-port"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "http_port")
	assert.NotContains(t, domain.AsError(err).Message, "not-a-port", "a refused value reached the message")
	assert.Equal(t, "1.2.0", currentVersion(t, h))
	assert.Equal(t, backups, countBackups(t, h), "a value refused only once the update was under way")
	assert.Len(t, h.Runtime.Calls, calls, "a value refused only once the update was under way")
}

// An update that fails is undone, and the value it was given goes with it.
func TestAFailedUpdateRecordsNoValue(t *testing.T) {
	h := convergedHarness(t)
	src := stageRecoverableUpgradeSource(t, h)
	requireAdminEmail(t, src)
	h.Health.FailingWaits = 1

	_, err := ops.Update(context.Background(), h.Deps, ops.UpdateOptions{
		Ref: src, Parameters: map[string]string{"admin_email": adminEmail},
	})
	require.Error(t, err)
	assert.Equal(t, "1.2.0", currentVersion(t, h))
	assert.NotContains(t, installation(t, h).Parameters, "admin_email", "a rolled-back update recorded its value")
}

// recordFailing fails, once, the save that records name=value: the update's
// last write, after everything else has converged.
type recordFailing struct {
	ports.StateStore
	name, value string
	armed       *bool
}

func (s recordFailing) SaveInstallation(ctx context.Context, inst domain.Installation) error {
	if inst.Parameters[s.name] == s.value && *s.armed {
		*s.armed = false
		return assert.AnError
	}
	return s.StateStore.SaveInstallation(ctx, inst)
}

// Stopped before it recorded its values, an update is resumed only with them
// given again: the journal never carried them.
func TestAResumedUpdateIsGivenItsValuesAgain(t *testing.T) {
	h := convergedHarness(t)
	// A failure past a migration 1.2.0 cannot read is an intervention, not
	// a run to resume, so this one migrates no further.
	src := stageRecoverableUpgradeSource(t, h)
	requireAdminEmail(t, src)
	armed := true
	store := h.Deps.State
	h.Deps.State = recordFailing{StateStore: store, name: "admin_email", value: adminEmail, armed: &armed}
	ctx := context.Background()

	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src, Parameters: map[string]string{"admin_email": adminEmail}})
	require.Error(t, err)
	h.Deps.State = store
	require.NotContains(t, installation(t, h).Parameters, "admin_email")

	_, err = ops.Update(ctx, h.Deps, ops.UpdateOptions{Options: ops.Options{Resume: true}, Ref: src})
	require.Error(t, err, "a resume without the values was let through")
	assert.Contains(t, err.Error(), "admin_email")

	_, err = ops.Update(ctx, h.Deps, ops.UpdateOptions{
		Options: ops.Options{Resume: true}, Ref: src, Parameters: map[string]string{"admin_email": adminEmail},
	})
	require.NoError(t, err)
	assert.Equal(t, adminEmail, installation(t, h).Parameters["admin_email"])
	assert.Equal(t, "1.3.0", currentVersion(t, h))
}

// The same for a value that changes a parameter the release has already: a
// resume without it would converge on the recorded value and drop the one the
// operator gave.
func TestAResumedUpdateIsGivenAChangedValueAgain(t *testing.T) {
	h := convergedHarness(t)
	src := stageRecoverableUpgradeSource(t, h)
	armed := true
	store := h.Deps.State
	h.Deps.State = recordFailing{StateStore: store, name: "log_level", value: "debug", armed: &armed}
	ctx := context.Background()

	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src, Parameters: map[string]string{"log_level": "debug"}})
	require.Error(t, err)
	h.Deps.State = store

	_, err = ops.Update(ctx, h.Deps, ops.UpdateOptions{Options: ops.Options{Resume: true}, Ref: src})
	require.Error(t, err, "a resume without the value it was given was let through")
	assert.Contains(t, err.Error(), "log_level")
	assert.NotEqual(t, "debug", installation(t, h).Parameters["log_level"])
}

// A plan names the parameters an update will record, never their values.
func TestAnUpdatePlanNamesTheValuesItWillRecord(t *testing.T) {
	h := convergedHarness(t)
	src := upgradeRequiringAdminEmail(t, h)

	result, err := ops.Update(context.Background(), h.Deps, ops.UpdateOptions{
		Options: ops.Options{DryRun: true}, Ref: src, Parameters: map[string]string{"admin_email": adminEmail},
	})
	require.NoError(t, err)
	assert.Equal(t, domain.StatusSucceeded, result.Record.Status)
	plans := h.Events.OfKind(events.KindPlan)
	require.NotEmpty(t, plans)
	var found bool
	for _, step := range plans[len(plans)-1].Plan {
		if step.ID == "record-parameters" {
			found = true
			assert.Contains(t, step.Description, "admin_email")
			assert.NotContains(t, step.Description, adminEmail)
		}
	}
	assert.True(t, found, "the plan has no record-parameters step")
	assert.NotContains(t, installation(t, h).Parameters, "admin_email", "a dry run recorded a value")
}

// An unattended tick cannot give a value, so it leaves the release staged and
// says which one it waits for.
func TestAnUnattendedTickLeavesAReleaseThatNeedsAValueStaged(t *testing.T) {
	h, _, _, _ := followingHarnessWith(t, func(t *testing.T, dir string) {
		t.Helper()
		for file, edit := range map[string][2]string{
			"manifest.yaml": {"database_schema_produces: 14", "database_schema_produces: 12"},
			"hooks/migrate": {"target=14", "target=12"},
		} {
			path := filepath.Join(dir, file)
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, []byte(strings.Replace(string(raw), edit[0], edit[1], 1)), 0o755))
		}
		requireAdminEmail(t, dir)
	})
	h.setHookEnv()
	applyBaseline(t, h)
	armAutoApply(t, h)

	res, err := ops.RunUnattended(context.Background(), h.Deps, ops.UnattendedOptions{})
	require.NoError(t, err)
	assert.False(t, res.Applied, "a release that needs a value was installed unattended")
	assert.Contains(t, res.Assessment.Why(), "admin_email")
	assert.Contains(t, res.Assessment.Why(), "--set admin_email=")
	assert.Equal(t, "1.2.0", currentVersion(t, h))
}
