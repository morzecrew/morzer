package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/agecrypt"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
)

// Facts going back to the authority (RFC 0057 §5.10).
//
// A release names a task whose stdout is what the authority needs to know
// about this machine: a public key, the address it is reached at. After a
// successful operation that can change those, the task runs, and its output
// is encrypted to `sync.recipient` and signed with the machine key. The pair is
// kept here, and every row publish writes it beside the row, before the row
// that names its digest (decision 28).
//
// **A facts run never fails what came before it.** It runs after an operation
// has finished, as a statement is filed after one: a task that will not run is
// a gap in what the authority knows, recorded in the row's facts_problem, and
// the previous facts stay.

// refreshFactsAfter refreshes the facts after an operation that succeeded and
// changed something, and does nothing after any other.
func (d *Deps) refreshFactsAfter(ctx context.Context, opts Options, rec domain.OperationRecord, err error) {
	if err == nil && !opts.DryRun && rec.Status == domain.StatusSucceeded {
		d.refreshFacts(ctx)
	}
}

// refreshFacts runs the release's facts task, when this machine publishes
// facts at all, after an operation that succeeded. Its caller says so: it is
// called on success paths only, and never under the deployment lock, which the
// task's own operation takes.
func (d *Deps) refreshFacts(ctx context.Context) {
	if d.Signer == nil {
		return
	}
	inst, _, rel, err := d.taskRelease(ctx)
	if err != nil || inst.Sync.Recipient == "" || rel.Manifest.Published.IsZero() || inst.Stopped != nil {
		return
	}

	problem, sealed, locked := d.runFactsTask(ctx, rel.Manifest.Published.Task, inst.Sync.Recipient)
	if locked {
		// Another operation took the lock first. It is not a failure
		// of the facts, and the next operation runs the task again;
		// waiting would queue a timer's tick behind it, which no tick
		// does.
		return
	}
	var sig []byte
	if problem == "" {
		if _, err := d.Signer.EnsureKey(ctx); err != nil {
			problem = "the facts could not be signed"
		} else if signed, err := d.Signer.Sign(ctx, sealed, domain.FactsComment(inst.Product, inst.ID)); err != nil {
			problem = "the facts could not be signed"
		} else {
			sig = signed.Encoded
		}
	}
	if problem == "" {
		// The signature last, as on a target: a ciphertext without
		// one beside it is not facts.
		if err := atomicfs.WriteFile(d.Paths.FactsFile(), sealed, 0o600); err != nil {
			problem = "the facts could not be kept"
		} else if err := atomicfs.WriteFile(d.Paths.FactsSignatureFile(), sig, 0o600); err != nil {
			problem = "the facts could not be kept"
		}
	}

	now := domain.NewTime(d.now())
	err = d.withLock(ctx, d.newOpID(), domain.OpTypeConfig, Options{Wait: true}, func(ctx context.Context) error {
		fresh, err := d.loadInstallation(ctx)
		if err != nil {
			return err
		}
		facts := domain.SyncFacts{}
		if fresh.Sync.Facts != nil {
			facts = *fresh.Sync.Facts
		}
		if problem == "" {
			sum := sha256.Sum256(sealed)
			facts = domain.SyncFacts{Digest: "sha256:" + hex.EncodeToString(sum[:]), At: now}
		} else {
			// The previous facts stay in force, and the row says why
			// they were not replaced.
			facts.Problem = problem
		}
		fresh.Sync.Facts = &facts
		return d.saveInstallation(ctx, fresh)
	})
	if err != nil {
		d.warnf("cannot record this machine's facts: %s", domain.AsError(err).Message)
	}
	if problem != "" {
		d.warnf("facts: %s; the previous facts stay", problem)
	}
}

// runFactsTask runs the task and encrypts what it printed, or says why there
// is nothing to encrypt. The reason is a class, never the output: it reaches
// the fleet row (decision 23's rule). locked says another operation held the
// lock, and the task did not run.
func (d *Deps) runFactsTask(ctx context.Context, task, recipient string) (problem string, sealed []byte, locked bool) {
	result, err := RunTask(ctx, d, TaskOptions{Name: task, facts: true})
	run, ran := result.Data.(TaskRun)
	switch {
	case errors.Is(err, domain.ErrLocked):
		return "", nil, true
	case ran && run.ExitCode != 0:
		return fmt.Sprintf("the facts task exited %d", run.ExitCode), nil, false
	case err != nil || !ran:
		if err != nil {
			d.warnf("facts task %s: %s", task, domain.AsError(err).Message)
		}
		return "the facts task did not run", nil, false
	case run.Truncated || len(run.Stdout) > domain.FactsLimit:
		return fmt.Sprintf("the facts task printed more than %d KiB", domain.FactsLimit>>10), nil, false
	}
	var out bytes.Buffer
	if err := agecrypt.Encrypt(&out, bytes.NewReader([]byte(run.Stdout)), []string{recipient}); err != nil {
		return "the facts could not be encrypted", nil, false
	}
	return "", out.Bytes(), false
}

// keptFacts is the facts pair kept for the next row publish, when there is
// one this machine still vouches for.
func (d *Deps) keptFacts(inst domain.Installation) (sealed, sig []byte, ok bool) {
	if inst.Sync.Facts == nil || inst.Sync.Facts.Digest == "" {
		return nil, nil, false
	}
	sealed, err := os.ReadFile(d.Paths.FactsFile())
	if err == nil {
		sig, err = os.ReadFile(d.Paths.FactsSignatureFile())
	}
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			d.warnf("cannot read this machine's facts: %v", err)
		}
		return nil, nil, false
	}
	sum := sha256.Sum256(sealed)
	if "sha256:"+hex.EncodeToString(sum[:]) != inst.Sync.Facts.Digest {
		// Not the facts the record names, so not ones a row can vouch
		// for: the next run replaces them.
		return nil, nil, false
	}
	return sealed, sig, true
}
