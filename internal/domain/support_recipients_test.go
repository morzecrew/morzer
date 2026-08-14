package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

// What a vendor is told when their declaration will not do (RFC 0024 P4a).
//
// The suite drives these shapes end to end and asserts that the command
// refused. That is the property that matters and it is not this one: a refusal
// is worth having only if the vendor can find the line, and every one of these
// messages could name the wrong thing -- or name a Go type -- while the suite
// stayed green, because it asserts the half of the sentence that does not
// change ("not a list", "not an age recipient"). So the naming half is pinned
// here, where a shape costs a map literal rather than an installation.
//
// The literals are the shapes the YAML decoder really produces, which `ops`'
// own decoder test is the evidence for; the domain layer holds no decoder and
// cannot ask.

func manifestDeclaring(recipients any) *domain.Manifest {
	return &domain.Manifest{
		Extensions: map[string]map[string]any{
			domain.SupportExtension: {"recipients": recipients},
		},
	}
}

// An element that is not a key names what it is instead.
func TestARecipientThatIsNotAKeyIsNamedByItsShape(t *testing.T) {
	for name, tc := range map[string]struct {
		element any
		says    string
	}{
		// The over-indented key: one space too many turns the recipient
		// into a list containing it. Reported as `a []interface {}` until
		// the list was named, which told the vendor about our decoder
		// rather than about their document.
		"a nested list":      {element: []any{"age1nope"}, says: "is a list, not an age recipient"},
		"a mapping":          {element: map[string]any{"key": "age1nope"}, says: "is a mapping, not an age recipient"},
		"a boolean":          {element: true, says: "is a boolean, not an age recipient"},
		"a number":           {element: 42, says: "is a number, not an age recipient"},
		"a float":            {element: 1.5, says: "is a number, not an age recipient"},
		"nothing at all":     {element: nil, says: "is empty, not an age recipient"},
		"a shape we can not": {element: struct{}{}, says: "is an unexpected value, not an age recipient"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := manifestDeclaring([]any{tc.element}).SupportRecipients()
			require.Error(t, err, "an unusable recipient was accepted")
			assert.Contains(t, domain.AsError(err).Message, tc.says)

			// The index, because a vendor with four recipients needs to
			// know which line to look at.
			assert.Contains(t, domain.AsError(err).Message, "recipients[0]")
		})
	}
}

// The block itself, when it is not a list of anything.
func TestADeclarationThatIsNotAListIsNamedByItsShape(t *testing.T) {
	for name, tc := range map[string]struct {
		recipients any
		says       string
	}{
		"a single value": {recipients: "age1nope", says: "is a single value, not a list of age recipients"},
		"a mapping":      {recipients: map[string]any{"primary": "age1nope"}, says: "is a mapping, not a list of age recipients"},
		"nothing at all": {recipients: nil, says: "is empty, not a list of age recipients"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := manifestDeclaring(tc.recipients).SupportRecipients()
			require.Error(t, err)
			assert.Contains(t, domain.AsError(err).Message, tc.says)
		})
	}
}

// A declaration that is usable comes back trimmed and in the vendor's order.
//
// The order is the assertion worth having: sorting them would reorder somebody
// else's list, and an operator comparing the printed recipients against the
// manifest reads them top to bottom.
func TestAUsableDeclarationSurvivesUnchanged(t *testing.T) {
	got, err := manifestDeclaring([]any{"  age1second  ", "age1first"}).SupportRecipients()
	require.NoError(t, err)
	assert.Equal(t, []string{"age1second", "age1first"}, got)
}

// No block is not a declaration of nobody, and neither is an error.
func TestAManifestWithNoSupportBlockDeclaresNothing(t *testing.T) {
	got, err := (&domain.Manifest{}).SupportRecipients()
	require.NoError(t, err, "a manifest that never mentioned support was refused")
	assert.Empty(t, got)

	// Another vendor's namespace is not ours, which is the whole point of
	// namespacing the block.
	other := &domain.Manifest{Extensions: map[string]map[string]any{
		"example.com/other": {"recipients": []any{"age1nope"}},
	}}
	got, err = other.SupportRecipients()
	require.NoError(t, err)
	assert.Empty(t, got)
}
