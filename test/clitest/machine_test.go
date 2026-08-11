package clitest_test

import (
	"os"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/test/clitest"
)

// A machine with more than one installation.
//
// The layout has always supported it -- every path, unit and lock is keyed by
// product -- and until RFC 0020 no command acknowledged it. These drive the two
// halves that answer for it: the listing, and what every *other* command does
// when nobody has said which installation they mean.

// withTwoInstallations returns a runner over a machine holding `demo` and
// `sandbox`.
//
// Built through `init` rather than by writing state files: a fixture assembled
// by hand is a fixture that keeps passing against a shape nothing produces any
// more.
func withTwoInstallations(t *testing.T) *clitest.Runner {
	t.Helper()

	r := clitest.NewInstalled(t)
	r.Run("init",
		"--product", "sandbox",
		"--release", r.Bundle,
		"--profile", "embedded",
		"--no-recovery-recipient",
		"--install-units=false",
	).ExitCode(0)
	return r
}

func TestListingNamesEveryInstallationOnTheMachine(t *testing.T) {
	r := withTwoInstallations(t)

	out := r.Run("ls").ExitCode(0)
	out.StdoutContains("PRODUCT", "demo", "sandbox")

	// The long name is the same command. Both exist because the short one
	// is what somebody types on a machine they have just logged into and
	// the long one is where the noun hierarchy puts it.
	alias := r.Run("installation", "list").ExitCode(0)
	if alias.Stdout != out.Stdout {
		t.Errorf("`installation list` and `ls` disagree:\n--- ls ---\n%s--- installation list ---\n%s",
			out.Stdout, alias.Stdout)
	}
}

// TestListingIsTheOneCommandAnAmbiguousMachineAnswers.
//
// `ls` exists precisely for this machine, so it must be the one command that
// does not refuse on it -- and the refusal the others give has to point here,
// or the operator is told what is wrong with no way to find out what they have.
func TestListingIsTheOneCommandAnAmbiguousMachineAnswers(t *testing.T) {
	r := withTwoInstallations(t)

	r.Run("ls").ExitCode(0).StdoutContains("demo", "sandbox")

	refused := r.Run("status").ExitCode(2)
	refused.SaysAll("this machine has 2 installations", "morzer ls")
}

// TestAListingReportsAnInstallationItCannotRead.
//
// Verified red against the alternative implementation: skipping a row whose
// state will not parse and reporting one whose state does. "Skipped" and
// "reported" look identical in a one-installation fixture, which is why the
// machine here has two and only one of them is broken.
//
// The file corrupted is the state store, not /etc/<product>/installation.yaml:
// that one is a report nothing reads back, and a test that broke it would
// assert nothing at all.
func TestAListingReportsAnInstallationItCannotRead(t *testing.T) {
	r := withTwoInstallations(t)
	r.Corrupt("var/lib/sandbox/manager/installation.json")

	out := r.Run("ls").ExitCode(0)

	// Both rows, and the working one intact: a broken neighbour must not
	// cost the reader the installation they came to look at.
	out.StdoutContains("demo", "sandbox", "unreadable")
	if !strings.Contains(out.Stdout, "1.2.0") {
		t.Errorf("the readable installation lost its release to its neighbour's problem:\n%s",
			out.Stdout)
	}

	entries := r.Run("ls", "--json").ExitCode(0)
	entries.FieldLen("data", 2)

	rows, _ := entries.JSON()["data"].([]any)
	broken, _ := rows[1].(map[string]any)
	if problem, _ := broken["problem"].(string); problem == "" {
		t.Errorf("the unreadable installation carries no problem in --json:\n%s", entries.Stdout)
	}
	// Nothing interpreted beside it. A row that says both "I cannot read
	// this" and "its mode is production" is the reading this rule exists to
	// prevent -- and a future schema is where it bites, because the fields
	// would be half-understood rather than absent.
	if _, ok := broken["schema_version"]; ok {
		t.Errorf("the unreadable installation reports interpreted fields anyway:\n%s",
			entries.Stdout)
	}
}

// TestTheCommandsThatReadTheStoreRefuseAnAmbiguousMachine is RFC 0020 §9,
// settled.
//
// These four read the release store, the secret state and the parameters
// without loading an installation, so the refusal that lived in the lookup
// never fired for them: on a machine with two installations they answered about
// the placeholder layout -- `no releases are installed` -- which is an answer an
// operator acts on rather than an error they investigate.
func TestTheCommandsThatReadTheStoreRefuseAnAmbiguousMachine(t *testing.T) {
	r := withTwoInstallations(t)

	for _, argv := range [][]string{
		{"release", "list"},
		{"secret", "list"},
		{"config", "list"},
		{"backup", "list"},
	} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			out := r.Run(argv...).ExitCode(2)
			out.SaysAll("this machine has 2 installations")
			out.NoOutputContains("no releases are installed")
		})
	}
}

// TestNamingAnInstallationAnswersOnTheSameMachine is the other half: the
// refusal is about ambiguity and nothing else, so every route that resolves it
// has to work. All three are documented, and the environment variable is the
// one nothing else covers.
func TestNamingAnInstallationAnswersOnTheSameMachine(t *testing.T) {
	r := withTwoInstallations(t)

	// Asserted on `status`, which names the installation it read. `release
	// list` would exit 0 on the placeholder layout too — it has no releases
	// either — so a selector that silently selected nothing would pass a
	// test that only checked the exit code, which is the exact shape of the
	// defect §9 was about.
	r.Run("--product", "sandbox", "status").ExitCode(0).StdoutContains("sandbox")
	r.Run("--config", r.Path("etc", "demo", "installation.yaml"), "status").
		ExitCode(0).StdoutContains("demo")

	t.Setenv("MORZER_PRODUCT", "sandbox")
	r.Run("status").ExitCode(0).StdoutContains("sandbox")

	// And the flag outranks it, which is what makes the variable usable in
	// a session pinned to one installation.
	r.Run("--product", "demo", "status").ExitCode(0).StdoutContains("demo")
}

// TestVersionAnswersOnAnAmbiguousMachine pins the commands the refusal must not
// reach. They are the ones an operator needs *on this machine*: a refusal that
// covered them would leave somebody unable to run the commands that resolve the
// ambiguity.
func TestVersionAnswersOnAnAmbiguousMachine(t *testing.T) {
	r := withTwoInstallations(t)

	r.Run("version").ExitCode(0)
	r.Run("release", "verify", r.Bundle).ExitCode(0)
	r.Run("doctor").Failed().SaysAll("this machine has 2 installations")
}

// TestInspectingABundleNeedsNoInstallation.
//
// `release show ./bundle` reads a directory named on the command line and works
// on a machine with no installation at all — which the scope declaration nearly
// took away, because a scope is resolved before the argument is parsed and the
// argument is what decides. The other two forms read this installation's store
// and are refused, from inside the resolver rather than from the pre-run.
func TestInspectingABundleNeedsNoInstallation(t *testing.T) {
	r := withTwoInstallations(t)

	r.Run("release", "show", r.Bundle).ExitCode(0).StdoutContains("demo", "1.2.0")

	r.Run("release", "show").ExitCode(2).SaysAll("this machine has 2 installations")
	r.Run("release", "show", "1.2.0").ExitCode(2).SaysAll("this machine has 2 installations")

	// And naming one answers, which is the half that would make a refusal
	// everywhere look like the same behaviour.
	r.Run("--product", "demo", "release", "show").ExitCode(0).StdoutContains("1.2.0")
}

// TestADirectoryNobodyCouldOpenIsListedAndNotCounted.
//
// `/etc` is a shared namespace: a `/etc/<product>` is 0750 root-only by
// construction, and so are several of the host's own directories, so an
// unprivileged process cannot tell one from the other. Failing would make the
// manager unusable for any non-root invocation on a real machine; counting them
// would tell an operator with one deployment that they have four.
//
// So it is listed, marked, and left out of the count — and `morzer ls` is what
// says an empty-looking machine may not be empty.
func TestADirectoryNobodyCouldOpenIsListedAndNotCounted(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root traverses a 0000 directory, so this arrangement proves nothing")
	}
	r := withTwoInstallations(t)

	sealed := r.Path("etc", "sandbox")
	if err := os.Chmod(sealed, 0o000); err != nil {
		t.Fatalf("cannot seal %s: %v", sealed, err)
	}
	t.Cleanup(func() { _ = os.Chmod(sealed, 0o755) })

	listing := r.Run("ls").ExitCode(0)
	listing.StdoutContains("demo", "sandbox")
	listing.SaysAll("not counted as an installation")

	// Not counted: the machine now has one installation this process can
	// see, so nothing is ambiguous and the command answers about it.
	r.Run("status").ExitCode(0).StdoutContains("demo")

	// And the row says so in the machine contract, so a script can tell a
	// deployment from a directory nobody could open.
	rows, _ := r.Run("ls", "--json").ExitCode(0).JSON()["data"].([]any)
	skipped, _ := rows[1].(map[string]any)
	if skipped["skipped"] != true {
		t.Errorf("the unreadable directory is not marked in --json: %v", skipped)
	}
}

// TestAnEtcNobodyCouldReadIsNotABareMachine is the whole root, rather than one
// directory inside it: there the distinction between "nothing here" and "I
// cannot look" is the manager's own, and a command that acts on an installation
// must not answer from the placeholder layout.
func TestAnEtcNobodyCouldReadIsNotABareMachine(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 directory, so this arrangement proves nothing")
	}
	r := withTwoInstallations(t)

	etc := r.Path("etc")
	if err := os.Chmod(etc, 0o000); err != nil {
		t.Fatalf("cannot seal %s: %v", etc, err)
	}
	t.Cleanup(func() { _ = os.Chmod(etc, 0o755) })

	refused := r.Run("status").Failed()
	refused.SaysAll("cannot read")
	refused.NoOutputContains("run `morzer init`")

	// A command about the machine still answers: refusing those would leave
	// nobody able to run the commands that diagnose the machine.
	r.Run("version").ExitCode(0)
}

// TestGeneratingARecoveryKeyNeedsNoInstallation.
//
// It writes a keypair to a path you name and reads no installation at all — it
// is what you run *before* `init`, so inheriting `secret`'s installation scope
// refused it on exactly the machine where a recovery key is being prepared.
func TestGeneratingARecoveryKeyNeedsNoInstallation(t *testing.T) {
	r := withTwoInstallations(t)

	out := r.Run("secret", "recipients", "generate-recovery-key",
		r.Path("recovery.key")).ExitCode(0)
	out.StdoutContains("age1")

	// Its neighbours read this installation's secret state and are refused.
	r.Run("secret", "recipients", "list").ExitCode(2).
		SaysAll("this machine has 2 installations")
}

// TestDoctorDiagnosesTheMachineItCannotChooseOn is decision 5d.
//
// `checkInstallationReadable` is fatal and fails first on an ambiguous machine.
// The runner continues, and the two machine-scope checks are the reason it must:
// refusing wholesale would take the diagnostic away at the exact moment the
// diagnosis is "you have two installations".
func TestDoctorDiagnosesTheMachineItCannotChooseOn(t *testing.T) {
	r := withTwoInstallations(t)

	out := r.Run("doctor", "--verbose", "--json").Failed()

	data, ok := out.JSON()["data"].(map[string]any)
	if !ok {
		t.Fatalf("`doctor --json` carries no report:\n%s", out.Stdout)
	}
	results, ok := data["results"].([]any)
	if !ok {
		t.Fatalf("the report carries no results:\n%s", out.Stdout)
	}

	ids := map[string]string{}
	for _, raw := range results {
		result, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id, _ := result["id"].(string)
		ids[id], _ = result["status"].(string)
	}

	for _, id := range []string{"machine.installations", "machine.ports"} {
		if _, ran := ids[id]; !ran {
			t.Errorf("%s did not run on a machine nobody chose an installation on, "+
				"so `doctor` reported only that it could not choose:\n%s", id, out.Stdout)
		}
	}
	if ids["config.installation"] != "fail" {
		t.Errorf("the ambiguity was not reported as a failed check: %q", ids["config.installation"])
	}
}
