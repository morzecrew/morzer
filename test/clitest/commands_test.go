package clitest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/test/clitest"
)

// These drive the commands as an operator's shell does. What they pin is the
// layer between the operations and the terminal: argument validation, refusals,
// the exit codes systemd and CI read, and the shape of the `--json` envelope.

func TestVersionReportsWhatItCanRead(t *testing.T) {
	r := clitest.New(t)

	r.Run("version").ExitCode(0).StdoutContains("selfhost/v1alpha1")

	// The API versions are a list under --json so a bundle author can check
	// compatibility programmatically rather than by trial and error.
	out := r.Run("--json", "version").ExitCode(0)
	versions, ok := out.Field("data.supported_api_versions").([]any)
	if !ok || len(versions) == 0 {
		t.Fatalf("supported_api_versions is not a non-empty list: %v",
			out.Field("data.supported_api_versions"))
	}
}

func TestInitCreatesAnInstallationAndRefusesASecond(t *testing.T) {
	r := clitest.New(t)

	r.Run("init", "--release", r.Bundle, "--profile", "embedded",
		"--domain", "demo.example", "--no-recovery-recipient",
		"--install-units=false").ExitCode(0)

	if _, err := os.Stat(r.Path("etc", "demo", "installation.yaml")); err != nil {
		t.Fatalf("init did not write the installation file: %v", err)
	}

	// Running it again must not quietly reconfigure a live deployment.
	r.Run("init", "--release", r.Bundle, "--no-recovery-recipient",
		"--install-units=false").
		ExitCode(domain.ExitInstallation).
		StderrContains("already exists", "--repair")
}

// TestInitRefusesAPolicyNothingCouldSatisfy pins a refusal that exists because
// discovering it later means unwinding a half-built installation.
func TestInitRefusesAPolicyNothingCouldSatisfy(t *testing.T) {
	r := clitest.New(t)

	r.Run("init", "--product", "demo", "--no-recovery-recipient",
		"--install-units=false", "--require-signature").
		ExitCode(domain.ExitUsage).
		StderrContains("--signing-key")
}

func TestInitInsistsOnARecoveryDecision(t *testing.T) {
	r := clitest.New(t)

	// Neither a recovery recipient nor an explicit refusal of one: the
	// operator has to say which, because a machine lost without either
	// takes its secrets with it.
	r.Run("init", "--product", "demo", "--install-units=false").
		ExitCode(domain.ExitUsage).
		StderrContains("--no-recovery-recipient")
}

func TestStatusReportsAnInstallation(t *testing.T) {
	r := clitest.NewInstalled(t)

	r.Run("status").ExitCode(0).StdoutContains("demo", "1.2.0", "embedded")

	out := r.Run("--json", "status").ExitCode(0)
	out.FieldEquals("data.product", "demo")
	out.FieldEquals("ok", true)
}

func TestStatusOnAMachineWithNoInstallation(t *testing.T) {
	r := clitest.New(t)

	// Named, not a stack trace: this is what an operator sees when they run
	// the tool in the wrong place.
	r.Run("status").ExitCode(domain.ExitInstallation).StderrContains("init")
}

func TestDoctorReportsAndSetsTheExitCode(t *testing.T) {
	r := clitest.NewInstalled(t)

	// Warnings exit 0 -- a monitoring system that paged on "the release was
	// tested on a different Ubuntu" gets turned off within a week.
	res := r.Run("doctor")
	if res.Code != 0 && res.Code != domain.ExitPreflight {
		t.Fatalf("doctor exited %d, want 0 or %d", res.Code, domain.ExitPreflight)
	}
	res.StdoutContains("secrets", "what to do")

	r.Run("--json", "doctor").Field("data.summary")
}

func TestConfigListGetSetAndUnset(t *testing.T) {
	r := clitest.NewInstalled(t)

	r.Run("config", "list").ExitCode(0).
		StdoutContains("http_port", "18080", "release")

	// `get` prints the value alone, because that is what a script
	// substitutes.
	got := r.Run("config", "get", "http_port").ExitCode(0)
	if strings.TrimSpace(got.Stdout) != "18080" {
		t.Errorf("config get printed %q, want exactly the value", got.Stdout)
	}

	r.Run("config", "set", "http_port=19000").ExitCode(0)
	r.Run("config", "get", "http_port").ExitCode(0).StdoutContains("19000")
	r.Run("config", "list").ExitCode(0).StdoutContains("installation")

	r.Run("config", "unset", "http_port").ExitCode(0)
	if v := strings.TrimSpace(r.Run("config", "get", "http_port").Stdout); v != "18080" {
		t.Errorf("unset left %q, want the release default", v)
	}
}

func TestConfigRefusesWhatTheReleaseDoesNotDeclare(t *testing.T) {
	r := clitest.NewInstalled(t)

	r.Run("config", "set", "htpp_port=9000").
		ExitCode(domain.ExitUsage).
		StderrContains("htpp_port", "http_port") // names the typo and the real one

	r.Run("config", "get", "nonexistent").ExitCode(domain.ExitUsage)

	// A malformed assignment is a usage error, not a parameter named
	// "http_port" with an empty value.
	r.Run("config", "set", "http_port").ExitCode(domain.ExitUsage)
}

func TestSecretListNeverPrintsAValue(t *testing.T) {
	r := clitest.NewInstalled(t)

	// The generated password is in the state; the point is that no command
	// puts it on a stream.
	rendered := r.Run("secret", "render").ExitCode(0)
	_ = rendered

	value, err := os.ReadFile(r.Path("run", "demo", "secrets", "db_password"))
	if err != nil {
		t.Fatalf("the secret was not rendered: %v", err)
	}
	secret := strings.TrimSpace(string(value))
	if len(secret) < 8 {
		t.Fatalf("the rendered secret is implausibly short: %q", secret)
	}

	r.Run("secret", "list").ExitCode(0).
		StdoutContains("db_password").
		NoOutputContains(secret)

	r.Run("--json", "secret", "list").ExitCode(0).NoOutputContains(secret)
}

func TestSecretRotateChangesTheValue(t *testing.T) {
	r := clitest.NewInstalled(t)
	r.Run("secret", "render").ExitCode(0)

	before, err := os.ReadFile(r.Path("run", "demo", "secrets", "db_password"))
	if err != nil {
		t.Fatalf("no rendered secret: %v", err)
	}

	r.Run("secret", "rotate", "db_password").ExitCode(0).
		NoOutputContains(strings.TrimSpace(string(before)))

	r.Run("secret", "render").ExitCode(0)
	after, err := os.ReadFile(r.Path("run", "demo", "secrets", "db_password"))
	if err != nil {
		t.Fatalf("no rendered secret after rotation: %v", err)
	}
	if string(before) == string(after) {
		t.Error("rotate left the value unchanged")
	}
}

func TestSecretRotateRefusesWhatItCannotGenerate(t *testing.T) {
	r := clitest.NewInstalled(t)

	// smtp_password is declared without a generator, so `rotate` has nothing
	// to produce. A usage error, because the fix is a different command or a
	// different flag -- and the hint is the load-bearing part: pointing at
	// `rotate` there would point at a command that fails.
	r.Run("secret", "rotate", "smtp_password").
		ExitCode(domain.ExitUsage).
		StderrContains("secret set", "--kind")

	r.Run("secret", "rotate", "not_a_secret").ExitCode(domain.ExitSecrets)
}

func TestSecretRecipientsListsWhoCanDecrypt(t *testing.T) {
	r := clitest.NewInstalled(t)

	r.Run("secret", "recipients", "list").ExitCode(0).StdoutContains("age1")
	r.Run("--json", "secret", "recipients", "list").ExitCode(0)
}

func TestSecretRecipientsRefusesAKeyThatIsNotOne(t *testing.T) {
	r := clitest.NewInstalled(t)

	r.Run("secret", "recipients", "add", "not-an-age-key").
		ExitCode(domain.ExitSecrets)
}

func TestReleaseListAndShow(t *testing.T) {
	r := clitest.NewInstalled(t)

	r.Run("release", "list").ExitCode(0).StdoutContains("1.2.0")
	r.Run("release", "show").ExitCode(0).StdoutContains("demo", "1.2.0")

	out := r.Run("--json", "release", "list").ExitCode(0)
	if _, ok := out.Field("data").([]any); !ok {
		t.Errorf("release list --json should carry a list, got %T", out.Field("data"))
	}
}

func TestReleaseVerifyAcceptsTheExampleAndRefusesRubbish(t *testing.T) {
	r := clitest.New(t)

	// The verdict is a summary, and summaries go to stderr: stdout carries
	// the result an operator pipes, which here is the name and the digest.
	r.Run("release", "verify", r.Bundle).ExitCode(0).
		StdoutContains("demo", "sha256:").
		StderrContains("valid")

	r.Run("release", "verify", r.Path("nope")).
		ExitCode(domain.ExitUsage)
}

// TestReleasePruneKeepsWhatRollbackNeeds pins the rule that makes rollback
// possible: the current and previous releases are never pruned.
func TestReleasePruneKeepsWhatRollbackNeeds(t *testing.T) {
	r := clitest.NewInstalled(t)

	r.Run("release", "prune").ExitCode(0)
	r.Run("release", "list").ExitCode(0).StdoutContains("1.2.0")
}

func TestInstallationExportRefusesAnUnsafeExport(t *testing.T) {
	r := clitest.NewInstalled(t)

	// This installation was created with --no-recovery-recipient, so the
	// only key that can open its state is this machine's. An export only
	// this machine could read is not a recovery plan, and is refused.
	r.Run("installation", "export", r.Path("export.json")).
		ExitCode(domain.ExitUsage).
		StderrContains("only recipient is the exporting machine")
}

func TestInstallationImportNeedsAnIdentity(t *testing.T) {
	r := clitest.New(t)

	r.Run("installation", "import", r.Path("nothing.json")).
		ExitCode(domain.ExitUsage).
		StderrContains("--identity")
}

// TestUnknownInputIsAUsageErrorNotABug pins the mapping that stops a typo
// being reported as an internal error, which would tell an operator their
// mistake was the manager's fault.
func TestUnknownInputIsAUsageErrorNotABug(t *testing.T) {
	r := clitest.New(t)

	for _, args := range [][]string{
		{"nonsense"},
		{"status", "--nonsense"},
		{"config", "get"},                // too few arguments
		{"release", "verify"},            // a required argument missing
		{"config", "get", "a", "b", "c"}, // too many
	} {
		res := r.Run(args...)
		if res.Code != domain.ExitUsage {
			t.Errorf("`morzer %s` exited %d, want %d (usage)",
				strings.Join(args, " "), res.Code, domain.ExitUsage)
		}
	}
}

// TestRestoreRefusesWithoutTheTypedConfirmation pins the guard on the most
// destructive command there is.
//
// Asserted by *which* refusal, not merely that one happened. Every path here
// exits 2, so a test that only checked the code would pass with the guard
// removed -- the failure would just have moved to the next step.
func TestRestoreRefusesWithoutTheTypedConfirmation(t *testing.T) {
	r := clitest.NewInstalled(t)
	id, ok := r.Run("--json", "status").Field("data.installation_id").(string)
	if !ok {
		t.Fatal("status --json does not carry the installation id")
	}

	// Force alone is not confirmation.
	r.Run("restore", "--force").ExitCode(domain.ExitUsage).
		StderrContains("confirming the installation id", id)

	// Nor is confirming something else.
	r.Run("restore", "--force", "--confirm", "not-this-installation").
		ExitCode(domain.ExitUsage).
		StderrContains("confirming the installation id")

	// Confirming is not force, either: both are required, and they
	// authorise different things.
	r.Run("restore", "--confirm", id).ExitCode(domain.ExitUsage).
		StderrContains("--force")

	// With both, the guard is past and the command fails on its merits --
	// which is how this test can tell the guard from the next refusal.
	r.Run("restore", "--force", "--confirm", id).
		StderrContains("no backups exist")
}

// TestTwoInstallationsAreNamedRatherThanInvented.
//
// The reproduction from RFC 0020 §2. With two installations and no `--product`,
// path resolution fell through to the placeholder product `morzer`, so the
// machine reported "no installation found at /etc/morzer" and advised `morzer
// init` — advice that would have created a third installation on a machine whose
// problem was already having two.
//
// Both halves are asserted, because either alone still passes on the old
// behaviour: the refusal must name the installations it found, and it must not
// send the operator to `init`.
func TestTwoInstallationsAreNamedRatherThanInvented(t *testing.T) {
	r := clitest.NewInstalled(t)

	// A second installation beside the first. Only the state file matters:
	// discovery looks for `<root>/etc/<product>/installation.yaml`, which is
	// also all an operator's half-removed installation leaves behind.
	other := filepath.Join(r.Root, "etc", "other")
	require.NoError(t, os.MkdirAll(other, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(other, "installation.yaml"), []byte("product: other\n"), 0o640))

	res := r.Run("status").Failed()
	res.OutputContains("2 installations", "--product", "demo", "other")
	res.NoOutputContains("morzer init")

	res.ExitCode(domain.ExitUsage)

	// And naming one gets on with it.
	r.Run("--product", "demo", "status").ExitCode(0).OutputContains("demo")
}

// TestAnInstallationWithNoRecordedStateIsNotAnAmbiguousMachine.
//
// The branch between the two answers: discovery finds `/etc/demo/installation.yaml`
// and the state store finds no `installation.json` beside it, which is a
// half-created or half-removed installation rather than an absent one or an
// unnamed one. It must keep the message it has always had.
//
// Reachable, and this is the shape it takes: a restore that got as far as the
// configuration, an `init` interrupted between its steps, an operator who
// cleared /var/lib by hand. Without this test the branch is dead code — a
// sabotage that removed it was killed by nothing.
func TestAnInstallationWithNoRecordedStateIsNotAnAmbiguousMachine(t *testing.T) {
	r := clitest.NewInstalled(t)

	require.NoError(t, os.Remove(filepath.Join(
		r.Root, "var", "lib", "demo", "manager", "installation.json")))

	res := r.Run("status").Failed()
	res.OutputContains("no installation found")
	res.NoOutputContains("--product is required", "no installation named")
	res.ExitCode(domain.ExitInstallation)
}

// TestAProductNobodyHasIsNotAMissingFlag.
//
// The first version of the refusal keyed on the *count* alone, so an operator
// who typed `--product typo` on a two-installation machine was told that
// `--product` is required — advice they had just followed. The machine's
// inventory and whether the operator named something are two different facts,
// and the refusal needs both.
func TestAProductNobodyHasIsNotAMissingFlag(t *testing.T) {
	r := clitest.NewInstalled(t)

	other := filepath.Join(r.Root, "etc", "other")
	require.NoError(t, os.MkdirAll(other, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(other, "installation.yaml"), []byte("product: other\n"), 0o640))

	res := r.Run("--product", "typo", "status").Failed()
	res.OutputContains("no installation named", "typo", "demo", "other")
	res.NoOutputContains("--product is required")
	res.ExitCode(domain.ExitUsage)
}

// TestAConfigPathNobodyHasIsNotABareMachine.
//
// `--config` and `--product` name an installation the same way, so they must
// refuse the same way when nobody has it. They did not: path resolution answers
// `--config` from the path alone and returned before discovery ran, so the
// inventory was empty and a mistyped path on a two-installation machine was
// answered as a bare machine — `morzer init`, on the machine whose problem was
// already having two.
func TestAConfigPathNobodyHasIsNotABareMachine(t *testing.T) {
	r := clitest.NewInstalled(t)

	other := filepath.Join(r.Root, "etc", "other")
	require.NoError(t, os.MkdirAll(other, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(other, "installation.yaml"), []byte("product: other\n"), 0o640))

	// The path shape is valid and no such installation exists, which is what
	// a systemd unit for a removed installation passes.
	config := filepath.Join(r.Root, "etc", "typo", "installation.yaml")

	res := r.Run("--config", config, "status").Failed()
	res.OutputContains("no installation named", "typo", "demo", "other")
	res.NoOutputContains("morzer init")
	res.ExitCode(domain.ExitUsage)
}

// TestAnInstallationNamedLikeThePlaceholderIsStillAmbiguous.
//
// `morzer` is the product name path resolution falls back to when nothing
// selected one — and it is also the most likely name for a real installation,
// being the manager's own. When both were true the fallback stopped being a
// placeholder and became a silent choice: a bare `status` on a machine with two
// installations loaded whichever one happened to be called `morzer` and reported
// on it, which is the wrong-deployment failure the ambiguity refusal exists to
// prevent.
//
// Asserted in both directions, because the refusal must not depend on what is at
// the placeholder path: with the state present the old code succeeded, and with
// it absent the old code advised `init`.
func TestAnInstallationNamedLikeThePlaceholderIsStillAmbiguous(t *testing.T) {
	r := clitest.NewInstalled(t)

	// A second real installation, created the way an operator would.
	r.Run("init",
		"--product", "morzer",
		"--release", r.Bundle,
		"--profile", "embedded",
		"--domain", "demo.example",
		"--no-recovery-recipient",
		"--install-units=false",
	).ExitCode(0)

	res := r.Run("status").Failed()
	res.OutputContains("2 installations", "--product", "demo", "morzer")
	res.NoOutputContains("morzer init")
	res.ExitCode(domain.ExitUsage)

	// And again with its state removed, which is the branch that reported a
	// half-created installation rather than an ambiguous machine.
	require.NoError(t, os.Remove(filepath.Join(
		r.Root, "var", "lib", "morzer", "manager", "installation.json")))

	res = r.Run("status").Failed()
	res.OutputContains("2 installations", "--product")
	res.NoOutputContains("morzer init")
	res.ExitCode(domain.ExitUsage)

	// Naming one still gets on with it, including the one named like the
	// placeholder: the refusal is about nobody having chosen, not about the
	// name.
	r.Run("--product", "demo", "status").ExitCode(0).OutputContains("demo")
}

// TestDoctorOnAnAmbiguousMachineSaysWhichMachineItIs.
//
// `doctor` degrades on purpose: with no installation it drops to the checks that
// still mean something. That made it the one command where the refusal was
// swallowed — an operator who ran it because they could not tell what was wrong
// was told that /etc/morzer holds no installation and advised to create one,
// which is the placeholder answering for the machine.
func TestDoctorOnAnAmbiguousMachineSaysWhichMachineItIs(t *testing.T) {
	r := clitest.NewInstalled(t)

	other := filepath.Join(r.Root, "etc", "other")
	require.NoError(t, os.MkdirAll(other, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(other, "installation.yaml"), []byte("product: other\n"), 0o640))

	res := r.Run("doctor").Failed()
	res.OutputContains("2 installations", "--product", "demo", "other")
	res.NoOutputContains("morzer init")
}

// TestOneInstallationNeedsNoFlag. The shape every other test in this repository
// runs in, asserted on its own so the refusal above cannot start firing for it.
func TestOneInstallationNeedsNoFlag(t *testing.T) {
	clitest.NewInstalled(t).Run("status").ExitCode(0).OutputContains("demo")
}

// TestABareMachineIsStillToldToInit. The other half of the two-answer refusal:
// a machine with no installations is a machine to run `init` on, and that advice
// must survive the change that stopped giving it to everyone.
func TestABareMachineIsStillToldToInit(t *testing.T) {
	res := clitest.New(t).Run("status").Failed()
	res.OutputContains("no installation found", "morzer init")
	res.ExitCode(domain.ExitInstallation)
}
