package domain_test

import (
	"strings"
	"testing"
	"unicode/utf8"

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

// A step's text is not all the manager's own words, and the statement travels.
//
// A failing hook contributes the last three lines of its stderr to the step's
// error, and a hook ships with the release. `lastLines` bounds it by lines,
// which is no bound at all when a line can be a megabyte -- and P4 pushes the
// document to a bucket automatically.
func TestAStepsTextCannotCarryWhateverAHookPrinted(t *testing.T) {
	record := domain.OperationRecord{
		ID: "op_1", Type: domain.OpTypeApply, Status: domain.StatusFailed,
		Steps: []domain.StepRecord{{
			ID:     "run-hook",
			Status: domain.StepFailed,
			Error: "hook \"migrate\" failed with exit code 1: " +
				strings.Repeat("A", 100_000),
		}},
	}

	stmt := domain.Attest(record, domain.AttestationInputs{})
	require.Len(t, stmt.Predicate.Steps, 1)

	got := stmt.Predicate.Steps[0].Error
	assert.LessOrEqual(t, len(got), domain.MaxAttestedText+len("… [truncated]"),
		"a hook's output reached a signed document unbounded")
	assert.Contains(t, got, "exit code 1", "the diagnosis was truncated away")
	assert.Contains(t, got, "truncated",
		"a silent truncation reads as the whole sentence")
}

// And escape sequences, for the same reason: a record that travels is read in
// terminals, in logs and in web views.
func TestAStepsTextCannotCarryAnEscapeSequence(t *testing.T) {
	record := domain.OperationRecord{
		ID: "op_1", Type: domain.OpTypeApply, Status: domain.StatusFailed,
		Steps: []domain.StepRecord{{
			ID:      "run-hook",
			Status:  domain.StepFailed,
			Message: "everything is\x1b[2J\x1b[H fine",
			Error:   "line one\nline two\ttabbed",
		}},
	}

	step := domain.Attest(record, domain.AttestationInputs{}).Predicate.Steps[0]
	assert.NotContains(t, step.Message, "\x1b",
		"an escape sequence in a signed document is a payload aimed at whoever opens it")
	assert.NotContains(t, step.Error, "\n")
	assert.NotContains(t, step.Error, "\t")
	assert.Contains(t, step.Error, "line one")
	assert.Contains(t, step.Error, "line two")
}

// The boundary of the live comparison, pinned as a **limitation rather than a
// promise**.
//
// Images are compared as a set, because the statement records repository and
// digest and not the service each was deployed to — the manifest declares
// images by name and the Compose file decides where each is used. These two
// cases therefore pass, and the reference page says so.
//
// Written down so the boundary is explicit rather than folklore, and so that
// whoever implements RFC 0025 §12 A5 is made to update the documentation: this
// test will fail the moment the comparison gets service identity, which is the
// intended signal.
func TestTheLiveComparisonIsBySetAndNotByService(t *testing.T) {
	const (
		a = "sha256:aa00000000000000000000000000000000000000000000000000000000000000"
		b = "sha256:bb00000000000000000000000000000000000000000000000000000000000000"
	)

	t.Run("two services that swap images with each other", func(t *testing.T) {
		stmt := domain.Statement{Predicate: domain.Predicate{Images: []domain.AttestedImage{
			{Ref: "reg/app", Digest: a}, {Ref: "reg/db", Digest: b},
		}}}
		assert.Empty(t, domain.CompareToLive(stmt, []domain.LiveImage{
			{Service: "app", Ref: "reg/db", Digest: b},
			{Service: "db", Ref: "reg/app", Digest: a},
		}), "if this now reports, the comparison gained service identity -- update the docs")
	})

	t.Run("a stopped service whose digest another still runs", func(t *testing.T) {
		stmt := domain.Statement{Predicate: domain.Predicate{Images: []domain.AttestedImage{
			{Ref: "reg/app", Digest: a},
		}}}
		assert.Empty(t, domain.CompareToLive(stmt, []domain.LiveImage{
			{Service: "worker", Ref: "reg/app", Digest: a},
		}), "if this now reports, the comparison gained service identity -- update the docs")
	})

	// And the cases it does catch, beside them, so the pin cannot be read
	// as "the comparison establishes nothing".
	t.Run("a digest nobody attested is still caught", func(t *testing.T) {
		stmt := domain.Statement{Predicate: domain.Predicate{Images: []domain.AttestedImage{
			{Ref: "reg/app", Digest: a},
		}}}
		assert.NotEmpty(t, domain.CompareToLive(stmt, []domain.LiveImage{
			{Service: "app", Ref: "reg/app", Digest: b},
		}))
	})
}

// The bound's own edges, decided rather than inherited.
//
// The empty case and the rune boundary are where a truncating helper goes
// wrong: a cut through the middle of a multi-byte rune produces a document that
// is no longer valid UTF-8, in a format whose whole purpose is to be readable
// by something that is not this program.
func TestTheTextBoundIsDecidedAtItsEdges(t *testing.T) {
	step := func(msg string) string {
		return domain.Attest(domain.OperationRecord{
			ID: "op", Steps: []domain.StepRecord{{Message: msg}},
		}, domain.AttestationInputs{}).Predicate.Steps[0].Message
	}

	assert.Empty(t, step(""))
	assert.Empty(t, step("\x00\x01\x1b"), "a string of nothing but control characters")

	exact := strings.Repeat("a", domain.MaxAttestedText)
	assert.Equal(t, exact, step(exact), "a string exactly at the bound was truncated")
	assert.Contains(t, step(exact+"b"), "truncated", "one byte over was not")

	// Two bytes per rune, so the limit falls between them.
	wide := step(strings.Repeat("é", 400))
	assert.True(t, utf8.ValidString(wide), "the truncation cut through a rune")
	assert.Contains(t, wide, "truncated")
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
