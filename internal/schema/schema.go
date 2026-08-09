// Package schema generates the JSON Schema for the release manifest from the Go
// types that enforce it.
//
// A hand-written schema is a second description of one contract, and two
// descriptions of one contract disagree -- usually quietly, and usually in the
// direction where the editor a vendor validates against is more permissive than
// the loader that will reject their bundle. Generating it means the schema
// cannot drift; the checked-in copy is compared against fresh output by a test,
// so forgetting to regenerate fails the build rather than shipping a lie.
//
// What it deliberately does not express: the rules that are not shape. Images
// must be pinned by digest, paths must not escape the release root, an unknown
// field is an error -- those live in Manifest.Validate and are checked by
// `morzer release verify`. The schema catches a typo in an editor; the loader
// catches everything.
package schema

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
)

// Dir is where the generated schemas live, relative to the repository root.
const Dir = "schemas"

// File names, exported so the drift test names the same files.
const (
	ManifestSchemaFile = "selfhost-v1alpha1-manifest.json"
	SecretSchemaFile   = "selfhost-v1alpha1-secrets.json"
)

const schemaBase = "https://morzecrew.github.io/morzer/schemas/"

// Manifest returns the generated manifest schema as formatted JSON.
func Manifest() ([]byte, error) { return render(manifestSchema()) }

// Secrets returns the generated schema for a bundle's secret schema.
func Secrets() ([]byte, error) { return render(secretSchema()) }

func render(s map[string]any) ([]byte, error) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func manifestSchema() map[string]any {
	s := schemaFor(reflect.TypeOf(domain.Manifest{}))
	s["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	s["$id"] = schemaBase + ManifestSchemaFile
	s["title"] = "morzer release manifest"
	s["description"] = "The release contract for api_version " +
		string(domain.APIVersionV1Alpha1) + ". Generated from the Go types " +
		"that enforce it; do not edit by hand."

	// Enumerations and required fields the struct shape cannot carry.
	props, _ := s["properties"].(map[string]any)
	props["api_version"] = withEnum(props["api_version"], apiVersions())
	props["kind"] = withEnum(props["kind"], []string{domain.KindApplicationRelease})
	s["required"] = []string{"api_version", "kind", "metadata", "runtime", "images"}

	return s
}

func secretSchema() map[string]any {
	s := schemaFor(reflect.TypeOf(domain.SecretSchema{}))
	s["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	s["$id"] = schemaBase + SecretSchemaFile
	s["title"] = "morzer secret schema"
	s["description"] = "The declaration of what secrets a release needs, " +
		"conventionally secrets.schema.yaml at the bundle root, though the manifest " +
		"names the path. Generated from the Go types; do not edit by hand."

	props, _ := s["properties"].(map[string]any)
	props["api_version"] = withEnum(props["api_version"], apiVersions())
	s["required"] = []string{"api_version", "secrets"}

	return s
}

func apiVersions() []string {
	out := make([]string, 0, len(domain.SupportedAPIVersions))
	for _, v := range domain.SupportedAPIVersions {
		out = append(out, string(v))
	}
	return out
}

func withEnum(node any, values []string) any {
	m, ok := node.(map[string]any)
	if !ok {
		return node
	}
	m["enum"] = values
	return m
}

// scalarSchemas maps the domain's wrapper types onto what they serialise as.
//
// Each of these marshals to a string with its own syntax -- a duration, a size,
// an octal mode, a semver range -- so reflecting into their unexported fields
// would describe the Go representation rather than the YAML a vendor writes.
var scalarSchemas = map[reflect.Type]map[string]any{
	reflect.TypeOf(domain.Version{}): {
		"type":        "string",
		"pattern":     `^v?\d+\.\d+\.\d+(?:[-+].*)?$`,
		"description": "A semantic version, e.g. 1.2.0.",
		"examples":    []string{"1.2.0"},
	},
	reflect.TypeOf(domain.Constraint{}): {
		"type":        "string",
		"description": "A semantic version constraint, e.g. \">=1.0.0 <2.0.0\".",
		"examples":    []string{">=1.0.0 <2.0.0"},
	},
	reflect.TypeOf(domain.Duration(0)): {
		"type":        "string",
		"pattern":     `^\d+(?:\.\d+)?(?:ns|us|ms|s|m|h)(?:\d+(?:\.\d+)?(?:ns|us|ms|s|m|h))*$`,
		"description": "A duration, e.g. 30s or 10m.",
		"examples":    []string{"30s", "10m"},
	},
	reflect.TypeOf(domain.ByteSize(0)): {
		"type":        "string",
		"description": "A byte size, e.g. 2GiB.",
		"examples":    []string{"2GiB", "512MiB"},
	},
	reflect.TypeOf(domain.FileMode(0)): {
		"type":        "string",
		"pattern":     `^0?[0-7]{3,4}$`,
		"description": "An octal file mode, quoted, e.g. \"0640\".",
		"examples":    []string{"0640"},
	},
	reflect.TypeOf(domain.Time{}): {
		"type":        "string",
		"format":      "date-time",
		"description": "An RFC3339 timestamp in UTC.",
	},
}

// requiredFields lists, per struct type, the keys the loader refuses to see
// missing. Nothing in the Go shape carries that -- an absent string and an
// empty one decode alike -- so it is stated here, next to the type it
// constrains.
//
// backup.volumes is the case: a volume listed with no consistency is refused
// by Manifest.Validate, because leaving the volume out of the map is already
// how a vendor asks for the default. Without this the editor would accept an
// entry the manager rejects.
var requiredFields = map[reflect.Type][]string{
	reflect.TypeOf(domain.VolumeSpec{}): {"consistency"},
}

// schemaFor renders a Go type as a JSON Schema node.
func schemaFor(t reflect.Type) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	if s, ok := scalarSchemas[t]; ok {
		return clone(s)
	}

	switch t.Kind() {
	case reflect.String:
		return stringSchema(t)

	case reflect.Bool:
		return map[string]any{"type": "boolean"}

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}

	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}

	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": schemaFor(t.Elem())}

	case reflect.Map:
		// A map of anything is a map of anything: `settings` and a
		// vendor's `extensions` block are deliberately unconstrained,
		// and pretending otherwise in the schema would reject data the
		// loader accepts.
		if isAny(t.Elem()) {
			return map[string]any{"type": "object"}
		}
		return map[string]any{"type": "object", "additionalProperties": schemaFor(t.Elem())}

	case reflect.Struct:
		return structSchema(t)

	case reflect.Interface:
		return map[string]any{}

	default:
		return map[string]any{}
	}
}

// stringSchema renders a string, adding an enum for the named string types
// whose values are a closed set.
func stringSchema(t reflect.Type) map[string]any {
	out := map[string]any{"type": "string"}

	switch t {
	case reflect.TypeOf(domain.OperationKindHook):
		out["enum"] = []string{
			string(domain.OperationKindHook),
			string(domain.OperationKindRuntimeService),
		}
	case reflect.TypeOf(domain.HealthHTTP):
		out["enum"] = []string{
			string(domain.HealthHTTP), string(domain.HealthTCP), string(domain.HealthCommand),
		}
	case reflect.TypeOf(domain.ParamPort):
		values := make([]string, len(domain.ParameterTypes))
		for i, pt := range domain.ParameterTypes {
			values[i] = string(pt)
		}
		out["enum"] = values
	case reflect.TypeOf(domain.VolumeCold):
		values := make([]string, len(domain.VolumeConsistencies))
		for i, c := range domain.VolumeConsistencies {
			values[i] = string(c)
		}
		out["enum"] = values
		out["description"] = "How this volume may be read. `cold` is the default " +
			"and needs no declaration: the services mounting it are stopped for " +
			"the copy. `hot` claims a copy taken while they run is usable. " +
			"`exclude` keeps the manager out of it entirely."
	case reflect.TypeOf(domain.PortSpec("")):
		out["description"] = "A port number, or a {{ .Parameters.<name> }} reference."
		out["examples"] = []string{"18080", "{{ .Parameters.http_port }}"}
	case reflect.TypeOf(domain.GeneratorPassword):
		out["enum"] = []string{
			string(domain.GeneratorPassword), string(domain.GeneratorHex),
			string(domain.GeneratorBase64), string(domain.GeneratorAgeKey),
			string(domain.GeneratorUUID),
		}
	}
	return out
}

// structSchema renders a struct from its yaml tags.
//
// The yaml tags rather than the json ones: the schema describes the file a
// vendor writes, and that file is YAML. They agree today, and taking the yaml
// tags means the schema follows the format it documents if they ever stop.
func structSchema(t reflect.Type) map[string]any {
	props := map[string]any{}
	var names []string

	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		props[name] = schemaFor(f.Type)
		names = append(names, name)
	}
	sort.Strings(names)

	out := map[string]any{
		"type":       "object",
		"properties": props,
		// The loader rejects an unknown field outright, so the schema
		// says so too -- an editor that accepted a typo the manager
		// refuses would be worse than no editor support at all.
		"additionalProperties": false,
	}
	if req, ok := requiredFields[t]; ok {
		out["required"] = req
	}
	return out
}

func isAny(t reflect.Type) bool {
	return t.Kind() == reflect.Interface && t.NumMethod() == 0
}

func clone(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
