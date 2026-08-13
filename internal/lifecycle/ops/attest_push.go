package ops

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// Statements leaving the machine (RFC 0025 §4.6).
//
// Local first, pushed second, and **a failed push does not fail the
// operation** -- which is the exact inverse of what RFC 0009 does for a backup,
// so the asymmetry is worth stating rather than discovering:
//
//   - A backup that did not leave the machine is a data-loss risk that has
//     already materialised. Reporting success for data that is only on the
//     doomed disk is what pushing exists to prevent, so a failed push fails the
//     backup.
//   - An attestation that did not leave is a gap in a *record* whose subject --
//     the deployment -- is fine, and whose local copy is still there to push
//     again. Failing an update because a log shipper was down would stop the
//     security fix an operator is applying, for bookkeeping.
//
// So the gap is reported instead: by a warning at the time, by `doctor` until
// it is closed, and by `morzer attest push` which closes it.

// attestationPrefix is the key statements are written under on a target.
//
// A directory of its own beside the backups, and it cannot be confused with
// one: `backup list` reports a directory only when it holds a `backup.json`,
// and retention removes only ids that listing produced. Nothing here writes a
// manifest, so an attestation prefix is invisible to every backup path -- which
// is the property RFC 0025 decision 5 wanted, arrived at by keeping the two
// interfaces separate rather than by a rule somebody has to remember.
const attestationPrefix = "attestations"

// pushStatement copies one statement, and its signature, to every target.
//
// Called from the emission path, where nothing may fail: the caller has already
// written the record locally and returning an error here would turn a
// bookkeeping problem into a failed operation.
func pushStatement(ctx context.Context, d *Deps, inst domain.Installation, file string) {
	targets, err := d.attestationTargets(ctx, inst)
	if err != nil {
		d.warnf("cannot resolve where attestations are pushed: %s", domain.AsError(err).Message)
		return
	}
	if len(targets) == 0 {
		return
	}

	for _, target := range targets {
		// Every target is attempted. Stopping at the first failure hides
		// the state of the rest, so an operator fixing one unreachable
		// target cannot tell whether the others worked -- the same
		// lesson the pre-update backup push already learned.
		if err := pushOne(ctx, d, target, file); err != nil {
			d.warnf("the attestation for %s is on this machine but did not reach %s: %s. "+
				"Run `morzer attest push` once it is reachable",
				filepath.Base(file), target, domain.AsError(err).Message)
		}
	}
}

// pushOne copies a statement and whatever signature sits beside it.
//
// The signature goes second, on the same reasoning that puts a backup's
// manifest last: a transfer interrupted between the two leaves a statement
// somebody can read and cannot check, which is honest, rather than a signature
// over bytes that are not there, which is not.
func pushOne(ctx context.Context, d *Deps, target ports.TargetRef, file string) error {
	body, err := os.ReadFile(file)
	if err != nil {
		return domain.Internal(err, "cannot read %s", file)
	}
	if err := d.Objects.PutObject(ctx, target, objectKeyFor(file), body); err != nil {
		return err
	}

	sig, err := os.ReadFile(file + minisigExt)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// No signature is a state, not a failure: a machine with no key
		// files unsigned statements, and refusing to push them would
		// leave the machines with the least evidence keeping all of it.
		return nil
	case err != nil:
		// Anything else -- a permission, a short read -- is a signature
		// that exists and did not go. Reported, because the alternative
		// is a target holding a document nobody can check while this
		// command says it pushed one.
		return domain.Internal(err, "cannot read the signature beside %s", file)
	}
	return d.Objects.PutObject(ctx, target, objectKeyFor(file)+minisigExt, sig)
}

// minisigExt is the suffix of a detached minisign signature.
const minisigExt = ".minisig"

// objectKeyFor is where a local statement lives on a target.
func objectKeyFor(file string) string {
	return path.Join(attestationPrefix, filepath.Base(file))
}

// attestationTargets resolves where this installation's statements are pushed.
//
// The same targets its backups go to (RFC 0025 decision 5), so there is one
// list to configure and one to keep reachable. Empty when the installation
// configures none, or when this build wires no object store -- both are
// ordinary states, and neither is an error to a caller that is only asking.
func (d *Deps) attestationTargets(
	ctx context.Context, inst domain.Installation,
) ([]ports.TargetRef, error) {
	if d.Objects == nil || !inst.Backup.HasTargets() {
		return nil, nil
	}
	return d.resolveTargets(ctx, inst)
}

// AttestPushReport is what `attest push` and `doctor` both need to know.
type AttestPushReport struct {
	// Targets is one entry per configured target.
	Targets []AttestTargetStatus `json:"targets"`

	// Local is how many statements this machine holds.
	Local int `json:"local"`
}

// AttestTargetStatus is one target's share of it.
type AttestTargetStatus struct {
	URL string `json:"url"`

	// Missing is how many local statements this target does not hold
	// completely -- the document, and the signature when there is one.
	Missing int `json:"missing"`

	// Pushed is how many this run copied.
	Pushed int `json:"pushed"`

	// Error is why the target could not be read or written, when it could
	// not. A target that cannot be reached is a finding to report, never a
	// reason to fail the command that asked.
	Error string `json:"error,omitempty"`

	// ref and missing are what the push needs and the report does not
	// publish: the resolved target, credentials attached, and the keys it
	// turned out not to have.
	//
	// Carried here rather than recomputed, so the listing and the push
	// cannot end up talking about different targets -- and so a credential
	// is resolved once per run rather than once per phase.
	ref     ports.TargetRef
	missing map[string]bool
}

// Unreachable reports whether anything went wrong anywhere.
func (r AttestPushReport) Unreachable() bool {
	for _, t := range r.Targets {
		if t.Error != "" {
			return true
		}
	}
	return false
}

// Missing totals the statements that are not on some target.
func (r AttestPushReport) Missing() int {
	var n int
	for _, t := range r.Targets {
		n += t.Missing
	}
	return n
}

// AttestPush copies every statement that is not on a target to it.
//
// The remedy `doctor` names, and the reason the emission path is allowed to
// warn rather than fail: a push that did not happen has to be closable
// afterwards, or the warning is an instruction to do something impossible.
//
// Read-only against the local directory, and idempotent: what is already there
// is not sent again, which is what makes it safe to run from cron on an
// installation whose target is usually up.
func AttestPush(ctx context.Context, d *Deps, opts Options) (Result, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return Result{}, err
	}
	if d.Objects == nil {
		return Result{}, domain.Internal(nil, "no target registry was wired")
	}
	if !inst.Backup.HasTargets() {
		return Result{}, domain.Usage("this installation configures no targets").
			WithHint("add one with `morzer backup target add file:///mnt/backups`; " +
				"attestations go to the same places backups do")
	}

	report, err := d.attestationPushState(ctx, inst)
	if err != nil {
		return Result{}, err
	}

	if opts.DryRun {
		return Result{
			Summary: pushSummary(report, true),
			Data:    report,
		}, nil
	}

	local, err := localStatements(d.Paths.AttestationsDir())
	if err != nil {
		return Result{}, err
	}

	for i := range report.Targets {
		status := &report.Targets[i]
		if status.Error != "" {
			continue
		}
		for _, file := range local {
			if !status.missing[objectKeyFor(file)] {
				continue
			}
			if err := pushOne(ctx, d, status.ref, file); err != nil {
				status.Error = domain.AsError(err).Message
				break
			}
			status.Pushed++
			status.Missing--
		}
	}

	return Result{Summary: pushSummary(report, false), Data: report}, nil
}

// attestationPushState reads what each target already holds.
func (d *Deps) attestationPushState(
	ctx context.Context, inst domain.Installation,
) (AttestPushReport, error) {
	local, err := localStatements(d.Paths.AttestationsDir())
	if err != nil {
		return AttestPushReport{}, err
	}

	report := AttestPushReport{Local: len(local)}

	// Which statements this machine has a signature for, read once. Asking
	// the filesystem again per target would let two targets disagree about
	// the same local file -- an operation finishing mid-run writes one, and
	// the target listed after that would count it while the one before did
	// not, from a single command.
	signed := make(map[string]bool, len(local))
	for _, file := range local {
		signed[file] = hasSignature(file)
	}

	for _, cfg := range inst.Backup.Targets {
		status := AttestTargetStatus{URL: cfg.URL, missing: map[string]bool{}}

		target, err := d.resolveTarget(ctx, cfg)
		if err != nil {
			status.Error = domain.AsError(err).Message
			report.Targets = append(report.Targets, status)
			continue
		}

		status.ref = target

		keys, err := d.Objects.ObjectKeys(ctx, target, attestationPrefix)
		if err != nil {
			status.Error = domain.AsError(err).Message
			report.Targets = append(report.Targets, status)
			continue
		}

		there := make(map[string]bool, len(keys))
		for _, key := range keys {
			there[key] = true
		}
		for _, file := range local {
			// The signature counts as well as the document, and
			// only because this machine has one to send. Counting
			// the document alone left the gap this command's own
			// failure mode creates: `pushOne` writes the document
			// first, so a transfer that died between the two put a
			// statement on the target and not its signature --
			// after which the document was there, nothing was
			// missing, `doctor` said all clear, and the record an
			// auditor holds could never be checked.
			//
			// A machine with no local signature asks for nothing,
			// which keeps the count reachable on an installation
			// that signs nothing at all.
			key := objectKeyFor(file)
			if !there[key] || (signed[file] && !there[key+minisigExt]) {
				status.missing[key] = true
				status.Missing++
			}
		}
		report.Targets = append(report.Targets, status)
	}
	return report, nil
}

// localStatements lists the statements on this machine, oldest name first.
//
// A directory that is not there holds none. That is every installation which
// has not run an operation since attestations existed, and it is not a failure
// to report to somebody who only asked how many there were.
func localStatements(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, domain.Internal(err, "cannot read %s", dir)
	}

	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	sort.Strings(out)
	return out, nil
}

// pushSummary says what happened, in the tense it happened in.
func pushSummary(r AttestPushReport, dry bool) string {
	switch {
	case r.Local == 0:
		return "there are no attestations on this machine to push"
	case dry && r.Missing() == 0:
		return fmt.Sprintf("every one of %d attestation(s) is already on %s",
			r.Local, describeTargetCount(len(r.Targets)))
	case dry:
		return fmt.Sprintf("would push %d attestation(s) to %s",
			r.Missing(), describeTargetCount(len(r.Targets)))
	}

	var pushed int
	for _, t := range r.Targets {
		pushed += t.Pushed
	}
	if pushed == 0 && !r.Unreachable() {
		return fmt.Sprintf("every one of %d attestation(s) is already on %s",
			r.Local, describeTargetCount(len(r.Targets)))
	}
	summary := fmt.Sprintf("pushed %d attestation(s) to %s",
		pushed, describeTargetCount(len(r.Targets)))
	if r.Unreachable() {
		summary += "; some targets did not answer"
	}
	return summary
}
