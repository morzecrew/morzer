package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

func at(s string) domain.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return domain.NewTime(t)
}

// A rebuilt machine is a different signer, and the whole value of recording the
// predecessor is that a verifier can tell "signed by a predecessor of this
// installation" from "unknown signer". These pin the transform that makes that
// possible.

func TestARebuiltMachineRecordsItsPredecessorAndClaimsNoKey(t *testing.T) {
	before := domain.Installation{
		ID:      "inst-1",
		Signing: domain.Signing{PublicKey: "RWQoriginal"},
	}

	after := before.SucceedSigning(at("2026-08-13T09:14:00Z"), domain.RetiredByRebuild)

	require.Len(t, after.Signing.PreviousKeys, 1)
	assert.Equal(t, "RWQoriginal", after.Signing.PreviousKeys[0].Key)
	assert.Equal(t, domain.RetiredByRebuild, after.Signing.PreviousKeys[0].Reason)
	assert.Equal(t, at("2026-08-13T09:14:00Z"), after.Signing.PreviousKeys[0].RetiredAt)

	// The load-bearing half. Leaving the predecessor's key here would
	// produce a machine claiming to sign with a key it does not hold --
	// which is exactly the disagreement doctor refuses, manufactured by the
	// import path itself.
	assert.Empty(t, after.Signing.PublicKey,
		"the imported installation still claims the dead machine's key")
}

func TestSuccessionKeepsTheWholeChainNewestFirst(t *testing.T) {
	before := domain.Installation{
		Signing: domain.Signing{
			PublicKey: "RWQsecond",
			PreviousKeys: []domain.RetiredKey{
				{Key: "RWQfirst", Reason: domain.RetiredByRebuild},
			},
		},
	}

	after := before.SucceedSigning(at("2026-08-13T09:14:00Z"), domain.RetiredByRebuild)

	require.Len(t, after.Signing.PreviousKeys, 2)
	assert.Equal(t, "RWQsecond", after.Signing.PreviousKeys[0].Key, "newest first")
	assert.Equal(t, "RWQfirst", after.Signing.PreviousKeys[1].Key)
}

// A machine rebuilt from an export that never signed has no key to retire. The
// case matters because it is the normal one for every installation created
// before schema 6, and an empty predecessor entry would be a row a verifier has
// to skip and an operator has to interpret.
func TestSuccessionFromAMachineThatNeverSignedRecordsNothing(t *testing.T) {
	before := domain.Installation{Signing: domain.Signing{}}

	after := before.SucceedSigning(at("2026-08-13T09:14:00Z"), domain.RetiredByRebuild)

	assert.Empty(t, after.Signing.PreviousKeys)
	assert.False(t, after.Signing.HasKey())
}

// And it must not drop a chain it inherited. A machine rebuilt twice, whose
// middle incarnation never signed, still has predecessors worth keeping -- and
// the early return in SucceedSigning is exactly where that would be lost.
func TestSuccessionThroughAMachineThatNeverSignedKeepsTheOlderChain(t *testing.T) {
	before := domain.Installation{
		Signing: domain.Signing{
			PreviousKeys: []domain.RetiredKey{{Key: "RWQancestor"}},
		},
	}

	after := before.SucceedSigning(at("2026-08-13T09:14:00Z"), domain.RetiredByRebuild)

	require.Len(t, after.Signing.PreviousKeys, 1)
	assert.Equal(t, "RWQancestor", after.Signing.PreviousKeys[0].Key)
}

// The no-key path detaches too.
//
// This test exists because a sabotage found the gap: deleting the defensive
// copy from that branch left every other test in this file passing. The
// with-key path builds a fresh slice on its way to prepending, so it detaches
// as a side effect of doing its job -- the early return has nothing else going
// on, and is the only place the aliasing can actually happen.
func TestSuccessionDetachesTheChainEvenWhenThereIsNoKeyToRetire(t *testing.T) {
	before := domain.Installation{
		Signing: domain.Signing{
			PreviousKeys: []domain.RetiredKey{{Key: "RWQancestor"}},
		},
	}

	after := before.SucceedSigning(at("2026-08-13T09:14:00Z"), domain.RetiredByRebuild)
	require.Len(t, after.Signing.PreviousKeys, 1)
	after.Signing.PreviousKeys[0].Key = "mutated"

	assert.Equal(t, "RWQancestor", before.Signing.PreviousKeys[0].Key,
		"the returned chain shares its array with the installation it came from")
}

// The transform is value-to-value, and a shared backing array is that promise
// being false in the one case nobody looks at.
func TestSuccessionDoesNotMutateTheInstallationItWasGiven(t *testing.T) {
	before := domain.Installation{
		Signing: domain.Signing{
			PublicKey:    "RWQcurrent",
			PreviousKeys: []domain.RetiredKey{{Key: "RWQold"}},
		},
	}

	after := before.SucceedSigning(at("2026-08-13T09:14:00Z"), domain.RetiredByRotation)
	after.Signing.PreviousKeys[0].Key = "mutated"

	assert.Equal(t, "RWQcurrent", before.Signing.PublicKey)
	require.Len(t, before.Signing.PreviousKeys, 1)
	assert.Equal(t, "RWQold", before.Signing.PreviousKeys[0].Key,
		"mutating the result reached into the input")
}

// retired_at is history for an operator, not a check a verifier may enforce
// (RFC 0028 decision 11). Nothing in the domain may grow a comparison against
// it: the date would come from the artifact, and the artifact is what a forger
// writes.
//
// Written as a test so a later reader who assumes the timestamp is enforced
// finds out here rather than from an incident.
func TestARetiredKeyRecordsWhenAndNeverDecidesWhether(t *testing.T) {
	key := domain.RetiredKey{
		Key:       "RWQold",
		RetiredAt: at("2026-08-13T09:14:00Z"),
		Reason:    domain.RetiredByRotation,
	}

	// The type carries no predicate over its own timestamp, and adding one
	// is the change this test exists to make somebody justify.
	assert.Equal(t, domain.RetiredByRotation, key.Reason)
	assert.True(t, key.Reason.Valid())
	assert.False(t, domain.RetirementReason("expired").Valid())
}

// An installation whose signing block is empty is legitimate, not invalid: it
// is every machine that reached schema 6 by migration. Validation must not have
// learned to require a key.
func TestAnInstallationWithNoSigningKeyIsValid(t *testing.T) {
	inst := domain.Installation{
		SchemaVersion: domain.InstallationSchemaVersion,
		ID:            "inst-1",
		Product:       "demo",
	}
	require.NoError(t, inst.Validate())
}

// A recorded predecessor with no key names a signer nobody can check against.
func TestARecordedPredecessorNeedsItsKey(t *testing.T) {
	inst := domain.Installation{
		SchemaVersion: domain.InstallationSchemaVersion,
		ID:            "inst-1",
		Product:       "demo",
		Signing: domain.Signing{
			PreviousKeys: []domain.RetiredKey{{Reason: domain.RetiredByRebuild}},
		},
	}

	err := inst.Validate()
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Message, "signing.previous_keys[0].key")
}

func TestAnUnknownRetirementReasonIsRefused(t *testing.T) {
	inst := domain.Installation{
		SchemaVersion: domain.InstallationSchemaVersion,
		ID:            "inst-1",
		Product:       "demo",
		Signing: domain.Signing{
			PreviousKeys: []domain.RetiredKey{{Key: "RWQold", Reason: "expired"}},
		},
	}

	err := inst.Validate()
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Message, "signing.previous_keys[0].reason")
}
