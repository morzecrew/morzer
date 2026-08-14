package domain

import (
	"fmt"
	"strings"
)

// What a sandbox must not inherit (RFC 0026 decision 7 and §3.5).
//
// The hazard is real and it is nobody's mistake: `installation import` keeps
// the original id *deliberately*, because backups are stamped with it and a
// rebuilt machine with a fresh id could not restore its own -- and RFC 0009
// puts backup targets and their credentials in the export. So a sandbox rebuilt
// from a production export holds the customer's bucket, the customer's
// credentials and a matching installation id, and every routine thing it does
// lands in production.
//
// **A list, not a special case, and that is the whole point of it being here.**
// The mitigation shipped as one line -- `inst.Backup.Targets = nil` -- in the
// import path, and it survived the arrival of fleet publishing only by luck:
// fleet rows go to the same targets, so the second thing to drop happened to be
// the same field as the first. RFC 0026 §10.1 measured that and decision 7
// exists to stop relying on it. What makes this a mechanism rather than a
// longer special case is the test beside it: every field of an installation is
// classified, so a *third* thing to drop cannot be added without somebody
// saying, in writing, which side it is on.
//
// The rule that decides membership is **reach**: does keeping this let a
// throwaway machine act on infrastructure the production machine owns? Not "is
// it sensitive" -- parameters and domains are sensitive, and a sandbox needs
// them to render anything at all.

// SandboxDrop is one thing removed on the way to a sandbox.
type SandboxDrop struct {
	// Field is the installation field, spelled as an operator finds it in
	// installation.yaml.
	Field string

	// Noun is what one of them is called in a sentence. Pluralised with an
	// `s`, which both of these take -- a field whose noun does not can
	// carry its own plural when there is one.
	Noun string

	// Why is what a sandbox would otherwise *do*, as a verb phrase. The
	// consequence rather than the category: "must not write into
	// production's bucket" is actionable and "holds credentials" is not.
	Why string

	// Count is how many of it this installation carries, so a report can
	// say what was taken rather than that something was.
	Count func(Installation) int

	// Drop removes it.
	Drop func(*Installation)
}

// SandboxDrops is everything an installation must shed to become a sandbox.
//
// Order is the order an operator is told about them, so the one with teeth
// comes first.
func SandboxDrops() []SandboxDrop {
	return []SandboxDrop{
		{
			Field: "backup.targets",
			Noun:  "backup target",
			Why:   "write into production's bucket",
			Count: func(i Installation) int { return len(i.Backup.Targets) },
			Drop:  func(i *Installation) { i.Backup.Targets = nil },
		},
		{
			// Not covered before, and the same shape as the first: a
			// notify target's endpoint may *be* its credential -- a
			// Slack or Teams webhook URL is a bearer token spelled as
			// a path -- and the secret state holding it travels in the
			// export. So a sandbox rebuilt from a production export
			// would page the customer's on-call about a machine that
			// exists in order to be broken.
			Field: "notify.targets",
			Noun:  "notify target",
			Why:   "report into production's alerting",
			Count: func(i Installation) int { return len(i.Notify.Targets) },
			Drop:  func(i *Installation) { i.Notify.Targets = nil },
		},
	}
}

// SandboxDropped is one thing that was removed, and how much of it.
type SandboxDropped struct {
	Field string `json:"field"`
	Count int    `json:"count"`

	// Noun and Why are carried so a caller can render the sentence without
	// looking the drop up again -- and so a `--json` consumer reading this
	// gets the reason rather than a field name it has to interpret.
	Noun string `json:"noun"`
	Why  string `json:"why"`
}

// Sandboxed returns this installation as a sandbox, and says what that cost.
//
// Value in, value out, like every other constructor here: the caller's copy is
// untouched. An empty result means there was nothing to drop, which is the
// ordinary case for an export from a machine that kept its backups on its own
// disk -- and the reason the caller must say nothing rather than "dropped 0",
// which trains an operator to skip the sentence that will one day matter.
func (i Installation) Sandboxed() (Installation, []SandboxDropped) {
	out := i
	out.Mode = ModeDev

	var dropped []SandboxDropped
	for _, drop := range SandboxDrops() {
		n := drop.Count(out)
		if n == 0 {
			continue
		}
		drop.Drop(&out)
		dropped = append(dropped, SandboxDropped{
			Field: drop.Field, Count: n, Noun: drop.Noun, Why: drop.Why,
		})
	}
	return out, dropped
}

// DescribeSandboxDrops renders what Sandboxed removed as one clause.
//
// What was taken, then why, rather than a reason per item: an operator reading
// an import summary needs to know their sandbox is not wired to production, and
// two sentences of justification in the middle of it is where they stop
// reading. Empty when nothing was dropped.
func DescribeSandboxDrops(dropped []SandboxDropped) string {
	if len(dropped) == 0 {
		return ""
	}

	what := make([]string, 0, len(dropped))
	why := make([]string, 0, len(dropped))
	for _, d := range dropped {
		noun := d.Noun
		if d.Count != 1 {
			noun += "s"
		}
		what = append(what, fmt.Sprintf("%d %s", d.Count, noun))
		why = append(why, d.Why)
	}
	return "dropped " + strings.Join(what, " and ") +
		" -- a sandbox must not " + strings.Join(why, " or ")
}
