package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"

	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// Publishing one row per installation (RFC 0026 P1).
//
// The whole feature in one sentence: after this runs, a stable key on a target
// this installation already uses holds a small document saying what is deployed
// here and whether it is working. Nothing listens, nothing is polled, and no
// machine accepts an inbound connection -- which is decision 1, and the reason
// the RFC exists at all.
//
// **A failed publish does not fail anything**, on the same reasoning that makes
// a failed attestation push a warning: a row that did not leave is a gap in a
// view whose subject -- the deployment -- is fine, and the local machine is
// still the source of truth for everything the row would have said. This
// command reports per target instead, which is what makes it usable from cron.

// fleetSigExt is the suffix of the detached signature published beside a row.
//
// The same extension attestations use, and the same detached shape, so
// `minisign -Vm status.json -P <key>` is the check -- one gesture for both
// artifacts rather than a second thing to learn.
const fleetSigExt = minisigExt

// FleetPublishOptions selects where this installation's row goes.
type FleetPublishOptions struct {
	TargetOptions
}

// FleetPublishReport is what `fleet publish` returns, and the `--json` shape.
type FleetPublishReport struct {
	// Row is exactly what was published, so `--dry-run` shows the document
	// that would leave rather than a description of it.
	Row domain.FleetRow `json:"row"`

	// Key is where it goes on every target.
	Key string `json:"key"`

	// Signed says a signature was published beside it. False on a machine
	// that has never minted a key, which is a state and not a failure.
	Signed bool `json:"signed"`

	Targets []FleetPublishTarget `json:"targets"`
}

// FleetPublishTarget is one target's share of it.
type FleetPublishTarget struct {
	URL string `json:"url"`

	// Published says the row reached this target.
	Published bool `json:"published"`

	// Declined is why this run left what was already there, when it did.
	// A newer row must not be replaced by an older one.
	Declined string `json:"declined,omitempty"`

	// Unchecked is why the ordering could not be established, when it could
	// not. **Not an error**: RFC 0026 §9 wants the credential on a managed
	// machine to be write-only, and a credential that cannot read is
	// exactly what that looks like from here. The row is published and the
	// reader is told the check did not happen.
	Unchecked string `json:"unchecked,omitempty"`

	// Error is why the row did not reach this target.
	Error string `json:"error,omitempty"`
}

// Unreachable reports whether any target refused the row.
func (r FleetPublishReport) Unreachable() bool {
	return slices.ContainsFunc(r.Targets, func(t FleetPublishTarget) bool { return t.Error != "" })
}

// FleetPublish writes this installation's row to every configured target.
//
// Not an engine operation: it mutates nothing on this machine, takes no lock
// and journals nothing, for the same reason `status` does not -- and because
// decision 8 says the row is derived entirely from what is already computed. If
// publishing ever needed to record something locally, that would be the signal
// the payload had grown into a record.
func FleetPublish(ctx context.Context, d *Deps, opts FleetPublishOptions) (Result, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return Result{}, err
	}
	if d.Objects == nil {
		return Result{}, domain.Internal(nil, "no target registry was wired")
	}

	key, err := domain.FleetKey(inst.Product, inst.ID)
	if err != nil {
		// Built from installation.yaml, and this is the guard before the
		// lookup rather than after it: the key names an object on a
		// target several machines write to, so a hand-edited product or
		// id must be refused here rather than resolved somewhere else.
		return Result{}, err
	}

	targets, err := d.targetsFor(ctx, opts.TargetOptions)
	if err != nil {
		return Result{}, err
	}

	row := d.fleetRow(ctx, inst, opts.DryRun)
	body, err := json.MarshalIndent(row, "", "  ")
	if err != nil {
		return Result{}, domain.Internal(err, "cannot serialise the fleet row")
	}
	body = append(body, '\n')

	report := FleetPublishReport{Row: row, Key: key}

	if opts.DryRun {
		// Not signed, and nothing is written. Signing is not itself a
		// change -- it reads a key and returns bytes -- but resolving
		// the key to sign with is: on a machine that has never signed,
		// that mints the identity every later signature is attributed
		// to. A plan does not create one, so a dry run reports whether
		// the row *would* be signed from the key that is already there.
		report.Signed = row.SigningKey != ""
		for _, target := range targets {
			report.Targets = append(report.Targets,
				FleetPublishTarget{URL: target.String()})
		}
		return Result{Summary: fleetPublishSummary(report, true), Data: report}, nil
	}

	// Signed over the bytes about to be written, never over the row
	// re-serialised by a reader (§3.6). A signature over "the JSON" is a
	// signature over whichever spelling of it the verifier reproduces --
	// key order, whitespace, escaping -- so it would need a canonical form
	// both ends implement identically. This has no such form to disagree
	// about: the publisher signs the bytes, the reader verifies before it
	// parses.
	sig := d.signFleetRow(ctx, inst, body)
	report.Signed = len(sig) > 0

	for _, target := range targets {
		status := FleetPublishTarget{URL: target.String()}

		verdict := d.fleetOrdering(ctx, target, key, row, opts.Force)
		if verdict.declined != "" {
			status.Declined = verdict.declined
			report.Targets = append(report.Targets, status)
			continue
		}
		status.Unchecked = verdict.unchecked

		published, err := d.putFleetRow(ctx, target, key, body, sig)
		status.Published = published
		if err != nil {
			status.Error = domain.AsError(err).Message
		}
		report.Targets = append(report.Targets, status)
	}

	return Result{Summary: fleetPublishSummary(report, false), Data: report}, nil
}

// putFleetRow writes the row and then its signature, and reports the two
// outcomes separately.
//
// The signature goes second, on the same reasoning that puts a backup's
// manifest last: a transfer interrupted between the two leaves a row somebody
// can read and cannot check, which is honest, rather than a signature over
// bytes that are not there, which is not.
//
// **published is whether the row reached the target, not whether everything
// worked**, and the two come apart in exactly one case. Returning a bare error
// collapsed them: a signature write that failed after the row landed left the
// report saying `published: false` about an object sitting on the target, which
// contradicts that field's own meaning. A `--json` consumer would conclude
// nothing was there, and an operator reading the count would go looking for a
// row they already have.
//
// The error still travels, so the run is not reported as clean and the exit
// status is still non-zero. What changes is that the report describes the
// target's actual state.
func (d *Deps) putFleetRow(
	ctx context.Context, target ports.TargetRef, key string, body, sig []byte,
) (published bool, err error) {
	if err := d.Objects.PutObject(ctx, target, key, body); err != nil {
		return false, err
	}
	if len(sig) == 0 {
		return true, nil
	}
	if err := d.Objects.PutObject(ctx, target, key+fleetSigExt, sig); err != nil {
		return true, err
	}
	return true, nil
}

// orderingVerdict is what the read-before-write concluded.
type orderingVerdict struct {
	// declined is why the existing row was left alone, when it was.
	declined string

	// unchecked is why no comparison was possible, when none was.
	unchecked string
}

// fleetOrdering reads the row already at the key and decides whether to replace
// it.
//
// The key is stable and the write replaces in place, so without this a slow
// publisher finishing after a fast one silently installs stale state as
// current. That is not hypothetical once P4's timer exists beside an operator
// typing `fleet publish`.
//
// **Every failure to read is a publish that happens anyway**, and that is the
// deliberate part. RFC 0026 §9 asks for a write-only, prefix-scoped credential
// on every managed machine, and a credential that cannot read is exactly what
// that looks like from inside this function. Refusing to publish would make the
// safer credential the one that breaks the feature, so the check is best-effort
// and the report says when it did not happen.
func (d *Deps) fleetOrdering(
	ctx context.Context, target ports.TargetRef, key string, row domain.FleetRow, force bool,
) orderingVerdict {
	if force {
		return orderingVerdict{unchecked: "--force was given, so nothing was compared"}
	}

	existing, err := d.Objects.GetObject(ctx, target, key)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// Nothing there. The state before a first publish, and the
		// state the whole feature starts in.
		return orderingVerdict{}
	case err != nil:
		return orderingVerdict{unchecked: domain.AsError(err).Message}
	}

	var prior domain.FleetRow
	if err := json.Unmarshal(existing, &prior); err != nil {
		// Not a row. This is *this installation's own key*, so whatever
		// is there is an older format, a truncated write, or somebody
		// else's bytes -- and in all three cases the current truth
		// about this machine is the better thing to have at this key.
		return orderingVerdict{unchecked: "what was there is not a fleet row"}
	}

	switch {
	case prior.Schema > domain.FleetSchemaVersion:
		// A newer manager published here and this one is older -- an
		// upgrade that was rolled back. Overwriting would silently
		// downgrade what a newer reader can see, so it is refused by
		// the same rule that refuses a future installation whole.
		return orderingVerdict{declined: fmt.Sprintf(
			"a row written by a newer manager is already there (schema %d, "+
				"this manager writes %d); re-run with --force to replace it",
			prior.Schema, domain.FleetSchemaVersion)}

	case prior.PublishedAt.After(row.PublishedAt.Time):
		return orderingVerdict{declined: fmt.Sprintf(
			"a newer row is already there (published %s, this one %s); "+
				"re-run with --force to replace it",
			prior.PublishedAt.Time.UTC().Format("2006-01-02T15:04:05Z"),
			row.PublishedAt.Time.UTC().Format("2006-01-02T15:04:05Z"))}
	}
	return orderingVerdict{}
}

// signFleetRow signs the bytes as published, or returns nothing.
//
// Nothing is a state rather than a failure, and the row is published anyway:
// a machine that has never minted a key produces exactly the row that most
// needs to be visible, and withholding it for want of a signature would hide
// the installations with the least evidence. `fleet ls` reports an unsigned row
// as unsigned, which is the honest outcome.
func (d *Deps) signFleetRow(ctx context.Context, inst domain.Installation, body []byte) []byte {
	if d.Signer == nil {
		return nil
	}
	sig, err := d.Signer.Sign(ctx, body,
		fmt.Sprintf("morzer fleet %s %s", inst.Product, inst.ID))
	if err != nil {
		d.warnf("this row is published unsigned: %s", domain.AsError(err).Message)
		return nil
	}
	return sig.Encoded
}

// fleetPublishSummary says what happened, in the tense it happened in.
func fleetPublishSummary(r FleetPublishReport, dry bool) string {
	if len(r.Targets) == 0 {
		return "this installation configures no targets, so the row went nowhere"
	}

	if dry {
		return fmt.Sprintf("would publish %s to %s",
			r.Key, describeTargetCount(len(r.Targets)))
	}

	// A target that took the row and refused its signature is counted as
	// published *and* named as a problem. The two are independent -- see
	// putFleetRow -- and a switch that treated any error as "not published"
	// would put the summary back at odds with the field it is summarising.
	var published, declined int
	var silent, unsigned []string

	for _, t := range r.Targets {
		switch {
		case t.Declined != "":
			declined++
		case t.Published:
			published++
		}

		switch {
		case t.Error != "" && t.Published:
			unsigned = append(unsigned, t.URL)
		case t.Error != "":
			silent = append(silent, t.URL)
		}
	}

	// The count that leads is the one an operator acts on. A run that
	// published nothing because every target already held something newer
	// is a different sentence from one that published nothing because
	// nothing answered, and reading the second as the first is how a fleet
	// view quietly stops updating.
	summary := fmt.Sprintf("published to %d of %s", published, describeTargetCount(len(r.Targets)))
	if declined > 0 {
		summary += fmt.Sprintf("; %d already held a row this one must not replace", declined)
	}
	if len(unsigned) > 0 {
		summary += "; the row reached " + strings.Join(unsigned, ", ") +
			" and its signature did not"
	}
	if len(silent) > 0 {
		summary += "; " + strings.Join(silent, ", ") + " did not answer"
	}
	return summary
}
