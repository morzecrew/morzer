package views_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	_ "github.com/morzecrew/morzer/internal/ui/views"
)

// What `support inspect` says about a signature (RFC 0024 P4b).
//
// Found by coverage rather than by sabotage: every outcome the reader can reach
// was rendered by a function no test called, in the one half of this feature
// somebody takes a verdict from. A view that printed "verified" for an
// unverifiable signature would have been caught by nothing here, and by nothing
// in the operation either -- the report would have been right and the sentence
// wrong.
//
// So every combination is rendered and pinned. What each assertion protects is
// not the wording but the *distinction*: the four outcomes must stay four
// different sentences, and each must name what it was checked against.

func TestEverySignatureOutcomeRendersDifferently(t *testing.T) {
	base := ops.SupportInspectReport{
		Product:        "demo",
		InstallationID: "op_01",
		ManagerVersion: "1.0.0",
		Entries:        []ops.SupportEntry{{Name: "meta.json", Bytes: 10}},
	}

	seen := map[string]string{}
	for _, c := range []struct {
		name string
		sig  ops.SupportSignature
		want []string
	}{
		{
			name: "verified against the installation",
			sig: ops.SupportSignature{
				Present: true,
				Source:  ops.SignatureSourceInstallation,
				Result: domain.SignatureResult{
					Outcome: domain.SignedByCurrentKey, Key: "RWQcurrent",
				},
			},
			want: []string{"verified", "installation's recorded keys", "RWQcurrent"},
		},
		{
			name: "verified against a named key",
			sig: ops.SupportSignature{
				Present: true,
				Source:  ops.SignatureSourceExpectedKey,
				Result: domain.SignatureResult{
					Outcome: domain.SignedByCurrentKey, Key: "RWQnamed",
				},
			},
			want: []string{"verified", "the key you named", "RWQnamed"},
		},
		{
			name: "verified against the key on disk",
			sig: ops.SupportSignature{
				Present: true,
				Source:  ops.SignatureSourceMachineKey,
				Result: domain.SignatureResult{
					Outcome: domain.SignedByCurrentKey, Key: "RWQdisk",
				},
			},
			// The weaker claim has to say it is weaker: state does not
			// back this one, and a reader must not take it for the row
			// above.
			want: []string{"verified", "on this machine's disk", "does not record"},
		},
		{
			name: "a retired key is provenance, not validity",
			sig: ops.SupportSignature{
				Present: true,
				Source:  ops.SignatureSourceInstallation,
				Result: domain.SignatureResult{
					Outcome:   domain.SignedByPredecessor,
					Key:       "RWQold",
					RetiredAt: domain.NewTime(time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)),
					Reason:    domain.RetiredByRebuild,
				},
			},
			want: []string{"RETIRED", "where the archive came from", "back-dates"},
		},
		{
			name: "does not verify",
			sig: ops.SupportSignature{
				Present:    true,
				Source:     ops.SignatureSourceInstallation,
				Result:     domain.SignatureResult{Outcome: domain.Unverifiable},
				ClaimedKey: "RWQclaimed",
			},
			want: []string{"does NOT verify", "a claim and not a check", "RWQclaimed"},
		},
		{
			name: "present and unchecked",
			sig: ops.SupportSignature{
				Present:    true,
				Source:     ops.SignatureSourceNone,
				ClaimedKey: "RWQclaimed",
			},
			want: []string{"NOT checked", "get that key from the operator", "--key"},
		},
		{
			name: "no signature at all",
			sig:  ops.SupportSignature{Present: false},
			want: []string{"no signature beside this archive"},
		},
		{
			name: "signature did not travel",
			sig: ops.SupportSignature{
				Present: false, ClaimedKey: "RWQclaimed",
			},
			want: []string{"did not travel with it", "RWQclaimed"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := base
			r.Signature = c.sig
			out := render(t, 100, r)

			// Flattened, because the document wraps at the terminal
			// measure and a sentence split across two lines is the
			// same sentence to a reader.
			flat := flatten(out)
			for _, want := range c.want {
				assert.Containsf(t, flat, want,
					"the %s rendering does not say %q:\n%s", c.name, want, out)
			}

			// Two outcomes that print the same sentence are one outcome
			// as far as a reader is concerned, which would undo the
			// whole point of reporting them separately.
			key := flat
			if prev, dup := seen[key]; dup {
				t.Fatalf("%q renders identically to %q", c.name, prev)
			}
			seen[key] = c.name
		})
	}
}

// A verified signature must never be printed without naming what it was checked
// against, because the reader supplies the missing half themselves and supplies
// it generously.
func TestAVerdictAlwaysNamesItsAnchor(t *testing.T) {
	for _, source := range []ops.SupportSignatureSource{
		ops.SignatureSourceInstallation,
		ops.SignatureSourceExpectedKey,
		ops.SignatureSourceMachineKey,
	} {
		out := render(t, 100, ops.SupportInspectReport{
			Signature: ops.SupportSignature{
				Present: true,
				Source:  source,
				Result: domain.SignatureResult{
					Outcome: domain.SignedByCurrentKey, Key: "RWQkey",
				},
			},
		})
		require.Containsf(t, flatten(out), "against",
			"a %s verdict does not say what it was checked against:\n%s", source, out)
	}
}

// An archive that cannot be listed says so before the empty table, not after.
func TestAnUnreadableArchiveSaysWhyBeforeTheEmptyTable(t *testing.T) {
	out := flatten(render(t, 100, ops.SupportInspectReport{
		Unreadable: "this archive is encrypted and no identity was given to read it",
		Signature: ops.SupportSignature{
			Present: true,
			Source:  ops.SignatureSourceExpectedKey,
			Result: domain.SignatureResult{
				Outcome: domain.SignedByCurrentKey, Key: "RWQkey",
			},
		},
	}))
	require.Contains(t, out, "the contents could not be read")
	assert.Less(t, strings.Index(out, "could not be read"), strings.Index(out, "nothing in it"),
		"the empty table came before the reason it is empty")
}

// The index is counted out loud, because `support bundle` counts it as a
// component and this counts what it enumerates -- one archive, two commands,
// and a difference of one that would otherwise read as drift.
func TestTheIndexIsAccountedForSeparately(t *testing.T) {
	out := render(t, 100, ops.SupportInspectReport{
		Entries:    []ops.SupportEntry{{Name: "journal.jsonl", Bytes: 2048}},
		TotalBytes: 2048,
		IndexBytes: 1024,
	})
	assert.Contains(t, flatten(out), "1 component(s)")
	assert.Contains(t, flatten(out), "plus the index (1.0 KiB)")
}
