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

// The commands that change a deployment. None of them can run to completion
// without Docker, so what is asserted here is everything up to that point:
// argument validation, the refusals, and the shape of what they print when
// they decline.

func TestBackupListOnAMachineWithNoBackups(t *testing.T) {
	r := clitest.NewInstalled(t)

	plain := r.Run("backup", "list").ExitCode(0)
	plain.OutputContains("no backups")

	// `data` is null rather than an empty list. Recorded rather than
	// asserted as right: a consumer writing `.data | length` gets an error
	// instead of 0, which is the kind of thing a JSON contract is supposed
	// to spare them.
	empty := r.Run("backup", "list", "--json").ExitCode(0)
	if got := empty.Field("data"); got != nil {
		t.Errorf("data = %v; if this is now a list, tighten the assertion", got)
	}
}

func TestBackupVerifyNeedsABackupThatExists(t *testing.T) {
	r := clitest.NewInstalled(t)

	// No argument and nothing to verify.
	r.Run("backup", "verify").Failed()

	// A named backup on a machine that has none at all. The message is
	// about the absence rather than about the id, which is the right order:
	// an operator who has taken no backups does not need to be told their
	// id was not found.
	out := r.Run("backup", "verify", "20260101T000000Z")
	out.Failed().OutputContains("no backups exist")
	out.OutputContains("morzer backup")
}

func TestBackupRefusesAComponentNobodyDefined(t *testing.T) {
	r := clitest.NewInstalled(t)

	out := r.Run("backup", "--component", "databsae")
	out.ExitCode(domain.ExitUsage)
	out.OutputContains("databsae")
	out.OutputContains("database")
}

func TestUpdateRefusesWhatItCannotResolve(t *testing.T) {
	r := clitest.NewInstalled(t)

	// No bundle and no --to.
	r.Run("update").Failed()

	// A path that is not there.
	r.Run("update", filepath.Join(t.TempDir(), "gone")).Failed().
		OutputContains("gone")

	// A version that is not in the store.
	r.Run("update", "--to", "9.9.9").Failed().OutputContains("9.9.9")

	// Both at once is ambiguous, and guessing which one the operator meant
	// is how the wrong release gets installed.
	both := r.Run("update", r.Bundle, "--to", "1.2.0")
	both.Failed()
}

func TestRollbackWithNothingToRollBackTo(t *testing.T) {
	r := clitest.NewInstalled(t)

	out := r.Run("rollback", "--force")
	out.Failed().OutputContains("previous")
}

func TestRollbackToAVersionThatIsNotInstalled(t *testing.T) {
	r := clitest.NewInstalled(t)

	r.Run("rollback", "--force", "--to", "9.9.9").Failed().
		OutputContains("9.9.9")
}

// TestInitRefusesASecondInstallation. Silently reconfiguring an existing
// deployment is how an operator loses one.
func TestInitRefusesASecondInstallation(t *testing.T) {
	r := clitest.NewInstalled(t)

	out := r.Run("init", "--product", "demo", "--no-recovery-recipient")
	out.Failed().OutputContains("already")
}

func TestInitRefusesAParameterTheReleaseDoesNotDeclare(t *testing.T) {
	r := clitest.New(t)

	out := r.Run("init", "--release", r.Bundle, "--no-recovery-recipient",
		"--set", "not_a_parameter=1")
	out.Failed().OutputContains("not_a_parameter")
}

// TestInitRefusesAParameterWithNoDefaultAndNoValue.
//
// A declaration without a default is the only way a manifest can say "the
// operator must choose this", and `init` is the one command that can be told --
// so it is the one command that refuses. Everything later resolves it as
// present-and-empty, because an `apply` reading months-old state cannot supply
// a value and taking the deployment down over a knob nobody touched would be
// worse than an empty string.
func TestInitRefusesAParameterWithNoDefaultAndNoValue(t *testing.T) {
	r := clitest.New(t)

	// The vendor declares a knob and no default for it.
	manifest := filepath.Join(r.Bundle, "manifest.yaml")
	data, err := os.ReadFile(manifest)
	require.NoError(t, err)
	edited := strings.Replace(string(data),
		"parameters:\n",
		"parameters:\n  admin_email:\n    type: string\n    description: Where alerts go\n",
		1)
	// Loudly, because a silent no-op here fails the test at the exit-code
	// assertion below and points nowhere near the manifest that drifted.
	require.NotEqual(t, string(data), edited,
		"the fixture no longer contains a `parameters:` block to add to")
	require.NoError(t, os.WriteFile(manifest, []byte(edited), 0o644))

	out := r.Run("init", "--release", r.Bundle, "--no-recovery-recipient",
		"--install-units=false")
	out.Failed().OutputContains("admin_email")
	out.OutputContains("--set admin_email=")

	// Given a value, the same install proceeds.
	r.Run("init", "--release", r.Bundle, "--no-recovery-recipient",
		"--install-units=false", "--set", "admin_email=ops@example").ExitCode(0)
}

func TestInitTakesTheProductNameFromTheBundle(t *testing.T) {
	r := clitest.New(t)

	// No --product: the manifest names it, and asking an operator to
	// repeat what the bundle already says is how the two disagree.
	out := r.Run("init", "--release", r.Bundle, "--no-recovery-recipient",
		"--install-units=false")
	out.ExitCode(0)

	r.Run("status").ExitCode(0).OutputContains("demo")
}

func TestInitWithoutAProductNameAndWithoutABundle(t *testing.T) {
	r := clitest.New(t)

	// Nothing to infer from, and no terminal to ask at.
	out := r.Run("init", "--no-recovery-recipient")
	out.ExitCode(domain.ExitUsage)
	out.OutputContains("--product")
}

// TestInitRepairRestoresTheLayout, which is what an operator runs after
// something removed a managed directory.
func TestInitRepairRestoresTheLayout(t *testing.T) {
	r := clitest.NewInstalled(t)

	// The second init refuses; with --repair it restores instead.
	// --install-units=false for the same reason NewInstalled uses it: the
	// unit steps need systemd and root, and neither is available to a test.
	r.Run("init", "--product", "demo", "--no-recovery-recipient", "--repair",
		"--install-units=false").ExitCode(0)
}

func TestConfigCommands(t *testing.T) {
	r := clitest.NewInstalled(t)

	list := r.Run("config", "list").ExitCode(0)
	list.OutputContains("http_port", "log_level")

	r.Run("config", "list", "--json").ExitCode(0).Field("data")

	get := r.Run("config", "get", "http_port").ExitCode(0)
	get.OutputContains("18080")

	r.Run("config", "get", "not_a_parameter").Failed().
		OutputContains("not_a_parameter")

	// Setting is an engine operation and needs a runtime; the refusal for
	// an undeclared name happens before any of that.
	r.Run("config", "set", "not_a_parameter=1").Failed().
		OutputContains("not_a_parameter")
}

func TestStatusAndDoctorOnAFreshMachine(t *testing.T) {
	r := clitest.New(t)

	// Both have to work before anything is installed: they are what an
	// operator runs when they are not sure what state they are in.
	r.Run("status").Failed().OutputContains("init")
	r.Run("doctor").Failed().OutputContains("init")
}

func TestVersionAndHelpNeedNoInstallation(t *testing.T) {
	r := clitest.New(t)

	r.Run("version").ExitCode(0).StdoutContains("test")
	r.Run("version", "--json").ExitCode(0).Field("data")
	r.Run("--help").ExitCode(0).StdoutContains("morzer")
}

// TestUnsettingAParameterWithNoDefaultIsRefused.
//
// `config unset` is the other command that can be told a value, so it is the
// other command that refuses to leave a required one without one. Resolution
// treats an absent value as present-and-empty -- which is what keeps `apply`
// working on months-old state -- so nothing downstream would have objected.
func TestUnsettingAParameterWithNoDefaultIsRefused(t *testing.T) {
	r := clitest.New(t)

	manifest := filepath.Join(r.Bundle, "manifest.yaml")
	data, err := os.ReadFile(manifest)
	require.NoError(t, err)
	edited := strings.Replace(string(data),
		"parameters:\n",
		"parameters:\n  admin_email:\n    type: string\n    description: Where alerts go\n",
		1)
	require.NotEqual(t, string(data), edited,
		"the fixture no longer contains a `parameters:` block to add to")
	require.NoError(t, os.WriteFile(manifest, []byte(edited), 0o644))

	r.Run("init", "--release", r.Bundle, "--no-recovery-recipient",
		"--install-units=false", "--set", "admin_email=ops@example").ExitCode(0)

	out := r.Run("config", "unset", "admin_email")
	out.Failed().OutputContains("admin_email")
	out.OutputContains("config set")
}
