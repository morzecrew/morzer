package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"maps"
	"slices"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/agecrypt"
	"github.com/morzecrew/morzer/internal/ports"
)

// Following an authority's desired state (RFC 0057 §5.8).
//
// One cycle: list this installation's prefix on `sync.target`, take the newest
// complete pair, verify its signature against `sync.authority` before anything
// else reads it, decrypt it with this machine's age identity, and apply it as
// one `config apply` operation within the release's `managed` names. Each
// cycle's outcome is recorded in state, where the fleet row, `doctor` and
// `sync status` read it.
//
// **Only an authenticated serial is ever recorded as refused.** The serial in
// an object's name is anybody's who can write to the bucket, so a pair that
// fails its signature, or whose signed comment names another installation,
// leaves the watermark alone: recording it would let a writer with no key
// refuse a serial on the authority's behalf.

// SyncOptions are `sync now`'s.
type SyncOptions struct {
	Options
}

// SyncReport is what one cycle did, and the `--json` shape.
type SyncReport struct {
	Result domain.SyncOutcome `json:"result"`

	// Serial is the document the cycle considered, when it got as far as
	// one.
	Serial int64 `json:"serial,omitempty"`

	// OperationID is the operation that applied or refused it, when one
	// ran.
	OperationID string `json:"operation_id,omitempty"`

	// Step, Class and Names say where and why a document was refused, and
	// which names it involved (decision 23).
	Step  string   `json:"step,omitempty"`
	Class string   `json:"class,omitempty"`
	Names []string `json:"names,omitempty"`

	// NoChange says the document was applied and changed nothing: its
	// values were already in force.
	NoChange bool `json:"no_change,omitempty"`

	// DryRun says the document was planned and not applied.
	DryRun bool `json:"dry_run,omitempty"`

	// Skipped is why the cycle did not run, when it did not.
	Skipped string `json:"skipped,omitempty"`
}

// errSyncStale stops a document a concurrent cycle has applied first.
var errSyncStale = errors.New("the document was applied while this cycle waited for the lock")

// syncRefusal is a document refused by this cycle, before or by its operation.
type syncRefusal struct {
	result domain.SyncOutcome
	step   string
	class  string
	names  []string
	err    error
}

func (r *syncRefusal) Error() string { return r.err.Error() }
func (r *syncRefusal) Unwrap() error { return r.err }

// fetchedDocument is a document that passed every check this side of the
// operation.
type fetchedDocument struct {
	serial    int64
	digest    string
	expiresAt domain.Time
	change    ConfigChange
}

// SyncNow runs one cycle.
func SyncNow(ctx context.Context, d *Deps, opts SyncOptions) (SyncReport, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return SyncReport{}, err
	}
	cfg := inst.Sync
	if !cfg.Enabled {
		return SyncReport{}, domain.Usage("sync is not enabled on this installation").
			WithHint("set sync.target and sync.authority, then `morzer config set sync.enabled=true`")
	}
	checker, ok := d.Checker.(ports.CommentChecker)
	if !ok || d.Objects == nil || d.Targets == nil {
		return SyncReport{}, domain.Internal(nil, "sync needs a signature checker and a target registry")
	}
	keyID := domain.MinisignKeyID(cfg.Authority)
	now := domain.NewTime(d.now())

	doc, outcome, err := d.fetchDesiredState(ctx, inst, checker, keyID, now)
	var refusal *syncRefusal
	switch {
	case errors.As(err, &refusal):
		return d.refuseDocument(ctx, inst, opts, keyID, now, doc.serial, refusal)
	case err != nil:
		return SyncReport{}, err
	case outcome != "":
		report := SyncReport{Result: outcome, Serial: doc.serial}
		return report, d.recordFetch(ctx, inst, opts, now, outcome)
	}

	run, err := d.runChange(ctx, opts.Options, doc.change, changeExtras{
		check: func(rel domain.Release, hasRel bool) error {
			return checkManaged(rel, hasRel, doc.change)
		},
		record: func(after *domain.Installation) error {
			// Under the lock, from the installation as it stands: a
			// cycle that waited may find the document applied, or the
			// authority re-pinned, while it did.
			if after.Sync.Authority != cfg.Authority {
				return errSyncStale
			}
			if applied, _ := syncSerials(after.Sync, keyID); doc.serial <= applied {
				return errSyncStale
			}
			after.Sync.Applied = &domain.SyncDocument{
				Serial: doc.serial, AuthorityKeyID: keyID, At: now, ExpiresAt: doc.expiresAt,
			}
			after.Sync.LastFetch = &domain.SyncFetch{At: now, Result: domain.SyncOK}
			after.Sync.LastReached = now
			return nil
		},
		desiredState: &domain.AttestedDesiredState{
			Serial: doc.serial, Digest: doc.digest, AuthorityKeyID: keyID,
		},
	})
	report := SyncReport{Result: domain.SyncOK, Serial: doc.serial, OperationID: run.record.ID}
	switch {
	case errors.Is(err, errSyncStale):
		return SyncReport{Result: domain.SyncCurrent, Serial: doc.serial}, nil
	case errors.Is(err, domain.ErrLocked):
		return SyncReport{}, err
	case errors.As(err, &refusal):
		return d.refuseDocument(ctx, inst, opts, keyID, now, doc.serial, refusal)
	case err != nil:
		failure := &syncRefusal{
			result: domain.SyncFailed, step: failedStep(err), class: failureClass(err),
			names: run.names.list(), err: err,
		}
		if failure.class == "invalid" {
			failure.result = domain.SyncInvalid
		}
		if failure.names == nil {
			failure.names = documentNames(doc.change)
		}
		report, recordErr := d.refuseDocument(ctx, inst, opts, keyID, now, doc.serial, failure)
		report.OperationID = run.record.ID
		return report, errors.Join(err, recordErr)
	}
	report.NoChange, report.DryRun = run.noChange, opts.DryRun
	return report, nil
}

// fetchDesiredState is §5.8's steps 1 to 3: the newest complete pair, verified,
// decrypted and parsed. It returns either a document to apply, an outcome
// that ends the cycle, or a refusal of an authenticated document.
func (d *Deps) fetchDesiredState(
	ctx context.Context, inst domain.Installation, checker ports.CommentChecker, keyID string, now domain.Time,
) (fetchedDocument, domain.SyncOutcome, error) {
	var doc fetchedDocument
	cfg := inst.Sync
	target, err := d.resolveTarget(ctx, domain.BackupTargetConfig{URL: cfg.Target, Credentials: cfg.Credentials})
	if err != nil {
		return doc, "", err
	}
	// Listed without the trailing slash, which an object key may not end
	// in, and filtered by the slashed prefix, so a neighbouring id that
	// shares the characters is not read as this one's.
	prefix := domain.DesiredStatePrefix(inst.Product, inst.ID)
	keys, err := d.Objects.ObjectKeys(ctx, target, strings.TrimSuffix(prefix, "/"))
	if err != nil {
		d.warnf("cannot list the sync target: %s", domain.AsError(err).Message)
		return doc, domain.SyncUnreachable, nil
	}

	serial, found := newestCompletePair(prefix, keys)
	if !found {
		return doc, domain.SyncAbsent, nil
	}
	doc.serial = serial
	applied, refused := syncSerials(cfg, keyID)
	switch {
	case serial == applied || serial == refused:
		return doc, currentOrExpired(cfg, keyID, serial, now), nil
	case serial < applied:
		return doc, domain.SyncReplay, nil
	}

	docKey, sigKey := domain.DesiredStateKeys(inst.Product, inst.ID, serial)
	sig, err := d.Objects.GetObject(ctx, target, sigKey)
	var body []byte
	if err == nil {
		body, err = d.Objects.GetObject(ctx, target, docKey)
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// Removed between the listing and the fetch, which a publish
		// pruning old pairs does.
		return doc, domain.SyncAbsent, nil
	case err != nil:
		d.warnf("cannot fetch desired-state document %d: %s", serial, domain.AsError(err).Message)
		return doc, domain.SyncUnreachable, nil
	}

	comment, verified := checker.CheckComment(body, sig, cfg.Authority)
	if !verified {
		return doc, domain.SyncBadSignature, nil
	}
	product, id, signed, ok := domain.ParseDesiredStateComment(comment)
	if !ok || product != inst.Product || id != inst.ID || signed != serial {
		// Signed by the authority, and not this pair: another
		// installation's document copied here, or a name that is not
		// the serial signed.
		return doc, domain.SyncBadSignature, nil
	}

	// The serial is the authority's from here, so a refusal is recorded.
	sum := sha256.Sum256(body)
	doc.digest = "sha256:" + hex.EncodeToString(sum[:])
	var plain bytes.Buffer
	if err := agecrypt.Decrypt(&plain, bytes.NewReader(body), d.Paths.AgeIdentityFile()); err != nil {
		return doc, "", &syncRefusal{
			result: domain.SyncUndecryptable, step: "decrypt", class: string(domain.SyncUndecryptable), err: err,
		}
	}
	env, change, err := ParseDesiredState(plain.Bytes())
	if err != nil {
		return doc, "", &syncRefusal{result: domain.SyncInvalid, step: "parse", class: "invalid", err: err}
	}
	if env.Product != inst.Product || env.Installation != inst.ID || env.Serial != serial {
		return doc, "", &syncRefusal{
			result: domain.SyncInvalid, step: "parse", class: "invalid",
			err: domain.Usage("the document names product %q, installation %q and serial %d, not its signature's",
				env.Product, env.Installation, env.Serial),
		}
	}
	doc.change, doc.expiresAt = change, domain.NewTime(env.ExpiresAt)
	if !now.Before(env.ExpiresAt) {
		return doc, "", &syncRefusal{
			result: domain.SyncExpired, step: "check", class: string(domain.SyncExpired),
			names: documentNames(change),
			err:   domain.Usage("the document expired at %s", doc.expiresAt.Format("2006-01-02T15:04:05Z07:00")),
		}
	}
	return doc, "", nil
}

// newestCompletePair is the highest serial with both its objects (decision
// 26). A ciphertext whose signature is not written yet is not a document yet.
func newestCompletePair(prefix string, keys []string) (int64, bool) {
	docs, sigs := map[int64]bool{}, map[int64]bool{}
	for _, key := range keys {
		serial, sig, ok := domain.ParseDesiredStateKey(prefix, key)
		switch {
		case !ok:
		case sig:
			sigs[serial] = true
		default:
			docs[serial] = true
		}
	}
	var newest int64
	found := false
	for serial := range docs {
		if sigs[serial] && (!found || serial > newest) {
			newest, found = serial, true
		}
	}
	return newest, found
}

// syncSerials is the watermark kept for the key, and zero for one another key
// signed (decision 27).
func syncSerials(cfg domain.SyncConfig, keyID string) (applied, refused int64) {
	if cfg.Applied != nil && cfg.Applied.AuthorityKeyID == keyID {
		applied = cfg.Applied.Serial
	}
	if cfg.Refused != nil && cfg.Refused.AuthorityKeyID == keyID {
		refused = cfg.Refused.Serial
	}
	return applied, refused
}

// currentOrExpired is a cycle that found nothing newer: current, unless the
// document in force has expired.
func currentOrExpired(cfg domain.SyncConfig, keyID string, serial int64, now domain.Time) domain.SyncOutcome {
	if a := cfg.Applied; a != nil && a.AuthorityKeyID == keyID && a.Serial >= serial &&
		!a.ExpiresAt.IsZero() && !now.Before(a.ExpiresAt.Time) {
		return domain.SyncExpired
	}
	return domain.SyncCurrent
}

// checkManaged refuses a document naming anything the release does not let be
// managed, whole (§5.6). No release lets nothing be.
func checkManaged(rel domain.Release, hasRel bool, change ConfigChange) error {
	var refused []string
	for _, section := range []string{"settings", "parameters", "secrets"} {
		for _, name := range changeSection(change, section) {
			if !hasRel || !rel.Manifest.Managed.Allows(section, name) {
				refused = append(refused, sectionName(section, name))
			}
		}
	}
	if len(refused) == 0 {
		return nil
	}
	return &syncRefusal{
		result: domain.SyncUnmanageable, step: "validate", class: string(domain.SyncUnmanageable),
		names: refused,
		err:   domain.Usage("the release does not let %d name(s) be managed from elsewhere", len(refused)),
	}
}

// changeSection is every name a change mentions in section, set or unset.
func changeSection(change ConfigChange, section string) []string {
	var names []string
	switch section {
	case "settings":
		names = append(slices.Collect(maps.Keys(change.Settings)), change.Unset.Settings...)
	case "parameters":
		names = append(slices.Collect(maps.Keys(change.Parameters)), change.Unset.Parameters...)
	case "secrets":
		names = append(slices.Collect(maps.Keys(change.Secrets)), change.Unset.Secrets...)
	}
	slices.Sort(names)
	return slices.Compact(names)
}

func sectionName(section, name string) string {
	if section == "secrets" {
		return "secret " + name
	}
	return name
}

// documentNames is every name a document mentions.
func documentNames(change ConfigChange) []string {
	var out []string
	for _, section := range []string{"settings", "parameters", "secrets"} {
		for _, name := range changeSection(change, section) {
			out = append(out, sectionName(section, name))
		}
	}
	return out
}

// failedStep is where an operation stopped, or the plan when it stopped
// before any step ran.
func failedStep(err error) string {
	if step := domain.AsError(err).StepID; step != "" {
		return step
	}
	return "plan"
}

// failureClass names why an operation failed, by the error's kind and never
// its text (decision 23).
func failureClass(err error) string {
	switch {
	case errors.Is(err, domain.ErrHealth):
		return "health-check"
	case errors.Is(err, domain.ErrUsage), errors.Is(err, domain.ErrValidation):
		return "invalid"
	case errors.Is(err, domain.ErrManualIntervention):
		return "manual-intervention"
	case errors.Is(err, domain.ErrRuntime):
		return "runtime"
	case errors.Is(err, domain.ErrSecrets):
		return "secrets"
	}
	return "failed"
}

// refuseDocument records an authenticated document as refused, and says so to
// the notify targets once: a refused serial is not tried again.
func (d *Deps) refuseDocument(
	ctx context.Context, inst domain.Installation, opts SyncOptions, keyID string, now domain.Time,
	serial int64, refusal *syncRefusal,
) (SyncReport, error) {
	report := SyncReport{
		Result: refusal.result, Serial: serial,
		Step: refusal.step, Class: refusal.class, Names: refusal.names,
	}
	if opts.DryRun {
		return report, nil
	}
	message := domain.AsError(refusal.err).Message
	if d.Redactor != nil {
		message = d.Redactor.Apply(message)
	}
	refused := domain.SyncDocument{
		Serial: serial, AuthorityKeyID: keyID, At: now,
		Step: refusal.step, Class: refusal.class, Names: refusal.names,
		OperationID: domain.AsError(refusal.err).OpID, Message: message,
	}
	err := d.updateSync(ctx, inst.Sync.Authority, opts, func(s *domain.SyncConfig) {
		s.Refused = &refused
		s.LastFetch = &domain.SyncFetch{At: now, Result: refusal.result}
		s.LastReached = now
	})
	if err == nil {
		d.notify(ctx, events.SyncRefused(inst.Product, refused))
	}
	return report, err
}

// recordFetch records an outcome that applied and refused nothing. An expiry
// is notified when a cycle first finds it.
func (d *Deps) recordFetch(
	ctx context.Context, inst domain.Installation, opts SyncOptions, now domain.Time, outcome domain.SyncOutcome,
) error {
	if opts.DryRun {
		return nil
	}
	wasExpired := inst.Sync.LastFetch != nil && inst.Sync.LastFetch.Result == domain.SyncExpired
	err := d.updateSync(ctx, inst.Sync.Authority, opts, func(s *domain.SyncConfig) {
		s.LastFetch = &domain.SyncFetch{At: now, Result: outcome}
		if outcome != domain.SyncUnreachable {
			s.LastReached = now
		}
	})
	if err == nil && outcome == domain.SyncExpired && !wasExpired && inst.Sync.Applied != nil {
		d.notify(ctx, events.SyncExpired(inst.Product, *inst.Sync.Applied))
	}
	return err
}

// updateSync changes the sync record under the lock, from the installation as
// it stands. Nothing is written when the authority was re-pinned meanwhile:
// what this cycle found belongs to the old key.
func (d *Deps) updateSync(ctx context.Context, authority string, opts SyncOptions, change func(*domain.SyncConfig)) error {
	return d.withLock(ctx, d.newOpID(), domain.OpTypeConfig, opts.Options, func(ctx context.Context) error {
		inst, err := d.loadInstallation(ctx)
		if err != nil {
			return err
		}
		if inst.Sync.Authority != authority {
			return nil
		}
		change(&inst.Sync)
		return d.saveInstallation(ctx, inst)
	})
}
