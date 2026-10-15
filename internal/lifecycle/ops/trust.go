package ops

import (
	"context"
	"slices"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// The keys a release signature is checked against change here after `init`
// (RFC 0004 decision 24). Not through `config set`: the policy is a list, and a
// rotation is two steps -- trust the new key, install a release it signed, stop
// trusting the old one -- which a single assignment cannot express.
//
// Like a setting, a change converges nothing, so it takes the deployment lock
// and writes the state, and is not an engine operation.

// TrustReport is the release signing keys this installation accepts.
type TrustReport struct {
	RequireSignature bool     `json:"require_signature"`
	Keys             []string `json:"keys"`
}

// TrustList reads the trusted keys. Read-only: no lock.
func TrustList(ctx context.Context, d *Deps) (TrustReport, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return TrustReport{}, err
	}
	return TrustReport{
		RequireSignature: inst.Policy.RequireSignature,
		Keys:             append([]string{}, inst.Policy.SigningKeys...),
	}, nil
}

// TrustAdd trusts release signatures made with key.
//
// Adding a key already trusted is a no-op that says so rather than an error: a
// rotation is a procedure an operator re-runs, and the re-run should not fail
// at the step that already worked.
func TrustAdd(ctx context.Context, d *Deps, opts Options, key string) (Result, error) {
	key = strings.TrimSpace(key)
	if err := d.checkSigningKeys([]string{key}); err != nil {
		return Result{}, err
	}
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return Result{}, err
	}
	already := "release signatures by " + key + " are already trusted"
	if slices.Contains(inst.Policy.SigningKeys, key) {
		return Result{Summary: already}, nil
	}
	if opts.DryRun {
		return Result{Summary: "would trust release signatures by " + key}, nil
	}

	added := false
	err = d.withLock(ctx, d.newOpID(), domain.OpTypeConfig, opts, func(ctx context.Context) error {
		// Re-read under the lock, so a concurrent writer's change is not
		// reverted by this copy of the state.
		current, err := d.loadInstallation(ctx)
		if err != nil {
			return err
		}
		if slices.Contains(current.Policy.SigningKeys, key) {
			return nil
		}
		current.Policy.SigningKeys = append(current.Policy.SigningKeys, key)
		added = true
		return d.saveInstallation(ctx, current)
	})
	if err != nil {
		return Result{}, err
	}
	if !added {
		return Result{Summary: already}, nil
	}
	return Result{Summary: "release signatures by " + key + " are now trusted"}, nil
}

// TrustRemove stops trusting release signatures made with key.
//
// Refused, before a plan returns, when it would leave a policy nothing can
// satisfy: a machine requiring a signature with no key to check one against
// refuses every release, and unattended updates require exactly that policy.
func TrustRemove(ctx context.Context, d *Deps, opts Options, key string) (Result, error) {
	key = strings.TrimSpace(key)
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return Result{}, err
	}
	if _, err := untrust(inst, key); err != nil {
		return Result{}, err
	}
	if opts.DryRun {
		return Result{Summary: "would stop trusting release signatures by " + key}, nil
	}

	err = d.withLock(ctx, d.newOpID(), domain.OpTypeConfig, opts, func(ctx context.Context) error {
		current, err := d.loadInstallation(ctx)
		if err != nil {
			return err
		}
		// Checked again against the fresh copy: another `remove` may
		// have taken the other key while this one waited for the lock.
		remaining, err := untrust(current, key)
		if err != nil {
			return err
		}
		current.Policy.SigningKeys = remaining
		return d.saveInstallation(ctx, current)
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Summary: "release signatures by " + key + " are no longer trusted"}, nil
}

// untrust returns the keys that remain once key is removed, or why it cannot be.
func untrust(inst domain.Installation, key string) ([]string, error) {
	trusted := inst.Policy.SigningKeys
	if !slices.Contains(trusted, key) {
		e := domain.Usage("%s is not a trusted signing key", key)
		if len(trusted) == 0 {
			return nil, e.WithHint("this installation trusts no signing key")
		}
		return nil, e.WithHint("the trusted keys are: %s", strings.Join(trusted, ", "))
	}
	remaining := slices.DeleteFunc(slices.Clone(trusted), func(k string) bool { return k == key })
	if len(remaining) == 0 && inst.Policy.RequireSignature {
		hint := "trust the replacement first with `morzer release trust add <key>`"
		if inst.Update.AutoApply {
			hint += "; update.auto_apply needs a required signature too"
		}
		return nil, domain.ValidationError(nil,
			"%s is the only trusted key, and this installation requires a signature, "+
				"so no release could be installed without it", key).WithHint("%s", hint)
	}
	return remaining, nil
}

// checkSigningKeys asks the verifier whether each key is one it could check a
// signature against. A verifier that cannot judge keys accepts them, which is
// what every key met before one could.
func (d *Deps) checkSigningKeys(keys []string) error {
	kc, ok := d.Verifier.(ports.KeyChecker)
	if !ok {
		return nil
	}
	for _, key := range keys {
		if err := kc.CheckKey(key); err != nil {
			return err
		}
	}
	return nil
}
