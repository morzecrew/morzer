package gotemplate

import (
	"reflect"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
)

func syntheticRelease() (domain.Release, domain.SecretSchema) {
	rel := domain.Release{
		Root:   "/opt/demo/releases/1.2.0",
		Digest: "sha256:" + strings.Repeat("a", 64),
	}
	rel.Manifest.Metadata.Name = "demo"
	rel.Manifest.Metadata.Version = domain.MustParseVersion("1.2.0")
	rel.Manifest.Metadata.Vendor = "example"
	rel.Manifest.Runtime.Profiles = map[string][]string{
		"embedded":    {"compose/compose.embedded.yaml"},
		"external-db": {"compose/compose.external-db.yaml"},
	}
	rel.Manifest.Parameters = map[string]domain.ParameterSpec{
		"http_port": {Type: domain.ParamPort, Default: "8080"},
		"log_level": {Type: domain.ParamEnum, Values: []string{"info", "debug"}},
	}

	schema := domain.SecretSchema{
		Secrets: []domain.SecretDeclaration{{Name: "db_password"}},
	}
	return rel, schema
}

// TestTheSyntheticContextFillsEveryFieldTheTemplateSees is the guard RFC 0013
// §9 asks for, walked rather than listed.
//
// The maintenance risk is specific: `--render-check` is only as wide as the
// context it renders against, so a field added to the template surface without
// a synthetic value silently narrows what the check exercises -- every template
// touching that field renders an empty string here and fails on a customer's
// machine, which is the failure the flag exists to move earlier. A hand-written
// list of expected fields would go stale in exactly the same way; reflection
// over the view cannot.
func TestTheSyntheticContextFillsEveryFieldTheTemplateSees(t *testing.T) {
	rel, schema := syntheticRelease()

	var missing []string
	walkView(reflect.ValueOf(newView(SyntheticData(rel, schema))), "", &missing)

	for _, path := range missing {
		t.Errorf("the synthetic context leaves %s empty, so `--render-check` "+
			"does not exercise templates that use it", path)
	}
}

// walkView reports every exported field that holds its zero value.
//
// Exported only, and it stops at anything that is not a struct: the leaves are
// what a template writes, and descending into an unexported field would report
// on the internals of types like domain.Version, which a template can only
// print whole.
func walkView(v reflect.Value, path string, missing *[]string) {
	if v.IsZero() {
		*missing = append(*missing, path)
		return
	}
	if v.Kind() != reflect.Struct {
		return
	}
	t := v.Type()
	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		walkView(v.Field(i), path+"."+field.Name, missing)
	}
}

// TestTheSyntheticContextDoesNotInventTheBundlesOwnDeclarations.
//
// Everything about the machine is invented -- that is what makes the check a
// smoke test rather than a promise. What must *not* be invented is what the
// bundle itself declares: if the secret map were filled from the template's
// requests rather than from the schema, `secretFile` would resolve any name at
// all and the check would pass every bundle it was given.
func TestTheSyntheticContextDoesNotInventTheBundlesOwnDeclarations(t *testing.T) {
	rel, schema := syntheticRelease()
	data := SyntheticData(rel, schema)

	if _, ok := data.Secrets["db_password"]; !ok {
		t.Error("the declared secret is missing from the synthetic context")
	}
	if _, ok := data.Secrets["db_passwrod"]; ok {
		t.Error("the synthetic context resolves an undeclared secret, so a " +
			"typo in a template would render clean")
	}
	if _, ok := data.Parameters["http_port"]; !ok {
		t.Error("the declared parameter is missing from the synthetic context")
	}
	if _, ok := data.Parameters["htpp_port"]; ok {
		t.Error("the synthetic context resolves an undeclared parameter")
	}
}

// TestASyntheticParameterSatisfiesItsDeclaration.
//
// A declared-but-undefaulted parameter is the operator's to supply, and
// resolving it to the empty string here -- which is what an installation does --
// would fail `{{ required "choose a port" .Parameters.http_port }}` on a bundle
// that is entirely correct. A smoke test that cries wolf about the operator's
// job is one a vendor turns off.
func TestASyntheticParameterSatisfiesItsDeclaration(t *testing.T) {
	specs := map[string]domain.ParameterSpec{
		"port":     {Type: domain.ParamPort, Required: true},
		"count":    {Type: domain.ParamInt},
		"enabled":  {Type: domain.ParamBool},
		"name":     {Type: domain.ParamString},
		"level":    {Type: domain.ParamEnum, Values: []string{"warn", "info"}},
		"timeout":  {Type: domain.ParamDuration},
		"capacity": {Type: domain.ParamBytes},
		"defaults": {Type: domain.ParamPort, Default: "08080"},
	}

	params := syntheticParameters(specs)
	for name, spec := range specs {
		value, ok := params[name]
		if !ok {
			t.Errorf("parameter %q is absent from the synthetic context", name)
			continue
		}
		if value == "" {
			t.Errorf("parameter %q resolves to the empty string, which "+
				"`required` refuses", name)
			continue
		}
		if _, err := spec.Parse(value); err != nil {
			t.Errorf("the synthetic value for %q does not satisfy its own "+
				"declaration: %v", name, err)
		}
	}

	// A default reaches a template normalised, exactly as it does at
	// install time -- otherwise the check renders a value no installation
	// ever produces.
	if got := params["defaults"]; got != "8080" {
		t.Errorf("a declared default is not normalised: %q", got)
	}
}

// TestARenderFailureNamesItsTemplateOnce.
//
// `--render-check` reports every template in one list, each line already
// labelled with the manifest field it came from. text/template prefixes its own
// error with `template: <name>:`, so a detail that dropped only the literal
// "template: " left the name in and produced
// `configuration[0].template: x.tmpl does not render: x.tmpl:1:2: ...`.
//
// The detail must therefore open at the position rather than at the name.
// text/template also writes the name inside its own `executing "x" at <...>`
// clause, which stays: that is the library's sentence about which template was
// running, and rewriting it would be editing an error to look tidier than the
// thing it describes.
func TestARenderFailureNamesItsTemplateOnce(t *testing.T) {
	const name = "application.yaml.tmpl"

	rel := domain.Release{}
	rel.Manifest.Metadata.Name = "demo"

	err := CheckRender(rel, domain.SecretSchema{}, name,
		[]byte("port: {{ .Parameters.nothing.at.all }}\n"))
	if err == nil {
		t.Fatal("a template referring to nothing rendered without complaint")
	}

	msg := domain.AsError(err).Message
	if !strings.HasPrefix(msg, name+" does not render: 1:") {
		t.Errorf("the detail does not open at the failing position: %q", msg)
	}
}
