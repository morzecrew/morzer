package domain

import (
	"log/slog"
	"maps"
	"slices"
	"sort"
	"strings"
)

// Redacted is what a secret renders as anywhere other than the file it is
// written to.
const Redacted = "[redacted]"

// Secret wraps a secret value. Its String and LogValue both return
// [redacted], so leaking a secret through fmt, slog, or an error message
// requires the deliberate act of calling Reveal.
//
// The type is a struct rather than a string alias precisely so that an
// accidental `string(s)` conversion does not compile.
type Secret struct {
	value string
}

func NewSecret(v string) Secret { return Secret{value: v} }

// Reveal returns the plaintext. Every call site is a place to ask "does this
// value now escape?" -- grep for it during review.
func (s Secret) Reveal() string { return s.value }

func (s Secret) IsEmpty() bool { return s.value == "" }
func (s Secret) Len() int      { return len(s.value) }

func (s Secret) String() string { return Redacted }

func (s Secret) GoString() string { return Redacted }

func (s Secret) LogValue() slog.Value { return slog.StringValue(Redacted) }

// MarshalJSON and MarshalYAML keep secrets out of --json output and out of
// any state file that is not the encrypted one.
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + Redacted + `"`), nil }

func (s Secret) MarshalYAML() (any, error) { return Redacted, nil }

// SecretSet is the decrypted secret state, held in memory for the duration of
// one operation and never written anywhere but the render target.
type SecretSet struct {
	values map[string]Secret
}

func NewSecretSet(values map[string]Secret) SecretSet {
	if values == nil {
		values = map[string]Secret{}
	}
	return SecretSet{values: values}
}

func (s SecretSet) Get(name string) (Secret, bool) {
	v, ok := s.values[name]
	return v, ok
}

func (s SecretSet) Has(name string) bool {
	v, ok := s.values[name]
	return ok && !v.IsEmpty()
}

// Names returns secret names in sorted order. Names are not sensitive --
// `secret list` shows them -- but values never accompany them.
func (s SecretSet) Names() []string {
	out := slices.Sorted(maps.Keys(s.values))
	return out
}

func (s SecretSet) Len() int { return len(s.values) }

// RevealAll returns the plaintext map for rendering. Callers must not retain
// or log the result; it exists so the renderer can substitute values in one
// place.
func (s SecretSet) RevealAll() map[string]string {
	out := make(map[string]string, len(s.values))
	for n, v := range s.values {
		out[n] = v.Reveal()
	}
	return out
}

// RedactionList returns every non-empty secret value, for registration with
// the exec runner and the log redaction handler. This is the last line of
// defence: the Secret type is the first.
func (s SecretSet) RedactionList() []string {
	out := make([]string, 0, len(s.values))
	for _, v := range s.values {
		// Very short values would redact harmlessly-common substrings
		// out of logs, hiding information without protecting anything.
		// The same constant the generator is bounded by, so the two
		// cannot drift: a value the generator would refuse to produce
		// is exactly the one this would refuse to scrub.
		if v.Len() >= MinRedactableLength {
			out = append(out, v.Reveal())
		}
	}
	return out
}

// SecretSchema is the release's declaration of what
// secrets exist. It is what lets `init` provision and `doctor` audit secrets
// without the manager knowing anything about the product.
type SecretSchema struct {
	APIVersion APIVersion          `yaml:"api_version" json:"api_version"`
	Secrets    []SecretDeclaration `yaml:"secrets" json:"secrets"`
}

type GeneratorKind string

const (
	// GeneratorNone means the operator must supply the value; `init`
	// prompts for it and `doctor` reports it missing.
	GeneratorNone GeneratorKind = ""
	// GeneratorPassword is a random string from an alphabet.
	GeneratorPassword GeneratorKind = "password"
	// GeneratorHex is random bytes rendered as hex -- for keys that must
	// survive being pasted into a shell.
	GeneratorHex GeneratorKind = "hex"
	// GeneratorBase64 is random bytes, standard base64.
	GeneratorBase64 GeneratorKind = "base64"
	// GeneratorAgeKey is an age identity, for products that do their own
	// envelope encryption.
	GeneratorAgeKey GeneratorKind = "age-key"
	// GeneratorUUID is a random v4 UUID.
	GeneratorUUID GeneratorKind = "uuid"
)

type SecretDeclaration struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description,omitempty"`
	Required    bool   `yaml:"required" json:"required"`

	Generator Generator `yaml:"generator" json:"generator,omitempty"`

	// File is the name under the render directory. Defaults to Name.
	File string `yaml:"file" json:"file,omitempty"`

	// Services are the Compose services that consume this secret. A
	// rotation restarts exactly these, not the whole project -- which is
	// the difference between a two-second blip and a full outage.
	Services []string `yaml:"services" json:"services,omitempty"`

	// RotationPeriod is advisory: `doctor` warns when a secret is older.
	RotationPeriod Duration `yaml:"rotation_period" json:"rotation_period,omitempty"`
}

// FileName is the name the secret is rendered under.
func (d SecretDeclaration) FileName() string {
	if d.File != "" {
		return d.File
	}
	return d.Name
}

type Generator struct {
	Kind     GeneratorKind `yaml:"kind" json:"kind,omitempty"`
	Length   int           `yaml:"length" json:"length,omitempty"`
	Alphabet string        `yaml:"alphabet" json:"alphabet,omitempty"`
}

// Auto reports whether the manager can produce this value itself.
func (g Generator) Auto() bool { return g.Kind != GeneratorNone }

// DefaultAlphabet excludes characters that are ambiguous when read aloud or
// that need shell quoting -- secrets get copied by humans more often than
// anyone plans for.
const DefaultAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

const DefaultSecretLength = 32

// MinRedactableLength is the shortest value the log redactor will scrub, and
// therefore the shortest one the manager will generate.
//
// Below it, redaction is worse than useless: replacing every occurrence of a
// four-character string would chop unrelated words out of a tool's output while
// protecting something an attacker could guess in a moment. So the redactor
// skips short values -- which means generating one hands the operator a
// credential that is guaranteed to appear in the logs in the clear.
//
// It lives here rather than beside the redactor because both sides have to
// agree, and infra imports domain rather than the other way round.
const MinRedactableLength = 6

// Validate refuses a generator the manager should not run.
//
// The length rules are two: nothing shorter than the redaction floor, ever, and
// a password of at least eight characters -- one is about what the manager can
// keep out of a log, the other about what an attacker has to guess.
func (g Generator) Validate() error {
	if !g.Auto() {
		return nil
	}
	if g.Length < 0 {
		return Usage("a generated secret cannot have a negative length")
	}

	// The kinds that ignore Length say nothing about it.
	switch g.Kind {
	case GeneratorPassword, GeneratorHex, GeneratorBase64:
	default:
		return nil
	}

	length := g.Resolved().Length
	if g.Kind == GeneratorPassword && length < 8 {
		return Usage("generated passwords must be at least 8 characters, got %d", length).
			WithHint("the default is %d", DefaultSecretLength)
	}
	if length < MinRedactableLength {
		return Usage("a generated secret must be at least %d characters, got %d",
			MinRedactableLength, length).
			WithHint("anything shorter is below the log redaction floor, so it " +
				"would appear in tool output in the clear")
	}
	return nil
}

// Resolved returns the generator with defaults applied.
func (g Generator) Resolved() Generator {
	if g.Length == 0 {
		g.Length = DefaultSecretLength
	}
	if g.Alphabet == "" {
		g.Alphabet = DefaultAlphabet
	}
	return g
}

// Declaration looks up a secret by name.
func (s SecretSchema) Declaration(name string) (SecretDeclaration, bool) {
	for _, d := range s.Secrets {
		if d.Name == name {
			return d, true
		}
	}
	return SecretDeclaration{}, false
}

// RequiredNames lists the secrets that must be present for `apply` to run.
func (s SecretSchema) RequiredNames() []string {
	var out []string
	for _, d := range s.Secrets {
		if d.Required {
			out = append(out, d.Name)
		}
	}
	sort.Strings(out)
	return out
}

// Missing returns required secrets absent from the set -- the check `doctor`
// and `apply` preflight both run.
func (s SecretSchema) Missing(set SecretSet) []string {
	var out []string
	for _, d := range s.Secrets {
		if d.Required && !set.Has(d.Name) {
			out = append(out, d.Name)
		}
	}
	sort.Strings(out)
	return out
}

// ServicesFor returns the Compose services depending on any of the named
// secrets, deduplicated and sorted. Used to restart the minimum set after a
// rotation.
func (s SecretSchema) ServicesFor(names []string) []string {
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, d := range s.Secrets {
		if !want[d.Name] {
			continue
		}
		for _, svc := range d.Services {
			if !seen[svc] {
				seen[svc] = true
				out = append(out, svc)
			}
		}
	}
	sort.Strings(out)
	return out
}

var secretNamePattern = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-."

// Validate checks the schema. Secret names become filenames, so they are
// constrained the same way manifest paths are.
func (s SecretSchema) Validate() error {
	var v validationErrors

	if s.APIVersion != "" && !isSupportedAPIVersion(s.APIVersion) {
		v.add("api_version", "unsupported secret schema version %q", s.APIVersion)
	}

	seen := map[string]bool{}
	for i, d := range s.Secrets {
		field := "secrets[" + itoa(i) + "]"
		switch {
		case d.Name == "":
			v.add(field+".name", "is required")
		case seen[d.Name]:
			v.add(field+".name", "duplicate secret name %q", d.Name)
		case strings.ContainsAny(d.Name, "/\\") || d.Name == "." || d.Name == "..":
			v.add(field+".name", "%q is not usable as a filename", d.Name)
		case strings.TrimLeft(d.Name, secretNamePattern) != "":
			v.add(field+".name", "%q contains characters outside [A-Za-z0-9_-.]", d.Name)
		default:
			seen[d.Name] = true
		}

		switch d.Generator.Kind {
		case GeneratorNone, GeneratorPassword, GeneratorHex, GeneratorBase64, GeneratorAgeKey, GeneratorUUID:
		default:
			v.add(field+".generator.kind", "unknown generator %q", d.Generator.Kind)
		}
		if err := d.Generator.Validate(); err != nil {
			v.add(field+".generator.length", "%s", AsError(err).Message)
		}
	}

	return v.err()
}

// itoa avoids pulling strconv into this file for one call site.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
