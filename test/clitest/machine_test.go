package clitest_test

import (
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
