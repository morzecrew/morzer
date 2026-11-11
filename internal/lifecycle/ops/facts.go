package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/agecrypt"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
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

// factsPair is the facts kept for the next row publish, as the one file that
// holds both halves.
type factsPair struct {
	Ciphertext []byte `json:"ciphertext"`
	Signature  []byte `json:"signature"`
}

// refreshFacts runs the release's facts task, when this machine publishes
// facts at all, after an operation that succeeded. Its caller says so: it is
// called on success paths only, and never under the deployment lock, which the
// task's own operation takes.
//
// One refresh at a time, under a lock of its own, and what it made is
// committed only under the deployment lock, and only if the recipient and the
// release it ran with are still the installation's: another operation may
// have changed either while the task ran.
func (d *Deps) refreshFacts(ctx context.Context) {
	if d.Signer == nil || d.Locker == nil {
		return
	}
	release, err := d.Locker.Acquire(ctx, "facts", ports.LockOptions{
		Owner: ports.LockOwner{PID: os.Getpid(), Type: string(domain.OpTypeTask), StartedAt: domain.NewTime(d.now())},
	})
	if err != nil {
		// A refresh under way makes the facts this one would.
		return
	}
	defer func() { _ = release() }()

	inst, current, rel, err := d.taskRelease(ctx)
	if err != nil || inst.Sync.Recipient == "" || rel.Manifest.Published.IsZero() || inst.Stopped != nil {
		return
	}
	recipient := inst.Sync.Recipient

	problem, sealed, locked := d.runFactsTask(ctx, rel.Manifest.Published.Task, recipient)
	if locked {
		// Another operation took the lock first. It is not a failure
		// of the facts, and the next operation runs the task again;
		// waiting would queue a timer's tick behind it, which no tick
		// does.
		return
	}
	var pair []byte
	if problem == "" {
		pair, problem = d.signFacts(ctx, inst, sealed)
	}

	now := domain.NewTime(d.now())
	stale := false
	err = d.withLock(ctx, d.newOpID(), domain.OpTypeConfig, Options{Wait: true}, func(ctx context.Context) error {
		fresh, err := d.loadInstallation(ctx)
		if err != nil {
			return err
		}
		installed, err := d.State.CurrentRelease(ctx)
		if err != nil || fresh.Sync.Recipient != recipient || installed.Digest != current.Digest {
			// Made for a recipient or a release that is no longer the
			// installation's: the run after that change makes new ones.
			stale = true
			return err
		}
		facts := domain.SyncFacts{}
		if fresh.Sync.Facts != nil {
			facts = *fresh.Sync.Facts
		}
		if problem == "" {
			sum := sha256.Sum256(sealed)
			digest := "sha256:" + hex.EncodeToString(sum[:])
			// Beside the facts the record names, which stay until a
			// record naming these is saved.
			if err := atomicfs.WriteFile(d.Paths.FactsFile(digest), pair, 0o600); err != nil {
				problem = "the facts could not be kept"
			} else {
				facts = domain.SyncFacts{Digest: digest, At: now, Recipient: recipient}
			}
		}
		if problem != "" {
			// The previous facts stay in force, and the row says why
			// they were not replaced.
			facts.Problem = problem
		}
		fresh.Sync.Facts = &facts
		if err := d.saveInstallation(ctx, fresh); err != nil {
			return err
		}
		d.removeFactsExcept(facts.Digest)
		return nil
	})
	if err != nil {
		d.warnf("cannot record this machine's facts: %s", domain.AsError(err).Message)
	}
	if problem != "" && !stale {
		d.warnf("facts: %s; the previous facts stay", problem)
	}
}

// removeFactsExcept removes every kept facts file but the one the record now
// names, including those a run left behind when its record did not save.
func (d *Deps) removeFactsExcept(digest string) {
	keep := d.Paths.FactsFile(digest)
	matches, _ := filepath.Glob(d.Paths.FactsFilePattern())
	for _, path := range matches {
		if path != keep {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				d.warnf("cannot remove old facts: %v", err)
			}
		}
	}
}

// signFacts signs the ciphertext with the machine key, under a comment naming
// the installation, and returns the pair as it is kept.
func (d *Deps) signFacts(ctx context.Context, inst domain.Installation, sealed []byte) ([]byte, string) {
	if _, err := d.Signer.EnsureKey(ctx); err != nil {
		return nil, "the facts could not be signed"
	}
	sig, err := d.Signer.Sign(ctx, sealed, domain.FactsComment(inst.Product, inst.ID))
	if err != nil {
		return nil, "the facts could not be signed"
	}
	pair, err := json.Marshal(factsPair{Ciphertext: sealed, Signature: sig.Encoded})
	if err != nil {
		return nil, "the facts could not be kept"
	}
	return pair, ""
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
		// A capture cut short can be under the bound, since the runtime
		// says only that one stream was cut: refused rather than
		// published as though it were all the task said.
		return fmt.Sprintf("the facts task printed more than %d KiB", domain.FactsLimit>>10), nil, false
	}
	var out bytes.Buffer
	if err := agecrypt.Encrypt(&out, bytes.NewReader([]byte(run.Stdout)), []string{recipient}); err != nil {
		return "the facts could not be encrypted", nil, false
	}
	return "", out.Bytes(), false
}

// keptFacts is the facts pair kept for the next row publish, when there is
// one this machine still vouches for: made for the recipient configured now,
// and the pair the record names.
func (d *Deps) keptFacts(inst domain.Installation) (sealed, sig []byte, ok bool) {
	f := inst.Sync.Facts
	if f == nil || f.Digest == "" || inst.Sync.Recipient == "" || f.Recipient != inst.Sync.Recipient {
		return nil, nil, false
	}
	data, err := os.ReadFile(d.Paths.FactsFile(f.Digest))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			d.warnf("cannot read this machine's facts: %v", err)
		}
		return nil, nil, false
	}
	var pair factsPair
	if err := json.Unmarshal(data, &pair); err != nil {
		return nil, nil, false
	}
	sum := sha256.Sum256(pair.Ciphertext)
	if "sha256:"+hex.EncodeToString(sum[:]) != f.Digest {
		// Not the facts the record names, so not ones a row can vouch
		// for: the next run replaces them.
		return nil, nil, false
	}
	return pair.Ciphertext, pair.Signature, true
}
