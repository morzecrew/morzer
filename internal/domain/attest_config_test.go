package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

// The configuration half of `--against-live`, and the two silences it keeps.
//
// Both directions matter for the same reason the image comparison's do: a
// finding on an ordinary machine trains a reader to ignore the output, and a
// missed edit is what the check exists for.

const testSalt = "0123456789abcdef0123456789abcdef"

func configured(salt string, rendered map[string][]byte) domain.Statement {
	return domain.Statement{
		Predicate: domain.Predicate{
			Config: domain.AttestedConfig{
				RenderedDigest: domain.SaltedConfigDigest(salt, domain.CanonicalConfig(rendered)),
			},
		},
	}
}

func TestConfigurationThatHasNotChangedReportsNothing(t *testing.T) {
	rendered := map[string][]byte{"/etc/demo/app.yaml": []byte("port: 8080\n")}
	assert.Empty(t, domain.CompareConfigToLive(configured(testSalt, rendered), testSalt, rendered))
}

func TestAnEditedConfigurationIsAMismatch(t *testing.T) {
	attested := map[string][]byte{"/etc/demo/app.yaml": []byte("port: 8080\n")}
	edited := map[string][]byte{"/etc/demo/app.yaml": []byte("port: 8080\ndebug: true\n")}

	found := domain.CompareConfigToLive(configured(testSalt, attested), testSalt, edited)
	require.Len(t, found, 1)
	assert.Equal(t, "config", found[0].Kind)
	assert.Contains(t, found[0].Detail, "does not match")
}

// A file added beside the attested ones changes the answer too. The digest is
// over the whole set by construction, so this is what stops an edit from being
// hidden by splitting it across a second file.
func TestAnExtraConfigurationFileIsAMismatch(t *testing.T) {
	attested := map[string][]byte{"/etc/demo/app.yaml": []byte("port: 8080\n")}
	extra := map[string][]byte{
		"/etc/demo/app.yaml":   []byte("port: 8080\n"),
		"/etc/demo/extra.yaml": []byte("debug: true\n"),
	}
	assert.Len(t, domain.CompareConfigToLive(configured(testSalt, attested), testSalt, extra), 1)
}

// Nothing on disk is a different sentence from "it differs", because it sends
// an operator somewhere else entirely.
func TestConfigurationThatIsGoneSaysSo(t *testing.T) {
	attested := map[string][]byte{"/etc/demo/app.yaml": []byte("port: 8080\n")}

	found := domain.CompareConfigToLive(configured(testSalt, attested), testSalt, nil)
	require.Len(t, found, 1)
	assert.Contains(t, found[0].Detail, "not on disk")
	assert.Empty(t, found[0].Actual)
}

// The two silences. A statement carrying no digest -- an operation that
// rendered nothing, or a machine that predates the salt -- and an installation
// with no salt to re-derive one. Reporting drift because the *evidence* is
// absent would put a finding on every machine upgraded into schema 6.
func TestNothingToCompareIsNotAFinding(t *testing.T) {
	rendered := map[string][]byte{"/etc/demo/app.yaml": []byte("port: 8080\n")}
	other := map[string][]byte{"/etc/demo/app.yaml": []byte("port: 9090\n")}

	t.Run("the statement carries no digest", func(t *testing.T) {
		assert.Empty(t, domain.CompareConfigToLive(domain.Statement{}, testSalt, other))
	})

	t.Run("the installation has no salt", func(t *testing.T) {
		assert.Empty(t, domain.CompareConfigToLive(configured(testSalt, rendered), "", other))
	})

	t.Run("a machine with no salt attested no digest", func(t *testing.T) {
		// The unsalted digest is refused at emission, so this is what
		// such a statement actually looks like.
		assert.Empty(t, domain.CompareConfigToLive(configured("", rendered), "", other))
	})
}

// The encoding is injective, which is what stops two different configurations
// sharing a digest. A path holding the delimiter is the case a delimited
// encoding gets wrong.
func TestTheCanonicalEncodingCannotBeConfused(t *testing.T) {
	first := map[string][]byte{"/etc/a": []byte("1:/etc/b1:x")}
	second := map[string][]byte{"/etc/a": []byte("1"), "/etc/b": []byte("x")}
	assert.NotEqual(t,
		string(domain.CanonicalConfig(first)),
		string(domain.CanonicalConfig(second)))

	// And it does not depend on map iteration order: identical input
	// digests identically however it was built, or a drift detector reports
	// drift on every second run.
	a := map[string][]byte{"/etc/a": []byte("1"), "/etc/b": []byte("2"), "/etc/c": []byte("3")}
	b := map[string][]byte{"/etc/c": []byte("3"), "/etc/b": []byte("2"), "/etc/a": []byte("1")}
	assert.Equal(t, string(domain.CanonicalConfig(a)), string(domain.CanonicalConfig(b)))

	assert.Nil(t, domain.CanonicalConfig(nil))
}
