package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

// The roster is the trust anchor, so every refusal here is one an operator
// meets when they write the file -- the only moment it is cheap to fix. A
// roster that parses and is wrong reports an installation absent forever, which
// is the exact failure it was added to detect, arriving as a false positive
// that trains somebody to ignore the column.

func validRoster() domain.FleetRoster {
	return domain.FleetRoster{
		Schema: domain.FleetRosterSchemaVersion,
		Installations: []domain.FleetRosterEntry{
			{Product: "demo", ID: "inst_01A", PublicKey: "RWQfaKe0000000000000000000000000000000000000000000000"},
		},
	}
}

func TestARosterValidatesWhatItCanCheck(t *testing.T) {
	require.NoError(t, validRoster().Validate())

	cases := map[string]struct {
		roster domain.FleetRoster
		says   string
	}{
		"no schema": {
			roster: domain.FleetRoster{Installations: validRoster().Installations},
			says:   "no schema version",
		},
		"a newer schema": {
			roster: domain.FleetRoster{
				Schema:        domain.FleetRosterSchemaVersion + 1,
				Installations: validRoster().Installations,
			},
			says: "newer manager",
		},
		"nobody": {
			roster: domain.FleetRoster{Schema: domain.FleetRosterSchemaVersion},
			says:   "names no installations",
		},
		"a product no key could be built from": {
			roster: domain.FleetRoster{
				Schema: domain.FleetRosterSchemaVersion,
				Installations: []domain.FleetRosterEntry{
					{Product: "../etc", ID: "inst_01A"},
				},
			},
			says: "is not an installation",
		},
		"an id no key could be built from": {
			roster: domain.FleetRoster{
				Schema: domain.FleetRosterSchemaVersion,
				Installations: []domain.FleetRosterEntry{
					{Product: "demo", ID: "../../etc/passwd"},
				},
			},
			says: "is not an installation",
		},
		"one installation twice": {
			roster: domain.FleetRoster{
				Schema: domain.FleetRosterSchemaVersion,
				Installations: []domain.FleetRosterEntry{
					{Product: "demo", ID: "inst_01A", PublicKey: "RWQone"},
					{Product: "demo", ID: "inst_01A", PublicKey: "RWQtwo"},
				},
			},
			says: "both name demo/inst_01A",
		},
		"a whole public-key file pasted in": {
			roster: domain.FleetRoster{
				Schema: domain.FleetRosterSchemaVersion,
				Installations: []domain.FleetRosterEntry{
					{
						Product:   "demo",
						ID:        "inst_01A",
						PublicKey: "untrusted comment: minisign public key\nRWQfaKe000",
					},
				},
			},
			says: "not usable",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := tc.roster.Validate()
			require.Error(t, err, "a roster that cannot do its job was accepted")
			assert.Contains(t, domain.AsError(err).Message, tc.says)
		})
	}
}

// A roster entry with no key is a roster entry, not a refusal.
//
// The fail-closed reading would require one, and it would make absence
// reporting -- the half an operator wants first -- unavailable until twelve
// public keys have been collected by hand. The reader says what it cannot do
// instead, which is the discipline this feature follows everywhere else.
func TestARosterEntryWithoutAKeyIsAllowedAndNamed(t *testing.T) {
	roster := domain.FleetRoster{
		Schema: domain.FleetRosterSchemaVersion,
		Installations: []domain.FleetRosterEntry{
			{Product: "demo", ID: "inst_01B"},
			{Product: "demo", ID: "inst_01A", PublicKey: "RWQfaKe"},
			{Product: "web", ID: "inst_01C", PublicKey: "   "},
		},
	}
	require.NoError(t, roster.Validate())
	assert.Equal(t, []string{"demo/inst_01B", "web/inst_01C"}, roster.Unkeyed(),
		"an entry binding nothing was not named, so the reader cannot say it")
}

// The zero value is "no roster", unambiguously.
//
// Validate refuses one naming nobody, so an empty file is an operator's mistake
// rather than a fleet of nobody -- which is what makes this test's premise hold
// and what stops a typo silently turning off both answers a roster buys.
func TestAnAbsentRosterIsDistinguishableFromAnEmptyOne(t *testing.T) {
	assert.False(t, domain.FleetRoster{}.Given())
	assert.True(t, validRoster().Given())

	empty := domain.FleetRoster{Schema: domain.FleetRosterSchemaVersion}
	require.Error(t, empty.Validate(),
		"an empty roster parsed as a fleet of nobody, which reads as no roster at all")
}

func TestARosterAnswersForOneInstallation(t *testing.T) {
	roster := validRoster()

	entry, ok := roster.Entry("demo", "inst_01A")
	require.True(t, ok)
	assert.NotEmpty(t, entry.PublicKey)

	_, ok = roster.Entry("demo", "inst_01B")
	assert.False(t, ok, "the roster answered for an installation it does not name")

	// The product is part of the identity, not decoration: two products may
	// legitimately use the same installation id, and a lookup that matched
	// on the id alone would hand one machine's key to the other.
	_, ok = roster.Entry("web", "inst_01A")
	assert.False(t, ok)
}
