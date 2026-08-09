package gotemplate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// SyntheticData builds a render context for a bundle that has no installation.
//
// It lives here, beside newView and the real context it imitates, because the
// two have to drift together: a field added to the template surface without a
// synthetic value silently narrows what `release verify --render-check`
// exercises, and a context that lived away from the renderer would go stale
// with nothing to notice.
//
// Every value in it is invented. That is the whole reason `--render-check` is
// opt-in and named as a smoke test (RFC 0013 decision 12): a template branching
// on `{{- if .Domains }}` exercises only the branch these values choose, and no
// amount of care here turns "rendered" into "will render on the customer's
// machine".
//
// What it is *not* inventing is the bundle's own declarations. Secret names come
// from the schema the manifest declares and parameters from the manifest's own
// specs, so `{{ secretFile .Secrets "typo" }}` and `{{ .Parameters.htpp_port }}`
// fail here rather than at an operator's apply -- which is the half of this
// check that carries real information.
func SyntheticData(rel domain.Release, schema domain.SecretSchema) ports.TemplateData {
	product := rel.Name()
	if product == "" {
		product = "product"
	}
	paths := domain.DefaultPaths(product)

	inst := domain.Installation{
		SchemaVersion: domain.InstallationSchemaVersion,
		ID:            "00000000-0000-0000-0000-000000000000",
		Product:       product,
		Profile:       syntheticProfile(rel),
		Domains:       []string{product + ".example"},
	}

	secretPaths := make(map[string]string, len(schema.Secrets))
	for _, decl := range schema.Secrets {
		secretPaths[decl.Name] = paths.SecretsRenderDir() + "/" + decl.FileName()
	}

	return ports.TemplateData{
		Installation: inst,
		Release: ports.ReleaseInfo{
			Name:    product,
			Version: rel.Version(),
			Digest:  rel.Digest,
			Root:    rel.Root,
			Vendor:  syntheticVendor(rel),
		},
		Profile: inst.Profile,
		Paths: ports.PathInfo{
			Etc:       paths.EtcDir,
			Var:       paths.VarDir,
			Run:       paths.RunDir,
			Opt:       paths.OptDir,
			Data:      paths.DataDir(),
			Backups:   paths.BackupsDir(),
			Secrets:   paths.SecretsRenderDir(),
			Generated: paths.GeneratedDir(),
		},
		Secrets:    secretPaths,
		Domains:    inst.Domains,
		Parameters: syntheticParameters(rel.Manifest.Parameters),
	}
}

// syntheticProfile picks a profile the release actually declares.
//
// A declared one rather than an invented one because `{{ if eq .Profile "ha" }}`
// is a branch a vendor writes, and a name no profile block matches would take
// the else side of every such template while looking like a real answer. Sorted
// rather than whatever the map yields, so the same branch is exercised on every
// run: a check that rendered a different profile each time would report a
// failure that disappears when someone looks into it.
func syntheticProfile(rel domain.Release) string {
	names := make([]string, 0, len(rel.Manifest.Runtime.Profiles))
	for name := range rel.Manifest.Runtime.Profiles {
		names = append(names, name)
	}
	if len(names) == 0 {
		// A release declaring no profiles installs with none, so this is
		// the honest value rather than a placeholder -- and the empty
		// string is what such a template sees in the field.
		return ""
	}
	sort.Strings(names)
	return names[0]
}

func syntheticVendor(rel domain.Release) string {
	if v := rel.Manifest.Metadata.Vendor; v != "" {
		return v
	}
	return "example"
}

// syntheticParameters resolves the release's declarations the way an
// installation would, inventing a value where the operator would have supplied
// one.
//
// A declared-but-undefaulted parameter resolves to the empty string in a real
// installation (domain.ResolveParameters), and using that here would fail
// `{{ required "set the port" .Parameters.http_port }}` on a bundle that is
// entirely correct -- a smoke test that cries wolf about the operator's job.
// The invented value satisfies the declaration, so a template that formats or
// compares it behaves the way it will in the field.
func syntheticParameters(declared map[string]domain.ParameterSpec) domain.Parameters {
	out := make(domain.Parameters, len(declared))
	for name, spec := range declared {
		if spec.Default != "" {
			// Parsed rather than copied, so the synthetic value is
			// normalised exactly as the real one is: a default of
			// `08` reaches a template as `8` at install time, and a
			// check that rendered `08` would be checking something
			// no installation produces.
			if value, err := spec.Parse(spec.Default); err == nil {
				out[name] = value
				continue
			}
			// An invalid default is a manifest error, and manifest
			// validation already refuses it by name. Falling through
			// to the invented value keeps that the reported failure
			// rather than burying it in a render error.
		}
		out[name] = syntheticValue(spec)
	}
	return out
}

// syntheticValue invents one value that satisfies a declaration.
func syntheticValue(spec domain.ParameterSpec) string {
	switch spec.Type {
	case domain.ParamPort:
		return "8080"
	case domain.ParamInt:
		return "1"
	case domain.ParamBool:
		return "false"
	case domain.ParamEnum:
		if len(spec.Values) > 0 {
			return spec.Values[0]
		}
		// An enum with no values is refused by manifest validation, so
		// this is unreachable through a loaded release. Returning a
		// non-empty string keeps the context's promise -- every declared
		// parameter is present with a value -- for a hand-built spec.
		return "example"
	case domain.ParamDuration:
		return "30s"
	case domain.ParamBytes:
		return "1MiB"
	case domain.ParamString:
		return "example"
	default:
		// An unknown type cannot be satisfied, and guessing would be
		// inventing a value the declaration does not admit. Manifest
		// validation refuses the type by name; a non-empty placeholder
		// here keeps the render check from failing on top of it with a
		// less useful message.
		return "example"
	}
}

// CheckRender renders a template against the synthetic context.
//
// The message carries the execution error whole, because that is where the
// information is: text/template reports the line, the column and the action that
// failed, and "does not render" without them tells an author which file to look
// at and nothing about where. It is the same text an operator would have met at
// apply, which is the point of moving it earlier.
func CheckRender(rel domain.Release, schema domain.SecretSchema, name string, raw []byte) error {
	tmpl, err := parse(name, raw)
	if err != nil {
		return err
	}

	var sb strings.Builder
	if err := tmpl.Execute(&sb, newView(SyntheticData(rel, schema))); err != nil {
		return domain.ValidationError(fmt.Errorf("%w: %w", domain.ErrTemplateRender, err),
			"%s does not render: %s", name, executionDetail(name, err)).
			WithHint("%s", renderHint(err))
	}
	return nil
}

// executionDetail trims the redundant prefix off a template execution error.
//
// text/template prefixes every one with "template: <name>:", and the caller has
// just named the template -- so quoting it whole reads as a stutter in a list of
// several problems. The name is trimmed with the prefix rather than left behind
// it: dropping only "template: " kept the stutter and merely moved it, which is
// what "configuration[0].template: config.yaml.tmpl does not render:
// config.yaml.tmpl:3:12: ..." reads as.
//
// What remains is the line, the column and the action that failed, which is the
// part carrying the information.
func executionDetail(name string, err error) string {
	return strings.TrimPrefix(err.Error(), "template: "+name+":")
}
