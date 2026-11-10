package ops

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"

	"github.com/morzecrew/morzer/internal/domain"
)

// ChangeDocumentLimit bounds a change document, the bound a piped secret
// already has (RFC 0056 decision 4).
const ChangeDocumentLimit = 1 << 20

// ConfigChange is one change to an installation: settings, parameters and
// secrets to set, and names to return to their absent state (RFC 0056 §5.1).
type ConfigChange struct {
	Settings   map[string]string
	Parameters map[string]string
	Secrets    map[string]domain.Secret
	Unset      ConfigChangeUnset
}

// ConfigChangeUnset names what a change removes, by section.
type ConfigChangeUnset struct {
	Settings   []string
	Parameters []string
	Secrets    []string
}

// IsEmpty reports whether the change names nothing at all.
func (c ConfigChange) IsEmpty() bool {
	return len(c.Settings) == 0 && len(c.Parameters) == 0 && len(c.Secrets) == 0 &&
		len(c.Unset.Settings) == 0 && len(c.Unset.Parameters) == 0 && len(c.Unset.Secrets) == 0
}

// ParseConfigChange reads a change document.
//
// Walked as a syntax tree rather than decoded into a struct, so that a scalar
// keeps the text it was written as: decoded, `9000` becomes a number and
// `1.10` becomes 1.1 before any parameter type sees it. Strict: an unknown
// key, a list or a map where a value goes, a null, a tag, an anchor or an
// alias is refused, each with its line.
func ParseConfigChange(data []byte) (ConfigChange, error) {
	change, _, err := parseChangeDocument(data, nil)
	return change, err
}

// DesiredStateEnvelope is what a desired-state document says about itself,
// around the change it carries (RFC 0057 §5.2).
type DesiredStateEnvelope struct {
	APIVersion   string
	Kind         string
	Product      string
	Installation string
	Serial       int64
	IssuedAt     time.Time
	ExpiresAt    time.Time
}

// ParseDesiredState reads a decrypted desired-state document: the envelope,
// and the change, which is a change document's body and is read by the same
// rules. Every envelope field is required.
func ParseDesiredState(data []byte) (DesiredStateEnvelope, ConfigChange, error) {
	var env DesiredStateEnvelope
	change, seen, err := parseChangeDocument(data, &env)
	if err != nil {
		return env, change, err
	}
	for _, key := range []string{"api_version", "kind", "product", "installation", "serial", "issued_at", "expires_at"} {
		if !seen[key] {
			return env, change, domain.Usage("the desired-state document has no %s", key)
		}
	}
	switch {
	case env.Kind != domain.DesiredStateKind:
		return env, change, domain.Usage("the document is of kind %q, not %s", env.Kind, domain.DesiredStateKind)
	case env.APIVersion != string(domain.APIVersionV1Alpha1):
		return env, change, domain.Usage("the document's api_version %q is not %s", env.APIVersion,
			domain.APIVersionV1Alpha1)
	case !env.ExpiresAt.After(env.IssuedAt):
		return env, change, domain.Usage("the document expires before it was issued")
	}
	return env, change, nil
}

// parseChangeDocument walks a document's top level. With env, the envelope
// keys are read into it; without, they are unknown sections like any other.
func parseChangeDocument(data []byte, env *DesiredStateEnvelope) (ConfigChange, map[string]bool, error) {
	change := ConfigChange{}
	seen := map[string]bool{}
	if len(data) > ChangeDocumentLimit {
		return change, seen, domain.Usage("the change document is larger than 1 MiB")
	}
	file, err := parser.ParseBytes(data, 0)
	if err != nil {
		return change, seen, notYAML(err)
	}
	if len(file.Docs) > 1 {
		return change, seen, domain.Usage("the change document holds %d YAML documents; it is one", len(file.Docs))
	}
	if len(file.Docs) == 0 || file.Docs[0].Body == nil {
		return change, seen, nil
	}
	top, err := mappingOf(file.Docs[0].Body, "the change document")
	if err != nil || top == nil {
		return change, seen, err
	}

	for _, entry := range top.Values {
		key, err := keyOf(entry)
		if err != nil {
			return change, seen, err
		}
		if seen[key] {
			return change, seen, lineError(entry.Key, "%q appears twice", key)
		}
		seen[key] = true
		switch key {
		case "settings":
			change.Settings, err = scalarMap(entry.Value, key)
		case "parameters":
			change.Parameters, err = scalarMap(entry.Value, key)
		case "secrets":
			var values map[string]string
			if values, err = scalarMap(entry.Value, key); err == nil && values != nil {
				change.Secrets = make(map[string]domain.Secret, len(values))
				for name, value := range values {
					// What a piped `secret set` strips, so `|` gives the
					// bytes `printf '%s\n' "$v" | secret set` stores.
					change.Secrets[name] = domain.NewSecret(strings.TrimSuffix(value, "\n"))
				}
			}
		case "unset":
			change.Unset, err = unsetLists(entry.Value)
		default:
			if env != nil && isEnvelopeKey(key) {
				err = readEnvelope(env, key, entry.Value)
				break
			}
			return change, seen, lineError(entry.Key, "%q is not a section of a change document", key).
				WithHint("the sections are settings, parameters, secrets and unset")
		}
		if err != nil {
			return change, seen, err
		}
	}
	return change, seen, nil
}

func isEnvelopeKey(key string) bool {
	return slices.Contains([]string{"api_version", "kind", "product", "installation", "serial", "issued_at", "expires_at"}, key)
}

func readEnvelope(env *DesiredStateEnvelope, key string, node ast.Node) error {
	text, ok := scalarText(node)
	if !ok || text == "" {
		return lineError(node, "%s is not a single value", key)
	}
	switch key {
	case "api_version":
		env.APIVersion = text
	case "kind":
		env.Kind = text
	case "product":
		env.Product = text
	case "installation":
		env.Installation = text
	case "serial":
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil || n <= 0 || strings.TrimLeft(text, "0123456789") != "" {
			return lineError(node, "serial %q is not a positive whole number", text)
		}
		env.Serial = n
	case "issued_at", "expires_at":
		t, err := time.Parse(time.RFC3339, text)
		if err != nil {
			return lineError(node, "%s %q is not an RFC 3339 time", key, text)
		}
		if key == "issued_at" {
			env.IssuedAt = t
		} else {
			env.ExpiresAt = t
		}
	}
	return nil
}

// mappingOf returns node as a mapping, or nil for an empty section.
func mappingOf(node ast.Node, what string) (*ast.MappingNode, error) {
	switch n := node.(type) {
	case *ast.MappingNode:
		return n, nil
	case *ast.MappingValueNode:
		// A mapping of one entry parses as the entry itself.
		return &ast.MappingNode{Values: []*ast.MappingValueNode{n}}, nil
	case *ast.NullNode:
		return nil, nil
	}
	return nil, lineError(node, "%s is a mapping of names to values", what)
}

func keyOf(entry *ast.MappingValueNode) (string, error) {
	key, ok := scalarText(entry.Key)
	if !ok || key == "" {
		return "", lineError(entry.Key, "a key is a plain name")
	}
	return key, nil
}

// scalarMap reads one section of names and values.
func scalarMap(node ast.Node, section string) (map[string]string, error) {
	m, err := mappingOf(node, section)
	if err != nil || m == nil {
		return nil, err
	}
	out := make(map[string]string, len(m.Values))
	for _, entry := range m.Values {
		name, err := keyOf(entry)
		if err != nil {
			return nil, err
		}
		if _, dup := out[name]; dup {
			return nil, lineError(entry.Key, "%s.%s appears twice", section, name)
		}
		if _, null := entry.Value.(*ast.NullNode); null {
			return nil, lineError(entry.Value, "%s.%s has no value", section, name).
				WithHint("write '' for an empty value, or list the name under unset.%s to remove it", section)
		}
		value, ok := scalarText(entry.Value)
		if !ok {
			return nil, lineError(entry.Value, "%s.%s is not a single value", section, name).
				WithHint("a value is a scalar or a block scalar (|), not a list, a map, a tag or an anchor")
		}
		out[name] = value
	}
	return out, nil
}

func unsetLists(node ast.Node) (ConfigChangeUnset, error) {
	var unset ConfigChangeUnset
	m, err := mappingOf(node, "unset")
	if err != nil || m == nil {
		return unset, err
	}
	seen := map[string]bool{}
	for _, entry := range m.Values {
		key, err := keyOf(entry)
		if err != nil {
			return unset, err
		}
		if seen[key] {
			return unset, lineError(entry.Key, "unset.%s appears twice", key)
		}
		seen[key] = true
		names, err := nameList(entry.Value, "unset."+key)
		if err != nil {
			return unset, err
		}
		switch key {
		case "settings":
			unset.Settings = names
		case "parameters":
			unset.Parameters = names
		case "secrets":
			unset.Secrets = names
		default:
			return unset, lineError(entry.Key, "unset.%s is not a list unset takes", key).
				WithHint("unset takes settings, parameters and secrets")
		}
	}
	return unset, nil
}

func nameList(node ast.Node, what string) ([]string, error) {
	if _, null := node.(*ast.NullNode); null {
		return nil, nil
	}
	seq, ok := node.(*ast.SequenceNode)
	if !ok {
		return nil, lineError(node, "%s is a list of names", what)
	}
	var names []string
	for _, item := range seq.Values {
		name, ok := scalarText(item)
		if !ok || name == "" {
			return nil, lineError(item, "%s holds something other than a name", what)
		}
		if slices.Contains(names, name) {
			return nil, lineError(item, "%s names %q twice", what, name)
		}
		names = append(names, name)
	}
	return names, nil
}

// scalarText is a scalar's text as written: a quoted string's content, a
// block scalar's content, and every other scalar's own token.
func scalarText(node ast.Node) (string, bool) {
	switch n := node.(type) {
	case *ast.StringNode:
		return n.Value, true
	case *ast.LiteralNode:
		return n.Value.Value, true
	case *ast.IntegerNode, *ast.FloatNode, *ast.BoolNode, *ast.InfinityNode, *ast.NanNode:
		return node.GetToken().Value, true
	}
	return "", false
}

// notYAML reports where the document stops parsing, and not the parser's own
// message: it quotes the document around the fault, and a change document
// holds secret values.
func notYAML(err error) *domain.Error {
	var syntax *yaml.SyntaxError
	if errors.As(err, &syntax) && syntax.Token != nil && syntax.Token.Position != nil {
		return domain.Usage("the change document is not valid YAML at line %d, column %d",
			syntax.Token.Position.Line, syntax.Token.Position.Column).
			WithHint("the parser's message is left out because it quotes the document, which can hold secret values")
	}
	return domain.Usage("the change document is not valid YAML").
		WithHint("the parser's message is left out because it quotes the document, which can hold secret values")
}

func lineError(node ast.Node, format string, args ...any) *domain.Error {
	message := strings.TrimSpace(fmt.Sprintf(format, args...))
	if tok := node.GetToken(); tok != nil && tok.Position != nil {
		return domain.Usage("change document line %d: %s", tok.Position.Line, message)
	}
	return domain.Usage("change document: %s", message)
}
