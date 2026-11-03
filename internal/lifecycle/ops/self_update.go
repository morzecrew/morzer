package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/infra/lock"
	"github.com/morzecrew/morzer/internal/infra/logging"
	"github.com/morzecrew/morzer/internal/ports"
)

// managerReleasePage is where a release's notes are, by tag.
const managerReleasePage = "https://github.com/morzecrew/morzer/releases/tag/v"

// SelfUpdateOptions is `morzer self-update`.
type SelfUpdateOptions struct {
	// To names the release; empty asks the feed for the newest.
	To string

	// From is a directory holding a release's archive for this platform,
	// SHA256SUMS and its signature, read instead of the feed (decision 14).
	From string

	// DryRun verifies and runs the new binary, then stops before any lock
	// is taken and leaves the one at the path alone.
	DryRun bool
}

// ManagerUpdate is what `self-update` did, or would have done.
type ManagerUpdate struct {
	// Path is the binary replaced: the one this command ran from.
	Path string `json:"path"`
	From string `json:"from"`
	To   string `json:"to"`

	// DevBuild is a running manager with no release version (decision 12).
	DevBuild bool `json:"dev_build"`

	// Updated is the binary at Path replaced. False when nothing newer was
	// found, and on a dry run.
	Updated bool `json:"updated"`
	DryRun  bool `json:"dry_run,omitempty"`

	// URL is the release's page, where its notes are.
	URL string `json:"url"`

	// Installations is every installation whose locks the swap held, or
	// would hold on a dry run.
	Installations []string `json:"installations"`

	// Warnings is what the operator should know and nothing refused: units
	// that run another copy, directories that could not be read, journals
	// that could not be written.
	Warnings []string `json:"warnings,omitempty"`
}

// SelfUpdate replaces the running manager with a published release (RFC 0046
// §5.3).
//
// The binary is verified against the key this one embeds before anything else
// happens to it (decision 6), run before it is trusted to answer for its own
// version (decision 8), and swapped in with one rename while every
// installation's deployment and outbox locks are held, so no operation is in
// flight anywhere on the machine (decision 7).
func SelfUpdate(ctx context.Context, d *Deps, opts SelfUpdateOptions) (ManagerUpdate, error) {
	if d.ManagerUnpacker == nil {
		return ManagerUpdate{}, domain.Internal(nil, "this build cannot replace itself")
	}
	// A relative path is the fallback for a binary that could not find
	// itself, and replacing it would replace a file in the working directory.
	if !filepath.IsAbs(d.ManagerPath) {
		return ManagerUpdate{}, domain.Internal(nil, "cannot tell which file this manager runs from, so cannot replace it").
			WithHint("run it by its absolute path")
	}
	running := d.ManagerVersion
	out := ManagerUpdate{
		Path:     d.ManagerPath,
		From:     running.String(),
		DevBuild: !running.GreaterThan(domain.MustParseVersion("0.0.0")),
		DryRun:   opts.DryRun,
		// A list, never null, for a script reading it.
		Installations: []string{},
	}

	// Both refusals come before anything is downloaded.
	if out.DevBuild && opts.To == "" && opts.From == "" {
		return out, domain.Usage("this is a dev build, with no version to compare with the newest release").
			WithHint("name the release to install with --to")
	}
	dir := filepath.Dir(d.ManagerPath)

	target, err := d.selfUpdateTarget(ctx, opts, &out)
	if err != nil {
		return out, err
	}
	if !out.DevBuild {
		named := opts.To != "" || opts.From != ""
		switch {
		case named && target.LessThan(running):
			return out, domain.ValidationError(nil,
				"%s is older than this manager, %s, and self-update only moves forward", target, running).
				WithHint("moving back is a re-run of install.sh with --version %s --dir %s, and an older "+
					"manager refuses state a newer one has moved to a newer schema; see Upgrading the manager",
					target, domain.ShellQuote(dir))
		case !target.GreaterThan(running):
			// Nothing to do is not a failure: a current machine
			// is the ordinary case.
			return out, nil
		}
	}
	if err := canWriteIn(dir); err != nil {
		return out, domain.ValidationError(err, "%s is not writable by this user", dir).
			WithHint("re-run with elevation: %s", sudoSelfUpdate(d.ManagerPath, opts))
	}

	// A dev build has no version to ask the file for again under the
	// locks, so it is held to being the same file (decision 12).
	var startDigest string
	if out.DevBuild {
		if startDigest, err = atomicfs.DigestFile(d.ManagerPath); err != nil {
			return out, err
		}
	}

	src := opts.From
	if src == "" {
		work, err := os.MkdirTemp("", "morzer-self-update-")
		if err != nil {
			return out, domain.Internal(err, "cannot make a directory to download into")
		}
		defer func() { _ = os.RemoveAll(work) }()
		if err := d.ManagerFeed.Download(ctx, target, work); err != nil {
			return out, err
		}
		src = work
	}

	// Beside the running binary, so the swap is a rename within one
	// directory and atomic.
	staged := filepath.Join(dir, fmt.Sprintf(".morzer.self-update.%d", os.Getpid()))
	_ = os.Remove(staged)
	swapped := false
	defer func() {
		if !swapped {
			_ = os.Remove(staged)
		}
	}()
	if err := d.ManagerUnpacker.Unpack(ctx, src, target, staged); err != nil {
		return out, err
	}
	reported, err := d.ManagerUnpacker.Version(ctx, staged)
	if err != nil {
		return out, err
	}
	if !reported.Equal(target) {
		return out, domain.ValidationError(nil,
			"the downloaded binary reports %s, not %s, so it is not the release it was fetched as", reported, target).
			WithHint("do not install it; check the release page: %s", out.URL)
	}

	inv, err := DiscoverProducts(d.Paths.Root())
	if err != nil {
		return out, err
	}
	out.Installations = append(out.Installations, inv.Products...)
	for _, product := range inv.Undecidable {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%s cannot be read by this process, so the swap "+
			"does not hold its locks; if it is an installation, re-run as root",
			domain.PathsUnder(d.Paths.Root(), product).EtcDir))
	}
	out.Warnings = append(out.Warnings, d.unitsElsewhere(ctx, inv.Products)...)

	if opts.DryRun {
		return out, nil
	}

	// Two self-updates must not both pass the check below before either
	// renames, and with no installation on the machine no other lock
	// stands between them.
	releaseDir, err := lock.Directory(dir)
	if err != nil {
		if errors.Is(err, domain.ErrLocked) {
			return out, domain.Locked("another self-update is replacing %s", d.ManagerPath).
				WithHint("wait for it to finish, then run `%s version`", d.ManagerPath)
		}
		return out, err
	}
	defer func() { _ = releaseDir() }()

	opIDs := make(map[string]string, len(inv.Products))
	for _, product := range inv.Products {
		opIDs[product] = d.newOpID()
	}
	started := d.now()
	release, err := d.lockEveryInstallation(ctx, inv.Products, opIDs, started)
	if err != nil {
		return out, err
	}
	defer release()

	// Another self-update may have finished since this one started.
	if out.DevBuild {
		now, err := atomicfs.DigestFile(d.ManagerPath)
		if err != nil {
			return out, err
		}
		if !atomicfs.SameDigest(now, startDigest) {
			return out, domain.ValidationError(nil, "%s changed since this command started", d.ManagerPath).
				WithHint("run `%s version` to see what is installed now", d.ManagerPath)
		}
	} else {
		now, err := d.ManagerUnpacker.Version(ctx, d.ManagerPath)
		if err != nil {
			return out, err
		}
		if !target.GreaterThan(now) {
			return out, domain.ValidationError(nil,
				"%s is now %s, which another self-update installed since this one started", d.ManagerPath, now).
				WithHint("run `%s version` to see what is installed now", d.ManagerPath)
		}
		// What the swap replaces, which another self-update may have
		// moved on from the version this command started as.
		running, out.From = now, now.String()
	}

	if err := os.Rename(staged, d.ManagerPath); err != nil {
		return out, domain.Internal(err, "cannot replace %s", d.ManagerPath)
	}
	swapped = true
	atomicfs.SyncDir(dir)
	out.Updated = true

	// While the locks are still held, so no operation the new manager
	// runs can be journalled before the record of the change (decision 11).
	for _, product := range inv.Products {
		if err := d.journalManagerUpdate(ctx, product, opIDs[product], started, running, target); err != nil {
			logging.FromContext(ctx).Error("cannot journal the manager update", "product", product, "error", err)
			out.Warnings = append(out.Warnings, fmt.Sprintf("%s's journal does not record the update: %s",
				product, domain.AsError(err).Message))
		}
	}
	return out, nil
}

// selfUpdateTarget is the release to install: the one --from carries, the one
// --to names, or the newest published.
func (d *Deps) selfUpdateTarget(ctx context.Context, opts SelfUpdateOptions, out *ManagerUpdate) (domain.Version, error) {
	var target domain.Version
	if opts.To != "" {
		v, err := domain.ParseVersion(opts.To)
		if err != nil {
			return target, err
		}
		target = v
	}
	switch {
	case opts.From != "":
		carried, err := d.ManagerUnpacker.Target(opts.From)
		if err != nil {
			return target, err
		}
		if !target.IsZero() && !carried.Equal(target) {
			return target, domain.Usage("--to %s does not match %s, the release in %s", target, carried, opts.From).
				WithHint("leave out --to; the files decide the release")
		}
		target = carried
	case target.IsZero():
		if d.ManagerFeed == nil {
			return target, domain.Internal(nil, "this build has no release feed to ask")
		}
		newest, err := d.ManagerFeed.Newest(ctx)
		if err != nil {
			return target, err
		}
		target, out.URL = newest.Version, newest.URL
	}
	out.To = target.String()
	if out.URL == "" {
		out.URL = managerReleasePage + out.To
	}
	return target, nil
}

// lockEveryInstallation takes each installation's deployment and outbox locks
// without waiting, and refuses on the first that is held, naming it.
func (d *Deps) lockEveryInstallation(ctx context.Context, products []string, opIDs map[string]string, started time.Time) (func(), error) {
	var held []func() error
	release := func() {
		for i := len(held) - 1; i >= 0; i-- {
			if err := held[i](); err != nil {
				logging.FromContext(ctx).Error("cannot release a lock", "error", err)
			}
		}
	}
	if len(products) > 0 && d.LockerFor == nil {
		return release, domain.Internal(nil, "this build cannot lock another installation")
	}
	for _, product := range products {
		locker := d.LockerFor(domain.PathsUnder(d.Paths.Root(), product))
		for _, name := range []string{"deployment", outboxLock} {
			rel, err := locker.Acquire(ctx, name, ports.LockOptions{Owner: ports.LockOwner{
				PID:         os.Getpid(),
				OperationID: opIDs[product],
				Type:        string(domain.OpTypeManagerUpdate),
				StartedAt:   domain.NewTime(started),
			}})
			if err != nil {
				release()
				e := domain.AsError(err)
				e.Message = fmt.Sprintf("cannot replace the manager while %s is busy: %s", product, e.Message)
				return func() {}, e
			}
			held = append(held, rel)
		}
	}
	return release, nil
}

// unitsElsewhere names the installations whose units run a different copy of
// the manager, which replacing this one leaves where they are (decision 13).
func (d *Deps) unitsElsewhere(ctx context.Context, products []string) []string {
	if d.Supervisor == nil {
		return nil
	}
	var out []string
	for _, product := range products {
		path, err := d.Supervisor.UnitsManagerPath(ctx, product)
		if err != nil {
			out = append(out, fmt.Sprintf("cannot read which binary %s's units run: %s",
				product, domain.AsError(err).Message))
			continue
		}
		if path == "" {
			continue
		}
		// The running binary's path is resolved, and a unit may name a
		// link to it.
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			path = resolved
		}
		if path != d.ManagerPath {
			out = append(out, fmt.Sprintf("%s's units run %s, not %s, and stay on it; "+
				"update that copy with `%s self-update`", product, path, d.ManagerPath, path))
		}
	}
	return out
}

func (d *Deps) journalManagerUpdate(ctx context.Context, product, opID string, started time.Time, from, to domain.Version) error {
	if d.StateFor == nil {
		return domain.Internal(nil, "this build cannot read installation state")
	}
	state := d.StateFor(domain.PathsUnder(d.Paths.Root(), product))
	// The ID is a convenience to a reader; an installation whose state does
	// not load still gets the record.
	inst, _ := state.LoadInstallation(ctx)
	return state.AppendOperation(ctx, domain.OperationRecord{
		SchemaVersion:  domain.OperationSchemaVersion,
		ID:             opID,
		Type:           domain.OpTypeManagerUpdate,
		Status:         domain.StatusSucceeded,
		From:           from,
		To:             to,
		StartedAt:      domain.NewTime(started),
		FinishedAt:     domain.NewTime(d.now()),
		ManagerVersion: from.String(),
		InstallationID: inst.ID,
		Flags:          map[string]string{"path": d.ManagerPath},
	})
}

// canWriteIn asks the filesystem rather than the mode bits, which say nothing
// about a read-only mount or an ACL.
func canWriteIn(dir string) error {
	f, err := os.CreateTemp(dir, ".morzer.self-update.probe.")
	if err != nil {
		return err
	}
	_ = f.Close()
	return os.Remove(f.Name())
}

// sudoSelfUpdate is the command to run instead; this one never escalates
// itself (decision 9).
func sudoSelfUpdate(path string, opts SelfUpdateOptions) string {
	cmd := "sudo " + domain.ShellQuote(path) + " self-update"
	if opts.To != "" {
		cmd += " --to " + domain.ShellQuote(opts.To)
	}
	if opts.From != "" {
		cmd += " --from " + domain.ShellQuote(opts.From)
	}
	return cmd
}
