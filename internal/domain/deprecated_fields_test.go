package domain_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

// Field-level deprecation, and what is left of it.
//
// `runtime:` was this mechanism's only member and stopped being read in 0.3.0
// (RFC 0023 decision 23), so what these tests pin is a registry with nothing in
// it and a machinery that still works. An empty registry is an untested one
// unless something drives it, which is what the synthetic deprecation below is
// for.

func legacyManifest() domain.Manifest {
	return domain.Manifest{
		APIVersion: domain.APIVersionV1Alpha1,
		Kind:       domain.KindApplicationRelease,
		Metadata:   domain.Metadata{Name: "demo"},
		Runtime:    domain.RuntimeSpec{Project: "demo", Files: []string{"compose.yaml"}},
	}
}

func currentManifest() domain.Manifest {
	return domain.Manifest{
		APIVersion: domain.APIVersionV1Alpha1,
		Kind:       domain.KindApplicationRelease,
		Metadata:   domain.Metadata{Name: "demo"},
		Runtimes: domain.Runtimes{"compose": {
			Files:   []string{"compose.yaml"},
			Options: map[string]string{"project": "demo"},
		}},
	}
}

// The legacy block is refused, not deprecated, and the difference is the whole
// of decision 23: a deprecation offers a grace period, and there is none to
// offer, because no released manager ever read `runtimes:`.
func TestTheLegacyRuntimeBlockIsRefusedRatherThanDeprecated(t *testing.T) {
	m := legacyManifest()

	assert.Empty(t, m.DeprecatedFields(),
		"reporting `runtime:` as deprecated would promise a grace period the "+
			"loader will not honour")

	err := m.Validate()
	require.Error(t, err, "a manifest carrying `runtime:` is refused")
	assert.Contains(t, err.Error(), "is no longer read")
}

func TestNoManifestReportsADeprecatedField(t *testing.T) {
	for name, m := range map[string]domain.Manifest{
		"the current spelling": currentManifest(),
		"the legacy block":     legacyManifest(),
		"the zero manifest":    {},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Empty(t, m.DeprecatedFields(),
				"nothing in this manifest is deprecated; `runtime:` is gone, "+
					"not on its way out")
		})
	}
}

// The synthetic member. Without it the mechanism has no member and no test, so
// the next field to need a deprecation would inherit machinery nobody has run
// since the day its only real member left.
//
// It drives FieldDeprecation directly rather than through a Manifest, because
// there is no manifest that produces one -- which is exactly the gap.
func TestTheDeprecationMachineryStillRendersASentenceThatCanBeActedOn(t *testing.T) {
	f := domain.FieldDeprecation{
		Field:       "someday",
		Replacement: "`somewhere.else`",
	}

	// The three things the sentence has to carry. A deprecation missing any
	// one of them cannot be acted on: what, when, and what instead.
	msg := f.Message()
	assert.Contains(t, msg, "someday",
		"spelled the way the vendor spelled it, or they go looking for a "+
			"field that is not there")
	assert.Contains(t, msg, domain.FieldRemovalRelease,
		"a deprecation with no removal release is a word in a document")
	assert.Contains(t, msg, "somewhere.else",
		"naming no successor makes it a complaint rather than an instruction")

	// And it is a whole sentence, opening with its own subject.
	//
	// This is what the publisher relies on to print it bare. A caller that
	// prepends one composes "this bundle uses `someday` is deprecated" --
	// two verbs in one clause, which is the defect wave 32 shipped and
	// fixed. That defect is unreachable now that no manifest reports a
	// deprecated field, so this is where the invariant lives: at the
	// sentence, which is the thing that has to stay true for the next
	// caller rather than for the one that has gone.
	assert.True(t, strings.HasPrefix(msg, "`someday`"),
		"the message must open with the field, so a publisher prints it bare: %q", msg)
}

// The removal release is machinery configuration now rather than a promise
// about `runtime:`, and it is still pinned: a mechanism whose only parameter
// can be blanked by any later edit reports deprecations that name no date.
//
// Deliberately no longer asserted to fall after RuntimesMinManagerVersion.
// That coupling was about one field -- the old spelling could not stop being
// read before the new one existed -- and with that field gone the two
// constants describe unrelated things.
func TestTheRemovalReleaseIsAUsableVersion(t *testing.T) {
	require.False(t, strings.TrimSpace(domain.FieldRemovalRelease) == "",
		"a deprecation without a removal release is a word in a document")

	_, err := domain.ParseVersion(domain.FieldRemovalRelease)
	require.NoError(t, err,
		"the removal release is rendered into a vendor-facing sentence and "+
			"compared against manager versions, so it must parse as one")
}
