package ports_test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// A release reference is operator input, and the one place a wrong answer is
// dangerous: plaintext http would fetch code over a channel anyone can rewrite.
func TestParseRefAcceptsEverySupportedForm(t *testing.T) {
	cases := map[string]struct{ scheme, location string }{
		"./bundle":                          {"file", "./bundle"},
		"/opt/demo/1.2.0":                   {"file", "/opt/demo/1.2.0"},
		"bundle-1.2.0.tar.zst":              {"file", "bundle-1.2.0.tar.zst"},
		"file:///opt/demo":                  {"file", "/opt/demo"},
		"file://localhost/opt/demo":         {"file", "/opt/demo"},
		"file://LOCALHOST/opt/demo":         {"file", "/opt/demo"},
		"https://example.com/b.tar.zst":     {"https", "example.com/b.tar.zst"},
		"oci://registry.example/demo:1.2.0": {"oci", "registry.example/demo:1.2.0"},
	}

	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			ref, err := ports.ParseRef(input)
			if err != nil {
				t.Fatalf("ParseRef(%q): %v", input, err)
			}
			if ref.Scheme != want.scheme || ref.Location != want.location {
				t.Errorf("got {%s, %s}, want {%s, %s}",
					ref.Scheme, ref.Location, want.scheme, want.location)
			}
		})
	}
}

// TestParseRefRefusesPlaintextHTTP is the one refusal here that is a security
// property: a bundle fetched over http is code fetched over a channel anyone
// on the path can rewrite.
func TestParseRefRefusesPlaintextHTTP(t *testing.T) {
	_, err := ports.ParseRef("http://example.com/bundle.tar.zst")
	if err == nil {
		t.Fatal("a plaintext http reference was accepted")
	}
	// The hint has to offer a way forward, or an operator just reaches for
	// a flag that does not exist.
	hint := domain.AsError(err).Hint
	if !strings.Contains(hint, "https") {
		t.Errorf("the refusal does not offer an alternative: %q", hint)
	}
}

func TestParseRefRefusesWhatItCannotUse(t *testing.T) {
	for _, input := range []string{
		"",
		"   ",
		"ftp://example.com/bundle",
		"s3://bucket/bundle",
		// Two slashes, so "home" parses as a URL host. Dropping it would
		// resolve to /user/bundle -- a different path than was written.
		"file://home/user/bundle",
		// localhost with a port is not the local filesystem.
		"file://localhost:8080/opt/demo",
	} {
		if _, err := ports.ParseRef(input); err == nil {
			t.Errorf("ParseRef(%q) was accepted", input)
		}
	}
}

func TestRefRoundTripsThroughItsString(t *testing.T) {
	cases := []string{
		"./bundle",
		"https://example.com/b.tar.zst",
		"oci://registry.example/demo:1.2.0",
	}
	for _, input := range cases {
		ref, err := ports.ParseRef(input)
		if err != nil {
			t.Fatal(err)
		}
		// A reference printed into an error message or a journal entry
		// has to be the one an operator typed, or it is not a reference
		// they can act on.
		again, err := ports.ParseRef(ref.String())
		if err != nil {
			t.Fatalf("%q did not survive a round trip: %v", input, err)
		}
		if again.Scheme != ref.Scheme || again.Location != ref.Location {
			t.Errorf("%q round-tripped to {%s, %s}", input, again.Scheme, again.Location)
		}
	}
}

// TestCredentialsPrintWhatIsSetAndNeverWhatItIs.
//
// These are live credentials in ordinary string fields, so a %+v while
// diagnosing a failed push -- the moment somebody is most likely to reach for
// one -- printed the private key into the log.
func TestCredentialsPrintWhatIsSetAndNeverWhatItIs(t *testing.T) {
	ref := ports.TargetRef{
		Scheme: "ssh", Host: "backups.example", Path: "/srv/backups",
		Credentials: ports.TargetCredentials{
			PrivateKey:      "-----BEGIN OPENSSH PRIVATE KEY-----\nMIIEpAIBAAK\n",
			Passphrase:      "correct horse battery staple",
			SecretAccessKey: "wJalrXUtnFEMI",
			Region:          "eu-central-1",
		},
	}

	// Every verb somebody reaches for, including the ones fmt applies to
	// nested fields.
	for _, printed := range []string{
		fmt.Sprintf("%v", ref),
		fmt.Sprintf("%+v", ref),
		fmt.Sprintf("%#v", ref),
		ref.Credentials.String(),
	} {
		for _, secret := range []string{
			"MIIEpAIBAAK", "correct horse battery staple", "wJalrXUtnFEMI",
		} {
			if strings.Contains(printed, secret) {
				t.Errorf("a credential was printed:\n%s", printed)
			}
		}
	}

	// What it does say is which credentials are configured, which is the
	// part a diagnosis actually needs.
	summary := ref.Credentials.String()
	for _, want := range []string{"private_key=<set>", "passphrase=<set>", "region=eu-central-1"} {
		if !strings.Contains(summary, want) {
			t.Errorf("the summary does not say %q: %s", want, summary)
		}
	}
	if got := (ports.TargetCredentials{}).String(); got != "TargetCredentials{}" {
		t.Errorf("empty credentials print as %q", got)
	}

	// And through encoding/json, which String does not reach. The fields
	// carry json tags because they are read from a secret document, so any
	// marshal of a ref -- a --json envelope, a captured event -- would
	// otherwise carry the private key with it.
	encoded, err := json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"MIIEpAIBAAK", "correct horse battery staple", "wJalrXUtnFEMI",
	} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("a credential was serialised:\n%s", encoded)
		}
	}
	// An endpoint is a URL, and a URL can carry userinfo -- a credential
	// wearing a hostname, which the summary printed verbatim.
	withUser := ports.TargetCredentials{Endpoint: "https://key:s3cr3t@minio.example:9000"}
	for _, printed := range []string{withUser.String(), string(mustJSON(t, withUser))} {
		if strings.Contains(printed, "s3cr3t") || strings.Contains(printed, "key:") {
			t.Errorf("an endpoint's userinfo was printed: %s", printed)
		}
		if !strings.Contains(printed, "minio.example") {
			t.Errorf("the host an operator needs for a diagnosis was dropped: %s", printed)
		}
	}

	// The shape survives: which credentials are configured is what a
	// consumer legitimately reports.
	for _, want := range []string{`"private_key":"[redacted]"`, `"region":"eu-central-1"`} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("the envelope does not carry %s:\n%s", want, encoded)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestUnitStateFailedIsTheOneStateThatMatters(t *testing.T) {
	if !(ports.UnitState{Active: "failed"}).Failed() {
		t.Error("a failed unit does not report itself failed")
	}
	for _, active := range []string{"active", "inactive", "activating", ""} {
		if (ports.UnitState{Active: active}).Failed() {
			t.Errorf("a unit that is %q was reported failed", active)
		}
	}
}

func TestExitResultOK(t *testing.T) {
	if !(ports.ExitResult{ExitCode: 0}).OK() {
		t.Error("exit 0 is not OK")
	}
	// 2 is "nothing to do" under the hook ABI, and is still not OK here:
	// the caller decides what a non-zero exit means, not this type.
	for _, code := range []int{1, 2, 137} {
		if (ports.ExitResult{ExitCode: code}).OK() {
			t.Errorf("exit %d reported OK", code)
		}
	}
}

func TestBackupRefIsZeroWhenNothingWasTaken(t *testing.T) {
	if !(ports.BackupRef{}).IsZero() {
		t.Error("an empty backup reference does not report itself zero")
	}
	if (ports.BackupRef{ID: "bk_01"}).IsZero() {
		t.Error("a backup with an id reported itself zero")
	}
}

// TestHookEnvVarsAlwaysCarriesDryRun pins the ABI rule a hook depends on:
// testing for the variable's presence rather than its value must not mutate
// during a plan.
func TestHookEnvVarsAlwaysCarriesDryRun(t *testing.T) {
	for _, dry := range []bool{true, false} {
		vars := ports.HookEnvVars(ports.HookEnv{Product: "demo", DryRun: dry})
		if _, ok := vars["DEMO_DRY_RUN"]; !ok {
			t.Errorf("DRY_RUN is absent when DryRun=%v, so a hook testing for it would mutate", dry)
		}
	}
}

func TestHookEnvPrefixSurvivesAnAwkwardProductName(t *testing.T) {
	cases := map[string]string{
		"demo":   "DEMO",
		"web-ui": "WEB_UI",
		"a.b":    "A_B",
		"":       "PRODUCT", // a fallback, so a variable is never named "_X"
	}
	for product, want := range cases {
		if got := (ports.HookEnv{Product: product}).Prefix(); got != want {
			t.Errorf("Prefix(%q) = %q, want %q", product, got, want)
		}
	}
}

// TestEveryValidProductNameIsAUsableVariablePrefix is the other half of that
// rule, and the reason domain.ValidateProductName refuses a leading digit.
//
// The product name is not only a path component: uppercased, it is the
// namespace of every variable a hook reads and every ${...} a Compose file
// interpolates. A name like "3cx" yields ${3CX_PARAM_...}, which no POSIX shell
// and no Compose file can reference -- so each parameter falls back to its `:-`
// default and the deployment comes up configured with none of the operator's
// values, silently.
func TestEveryValidProductNameIsAUsableVariablePrefix(t *testing.T) {
	// POSIX: a name is a letter or underscore, then letters, digits and
	// underscores.
	usable := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

	for _, name := range []string{"demo", "my-product", "product2", "a", "x-9-y"} {
		if err := domain.ValidateProductName(name); err != nil {
			t.Fatalf("%q is meant to be a valid product name: %v", name, err)
		}
		prefix := (ports.HookEnv{Product: name}).Prefix()
		if !usable.MatchString(prefix) {
			t.Errorf("%q is accepted but yields ${%s_...}, which nothing can interpolate",
				name, prefix)
		}
	}

	for _, name := range []string{"3cx", "2fa-portal", "9"} {
		if err := domain.ValidateProductName(name); err == nil {
			t.Errorf("%q is accepted, and its prefix %q cannot name a variable",
				name, (ports.HookEnv{Product: name}).Prefix())
		}
	}
}

func TestHookEnvVarsOmitsWhatIsNotSet(t *testing.T) {
	vars := ports.HookEnvVars(ports.HookEnv{Product: "demo"})

	// An empty value would be indistinguishable from a real one, and a hook
	// using `${VAR:?}` needs absence to mean absence.
	if _, ok := vars["DEMO_BACKUP_DIR"]; ok {
		t.Error("an unset directory was exported as an empty variable")
	}
	if vars["DEMO_PRODUCT"] != "demo" {
		t.Errorf("the product is not exported: %v", vars["DEMO_PRODUCT"])
	}
}

// The two service-state predicates are conservative in opposite directions, and
// that is the point rather than an inconsistency.
//
// OccupiesVolume decides whether writing is safe, so an unrecognised state must
// count as occupied and refuse. Quiescible decides whether a service can be
// stopped *and put back*, so an unrecognised state must not be touched.
// Collapsing them into one predicate is how `removing` -- which occupies a
// volume but cannot be started again -- turned a transient state into a failed
// backup claiming the deployment was down.
func TestTheTwoServiceStatePredicatesAreConservativeInOppositeDirections(t *testing.T) {
	cases := []struct {
		state      string
		occupies   bool
		quiescible bool
	}{
		{ports.StateRunning, true, true},
		{ports.StatePaused, true, true},
		{ports.StateRestarting, true, true},
		{ports.StateRemoving, true, false},
		{ports.StateExited, false, false},
		{ports.StateCreated, false, false},
		{ports.StateDead, false, false},
		{"", false, false},
		{"  RUNNING  ", true, true},
		// A state no version of this manager has seen.
		{"hibernating", true, false},
	}

	for _, c := range cases {
		s := ports.ServiceState{Name: "app", State: c.state}
		if got := s.OccupiesVolume(); got != c.occupies {
			t.Errorf("OccupiesVolume(%q) = %v, want %v -- an unrecognised state "+
				"must refuse a restore", c.state, got, c.occupies)
		}
		if got := s.Quiescible(); got != c.quiescible {
			t.Errorf("Quiescible(%q) = %v, want %v -- an unrecognised state "+
				"must not be stopped", c.state, got, c.quiescible)
		}
	}
}

// An unhealthy container still holds its files open. Running() is about whether
// the product is serving; OccupiesVolume is about whether its data can be
// written underneath it, and they are not the same question.
func TestAnUnhealthyServiceStillOccupiesItsVolume(t *testing.T) {
	s := ports.ServiceState{Name: "app", State: ports.StateRunning, Health: ports.HealthUnhealthy}

	if s.Running() {
		t.Fatal("the fixture is not the state under test")
	}
	if !s.OccupiesVolume() {
		t.Error("an unhealthy container was treated as having released its volume, " +
			"so a restore would untar over files it still holds open")
	}
}
