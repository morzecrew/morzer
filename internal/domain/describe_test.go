package domain_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
)

// RFC 0027 §12.1 asks the question this file answers: *is a live installation
// fully expressible?* The RFC says the answer decides whether the artifact is
// worth shipping at all — "a file that recreates *most* of the installation" is
// a much worse thing than one that recreates it — and that it must be answered
// before the exporter is called done rather than after.

// TestEveryInstallationFieldIsAccounted.
//
// Not "the document has the fields somebody listed", which is a test that
// passes forever after the list is written. Every field of Installation must be
// either carried by the document or excluded with a stated reason; a field that
// is neither is a field the document silently stopped describing, and it lands
// here the day it is added rather than the day an operator notices their file
// rebuilt a different machine.
func TestEveryInstallationFieldIsAccounted(t *testing.T) {
	carried, excluded, unaccounted := domain.DescribedInstallationFields()

	if len(unaccounted) > 0 {
		t.Errorf("these Installation fields are neither described nor excluded: %v\n"+
			"add them to InstallationDocument, or to installationFieldsNotDescribed "+
			"with the reason they are not a choice an operator made",
			unaccounted)
	}
	if len(carried) == 0 {
		t.Fatal("the document carries no installation field at all")
	}

	// The exclusions carry reasons rather than being a bare list, because a
	// list is what an exclusion becomes when nobody has to justify it.
	for field, why := range excluded {
		if len(strings.TrimSpace(why)) < 20 {
			t.Errorf("%s is excluded with no real reason: %q", field, why)
		}
	}
}

// TestTheOperatorsAnswersSurviveTheDocument.
//
// The RFC's claim is that the file recreates the installation, so every field
// an operator can set has to arrive on the other side with the value they set.
// Populated by hand rather than with a zero value: a document assembled from an
// empty installation would pass a round-trip test while carrying nothing.
func TestTheOperatorsAnswersSurviveTheDocument(t *testing.T) {
	inst := domain.Installation{
		SchemaVersion: domain.InstallationSchemaVersion,
		ID:            "inst-7",
		Product:       "acme",
		Mode:          domain.ModeDev,
		Profile:       "edge",
		Domains:       []string{"acme.example", "www.acme.example"},
		Parameters:    map[string]string{"http_port": "8443", "site_name": "Acme"},
		Policy: domain.Policy{
			RequireSignature: true,
			SigningKeys:      []string{"RWQf6L"},
			RetainReleases:   4,
		},
		Update: domain.UpdateConfig{Check: true, Channel: "stable"},
		Notify: domain.NotifyConfig{Targets: []domain.NotifyTargetConfig{
			{Name: "ops", URLSecret: "webhook_token", MinLevel: "error"},
		}},
		Backup: domain.BackupConfig{Targets: []domain.BackupTargetConfig{
			{URL: "s3://bucket/prefix", Credentials: "s3_creds"},
		}},
	}
	release := domain.DescribedRelease{
		Name:   "acme",
		Digest: "sha256:abc",
		Ref:    "oci://registry.example.com/acme/release",
	}

	doc := inst.Describe(release, []string{"db_password", "api_key"})

	for _, tc := range []struct {
		what      string
		got, want any
	}{
		{"id", doc.ID, inst.ID},
		{"product", doc.Product, inst.Product},
		{"mode", doc.Mode, inst.Mode},
		{"profile", doc.Profile, inst.Profile},
		{"domains", doc.Domains, inst.Domains},
		{"parameters", doc.Parameters, inst.Parameters},
		{"policy", doc.Policy, inst.Policy},
		{"update", doc.Update, inst.Update},
		{"notify", doc.Notify, inst.Notify},
		{"backup", doc.Backup, inst.Backup},
		{"release", doc.Release, release},
	} {
		if !reflect.DeepEqual(tc.got, tc.want) {
			t.Errorf("%s did not survive: got %#v, want %#v", tc.what, tc.got, tc.want)
		}
	}

	// Sorted, because a document whose key order depends on map iteration
	// produces a different file every run and cannot be diffed.
	if !reflect.DeepEqual(doc.Secrets, []string{"api_key", "db_password"}) {
		t.Errorf("secret names are not sorted: %v", doc.Secrets)
	}
}

// TestTheDocumentIsStableAcrossRuns.
//
// Its whole value is being diffable and committable, and a file that changes
// when nothing changed is neither. Map iteration order is the usual culprit.
func TestTheDocumentIsStableAcrossRuns(t *testing.T) {
	inst := domain.Installation{
		ID:      "inst-7",
		Product: "acme",
		Parameters: map[string]string{
			"a": "1", "b": "2", "c": "3", "d": "4", "e": "5", "f": "6",
		},
	}
	names := []string{"z_secret", "a_secret", "m_secret"}

	first, err := json.Marshal(inst.Describe(domain.DescribedRelease{}, names))
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		again, err := json.Marshal(inst.Describe(domain.DescribedRelease{}, names))
		if err != nil {
			t.Fatal(err)
		}
		if string(again) != string(first) {
			t.Fatalf("two describes of one installation differ:\n%s\n%s", first, again)
		}
	}
}

// TestTheDocumentCannotCarryASecretValue.
//
// RFC 0027 decision 2: the schema has no place to put a value, and that is a
// property of the types rather than a rule somebody follows. The credential
// fields the document carries are references by name -- if one of them ever
// becomes a value, or a value-shaped field is added, this fails.
//
// Asserted by putting a distinctive value everywhere a value could hide and
// requiring it not to appear in the rendered document.
func TestTheDocumentCannotCarryASecretValue(t *testing.T) {
	const secret = "hunter2-THE-ACTUAL-SECRET"

	inst := domain.Installation{
		ID:         "inst-7",
		Product:    "acme",
		Parameters: map[string]string{"http_port": "8443"},
		Notify: domain.NotifyConfig{Targets: []domain.NotifyTargetConfig{
			{Name: "ops", URLSecret: "webhook_token"},
		}},
		Backup: domain.BackupConfig{Targets: []domain.BackupTargetConfig{
			{URL: "s3://bucket/prefix", Credentials: "s3_creds"},
		}},
	}

	doc := inst.Describe(domain.DescribedRelease{}, []string{"db_password"})
	rendered, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rendered), secret) {
		t.Fatalf("a secret value reached the document:\n%s", rendered)
	}

	// And the shape itself: every credential-bearing field is a name.
	if doc.Notify.Targets[0].URLSecret != "webhook_token" {
		t.Error("the notify credential is not carried as a reference")
	}
	if doc.Backup.Targets[0].Credentials != "s3_creds" {
		t.Error("the backup credential is not carried as a reference")
	}
	if len(doc.Secrets) != 1 || doc.Secrets[0] != "db_password" {
		t.Errorf("secrets are not names: %v", doc.Secrets)
	}
}

// TestTheDocumentDoesNotAliasTheInstallation.
//
// It is handed to a renderer and may outlive the installation it came from. A
// document sharing a map or slice with live state is one an unrelated later
// write can change under a caller who has already read it.
func TestTheDocumentDoesNotAliasTheInstallation(t *testing.T) {
	inst := domain.Installation{
		Parameters: map[string]string{"http_port": "8443"},
		Domains:    []string{"acme.example"},
	}
	doc := inst.Describe(domain.DescribedRelease{}, nil)

	inst.Parameters["http_port"] = "9999"
	inst.Domains[0] = "somewhere.else"

	if doc.Parameters["http_port"] != "8443" {
		t.Error("the document's parameters alias the installation's")
	}
	if doc.Domains[0] != "acme.example" {
		t.Error("the document's domains alias the installation's")
	}
}
