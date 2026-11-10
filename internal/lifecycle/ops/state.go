package ops

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/agecrypt"
	"github.com/morzecrew/morzer/internal/ports"
)

// The authority's side of desired state (RFC 0057 §5.11).
//
// Commands that need no installation of their own, as `fleet ls` needs none:
// they run on the machine that holds the authority's key, read the fleet rows
// the managed machines publish, and write documents where those machines look.
//
// **The roster is the only trust anchor** (decision 13). A row carries the key
// that signed it and the recipient to encrypt to, and another machine with
// write access to the bucket can post a row of its own for any id. So a
// document is encrypted only to the recipient of a row that verifies against
// the key the roster lists for that installation, and never to one a row
// vouches for itself.

const (
	// DefaultDesiredStateValidity is how long a document stays valid when
	// the authority does not say (decision 9).
	DefaultDesiredStateValidity = 7 * 24 * time.Hour

	// desiredStateKept is how many complete pairs a publish leaves behind
	// it (§5.3): the new one and the two before it.
	desiredStateKept = 3
)

// StatePublishOptions are `state publish`'s.
type StatePublishOptions struct {
	TargetOptions

	// Roster is the expected population, and the trust anchor. Required.
	Roster domain.FleetRoster

	Product        string
	InstallationID string

	// Document is the change the authority sends: a change document's
	// sections, without the envelope this command adds. It is cumulative,
	// every value the authority manages for the installation (decision 24).
	Document []byte

	// Signer signs with the authority's key.
	Signer ports.Signer

	// Serial is the document's serial. Zero takes the time in
	// milliseconds (decision 18).
	Serial int64

	// ValidFor is how long the document stays valid. Zero takes
	// DefaultDesiredStateValidity.
	ValidFor time.Duration

	// Recipients are encrypted to as well as the machine, such as the
	// authority's own, so it can read back what it sent.
	Recipients []string
}

// StatePublishReport is what `state publish` returns, and the `--json` shape.
type StatePublishReport struct {
	Target         string      `json:"target"`
	Product        string      `json:"product"`
	InstallationID string      `json:"installation_id"`
	Serial         int64       `json:"serial"`
	IssuedAt       domain.Time `json:"issued_at"`
	ExpiresAt      domain.Time `json:"expires_at"`

	// Document and Signature are the two keys, written in that order.
	Document  string `json:"document"`
	Signature string `json:"signature"`

	// Recipient is the machine's, from its verified row.
	Recipient      string `json:"recipient"`
	AuthorityKeyID string `json:"authority_key_id,omitempty"`
	Bytes          int    `json:"bytes"`

	// Pruned are the serials removed behind the new pair, and
	// PruneProblem why the prune stopped, when it did. A prune that fails
	// fails nothing: the document is published, and the next publish
	// prunes again.
	Pruned       []int64 `json:"pruned,omitempty"`
	PruneProblem string  `json:"prune_problem,omitempty"`

	DryRun bool `json:"dry_run,omitempty"`
}

// StatePublish encrypts a document to one installation and writes it, with its
// signature, where that installation looks.
func StatePublish(ctx context.Context, d *Deps, opts StatePublishOptions) (StatePublishReport, error) {
	report := StatePublishReport{Product: opts.Product, InstallationID: opts.InstallationID, DryRun: opts.DryRun}
	if d.Objects == nil || opts.Signer == nil {
		return report, domain.Internal(nil, "state publish needs a target registry and a signer")
	}
	if opts.URL == "" {
		return report, domain.Usage("state publish needs --target").
			WithHint("the target the machines publish their fleet rows to")
	}
	entry, ok := opts.Roster.Entry(opts.Product, opts.InstallationID)
	switch {
	case !opts.Roster.Given():
		return report, domain.Usage("state publish needs --expect, the roster").
			WithHint("a document is encrypted only to a machine whose row verifies against the key the roster lists")
	case !ok:
		return report, domain.Usage("the roster does not list %s %s", opts.Product, opts.InstallationID).
			WithHint("add it with the key its row signs with; a machine that is not in the roster is refused")
	case entry.PublicKey == "":
		return report, domain.Usage("the roster lists %s %s without a key", opts.Product, opts.InstallationID).
			WithHint("add the key its row signs with: `morzer fleet publish --dry-run --json` on that machine prints it")
	}
	if _, err := ParseConfigChange(opts.Document); err != nil {
		return report, err
	}
	for _, r := range opts.Recipients {
		if err := agecrypt.ValidateRecipient(r); err != nil {
			return report, domain.Usage("%q is not an age recipient", r)
		}
	}

	now := d.now()
	serial := opts.Serial
	if serial == 0 {
		serial = now.UnixMilli()
	}
	if serial < 0 {
		return report, domain.Usage("--serial must be a positive whole number")
	}
	validFor := opts.ValidFor
	if validFor == 0 {
		validFor = DefaultDesiredStateValidity
	}
	if validFor < 0 {
		return report, domain.Usage("--valid-for must be positive")
	}
	report.Serial = serial
	report.IssuedAt = domain.NewTime(now)
	report.ExpiresAt = domain.NewTime(now.Add(validFor))

	targets, err := d.targetsFor(ctx, opts.TargetOptions)
	if err != nil {
		return report, err
	}
	target := targets[0]
	report.Target = target.String()

	// The serial against what is published: a pair is written once, and
	// one not newer than the newest the machine already has would be
	// refused there as a replay.
	prefix := domain.DesiredStatePrefix(opts.Product, opts.InstallationID)
	listed, err := d.Objects.ObjectKeys(ctx, target, strings.TrimSuffix(prefix, "/"))
	if err != nil {
		return report, err
	}
	if newest, found := newestCompletePair(prefix, listed); found && serial <= newest {
		return report, domain.Usage("serial %d is not newer than document %d already published", serial, newest).
			WithHint("leave --serial out to take the time in milliseconds")
	}
	report.Document, report.Signature = domain.DesiredStateKeys(opts.Product, opts.InstallationID, serial)
	if slices.Contains(listed, report.Document) || slices.Contains(listed, report.Signature) {
		return report, domain.Usage("serial %d is already published, and a pair is never replaced", serial)
	}

	row, err := d.verifiedRow(ctx, target, opts.Roster, opts.Product, opts.InstallationID)
	if err != nil {
		return report, err
	}
	if row.AgeRecipient == "" || agecrypt.ValidateRecipient(row.AgeRecipient) != nil {
		return report, domain.Usage("the row of %s %s carries no age recipient", opts.Product, opts.InstallationID).
			WithHint("it was published by a manager older than desired state; upgrade that machine")
	}
	report.Recipient = row.AgeRecipient

	plain := desiredStateDocument(opts.Product, opts.InstallationID, serial, now, now.Add(validFor), opts.Document)
	if _, _, err := ParseDesiredState(plain); err != nil {
		return report, err
	}
	var sealed bytes.Buffer
	if err := agecrypt.Encrypt(&sealed, bytes.NewReader(plain), append([]string{row.AgeRecipient}, opts.Recipients...)); err != nil {
		return report, err
	}
	report.Bytes = sealed.Len()
	if sealed.Len() > ports.MaxObjectBytes {
		// The machine reads a document back through GetObject, which
		// refuses more (decision 20).
		return report, domain.Usage("the encrypted document is %d bytes, over the %d a machine reads",
			sealed.Len(), ports.MaxObjectBytes)
	}
	sig, err := opts.Signer.Sign(ctx, sealed.Bytes(), domain.DesiredStateComment(opts.Product, opts.InstallationID, serial))
	if err != nil {
		return report, err
	}
	report.AuthorityKeyID = sig.KeyID
	if opts.DryRun {
		return report, nil
	}

	// The ciphertext first and its signature last: until the signature is
	// there, the pair is not a document, so a publish cut off part way
	// leaves the previous one in force (decision 26).
	if err := d.Objects.PutObject(ctx, target, report.Document, sealed.Bytes()); err != nil {
		return report, err
	}
	if err := d.Objects.PutObject(ctx, target, report.Signature, sig.Encoded); err != nil {
		return report, err
	}
	report.Pruned, report.PruneProblem = d.pruneDesiredState(ctx, target, opts.Product, opts.InstallationID)
	return report, nil
}

// verifiedRow is an installation's row, only when it verifies against the key
// the roster lists for it.
func (d *Deps) verifiedRow(
	ctx context.Context, target ports.TargetRef, roster domain.FleetRoster, product, id string,
) (domain.FleetRow, error) {
	key, err := domain.FleetKey(product, id)
	if err != nil {
		return domain.FleetRow{}, err
	}
	listed, err := d.Objects.ObjectKeys(ctx, target, strings.TrimSuffix(key, "/"+domain.FleetFileName))
	if err != nil {
		return domain.FleetRow{}, err
	}
	present := map[string]bool{}
	for _, k := range listed {
		present[k] = true
	}
	if !present[key] {
		return domain.FleetRow{}, domain.Usage("no fleet row is published for %s %s on %s", product, id, target).
			WithHint("the machine publishes one with `morzer fleet publish`, to a target it backs up to")
	}
	status := d.fleetRowAt(ctx, target, key, fleetReading{now: d.now(), stale: -1, roster: roster}, present)
	if status.Signature != FleetVerified || status.Row == nil {
		problem := status.Problem
		if problem == "" {
			problem = string(status.Signature)
		}
		return domain.FleetRow{}, domain.Usage("the row of %s %s does not verify against the roster's key: %s",
			product, id, problem).
			WithHint("a document is encrypted only to a machine whose row the roster's key signed")
	}
	return *status.Row, nil
}

// desiredStateDocument is the envelope around the authority's change.
func desiredStateDocument(product, id string, serial int64, issued, expires time.Time, change []byte) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "api_version: %s\nkind: %s\nproduct: %s\ninstallation: %s\nserial: %d\n",
		domain.APIVersionV1Alpha1, domain.DesiredStateKind, product, id, serial)
	fmt.Fprintf(&b, "issued_at: %s\nexpires_at: %s\n",
		issued.UTC().Format(time.RFC3339), expires.UTC().Format(time.RFC3339))
	b.Write(change)
	return b.Bytes()
}

// pruneDesiredState removes every object below the newest desiredStateKept
// complete pairs: each pair's signature first, so a pair half removed is not
// a document, and lone objects below them too.
func (d *Deps) pruneDesiredState(ctx context.Context, target ports.TargetRef, product, id string) ([]int64, string) {
	prefix := domain.DesiredStatePrefix(product, id)
	listed, err := d.Objects.ObjectKeys(ctx, target, strings.TrimSuffix(prefix, "/"))
	if err != nil {
		return nil, domain.AsError(err).Message
	}
	seen, docs, sigs := map[int64]bool{}, map[int64]bool{}, map[int64]bool{}
	for _, key := range listed {
		if serial, sig, ok := domain.ParseDesiredStateKey(prefix, key); ok {
			seen[serial] = true
			if sig {
				sigs[serial] = true
			} else {
				docs[serial] = true
			}
		}
	}
	var complete []int64
	for serial := range docs {
		if sigs[serial] {
			complete = append(complete, serial)
		}
	}
	if len(complete) <= desiredStateKept {
		return nil, ""
	}
	slices.Sort(complete)
	cutoff := complete[len(complete)-desiredStateKept]

	var pruned []int64
	for _, serial := range slices.Sorted(maps.Keys(seen)) {
		if serial >= cutoff {
			continue
		}
		doc, sig := domain.DesiredStateKeys(product, id, serial)
		for _, key := range []string{sig, doc} {
			if err := d.Objects.DeleteObject(ctx, target, key); err != nil {
				return pruned, domain.AsError(err).Message
			}
		}
		pruned = append(pruned, serial)
	}
	return pruned, ""
}

// StateListOptions are `state ls`'s.
type StateListOptions struct {
	TargetOptions

	// Roster is the expected population and the trust anchor, as for
	// `fleet ls`: a row's sync outcome is the machine's only when the row
	// verifies against the key the roster lists.
	Roster domain.FleetRoster
}

// StateReport is what `state ls` answers, and the `--json` shape.
type StateReport struct {
	Targets     []string   `json:"targets"`
	Rows        []StateRow `json:"rows"`
	Limitations []string   `json:"limitations"`
}

// StateRow is one installation: the newest document published for it, and
// what its row says it did with documents.
type StateRow struct {
	Target         string         `json:"target,omitempty"`
	Product        string         `json:"product,omitempty"`
	InstallationID string         `json:"installation_id,omitempty"`
	Signature      FleetSignature `json:"signature,omitempty"`

	// Expected and Absent are `fleet ls`'s: the roster names it, and no
	// target holds its row. A row the roster does not name is shown as
	// unexpected rather than dropped.
	Expected bool `json:"expected,omitempty"`
	Absent   bool `json:"absent,omitempty"`

	// Published is the newest complete pair on the target, zero when none.
	Published int64 `json:"published,omitempty"`

	// Sync is the row's block, present only on a row this run read.
	Sync *domain.FleetSync `json:"sync,omitempty"`

	Problem string `json:"problem,omitempty"`
}

// StateList reads every row on a target, with the newest document published
// for each installation.
func StateList(ctx context.Context, d *Deps, opts StateListOptions) (StateReport, error) {
	fleet, err := FleetList(ctx, d, FleetListOptions{TargetOptions: opts.TargetOptions, Roster: opts.Roster, StaleAfter: -1})
	if err != nil {
		return StateReport{}, err
	}
	targets, err := d.targetsFor(ctx, opts.TargetOptions)
	if err != nil {
		return StateReport{}, err
	}
	byName := map[string]ports.TargetRef{}
	for _, t := range targets {
		byName[t.String()] = t
	}

	report := StateReport{Targets: fleet.Targets, Limitations: fleet.Limitations}
	for _, f := range fleet.Rows {
		row := StateRow{
			Target: f.Target, Product: f.Product, InstallationID: f.InstallationID,
			Signature: f.Signature, Expected: f.Expected, Absent: f.Absent, Problem: f.Problem,
		}
		if f.Row != nil {
			row.Sync = f.Row.Sync
		}
		if target, ok := byName[f.Target]; ok && f.Product != "" && f.InstallationID != "" {
			prefix := domain.DesiredStatePrefix(f.Product, f.InstallationID)
			if listed, err := d.Objects.ObjectKeys(ctx, target, strings.TrimSuffix(prefix, "/")); err == nil {
				row.Published, _ = newestCompletePair(prefix, listed)
			} else if row.Problem == "" {
				row.Problem = domain.BoundedText(domain.AsError(err).Message)
			}
		}
		report.Rows = append(report.Rows, row)
	}
	return report, nil
}
