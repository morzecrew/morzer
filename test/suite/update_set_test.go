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

	_, err := ops.Update(context.Background(), h.Deps, ops.UpdateOptions{
		Ref: src, Parameters: map[string]string{"admin_email": adminEmail, "http_port": "not-a-port"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "http_port")
	assert.NotContains(t, domain.AsError(err).Message, "not-a-port", "a refused value reached the message")
	assert.Equal(t, "1.2.0", currentVersion(t, h))
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

// adminEmailSaveFailing fails, once, the save that records admin_email: the
// update's last write, after everything else has converged.
type adminEmailSaveFailing struct {
	ports.StateStore
	armed *bool
}

func (s adminEmailSaveFailing) SaveInstallation(ctx context.Context, inst domain.Installation) error {
	if _, ok := inst.Parameters["admin_email"]; ok && *s.armed {
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
	h.Deps.State = adminEmailSaveFailing{StateStore: store, armed: &armed}
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
