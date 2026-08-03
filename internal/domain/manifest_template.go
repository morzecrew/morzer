package domain

import (
	"fmt"
	"strconv"
	"strings"
	"text/template"
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
// The context is `.Parameters` and nothing else. Not paths, not secrets, not
// the installation: a manifest is read before an installation is necessarily
// resolvable, and a health URL able to interpolate a secret path is a health
// URL able to put one in a log line.

// templateContext is everything a manifest field can see.
type templateContext struct {
	Parameters map[string]string
}

// Resolve renders a manifest field against the parameters.
//
// A field with no action is returned untouched, so the overwhelmingly common
// literal costs nothing and cannot be broken by a templating bug.
func (p Parameters) Resolve(field, text string) (string, error) {
	if !strings.Contains(text, "{{") {
		return text, nil
	}

	// missingkey=error rather than the default: `{{ .Parameters.htpp_port }}`
	// would otherwise render as "<no value>" and produce a URL nothing
	// serves, discovered two minutes later as a health-check timeout.
	tmpl, err := template.New(field).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", ValidationError(err, "%s is not a valid template", field).
			WithHint("only {{ .Parameters.<name> }} is available here")
	}

	var b strings.Builder
	if err := tmpl.Execute(&b, templateContext{Parameters: p}); err != nil {
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

// ResolvePorts renders `requirements.ports` and converts it to numbers.
//
// The field is a list of strings so one mechanism covers both a literal
// `18080` and a `{{ .Parameters.http_port }}`; a manifest written before
// parameters existed keeps working unchanged.
func (m *Manifest) ResolvePorts(params Parameters) ([]int, error) {
	out := make([]int, 0, len(m.Requirements.Ports))

	for i, raw := range m.Requirements.Ports {
		field := fmt.Sprintf("requirements.ports[%d]", i)

		text, err := params.Resolve(field, string(raw))
		if err != nil {
			return nil, err
		}
		port, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil || port < 1 || port > 65535 {
			return nil, ValidationError(nil, "%s resolved to %q, which is not a port number", field, text)
		}
		out = append(out, port)
	}
	return out, nil
}

// ResolveHealthChecks renders the URL of every http check.
//
// Address and command checks are returned untouched: a TCP address is a
// host:port pair the manager does not publish, and a command is the vendor's
// executable. Extending either is decision 5's "a third field is a new RFC".
func (m *Manifest) ResolveHealthChecks(params Parameters) ([]HealthCheck, error) {
	out := make([]HealthCheck, len(m.Health.Checks))
	copy(out, m.Health.Checks)

	for i := range out {
		if out[i].Type != HealthHTTP || out[i].URL == "" {
			continue
		}
		url, err := params.Resolve(
			fmt.Sprintf("health.checks[%d].url", i), out[i].URL)
		if err != nil {
			return nil, err
		}
		out[i].URL = url
	}
	return out, nil
}
