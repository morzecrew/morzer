package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
)

// `morzer attest verify` — reading the record back.
//
// Three questions, answered separately because they fail for different reasons
// and an operator acts on them differently:
//
//   - **Signature.** Did a key this installation knows about produce these
//     bytes, and was it the current one or a retired one?
//   - **Chain.** Do the version-moving statements join up, or did something
//     install a release without filing a record?
//   - **Live** (`--against-live`). Does the deployment running right now match
//     the newest successful statement?
//
// The third is the one RFC 0025 decision 8 makes the design conditional on. A
// verifier that can only pass is a verifier that proves nothing, so `--against-live`
// exists to be able to fail for a reason nobody planted -- and the acceptance
// suite injects exactly that.

// VerifyOptions is what `attest verify` needs.
type VerifyOptions struct {
	Options

	// Path is a statement file or a directory of them. Empty means the
	// installation's own attestation directory.
	Path string

	// AgainstLive compares the newest successful statement with the
	// deployment that is running.
	AgainstLive bool
}

// StatementReport is one statement's verdict.
type StatementReport struct {
	File      string `json:"file"`
	Operation string `json:"operation,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Outcome   string `json:"outcome,omitempty"`

	Signature domain.SignatureResult `json:"signature"`

	// Unreadable marks a file that is not a statement at all, kept in the
	// report rather than skipped: a directory of audit records containing
	// something unparseable is itself a finding.
	Unreadable string `json:"unreadable,omitempty"`
}

// VerifyReport is what the command prints.
type VerifyReport struct {
	Statements []StatementReport     `json:"statements"`
	Chain      []domain.ChainBreak   `json:"chain_breaks,omitempty"`
	Live       []domain.LiveMismatch `json:"live_mismatches,omitempty"`

	// LiveChecked distinguishes "compared and found nothing" from "did not
	// compare", which an empty mismatch list cannot.
	LiveChecked bool `json:"live_checked"`

	// LiveAgainst names the statement the live comparison used.
	LiveAgainst string `json:"live_against,omitempty"`
}

// Problems reports whether anything in the report needs a human.
//
// A predecessor signature is **not** a problem: it is a machine that was
// rebuilt or rotated, which is a normal history and the whole reason that
// outcome is distinct rather than folded into failure. What is a problem is a
// signature no key accounts for, a broken chain, or a live mismatch.
func (r VerifyReport) Problems() int {
	n := len(r.Chain) + len(r.Live)
	for _, s := range r.Statements {
		if s.Unreadable != "" || s.Signature.Outcome == domain.Unverifiable {
			n++
		}
	}
	return n
}

// AttestVerify checks the statements on this machine, or at a path.
func AttestVerify(ctx context.Context, d *Deps, opts VerifyOptions) (VerifyReport, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return VerifyReport{}, err
	}

	path := opts.Path
	if path == "" {
		path = d.Paths.AttestationsDir()
	}

	files, err := statementFiles(path)
	if err != nil {
		return VerifyReport{}, err
	}
	if len(files) == 0 {
		return VerifyReport{}, domain.InstallationError(domain.ErrNotFound, "no attestations at %s", path).
			WithHint("statements are written after each operation; " +
				"an installation that has not run one since upgrading has none")
	}

	report := VerifyReport{}
	parsed := make([]domain.Statement, 0, len(files))

	for _, file := range files {
		item := StatementReport{File: file}

		body, err := os.ReadFile(file)
		if err != nil {
			item.Unreadable = domain.AsError(err).Message
			report.Statements = append(report.Statements, item)
			continue
		}

		var stmt domain.Statement
		if err := json.Unmarshal(body, &stmt); err != nil {
			item.Unreadable = "not a JSON statement: " + err.Error()
			report.Statements = append(report.Statements, item)
			continue
		}
		if stmt.PredicateType != domain.PredicateType {
			// Refused by name rather than verified anyway. A
			// document of another predicate type may be a perfectly
			// good attestation of something else, and reporting on
			// it as though it described this deployment is the
			// confusion the predicate type exists to prevent.
			item.Unreadable = fmt.Sprintf("predicate type %q is not %s",
				stmt.PredicateType, domain.PredicateType)
			report.Statements = append(report.Statements, item)
			continue
		}

		item.Operation = stmt.Predicate.Operation.ID
		item.Kind = stmt.Predicate.Operation.Kind
		item.Outcome = stmt.Predicate.Operation.Outcome

		signature, hasSig := readSignature(file)
		item.Signature = domain.VerifySignature(inst.Signing, hasSig, func(key string) bool {
			return d.Checker != nil && d.Checker.Check(body, signature, key)
		})

		parsed = append(parsed, stmt)
		report.Statements = append(report.Statements, item)
	}

	report.Chain = domain.VerifyChain(parsed)

	if opts.AgainstLive {
		newest, ok := newestSucceeded(parsed)
		if !ok {
			return report, domain.InstallationError(domain.ErrNotFound,
				"no successful statement to compare the deployment against").
				WithHint("--against-live needs one operation that succeeded; " +
					"every statement here records a failure")
		}
		rel, err := d.currentReleaseFor(ctx)
		if err != nil {
			return report, err
		}
		live, err := d.liveImages(ctx, inst, rel)
		if err != nil {
			return report, err
		}
		report.LiveChecked = true
		report.LiveAgainst = newest.Predicate.Operation.ID
		report.Live = domain.CompareToLive(newest, live)

		// Two comparisons, one list. They answer the same question about
		// different halves of a deployment -- what is running, and what
		// it was configured with -- and an operator who has to ask for
		// the second one separately is an operator who will not.
		report.Live = append(report.Live,
			domain.CompareConfigToLive(newest, inst.AttestationSalt, configOnDisk(d, rel))...)
	}

	return report, nil
}

// statementFiles lists the statements at a path, which may be one file.
func statementFiles(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, domain.InstallationError(domain.ErrNotFound, "%s does not exist", path)
		}
		return nil, domain.Internal(err, "cannot read %s", path)
	}
	if !info.IsDir() {
		return []string{path}, nil
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, domain.Internal(err, "cannot read %s", path)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		out = append(out, filepath.Join(path, e.Name()))
	}
	sort.Strings(out)
	return out, nil
}

// readSignature reads the detached signature beside a statement.
func readSignature(file string) ([]byte, bool) {
	body, err := os.ReadFile(file + ".minisig")
	if err != nil {
		return nil, false
	}
	return body, true
}

// liveImages asks the runtime what each service is actually running.
//
// Reported by repository and digest so it lines up with what the statement
// recorded. A service whose image carries no digest contributes an empty one,
// and CompareToLive treats that as "cannot tell" rather than as a mismatch --
// an unpinned image is a manifest that never promised a digest, not evidence
// that somebody swapped one.
func (d *Deps) liveImages(
	ctx context.Context, inst domain.Installation, rel domain.Release,
) ([]domain.LiveImage, error) {
	if d.Runtime == nil {
		return nil, domain.Internal(nil, "no runtime configured")
	}

	cfg, err := d.runtimeConfig(rel, inst, "")
	if err != nil {
		return nil, err
	}

	states, err := d.Runtime.Status(ctx, cfg)
	if err != nil {
		return nil, err
	}

	out := make([]domain.LiveImage, 0, len(states))
	for _, s := range states {
		if s.Image == "" {
			continue
		}
		digest := domain.DigestFromRef(s.Image)
		out = append(out, domain.LiveImage{
			Service: s.Name,
			Ref:     domain.RepositoryOf(s.Image),
			Digest:  digest,
		})
	}
	return out, nil
}

// currentReleaseFor loads the installed release, for the comparisons that need
// to know what should be there.
func (d *Deps) currentReleaseFor(ctx context.Context) (domain.Release, error) {
	current, err := d.State.CurrentRelease(ctx)
	if err != nil {
		return domain.Release{}, err
	}
	return d.resolveCurrentRelease(ctx, current)
}

// configOnDisk reads the configuration files the release renders, as they are
// now.
//
// Keyed by the same target paths the render step wrote them to, because that is
// what the attested digest was taken over -- the statement carries the digest
// and never the paths, so the release manifest is the only thing that can say
// which files were in it.
//
// A file that cannot be read is left out rather than reported here. That makes
// the digest differ, which is the finding, and it keeps the decision about what
// a missing file *means* in one place instead of two.
func configOnDisk(d *Deps, rel domain.Release) map[string][]byte {
	out := make(map[string][]byte, len(rel.Manifest.Configuration))
	for _, cfg := range rel.Manifest.Configuration {
		target := d.configTarget(cfg.Target)
		body, err := os.ReadFile(target)
		if err != nil {
			continue
		}
		out[target] = body
	}
	return out
}

// newestSucceeded returns the latest statement whose operation succeeded.
//
// Succeeded, not merely latest: comparing the running deployment against a
// failed operation would report every difference the failure caused as though
// somebody had made it by hand.
func newestSucceeded(statements []domain.Statement) (domain.Statement, bool) {
	var best domain.Statement
	found := false
	for _, s := range statements {
		if s.Predicate.Operation.Outcome != string(domain.StatusSucceeded) {
			continue
		}
		if !found || s.Predicate.Operation.Started.After(best.Predicate.Operation.Started.Time) {
			best, found = s, true
		}
	}
	return best, found
}
