package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/infra/logging"
	"github.com/morzecrew/morzer/internal/lifecycle/preflight"
	"github.com/morzecrew/morzer/internal/ports"
)

// The outbox keeps what a target did not accept and could accept later
// (RFC 0043 §5.2, decision 2): one file per event and target, sent by the
// scheduled `doctor` and `notify flush` and never during an operation, so an
// operation never waits on a target that is down.

// outboxBound is how many entries a target keeps; past it the oldest go.
const outboxBound = 100

// outboxLock is taken without waiting by whatever sends the outbox, so two
// senders never send one entry (decision 11).
const outboxLock = "notify-outbox"

// outboxEntry is one event a target has still to accept. The target is its
// label, which is what its notifier answers to.
type outboxEntry struct {
	Target string       `json:"target"`
	Event  events.Event `json:"event"`
}

// outboxToken names a target in file names: its label can be a URL, and a URL
// holds characters a file name cannot.
func outboxToken(label string) string {
	sum := sha256.Sum256([]byte(label))
	return hex.EncodeToString(sum[:8])
}

// keepUndelivered writes an entry for every target whose failure a later send
// could get past, and none for a final one or for a target that accepted.
func (d *Deps) keepUndelivered(ctx context.Context, ev events.Event, err error) {
	// shortcut: no var dir means no outbox; only a test assembles such Deps.
	if d.Paths.VarDir == "" {
		return
	}
	for _, failed := range deliveryErrors(err) {
		if !failed.Resendable {
			continue
		}
		if werr := d.writeOutboxEntry(failed.Target, ev); werr != nil {
			logging.FromContext(ctx).Warn("cannot keep an undelivered notification",
				"notifier", failed.Target, "error", werr)
		}
	}
}

// deliveryErrors finds every target's failure in what the fan-out joined
// (decision 10). errors.As finds only the first, so the tree is walked.
func deliveryErrors(err error) []*ports.DeliveryError {
	var out []*ports.DeliveryError
	var walk func(error)
	walk = func(e error) {
		switch e := e.(type) {
		case nil:
		case *ports.DeliveryError:
			out = append(out, e)
		case interface{ Unwrap() []error }:
			for _, inner := range e.Unwrap() {
				walk(inner)
			}
		case interface{ Unwrap() error }:
			walk(e.Unwrap())
		}
	}
	walk(err)
	return out
}

func (d *Deps) writeOutboxEntry(target string, ev events.Event) error {
	dir := d.Paths.NotifyOutboxDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(outboxEntry{Target: target, Event: ev})
	if err != nil {
		return err
	}
	token := outboxToken(target)
	if err := atomicfs.WriteFile(filepath.Join(dir, ev.ID+"."+token+".json"), data, 0o600); err != nil {
		return err
	}

	// The oldest go first past the bound, and are counted so `doctor` can
	// say how many a target lost.
	names, err := outboxNames(dir, token)
	if err != nil || len(names) <= outboxBound {
		return err
	}
	over := names[:len(names)-outboxBound]
	for _, name := range over {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	dropped := readDropped(dir, token) + len(over)
	return atomicfs.WriteFile(filepath.Join(dir, token+".dropped"), []byte(strconv.Itoa(dropped)+"\n"), 0o600)
}

// outboxNames lists a target's entries, oldest first: the event id is a ULID,
// so its name sorts by time. An empty token lists every target's.
func outboxNames(dir, token string) ([]string, error) {
	files, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range files {
		name := f.Name()
		if !strings.HasSuffix(name, ".json") || (token != "" && !strings.HasSuffix(name, "."+token+".json")) {
			continue
		}
		out = append(out, name)
	}
	slices.Sort(out)
	return out, nil
}

func readDropped(dir, token string) int {
	raw, err := os.ReadFile(filepath.Join(dir, token+".dropped"))
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	return n
}

// forgetTarget removes a target's entries and its drop count, as `notify target
// remove` does.
func (d *Deps) forgetTarget(label string) error {
	dir := d.Paths.NotifyOutboxDir()
	token := outboxToken(label)
	names, err := outboxNames(dir, token)
	if err != nil {
		return err
	}
	for _, name := range append(names, token+".dropped") {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// OutboxReport is what one send of the outbox did.
type OutboxReport struct {
	// Busy is true when another sender held the outbox, so nothing was
	// sent.
	Busy bool `json:"busy"`

	Sent int `json:"sent"`
	// Kept are left for a later send, because their target is still down.
	Kept int `json:"kept"`
	// Refused are entries their target answered with a final error.
	Refused int `json:"refused"`
	// Orphaned are entries whose target is no longer configured.
	Orphaned int `json:"orphaned"`
}

// Summary says what the send did, in one line.
func (r OutboxReport) Summary() string {
	if r.Busy {
		return "another sender holds the notification outbox, so nothing was sent"
	}
	if r.Sent+r.Kept+r.Refused+r.Orphaned == 0 {
		return "the notification outbox is empty"
	}
	parts := []string{fmt.Sprintf("sent %d", r.Sent)}
	if r.Kept > 0 {
		parts = append(parts, fmt.Sprintf("kept %d for a target that is still down", r.Kept))
	}
	if r.Refused > 0 {
		parts = append(parts, fmt.Sprintf("dropped %d the target refused", r.Refused))
	}
	if r.Orphaned > 0 {
		parts = append(parts, fmt.Sprintf("dropped %d for a target no longer configured", r.Orphaned))
	}
	return strings.Join(parts, "; ")
}

// NotifyFlush sends the outbox now (RFC 0043 §5.2).
func NotifyFlush(ctx context.Context, d *Deps) (OutboxReport, error) {
	if _, err := d.loadInstallation(ctx); err != nil {
		return OutboxReport{}, err
	}
	return d.sendOutbox(ctx)
}

// sendOutbox sends every entry, oldest first, under the outbox lock. An entry
// is deleted once its target answers, accepted or refused, and kept on an
// answer a later send could get past. A target that answers so sends none of
// its later entries in this run: it is down, and each would wait out the
// client's timeout to say so again.
func (d *Deps) sendOutbox(ctx context.Context) (OutboxReport, error) {
	dir := d.Paths.NotifyOutboxDir()
	names, err := outboxNames(dir, "")
	if err != nil || len(names) == 0 {
		return OutboxReport{}, err
	}

	release, err := d.Locker.Acquire(ctx, outboxLock, ports.LockOptions{
		Owner: ports.LockOwner{PID: os.Getpid(), Type: "notify-flush", StartedAt: domain.NewTime(d.now())},
	})
	if errors.Is(err, domain.ErrLocked) {
		return OutboxReport{Busy: true}, nil
	}
	if err != nil {
		return OutboxReport{}, err
	}
	defer func() {
		if err := release(); err != nil {
			logging.FromContext(ctx).Error("cannot release the notify-outbox lock", "error", err)
		}
	}()

	// Listed again under the lock: another sender may have emptied it.
	if names, err = outboxNames(dir, ""); err != nil {
		return OutboxReport{}, err
	}
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return OutboxReport{}, err
	}
	// Configured is the installation's answer, not the notifiers': a target
	// whose secret will not decrypt this run is left out of those, and its
	// entries wait for it rather than going with it.
	configured := map[string]bool{}
	for _, t := range inst.Notify.Targets {
		configured[t.Label()] = true
	}
	targets := notifiersByName(d.Notifier)
	down := map[string]bool{}
	var out OutboxReport
	for _, name := range names {
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue // dropped past the bound since it was listed
		}
		if err != nil {
			return out, err
		}
		var entry outboxEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			// It can never be sent, and kept it would be read and
			// refused on every send.
			logging.FromContext(ctx).Warn("an unreadable outbox entry was dropped", "entry", path, "error", err)
			out.Refused++
			if err := removeEntry(path); err != nil {
				return out, err
			}
			continue
		}
		if down[entry.Target] {
			out.Kept++
			continue
		}
		if !configured[entry.Target] {
			out.Orphaned++
			if err := removeEntry(path); err != nil {
				return out, err
			}
			continue
		}
		target, ok := targets[entry.Target]
		if !ok {
			out.Kept++
			down[entry.Target] = true
			continue
		}

		sendCtx, cancel := context.WithTimeout(ctx, NotifyDeadline)
		err = target.Notify(sendCtx, entry.Event)
		cancel()
		var failed *ports.DeliveryError
		switch {
		case err == nil:
			out.Sent++
		case errors.As(err, &failed) && failed.Resendable:
			out.Kept++
			down[entry.Target] = true
			continue
		default:
			out.Refused++
			logging.FromContext(ctx).Warn("notifier failed", "notifier", entry.Target, "error", err)
		}
		if err := removeEntry(path); err != nil {
			return out, err
		}
	}

	// A target that caught up starts counting its drops again.
	for label := range configured {
		token := outboxToken(label)
		if left, _ := outboxNames(dir, token); len(left) == 0 {
			_ = os.Remove(filepath.Join(dir, token+".dropped"))
		}
	}
	return out, nil
}

// entryToken is the target token in an entry's name, `<event-id>.<token>.json`.
func entryToken(name string) string {
	base := strings.TrimSuffix(name, ".json")
	return base[strings.LastIndexByte(base, '.')+1:]
}

func removeEntry(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// notifiersByName is each configured target by the label its failures carry.
func notifiersByName(n ports.Notifier) map[string]ports.Notifier {
	out := map[string]ports.Notifier{}
	switch n := n.(type) {
	case nil:
	case ports.Notifiers:
		for _, target := range n {
			out[target.Name()] = target
		}
	default:
		out[n.Name()] = n
	}
	return out
}

// checkOutbox warns while a target holds undelivered events (RFC 0043 §5.2).
func (d *Deps) checkOutbox() preflight.Check {
	return preflight.Check{
		ID:          "notify.outbox",
		Category:    preflight.CategoryConfig,
		Description: "every notification reached its target",
		Run: func(ctx context.Context) events.CheckResult {
			dir := d.Paths.NotifyOutboxDir()
			names, err := outboxNames(dir, "")
			if err != nil {
				return preflight.Warn("", "cannot read the notification outbox: %v", err)
			}
			if len(names) == 0 {
				return preflight.OK("no notification is waiting for its target")
			}
			counts := map[string]int{}
			labels := map[string]string{}
			for _, name := range names {
				token := entryToken(name)
				counts[token]++
				if _, ok := labels[token]; ok {
					continue
				}
				var entry outboxEntry
				if raw, err := os.ReadFile(filepath.Join(dir, name)); err == nil && json.Unmarshal(raw, &entry) == nil {
					labels[token] = entry.Target
				}
			}
			tokens := slices.Sorted(maps.Keys(counts))
			parts := make([]string, 0, len(tokens))
			for _, token := range tokens {
				part := fmt.Sprintf("%s holds %d", labels[token], counts[token])
				if dropped := readDropped(dir, token); dropped > 0 {
					part += fmt.Sprintf(" and dropped %d past the bound of %d", dropped, outboxBound)
				}
				parts = append(parts, part)
			}
			return preflight.Warn(
				"`morzer notify flush` sends them once the target answers; the scheduled doctor sends them too",
				"undelivered notifications: %s", strings.Join(parts, "; "))
		},
	}
}
