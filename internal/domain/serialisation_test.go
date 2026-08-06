package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Everything here crosses a boundary somebody else reads: the `--json`
// envelope a monitoring script parses, the installation file an operator
// edits, the manifest a vendor writes. A serialisation that accepts what it
// should refuse produces a value nothing later can explain.

func TestPortSpecAcceptsBothShapesAVendorWrites(t *testing.T) {
	// A port is a number to most people and a template to anyone following a
	// parameter, and YAML gives the two different types.
	var manifest struct {
		Ports []PortSpec `yaml:"ports" json:"ports"`
	}

	if err := json.Unmarshal([]byte(`{"ports":[8080,"{{ .Parameters.http_port }}","443"]}`),
		&manifest); err != nil {
		t.Fatal(err)
	}
	want := []string{"8080", "{{ .Parameters.http_port }}", "443"}
	if len(manifest.Ports) != 3 {
		t.Fatalf("got %d ports: %v", len(manifest.Ports), manifest.Ports)
	}
	for i, w := range want {
		if string(manifest.Ports[i]) != w {
			t.Errorf("ports[%d] = %q, want %q", i, manifest.Ports[i], w)
		}
	}
}

func TestPortSpecRefusesWhatIsNeither(t *testing.T) {
	var manifest struct {
		Ports []PortSpec `json:"ports"`
	}
	for _, bad := range []string{`{"ports":[{"a":1}]}`, `{"ports":[[8080]]}`, `{"ports":[true]}`} {
		if err := json.Unmarshal([]byte(bad), &manifest); err == nil {
			t.Errorf("%s was accepted as a port list", bad)
		}
	}
}

// TestAManifestWithNoBackupSectionDoesNotGrowOne. `release show --json` is
// parsed by other people's scripts, and encoding/json does not omit an empty
// struct under omitempty -- so an optional section tagged that way appears in
// every release ever written, including the ones from before it existed.
func TestAManifestWithNoBackupSectionDoesNotGrowOne(t *testing.T) {
	// Decoded rather than searched as text: `providers.backup` is a
	// different field of the same name, and a substring match would pass
	// while the section was still there.
	sections := func(m Manifest) map[string]json.RawMessage {
		out, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(out, &doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}

	if section, ok := sections(validManifest())["backup"]; ok {
		t.Errorf("a release that declares no backup section publishes %s anyway, "+
			"so every consumer of --json sees a section its manifest never had",
			section)
	}

	// And a declared one is still published, or the omission would hide
	// what the vendor did say.
	m := validManifest()
	m.Backup = BackupSpec{Volumes: map[string]VolumeSpec{"uploads": {Consistency: VolumeHot}}}
	section, ok := sections(m)["backup"]
	if !ok || !strings.Contains(string(section), `"consistency":"hot"`) {
		t.Errorf("a declared consistency is missing from --json (%s), so nothing "+
			"downstream can tell how the volume was captured", section)
	}
}

func TestDurationRoundTripsThroughText(t *testing.T) {
	cases := map[string]time.Duration{
		"30s":   30 * time.Second,
		"10m":   10 * time.Minute,
		"2h":    2 * time.Hour,
		"1h30m": 90 * time.Minute,
		"500ms": 500 * time.Millisecond,
		"0s":    0,
	}

	for text, want := range cases {
		var d Duration
		if err := d.UnmarshalText([]byte(text)); err != nil {
			t.Errorf("%q: %v", text, err)
			continue
		}
		if time.Duration(d) != want {
			t.Errorf("%q = %v, want %v", text, time.Duration(d), want)
		}

		out, err := d.MarshalText()
		if err != nil {
			t.Errorf("%q: %v", text, err)
			continue
		}
		// Re-parsed rather than compared as text: "1h30m" and "1h30m0s"
		// are the same duration, and pinning the spelling would make
		// this a test of Go's formatter.
		var again Duration
		if err := again.UnmarshalText(out); err != nil {
			t.Errorf("%q round trip: %v", text, err)
		}
		if again != d {
			t.Errorf("%q round-tripped to %v", text, time.Duration(again))
		}
	}
}

func TestDurationRefusesWhatIsNotOne(t *testing.T) {
	for _, bad := range []string{"soon", "10", "-", "5 minutes", "10x"} {
		var d Duration
		if err := d.UnmarshalText([]byte(bad)); err == nil {
			t.Errorf("%q was accepted as a duration (%v)", bad, time.Duration(d))
		}
	}

	// Empty is zero rather than an error: an omitted timeout is the
	// defaulting path, not a malformed manifest.
	var d Duration
	if err := d.UnmarshalText(nil); err != nil {
		t.Errorf("an omitted duration was refused: %v", err)
	}
}

func TestByteSizeRoundTripsThroughText(t *testing.T) {
	cases := map[string]int64{
		"2GiB":   2 << 30,
		"512MiB": 512 << 20,
		"1KiB":   1 << 10,
		"100":    100,
		"5GB":    5_000_000_000,
	}

	for text, want := range cases {
		var b ByteSize
		if err := b.UnmarshalText([]byte(text)); err != nil {
			t.Errorf("%q: %v", text, err)
			continue
		}
		if b.Bytes() != want {
			t.Errorf("%q = %d bytes, want %d", text, b.Bytes(), want)
		}

		out, err := b.MarshalText()
		if err != nil {
			t.Errorf("%q: %v", text, err)
			continue
		}
		var again ByteSize
		if err := again.UnmarshalText(out); err != nil {
			t.Errorf("%q round trip: %v", text, err)
		}
		// Binary units round-trip exactly. Decimal ones do not: 5GB
		// renders as 4.7GiB, which parses back to 5046586572 rather
		// than 5000000000. That is recorded below rather than asserted
		// here, because nothing in this program round-trips a size --
		// requirements are read from a manifest and never written back.
		if strings.HasSuffix(text, "iB") || text == "100" {
			if again != b {
				t.Errorf("%q round-tripped to %d bytes", text, again.Bytes())
			}
		}
	}
}

// TestADecimalSizeDoesNotSurviveBeingReSerialised is a known limit.
//
// MarshalText renders in binary units to one decimal place, so a manifest
// saying 5GB comes back as 4.7GiB and parses to a different number. It is
// harmless today because sizes are read from a manifest and never written
// back; this fails if that ever stops being true.
func TestADecimalSizeDoesNotSurviveBeingReSerialised(t *testing.T) {
	var b ByteSize
	if err := b.UnmarshalText([]byte("5GB")); err != nil {
		t.Fatal(err)
	}
	out, err := b.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	var again ByteSize
	if err := again.UnmarshalText(out); err != nil {
		t.Fatal(err)
	}
	if again == b {
		t.Log("decimal sizes now round-trip exactly, which is an improvement: " +
			"delete this test and tighten the one above")
		t.Fail()
	}
}

func TestByteSizeRefusesWhatIsNotOne(t *testing.T) {
	for _, bad := range []string{"lots", "2 gigabytes", "GiB", "-5GiB", "2XiB"} {
		var b ByteSize
		if err := b.UnmarshalText([]byte(bad)); err == nil {
			t.Errorf("%q was accepted as a size (%d bytes)", bad, b.Bytes())
		}
	}
}

func TestVersionRoundTripsThroughText(t *testing.T) {
	v := MustParseVersion("1.2.3")

	out, err := v.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "1.2.3" {
		t.Errorf("marshalled to %q", out)
	}

	var again Version
	if err := again.UnmarshalText(out); err != nil {
		t.Fatal(err)
	}
	if !again.Equal(v) {
		t.Errorf("round-tripped to %s", again)
	}

	// A zero version marshals to nothing rather than to "0.0.0", which
	// would make an omitted version indistinguishable from a real one.
	var zero Version
	if !zero.IsZero() {
		t.Error("the zero Version does not report itself zero")
	}

	if err := again.UnmarshalText([]byte("wednesday")); err == nil {
		t.Error("a word was accepted as a version")
	}
}

func TestConstraintRoundTripsThroughText(t *testing.T) {
	c, err := ParseConstraint(">=1.2.0 <2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Allows(MustParseVersion("1.5.0")) {
		t.Error("1.5.0 is outside >=1.2.0 <2.0.0")
	}
	if c.Allows(MustParseVersion("2.0.0")) {
		t.Error("2.0.0 satisfied <2.0.0")
	}

	out, err := c.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	var again Constraint
	if err := again.UnmarshalText(out); err != nil {
		t.Fatal(err)
	}
	if again.String() != c.String() {
		t.Errorf("round-tripped %q to %q", c, again)
	}

	if err := again.UnmarshalText([]byte("=~= 1.0")); err == nil {
		t.Error("nonsense was accepted as a constraint")
	}
	// An empty constraint allows everything, which is what an omitted
	// requirement has to mean.
	var empty Constraint
	if !empty.IsZero() {
		t.Error("an omitted constraint does not report itself zero")
	}
}

// TestASecretNeverSerialisesItsValue is the structural half of the redaction
// claim: whatever a caller does with a Secret, marshalling it cannot leak.
func TestASecretNeverSerialisesItsValue(t *testing.T) {
	const value = "a-real-database-password"
	s := NewSecret(value)

	asJSON, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(asJSON), value) {
		t.Errorf("JSON carries the value: %s", asJSON)
	}

	asYAML, err := s.MarshalYAML()
	if err != nil {
		t.Fatal(err)
	}
	if text, ok := asYAML.(string); ok && strings.Contains(text, value) {
		t.Errorf("YAML carries the value: %v", asYAML)
	}

	// Including inside a struct somebody serialises without thinking.
	wrapper := struct {
		Name  string `json:"name"`
		Value Secret `json:"value"`
	}{Name: "db_password", Value: s}

	out, err := json.Marshal(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), value) {
		t.Errorf("a Secret in a struct serialised its value: %s", out)
	}
}

// TestRevealAllIsTheOneDeliberateWayOut, used where the values have to reach a
// file the product reads.
func TestRevealAllIsTheOneDeliberateWayOut(t *testing.T) {
	set := NewSecretSet(map[string]Secret{
		"db_password": NewSecret("one"),
		"session_key": NewSecret("two"),
	})

	all := set.RevealAll()
	if len(all) != 2 || all["db_password"] != "one" || all["session_key"] != "two" {
		t.Errorf("RevealAll = %v", all)
	}

	// The returned map must be a copy: a caller mutating it must not
	// change the set every later render reads.
	all["db_password"] = "tampered"
	if v, _ := set.Get("db_password"); v.Reveal() != "one" {
		t.Error("RevealAll handed out the set's own map, so a caller can rewrite " +
			"a credential the store still believes it holds")
	}
}

func TestInstallationValidation(t *testing.T) {
	valid := Installation{
		SchemaVersion: InstallationSchemaVersion,
		ID:            "op_01JZZ0000000000000000000",
		Product:       "demo",
		CreatedAt:     NewTime(time.Now()),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("a well-formed installation was refused: %v", err)
	}

	cases := map[string]func(*Installation){
		"no id":                         func(i *Installation) { i.ID = "" },
		"no product":                    func(i *Installation) { i.Product = "" },
		"a product name that is a path": func(i *Installation) { i.Product = "../etc" },
		"a schema from the future":      func(i *Installation) { i.SchemaVersion = InstallationSchemaVersion + 1 },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			bad := valid
			mutate(&bad)
			if err := bad.Validate(); err == nil {
				t.Errorf("%s was accepted", name)
			}
		})
	}
}

func TestFirstIncompleteStepIsWhatResumeStartsFrom(t *testing.T) {
	rec := OperationRecord{Steps: []StepRecord{
		{ID: "a", Status: StepSucceeded},
		{ID: "b", Status: StepSucceeded},
		{ID: "c", Status: StepFailed},
		{ID: "d", Status: StepPending},
	}}

	idx, ok := rec.FirstIncompleteStep()
	if !ok {
		t.Fatal("a record with a failed step reports nothing to resume from")
	}
	if got := rec.Steps[idx].ID; got != "c" {
		t.Errorf("resume would start at %q, want the first step that did not finish", got)
	}

	// A record where everything finished has nothing to resume.
	done := OperationRecord{Steps: []StepRecord{
		{ID: "a", Status: StepSucceeded},
		{ID: "b", Status: StepSkipped},
	}}
	if _, ok := done.FirstIncompleteStep(); ok {
		t.Error("a completed operation offered a step to resume from")
	}

	if _, ok := (OperationRecord{}).FirstIncompleteStep(); ok {
		t.Error("an operation with no steps offered one to resume from")
	}
}

func TestPathsAreDerivedFromOneRoot(t *testing.T) {
	p := PathsUnder("/srv/test", "demo")

	cases := map[string]string{
		p.LockFile("deployment"): "/srv/test/var/lib/demo/manager/locks/deployment.lock",
		p.PreviousLink():         "/srv/test/opt/demo/previous",
		p.CurrentLink():          "/srv/test/opt/demo/current",
		p.JournalFile():          "/srv/test/var/lib/demo/manager/operations.jsonl",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
	}
}

func TestReleaseRecordDescribesItself(t *testing.T) {
	rec := ReleaseRecord{Name: "demo", Version: MustParseVersion("1.2.0")}
	if got := rec.String(); !strings.Contains(got, "demo") || !strings.Contains(got, "1.2.0") {
		t.Errorf("String() = %q; status prints this", got)
	}

	if !(ReleaseRecord{}).IsZero() {
		t.Error("an empty release record does not report itself empty")
	}
}
