package main

import "sort"

// The leak inventory — RFC 0023 P1.
//
// Every place above `internal/adapters` that names a concrete runtime, each
// classified and each carrying the thing that would remove it. This is the
// list; `len(inventory)` is the number.
//
// It is an allowlist, so it is also the enforcement: a name not here fails the
// build. And it is checked in both directions — an entry describing a symbol
// that no longer exists fails too, because an allowlist that only ever grows is
// a list nobody has to shrink, and the point of writing this down was that the
// count is supposed to fall.

// Class is what P1 was asked to decide about each leak.
type Class string

const (
	// PortShaped: the concept belongs above the adapter and only its name
	// is borrowed from Compose. The fix is a rename, and it costs nothing
	// outside this repository.
	PortShaped Class = "port-shaped"

	// ComposeShaped: the concept is Compose's own. It has to move below the
	// adapter boundary, and where it is a published ABI that move is a
	// breaking change for bundles in the field.
	ComposeShaped Class = "compose-shaped"

	// Catalogue: a runtime named as *data* — a key in a table of tools the
	// manager can probe, which a second runtime extends rather than
	// contradicts. Not a leak; listed because a reader counting greps will
	// find it and should know why it stays.
	Catalogue Class = "catalogue"
)

// Entry is one allowlisted mention.
type Entry struct {
	File   string
	Symbol string
	Class  Class

	// Why the classification is what it is.
	Why string

	// Removes is what would take this entry off the list. For a catalogue
	// entry it is empty: nothing removes it, and nothing should.
	Removes string
}

var inventory = []Entry{
	// ---- internal/ports ----
	{
		File: "internal/ports/compose_abi.go", Symbol: "file compose_abi.go", Class: PortShaped,
		Why: "the file name is the loudest of the fourteen: a ports file called " +
			"compose_abi says the interpolation contract belongs to one runtime, " +
			"before a reader opens it. Its three symbols say the same thing and its " +
			"contents say the opposite",
		Removes: "the rename that removes its three symbols renames the file with them",
	},
	{
		File: "internal/ports/compose_abi.go", Symbol: "name ComposeVars", Class: PortShaped,
		Why: "the values are DATA_DIR, SECRETS_DIR, CONFIG_FILE, RELEASE_DIR, VERSION, " +
			"PROFILE, DOMAIN. Not one of them is a Compose concept: they are the " +
			"facts about an installation that a declarative file may refer to, and " +
			"a Quadlet unit needs the same seven. Only the Go identifier says Compose",
		Removes: "renaming the file and its three symbols to the runtime-neutral vocabulary; " +
			"no environment variable a vendor writes changes, so no bundle breaks",
	},
	{
		File: "internal/ports/compose_abi.go", Symbol: "name ComposeVarPatterns", Class: PortShaped,
		Why:     "IMAGE_<NAME> and PARAM_<NAME> are the manifest's own two families",
		Removes: "the same rename",
	},
	{
		File: "internal/ports/compose_abi.go", Symbol: "func ComposeVarNames", Class: PortShaped,
		Why:     "renders the fixed set for a product; it calls HookEnv.Var and knows nothing about Compose",
		Removes: "the same rename",
	},
	{
		File: "internal/ports/hooks.go", Symbol: "field ComposeProject", Class: ComposeShaped,
		Why: "the expensive one. A Compose project is Compose's grouping primitive, " +
			"and Quadlet has no equivalent -- a unit prefix is a naming convention, " +
			"not a handle. It reaches every vendor hook as <PRODUCT>_COMPOSE_PROJECT " +
			"and the reference page documents it as being for a hook that shells out " +
			"to `docker compose`, so it is a published ABI whose *meaning* is absent " +
			"under a second runtime, not merely whose name is",
		Removes: "nothing cheap. The variable stays for Compose installations and is " +
			"absent under another runtime, which makes it a runtime-supplied variable " +
			"rather than a core one -- a change to what the hook ABI promises, and " +
			"therefore a decision for P2 rather than a rename",
	},

	// ---- internal/domain ----
	{
		File: "internal/domain/manifest.go", Symbol: "func ComposeFiles", Class: PortShaped,
		Why: "returns the base files plus a profile's additions and refuses an unknown " +
			"profile. Both halves are the manager's own model of a deployment profile; " +
			"the files happen to be Compose's today",
		Removes: "renaming to the runtime-neutral form. The yaml key is already `files`",
	},
	{
		File: "internal/domain/release.go", Symbol: "func ComposeFilePaths", Class: PortShaped,
		Why:     "resolves the above to absolute paths through Release.Path",
		Removes: "the same rename",
	},

	// ---- internal/infra ----
	{
		File: "internal/infra/tools/registry.go", Symbol: "name Docker", Class: Catalogue,
		Why: "a key in the probe catalogue, matching what a vendor writes in " +
			"requirements.tools. A Podman bundle declares podman and the catalogue " +
			"grows an entry; nothing here decides which runtime an installation uses",
	},
	{
		File: "internal/infra/tools/registry.go", Symbol: "name Compose", Class: Catalogue,
		Why: "the second key, probing `docker compose version --short`, because the " +
			"plugin has a version of its own that the daemon's does not imply. Two " +
			"catalogue keys for one binary, which is what makes this a table of " +
			"things to probe rather than a model of the runtime",
	},
	{
		File: "internal/infra/tools/registry.go", Symbol: "type dockerVersionDoc", Class: Catalogue,
		Why: "the shape of `docker version --format {{json .}}`, which only the probe reads",
	},
	{
		File: "internal/infra/tools/registry.go", Symbol: "func parseDockerVersion", Class: Catalogue,
		Why: "parses that shape, falling back when the daemon is down. One catalogue " +
			"entry's parser, not a statement about the installation",
	},

	// ---- internal/cli ----
	{
		File: "internal/cli/release_new.go", Symbol: "name scaffoldCompose", Class: ComposeShaped,
		Why: "the scaffold writes a working example bundle, and an example has to pick " +
			"a runtime. It picks Compose in the file's name and in the `providers: " +
			"runtime: {name: compose}` it emits",
		Removes: "a --runtime flag on `release new` once a second runtime exists, " +
			"selecting between scaffolds. Cheap, and P3's problem rather than P1's",
	},

	// ---- tools ----
	{
		File: "tools/docscheck/main.go", Symbol: "func checkComposeVars", Class: PortShaped,
		Why: "asserts the reference page documents every variable in ports.ComposeVars, " +
			"which is the drift gate that makes the ABI real",
		Removes: "follows the ports rename; it is the same symbol one layer out",
	},

	// ---- tests ----
	//
	// Listed rather than exempted. A rule with an exemption for test files
	// is a rule with a hole the width of a package: a `composeFixture`
	// helper would be invisible, and helpers are where vocabulary settles.
	// These two name the symbol under test, so they are removed by the same
	// rename that removes it -- which is what the entries say, and what
	// makes them cost nothing to keep honest.
	{
		File: "internal/domain/manifest_test.go", Symbol: "func TestComposeFilesForProfile", Class: PortShaped,
		Why:     "names RuntimeSpec.ComposeFiles, which it tests",
		Removes: "the ports/domain rename",
	},
	{
		File: "internal/infra/tools/registry_test.go", Symbol: "func TestDockerVersionFallsBackWhenItIsNotJSON", Class: Catalogue,
		Why:     "names parseDockerVersion, the catalogue entry's parser, which it tests",
		Removes: "nothing — it follows its catalogue entry",
	},
}

// allowed indexes the inventory by the position a Finding reports.
func allowed() map[string]Entry {
	out := make(map[string]Entry, len(inventory))
	for _, e := range inventory {
		out[e.File+"\x00"+e.Symbol] = e
	}
	return out
}

// ClassCount is one row of the summary.
type ClassCount struct {
	Class Class
	N     int
}

// counts summarises the inventory by class, in a fixed order so two runs are
// comparable and so a class with nothing in it still reports zero — a class
// that vanishes when it empties is a class nobody notices reaching zero.
func counts() []ClassCount {
	byClass := map[Class]int{}
	for _, e := range inventory {
		byClass[e.Class]++
	}
	out := make([]ClassCount, 0, 3)
	for _, c := range []Class{PortShaped, ComposeShaped, Catalogue} {
		out = append(out, ClassCount{c, byClass[c]})
	}
	return out
}

func sortedInventory() []Entry {
	out := append([]Entry(nil), inventory...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Symbol < out[j].Symbol
	})
	return out
}
