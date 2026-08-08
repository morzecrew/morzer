// Package gotemplate implements ports.Renderer over stdlib text/template.
//
// The renderer runs in strict mode: an unknown key is an error, never an empty
// string. A configuration file silently missing a value is the failure this
// rule exists to prevent -- the product starts, looks fine, and behaves
// wrongly until someone correlates an outage with a template typo.
package gotemplate

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// Name is the provider name.
const Name = "gotemplate"

type Renderer struct{}

func New() *Renderer { return &Renderer{} }

var _ ports.Renderer = (*Renderer)(nil)

// parse builds the template exactly as rendering will.
//
// Extracted so CheckSyntax and Render cannot construct it differently. A
// syntax check that parsed with a different function set or a different
// missingkey option would pass templates that then fail at install, which is
// the failure `release verify` exists to move earlier.
func parse(name string, raw []byte) (*template.Template, error) {
	tmpl, err := template.New(name).
		Funcs(funcs()).
		// missingkey=error is the whole point: without it, a reference
		// to a field that does not exist renders as "<no value>" and
		// ships to production.
		Option("missingkey=error").
		Parse(string(raw))
	if err != nil {
		return nil, domain.ValidationError(
			fmt.Errorf("%w: %w", domain.ErrTemplateSyntax, err),
			"template %s does not parse", name).
			WithHint("check the delimiters and function names in the template")
	}
	return tmpl, nil
}

// CheckSyntax reports whether a template parses, without rendering it.
//
// Parsing needs no installation, no parameters and no network, which is what
// makes it safe in the path a vendor runs on every commit. It is deliberately
// *only* parsing: a template that parses can still fail to render against a
// real context, and saying otherwise is the over-claim RFC 0013 exists to
// avoid.
func CheckSyntax(name string, raw []byte) error {
	_, err := parse(name, raw)
	return err
}

// Render executes a template against the documented context.
func (r *Renderer) Render(ctx context.Context, ref ports.TemplateRef, data ports.TemplateData) ([]byte, error) {
	raw, err := readTemplate(ref)
	if err != nil {
		return nil, err
	}

	tmpl, err := parse(ref.Name, raw)
	if err != nil {
		return nil, err
	}

	view := newView(data)

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, view); err != nil {
		return nil, domain.ValidationError(err, "cannot render template %s", ref.Name).
			WithHint("%s", renderHint(err))
	}
	return buf.Bytes(), nil
}

// readTemplate reads a template through the release root.
//
// os.Root is the containment, and it is the kernel's rather than this package's:
// every path component is resolved inside the root, so a symlink pointing at
// /etc/shadow fails to open rather than being rendered into a configuration
// file the product then serves. The manifest's own path check refuses "../"
// spellings before this, but a symlink is not a spelling -- it is a file, and
// only an open can see it.
//
// This matters for directory-sourced bundles specifically. An archive is
// extracted, and extraction refuses symlinks outright; a directory handed to
// `morzer update ./bundle` is read where it lies.
func readTemplate(ref ports.TemplateRef) ([]byte, error) {
	if ref.Root == "" {
		return nil, domain.Internal(nil,
			"template %s was requested without a release root", ref.Name)
	}

	root, err := os.OpenRoot(ref.Root)
	if err != nil {
		return nil, domain.ValidationError(err, "cannot read the release at %s", ref.Root)
	}
	defer func() { _ = root.Close() }()

	f, err := root.Open(filepath.ToSlash(filepath.Clean(ref.Name)))
	if err != nil {
		return nil, domain.ValidationError(err, "cannot read template %s", ref.Name).
			WithHint("the manifest names it relative to the release root, " +
				"and it must be a real file inside the bundle")
	}
	defer func() { _ = f.Close() }()

	raw, err := io.ReadAll(f)
	if err != nil {
		return nil, domain.ValidationError(err, "cannot read template %s", ref.Name)
	}
	return raw, nil
}

// renderHint turns Go's template errors into something an author can act on.
func renderHint(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "map has no entry for key"):
		return "the template referenced a key that the render context does not define. " +
			"Available top-level fields: Installation, Release, Profile, Paths, Secrets, Domains, Parameters"
	case strings.Contains(msg, "can't evaluate field"):
		return "the template referenced a field that does not exist on that type; " +
			"check the field name and its capitalisation"
	default:
		return "check the template against the documented render context"
	}
}

// view is what a template actually sees.
//
// It is a distinct type from ports.TemplateData so the template context is a
// deliberate, documented surface rather than whatever happens to be a field on
// an internal struct. Adding a field here is a contract change; adding one to
// an internal type is not.
type view struct {
	Installation installationView
	Release      ports.ReleaseInfo
	Profile      string
	Paths        ports.PathInfo

	// Secrets maps a secret name to the path of its rendered file.
	// Templates get references, never values: a config file in /etc must
	// not contain a credential.
	Secrets map[string]string

	Domains    []string
	Parameters domain.Parameters
}

// installationView exposes the installation without its Providers block,
// which is manager wiring rather than product configuration.
type installationView struct {
	ID      string
	Product string
	Profile string
	Domains []string

	// Domain is the canonical name, the overwhelmingly common case. Having
	// it saves every template writing `index .Installation.Domains 0`.
	Domain string
	URL    string
}

func newView(d ports.TemplateData) view {
	domain0 := ""
	if len(d.Installation.Domains) > 0 {
		domain0 = d.Installation.Domains[0]
	}

	// Nil maps would make a lookup fail with a nil-map error rather than
	// the informative missingkey message.
	secrets := d.Secrets
	if secrets == nil {
		secrets = map[string]string{}
	}
	params := d.Parameters
	if params == nil {
		params = domain.Parameters{}
	}
	return view{
		Installation: installationView{
			ID:      d.Installation.ID,
			Product: d.Installation.Product,
			Profile: d.Installation.Profile,
			Domains: d.Installation.Domains,
			Domain:  domain0,
			URL:     d.Installation.PublicURL(),
		},
		Release:    d.Release,
		Profile:    d.Profile,
		Paths:      d.Paths,
		Secrets:    secrets,
		Domains:    d.Domains,
		Parameters: params,
	}
}

// funcs is the template function set.
//
// It is deliberately small. sprig would add several hundred functions and a
// large dependency surface for a use case that renders configuration files;
// the spec allows it only "if templates genuinely need helpers". These are the
// ones a config template cannot reasonably do without.
func funcs() template.FuncMap {
	return template.FuncMap{
		// secretFile resolves a secret to its rendered path, failing
		// loudly when the secret is not declared. A template that
		// silently pointed at a nonexistent file would produce a
		// product that fails at startup with a file-not-found.
		"secretFile": func(secrets map[string]string, name string) (string, error) {
			path, ok := secrets[name]
			if !ok {
				return "", fmt.Errorf(
					"secret %q is not declared in the release secret schema", name)
			}
			return path, nil
		},

		"default": func(fallback, value any) any {
			if isEmpty(value) {
				return fallback
			}
			return value
		},
		"required": func(message string, value any) (any, error) {
			if isEmpty(value) {
				return nil, fmt.Errorf("%s", message)
			}
			return value, nil
		},

		"join":     strings.Join,
		"quote":    func(s string) string { return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"` },
		"upper":    strings.ToUpper,
		"lower":    strings.ToLower,
		"trim":     strings.TrimSpace,
		"contains": strings.Contains,
		"replace":  strings.ReplaceAll,

		"indent": func(spaces int, s string) string {
			pad := strings.Repeat(" ", spaces)
			lines := strings.Split(s, "\n")
			for i, l := range lines {
				if l != "" {
					lines[i] = pad + l
				}
			}
			return strings.Join(lines, "\n")
		},
	}
}

func isEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case bool:
		return !t
	case int:
		return t == 0
	case []string:
		return len(t) == 0
	case map[string]string:
		return len(t) == 0
	default:
		return false
	}
}
