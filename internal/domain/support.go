package domain

import "sort"

// The support bundle's inclusion policy (RFC 0024 §3.2).
//
// 0015 established the rule for what leaves a machine: an allowlist, not a
// denylist, so a kind added later is not forwarded until somebody classifies
// it. This is that rule one level up, and it lives here rather than in the
// operation because it is a policy statement rather than a procedure -- the
// same reason the parameter contract lives beside the parameter type.
//
// The table is the ABI. 0017 learned that whatever ships becomes a contract
// whatever the docs say, and here it runs in the other direction too: an
// operator who has read the list once will not read it again, so the list may
// grow freely and may only ever *shrink* with a version bump. The reference
// page is generated from this table, so the page cannot describe an archive
// nobody produces.

// SupportClass is what happens to a component when a bundle is built.
type SupportClass string

const (
	// SupportInclude is manager-produced and already structured. It goes in
	// as it is.
	SupportInclude SupportClass = "include"

	// SupportRedact is included only after passing through the redactor.
	// Today that is exactly one component -- container logs, the only raw
	// vendor bytes in the archive -- and the class exists rather than being
	// folded into `include` because the distinction is the whole safety
	// argument, not an implementation note.
	SupportRedact SupportClass = "redact"

	// SupportNever is enumerated because the enumeration is the point.
	//
	// A denylist would be worthless here -- inclusion is already an
	// allowlist, so these are excluded by not being listed. They are named
	// anyway so that "is the age identity in there?" has an answer somebody
	// can check, and so that adding a collector for one is a test failure
	// rather than a code review nobody ran.
	SupportNever SupportClass = "never"
)

// SupportComponent is one candidate for the archive.
type SupportComponent struct {
	// Name is the entry's path inside the archive, and the identifier the
	// collector is registered under. Empty for a `never` row: it names
	// something on this machine, not something in the bundle.
	Name string

	// Title is what the reference page calls it.
	Title string

	Class SupportClass

	// Reason is why this row is classified the way it is. It is prose that
	// ships -- the generated page prints it verbatim -- so it says why
	// rather than what.
	Reason string

	// Sources, for a `never` row, are the paths on this machine the row
	// refuses. Nil for everything else.
	//
	// A function of Paths rather than a literal, because these are the real
	// accessors: a test can therefore assert that no archive entry lies
	// under one of them, which is an outcome-guard. A list of strings here
	// would only be an intent-guard, and would go stale the first time a
	// path moved.
	Sources func(Paths) []string
}

// SupportSignatureBound is what a signature over a support archive proves, and
// what it does not.
//
// Carried inside `meta.json` rather than left to the documentation, because the
// reader who most needs it is the one handed the archive in a ticket with no
// documentation anywhere near it -- the same argument RFC 0025 makes for
// putting the bound in every attestation.
//
// The second sentence is RFC 0024 decision 11 and it is here rather than in a
// doc because it is the mistake a careful reader makes: the archive names the
// key that signed it, and checking the signature against that key is checking
// the archive against itself. Whoever wrote the archive wrote the name.
const SupportSignatureBound = "This signature proves that a process holding this " +
	"installation's signing key produced these bytes. It does not prove the archive " +
	"came from that machine -- a copied key signs from anywhere -- it does not prove " +
	"nobody edited the contents before the archive was made, and it does not identify " +
	"the operator. The key named above identifies which key to obtain from the " +
	"installation's operator; it is not a key to verify against, because this file " +
	"and that name were written by the same hand."

// SupportInventory is every component, classified.
//
// Ordered as an operator reads an archive rather than alphabetically: what the
// installation is, then what it did, then what its services are doing, then the
// raw stream, then the archive's own account of itself. The refusals come last
// because they are the part somebody checks once.
var SupportInventory = []SupportComponent{
	{
		Name:  "manifest.yaml",
		Title: "The resolved manifest",
		Class: SupportInclude,
		Reason: "What the release actually declares after templating, which is " +
			"the document every other answer is relative to. A vendor reading a " +
			"bundle without it is guessing which of their own releases this is.",
	},
	{
		Name:  "installation.yaml",
		Title: "Installation state",
		Class: SupportInclude,
		Reason: "What the operator chose: product, layout, policy, targets by " +
			"name. It holds no secret value and cannot -- every credential in an " +
			"installation is a reference to a secret by name, which is what makes " +
			"`installation describe` safe to commit and makes this safe to send.",
	},
	{
		Name:  "parameters.json",
		Title: "Parameters and their values",
		Class: SupportInclude,
		Reason: "Values as well as names. A parameter is not a secret by " +
			"construction -- its value already reaches Compose as an environment " +
			"variable, `docker inspect`, `status --json` and the journal -- so " +
			"withholding the values here would hide the most common cause of a " +
			"support question while protecting nothing.",
	},
	{
		Name:  "config-diff.txt",
		Title: "Configuration drift",
		Class: SupportInclude,
		Reason: "Where the files on disk differ from what the release renders — " +
			"the evidence an operator is usually asked for and least able to " +
			"produce by hand. It cannot embed a secret: a configuration template " +
			"is rendered with secret *references*, a name to the path of its " +
			"rendered file, and the values never enter the render context.",
	},
	{
		Name:  "journal.jsonl",
		Title: "The operation journal",
		Class: SupportInclude,
		Reason: "Every operation this installation has run, newest first, with " +
			"its steps and outcomes. It is the difference between \"the update " +
			"failed\" and a timestamped account of which step failed and what it " +
			"said.",
	},
	{
		Name:  "doctor.json",
		Title: "Diagnostic checks",
		Class: SupportInclude,
		Reason: "`doctor`'s results as it would print them now, including the " +
			"host facts its machine checks collect. Those facts have no producer " +
			"separate from the checks that report them, so they are here rather " +
			"than in a file of their own.",
	},
	{
		Name:  "releases.json",
		Title: "Version history",
		Class: SupportInclude,
		Reason: "Which releases are on this machine and which are current, " +
			"previous and staged. \"It worked before\" needs a before.",
	},
	{
		Name:  "services.json",
		Title: "Service and health state",
		Class: SupportInclude,
		Reason: "What the runtime reports for each service right now. A bundle " +
			"taken while something is down is worth more than one taken after a " +
			"restart, and this is the part that says which it was.",
	},
	{
		Name:  "logs/",
		Title: "Container logs",
		Class: SupportRedact,
		Reason: "The single most useful component and the only raw vendor bytes " +
			"in the archive. Bounded by lines and by bytes, passed through the " +
			"same redactor `morzer logs` uses, and omitted entirely rather than " +
			"included unfiltered when that redactor cannot be armed.",
	},
	{
		Name:  "manager.json",
		Title: "Manager version and build",
		Class: SupportInclude,
		Reason: "Which binary produced this archive. It also identifies the " +
			"redaction logic that ran, since the redactor ships with the manager " +
			"and has no version of its own to state.",
	},
	{
		Name:  "meta.json",
		Title: "The archive's own account of itself",
		Class: SupportInclude,
		Reason: "What was collected, what was omitted and why, and the redaction " +
			"count per file. A count of zero is not proof of cleanliness, but it " +
			"is the first thing a reviewer looks at and it must not have to be " +
			"inferred from the archive's shape.",
	},
	{
		Title: "The age identity",
		Class: SupportNever,
		Reason: "The key every secret in this installation is encrypted to. " +
			"Sending it converts an archive that is safe to hand to a stranger " +
			"into the machine's crown jewels in a ticket system.",
		Sources: func(p Paths) []string { return []string{p.AgeDir()} },
	},
	{
		Title: "Secret ciphertext",
		Class: SupportNever,
		Reason: "Useless to a vendor -- they cannot decrypt it -- and everything " +
			"to an attacker who later gets the identity. Nothing is gained by " +
			"including it, which is the easiest kind of refusal to keep.",
		Sources: func(p Paths) []string { return []string{p.SecretsFile()} },
	},
	{
		Title: "The machine's signing key",
		Class: SupportNever,
		Reason: "0028's per-installation identity. A copied signing key signs " +
			"from anywhere, so an archive carrying it would let its own recipient " +
			"forge statements attributed to this machine.",
		Sources: func(p Paths) []string { return []string{p.SigningDir()} },
	},
	{
		Title: "Backup target credentials",
		Class: SupportNever,
		Reason: "0009 put them in the recovery export, so the export is adjacent " +
			"to this archive and must not be mistaken for includable. They are " +
			"write access to the place the backups live.",
	},
	{
		Title: "The recovery export and its recipients' private material",
		Class: SupportNever,
		Reason: "0017's artifact exists to rebuild a machine. An archive that " +
			"carried it would be a rebuild kit for this installation, addressed " +
			"to whoever is reading the ticket.",
	},
	{
		Title: "The render directory",
		Class: SupportNever,
		Reason: "Where secrets are rendered as plaintext for the runtime to " +
			"read, on tmpfs, for as long as the deployment is up. It is the one " +
			"place on the machine where every secret exists in the clear.",
		Sources: func(p Paths) []string { return []string{p.SecretsRenderDir(), p.GeneratedDir()} },
	},
}

// SupportComponents returns the rows of one class, in inventory order.
func SupportComponents(class SupportClass) []SupportComponent {
	out := make([]SupportComponent, 0, len(SupportInventory))
	for _, c := range SupportInventory {
		if c.Class == class {
			out = append(out, c)
		}
	}
	return out
}

// SupportCollected reports whether a component's name is one a bundle carries,
// which is every row that is not a refusal.
func SupportCollected(name string) bool {
	for _, c := range SupportInventory {
		if c.Name == name {
			return c.Class != SupportNever
		}
	}
	return false
}

// SupportRefusedPaths is every path on this machine the inventory refuses,
// sorted and deduplicated.
//
// The archive is checked against this rather than against a list written beside
// the check, so a refusal is enforced where it is declared. Sorted so that a
// failure names paths in a stable order.
func SupportRefusedPaths(p Paths) []string {
	seen := map[string]bool{}
	for _, c := range SupportInventory {
		if c.Sources == nil {
			continue
		}
		for _, path := range c.Sources(p) {
			seen[path] = true
		}
	}
	out := make([]string, 0, len(seen))
	for path := range seen {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}
