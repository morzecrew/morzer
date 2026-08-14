package domain

import "strings"

// Who a support bundle is encrypted to (RFC 0024 P4).
//
// **Why this lives under `extensions` and not at the top level.** RFC 0024
// §11.4 recorded that the field could be first-class only while no tag existed,
// and left it as the one item in that RFC with an external deadline. The
// deadline passed: v0.1.0 and v0.1.1 are tagged. Measured against a v0.1.1
// binary, a top-level `support:` block makes a released manager refuse the
// whole bundle with `unknown field "support"` -- a bundle that no longer
// installs, over a field about diagnostics. The same declaration under
// `extensions."morzer.dev/support"` verifies clean on that same binary, which
// is exactly what RFC 0018 §5.4 said the namespace was for.
//
// The trade is stated rather than hidden: an old manager *tolerates* the block
// and ignores it, so a vendor who declares recipients gets plaintext bundles
// from operators who have not upgraded. That costs nothing those operators had
// -- a manager without P4 cannot encrypt whatever the field is called -- and it
// is the only option that does not make a support field break an installation.

// SupportExtension is the extensions namespace a vendor declares support
// recipients in.
const SupportExtension = "morzer.dev/support"

// supportRecipientsKey is the one key that namespace carries today.
const supportRecipientsKey = "recipients"

// SupportRecipients returns the age recipients this release's vendor wants
// support bundles encrypted to.
//
// Three outcomes, and the middle one is the reason this returns an error at
// all. No block means no declaration: the archive is plaintext and the command
// says so, which decision 3 keeps available because the operator posting to a
// forum is the case the whole feature rests on. A block that parses names the
// recipients. A block that does *not* parse is a refusal -- decision 3a, and
// the grade on that row is LOCKED.
//
// "Declared but unusable" must never read as "absent". Falling back would hand
// a plaintext archive to the operator who most clearly asked for an encrypted
// one, and do it quietly, at the moment they are attaching it to a ticket.
//
// The shape only. Whether a string is a usable age key is not a question this
// layer can answer -- the domain holds no crypto -- so the caller validates
// each one before anything is collected.
func (m *Manifest) SupportRecipients() ([]string, error) {
	block, declared := m.Extensions[SupportExtension]
	if !declared {
		return nil, nil
	}

	raw, present := block[supportRecipientsKey]
	if !present {
		// A namespace with no recipients in it is a typo, not a
		// configuration. `recipient:`, `recipents:`, or a key moved one
		// level too deep all land here, and every one of them would
		// otherwise produce a plaintext archive from a manifest whose
		// author believed they had asked for encryption.
		return nil, ValidationError(nil,
			"`extensions.%q` declares no %s", SupportExtension, supportRecipientsKey).
			WithHint("the block exists to name who a support bundle is "+
				"encrypted to; remove it, or give it a `%s:` list",
				supportRecipientsKey)
	}

	list, ok := raw.([]any)
	if !ok {
		return nil, ValidationError(nil,
			"`extensions.%q.%s` is %s, not a list of age recipients",
			SupportExtension, supportRecipientsKey, yamlKindOf(raw)).
			WithHint("write it as a list, even for one recipient")
	}
	if len(list) == 0 {
		// Parseable and empty, which is not the same as absent: it names
		// nobody, so encrypting to it is impossible and falling back to
		// plaintext is the trap decision 3a closes.
		return nil, ValidationError(nil,
			"`extensions.%q.%s` names nobody", SupportExtension, supportRecipientsKey).
			WithHint("remove the block to produce plaintext archives on purpose")
	}

	out := make([]string, 0, len(list))
	for i, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, ValidationError(nil,
				"`extensions.%q.%s[%d]` is %s, not an age recipient",
				SupportExtension, supportRecipientsKey, i, yamlKindOf(item))
		}
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			return nil, ValidationError(nil,
				"`extensions.%q.%s[%d]` is empty",
				SupportExtension, supportRecipientsKey, i)
		}
		out = append(out, trimmed)
	}
	return out, nil
}

// yamlKindOf names what arrived, for a message that has to tell a vendor which
// of their lines is wrong. Go's %T would say `map[string]interface {}`, which
// describes the decoder rather than the document.
//
// Every shape a YAML node can decode to is named, including the list -- an
// over-indented key turns a recipient into a nested list, and that is one of
// the likelier mistakes rather than an exotic one. It reached the fallback and
// was reported as `a []interface {}`, which is the leak this function exists to
// prevent, in the branch written to prevent it.
//
// The fallback names no type at all. It is reachable only for a decoder shape
// this list has not learned about, and at that point the vendor is better
// served by the path already in the message -- which points at the line -- than
// by the name of a Go type they did not write.
func yamlKindOf(v any) string {
	switch v.(type) {
	case nil:
		return "empty"
	case string:
		return "a single value"
	case []any:
		return "a list"
	case map[string]any:
		return "a mapping"
	case bool:
		return "a boolean"
	case int, int64, uint64, float64:
		return "a number"
	default:
		return "an unexpected value"
	}
}
