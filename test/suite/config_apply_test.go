package suite

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// `config apply` is one operation for a change to settings, parameters and
// secrets (RFC 0056): one record, one render, one converge, one health check,
// and every value put back when a step fails.

func runningHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	return h
}

func configApply(t *testing.T, h *harness, doc string, opts ops.Options) (ops.Result, error) {
	t.Helper()
	change, err := ops.ParseConfigChange([]byte(doc))
	require.NoError(t, err)
	return ops.ConfigApply(context.Background(), h.Deps, ops.ConfigApplyOptions{Options: opts, Change: change})
}

func stepIDs(rec domain.OperationRecord) []string {
	var ids []string
	for _, step := range rec.Steps {
		ids = append(ids, step.ID)
	}
	return ids
}

func storedSecret(t *testing.T, h *harness, name string) (string, bool) {
	t.Helper()
	set, err := h.Secrets.Load(context.Background())
	require.NoError(t, err)
	v, ok := set.Get(name)
	return v.Reveal(), ok
}

func renderedSecret(t *testing.T, h *harness, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(h.Paths.SecretsRenderDir(), name))
	require.NoError(t, err)
	return string(data)
}

func lastChangedOf(t *testing.T, h *harness, name string) time.Time {
	t.Helper()
	meta, err := h.Secrets.Metadata(context.Background())
	require.NoError(t, err)
	for _, m := range meta {
		if m.Name == name {
			return m.LastChanged.Time
		}
	}
	return time.Time{}
}

const mixedChange = `
settings:
  update.check: "true"
parameters:
  log_level: debug
secrets:
  smtp_password: the-new-smtp-password-value
`

func TestConfigApplyIsOneOperation(t *testing.T) {
	h := runningHarness(t)
	ups := len(h.Runtime.UpOptions)

	result, err := configApply(t, h, mixedChange, ops.Options{})
	require.NoError(t, err)

	records := configRecords(t, h)
	require.Len(t, records, 1, "a change to three names was more than one operation")
	assert.Equal(t, []string{
		"record-installation", "stage-secrets", "load-secrets", "render-secrets",
		"render-configuration", "recreate-services", "health-checks",
	}, stepIDs(records[0]))
	assert.Equal(t, "update.check", records[0].Flags["settings"])
	assert.Equal(t, "log_level", records[0].Flags["parameters"])
	assert.Equal(t, "smtp_password", records[0].Flags["secrets"])

	require.Len(t, h.Runtime.UpOptions, ups+1, "the services were converged more than once")
	assert.Equal(t, []string{"app"}, h.Runtime.UpOptions[ups].Services)
	assert.True(t, h.Runtime.UpOptions[ups].ForceRecreate)

	inst := installation(t, h)
	assert.Equal(t, "debug", inst.Parameters["log_level"])
	assert.True(t, inst.Update.Check)
	got, _ := storedSecret(t, h, "smtp_password")
	assert.Equal(t, "the-new-smtp-password-value", got)
	assert.Equal(t, "the-new-smtp-password-value", renderedSecret(t, h, "smtp_password"))

	assert.Equal(t, "applied update.check, log_level, secret smtp_password; re-created app", result.Summary)
}

// Each step made to fail in turn leaves what was there before the change:
// the installation, the store, the rendered secret file and the services'
// last definition.
func TestConfigApplyUndoesEveryValueWhicheverStepFails(t *testing.T) {
	for _, failAt := range []struct{ step, method string }{
		{"stage-secrets", "Set"},
		{"render-secrets", "Render"},
		{"recreate-services", "Up"},
		{"health-checks", ""},
	} {
		t.Run(failAt.step, func(t *testing.T) {
			h := runningHarness(t)
			before := installation(t, h)
			h.Secrets.Seed(map[string]string{"smtp_password": "the-old-smtp-password-value"})
			was := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			h.Secrets.SetChanged("smtp_password", was)
			_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
			require.NoError(t, err)
			ups := len(h.Runtime.UpOptions)

			// What the secret file holds when the undo brings the old
			// services back: the engine undoes the failed step first, so a
			// re-create that did not put the secrets back itself would
			// start them on the new file, whatever the stage's undo
			// restores afterwards.
			var ups2, atUndo []string
			h.Runtime.OnCall = func(method string) {
				if method != "Up" {
					return
				}
				if ups2 = append(ups2, method); len(ups2) > 1 {
					data, _ := os.ReadFile(filepath.Join(h.Paths.SecretsRenderDir(), "smtp_password"))
					atUndo = append(atUndo, string(data))
				}
				// The error is taken before the hook runs, so only the
				// first Up fails and the undo's Up goes through.
				delete(h.Runtime.Fail, "Up")
			}
			switch failAt.method {
			case "Up":
				h.Runtime.Fail["Up"] = domain.RuntimeError(nil, "injected: the new value does not start")
			case "":
				h.Health.FailingWaits = 1
			default:
				h.Secrets.Fail = map[string]error{failAt.method: domain.SecretsError(nil, "injected: %s", failAt.method)}
			}

			_, err = configApply(t, h, mixedChange, ops.Options{})
			require.Error(t, err, "the change did not fail at %s", failAt.step)

			after := installation(t, h)
			assert.Equal(t, before.Parameters, after.Parameters)
			assert.Equal(t, before.Update.Check, after.Update.Check)
			got, _ := storedSecret(t, h, "smtp_password")
			assert.Equal(t, "the-old-smtp-password-value", got)
			assert.True(t, was.Equal(lastChangedOf(t, h, "smtp_password")),
				"the undo stamped the secret as changed now, which reads as a rotation")
			assert.Equal(t, "the-old-smtp-password-value", renderedSecret(t, h, "smtp_password"),
				"the rendered file kept the new value while the store went back")
			if n := len(h.Runtime.UpConfigs); n > ups {
				assert.NotEqual(t, "debug", h.Runtime.UpConfigs[n-1].Env["DEMO_PARAM_LOG_LEVEL"],
					"the services were left on the new value")
			}
			for _, held := range atUndo {
				assert.Equal(t, "the-old-smtp-password-value", held,
					"the undo brought the old services back on the new secret file")
			}
			if failAt.step == "recreate-services" || failAt.step == "health-checks" {
				assert.NotEmpty(t, atUndo, "the undo re-created nothing, so this proves nothing")
			}
		})
	}
}

// The configuration render fails after the secrets were rendered: the
// secret stage's own undo renders them again, since `apply`'s render-secrets
// undo keeps the files while the product runs (RFC 0056 §5.4).
func TestAFailedConfigurationRenderLeavesTheSecretFilesOld(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
	h := runningHarness(t)
	h.Secrets.Seed(map[string]string{"smtp_password": "the-old-smtp-password-value"})
	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)

	etc := filepath.Join(h.Root, "etc", "demo")
	require.NoError(t, os.Chmod(etc, 0o500))
	t.Cleanup(func() { _ = os.Chmod(etc, 0o750) })

	// A domain change, because the template renders the domains: a change
	// that leaves the rendered file as it was skips the render.
	_, err = configApply(t, h,
		"settings:\n  installation.domains: new.example\nsecrets:\n  smtp_password: the-new-smtp-password-value\n",
		ops.Options{})
	require.Error(t, err)
	assert.Contains(t, stepIDs(configRecords(t, h)[0]), "render-secrets",
		"the secrets were not rendered before the configuration failed, so this proves nothing")
	got, _ := storedSecret(t, h, "smtp_password")
	assert.Equal(t, "the-old-smtp-password-value", got)
	assert.Equal(t, "the-old-smtp-password-value", renderedSecret(t, h, "smtp_password"))
}

// A secret set by `secret set` while the change runs is a different name the
// rollback must not touch: it puts back only what this change touched (§5.5).
func TestARollbackKeepsASecretSetMeanwhile(t *testing.T) {
	h := runningHarness(t)
	ups := len(h.Runtime.UpOptions)
	h.Runtime.Fail["Up"] = domain.RuntimeError(nil, "injected: the new value does not start")
	h.Runtime.OnCall = func(method string) {
		if method != "Up" || len(h.Runtime.UpOptions) > ups {
			return
		}
		if _, failing := h.Runtime.Fail["Up"]; failing {
			delete(h.Runtime.Fail, "Up")
			require.NoError(t, h.Secrets.Set(context.Background(), "session_key", domain.NewSecret("set-meanwhile-by-hand")))
		}
	}

	_, err := configApply(t, h, mixedChange, ops.Options{})
	require.Error(t, err)
	got, _ := storedSecret(t, h, "session_key")
	assert.Equal(t, "set-meanwhile-by-hand", got, "the rollback undid a change it did not make")
	_, present := storedSecret(t, h, "smtp_password")
	assert.False(t, present, "the change's own secret was not removed again")
}

// The dotdash switch: a profile, a parameter and a secret as one operation,
// and the same change failing its health checks with the old profile left
// running on the old secret.
func TestAProfileChangeWithASecretAndAParameterIsOneOperation(t *testing.T) {
	const change = `
settings:
  installation.profile: external-db
parameters:
  log_level: debug
secrets:
  db_password: the-new-database-password
`
	t.Run("applied", func(t *testing.T) {
		h := runningHarness(t)
		result, err := configApply(t, h, change, ops.Options{})
		require.NoError(t, err)
		records := configRecords(t, h)
		require.Len(t, records, 1)
		ids := stepIDs(records[0])
		require.Contains(t, ids, "restore-point")
		require.Contains(t, ids, "stage-secrets")
		assert.Less(t, slices.Index(ids, "restore-point"), slices.Index(ids, "stage-secrets"),
			"the secrets were staged before the restore point, so its undo would bring the old "+
				"services up on the new secrets")
		assert.Equal(t, []string{"external-db"}, installation(t, h).Profiles)
		assert.Equal(t, "the-new-database-password", renderedSecret(t, h, "db_password"))
		assert.Contains(t, result.Summary, "re-created every service under the profile external-db")
	})

	t.Run("failing its health checks", func(t *testing.T) {
		h := runningHarness(t)
		h.Health.FailingWaits = 1
		// The secret file each time services come up: the last is the
		// restore point bringing the old profile back.
		var atUp []string
		h.Runtime.OnCall = func(method string) {
			if method == "Up" {
				atUp = append(atUp, renderedSecret(t, h, "db_password"))
			}
		}
		_, err := configApply(t, h, change, ops.Options{})
		require.Error(t, err)
		require.Len(t, atUp, 2, "expected the new profile's up and the restore point's")
		assert.Equal(t, "a-real-database-password", atUp[1],
			"the restore point brought the old profile up on the new secret file")

		inst := installation(t, h)
		assert.Equal(t, []string{"embedded"}, inst.Profiles)
		assert.NotEqual(t, "debug", inst.Parameters["log_level"])
		got, _ := storedSecret(t, h, "db_password")
		assert.Equal(t, "a-real-database-password", got)
		assert.Equal(t, "a-real-database-password", renderedSecret(t, h, "db_password"),
			"the old profile's services came back reading the new secret")
		last := h.Runtime.UpConfigs[len(h.Runtime.UpConfigs)-1]
		assert.True(t, slicesContainSuffix(last.Files, "compose.embedded.yaml"),
			"the old profile was not brought back: %v", last.Files)
	})
}

// §5.3, one case each.
func TestConfigApplyTouchesWhatTheChangeReaches(t *testing.T) {
	t.Run("secrets alone restart their dependents", func(t *testing.T) {
		h := runningHarness(t)
		ups := len(h.Runtime.UpOptions)
		result, err := configApply(t, h, "secrets:\n  session_key: a-brand-new-session-key\n", ops.Options{})
		require.NoError(t, err)
		assert.Contains(t, h.Runtime.Calls, "Restart:app")
		assert.Len(t, h.Runtime.UpOptions, ups, "a change of secrets alone re-created the services")
		assert.Equal(t, []string{"stage-secrets", "load-secrets", "render-secrets", "restart-services", "health-checks"},
			stepIDs(configRecords(t, h)[0]))
		assert.Equal(t, "applied secret session_key; restarted app", result.Summary)
	})

	t.Run("a domain change re-creates every running service", func(t *testing.T) {
		h := runningHarness(t)
		ups := len(h.Runtime.UpOptions)
		_, err := configApply(t, h, "settings:\n  installation.domains: new.example\nparameters:\n  log_level: debug\n", ops.Options{})
		require.NoError(t, err)
		require.Len(t, h.Runtime.UpOptions, ups+1)
		assert.Equal(t, []string{"app", "db"}, h.Runtime.UpOptions[ups].Services)
	})

	t.Run("a settings change that reaches no service converges nothing", func(t *testing.T) {
		h := runningHarness(t)
		ups := len(h.Runtime.UpOptions)
		result, err := configApply(t, h, "settings:\n  update.check: \"true\"\n", ops.Options{})
		require.NoError(t, err)
		assert.Len(t, h.Runtime.UpOptions, ups)
		assert.Equal(t, []string{"record-settings"}, stepIDs(configRecords(t, h)[0]))
		assert.Equal(t, "applied update.check", result.Summary)
	})

	t.Run("while stopped, nothing is converged", func(t *testing.T) {
		h := runningHarness(t)
		inst := installation(t, h)
		inst.Stopped = &domain.Stop{At: domain.NewTime(time.Now()), OperationID: "op_stop"}
		require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))
		ups := len(h.Runtime.UpOptions)
		result, err := configApply(t, h, mixedChange, ops.Options{})
		require.NoError(t, err)
		assert.Len(t, h.Runtime.UpOptions, ups)
		assert.NotContains(t, h.Runtime.Calls, "Restart:app")
		assert.Equal(t, "the-new-smtp-password-value", renderedSecret(t, h, "smtp_password"),
			"a stopped container reads the files when it comes back")
		assert.Contains(t, result.Summary, "takes effect on `morzer start`")
	})

	t.Run("before anything is created, the secrets are stored and not rendered", func(t *testing.T) {
		h := newHarness(t)
		h.install()
		h.setHookEnv()
		_, err := configApply(t, h, "secrets:\n  smtp_password: the-new-smtp-password-value\n", ops.Options{})
		require.NoError(t, err)
		got, _ := storedSecret(t, h, "smtp_password")
		assert.Equal(t, "the-new-smtp-password-value", got)
		assert.NoFileExists(t, filepath.Join(h.Paths.SecretsRenderDir(), "smtp_password"))
	})

	t.Run("a dependent behind an inactive profile is left out", func(t *testing.T) {
		h := harnessWithLogLevelServices(t, "app, vpn")
		ups := len(h.Runtime.UpOptions)
		_, err := configApply(t, h, "parameters:\n  log_level: debug\n", ops.Options{})
		require.NoError(t, err)
		require.Len(t, h.Runtime.UpOptions, ups+1)
		assert.Equal(t, []string{"app"}, h.Runtime.UpOptions[ups].Services)
	})
}

// RFC 0056 §5.2: every refusal, and all of a document's problems at once.
func TestConfigApplyRefusesWhatItCannotApply(t *testing.T) {
	for doc, want := range map[string]string{
		"settings:\n  no.such: x\n":                                            `no.such`,
		"settings:\n  update.check: maybe\n":                                   "is not a boolean",
		"parameters:\n  no_such: x\n":                                          "no_such",
		"parameters:\n  log_level: loud\n":                                     "log_level",
		"secrets:\n  no_such: x\n":                                             `declares no secret named "no_such"`,
		"secrets:\n  smtp_password: ''\n":                                      `the value for secret "smtp_password" is empty`,
		"unset:\n  secrets: [db_password]\n":                                   `secret "db_password" is required`,
		"unset:\n  secrets: [never_existed]\n":                                 `declares no secret named "never_existed", and none is stored`,
		"parameters:\n  log_level: debug\nunset:\n  parameters: [log_level]\n": "parameters.log_level is both set and unset",
	} {
		h := runningHarness(t)
		before := installation(t, h)
		_, err := configApply(t, h, doc, ops.Options{})
		require.Error(t, err, "%q was applied", doc)
		assert.Contains(t, err.Error(), want, "%q", doc)
		assert.Empty(t, configRecords(t, h), "%q ran an operation", doc)
		assert.Equal(t, before.Parameters, installation(t, h).Parameters, "%q", doc)
	}

	h := runningHarness(t)
	_, err := configApply(t, h, "settings:\n  no.such: x\nparameters:\n  log_level: loud\nsecrets:\n  no_such: x\n", ops.Options{})
	require.Error(t, err)
	msg := domain.AsError(err).Message
	assert.Contains(t, msg, "the change has 3 problems")
	for _, name := range []string{"no.such", "log_level", "no_such"} {
		assert.Contains(t, msg, name)
	}
}

func TestConfigApplyRemovesARequiredSecretOnlyWithForce(t *testing.T) {
	h := newHarness(t)
	h.install()
	_, err := configApply(t, h, "unset:\n  secrets: [session_key]\n", ops.Options{Force: true})
	require.NoError(t, err)
	_, present := storedSecret(t, h, "session_key")
	assert.False(t, present)
}

func TestConfigApplyWithNothingToChangeRunsNoOperation(t *testing.T) {
	h := runningHarness(t)
	result, err := configApply(t, h, "settings:\n  update.check: \"false\"\nsecrets:\n  session_key: a-real-session-key-value\n", ops.Options{})
	require.NoError(t, err)
	assert.Equal(t, "no change: every value already matches", result.Summary)
	assert.Empty(t, configRecords(t, h))
}

// No secret value in the plan, the summary, the journal, the attestation,
// a notification or any file but the rendered secret itself.
func TestConfigApplyLeaksNoSecretValue(t *testing.T) {
	const needle = "SECRET-MUST-NOT-TRAVEL-4c1f"
	h := verifyHarness(t)
	attested(t, h)
	doc := "parameters:\n  log_level: debug\nsecrets:\n  smtp_password: " + needle + "\n"

	plan, err := configApply(t, h, doc, ops.Options{DryRun: true})
	require.NoError(t, err)
	assert.NotContains(t, plan.Summary, needle)
	result, err := configApply(t, h, doc, ops.Options{})
	require.NoError(t, err)
	assert.NotContains(t, result.Summary, needle)

	for _, e := range h.Events.Events() {
		assert.NotContains(t, fmt.Sprintf("%+v", e), needle, "an event carried the value")
	}
	require.NoError(t, filepath.WalkDir(h.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasPrefix(path, h.Paths.SecretsRenderDir()) {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		assert.NotContains(t, string(body), needle, "%s holds the secret value", path)
		return nil
	}))
}

// P2 (decision 14): `config set` of a parameter is the same operation, so it
// waits for the release's health checks, and a failing one undoes it.
func TestConfigSetUndoesAChangeThatFailsItsHealthChecks(t *testing.T) {
	h := runningHarness(t)
	h.Health.FailingWaits = 1
	_, err := ops.ConfigSet(context.Background(), h.Deps, ops.ConfigSetOptions{
		Set: map[string]string{"log_level": "debug"},
	})
	require.Error(t, err)
	assert.NotEqual(t, "debug", installation(t, h).Parameters["log_level"])
	assert.Contains(t, stepIDs(configRecords(t, h)[0]), "health-checks")
}

func slicesContainSuffix(list []string, suffix string) bool {
	for _, v := range list {
		if strings.HasSuffix(v, suffix) {
			return true
		}
	}
	return false
}

// A render cut off after writing some files has not set what the undo reads
// to know it rendered, so the undo renders again whenever the render ran.
func TestARenderCutOffPartWayIsPutBack(t *testing.T) {
	h := runningHarness(t)
	h.Secrets.Seed(map[string]string{"smtp_password": "the-old-smtp-password-value"})
	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)
	h.Secrets.RenderFailsAfterWriting = domain.SecretsError(nil, "injected: the disk filled part way")

	_, err = configApply(t, h, mixedChange, ops.Options{})
	require.Error(t, err)
	assert.Equal(t, "the-old-smtp-password-value", renderedSecret(t, h, "smtp_password"),
		"a file the cut-off render wrote kept the new value")
}

// Removing a required secret while containers exist fails at load-secrets,
// which refuses a release missing one. Under `apply` that step aborts, since
// nothing before it needs undoing; here the record and the stage do.
func TestARefusedSecretLoadUndoesTheChange(t *testing.T) {
	h := runningHarness(t)
	_, err := configApply(t, h, "settings:\n  update.check: \"true\"\nunset:\n  secrets: [session_key]\n",
		ops.Options{Force: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session_key")
	got, present := storedSecret(t, h, "session_key")
	assert.True(t, present, "the removed secret was not put back")
	assert.Equal(t, "a-real-session-key-value", got)
	assert.False(t, installation(t, h).Update.Check, "the recorded setting was not undone")
}

// With nothing running, `config set` says the change waits for the next
// apply, not that the release declares no dependent services.
func TestConfigSetWithNothingRunningSaysItWaitsForApply(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	result, err := ops.ConfigSet(context.Background(), h.Deps, ops.ConfigSetOptions{
		Set: map[string]string{"log_level": "debug"},
	})
	require.NoError(t, err)
	assert.Equal(t, "set log_level; nothing is running, so it takes effect on the next `morzer apply`", result.Summary)
}

// A failed `config set` reports no data: the values it would have recorded
// were put back, and a JSON reader would take them for what is in force.
func TestAFailedConfigSetReportsNoValues(t *testing.T) {
	h := runningHarness(t)
	h.Health.FailingWaits = 1
	result, err := ops.ConfigSet(context.Background(), h.Deps, ops.ConfigSetOptions{
		Set: map[string]string{"log_level": "debug"},
	})
	require.Error(t, err)
	assert.Nil(t, result.Data, "a failed change reported the values it put back")
}
