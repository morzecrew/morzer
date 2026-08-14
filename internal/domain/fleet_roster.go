package domain

import (
	"sort"
	"strings"
)

// The roster: the file that says which installations exist (RFC 0026 P3).
//
// It answers two questions the target itself structurally cannot, and they turn
// out to be the same question asked twice.
//
// **Which installations are absent.** Listing a prefix shows what is there,
// which is precisely the population that is fine. An object that was never
// written cannot announce itself, so the only way to notice a machine that
// stopped publishing three weeks ago is to hold a list of the machines that
// should have.
//
// **Which key may speak for an installation.** A row carries the public half of
// the key that signed it, and checking the signature against that key
// authenticates nothing: rows from many machines share one target, every one of
// those machines holds a valid signing key, and a machine overwriting its
// neighbour's row rewrites payload, key and signature together. The result
// verifies perfectly against itself. So the anchor has to come from outside the
// row, and the roster is the only outside there is (decision 6b).
//
// One file, because the two are one fact: *these installations, signing with
// these keys, are the fleet*. A reader without it says both of those things are
// unknown, in one breath, because they have one cause.

// FleetRosterSchemaVersion versions the roster document.
//
// Stated in the file for the same reason the row states its own: a reader that
// meets a version it does not know must be able to say so, and it cannot say
// that about a document which does not claim a version.
const FleetRosterSchemaVersion = 1

// FleetRoster is the expected population of a fleet.
type FleetRoster struct {
	Schema int `yaml:"schema" json:"schema"`

	// Installations is who is expected, and what each one signs with.
	Installations []FleetRosterEntry `yaml:"installations" json:"installations"`
}

// FleetRosterEntry binds one installation id to the key it signs with.
type FleetRosterEntry struct {
	Product string `yaml:"product" json:"product"`
	ID      string `yaml:"id" json:"id"`

	// PublicKey is the minisign public key line this installation signs
	// with, obtained out of band -- `morzer installation describe` prints
	// it on the machine itself.
	//
	// Optional, and the choice is deliberate. Requiring it would be the
	// fail-closed reading, and it would make absence reporting -- the half
	// of this file an operator wants first -- unavailable until twelve
	// public keys have been collected by hand. So an entry without a key
	// still says the installation is expected, and the reader states, on
	// every run, that rows from it cannot be authenticated. That is the
	// discipline this feature already follows everywhere else: a reader
	// that says what it cannot do beats one that refuses to run.
	PublicKey string `yaml:"key,omitempty" json:"key,omitempty"`
}

// Given reports whether a roster was supplied at all.
//
// The zero value is "no roster", which is unambiguous because Validate refuses
// one naming nobody: an empty file is an operator's mistake, not a fleet of
// nobody, and reading it as the latter would silently turn off both of the
// answers they passed it for.
func (r FleetRoster) Given() bool { return len(r.Installations) > 0 }

// Entry returns what the roster says about one installation.
func (r FleetRoster) Entry(product, installationID string) (FleetRosterEntry, bool) {
	for _, e := range r.Installations {
		if e.Product == product && e.ID == installationID {
			return e, true
		}
	}
	return FleetRosterEntry{}, false
}

// Keys lists the object keys this roster expects a row at, sorted.
//
// Built through FleetKey rather than formatted here, so the roster and the
// publisher cannot disagree about where a row lives -- and so a roster entry
// that could never match any key is impossible by the time this is called,
// because Validate refuses it.
func (r FleetRoster) Keys() []string {
	out := make([]string, 0, len(r.Installations))
	for _, e := range r.Installations {
		key, err := FleetKey(e.Product, e.ID)
		if err != nil {
			continue
		}
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// Unkeyed lists the entries that bind no public key, sorted.
//
// The reader turns this into a statement it prints: an operator whose roster is
// half-keyed is authenticating half a fleet, and a table that showed the other
// half as merely "signed" without saying why would be the complete-looking
// table this design exists to refuse.
func (r FleetRoster) Unkeyed() []string {
	var out []string
	for _, e := range r.Installations {
		if strings.TrimSpace(e.PublicKey) == "" {
			out = append(out, e.Product+"/"+e.ID)
		}
	}
	sort.Strings(out)
	return out
}

// Validate refuses a roster that cannot do its job.
//
// Every refusal here is one an operator meets when they write the file, which
// is the only moment they can fix it cheaply. The alternative is a roster that
// parses, runs, and reports an installation absent forever because its id has a
// typo in it -- the exact failure the roster was added to detect, arriving as a
// false positive that trains somebody to ignore the column.
func (r FleetRoster) Validate() error {
	switch {
	case r.Schema == 0:
		return ValidationError(nil, "the roster states no schema version").
			WithHint("a roster begins with `schema: %d`", FleetRosterSchemaVersion)
	case r.Schema > FleetRosterSchemaVersion:
		return ValidationError(nil,
			"the roster was written for a newer manager (schema %d, this manager reads %d)",
			r.Schema, FleetRosterSchemaVersion).
			WithHint("upgrade the manager reading the fleet")
	case len(r.Installations) == 0:
		return ValidationError(nil, "the roster names no installations").
			WithHint("each entry is a product, an installation id and the " +
				"public key it signs with; `morzer installation describe` " +
				"prints the last two on the machine itself")
	}

	seen := make(map[string]int, len(r.Installations))
	for i, e := range r.Installations {
		// Through FleetKey, so an entry is validated by exactly the guard
		// that builds the key it will be matched against. A second
		// spelling of "is this a usable product and id" would drift, and
		// the drift would show up as an installation the roster expects
		// at a key nothing can ever be published to.
		key, err := FleetKey(e.Product, e.ID)
		if err != nil {
			return ValidationError(err, "roster entry %d is not an installation", i+1)
		}
		if prev, dup := seen[key]; dup {
			return ValidationError(nil,
				"roster entries %d and %d both name %s/%s", prev+1, i+1, e.Product, e.ID).
				WithHint("one installation publishes one row at one key, so a " +
					"second entry can only disagree with the first")
		}
		seen[key] = i

		if err := validateRosterKey(e.PublicKey); err != nil {
			return ValidationError(err,
				"the key for %s/%s is not usable", e.Product, e.ID)
		}
	}
	return nil
}

// validateRosterKey refuses a public key that could never verify anything.
//
// Not a cryptographic check -- the domain layer holds no crypto and must not.
// It refuses the two shapes an operator actually produces: the whole `.pub`
// file pasted in, comment line and all, and a key with whitespace through it
// from a terminal that wrapped. Both would report every row from that machine
// as unverifiable, which reads exactly like the attack this roster exists to
// detect. A refusal at the file is worth a great deal more than a false alarm
// in the table.
func validateRosterKey(key string) error {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return nil
	}
	if strings.ContainsAny(trimmed, " \t\r\n") {
		return ValidationError(nil, "it is not a single line").
			WithHint("a roster key is the one base64 line minisign prints, " +
				"not the whole public-key file -- `morzer installation " +
				"describe` prints the line on the machine itself")
	}
	return nil
}
