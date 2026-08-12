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

// fullyPopulatedInstallation is an installation with every operator-settable
// field set, and every reference-bearing one non-empty.
//
// Shared by the round-trip and aliasing tests because both are worthless
// against a zero value: a document assembled from an empty installation
// round-trips perfectly while carrying nothing, and shares no slice because
// there is no slice to share.
func fullyPopulatedInstallation() domain.Installation {
	return domain.Installation{
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
}

// TestTheOperatorsAnswersSurviveTheDocument.
//
// The RFC's claim is that the file recreates the installation, so every field
// an operator can set has to arrive on the other side with the value they set.
// Populated by hand rather than with a zero value: a document assembled from an
// empty installation would pass a round-trip test while carrying nothing.
func TestTheOperatorsAnswersSurviveTheDocument(t *testing.T) {
	inst := fullyPopulatedInstallation()
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
// property of the types rather than a rule somebody follows.
//
// So it is asserted against the type graph. The earlier version of this test
// put a distinctive literal in a constant, never gave it to `Describe`, and
// checked the rendered document did not contain it -- which no document could,
// and which would have kept passing with a `domain.Secret` field added to the
// document, since `Secret` renders as `[redacted]` and the literal still would
// not have appeared. A test that cannot fail is a decision nobody is enforcing.
//
// The machine-level claim -- a real installation holding a real secret, whose
// value does not reach the file -- is `TestDescribeCarriesNoSecretValue` in the
// CLI suite, where there is a secret store to hold one.
func TestTheDocumentCannotCarryASecretValue(t *testing.T) {
	if found := placesASecretCouldGo(reflect.TypeFor[domain.InstallationDocument](),
		"InstallationDocument", map[reflect.Type]bool{}); len(found) > 0 {
		t.Errorf("the document has somewhere to put a secret value, and a document "+
			"that can hold one eventually does: %v", found)
	}

	// And the credential-bearing fields carry the names they were given,
	// which is the other half of decision 2: a reference is only safe if it
	// stays a reference.
	inst := fullyPopulatedInstallation()
	doc := inst.Describe(domain.DescribedRelease{}, []string{"db_password"})

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

// placesASecretCouldGo walks a type graph and names every path reaching a type
// that holds a secret value.
//
// A function returning findings rather than an assertion, for the reason
// accountFields is one: against the real document it finds nothing -- that is
// the point of it -- so a test that only runs it against InstallationDocument
// can never see it work, and a detector nobody has seen work is one that may
// have stopped. Deleting its struct-descent survived a mutation sweep for
// exactly that reason.
func placesASecretCouldGo(typ reflect.Type, path string, seen map[reflect.Type]bool) []string {
	valueBearing := map[reflect.Type]bool{
		reflect.TypeFor[domain.Secret]():    true,
		reflect.TypeFor[domain.SecretSet](): true,
	}
	if valueBearing[typ] {
		return []string{path + " is a " + typ.String()}
	}
	if seen[typ] {
		return nil
	}
	seen[typ] = true

	var found []string
	switch typ.Kind() {
	case reflect.Struct:
		for i := range typ.NumField() {
			f := typ.Field(i)
			found = append(found, placesASecretCouldGo(f.Type, path+"."+f.Name, seen)...)
		}
	case reflect.Slice, reflect.Array, reflect.Pointer:
		found = append(found, placesASecretCouldGo(typ.Elem(), path+"[]", seen)...)
	case reflect.Map:
		found = append(found, placesASecretCouldGo(typ.Elem(), path+"[k]", seen)...)
	}
	return found
}

// TestASecretHiddenAnywhereInTheGraphIsFound drives the detector against a type
// that does hold a secret, since the real document deliberately does not.
//
// Nested inside a slice inside a struct, because the shallow version of this
// check -- look at the document's own fields -- would pass the type that
// actually worries anybody: not `InstallationDocument.Password`, which nobody
// would write, but a value quietly added to a target config three levels down.
func TestASecretHiddenAnywhereInTheGraphIsFound(t *testing.T) {
	type target struct {
		URL      string
		Password domain.Secret
	}
	type config struct {
		Targets []target
	}
	type document struct {
		ID     string
		Backup config
	}

	found := placesASecretCouldGo(reflect.TypeFor[document](), "document",
		map[reflect.Type]bool{})

	if len(found) != 1 {
		t.Fatalf("want the one buried secret, got %v", found)
	}
	if found[0] != "document.Backup.Targets[].Password is a domain.Secret" {
		t.Errorf("the finding does not say where it is: %q", found[0])
	}
}

// TestTheDocumentDoesNotAliasTheInstallation.
//
// It is handed to a renderer and may outlive the installation it came from. A
// document sharing a map or slice with live state is one an unrelated later
// write can change under a caller who has already read it.
//
// Every reference is mutated by walking the installation rather than by naming
// the fields, because naming them is how this test came to check `Parameters`
// and `Domains` while `Policy.SigningKeys`, `Notify.Targets` and
// `Backup.Targets` -- all reached through a struct that was copied by value,
// so all still shared -- went unchecked. A nested slice added tomorrow is
// covered the day it is added.
func TestTheDocumentDoesNotAliasTheInstallation(t *testing.T) {
	inst := fullyPopulatedInstallation()
	doc := inst.Describe(domain.DescribedRelease{}, []string{"db_password"})

	before, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}

	mutateThroughReferences(reflect.ValueOf(&inst).Elem(), false)

	after, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("mutating the installation changed a document already assembled:\nbefore %s\nafter  %s",
			before, after)
	}
}

// mutateThroughReferences changes every string reachable from v *through* a
// slice, a map or a pointer -- exactly the values a struct copy still shares
// with its original, and none of the ones it does not.
//
// The distinction is the whole test. Mutating `inst.Product` proves nothing:
// it is a string field, copied by value, and no document could see the change.
// Mutating `inst.Backup.Targets[0].URL` reaches through a slice header that a
// shallow copy hands over intact.
func mutateThroughReferences(v reflect.Value, throughReference bool) {
	switch v.Kind() {
	case reflect.String:
		if throughReference && v.CanSet() {
			v.SetString("MUTATED-AFTER-THE-DOCUMENT-WAS-ASSEMBLED")
		}
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Field(i).CanSet() {
				mutateThroughReferences(v.Field(i), throughReference)
			}
		}
	case reflect.Slice:
		for i := range v.Len() {
			mutateThroughReferences(v.Index(i), true)
		}
	case reflect.Map:
		// Map values are unaddressable, so each is copied out, mutated
		// and written back -- which changes this map and any map
		// sharing its header, and leaves a copied map alone.
		for _, k := range v.MapKeys() {
			elem := reflect.New(v.Type().Elem()).Elem()
			elem.Set(v.MapIndex(k))
			mutateThroughReferences(elem, true)
			v.SetMapIndex(k, elem)
		}
	case reflect.Pointer:
		if !v.IsNil() {
			mutateThroughReferences(v.Elem(), true)
		}
	}
}
