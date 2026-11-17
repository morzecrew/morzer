package domain

import (
	"fmt"
	"slices"
	"strings"
	"text/template"
	tparse "text/template/parse"
)

// Manifest fields may interpolate parameters. Exactly two do:
// `requirements.ports` and `health.checks[].url`. They are the two that
// contradict a changed port -- publish on 9000 while preflight checks 18080 and
// the health probe asks 18080 -- and an `apply` that fails at "wait for health"
// on a working deployment is the failure this closes.
//
// A third field is a new RFC. Once images, commands and hook arguments
// interpolate, a manifest is a program, and a manifest that is a program cannot
// be validated before it runs.
//
// The context is `.Parameters` and `.Profiles`, with `has`, and nothing else
// (RFC 0059 decision 5, superseding RFC 0007 decision 6 in part). Not paths,
// not secrets, not the installation: a manifest is read before an installation
// is necessarily resolvable, and a health URL able to interpolate a secret path
// is a health URL able to put one in a log line. A profile name is none of
// those, and is already in `status --json`.

// templateContext is everything a manifest field can see. Profiles is the
// installation's effective selection in layering order, never nil, as the
// configuration templates see it (RFC 0059 decision 6).
type templateContext struct {
	Parameters map[string]string
	Profiles   []string
}

// templateFuncs is the configuration templates' `has`, in the same argument
// order, `has "xdns" .Profiles`. The renderer is an adapter this package cannot
// import (logs/ports-by-address.md).
var templateFuncs = template.FuncMap{
	"has": func(needle string, haystack []string) bool { return slices.Contains(haystack, needle) },
}

// parseField parses a manifest field's template.
func parseField(field, text string) (*template.Template, error) {
	// missingkey=error rather than the default: `{{ .Parameters.htpp_port }}`
	// would otherwise render as "<no value>" and produce a URL nothing
	// serves, discovered two minutes later as a health-check timeout.
	tmpl, err := template.New(field).Funcs(templateFuncs).Option("missingkey=error").Parse(text)
	if err != nil {
		// Double-wrapped: the sentinel is what a caller matches on, the
		// cause is what an author reads.
		return nil, ValidationError(fmt.Errorf("%w: %w", ErrTemplateSyntax, err),
			"%s is not a valid template", field).
			WithHint("only {{ .Parameters.<name> }}, {{ .Profiles }} and has are available here")
	}
	return tmpl, nil
}

// Resolve renders a manifest field against the parameters and the profiles an
// installation selects.
//
// A field with no action is returned untouched, so the overwhelmingly common
// literal costs nothing and cannot be broken by a templating bug.
func (p Parameters) Resolve(field, text string, profiles []string) (string, error) {
	if !strings.Contains(text, "{{") {
		return text, nil
	}
	tmpl, err := parseField(field, text)
	if err != nil {
		return "", err
	}
	if profiles == nil {
		profiles = []string{}
	}

	var b strings.Builder
	if err := tmpl.Execute(&b, templateContext{Parameters: p, Profiles: profiles}); err != nil {
		return "", ValidationError(err, "%s could not be resolved", field).
			WithHint("available parameters: %s", p.namesOrNone())
	}

	out := b.String()
	if strings.Contains(out, "<no value>") {
		return "", ValidationError(nil, "%s refers to a parameter the release does not declare", field).
			WithHint("available parameters: %s", p.namesOrNone())
	}
	return out, nil
}

func (p Parameters) namesOrNone() string {
	names := sortedStringKeys(p)
	if len(names) == 0 {
		return "none; the release declares no parameters"
	}
	return strings.Join(names, ", ")
}

// templateUse is what a manifest field's template reads, and the text it writes
// outside its actions.
type templateUse struct {
	parameters []string
	// indexed are the parameters read by `index .Parameters "name"`,
	// which renders an undeclared name as empty rather than failing.
	indexed  []string
	profiles bool // `.Profiles` or `has`
	text     string
}

// readTemplate walks a field's parse trees, every template it defines included,
// for the floor (RFC 0059 §5.5), the parameters a refusal names and the ones
// validation checks are declared. A field that does not parse reads as
// nothing; validation reports it. It reads conservatively: any path through
// `Profiles`, `$` or not, counts as reading the profiles.
func readTemplate(field, text string) templateUse {
	var u templateUse
	tmpl, err := parseField(field, text)
	if err != nil {
		return u
	}
	read := func(idents []string) {
		if slices.Contains(idents, "Profiles") {
			u.profiles = true
		}
		if len(idents) > 1 && idents[0] == "Parameters" && !slices.Contains(u.parameters, idents[1]) {
			u.parameters = append(u.parameters, idents[1])
		}
	}
	var walk func(tparse.Node)
	walk = func(node tparse.Node) {
		switch n := node.(type) {
		case *tparse.ListNode:
			if n != nil {
				for _, child := range n.Nodes {
					walk(child)
				}
			}
		case *tparse.TextNode:
			u.text += string(n.Text)
		case *tparse.ActionNode:
			walk(n.Pipe)
		case *tparse.IfNode:
			walk(n.Pipe)
			walk(n.List)
			walk(n.ElseList)
		case *tparse.RangeNode:
			walk(n.Pipe)
			walk(n.List)
			walk(n.ElseList)
		case *tparse.WithNode:
			walk(n.Pipe)
			walk(n.List)
			walk(n.ElseList)
		case *tparse.PipeNode:
			if n != nil {
				for _, cmd := range n.Cmds {
					walk(cmd)
				}
			}
		case *tparse.CommandNode:
			if name, ok := indexedParameter(n); ok {
				read([]string{"Parameters", name})
				u.indexed = append(u.indexed, name)
			}
			for _, arg := range n.Args {
				walk(arg)
			}
		case *tparse.ChainNode:
			walk(n.Node)
			read(n.Field)
		case *tparse.FieldNode:
			read(n.Ident)
		case *tparse.VariableNode:
			read(n.Ident[1:])
		case *tparse.TemplateNode:
			walk(n.Pipe)
		case *tparse.IdentifierNode:
			if n.Ident == "has" {
				u.profiles = true
			}
		}
	}
	for _, t := range tmpl.Templates() {
		walk(t.Root)
	}
	slices.Sort(u.parameters)
	return u
}

// indexedParameter is the name in `index .Parameters "name"`, or `$.Parameters`.
func indexedParameter(cmd *tparse.CommandNode) (string, bool) {
	if len(cmd.Args) != 3 {
		return "", false
	}
	fn, ok := cmd.Args[0].(*tparse.IdentifierNode)
	key, isKey := cmd.Args[2].(*tparse.StringNode)
	if !ok || fn.Ident != "index" || !isKey {
		return "", false
	}
	var path []string
	switch m := cmd.Args[1].(type) {
	case *tparse.FieldNode:
		path = m.Ident
	case *tparse.VariableNode:
		path = m.Ident[1:]
	}
	return key.Text, slices.Equal(path, []string{"Parameters"})
}

// ResolvePorts renders `requirements.ports` and reads each entry, against the
// parameters and the profiles an installation selects. A templated entry that
// renders empty requires nothing (RFC 0059 decision 7).
//
// The field is a list of strings so one mechanism covers both a literal
// `18080` and a `{{ .Parameters.http_port }}`; a manifest written before
// parameters existed keeps working unchanged.
func (m *Manifest) ResolvePorts(params Parameters, profiles []string) ([]PortRequirement, error) {
	out := make([]PortRequirement, 0, len(m.Requirements.Ports))

	for i, raw := range m.Requirements.Ports {
		field := fmt.Sprintf("requirements.ports[%d]", i)

		text, err := params.Resolve(field, string(raw), profiles)
		if err != nil {
			return nil, err
		}
		templated := strings.Contains(string(raw), "{{")
		if templated && strings.TrimSpace(text) == "" {
			continue
		}
		req, err := ParsePortEntry(text)
		if err != nil {
			return nil, ValidationError(nil, "%s resolved to %q: %s", field, text, err)
		}
		if templated {
			req.Parameters = readTemplate(field, string(raw)).parameters
		}
		out = append(out, req)
	}
	return out, nil
}

// ResolveHealthChecks renders the URL of every http check.
//
// Address and command checks are returned untouched: a TCP address is a
// host:port pair the manager does not publish, and a command is the vendor's
// executable. Extending either is decision 5's "a third field is a new RFC".
func (m *Manifest) ResolveHealthChecks(params Parameters, profiles []string) ([]HealthCheck, error) {
	out := make([]HealthCheck, len(m.Health.Checks))
	copy(out, m.Health.Checks)

	for i := range out {
		if out[i].Type != HealthHTTP || out[i].URL == "" {
			continue
		}
		url, err := params.Resolve(
			fmt.Sprintf("health.checks[%d].url", i), out[i].URL, profiles)
		if err != nil {
			return nil, err
		}
		out[i].URL = url
	}
	return out, nil
}
