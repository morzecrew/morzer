package ops

import (
	"context"
	"errors"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/engine"
	"github.com/morzecrew/morzer/internal/ports"
)

// RotationReport is what a signing-key rotation did, or would do.
type RotationReport struct {
	// Retired is the key this machine stopped signing with, now the newest
	// entry of signing.previous_keys.
	Retired   string `json:"retired_key"`
	RetiredID string `json:"retired_key_id"`

	// Current is the key the machine signs with from now on. Empty in a
	// plan, which mints nothing.
	Current   string `json:"current_key,omitempty"`
	CurrentID string `json:"current_key_id,omitempty"`

	DryRun bool `json:"dry_run"`
}

// keyRotatedFrom is the installation as it stood before record-signing-key
// changed it, so the step can put it back.
const keyRotatedFrom = "rotation-previous-installation"

// The journal flags naming the key a rotation retires.
const (
	flagRetiringKey   = "retiring_key"
	flagRetiringKeyID = "retiring_key_id"
)

// RotateSigningKey replaces the machine's signing key (RFC 0028 P2).
//
// What it does is narrow, and what it does not do is the part to read before
// running it after a suspected compromise: the old key's public half moves to
// signing.previous_keys, so everything it signed still verifies -- as "signed
// by a predecessor", never as current (decision 10) -- and nothing it signed is
// repaired. A stolen key signs back-dated statements as easily as current ones,
// so the retirement time is history and not a check (decision 11).
func RotateSigningKey(ctx context.Context, d *Deps, opts Options) (Result, error) {
	inst, err := d.State.LoadInstallation(ctx)
	if err != nil {
		return Result{}, err
	}
	rotator, ok := d.Signer.(ports.SigningKeyRotator)
	if !ok {
		return Result{}, domain.Usage("this build cannot rotate a signing key")
	}

	old, err := d.Signer.PublicKey(ctx)
	if errors.Is(err, domain.ErrNoSigningKey) {
		return Result{}, domain.Usage("this machine has never minted a signing key, so there is nothing to rotate").
			WithHint("one is minted the first time the machine signs something, or by `morzer init`")
	}
	if err != nil {
		return Result{}, err
	}

	var prior *domain.OperationRecord
	if opts.Resume {
		if prior, err = d.findResumable(ctx, domain.OpTypeRotateSigningKey); err != nil {
			return Result{}, err
		}
		// The key the interrupted run set out to retire, not the one on
		// disk now: a run killed after its move left the new key there,
		// and retiring that would be a second rotation.
		if line := prior.Flags[flagRetiringKey]; line != "" {
			old = ports.PublicSigningKey{Line: line, KeyID: prior.Flags[flagRetiringKeyID]}
		}
	} else if inst.Signing.HasKey() && inst.Signing.PublicKey != old.Line {
		// Retiring a key state never recorded would put a stranger in
		// previous_keys and drop the one state knows. A resumed rotation
		// is exempt by design: it recorded the new key before moving it.
		return Result{}, domain.SecretsError(nil,
			"the signing key on disk is not the one installation state records, so it is not clear which key to retire").
			WithHint("run `morzer init --repair` to record the key on disk, then rotate")
	}

	opID := d.newOpID()
	op := engine.Operation{
		ID:          opID,
		Type:        domain.OpTypeRotateSigningKey,
		Description: "rotate the signing key " + old.KeyID,
		Steps:       rotationSteps(d, rotator, old),
		// Public halves only. Journaled so a resume retires the key this
		// run named rather than whatever is on disk by then.
		Flags: map[string]string{flagRetiringKey: old.Line, flagRetiringKeyID: old.KeyID},
	}

	var result engine.Result
	runErr := d.withLock(ctx, opID, domain.OpTypeRotateSigningKey, opts, func(ctx context.Context) error {
		if opts.Resume {
			var err error
			if prior, err = d.refreshResumable(ctx, domain.OpTypeRotateSigningKey, prior); err != nil {
				return err
			}
		}
		var err error
		result, err = d.Engine.Run(ctx, op, d.engineOptions(opts, inst.ID, prior))
		return err
	})

	report := RotationReport{Retired: old.Line, RetiredID: old.KeyID, DryRun: opts.DryRun}
	out := Result{Record: result.Record, Data: report}
	if opts.DryRun {
		out.Summary = "would rotate the signing key " + old.KeyID
		return out, runErr
	}

	d.notifyFinished(ctx, opID, domain.OpTypeRotateSigningKey, result.Record, runErr)

	// Read back rather than assumed, so a statement filed after a failed
	// rotation describes the installation as it stands. Signed by whatever
	// key is on disk now: the new one after a rotation, the old one after
	// a failure. Not countersigned by the old key -- a key retired for a
	// suspected compromise vouching for its successor proves nothing.
	after, err := d.State.LoadInstallation(ctx)
	if err != nil {
		after = inst
	}
	current, err := d.State.CurrentRelease(ctx)
	if err != nil {
		current = domain.ReleaseRecord{}
	}
	emitAttestation(ctx, d, result.Record,
		attestationInputs(after, domain.Release{}, current, domain.Version{}, nil))

	if runErr != nil {
		return out, runErr
	}
	if key, err := d.Signer.PublicKey(ctx); err == nil {
		report.Current, report.CurrentID = key.Line, key.KeyID
		out.Data = report
	}
	out.Summary = "rotated the signing key: " + old.KeyID + " is retired, " +
		report.CurrentID + " signs from now on"
	return out, nil
}

// rotationSteps stage the new key, record the old one as retired, and only then
// move the new one into place.
//
// The order is what makes a crash survivable. Moving the key first and
// recording it second leaves a machine whose old public key is in neither the
// key file nor state, and `init --repair` -- the remedy `doctor` names for a key
// file state does not record -- would then record the new key with no
// predecessor, so everything the old key signed reads as an unknown signer.
// This way round the old public key is recorded before its private key is gone,
// and `--resume` finishes the move.
func rotationSteps(d *Deps, rotator ports.SigningKeyRotator, old ports.PublicSigningKey) []engine.Step {
	return []engine.Step{
		{
			ID:          "stage-signing-key",
			Description: "mint the new signing key",
			Idempotent:  true,
			OnFailure:   engine.Compensate,
			Timeout:     time.Minute,
			// Done when the key on disk is no longer the one this
			// rotation set out to retire: a resumed run whose key was
			// already moved into place must not mint a second one.
			Check: func(ctx context.Context, st *engine.State) (bool, error) {
				key, err := d.Signer.PublicKey(ctx)
				if err != nil {
					return false, err
				}
				return key.Line != old.Line, nil
			},
			Execute: func(ctx context.Context, st *engine.State) error {
				key, err := rotator.StageKey(ctx)
				if err != nil {
					return err
				}
				st.Set(engine.KeySigningKey, key.Line)
				st.Detail("key %s", key.KeyID)
				return nil
			},
			Compensate: func(ctx context.Context, st *engine.State) error {
				return rotator.DiscardStagedKey(ctx)
			},
		},
		{
			ID:          "record-signing-key",
			Description: "retire the old key in installation state",
			Idempotent:  true,
			OnFailure:   engine.Compensate,
			Timeout:     time.Minute,
			PlanDetail: func(ctx context.Context, st *engine.State) (string, string) {
				return "retire " + old.KeyID + "; it stays in previous_keys", ""
			},
			Execute: func(ctx context.Context, st *engine.State) error {
				next, err := engine.GetTyped[string](st, engine.KeySigningKey)
				if err != nil {
					// Nothing staged this run: the key was moved into
					// place by the run this one resumes.
					return nil
				}
				inst, err := d.State.LoadInstallation(ctx)
				if err != nil {
					return err
				}
				if inst.Signing.PublicKey == next {
					return nil
				}
				st.Set(keyRotatedFrom, inst)
				// The key on disk, which is also the recorded one, or the
				// key a migrated machine minted before anything recorded
				// it (§5.6): either way the one this machine signed with.
				inst.Signing.PublicKey = old.Line
				inst = inst.SucceedSigning(domain.NewTime(d.now()), domain.RetiredByRotation)
				inst.Signing.PublicKey = next
				return d.saveInstallation(ctx, inst)
			},
			Compensate: func(ctx context.Context, st *engine.State) error {
				before, err := engine.GetTyped[domain.Installation](st, keyRotatedFrom)
				if err != nil {
					return nil
				}
				return d.saveInstallation(ctx, before)
			},
		},
		{
			ID:          "activate-signing-key",
			Description: "sign with the new key from now on",
			Idempotent:  true,
			OnFailure:   engine.Compensate,
			Timeout:     time.Minute,
			Execute: func(ctx context.Context, st *engine.State) error {
				return rotator.ActivateStagedKey(ctx)
			},
		},
	}
}
