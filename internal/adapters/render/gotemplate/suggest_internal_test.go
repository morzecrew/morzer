package gotemplate

import (
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
)

// TestARenderFailureSaysWhereAndWhatInTheTemplatesTerms: the message is all an
// operator is shown, so it carries the location, the action as written, the
// owner by its template path rather than its Go type, and the likely fix.
func TestARenderFailureSaysWhereAndWhatInTheTemplatesTerms(t *testing.T) {
	cases := []struct{ body, want string }{
		{`{{ .Installation.Urrl }}`,
			`1:16: .Installation.Urrl: .Installation has no field Urrl; did you mean URL?`},
		{`{{ .Paths.Dta }}`,
			`1:9: .Paths.Dta: .Paths has no field Dta; did you mean Data?`},
		{`{{ with .Paths }}{{ .Dta }}{{ end }}`,
			`1:20: .Dta: .Paths has no field Dta; did you mean Data?`},
		{`{{ .Domian }}`,
			`.Domian: the render context has no field Domian; did you mean Domains?`},
		{`{{ .Parameters.htpp_port }}`,
			`.Parameters.htpp_port names parameter "htpp_port", which the manifest does not declare; did you mean http_port?`},
		{`{{ secretFile .Secrets "db_pasword" }}`,
			`secret "db_pasword" is not declared in the release secret schema; did you mean db_password?`},
		{`{{ .Paths.Zzzzzz }}`,
			`.Paths has no field Zzzzzz: `},
		{`{{ with .Secrets }}{{ .typo }}{{ end }}`,
			`.typo: map has no entry for key "typo"`},
		{`{{ upper .Domains }}`,
			`1:9: .Domains: wrong type for value; expected string; got []string`},
	}
	rel, schema := syntheticRelease()
	for _, tc := range cases {
		err := CheckRender(rel, schema, "t", []byte(tc.body))
		if err == nil {
			t.Fatalf("%s rendered", tc.body)
		}
		msg := domain.AsError(err).Message + ": "
		if !strings.Contains(msg, tc.want) {
			t.Errorf("%s:\n got %q\nwant %q", tc.body, msg, tc.want)
		}
		for _, internal := range []string{"gotemplate.", "ports.", "executing"} {
			if strings.Contains(msg, internal) {
				t.Errorf("%s: the message names %q: %s", tc.body, internal, msg)
			}
		}
	}
}

func TestASuggestionIsOfferedOnlyWhenNear(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"Urrl", "URL"},
		{"htpp_port", "http_port"},
		{"url", "URL"},
		{"Zzzzzz", ""},
		{"x", ""},
	} {
		got := didYouMean(tc.name, []string{"URL", "http_port", "Data"})
		if want := map[bool]string{true: "", false: "; did you mean " + tc.want + "?"}[tc.want == ""]; got != want {
			t.Errorf("didYouMean(%q) = %q, want %q", tc.name, got, want)
		}
	}
}
