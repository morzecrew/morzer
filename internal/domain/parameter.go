package domain

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ParameterType is the closed set of types a parameter may declare.
//
// Closed, because the point of declaring a parameter is that setting one wrong
// is caught before it reaches a deployment. An open type system would be a
// string map with extra steps.
type ParameterType string

const (
	ParamPort     ParameterType = "port"
	ParamInt      ParameterType = "int"
	ParamBool     ParameterType = "bool"
	ParamString   ParameterType = "string"
	ParamEnum     ParameterType = "enum"
	ParamDuration ParameterType = "duration"
	ParamBytes    ParameterType = "bytes"
)

// ParameterTypes is every valid type, for error messages and the schema.
var ParameterTypes = []ParameterType{
	ParamPort, ParamInt, ParamBool, ParamString, ParamEnum, ParamDuration, ParamBytes,
}

// ParameterSpec is one knob a release exposes to the operator.
//
// A parameter is *not* a secret. Its value reaches Compose as an environment
// variable, appears in `docker inspect`, in `status --json` and in the journal,
// and is written to installation.yaml in the clear. Secrets have their own
// declared, audited, tmpfs-rendered path and this must never become a second
// one.
type ParameterSpec struct {
	Type        ParameterType `yaml:"type" json:"type"`
	Default     string        `yaml:"default" json:"default,omitempty"`
	Description string        `yaml:"description" json:"description,omitempty"`

	// Values is the permitted set for an enum, and is meaningless otherwise.
	Values []string `yaml:"values" json:"values,omitempty"`

	// Services are restarted when the value changes. The same field
	// secrets already use for rotation, so an operator changing a port and
	// an operator rotating a password get the same behaviour.
	//
	// Empty means the change needs a full `apply` -- stated by the vendor
	// rather than guessed by the manager.
	Services []string `yaml:"services" json:"services,omitempty"`
}

// parameterName is the permitted shape of a parameter name.
//
// Lowercase with underscores, because the name becomes the tail of an
// environment variable and a name that had to be transliterated would make
// `<PRODUCT>_PARAM_<NAME>` unpredictable from the manifest.
var parameterName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// ValidateParameters checks a manifest's declarations.
func ValidateParameters(params map[string]ParameterSpec, v *validationErrors) {
	for _, name := range sortedParameterNames(params) {
		spec := params[name]
		field := "parameters." + name

		if !parameterName.MatchString(name) {
			v.add(field, "must be lowercase letters, digits and underscores, starting with a letter")
			continue
		}

		if spec.Type == "" {
			v.add(field+".type", "is required (%s)", joinParameterTypes())
			continue
		}
		if !isParameterType(spec.Type) {
			v.add(field+".type", "unknown type %q (%s)", spec.Type, joinParameterTypes())
			continue
		}

		switch {
		case spec.Type == ParamEnum && len(spec.Values) == 0:
			v.add(field+".values", "is required for type %q", ParamEnum)
		case spec.Type != ParamEnum && len(spec.Values) > 0:
			v.add(field+".values", "is only meaningful for type %q", ParamEnum)
		}

		// A default that does not satisfy its own declaration is the
		// worst kind of manifest bug: nothing fails until an operator
		// who never touched the parameter runs `apply`.
		if spec.Default != "" {
			if _, err := spec.Parse(spec.Default); err != nil {
				v.add(field+".default", "%s", AsError(err).Message)
			}
		}
	}
}

// Parse validates a raw value against the declaration and returns it
// normalised, so `0x1f`, `08` and `31` do not become three different ports.
func (p ParameterSpec) Parse(raw string) (string, error) {
	value := strings.TrimSpace(raw)

	switch p.Type {
	case ParamPort:
		n, err := strconv.Atoi(value)
		if err != nil {
			return "", Usage("%q is not a port number", raw)
		}
		// 0 is excluded deliberately: Compose reads it as "pick one",
		// which makes the health check unable to find the service.
		if n < 1 || n > 65535 {
			return "", Usage("port %d is out of range (1-65535)", n)
		}
		return strconv.Itoa(n), nil

	case ParamInt:
		n, err := strconv.Atoi(value)
		if err != nil {
			return "", Usage("%q is not a whole number", raw)
		}
		return strconv.Itoa(n), nil

	case ParamBool:
		b, err := strconv.ParseBool(value)
		if err != nil {
			return "", Usage("%q is not a boolean (true or false)", raw)
		}
		return strconv.FormatBool(b), nil

	case ParamString:
		return value, nil

	case ParamEnum:
		for _, allowed := range p.Values {
			if value == allowed {
				return value, nil
			}
		}
		return "", Usage("%q is not one of: %s", raw, strings.Join(p.Values, ", "))

	case ParamDuration:
		var d Duration
		if err := d.UnmarshalText([]byte(value)); err != nil {
			return "", Usage("%q is not a duration (30s, 5m, 2h)", raw)
		}
		return d.String(), nil

	case ParamBytes:
		var b ByteSize
		if err := b.UnmarshalText([]byte(value)); err != nil {
			return "", Usage("%q is not a size (512KiB, 25MiB, 2GiB)", raw)
		}
		return b.String(), nil

	default:
		return "", Usage("parameter has unknown type %q", p.Type)
	}
}

// Parameters are the resolved values for one installation.
//
// Every declared parameter is present: an unset one holds its declared default.
// A consumer therefore never has to distinguish "unset" from "set to the
// default", and a Compose file's `:-` fallback is belt-and-braces rather than
// the actual source of the value.
type Parameters map[string]string

// ResolveParameters merges a release's declarations with an operator's choices.
//
// Every value is validated against its declaration on the way through, so a
// release that narrows an enum, or an installation carrying a value from an
// older release, fails here rather than inside Compose.
func ResolveParameters(declared map[string]ParameterSpec, set map[string]string) (Parameters, error) {
	out := make(Parameters, len(declared))

	// A declaration with no default is present with an empty value, not
	// absent. Skipping it broke the type's own contract -- every declared
	// parameter is present -- and produced a refusal that read "the release
	// declares no parameter %q" about a parameter the release declares.
	//
	// Present-empty rather than refused, because this function runs on
	// every operation and only one of them can do anything about a missing
	// value: `init` takes --set, and refuses there (see MissingValues). An
	// update that meets a parameter the *new* release added would otherwise
	// have no way forward at all -- the value cannot be set before the
	// release that declares it is installed.
	for name, spec := range declared {
		if spec.Default == "" {
			out[name] = ""
			continue
		}
		value, err := spec.Parse(spec.Default)
		if err != nil {
			return nil, ValidationError(err, "release parameter %q has an invalid default", name)
		}
		out[name] = value
	}

	for _, name := range sortedStringKeys(set) {
		spec, ok := declared[name]
		if !ok {
			return nil, undeclaredParameter(name, declaredNames(declared))
		}
		value, err := spec.Parse(set[name])
		if err != nil {
			return nil, ValidationError(err, "parameter %q", name).
				WithHint("%s", DescribeParameter(name, spec))
		}
		out[name] = value
	}

	return out, nil
}

// MissingValues lists declared parameters that have neither a default nor a
// value: the ones an operator has to choose, and the only spelling a manifest
// has for "you must choose this".
//
// Separate from ResolveParameters because only the commands that can accept a
// value should refuse over one. `init` does; an `apply` reading state that was
// written months ago cannot, and refusing there would take a deployment down
// over a knob nobody has touched.
func MissingValues(declared map[string]ParameterSpec, set map[string]string) []string {
	var missing []string
	for name, spec := range declared {
		if spec.Default != "" {
			continue
		}
		if value, ok := set[name]; !ok || strings.TrimSpace(value) == "" {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

// Require returns the value of a declared parameter, or an error naming what is
// declared. Used by the manifest templating, where a typo in `{{ .Parameters.htpp_port }}`
// would otherwise render as the empty string and produce a URL nothing serves.
func (p Parameters) Require(name string) (string, error) {
	if v, ok := p[name]; ok {
		return v, nil
	}
	return "", undeclaredParameter(name, sortedStringKeys(p))
}

func undeclaredParameter(name string, declared []string) error {
	err := ValidationError(nil, "the release declares no parameter %q", name)
	if len(declared) > 0 {
		return err.WithHint("it declares: %s", strings.Join(declared, ", "))
	}
	return err.WithHint("this release declares no parameters at all")
}

// ValidateAgainst reports values that the declarations no longer accept.
//
// Separate from ResolveParameters because an update is allowed to drop a
// parameter -- that is the vendor's decision -- and the operator should be told
// rather than blocked.
func (p Parameters) ValidateAgainst(declared map[string]ParameterSpec) []string {
	var stale []string
	for _, name := range sortedStringKeys(p) {
		if _, ok := declared[name]; !ok {
			stale = append(stale, name)
		}
	}
	return stale
}

// DescribeParameter is the hint shown when a value is refused.
func DescribeParameter(name string, spec ParameterSpec) string {
	var b strings.Builder
	if spec.Type == ParamEnum {
		fmt.Fprintf(&b, "%s accepts %s", name, strings.Join(spec.Values, ", "))
	} else {
		fmt.Fprintf(&b, "%s takes a %s value", name, spec.Type)
	}
	if spec.Default != "" {
		fmt.Fprintf(&b, "; default %s", spec.Default)
	}
	if spec.Description != "" {
		fmt.Fprintf(&b, " -- %s", spec.Description)
	}
	return b.String()
}

// ParseAssignments turns `name=value` arguments into a map.
//
// Splits on the first `=` only, so a value may contain one.
func ParseAssignments(args []string) (map[string]string, error) {
	out := make(map[string]string, len(args))
	for _, arg := range args {
		name, value, ok := strings.Cut(arg, "=")
		if !ok {
			return nil, Usage("%q is not a name=value assignment", arg)
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, Usage("%q has an empty parameter name", arg)
		}
		if _, dup := out[name]; dup {
			return nil, Usage("parameter %q is set twice", name)
		}
		out[name] = value
	}
	return out, nil
}

func isParameterType(t ParameterType) bool {
	for _, known := range ParameterTypes {
		if t == known {
			return true
		}
	}
	return false
}

func joinParameterTypes() string {
	names := make([]string, len(ParameterTypes))
	for i, t := range ParameterTypes {
		names[i] = string(t)
	}
	return strings.Join(names, ", ")
}

func declaredNames(m map[string]ParameterSpec) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func sortedParameterNames(m map[string]ParameterSpec) []string { return declaredNames(m) }

func sortedStringKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
