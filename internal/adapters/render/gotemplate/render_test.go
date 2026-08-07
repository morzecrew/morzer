package gotemplate_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/adapters/render/gotemplate"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// A rendered configuration file is what the product actually reads, so a
// template that half-works produces a deployment that half-starts. These pin
// the two things that matter: that a mistake is refused loudly rather than
// rendered as "<no value>", and that a secret appears as a path.

func render(t *testing.T, body string, data ports.TemplateData) ([]byte, error) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "application.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return gotemplate.New().Render(context.Background(),
		ports.TemplateRef{Root: root, Name: "application.yaml"}, data)
}

func testData() ports.TemplateData {
	return ports.TemplateData{
		Installation: domain.Installation{
			ID: "inst_01", Product: "demo", Profile: "embedded",
			Domains: []string{"demo.example", "www.demo.example"},
		},
		Profile:    "embedded",
		Domains:    []string{"demo.example", "www.demo.example"},
		Parameters: domain.Parameters{"http_port": "18080", "log_level": "info"},
		Secrets:    map[string]string{"db_password": "/run/demo/secrets/db_password"},
		Paths:      ports.PathInfo{Data: "/var/lib/demo/data", Etc: "/etc/demo"},
		Release: ports.ReleaseInfo{
			Name: "demo", Version: domain.MustParseVersion("1.2.0"), Digest: "sha256:abc",
		},
	}
}

func TestRenderResolvesTheWholeContext(t *testing.T) {
	out, err := render(t, strings.Join([]string{
		"id: {{ .Installation.ID }}",
		"domain: {{ .Installation.Domain }}",
		"profile: {{ .Profile }}",
		"port: {{ .Parameters.http_port }}",
		"data: {{ .Paths.Data }}",
		"version: {{ .Release.Version }}",
		"secret: {{ secretFile .Secrets \"db_password\" }}",
	}, "\n"), testData())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	for _, want := range []string{
		"id: inst_01", "domain: demo.example", "profile: embedded",
		"port: 18080", "data: /var/lib/demo/data", "version: 1.2.0",
		"secret: /run/demo/secrets/db_password",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the rendering is missing %q:\n%s", want, out)
		}
	}
}

// TestAMissingFieldIsRefusedRatherThanRendered is why missingkey=error is set.
// Without it a typo renders as "<no value>" and ships.
func TestAMissingFieldIsRefusedRatherThanRendered(t *testing.T) {
	out, err := render(t, "port: {{ .Parameters.htpp_port }}", testData())
	if err == nil {
		t.Fatalf("a reference to a field that does not exist rendered:\n%s", out)
	}
	if strings.Contains(string(out), "<no value>") {
		t.Error("the template rendered <no value> instead of failing")
	}
	// The hint has to be actionable: an author reading it needs to know
	// what is available.
	if hint := domain.AsError(err).Hint; hint == "" {
		t.Error("a render failure carries no hint")
	}
}

func TestAnUndeclaredSecretIsRefusedByName(t *testing.T) {
	_, err := render(t, `f: {{ secretFile .Secrets "not_declared" }}`, testData())
	if err == nil {
		t.Fatal("a template pointing at an undeclared secret rendered")
	}
	// Naming it is the point: a template that silently pointed at a
	// nonexistent file would produce a product that fails at startup with a
	// file-not-found and no explanation.
	if !strings.Contains(err.Error(), "not_declared") {
		t.Errorf("the error does not name the secret: %v", err)
	}
}

func TestATemplateThatDoesNotParseIsRefused(t *testing.T) {
	_, err := render(t, "broken: {{ .Installation.ID ", testData())
	if err == nil {
		t.Fatal("an unterminated action rendered")
	}
	if !strings.Contains(err.Error(), "does not parse") {
		t.Errorf("the error does not say the template is malformed: %v", err)
	}
}

func TestRenderReportsAMissingTemplateFile(t *testing.T) {
	_, err := gotemplate.New().Render(context.Background(),
		ports.TemplateRef{Root: t.TempDir(), Name: "gone.yaml"},
		testData())
	if err == nil {
		t.Fatal("rendering a template that does not exist succeeded")
	}
}

// TestATemplateSymlinkOutOfTheBundleIsRefused is why the read goes through
// os.Root and not os.ReadFile.
//
// A directory-sourced bundle -- `morzer update ./bundle` -- is never extracted,
// so the extractor's symlink refusal never sees it, and the manifest's path
// check sees only the string "config/app.yaml", which escapes nothing. The
// escape is the file itself. Rendering it would copy a host file the manager can
// read into a configuration file the product then serves.
func TestATemplateSymlinkOutOfTheBundleIsRefused(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "shadow")
	if err := os.WriteFile(outside, []byte("root:$6$secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "application.yaml")); err != nil {
		t.Skipf("no symlink support here: %v", err)
	}

	out, err := gotemplate.New().Render(context.Background(),
		ports.TemplateRef{Root: root, Name: "application.yaml"}, testData())
	if err == nil {
		t.Fatalf("a template symlinked outside the release rendered:\n%s", out)
	}
	if strings.Contains(string(out), "secret") {
		t.Error("the host file's contents were rendered")
	}
}

// A symlink that stays inside the bundle is a legitimate authoring choice --
// two profiles sharing one template -- and os.Root allows it.
func TestATemplateSymlinkInsideTheBundleStillRenders(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "shared.yaml"), []byte("id: {{ .Installation.ID }}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("shared.yaml", filepath.Join(root, "application.yaml")); err != nil {
		t.Skipf("no symlink support here: %v", err)
	}

	out, err := gotemplate.New().Render(context.Background(),
		ports.TemplateRef{Root: root, Name: "application.yaml"}, testData())
	if err != nil {
		t.Fatalf("a symlink inside the bundle was refused: %v", err)
	}
	if !strings.Contains(string(out), "inst_01") {
		t.Errorf("the shared template did not render: %s", out)
	}
}

func TestTheHelperSet(t *testing.T) {
	cases := map[string]struct{ body, want string }{
		"default fills an empty value":    {`{{ "" | default "fallback" }}`, "fallback"},
		"default leaves a set value":      {`{{ "set" | default "fallback" }}`, "set"},
		"default treats zero as empty":    {`{{ 0 | default 8080 }}`, "8080"},
		"default treats false as empty":   {`{{ false | default "on" }}`, "on"},
		"join":                            {`{{ join .Domains "," }}`, "demo.example,www.demo.example"},
		"quote escapes embedded quotes":   {`{{ quote "a\"b" }}`, `"a\"b"`},
		"upper":                           {`{{ upper "abc" }}`, "ABC"},
		"lower":                           {`{{ lower "ABC" }}`, "abc"},
		"trim":                            {`{{ trim "  x  " }}`, "x"},
		"contains":                        {`{{ contains "haystack" "stack" }}`, "true"},
		"replace":                         {`{{ replace "a-b" "-" "_" }}`, "a_b"},
		"indent leaves blank lines alone": {"{{ indent 2 \"a\\n\\nb\" }}", "  a\n\n  b"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := render(t, tc.body, testData())
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Errorf("rendered %q, want it to contain %q", out, tc.want)
			}
		})
	}
}

// TestRequiredStopsARenderRatherThanShippingABlank gives a bundle author a way
// to insist on a value the manager cannot know is needed.
func TestRequiredStopsARenderRatherThanShippingABlank(t *testing.T) {
	// A parameter that is declared and empty. A name that is not declared at
	// all never reaches `required`: missingkey=error refuses it first, which
	// is the stronger guard.
	data := testData()
	data.Parameters["smtp_host"] = ""

	_, err := render(t,
		`{{ required "an smtp host must be configured" .Parameters.smtp_host }}`, data)
	if err == nil {
		t.Fatal("`required` let an unset value through")
	}
	if !strings.Contains(err.Error(), "smtp host") {
		t.Errorf("the author's own message did not survive: %v", err)
	}

	// And it passes a value that is set.
	out, err := render(t, `{{ required "needed" .Parameters.http_port }}`, data)
	if err != nil {
		t.Fatalf("`required` rejected a value that is set: %v", err)
	}
	if !strings.Contains(string(out), "18080") {
		t.Errorf("rendered %q", out)
	}
}

// TestAnAbsentDomainDoesNotRenderAsNothing covers the view's own defaulting:
// an installation with no domain has no canonical one, and a template asking
// for it must be able to fall back.
func TestAnAbsentDomainDoesNotRenderAsNothing(t *testing.T) {
	data := testData()
	data.Installation.Domains = nil
	data.Domains = nil

	out, err := render(t, `{{ .Installation.Domain | default "localhost" }}`, data)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(out), "localhost") {
		t.Errorf("rendered %q, want the fallback", out)
	}
}
