package ops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/lifecycle/engine"
	"github.com/morzecrew/morzer/internal/ports"
)

// resolveTargets turns the installation's configured targets into references an
// adapter can use, credentials attached.
//
// This is the only place a target's credentials are read. An adapter must not
// know that a secret store exists -- and more importantly, this is the one layer
// that can also take them from a flag or from the environment, which is what a
// recovery needs when the secret store is on the machine that died.
func (d *Deps) resolveTargets(ctx context.Context, inst domain.Installation) ([]ports.TargetRef, error) {
	if !inst.Backup.HasTargets() {
		return nil, nil
	}
	if d.Targets == nil {
		return nil, domain.Internal(nil,
			"this installation configures backup targets but no target registry was wired")
	}

	out := make([]ports.TargetRef, 0, len(inst.Backup.Targets))
	for _, cfg := range inst.Backup.Targets {
		ref, err := d.resolveTarget(ctx, cfg)
		if err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, nil
}

// resolveTarget resolves one configured target.
func (d *Deps) resolveTarget(ctx context.Context, cfg domain.BackupTargetConfig) (ports.TargetRef, error) {
	ref, err := ports.TargetURL(cfg.URL)
	if err != nil {
		return ports.TargetRef{}, err
	}
	if cfg.Credentials == "" {
		return ref, nil
	}

	creds, err := d.targetCredentials(ctx, cfg.Credentials)
	if err != nil {
		return ports.TargetRef{}, err
	}
	return ref.WithCredentials(creds), nil
}

// targetCredentials reads a credential document out of the secret store.
//
// One secret holds one document rather than one field, because a target needs
// several values at once -- an access key and its secret, or a private key and
// the host key that pins the server -- and three secrets that must be rotated
// together is three chances to rotate two of them.
func (d *Deps) targetCredentials(ctx context.Context, name string) (ports.TargetCredentials, error) {
	if d.Secrets == nil {
		return ports.TargetCredentials{}, domain.Internal(nil,
			"a backup target names credentials but no secret store was wired")
	}

	set, err := d.Secrets.Load(ctx)
	if err != nil {
		return ports.TargetCredentials{}, domain.BackupError(err,
			"cannot read the credentials for a backup target")
	}
	secret, ok := set.Get(name)
	if !ok {
		return ports.TargetCredentials{}, domain.BackupError(domain.ErrNotFound,
			"the backup target names credentials %q, which is not set", name).
			WithHint("set them with `morzer secret set %s`; the value is a small "+
				"YAML document -- see `morzer backup target add --help`", name)
	}

	creds, err := ParseTargetCredentials(secret.Reveal())
	if err != nil {
		// The cause is never wrapped with the value: a parse error from a
		// YAML decoder can quote the line it failed on, and that line is
		// the credential.
		return ports.TargetCredentials{}, domain.BackupError(nil,
			"the secret %q is not a backup credential document: %s",
			name, domain.AsError(err).Message).
			WithHint("%s", domain.AsError(err).Hint)
	}

	// Everything sensitive in it is registered for redaction before it can
	// reach a log line or a subprocess argument.
	if d.Redactor != nil {
		d.Redactor.Register(creds.Redactions()...)
	}
	return creds, nil
}

// ParseTargetCredentials reads the credential document a target secret holds.
//
// Exported because the CLI parses one too, when an operator supplies it from a
// file during a recovery on a machine whose secret store is not there yet.
func ParseTargetCredentials(raw string) (ports.TargetCredentials, error) {
	var creds ports.TargetCredentials
	if strings.TrimSpace(raw) == "" {
		return creds, domain.ValidationError(nil, "it is empty").
			WithHint("a credential document is YAML: access_key_id and " +
				"secret_access_key for s3://, private_key and known_hosts for ssh://")
	}

	if err := yaml.Unmarshal([]byte(raw), &creds); err != nil {
		// Deliberately not %v on the decoder's error: it quotes the
		// offending line, which here is a credential.
		return ports.TargetCredentials{}, domain.ValidationError(nil, "it is not valid YAML").
			WithHint("a credential document is YAML: access_key_id and " +
				"secret_access_key for s3://, private_key and known_hosts for ssh://")
	}
	if creds.IsZero() {
		return creds, domain.ValidationError(nil, "it names no credential field").
			WithHint("expected some of: access_key_id, secret_access_key, region, " +
				"endpoint, private_key, passphrase, known_hosts")
	}
	return creds, nil
}

// targetSummary renders a target list for a log line or a step detail.
func targetSummary(refs []ports.TargetRef) string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.String()
	}
	return strings.Join(out, ", ")
}

// remoteRetention applies the retention policy to a target.
//
// The same policy as the local pass and the same refusal: never the most
// recent, never an exempt reason. Two passes over one policy is one policy too
// few to contradict itself; they run at different times against different
// listings, so the worst they can disagree about is an extra copy retained.
func (d *Deps) remoteRetention(
	ctx context.Context, ref ports.TargetRef, policy ports.RetentionPolicy,
) ([]string, error) {
	manifests, err := d.Targets.List(ctx, ref)
	if err != nil {
		return nil, err
	}

	keep := policy.Keep
	if keep < 1 {
		keep = 1
	}
	if len(manifests) <= keep {
		return nil, nil
	}

	exempt := make(map[string]bool, len(policy.KeepReasons))
	for _, r := range policy.KeepReasons {
		exempt[r] = true
	}

	var removed []string
	// List is newest-first, so everything past keep is a candidate.
	for _, m := range manifests[keep:] {
		if exempt[m.Reason] {
			continue
		}
		if err := d.Targets.Remove(ctx, ports.RemoteRef{Target: ref, ID: m.ID}); err != nil {
			return removed, err
		}
		removed = append(removed, m.ID)
	}
	return removed, nil
}

// TargetStatus is what `doctor` and `backup target list` report about one
// configured target.
type TargetStatus struct {
	URL string `json:"url"`

	// Reachable is whether the target answered a listing.
	Reachable bool `json:"reachable"`

	// Error is why it did not, when it did not.
	Error string `json:"error,omitempty"`

	// Backups is how many are there.
	Backups int `json:"backups"`

	// Latest is the newest backup on the target.
	Latest string `json:"latest,omitempty"`
}

// TargetStatuses probes every configured target.
//
// Read-only and best effort: a target that cannot be reached is a finding to
// report, never a reason to fail the command that asked.
func (d *Deps) TargetStatuses(ctx context.Context, inst domain.Installation) []TargetStatus {
	out := make([]TargetStatus, 0, len(inst.Backup.Targets))

	for _, cfg := range inst.Backup.Targets {
		status := TargetStatus{URL: cfg.URL}

		ref, err := d.resolveTarget(ctx, cfg)
		if err != nil {
			status.Error = domain.AsError(err).Message
			out = append(out, status)
			continue
		}

		manifests, err := d.Targets.List(ctx, ref)
		if err != nil {
			status.Error = domain.AsError(err).Message
			out = append(out, status)
			continue
		}

		status.Reachable = true
		status.Backups = len(manifests)
		if len(manifests) > 0 {
			status.Latest = manifests[0].ID
		}
		out = append(out, status)
	}
	return out
}

// TargetAddOptions configures adding a target.
type TargetAddOptions struct {
	Options

	URL string

	// Credentials names a secret holding the target's credential document.
	Credentials string
}

// TargetAdd records a new place this deployment's backups are kept.
//
// The target is reached before it is recorded. A target that only fails at push
// time fails during the nightly backup -- and because a failed push fails the
// backup, a typo here would turn into a red operation at three in the morning
// rather than an error at the terminal where it was made.
func TargetAdd(ctx context.Context, d *Deps, opts TargetAddOptions) (Result, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return Result{}, err
	}
	if d.Targets == nil {
		return Result{}, domain.Internal(nil, "no backup target registry was wired")
	}

	ref, err := ports.TargetURL(opts.URL)
	if err != nil {
		return Result{}, err
	}
	for _, existing := range inst.Backup.Targets {
		if parsed, perr := ports.TargetURL(existing.URL); perr == nil && parsed.String() == ref.String() {
			return Result{}, domain.Usage("%s is already a backup target", ref).
				WithHint("run `morzer backup target list` to see them all")
		}
	}

	cfg := domain.BackupTargetConfig{URL: ref.String(), Credentials: opts.Credentials}
	resolved, err := d.resolveTarget(ctx, cfg)
	if err != nil {
		return Result{}, err
	}
	if _, err := d.Targets.List(ctx, resolved); err != nil {
		return Result{}, domain.BackupError(err, "the backup target %s does not answer", ref).
			WithHint("it was not added; a target that cannot be reached fails " +
				"every backup at the push step")
	}

	// The reachability check above runs either way -- it reads and mutates
	// nothing -- so a dry run still tells an operator whether the target they
	// are about to add actually answers, which is the only question worth
	// asking before adding one.
	if opts.DryRun {
		return Result{
			Summary: fmt.Sprintf("would add backup target %s, which answers", ref),
			Data:    cfg,
		}, nil
	}

	inst.Backup.Targets = append(inst.Backup.Targets, cfg)

	// Under the deployment lock, like every other command that writes the
	// installation. Without it, adding a target concurrently with `config
	// set` -- or with the installation write an update makes as it rolls
	// back -- is a lost update: both load, both mutate, and one change
	// silently disappears.
	if err := d.withLock(ctx, d.newOpID(), domain.OpTypeConfig, opts.Options,
		func(ctx context.Context) error { return d.saveInstallation(ctx, inst) }); err != nil {
		return Result{}, err
	}

	return Result{
		Summary: fmt.Sprintf("backup target %s added; the next backup will be copied there", ref),
		Data:    cfg,
	}, nil
}

// TargetRemove stops keeping backups at a target.
//
// Nothing on the target is deleted. An operator retiring one medium for another
// wants the old copies to stay exactly where they are, and a command that
// silently erased an off-site archive because its URL was removed from a config
// file would be the worst possible reading of "remove".
func TargetRemove(ctx context.Context, d *Deps, opts Options, url string) (Result, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return Result{}, err
	}

	ref, err := ports.TargetURL(url)
	if err != nil {
		return Result{}, err
	}

	kept := make([]domain.BackupTargetConfig, 0, len(inst.Backup.Targets))
	var found bool
	for _, cfg := range inst.Backup.Targets {
		if parsed, perr := ports.TargetURL(cfg.URL); perr == nil && parsed.String() == ref.String() {
			found = true
			continue
		}
		kept = append(kept, cfg)
	}
	if !found {
		return Result{}, domain.Usage("%s is not a backup target of this installation", ref).
			WithHint("run `morzer backup target list` to see them")
	}

	summary := fmt.Sprintf("backup target %s removed; what is already there was left alone", ref)
	if len(kept) == 0 {
		summary += "\nevery copy of this deployment's data is now on this machine"
	}

	if opts.DryRun {
		return Result{Summary: "would remove backup target " + ref.String()}, nil
	}

	inst.Backup.Targets = kept
	if err := d.withLock(ctx, d.newOpID(), domain.OpTypeConfig, opts,
		func(ctx context.Context) error { return d.saveInstallation(ctx, inst) }); err != nil {
		return Result{}, err
	}

	return Result{Summary: summary}, nil
}

// TargetList reports every configured target and whether it answers.
func TargetList(ctx context.Context, d *Deps) ([]TargetStatus, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return nil, err
	}
	if d.Targets == nil {
		return nil, domain.Internal(nil, "no backup target registry was wired")
	}
	return d.TargetStatuses(ctx, inst), nil
}

// TargetOptions selects which target a read-only command addresses.
type TargetOptions struct {
	Options

	// URL names one target. Empty means every target the installation
	// configures.
	URL string

	// Credentials are supplied from outside the secret store.
	//
	// This is the escape hatch of RFC 0009 §5.5, and it is not a
	// convenience. On a rebuilt machine the credentials for the bucket are
	// in the secret state, the secret state is in the backup, and the
	// backup is in the bucket. An operator who has the bucket and an access
	// key must be able to break that circle from outside, or the recovery
	// path only works on machines that did not need it.
	Credentials ports.TargetCredentials
}

// RemoteBackup is one backup on one target.
type RemoteBackup struct {
	Target   string               `json:"target"`
	Manifest ports.BackupManifest `json:"manifest"`
}

// targetsFor resolves the targets a read-only command should address.
//
// A URL given on the command line that matches a configured target picks up
// that target's stored credentials, so `--target` is a filter in the ordinary
// case and a complete specification in the recovery case. Explicitly supplied
// credentials always win: during a recovery the stored ones may be wrong, or
// unreadable, or the reason the operator is here.
func (d *Deps) targetsFor(ctx context.Context, opts TargetOptions) ([]ports.TargetRef, error) {
	if d.Targets == nil {
		return nil, domain.Internal(nil, "no backup target registry was wired")
	}

	if opts.URL != "" {
		ref, err := ports.TargetURL(opts.URL)
		if err != nil {
			return nil, err
		}
		if !opts.Credentials.IsZero() {
			// Registered here as well as in targetCredentials. The
			// secret-store path arms the redactor; credentials handed in
			// from a file did not, so the second line of defence was
			// missing on exactly the path a recovery uses.
			if d.Redactor != nil {
				d.Redactor.Register(opts.Credentials.Redactions()...)
			}
			return []ports.TargetRef{ref.WithCredentials(opts.Credentials)}, nil
		}
		// Not fatal when the installation is unreadable: that is the
		// state a recovery starts in, and a file:// target needs no
		// credential at all.
		if inst, err := d.loadInstallation(ctx); err == nil {
			for _, cfg := range inst.Backup.Targets {
				parsed, parseErr := ports.TargetURL(cfg.URL)
				if parseErr != nil || parsed.String() != ref.String() {
					continue
				}
				if resolved, resolveErr := d.resolveTarget(ctx, cfg); resolveErr == nil {
					return []ports.TargetRef{resolved}, nil
				}
			}
		}
		return []ports.TargetRef{ref}, nil
	}

	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return nil, err
	}
	if !inst.Backup.HasTargets() {
		return nil, domain.Usage("this installation configures no backup targets").
			WithHint("name one with --target, or add one with " +
				"`morzer backup target add file:///mnt/backups`")
	}
	return d.resolveTargets(ctx, inst)
}

// ListRemote enumerates the backups on a target, newest first.
//
// Only manifests are transferred, and the manifest is the one plaintext file in
// a backup -- so this works from a machine that has lost every key it ever had,
// which is the machine most likely to be running it.
func ListRemote(ctx context.Context, d *Deps, opts TargetOptions) ([]RemoteBackup, error) {
	targets, err := d.targetsFor(ctx, opts)
	if err != nil {
		return nil, err
	}

	var out []RemoteBackup
	for _, target := range targets {
		manifests, err := d.Targets.List(ctx, target)
		if err != nil {
			return nil, err
		}
		for _, m := range manifests {
			out = append(out, RemoteBackup{Target: target.String(), Manifest: m})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Manifest.CreatedAt.After(out[j].Manifest.CreatedAt.Time)
	})
	return out, nil
}

// VerifyRemote reads every backup on the target back and checks its checksums.
//
// The remote equivalent of `backup verify`, and it exists for the same reason:
// a backup nobody has read back is a hope, and copying one to a bucket does not
// change that. Opt-in per run rather than part of every backup, because it is a
// full transfer -- which is the honest cost of the claim.
//
// One id verifies one backup; no id verifies every backup on every target,
// which is what a scheduled job wants.
func VerifyRemote(ctx context.Context, d *Deps, opts FetchOptions) (Result, error) {
	targets, err := d.targetsFor(ctx, opts.TargetOptions)
	if err != nil {
		return Result{}, err
	}

	var checked int
	for _, target := range targets {
		manifests, err := d.Targets.List(ctx, target)
		if err != nil {
			return Result{}, err
		}
		for _, m := range manifests {
			if opts.BackupID != "" && m.ID != opts.BackupID {
				continue
			}
			if err := d.Targets.Verify(ctx, ports.RemoteRef{Target: target, ID: m.ID}); err != nil {
				return Result{}, err
			}
			checked++
		}
	}

	if checked == 0 {
		if opts.BackupID != "" {
			return Result{}, domain.BackupError(domain.ErrNotFound,
				"no backup with id %q on %s", opts.BackupID, targetSummary(targets)).
				WithHint("run `morzer backup list --remote` to see what is there")
		}
		return Result{}, domain.BackupError(domain.ErrNotFound,
			"there are no backups on %s", targetSummary(targets))
	}

	return Result{
		Summary: fmt.Sprintf("%d backup(s) on %s verified",
			checked, targetSummary(targets)),
	}, nil
}

// FetchOptions configures a fetch from a target.
type FetchOptions struct {
	TargetOptions

	// BackupID selects the backup; empty means the newest on the target.
	BackupID string
}

// FetchRemote copies a backup down from a target into the local backup store.
//
// Deliberately separate from `restore` rather than a flag on it. A backup that
// has come back from a bucket is a backup an operator wants to look at --
// `backup verify` it, read its manifest, check it is the night they meant --
// before it overwrites a database. Folding the two together would make the
// only path to a remote restore one that never pauses.
func FetchRemote(ctx context.Context, d *Deps, opts FetchOptions) (Result, error) {
	targets, err := d.targetsFor(ctx, opts.TargetOptions)
	if err != nil {
		return Result{}, err
	}

	target, manifest, err := findRemote(ctx, d, targets, opts.BackupID)
	if err != nil {
		return Result{}, err
	}

	dest := filepath.Join(d.Paths.BackupsDir(), manifest.ID)
	if _, err := os.Stat(dest); err == nil {
		return Result{}, domain.BackupError(nil,
			"backup %s is already on this machine at %s", manifest.ID, dest).
			WithHint("restore it with `morzer restore --backup %s`, or remove the "+
				"local copy first if you believe it is damaged", manifest.ID)
	}

	// Into a neighbour and renamed: a fetch interrupted halfway must not
	// leave something in the backup store that `backup list` offers.
	staging := dest + ".fetching"
	if err := atomicfs.RemoveAll(staging); err != nil {
		return Result{}, err
	}
	if err := d.Targets.Fetch(ctx, ports.RemoteRef{Target: target, ID: manifest.ID}, staging); err != nil {
		_ = atomicfs.RemoveAll(staging)
		return Result{}, err
	}
	if err := os.Rename(staging, dest); err != nil {
		_ = atomicfs.RemoveAll(staging)
		return Result{}, domain.BackupError(err, "cannot place the fetched backup at %s", dest)
	}

	// Verified here rather than left to the restore. The transfer is the
	// new thing that can go wrong, and the moment to find out is now --
	// while the operator is watching and nothing has been overwritten.
	ref := ports.BackupRef{ID: manifest.ID, Path: dest, At: manifest.CreatedAt}
	if d.Backup != nil {
		if err := d.Backup.Verify(ctx, ref); err != nil {
			return Result{}, err
		}
	}

	return Result{
		Summary: fmt.Sprintf("backup %s fetched from %s", manifest.ID, target),
		Data:    ref,
	}, nil
}

// findRemote locates a backup across the targets, or the newest one when no id
// was given.
func findRemote(
	ctx context.Context, d *Deps, targets []ports.TargetRef, id string,
) (ports.TargetRef, ports.BackupManifest, error) {
	var newest ports.BackupManifest
	var newestTarget ports.TargetRef

	for _, target := range targets {
		manifests, err := d.Targets.List(ctx, target)
		if err != nil {
			return ports.TargetRef{}, ports.BackupManifest{}, err
		}
		for _, m := range manifests {
			if id != "" {
				if m.ID == id {
					return target, m, nil
				}
				continue
			}
			if newest.ID == "" || m.CreatedAt.After(newest.CreatedAt.Time) {
				newest, newestTarget = m, target
			}
		}
	}

	if newest.ID != "" {
		return newestTarget, newest, nil
	}
	if id != "" {
		return ports.TargetRef{}, ports.BackupManifest{}, domain.BackupError(domain.ErrNotFound,
			"no backup with id %q on %s", id, targetSummary(targets)).
			WithHint("run `morzer backup list --remote` to see what is there")
	}
	return ports.TargetRef{}, ports.BackupManifest{}, domain.BackupError(domain.ErrNotFound,
		"there are no backups on %s", targetSummary(targets))
}

// describeTargetCount renders "1 target" or "3 targets".
func describeTargetCount(n int) string {
	if n == 1 {
		return "1 target"
	}
	return fmt.Sprintf("%d targets", n)
}

// pushPreUpdateBackup copies the backup guarding an update to the configured
// targets, and warns rather than failing when it cannot.
//
// It has to be pushed. This is the backup an operator restores from when an
// update goes wrong, and leaving it on the machine alone means the one moment
// the deployment is most fragile is the one moment its backup is least durable.
// Without this, `doctor` would also report a stale target after every update --
// correctly, which is how the gap was found.
//
// But a failure here is a warning, not a failed update, and the asymmetry with
// `morzer backup` is deliberate. That operation exists to produce a durable
// backup, so a copy that never left the machine means it did not do its job.
// This one exists to install a release; the local copy is what a rollback on
// this machine uses, and refusing to update because a USB disk was unplugged
// would block the security fix an operator is trying to apply. The gap is
// reported instead -- by the warning here, and by `doctor` until it is closed
// with `morzer backup push`.
func (d *Deps) pushPreUpdateBackup(
	ctx context.Context, st *engine.State, inst domain.Installation, ref ports.BackupRef,
) {
	if d.Targets == nil || !inst.Backup.HasTargets() {
		return
	}

	targets, err := d.resolveTargets(ctx, inst)
	if err != nil {
		st.Warn("the pre-update backup could not be copied off this machine: %s",
			domain.AsError(err).Message)
		return
	}

	for _, target := range targets {
		if _, err := d.Targets.Push(ctx, target, ref.Path, ref.ID); err != nil {
			st.Warn("the pre-update backup is on this machine but could not be "+
				"copied to %s: %s. Run `morzer backup push %s` once it is reachable",
				target, domain.AsError(err).Message, ref.ID)
			return
		}
	}
	st.Detail("%s, copied to %s", ref.ID, targetSummary(targets))
}
