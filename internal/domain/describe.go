package domain

import (
	"maps"
	"reflect"
	"sort"
)

// KindInstallationDocument is the document kind of a declarative description.
const KindInstallationDocument = "installation-document"

// InstallationDocument is an installation as a file: everything an operator
// chose, in a form that can be read, reviewed, diffed and committed.
//
// It is the fifth versioned contract, and it is the only one that is *written
// for a human to read*. That changes what belongs in it. The installation
// record is state and carries bookkeeping -- a schema version, a creation
// timestamp, the provider names the release declared. None of that is a choice
// anybody made, so none of it is here. What is here is the set of answers an
// operator gave: which release, which parameters, which targets, which policy.
//
// It contains no secret value and cannot: every credential in the installation
// is already a *reference* to a secret by name (`url_secret`, `credentials`),
// so carrying those fields verbatim carries names and never values. That is not
// a precaution taken here, it is a property of the configuration this document
// describes, and RFC 0027 decision 2 depends on it staying true -- which
// TestTheDocumentCannotCarryASecretValue is what enforces.
//
// See RFC 0027. P1 produces it and nothing consumes it: `apply -f` is specified
// there and deliberately not built, gated on somebody asking for it.
type InstallationDocument struct {
	APIVersion APIVersion `yaml:"api_version" json:"api_version"`
	Kind       string     `yaml:"kind" json:"kind"`

	// ID identifies which installation this describes. Immutable, and here
	// so that two documents from two machines can be told apart -- an
	// operator diffing them needs to know they are diffing the right pair.
	// It is not a field a reader may set to something else: RFC 0027 §4.3
	// makes an immutable field a named refusal rather than a silent no-op.
	ID      string `yaml:"id" json:"id"`
	Product string `yaml:"product" json:"product"`

	// Mode is immutable in the same way, and for a much sharper reason:
	// both transitions are dangerous, in different shapes.
	Mode Mode `yaml:"mode,omitempty" json:"mode,omitempty"`

	Release DescribedRelease `yaml:"release" json:"release"`

	Profile string   `yaml:"profile,omitempty" json:"profile,omitempty"`
	Domains []string `yaml:"domains,omitempty" json:"domains,omitempty"`

	Parameters map[string]string `yaml:"parameters,omitempty" json:"parameters,omitempty"`

	// Secrets are names, never values, and the list is what `secret set`
	// would have to be run for. A document that could hold a value would
	// be a document that eventually does.
	Secrets []string `yaml:"secrets,omitempty" json:"secrets,omitempty"`

	Policy Policy       `yaml:"policy" json:"policy"`
	Update UpdateConfig `yaml:"update,omitempty" json:"update,omitempty"`
	Notify NotifyConfig `yaml:"notify,omitempty" json:"notify,omitempty"`
	Backup BackupConfig `yaml:"backup,omitempty" json:"backup,omitempty"`
}

// DescribedRelease identifies the release by what survives a machine, rather
// than by where it happened to be unpacked.
//
// A release root is a path on a host that may not exist tomorrow. The version
// and the content digest are what let somebody fetch the same bundle again and
// know they got the same bytes -- the same reasoning ExportedRelease uses.
type DescribedRelease struct {
	Name    string  `yaml:"name,omitempty" json:"name,omitempty"`
	Version Version `yaml:"version,omitempty" json:"version,omitempty"`
	Digest  string  `yaml:"digest,omitempty" json:"digest,omitempty"`

	// Ref is where this release came from, when the installation recorded
	// one: an `oci://`, `https://` or `file://` reference. Absent for an
	// installation applied from a local directory, which is why it is
	// omitempty rather than required -- a document that claimed a source it
	// does not have would send a reader to a URL that never existed.
	Ref string `yaml:"ref,omitempty" json:"ref,omitempty"`
}

// installationFieldsNotDescribed names every Installation field this document
// deliberately leaves out, and why.
//
// The map is the point rather than documentation of it. A field added to
// Installation and forgotten here fails TestEveryInstallationFieldIsAccounted,
// so the document cannot silently stop describing an installation -- which is
// the failure that would make RFC 0027's central claim false while every test
// still passed.
var installationFieldsNotDescribed = map[string]string{
	"SchemaVersion": "state bookkeeping: the document carries api_version, which is its own contract",
	"CreatedAt":     "history, not a choice -- a recreated installation has its own creation time",
	"Providers":     "declared by the release manifest, not chosen by the operator",
}

// Describe assembles the document from an installation, the release it is
// running and the names of its secrets.
//
// Secret *names* are a parameter rather than read from the installation
// because they live in the secret store, not in the installation record --
// and passing them in keeps this function pure, which is what lets the
// completeness test run without a store.
func (i Installation) Describe(release DescribedRelease, secretNames []string) InstallationDocument {
	names := append([]string(nil), secretNames...)
	sort.Strings(names)

	return InstallationDocument{
		APIVersion: APIVersionV1Alpha1,
		Kind:       KindInstallationDocument,
		ID:         i.ID,
		Product:    i.Product,
		Mode:       i.Mode,
		Release:    release,
		Profile:    i.Profile,
		Domains:    append([]string(nil), i.Domains...),
		Parameters: copyStringMap(i.Parameters),
		Secrets:    names,
		Policy:     i.Policy,
		Update:     i.Update,
		Notify:     i.Notify,
		Backup:     i.Backup,
	}
}

// DescribedInstallationFields sorts every Installation field into one of three
// answers, read off the structs themselves rather than off a list somebody
// maintains: carried by the document, excluded with a stated reason, or
// unaccounted for.
//
// The third is what the test looks at. A field added to Installation and
// forgotten here lands there, and the document silently describing less than an
// installation is the failure that would leave RFC 0027's central claim false
// with every test still green.
func DescribedInstallationFields() (carried []string, excluded map[string]string, unaccounted []string) {
	return accountFields(
		fieldNames(reflect.TypeFor[Installation]()),
		fieldNames(reflect.TypeFor[InstallationDocument]()),
		installationFieldsNotDescribed,
	)
}

func fieldNames(t reflect.Type) []string {
	out := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		out = append(out, t.Field(i).Name)
	}
	return out
}

// accountFields is the sorting itself, over names rather than over types.
//
// Separated from the reflection so it can be tested against a field nobody
// accounted for. With the real types there is no such field -- that is the
// point of the check -- so a test of DescribedInstallationFields alone can
// never see the detector work, and a detector nobody has seen work is one that
// may have stopped. Found by a mutation that deleted the `unaccounted` branch
// and passed every test.
func accountFields(installation, document []string, reasons map[string]string) (
	carried []string, excluded map[string]string, unaccounted []string,
) {
	described := make(map[string]bool, len(document))
	for _, name := range document {
		described[name] = true
	}

	excluded = map[string]string{}
	for _, name := range installation {
		switch {
		case described[name]:
			carried = append(carried, name)
		case reasons[name] != "":
			excluded[name] = reasons[name]
		default:
			unaccounted = append(unaccounted, name)
		}
	}
	sort.Strings(carried)
	sort.Strings(unaccounted)
	return carried, excluded, unaccounted
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}
