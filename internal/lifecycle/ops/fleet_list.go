package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// Reading the rows back (RFC 0026 P2 and P3).
//
// Stateless: a listing, a fetch per row, a table. No daemon, no database, no
// cache, and nothing on this machine changes. It runs on a laptop, which is why
// it takes a target URL and credentials rather than assuming an installation.
//
// **Everything here turns on where the anchor comes from.** Without a roster
// this reader cannot authenticate a row and cannot show an installation that
// never published -- one cause, because the roster is both the trust anchor
// (decision 6b) and the only source of an expected population (decision 5). It
// says both on every run rather than in documentation an operator reading a
// complete-looking table is not reading, which is the only reason RFC 0026 §8
// let it ship before the roster existed.
//
// With one, the refusal that matters is the one still not written: this reader
// never *accepts* a signature against the key the row itself carries. That
// check passes, looks like verification, and establishes nothing -- the machine
// overwriting its neighbour's row rewrites payload, key and signature together,
// and the result verifies perfectly against itself. The row's own key is
// consulted in exactly one place, `fleetVerdict`, and only to name a failure
// that has already happened: a signature valid under it and invalid under the
// roster's is what an overwrite looks like from outside, and saying so is worth
// the extra check. Accepting it would be the defect decision 6b removed.

// DefaultFleetStaleAfter is how old a row may be before it is called stale.
//
// A default rather than a required flag, because a fleet view whose staleness
// column is empty until somebody configures it is a fleet view that never
// reports the thing it exists to report. A day is long enough that an hourly
// publisher missing one run is not an alarm, and short enough that a machine
// which stopped publishing yesterday is visible today.
//
// The threshold used is printed with the table, so it is never a hidden
// judgement: a reader disagreeing with it can see what to pass instead.
const DefaultFleetStaleAfter = 24 * time.Hour

// FleetSignature is what this reader established about a row's signature.
//
// The vocabulary divides in two, and the division is the roster. Without one,
// the only honest answers are whether a signature is *there* -- `attest log`
// set that precedent, and the reasoning is identical: whether a signature
// exists and whether it checks out are different questions, and a listing that
// conflated them would answer the second without having asked it.
//
// With a roster, the reader has an anchor outside the row and can answer the
// second question. What it must never do is answer it from the row's own
// embedded key, which is why there is no path from `signed` to `verified` that
// does not pass through a roster entry (decision 6b).
type FleetSignature string

const (
	// FleetSigned means a signature was published beside the row and
	// nothing checked it: no roster was given, the roster does not name
	// this installation, or it names it without a key.
	FleetSigned FleetSignature = "signed"

	// FleetUnsigned means none was, and nothing expected one. A machine
	// that has never minted a key publishes rows without one, which is a
	// state and not a fault.
	FleetUnsigned FleetSignature = "unsigned"

	// FleetVerified means the key the roster binds to this installation
	// produced these bytes.
	//
	// The only claim in this vocabulary that is an authentication, and the
	// only one whose evidence comes from outside the object it describes.
	FleetVerified FleetSignature = "verified"

	// FleetSignedByAnotherKey means the signature is valid -- and made by
	// the key the row itself carries, which the roster does not name.
	//
	// **This is the overwrite, and it is what the whole design turns on.**
	// A machine with write access to the shared prefix rewrites its
	// neighbour's payload, embedded public key and signature together; the
	// result verifies perfectly against itself, and a reader anchored in
	// the row reports it as good. This one reports it by name.
	FleetSignedByAnotherKey FleetSignature = "signed-by-another-key"

	// FleetMissingSignature means the roster names a key for this
	// installation and the row arrived without a signature at all.
	//
	// Distinct from FleetUnsigned, and the distinction closes a downgrade:
	// an attacker who cannot forge a signature can *remove* one, and if a
	// stripped signature read as the ordinary unsigned state, deleting the
	// `.minisig` beside a forged row would be enough to escape the check.
	// The roster is what makes this answerable -- it says this installation
	// signs.
	FleetMissingSignature FleetSignature = "missing-signature"

	// FleetUnverifiable means a signature is there and no key available to
	// this reader accounts for it.
	FleetUnverifiable FleetSignature = "unverifiable"
)

// Finding reports whether this verdict is one somebody has to act on.
//
// `unsigned` and `signed` are not: they are what a reader without an anchor can
// say, and a fleet listed without a roster must not exit non-zero on every row.
// The other three are the roster earning its place.
func (s FleetSignature) Finding() bool {
	switch s {
	case FleetSignedByAnotherKey, FleetMissingSignature, FleetUnverifiable:
		return true
	default:
		return false
	}
}

// FleetListOptions selects the target, the staleness threshold and the roster.
type FleetListOptions struct {
	TargetOptions

	// StaleAfter is the age at which a row is called stale. Zero takes
	// DefaultFleetStaleAfter; negative disables the judgement entirely,
	// which is what a reader who does not want one should be able to say.
	StaleAfter time.Duration

	// Roster is the expected population, and the trust anchor. Zero means
	// none was given, which is the state this reader shipped in and the
	// state it says out loud on every run.
	Roster domain.FleetRoster
}

// FleetReport is what `fleet ls` answers, and the `--json` contract.
type FleetReport struct {
	// Targets is where the rows were read from, in the order they were
	// read.
	Targets []string `json:"targets"`

	// Rows is every row found, and every key that could not be turned into
	// one. Sorted by product then installation, so two runs against the
	// same target print the same table.
	Rows []FleetRowStatus `json:"rows"`

	// StaleAfter is the threshold this run applied, so the verdict in the
	// stale column is never one the reader has to guess at. Empty when
	// staleness was not judged.
	StaleAfter string `json:"stale_after,omitempty"`

	// Expected is how many installations the roster named, zero when none
	// was given. It is what makes "3 rows" readable: three of three is a
	// fleet, three of twelve is an incident.
	Expected int `json:"expected,omitempty"`

	// Limitations is what this reader could not do. Never empty -- see the
	// package comment above -- and read by a human rather than a machine,
	// which is why it is prose.
	//
	// A roster shortens the list and does not empty it. The anchor is then
	// a file the operator maintains, and a reader that stopped saying so
	// would be presenting its own input back as evidence.
	Limitations []string `json:"limitations"`
}

// Problems counts the rows carrying one, which is what an exit status is built
// from.
func (r FleetReport) Problems() int {
	var n int
	for _, row := range r.Rows {
		if row.Problem != "" {
			n++
		}
	}
	return n
}

// Absent counts the installations the roster expects and no target holds.
//
// The count this whole phase exists for. An object that was never written
// cannot announce itself, so without a roster this number is structurally
// unavailable rather than merely zero.
func (r FleetReport) Absent() int {
	var n int
	for _, row := range r.Rows {
		if row.Absent {
			n++
		}
	}
	return n
}

// Stale counts the rows older than the threshold.
func (r FleetReport) Stale() int {
	var n int
	for _, row := range r.Rows {
		if row.Stale {
			n++
		}
	}
	return n
}

// FleetRowStatus is one row as the reader found it.
type FleetRowStatus struct {
	// Target is which target this was read from, so a fleet spread over two
	// buckets does not present one installation as two mysteries.
	Target string `json:"target"`

	// Key is where it was found, always -- including on a row that could
	// not be read, which is the case where knowing the key is the only way
	// to go and look.
	Key string `json:"key"`

	// Product and InstallationID come from the *key*, not from the row, so
	// they are present even when the document is unreadable.
	Product        string `json:"product,omitempty"`
	InstallationID string `json:"installation_id,omitempty"`

	// Row is the payload, nil when it could not be read, would not
	// validate, or -- once a roster names a key for this installation --
	// could not be authenticated. A row this manager refused is refused
	// whole: reading the fields it happens to recognise out of a document
	// it does not understand produces something that looks complete and is
	// not, and rendering an impostor's counts beside a caption would be the
	// caption doing the work.
	//
	// So a payload here has passed every check this run was able to make,
	// which is an invariant a `--json` consumer can build on.
	Row *domain.FleetRow `json:"row,omitempty"`

	// Signature is what the reader established. Empty on an absent row,
	// which has no signature because it has no row.
	Signature FleetSignature `json:"signature"`

	// Expected says the roster names this installation.
	//
	// False on every row when no roster was given, and false with one for a
	// row the roster does not account for -- which is a machine somebody
	// forgot to add, or one somebody added to the bucket. Worth showing and
	// deliberately not worth failing on: a roster covering three of twelve
	// machines is a legitimate way to start, and a reader that reported the
	// other nine as findings would be unusable on the way in.
	Expected bool `json:"expected,omitempty"`

	// Absent marks an installation the roster expects and no target holds.
	// There is no row behind it -- it is synthesised from the roster, which
	// is the only place its existence is recorded.
	Absent bool `json:"absent,omitempty"`

	// Age is how long ago the row says it was published, rendered. Empty
	// when there is no row to ask.
	Age string `json:"age,omitempty"`

	// Stale says the row is older than the threshold this run applied.
	Stale bool `json:"stale,omitempty"`

	// Problem is why this is not a row anybody can read, when it is not.
	// A key that produced no row is still a line in the table: a view that
	// dropped what it could not read would report health it did not
	// observe, which is decision 4.
	Problem string `json:"problem,omitempty"`
}

// FleetList reads every row on a target.
func FleetList(ctx context.Context, d *Deps, opts FleetListOptions) (FleetReport, error) {
	if d.Objects == nil {
		return FleetReport{}, domain.Internal(nil, "no target registry was wired")
	}

	targets, err := d.targetsFor(ctx, opts.TargetOptions)
	if err != nil {
		return FleetReport{}, err
	}

	report := FleetReport{
		Expected:    len(opts.Roster.Installations),
		Limitations: d.fleetLimitations(opts.Roster),
	}

	stale := opts.StaleAfter
	if stale == 0 {
		stale = DefaultFleetStaleAfter
	}
	if stale > 0 {
		report.StaleAfter = stale.String()
	}

	reading := fleetReading{now: d.now(), stale: stale, roster: opts.Roster}
	for _, target := range targets {
		report.Targets = append(report.Targets, target.String())
		report.Rows = append(report.Rows, d.fleetRowsOn(ctx, target, reading)...)
	}

	// Absence, last, because it is a statement about every target at once.
	//
	// A machine publishes to one target and is read from another, so an
	// installation is absent only when *no* target holds it -- computing
	// this per target would report a fleet split across two buckets as half
	// missing on each.
	report.Rows = append(report.Rows, absentRows(opts.Roster, report.Rows)...)

	// Sorted by what an operator reads down, and by the key last so the
	// order is total: two runs against an unchanged target must print the
	// same table, and a listing's order is the transport's, not ours.
	sort.SliceStable(report.Rows, func(i, j int) bool {
		a, b := report.Rows[i], report.Rows[j]
		switch {
		case a.Product != b.Product:
			return a.Product < b.Product
		case a.InstallationID != b.InstallationID:
			return a.InstallationID < b.InstallationID
		default:
			return a.Key < b.Key
		}
	})
	return report, nil
}

// fleetReading is what every row on one run is read against.
//
// A struct rather than three parameters threaded through two functions: they
// are constant for the run, they travel together, and the roster arriving as a
// fourth positional argument is how a call site ends up passing the staleness
// threshold as the clock.
type fleetReading struct {
	now    time.Time
	stale  time.Duration
	roster domain.FleetRoster
}

// fleetLimitations says what this reader could not do, given what it was given.
//
// Never empty, in any of the three states, and that is the point rather than a
// side effect. RFC 0026 §8 lets this command exist at all because it states its
// own limits on every run instead of leaving them to documentation an operator
// reading a complete-looking table is not reading. A roster narrows the list;
// nothing empties it, because the anchor is then a file the operator maintains
// and a reader that stopped saying so would be presenting its own input back as
// evidence.
func (d *Deps) fleetLimitations(roster domain.FleetRoster) []string {
	if !roster.Given() {
		return []string{
			// One cause, said as one cause. Splitting these into two
			// unrelated notes would let a reader fix one impression
			// -- "I should pass a roster to check signatures" --
			// without ever learning the other.
			"no roster was given, so no row below is authenticated and no absent " +
				"installation can be shown; both need the roster that binds an " +
				"installation id to a public key",
		}
	}

	out := []string{
		"every verdict below is against the roster you supplied: it is what says " +
			"which installations exist and which key each one signs with, and " +
			"nothing here can check that it is right",
	}

	if unkeyed := roster.Unkeyed(); len(unkeyed) > 0 {
		// About the roster rather than about the rows, because it is
		// computed before any row is read and has to stay true either
		// way: "rows from it are shown as signed" reads as a statement
		// about rows that exist, and the entry most likely to be missing
		// a key is the one whose machine never published.
		out = append(out, fmt.Sprintf(
			"the roster binds no key to %s, so nothing published under %s can be "+
				"authenticated; `morzer fleet publish --dry-run --json` prints "+
				"the key on the machine itself",
			strings.Join(unkeyed, ", "), pluralThem(len(unkeyed))))
	}

	if d.Checker == nil {
		// A build with no signature checker wired. Reporting every row as
		// unverifiable would be this reader accusing a fleet of what is
		// missing from itself.
		out = append(out, "this build cannot check signatures, so no row below is verified")
	}
	return out
}

// pluralThem renders "it" or "them", so a sentence naming one installation does
// not read as though it named several.
func pluralThem(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

// absentRows synthesises a row for every installation the roster expects and no
// target held.
//
// **The row this whole phase exists for.** An object that was never written
// cannot announce itself, so listing a prefix shows exactly the population that
// is fine -- which is the failure mode of every fleet view ever built. These
// are the only rows here that are not derived from something on a target, and
// they are the ones an operator is looking for.
//
// Matched on the product and installation id parsed out of the *key*, not on
// the key string: a key that would not parse produced no product and cannot
// account for any roster entry, which is correct -- an unreadable object at
// `fleet/demo/inst_A/../status.json` is not `demo/inst_A` reporting in.
func absentRows(roster domain.FleetRoster, found []FleetRowStatus) []FleetRowStatus {
	if !roster.Given() {
		return nil
	}

	seen := make(map[string]bool, len(found))
	for _, row := range found {
		if row.Product != "" {
			seen[row.Product+"/"+row.InstallationID] = true
		}
	}

	var out []FleetRowStatus
	for _, e := range roster.Installations {
		if seen[e.Product+"/"+e.ID] {
			continue
		}
		// Validate has already refused a roster entry FleetKey would not
		// accept, so this cannot fail -- and if it somehow did, the row
		// still belongs in the table with no key to go and look at.
		key, _ := domain.FleetKey(e.Product, e.ID)
		out = append(out, FleetRowStatus{
			Key:            key,
			Product:        e.Product,
			InstallationID: e.ID,
			Expected:       true,
			Absent:         true,
			Problem:        "the roster expects this installation; no target holds a row",
		})
	}
	return out
}

// fleetRowsOn reads one target.
//
// Every failure below produces a row rather than an error. A target that cannot
// be listed at all is the exception -- there are no rows to carry the problem,
// so it becomes one row of its own naming the target.
func (d *Deps) fleetRowsOn(
	ctx context.Context, target ports.TargetRef, reading fleetReading,
) []FleetRowStatus {
	listed, err := d.Objects.ObjectKeys(ctx, target, domain.FleetPrefix)
	if err != nil {
		return []FleetRowStatus{{
			Target:  target.String(),
			Key:     domain.FleetPrefix,
			Problem: domain.BoundedText(domain.AsError(err).Message),
		}}
	}

	// A listing prefix is a string match, not a path one.
	//
	// Asking a store for `fleet` is answered with `fleet-old/notes.txt` as
	// readily as with `fleet/demo/…`, because every adapter filters on
	// `strings.HasPrefix`. Left in, those keys became rows carrying "not a
	// fleet row's key" -- and since a problem row makes `fleet ls` exit
	// non-zero, an unrelated directory on a shared target turned a healthy
	// fleet into a failing command.
	//
	// Filtered here rather than by listing `fleet/`, which cannot be asked
	// for: `guardObjectKey` refuses a prefix that `path.Clean` would change,
	// so the trailing slash is rejected before any adapter sees it. Widening
	// that guard would loosen the same check attestations rely on, for a
	// problem this line solves.
	//
	// Dropping them rather than reporting them is the right reading of
	// decision 4: a key outside this namespace is not a fleet row that could
	// not be read, it is somebody else's object.
	keys := make([]string, 0, len(listed))
	for _, key := range listed {
		if strings.HasPrefix(key, domain.FleetPrefix+"/") {
			keys = append(keys, key)
		}
	}

	present := make(map[string]bool, len(keys))
	for _, key := range keys {
		present[key] = true
	}

	// The roster's own keys first, and the reason is the interaction
	// between two bounds that were written apart.
	//
	// MaxFleetRows bounds what a writer with access to the prefix can make
	// this reader do. Absence is computed from the rows that came back. Put
	// together and left in listing order, a flood of a thousand junk keys
	// pushes the twelve real ones past the cap and the report says the whole
	// fleet is absent -- turning a nuisance into twelve machines somebody
	// gets out of bed for. Ordering the expected keys first costs nothing
	// and means a flood can only ever truncate objects nobody asked about.
	keys = expectedFirst(keys, reading.roster)

	var out []FleetRowStatus
	var fetched int

	for _, key := range keys {
		if strings.HasSuffix(key, fleetSigExt) {
			// A signature whose row is there is accounted for by
			// that row. One whose row is *not* there is a finding
			// worth a line: a publisher that wrote the signature
			// and not the document, or a document somebody removed
			// and a signature they did not.
			if !present[strings.TrimSuffix(key, fleetSigExt)] {
				out = append(out, FleetRowStatus{
					Target: target.String(), Key: domain.BoundedText(key),
					Problem: "a signature with no row beside it",
				})
			}
			continue
		}

		// One fetch per row, and the row count is bounded.
		//
		// Every object is capped at ports.MaxObjectBytes, which bounds
		// each fetch and not the number of them -- and the number is
		// chosen by whoever can write to the prefix, which §9 says is
		// every machine in the fleet plus whoever holds one of their
		// credentials. A flooded prefix would otherwise cost this
		// reader one request and one allocation per key, on a laptop,
		// during the incident that made somebody run it.
		//
		// The cap is *reported*, never silent: a truncated listing that
		// looked complete would be the failure this whole design is
		// written against, so the excess becomes a row saying how much
		// was not read.
		if fetched >= MaxFleetRows {
			out = append(out, FleetRowStatus{
				Target: target.String(), Key: domain.FleetPrefix,
				Problem: fmt.Sprintf(
					"this target holds more than %d rows; %d were not read, so this "+
						"listing is incomplete", MaxFleetRows, len(keys)-fetched),
			})
			break
		}
		fetched++

		out = append(out, d.fleetRowAt(ctx, target, key, reading, present))
	}
	return out
}

// expectedFirst puts the keys the roster names at the front, keeping the
// relative order of each group.
//
// Stable rather than sorted: the listing's own order is the transport's, the
// report is sorted at the end anyway, and a second ordering here would be a
// second thing to keep in step with that one.
func expectedFirst(keys []string, roster domain.FleetRoster) []string {
	if !roster.Given() {
		return keys
	}

	// Row keys only. A signature is accounted for by the row beside it and
	// costs no fetch of its own, so its position in the listing decides
	// nothing.
	wanted := make(map[string]bool, len(roster.Installations))
	for _, e := range roster.Installations {
		// Validate has already refused an entry FleetKey would reject.
		if key, err := domain.FleetKey(e.Product, e.ID); err == nil {
			wanted[key] = true
		}
	}

	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if wanted[key] {
			out = append(out, key)
		}
	}
	for _, key := range keys {
		if !wanted[key] {
			out = append(out, key)
		}
	}
	return out
}

// MaxFleetRows bounds how many rows one target contributes to a listing.
//
// Generous: a fleet this design is built for is twelve machines, and a hundred
// times that is still a table nobody reads. It is not a capacity limit, it is a
// bound on what a writer with access to the prefix can make this reader do --
// and reaching it produces a row saying the listing is incomplete rather than a
// shorter table that looks whole.
const MaxFleetRows = 1000

// fleetRowAt reads one key.
func (d *Deps) fleetRowAt(
	ctx context.Context,
	target ports.TargetRef,
	key string,
	reading fleetReading,
	present map[string]bool,
) FleetRowStatus {
	// The key is bounded before it is stored on the status, not after.
	//
	// It came out of a listing of a target several machines can write to, so
	// it is remote text like any other -- and it is the one field that
	// reaches the terminal on the path where *nothing else* does: a key that
	// will not parse has no row behind it, and the table prints the key
	// itself in both the name column and the problem table.
	status := FleetRowStatus{
		Target:    target.String(),
		Key:       domain.BoundedText(key),
		Signature: FleetUnsigned,
	}

	// Parsed before it is fetched, and parsed from the *original* key: the
	// bounded copy exists to be displayed, and using it here would let a
	// dropped control character change which object is fetched. The keys came
	// out of a prefix several machines write to, so `fleet/../../etc/passwd`
	// has to be a finding rather than a read -- the transports refuse it too,
	// and this layer must not depend on being saved by the one below it.
	product, id, err := domain.ParseFleetKey(key)
	if err != nil {
		status.Problem = domain.AsError(err).Message
		return status
	}
	status.Product, status.InstallationID = product, id

	entry, expected := reading.roster.Entry(product, id)
	status.Expected = expected

	signed := present[key+fleetSigExt]
	if signed {
		// Present, and nothing more is claimed yet. See FleetSignature.
		status.Signature = FleetSigned
	}

	body, err := d.Objects.GetObject(ctx, target, key)
	if err != nil {
		// The adapter's message quotes the key it was asked for.
		status.Problem = domain.BoundedText(domain.AsError(err).Message)
		return status
	}

	// **Verified before it is parsed** (§3.6), and a row that fails is a row
	// carrying that problem with no payload behind it.
	//
	// The order is what makes the signature mean anything: it covers the
	// bytes as published, so checking it against a re-serialisation would
	// need a canonical form both ends implement identically. And returning
	// here rather than rendering the row is the fail-closed half -- when a
	// roster names a key for this installation, `row` being non-nil below
	// means those bytes were authenticated, which is an invariant a --json
	// consumer can build on. Displaying an impostor's `3/3 up` beside a
	// caption would be the caption doing the work.
	if verdict, why := d.fleetVerdict(ctx, target, key, entry, expected, signed, body); verdict != "" {
		status.Signature = verdict
		if verdict.Finding() {
			status.Problem = why
			return status
		}
	}

	var row domain.FleetRow
	if err := json.Unmarshal(body, &row); err != nil {
		// The decoder's message quotes the input, so it is bounded like
		// everything else that arrived here.
		status.Problem = domain.BoundedText("it is not a fleet row: " + err.Error())
		return status
	}
	if err := row.Validate(); err != nil {
		status.Problem = domain.BoundedText(domain.AsError(err).Message)
		return status
	}

	// The row's own account of who it is, against the key it was found at.
	//
	// A check that costs nothing and is the only integrity statement a
	// reader can make *without* a roster: a row claiming to be a different
	// installation from the one whose key it occupies was either published
	// to the wrong place or put there by somebody else. Neither is a row to
	// display as that installation's status. It still runs with a roster,
	// where it catches the case a signature check cannot -- a row correctly
	// signed by the machine that wrote it, sitting at somebody else's key.
	//
	// **Compared before the row is bounded**, and the order is the check.
	// Bounding drops control characters, so a row naming `demo\u001b` would
	// become `demo` and match a key it does not belong at -- sanitising
	// first would hand an attacker a way past it. The message is bounded
	// instead, since it quotes what it refused.
	if row.Product != product || row.InstallationID != id {
		status.Problem = domain.BoundedText(
			"the row says it is " + row.Product + "/" + row.InstallationID +
				", which is not the installation whose key it is at")
		return status
	}

	// Bounded once it has passed every check, and before anything can render
	// it. Every string in it came off a target several machines can write to;
	// see FleetRow.Bounded for what that costs if it does not happen.
	row = row.Bounded()

	status.Row = &row
	status.Age = fleetAge(row.Age(reading.now))
	status.Stale = row.Stale(reading.now, reading.stale)
	return status
}

// fleetVerdict checks a row's signature against the roster, and says what it
// found. An empty verdict means nothing was checked and the presence-only
// answer already on the status stands.
//
// **The key comes from the roster and never from the row.** That sentence is
// decision 6b and it is the whole of this function's reason to exist: a row
// carries the public half of the key that signed it, and a machine overwriting
// its neighbour's row rewrites payload, key and signature together, so a
// verifier anchored in the row authenticates nothing against the only attacker
// this design has.
//
// The row's own key *is* read here, and only ever to characterise a failure
// that has already happened. A signature that verifies against it after failing
// against the roster's is not a corrupt file or a bit flip -- it is a valid
// signature by a machine the roster does not name, which is precisely what the
// overwrite looks like from outside. Naming it is worth the extra check;
// accepting it would be the defect.
func (d *Deps) fleetVerdict(
	ctx context.Context,
	target ports.TargetRef,
	key string,
	entry domain.FleetRosterEntry,
	expected, signed bool,
	body []byte,
) (FleetSignature, string) {
	anchor := strings.TrimSpace(entry.PublicKey)
	if d.Checker == nil || !expected || anchor == "" {
		// Nothing to check against. The reason is in the report's
		// limitations rather than on the row: it is a property of what
		// this run was given, identical for every row it applies to, and
		// repeating it per row would bury the rows where it does not.
		return "", ""
	}

	if !signed {
		return FleetMissingSignature,
			"the roster says this installation signs, and this row is unsigned"
	}

	sig, err := d.Objects.GetObject(ctx, target, key+fleetSigExt)
	if err != nil {
		// The listing named it and the fetch did not produce it. Not an
		// absence -- something is there -- so it is reported as a
		// signature nothing could be established about.
		return FleetUnverifiable, domain.BoundedText(
			"the signature beside this row could not be read: " +
				domain.AsError(err).Message)
	}

	if d.Checker.Check(body, sig, anchor) {
		return FleetVerified, ""
	}

	if own := embeddedSigningKey(body); own != "" && own != anchor && d.Checker.Check(body, sig, own) {
		return FleetSignedByAnotherKey, "signed by a key the roster does not name"
	}

	return FleetUnverifiable, "no key the roster names produced these bytes"
}

// embeddedSigningKey reads the public key a row claims for itself.
//
// Deliberately a second, minimal parse rather than a use of the row the caller
// goes on to build: the value is an *input to a check*, never a field that
// reaches a reader, and the row this reads from is one that has already failed
// verification. Nothing it returns is displayed, stored or trusted -- the only
// question asked of it is whether these bytes and this key were published
// together, and a wrong answer downgrades the verdict to `unverifiable`, which
// is the safe direction.
func embeddedSigningKey(body []byte) string {
	var claim struct {
		SigningKey string `json:"signing_key"`
	}
	// The error return is redundant with the caller's `own != ""` guard --
	// bytes that will not decode produce no key either way, and the verdict
	// is `unverifiable` in both cases. It is written out rather than
	// discarded because a swallowed error is a swallowed error, and because
	// the redundancy is with a guard three lines away in another function.
	if err := json.Unmarshal(body, &claim); err != nil {
		return ""
	}
	return strings.TrimSpace(claim.SigningKey)
}

// fleetAge renders how long ago a row was published.
//
// A row from the future is rendered as such rather than as a negative duration
// or a clamped zero: a machine whose clock is wrong is a finding, and "in 3h"
// is the reading that makes somebody go and look at it.
func fleetAge(age time.Duration) string {
	if age < 0 {
		return "in " + (-age).Round(time.Minute).String()
	}
	return age.Round(time.Minute).String() + " ago"
}
