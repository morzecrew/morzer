package gotemplate

import (
	"errors"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
)

// TestReadingATemplateFindsWhatItNamesAndNothingDeclares is the reference
// check's whole contract, one row per thing it promises and per thing it
// deliberately leaves alone.
//
// The false-branch rows are the reason it exists beside `--render-check`: a
// synthetic render takes one side of an `if`, and an undeclared name on the
// other side ships.
func TestReadingATemplateFindsWhatItNamesAndNothingDeclares(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"declared names pass",
			`{{ .Paths.Data }} {{ .Parameters.http_port }} {{ secretFile .Secrets "db_password" }} {{ .Installation.Domain }} {{ .Release.Version.String }}`, ""},
		{"an undeclared secret by secretFile",
			`{{ secretFile .Secrets "db_pasword" }}`, `t:1:3: secretFile names secret "db_pasword"`},
		{"an undeclared secret by field",
			`{{ .Secrets.session_key }}`, `.Secrets.session_key names secret "session_key"`},
		{"an undeclared parameter",
			`{{ .Parameters.htpp_port }}`, `parameter "htpp_port", which the manifest does not declare`},
		{"a field the context does not have",
			`{{ .Paths.Dta }}`, `.Paths.Dta: .Paths has no field Dta`},
		{"a top-level field the context does not have",
			`{{ .Domian }}`, `.Domian: the render context has no field Domian`},
		{"a field on a value that has none",
			`{{ .Profile.Name }}`, `.Profile.Name: .Profile is a string, which has no fields`},
		{"the branch a synthetic render skips",
			`{{ if .Domains }}{{ .Paths.Data }}{{ else }}{{ .Parameters.typo }}{{ end }}`, `parameter "typo"`},
		{"an if's body keeps the context",
			`{{ if .Domains }}{{ .Parameters.typo }}{{ end }}`, `parameter "typo"`},
		{"$ inside a range is still the context",
			`{{ range .Domains }}{{ . }} {{ $.Parameters.typo }}{{ end }}`, `parameter "typo"`},
		{"a range's else keeps the context",
			`{{ range .Domains }}{{ . }}{{ else }}{{ .Paths.Dta }}{{ end }}`, `.Paths has no field Dta`},
		{"a secretFile inside a define",
			`{{ define "x" }}{{ secretFile .Secrets "nope" }}{{ end }}{{ template "x" . }}`, `secret "nope"`},
		{"a parenthesised pipeline is read",
			`{{ (default "x" .Parameters.typo) }}`, `parameter "typo"`},
		{"an index into the parameters",
			`{{ index .Parameters "typo" }}`, `parameter "typo"`},
		{"an index into the secrets through $",
			`{{ range .Domains }}{{ index $.Secrets "nope" }}{{ end }}`, `secret "nope"`},
		{"an index with a declared key",
			`{{ index .Parameters "http_port" }} {{ index .Domains 0 }}`, ""},
		{"an index into a struct",
			`{{ index .Paths "Data" }}`, `index cannot look up "Data" in .Paths`},
		{"an index into a parenthesised root chain",
			`{{ index (.Parameters) "typo" }}`, `parameter "typo"`},
		{"an index through a moved dot is left alone",
			`{{ with .Paths }}{{ index .Anything "x" }}{{ end }}`, ""},
		{"a misspelt secret is offered the declared one",
			`{{ secretFile .Secrets "db_pasword" }}`, `does not declare; did you mean db_password?`},
		{"a misspelt parameter is offered the declared one",
			`{{ index .Parameters "htpp_port" }}`, `did you mean http_port?`},
		{"a misspelt field is offered the real one",
			`{{ .Installation.Urrl }}`, `.Installation has no field Urrl; did you mean URL?`},
		{"a moved dot is left alone",
			`{{ with .Paths }}{{ .Data }}{{ .Anything }}{{ end }}{{ range .Domains }}{{ .Whatever }}{{ end }}`, ""},
		{"a define's dot is left alone",
			`{{ define "x" }}{{ .Anything }}{{ end }}{{ template "x" .Paths }}`, ""},
		{"a variable other than $ is left alone",
			`{{ $p := .Paths }}{{ $p.Anything }}`, ""},
	}
	rel, schema := syntheticRelease()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckReferences(rel, schema, "t", []byte(tc.body))
			if tc.want == "" {
				if err != nil {
					t.Fatalf("refused a template naming only what is declared: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("passed a template that names something undeclared; want %q", tc.want)
			}
			if !errors.Is(err, domain.ErrTemplateReference) {
				t.Errorf("not marked ErrTemplateReference: %v", err)
			}
			if msg := domain.AsError(err).Message; !strings.Contains(msg, tc.want) {
				t.Errorf("message %q does not say %q", msg, tc.want)
			}
		})
	}
}
