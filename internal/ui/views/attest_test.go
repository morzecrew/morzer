package views_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ui/views"
)

// What an operator actually sees for each verification outcome.
//
// These rows are the finding. `attest verify` exits non-zero on a problem, but
// the row is what says *which* statement and *why* -- and the four outcomes are
// deliberately not one axis, so a renderer that collapsed two of them would
// undo the distinction the domain draws (RFC 0028 decision 10) without failing
// any test of the domain.
func TestEachSignatureOutcomeRendersAsItsOwnVerdict(t *testing.T) {
	verdict := func(s domain.SignatureResult) views.StatementVerdict {
		return views.StatementVerdict{
			File: "/var/lib/demo/attestations/op_01K2Z9.json",
			Kind: "apply", Outcome: "succeeded", Signature: s,
		}
	}

	t.Run("a key this machine cannot account for is a failure", func(t *testing.T) {
		out := render(t, 100, views.Verification{
			Statements: []views.StatementVerdict{
				verdict(domain.SignatureResult{Outcome: domain.Unverifiable}),
			},
			Problems: 1,
		})
		assert.Contains(t, flatten(out), "1 problem(s)")
		assert.Contains(t, out, "no key this installation knows about signed it")
	})

	t.Run("a predecessor's signature is provenance, not a failure", func(t *testing.T) {
		out := render(t, 100, views.Verification{
			Statements: []views.StatementVerdict{
				verdict(domain.SignatureResult{
					Outcome: domain.SignedByPredecessor,
					Reason:  domain.RetiredByRebuild,
				}),
			},
		})
		assert.Contains(t, out, "signed by a predecessor")
		assert.Contains(t, out, string(domain.RetiredByRebuild),
			"the row does not say why the key was retired")
		assert.Contains(t, flatten(out), "0 problem(s)")
	})

	t.Run("unsigned is distinct from unverifiable", func(t *testing.T) {
		out := render(t, 100, views.Verification{
			Statements: []views.StatementVerdict{
				verdict(domain.SignatureResult{Outcome: domain.Unsigned}),
			},
		})
		assert.Contains(t, out, "unsigned")
		assert.NotContains(t, out, "no key this installation knows about",
			"an unsigned record was reported as one nothing accounts for; "+
				"the remedies are entirely different")
	})

	t.Run("a live mismatch names what disagrees", func(t *testing.T) {
		out := render(t, 120, views.Verification{
			Statements:  []views.StatementVerdict{verdict(domain.SignatureResult{Outcome: domain.SignedByCurrentKey})},
			Live:        []domain.LiveMismatch{{Kind: "config", Detail: "the rendered configuration on disk does not match"}},
			LiveChecked: true,
			Problems:    1,
		})
		assert.Contains(t, out, "live: config")
		assert.Contains(t, out, "does not match")
	})

	t.Run("a clean live check says so rather than staying silent", func(t *testing.T) {
		out := render(t, 100, views.Verification{
			Statements:  []views.StatementVerdict{verdict(domain.SignatureResult{Outcome: domain.SignedByCurrentKey})},
			LiveChecked: true,
			LiveAgainst: "op_01K2Z9",
		})
		assert.Contains(t, out, "op_01K2Z9",
			"a comparison that found nothing must not be indistinguishable from one that did not run")
	})
}

// The log's `signed` column says a signature is *there*, never that it checks
// out -- the overclaim the whole format is built to refuse.
func TestTheLogSaysSignedAndNeverValid(t *testing.T) {
	out := render(t, 120, views.AttestationLog{Entries: []views.LogRow{
		{Operation: "op_01K2ZB", Kind: "update", Outcome: "succeeded",
			From: "1.2.0", To: "1.3.0", Signed: true},
		{Operation: "op_01K2Z9", Kind: "apply", Outcome: "succeeded", To: "1.2.0"},
	}})

	flat := flatten(out)
	assert.Contains(t, flat, "1.2.0 -> 1.3.0", "the release column does not show the move")
	assert.Contains(t, flat, "signed")
	assert.Contains(t, flat, "unsigned")
	assert.NotContains(t, strings.ToLower(flat), "valid",
		"the listing claimed validity, which only `attest verify` establishes")
}
