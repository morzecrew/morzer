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

// Reading the rows back (RFC 0026 P2).
//
// Stateless: a listing, a fetch per row, a table. No daemon, no database, no
// cache, and nothing on this machine changes. It runs on a laptop, which is why
// it takes a target URL and credentials rather than assuming an installation.
//
// **What this phase deliberately cannot do**, and says so on every run: it
// cannot authenticate a row, and it cannot show an installation that never
// published. The two have one cause -- the roster, which arrives in P3, is both
// the trust anchor (decision 6b) and the only source of an expected population
// (decision 5). RFC 0026 §8 makes this acceptable *only* because the reader
// states it, so the limitations are part of the report rather than a note in
// the documentation.
//
// The refusal that matters here is the one not written: this reader never
// checks a signature against the key the row itself carries. That check would
// pass, would look like verification, and would establish nothing -- the
// machine overwriting its neighbour's row rewrites payload, key and signature
// together, and the result verifies perfectly against itself. Reporting that as
// verified is the defect decision 6b removed, and a phase boundary is not a
// reason to reintroduce it.

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

// FleetSignature is what this reader can say about a row's signature.
//
// Two values, and no third. `attest log` set the precedent and the reasoning is
// identical: whether a signature is *there* and whether it *checks out* are
// different questions, and a listing that conflated them would answer the
// second without having asked it.
type FleetSignature string

const (
	// FleetSigned means a signature was published beside the row. It says
	// nothing about whether it verifies, or by whom.
	FleetSigned FleetSignature = "signed"

	// FleetUnsigned means none was. A machine that has never minted a key
	// publishes rows without one, which is a state and not a fault.
	FleetUnsigned FleetSignature = "unsigned"
)

// FleetListOptions selects the target and the staleness threshold.
type FleetListOptions struct {
	TargetOptions

	// StaleAfter is the age at which a row is called stale. Zero takes
	// DefaultFleetStaleAfter; negative disables the judgement entirely,
	// which is what a reader who does not want one should be able to say.
	StaleAfter time.Duration
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

	// Limitations is what this reader could not do. Never empty in this
	// phase -- see the package comment above -- and read by a human rather
	// than a machine, which is why it is prose.
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

	// Row is the payload, nil when it could not be read or would not
	// validate. A row this manager refused is refused whole: reading the
	// fields it happens to recognise out of a document it does not
	// understand produces something that looks complete and is not.
	Row *domain.FleetRow `json:"row,omitempty"`

	Signature FleetSignature `json:"signature"`

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
		Limitations: []string{
			// One cause, said as one cause. Splitting these into two
			// unrelated notes would let a reader fix one impression
			// -- "I should pass a roster to check signatures" --
			// without ever learning the other.
			"no roster was given, so no row below is authenticated and no absent " +
				"installation can be shown; both need the roster that binds an " +
				"installation id to a public key",
		},
	}

	stale := opts.StaleAfter
	if stale == 0 {
		stale = DefaultFleetStaleAfter
	}
	if stale > 0 {
		report.StaleAfter = stale.String()
	}

	now := d.now()
	for _, target := range targets {
		report.Targets = append(report.Targets, target.String())
		report.Rows = append(report.Rows, d.fleetRowsOn(ctx, target, now, stale)...)
	}

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

// fleetRowsOn reads one target.
//
// Every failure below produces a row rather than an error. A target that cannot
// be listed at all is the exception -- there are no rows to carry the problem,
// so it becomes one row of its own naming the target.
func (d *Deps) fleetRowsOn(
	ctx context.Context, target ports.TargetRef, now time.Time, stale time.Duration,
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

		out = append(out, d.fleetRowAt(ctx, target, key, now, stale, present))
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
	now time.Time,
	stale time.Duration,
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

	if present[key+fleetSigExt] {
		// Present, and nothing more is claimed. See FleetSignature.
		status.Signature = FleetSigned
	}

	body, err := d.Objects.GetObject(ctx, target, key)
	if err != nil {
		// The adapter's message quotes the key it was asked for.
		status.Problem = domain.BoundedText(domain.AsError(err).Message)
		return status
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
	// A check that costs nothing and is the only integrity statement this
	// phase can make without a roster: a row claiming to be a different
	// installation from the one whose key it occupies was either published
	// to the wrong place or put there by somebody else. Neither is a row to
	// display as that installation's status.
	//
	// **Compared before the row is bounded**, and the order is the check.
	// Bounding drops control characters, so a row naming `demo\u001b` would
	// become `demo` and match a key it does not belong at -- sanitising
	// first would hand an attacker a way through the one check this phase
	// has. The message is bounded instead, since it quotes what it refused.
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
	status.Age = fleetAge(row.Age(now))
	status.Stale = row.Stale(now, stale)
	return status
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
