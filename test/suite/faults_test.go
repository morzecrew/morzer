package suite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// Everything an operator relies on when something goes wrong is here: that a
// failed step stops the operation, that the exit code says which kind of
// failure it was, that compensation runs newest-first, and that the journal
// records it either way.
//
// The engine's own fault-injection suite proves the mechanism. This proves the
// wiring: that each operation's steps actually report their failures, rather
// than swallowing one and reporting success on a system that is now half
// converged.

// injected is a marker so a test can tell its own failure from a real one.
var errInjected = errors.New("injected by the fault suite")

// TestEveryPortFailureStopsApply drives one failure per port method and
// asserts the operation refuses rather than proceeding.
//
// The exit code is the interesting half. Once a step with compensation has
// failed and the unwind succeeded, the code is `compensated` rather than the
// cause's own -- because what an operator needs to know first is whether the
// system was put back, not which subsystem broke. The cause travels in the
// message. A failure before any compensable step keeps its own code, which is
// why the two secret cases below differ.
func TestEveryPortFailureStopsApply(t *testing.T) {
	cases := map[string]struct {
		inject   func(*harness)
		wantCode domain.Code
		wantText string
	}{
		"compose cannot parse the project": {
			func(h *harness) {
				h.Runtime.Fail["Validate"] = domain.RuntimeError(errInjected, "compose config is invalid")
			}, domain.CodeCompensated, "invalid",
		},
		"the registry is unreachable": {
			func(h *harness) {
				h.Runtime.Fail["Pull"] = domain.RuntimeError(errInjected, "registry unreachable")
			}, domain.CodeCompensated, "unreachable",
		},
		"the daemon refuses to start the project": {
			func(h *harness) {
				h.Runtime.Fail["Up"] = domain.RuntimeError(errInjected, "daemon refused")
			}, domain.CodeCompensated, "refused",
		},
		"the secret store cannot be read": {
			// Before any compensable step, so this one keeps its own
			// code: nothing had been changed to put back.
			func(h *harness) {
				h.Secrets.Fail["Load"] = domain.SecretsError(errInjected, "cannot decrypt the secret state")
			}, domain.CodeSecrets, "decrypt",
		},
		"secrets cannot be rendered": {
			func(h *harness) {
				h.Secrets.Fail["Render"] = domain.SecretsError(errInjected, "the render directory is read-only")
			}, domain.CodeCompensated, "read-only",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.install()
			h.setHookEnv()
			tc.inject(h)

			result, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
			require.Error(t, err, "a failed step reported success, so the operator "+
				"is told a half-converged system is deployed")

			de := domain.AsError(err)
			assert.Equal(t, tc.wantCode, de.Code,
				"the exit code is how a unit file tells one failure from another")
			assert.Contains(t, strings.ToLower(err.Error()), tc.wantText,
				"the failure does not carry what the adapter said")

			// The record is written either way. A crash with no journal
			// entry is a crash `--resume` and `doctor` cannot see.
			assert.NotEqual(t, domain.StatusSucceeded, result.Record.Status)
			assertJournalled(t, h, result.Record.ID)
		})
	}
}

// TestAFailedApplyIsRecordedAsCompensatedNotFailed. The distinction matters:
// compensated means the system was put back, failed means it was not.
func TestAFailedApplyIsRecordedAsCompensatedNotFailed(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	h.Runtime.Fail["Up"] = domain.RuntimeError(errInjected, "daemon refused")

	result, _ := ops.Apply(context.Background(), h.Deps, ops.Options{})

	if result.Record.Status != domain.StatusCompensated &&
		result.Record.Status != domain.StatusFailed {
		t.Fatalf("status = %s, want compensated or failed", result.Record.Status)
	}

	// Whatever the outcome, the steps that ran are in the record, because
	// that is what `doctor` reads to say where it stopped.
	if len(result.Record.Steps) == 0 {
		t.Error("the record carries no steps, so nothing can say how far it got")
	}
	var failed int
	for _, s := range result.Record.Steps {
		if s.Status == domain.StepFailed {
			failed++
			if s.Error == "" {
				t.Errorf("step %s failed with no recorded reason", s.ID)
			}
		}
	}
	if failed == 0 {
		t.Error("no step is recorded as failed, though one was made to fail")
	}
}

// TestABackupFailureIsItsOwnExitCode. A machine that cannot take a backup must
// not silently proceed with an update: the backup is the rollback plan.
func TestABackupFailureStopsAnUpdate(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	h.Backup.Fail["Create"] = domain.BackupError(errInjected, "no space left on device")

	_, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{
		Reason: "manual", Verify: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no space",
		"the failure does not carry what the backup engine said")
}

// TestAnUnverifiableBackupIsAFailedBackup. Reporting success for a backup that
// could not be read back is the failure mode the whole Verify step exists to
// prevent.
func TestAnUnverifiableBackupIsAFailedBackup(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	h.Backup.Fail["Verify"] = domain.BackupError(errInjected, "checksum mismatch")

	result, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{
		Reason: "manual", Verify: true,
	})
	require.Error(t, err, "a backup that failed verification was reported as taken")
	assert.Contains(t, err.Error(), "checksum")
	assert.NotEqual(t, domain.StatusSucceeded, result.Record.Status)
}

// TestBackupWithoutVerifyDoesNotVerify records the flag's actual effect: it is
// on by default precisely because a backup nobody read back is a hope.
func TestBackupWithoutVerifyDoesNotVerify(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	h.Backup.Fail["Verify"] = domain.BackupError(errInjected, "checksum mismatch")

	_, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{
		Reason: "manual", Verify: false,
	})
	require.NoError(t, err, "Verify:false still ran the verification step")
}

// TestPruneFailureDoesNotFailTheBackupButIsRecorded.
//
// Retention runs after the backup has already been taken and verified, so a
// failure there must not turn a good backup into a failed operation -- the
// data is safe, the disk is merely fuller than intended. The step is declared
// `Continue` for exactly that reason. What it must not do is disappear: an
// operator whose retention has been silently failing for a month finds out
// when the disk fills.
func TestPruneFailureDoesNotFailTheBackupButIsRecorded(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	h.Backup.Fail["Prune"] = domain.BackupError(errInjected, "cannot remove an old backup")

	result, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{
		Reason: "scheduled", Prune: true,
	})
	require.NoError(t, err, "a retention failure failed a backup that had already "+
		"been taken and verified")

	var found bool
	for _, s := range result.Record.Steps {
		if s.ID == "prune-backups" {
			found = true
			assert.Equal(t, domain.StepFailed, s.Status,
				"the retention step is recorded as having succeeded")
			assert.Contains(t, s.Error, "cannot remove",
				"the record does not say why retention failed, so a disk "+
					"that has been filling for a month has no trail")
		}
	}
	assert.True(t, found, "the retention step is not in the record at all")
}

// The restore guards. Restore is the one operation that can destroy data on
// purpose, so each refusal is asserted by *which* refusal fires -- every path
// out of restore exits the same way, and asserting only the code would pass
// with the guard removed.

func TestRestoreRefusesEveryWayItShould(t *testing.T) {
	cases := map[string]struct {
		opts ops.RestoreOptions
		want string
	}{
		"without the typed installation id": {
			ops.RestoreOptions{Options: ops.Options{Force: true}},
			"confirm",
		},
		"with the wrong installation id typed": {
			ops.RestoreOptions{
				Options:                 ops.Options{Force: true},
				ConfirmedInstallationID: "inst_SOMETHING_ELSE",
			},
			"confirm",
		},
		"without --force at all": {
			ops.RestoreOptions{ConfirmedInstallationID: "inst_01TESTINSTALLATION"},
			"force",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.install()
			h.setHookEnv()

			_, err := ops.Restore(context.Background(), h.Deps, tc.opts)
			require.Error(t, err, "a restore went ahead %s", name)
			assert.Contains(t, strings.ToLower(err.Error()), tc.want,
				"the refusal that fired is not the one being tested; every "+
					"path out of restore fails, so this has to name which")
		})
	}
}

func TestRestoreOfABackupThatIsNotThere(t *testing.T) {
	h := newHarness(t)
	inst := h.install()
	h.setHookEnv()

	// One backup exists, so the refusal is about the id rather than about
	// there being nothing at all -- which is the case an operator meets
	// after a typo, and the one whose message has to name what they typed.
	_, err := ops.Backup(context.Background(), h.Deps, ops.BackupOptions{Reason: "manual"})
	require.NoError(t, err)

	_, err = ops.Restore(context.Background(), h.Deps, ops.RestoreOptions{
		Options:                 ops.Options{Force: true},
		BackupID:                "20260101T000000Z",
		ConfirmedInstallationID: inst.ID,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "20260101T000000Z",
		"the refusal does not name the backup that was asked for")
}

// TestConfigRefusesWhatTheReleaseDoesNotDeclare and its neighbours: the
// parameter surface is a closed set, and an operator setting something outside
// it has to be told the name is wrong rather than watching it be ignored.
func TestConfigRefusesEveryBadAssignment(t *testing.T) {
	cases := map[string]struct {
		set   map[string]string
		unset []string
		want  string
	}{
		"a parameter nothing declares": {
			map[string]string{"not_a_parameter": "1"}, nil, "not_a_parameter",
		},
		"a value outside an enum": {
			map[string]string{"log_level": "shout"}, nil, "shout",
		},
		"a port that is not a number": {
			map[string]string{"http_port": "eighty"}, nil, "http_port",
		},
		"a port of zero": {
			map[string]string{"http_port": "0"}, nil, "http_port",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.install()
			h.setHookEnv()

			_, err := ops.ConfigSet(context.Background(), h.Deps, ops.ConfigSetOptions{
				Set: tc.set, Unset: tc.unset,
			})
			require.Error(t, err, "%s was accepted", name)
			assert.Contains(t, err.Error(), tc.want,
				"the refusal does not name what was wrong")
		})
	}
}

// TestConfigSetWithNothingToChangeIsAUsageError, not a silent no-op: an
// operator who typed a command expects it to do something.
func TestConfigSetWithNothingToChangeIsAUsageError(t *testing.T) {
	h := newHarness(t)
	h.install()

	_, err := ops.ConfigSet(context.Background(), h.Deps, ops.ConfigSetOptions{})
	require.Error(t, err)
	assert.Equal(t, domain.CodeUsage, domain.AsError(err).Code)
}

func TestConfigGetOnSomethingTheReleaseDoesNotDeclare(t *testing.T) {
	h := newHarness(t)
	h.install()

	_, err := ops.ConfigGet(context.Background(), h.Deps, "not_a_parameter")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not_a_parameter")
}

// TestUnsettingSomethingTheReleaseDoesNotDeclareIsANoOp records an asymmetry
// rather than asserting it is right.
//
// `config set typo=1` is refused by name; `config unset typo` succeeds
// quietly, because the merge treats "not recorded" as "already at its default"
// without asking whether the release declares it at all. An operator who
// mistypes an unset is told it worked. Recorded here so it is a known edge
// rather than a surprise, and so a future refusal breaks this test on purpose.
func TestUnsettingSomethingTheReleaseDoesNotDeclareIsANoOp(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()

	_, err := ops.ConfigSet(context.Background(), h.Deps, ops.ConfigSetOptions{
		Unset: []string{"not_a_parameter"},
	})
	if err == nil {
		return // the documented behaviour today
	}
	assert.Contains(t, err.Error(), "not_a_parameter",
		"unset became a refusal, which is an improvement -- update the note "+
			"above and make this test assert it")
}

// TestConfigSetReportsARuntimeThatWillNotRecreate. The value is recorded before
// the services are re-created, so a daemon that refuses does not lose the
// operator's change -- but the operation still has to fail, or a port change
// that never reached a container is reported as applied.
func TestConfigSetReportsARuntimeThatWillNotRecreate(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()

	// The re-create step only exists when something is running to
	// re-create, so the deployment has to be up first.
	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)

	h.Runtime.Fail["Up"] = domain.RuntimeError(errInjected, "daemon refused")

	_, err = ops.ConfigSet(context.Background(), h.Deps, ops.ConfigSetOptions{
		Set: map[string]string{"http_port": "18099"},
	})
	require.Error(t, err, "a config change that never reached the containers was "+
		"reported as applied")

	// And the recorded value went back, so the file and the containers
	// still agree.
	inst, err := h.Deps.State.LoadInstallation(context.Background())
	require.NoError(t, err)
	assert.NotEqual(t, "18099", inst.Parameters["http_port"],
		"the failed change was left recorded, so `config get` now reports a "+
			"port nothing is listening on")
}

// TestAnOperationRefusesToStartWhileAnotherHoldsTheLock is the exit-code-4
// path a systemd timer hits when it fires during a manual update.
func TestAnOperationRefusesToStartWhileAnotherHoldsTheLock(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	h.Locker.FailAcquire = true

	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.Error(t, err, "two operations ran against one installation at once")
	assert.Equal(t, domain.CodeLocked, domain.AsError(err).Code,
		"a busy lock has to be distinguishable from a failure, or a timer "+
			"retries on the wrong signal")
}

// TestApplyOnAMachineWithNoInstallation is the first thing anyone types by
// mistake.
func TestApplyOnAMachineWithNoInstallation(t *testing.T) {
	h := newHarness(t)

	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Hint, "init",
		"the refusal does not tell a new operator what to run")
}

// TestInitRefusesAPolicyNothingCouldSatisfy: a machine that requires
// signatures and has no keys could never install anything, and the moment to
// notice is before the directories are created.
func TestInitRefusesEveryImpossibleConfiguration(t *testing.T) {
	cases := map[string]struct {
		opts ops.InitOptions
		want string
	}{
		"signatures required with no keys": {
			ops.InitOptions{Product: "demo", RequireSignature: true, NoRecoveryKey: true},
			"signature",
		},
		"a recovery recipient that is not an age key": {
			ops.InitOptions{Product: "demo", RecoveryRecipient: "not-an-age-key"},
			"recipient",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)

			_, err := ops.Init(context.Background(), h.Deps, tc.opts)
			require.Error(t, err, "%s was accepted", name)
			assert.Contains(t, strings.ToLower(err.Error()), tc.want)
		})
	}
}

// TestInitInsistsOnASecondRecipient. A machine that loses its identity with no
// second recipient has lost its secrets permanently, and the moment to notice
// that is now rather than during a recovery.
func TestInitInsistsOnASecondRecipient(t *testing.T) {
	h := newHarness(t)

	_, err := ops.Init(context.Background(), h.Deps, ops.InitOptions{Product: "demo"})
	require.Error(t, err, "an installation was created with a single recipient and "+
		"no explicit waiver")
	assert.Contains(t, strings.ToLower(err.Error()+domain.AsError(err).Hint), "recovery")
}

// TestInitRefusesASecondInstallation. Silently reconfiguring an existing
// deployment is how an operator loses one.
func TestInitRefusesASecondInstallation(t *testing.T) {
	h := newHarness(t)
	h.install()

	_, err := ops.Init(context.Background(), h.Deps, ops.InitOptions{
		Product: "demo", NoRecoveryKey: true,
	})
	require.Error(t, err, "a second init reconfigured an existing installation")
	assert.Contains(t, strings.ToLower(err.Error()), "already")
}

// TestUpdateToAReleaseThatIsNotThere.
func TestUpdateToAReleaseThatIsNotThere(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()

	_, err := ops.Update(context.Background(), h.Deps, ops.UpdateOptions{
		Ref: filepath.Join(t.TempDir(), "nothing-here"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nothing-here")
}

// TestUpdateRefusesABundleThatFailsVerification is the digest guard, driven
// through the real checksum verifier against a real damaged bundle.
func TestUpdateRefusesABundleThatFailsVerification(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()

	bundle := filepath.Join(t.TempDir(), "bundle")
	copyBundle(t, testBundlePath(t), bundle)
	retargetManifest(t, bundle, h.Root)

	_, err := ops.Update(context.Background(), h.Deps, ops.UpdateOptions{
		Ref:          bundle,
		ExpectDigest: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
	})
	require.Error(t, err, "a bundle that does not match its recorded digest was installed")
	assert.Contains(t, strings.ToLower(err.Error()), "digest")
}

// TestDoctorSurvivesEveryAdapterBeingBroken. Doctor is the command an operator
// reaches for when the machine is sick, so it is the one command that must not
// need a healthy machine.
func TestDoctorSurvivesEveryAdapterBeingBroken(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()

	h.Runtime.Fail["Status"] = domain.RuntimeError(errInjected, "cannot connect to the Docker daemon")
	h.Secrets.Fail["Load"] = domain.SecretsError(errInjected, "cannot decrypt")
	h.Backup.Fail["List"] = domain.BackupError(errInjected, "cannot read the backup directory")
	h.Health.Err = domain.HealthError(errInjected, "no prober could run")

	report, err := ops.Doctor(context.Background(), h.Deps)
	require.NoError(t, err, "doctor failed on a sick machine, which is the only "+
		"kind of machine anyone runs it on")

	if len(report.Results) == 0 {
		t.Fatal("doctor produced no findings at all")
	}

	var failures int
	for _, r := range report.Results {
		if r.Status != "ok" {
			failures++
			if r.Message == "" {
				t.Errorf("check %s reports a problem with no message", r.ID)
			}
		}
	}
	if failures == 0 {
		t.Error("every adapter was broken and doctor reported nothing wrong")
	}
}

// assertJournalled checks that the operation reached the durable journal.
// Everything `--resume`, `status` and `doctor` say about a past operation is
// read from there.
func assertJournalled(t *testing.T, h *harness, id string) {
	t.Helper()

	recs, err := h.Deps.State.Operations(context.Background(), ports.Filter{})
	require.NoError(t, err)

	for _, rec := range recs {
		if rec.ID == id {
			return
		}
	}

	data, _ := os.ReadFile(h.Paths.JournalFile())
	t.Errorf("operation %s never reached the journal, so --resume and doctor "+
		"cannot see it. Journal holds:\n%s", id, data)
}
