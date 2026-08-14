package ops

import (
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/morzecrew/morzer/internal/domain"
)

// Reading a roster (RFC 0026 P3).
//
// The parse lives here rather than in the domain for the same reason
// ParseTargetCredentials does: the domain layer holds no YAML decoder, by a
// rule depguard enforces. The *shape* of a roster and every refusal about it
// are domain.FleetRoster's; this is the decoder in front of them.

// ParseFleetRoster reads a roster document.
//
// Strict, and unknown fields are refused. A roster is a file an operator writes
// by hand and reads back rarely, so `installation:` where `installations:` was
// meant would otherwise parse into an empty roster -- which reports the whole
// fleet absent, or nothing absent at all, depending on which key was misspelt.
// Both are worse than a refusal naming the line.
func ParseFleetRoster(raw string) (domain.FleetRoster, error) {
	if strings.TrimSpace(raw) == "" {
		return domain.FleetRoster{}, domain.ValidationError(nil, "it is empty").
			WithHint("%s", rosterHint)
	}

	var roster domain.FleetRoster
	// Both, as every other strict decode in this repository spells it.
	// `Strict()` already sets the same flag, so the second is redundant
	// today -- a sabotage that removed it killed nothing, which is how that
	// was established. Kept because matching the sibling call sites is
	// worth more than one word, and because the option's own name is what
	// tells a reader what strictness means here.
	if err := yaml.UnmarshalWithOptions([]byte(raw), &roster,
		yaml.Strict(),
		yaml.DisallowUnknownField(),
	); err != nil {
		// The decoder's own message is kept here, unlike the credential
		// parser's: a roster holds no secrets, and the line and column it
		// quotes are the whole value of a refusal about a hand-written
		// file.
		return domain.FleetRoster{}, domain.ValidationError(nil,
			"it is not a roster: %s", firstLine(err.Error())).
			WithHint("%s", rosterHint)
	}

	if err := roster.Validate(); err != nil {
		return domain.FleetRoster{}, err
	}
	return roster, nil
}

// rosterHint is the same advice wherever a roster is refused. One string
// because an operator meeting two different descriptions of one file format
// has to work out which is current.
const rosterHint = "a roster is YAML: `schema: 1` and an `installations:` list of " +
	"product, id and key -- `morzer fleet publish --dry-run --json` prints all three " +
	"on the machine itself"

// firstLine keeps a decoder's error to its first line.
//
// goccy's messages carry an excerpt of the source underneath the message, which
// is useful in a terminal and wrong inside an error string that may itself be
// rendered into a table cell.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
