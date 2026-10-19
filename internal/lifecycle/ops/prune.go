package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

// ReleaseEntry is one release in the store, with the roles that make it
// unprunable.
//
// No digest: this listing reads manifests, and a content digest is a property
// of every byte under the root. Reporting one would mean hashing the whole store
// to answer `release list`, and reporting an empty string -- which is what the
// field did before it was removed -- tells a machine reader the release has no
// digest. `release show` computes it for the one release being asked about.
type ReleaseEntry struct {
	Version  domain.Version `json:"version"`
	Root     string         `json:"root"`
	Current  bool           `json:"current"`
	Previous bool           `json:"previous"`

	// Staged marks the candidate a channel poll fetched and nobody has
	// installed yet.
	Staged bool `json:"staged,omitempty"`
}

// Exempt reports whether retention may not remove this release.
//
// One predicate for the three roles rather than a condition at each call site.
// `release prune` and dev mode's automatic prune must agree about what is
// unprunable, and the way they would come to disagree is one of them growing a
// role the other never heard of.
func (e ReleaseEntry) Exempt() bool { return e.Current || e.Previous || e.Staged }

// InstalledReleases lists the release store, newest first.
func InstalledReleases(ctx context.Context, d *Deps) ([]ReleaseEntry, error) {
	dir := d.Paths.ReleasesDir()

	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, domain.InstallationError(err, "cannot list %s", dir)
	}

	// Propagated, never discarded. These three reads are what make an entry
	// exempt, and a read that failed answers "not current, not previous, not
	// staged" -- so a transient I/O error would hand `prune` the running
	// release and the rollback target as things to remove. A listing that
	// refuses because it cannot tell costs an operator a retry; the other
	// way costs them the deployment.
	current, err := d.State.CurrentRelease(ctx)
	if err != nil {
		return nil, err
	}
	previous, err := d.State.PreviousRelease(ctx)
	if err != nil {
		return nil, err
	}
	candidate, err := d.State.UpdateCandidate(ctx)
	if err != nil {
		return nil, err
	}

	var out []ReleaseEntry
	for _, entry := range dirEntries {
		if !entry.IsDir() {
			continue
		}
		root := filepath.Join(dir, entry.Name())

		// A directory whose manifest will not load is not a release.
		// Skipping it keeps `release list` working when a fetch was
		// interrupted midway.
		manifest, err := release.LoadManifest(filepath.Join(root, release.ManifestFileName))
		if err != nil {
			continue
		}

		out = append(out, ReleaseEntry{
			Version:  manifest.Metadata.Version,
			Root:     root,
			Current:  !current.IsZero() && current.Root == root,
			Previous: !previous.IsZero() && previous.Root == root,
			Staged:   candidate.IsStaged() && candidate.Version.Equal(manifest.Metadata.Version),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Version.GreaterThan(out[j].Version) })
	return out, nil
}

// PruneResult is what a retention pass did, or would do.
type PruneResult struct {
	// DryRun says the lists are a plan. Without it `removed` reads the same
	// for a plan and an act (RFC 0031 §2).
	DryRun   bool        `json:"dry_run"`
	Removed  []string    `json:"removed"`
	Retained int         `json:"retained"`
	Images   ImagesPrune `json:"images"`
}

// ImagesPrune is what became of the images of the releases a prune removed.
type ImagesPrune struct {
	Removed []string    `json:"removed"`
	Kept    []KeptImage `json:"kept"`

	// Skipped is why no image was removed at all, and empty when removal
	// ran. It names what to fix, because the cause can outlive the prune.
	Skipped string `json:"skipped"`
}

// KeptImage is a name a prune would have removed and did not.
type KeptImage struct {
	Ref string `json:"ref"`
	Why string `json:"why"`
}

// PruneReleasesLocked is `release prune`: PruneReleases under this
// installation's deployment lock, so it waits for or refuses beside an update
// exactly as the other writers of the release store do.
//
// The lock is taken here rather than inside PruneReleases because dev mode's
// prune runs as a step of `update`, which already holds it (RFC 0031 decision
// 11).
func PruneReleasesLocked(ctx context.Context, d *Deps, keep int, opts Options) (PruneResult, error) {
	// Bounded by `--timeout` here, as `backup push` is: the flag otherwise
	// reaches only the step engine, which this command does not run.
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	var out PruneResult
	err := d.withLock(ctx, d.newOpID(), domain.OpTypeRelease, opts, func(ctx context.Context) error {
		var err error
		out, err = PruneReleases(ctx, d, keep, opts.DryRun)
		return err
	})
	return out, err
}

// PruneReleases removes releases beyond the retention policy, and then the
// images only they named.
//
// Never the current one, never the previous one -- rollback depends on both --
// and never a staged candidate, which was fetched ahead of a decision that is
// still waiting. keep is the number of non-active releases to retain; zero uses
// the installation's policy.
//
// The images follow RFC 0031: a name goes only when no release directory left
// on the machine, in any installation, names it (planImageRemoval), and only
// after the directories, so an interrupted prune leaks names rather than
// leaving a rollback target whose images are gone. A name that cannot be
// removed is reported and the prune still succeeds: the directories were the
// operation.
func PruneReleases(ctx context.Context, d *Deps, keep int, dryRun bool) (PruneResult, error) {
	entries, err := InstalledReleases(ctx, d)
	if err != nil {
		return PruneResult{}, err
	}

	retain := keep
	if retain <= 0 {
		retain, err = d.retentionReleases(ctx)
		if err != nil {
			return PruneResult{}, err
		}
	}

	// Counted over the non-exempt entries only, because that is what the
	// number means everywhere it is written down: `--keep 1` retains one
	// release beyond the ones roles already protect. Counting the current
	// and previous releases toward the quota made `--keep 1` on an ordinary
	// machine -- which always has both -- delete every inactive release,
	// which is the opposite of what the operator asked for.
	out := PruneResult{
		DryRun:   dryRun,
		Retained: retain,
		Images:   ImagesPrune{Removed: []string{}, Kept: []KeptImage{}},
	}
	var pruned []ReleaseEntry
	kept := 0
	for _, e := range entries {
		if e.Exempt() {
			continue
		}
		if kept < retain {
			kept++
			continue
		}
		pruned = append(pruned, e)
	}

	// Planned before anything is removed: the manifests that name the
	// candidates go with their directories.
	plan := d.planImageRemoval(pruned)

	for _, e := range pruned {
		if !dryRun {
			if err := atomicfs.RemoveAll(e.Root); err != nil {
				return out, err
			}
		}
		out.Removed = append(out.Removed, e.Version.String())
	}

	out.Images.Skipped = plan.skipped
	out.Images.Kept = append(out.Images.Kept, plan.kept...)
	for i, ref := range plan.remove {
		if dryRun {
			out.Images.Removed = append(out.Images.Removed, ref)
			continue
		}
		// Out of time, every further call fails at once, and reporting
		// each as the runtime's refusal would name a reason that is not one.
		if err := ctx.Err(); err != nil {
			for _, rest := range plan.remove[i:] {
				out.Images.Kept = append(out.Images.Kept, KeptImage{Ref: rest, Why: "not tried: " + err.Error()})
			}
			break
		}
		if err := plan.remover.RemoveImage(ctx, ref); err != nil {
			out.Images.Kept = append(out.Images.Kept, KeptImage{Ref: ref, Why: keptBecause(err)})
			continue
		}
		out.Images.Removed = append(out.Images.Removed, ref)
	}
	return out, nil
}

// imagePlan is the names a prune may remove, or why it may remove none.
type imagePlan struct {
	remover ports.ImageRemover
	remove  []string
	kept    []KeptImage
	skipped string
}

// planImageRemoval decides which of the pruned releases' images may go.
//
// The candidates are what the manager put in the store for those releases
// (decision 2). The exclusion is every reference named by every release
// directory that stays, in every installation under this root, whatever its
// role (decision 3), compared by the name the daemon resolves (decision 14).
// And when any of that cannot be read, nothing is removed (decision 5): an
// unreadable manifest names images nobody can enumerate.
func (d *Deps) planImageRemoval(pruned []ReleaseEntry) imagePlan {
	var candidates []string
	for _, e := range pruned {
		manifest, err := release.LoadManifest(filepath.Join(e.Root, release.ManifestFileName))
		if err != nil {
			// InstalledReleases loaded it a moment ago; one that will
			// not load now is the same unknown as anywhere else.
			return imagePlan{skipped: fmt.Sprintf("cannot read the manifest of %s: %v", e.Root, err)}
		}
		candidates = append(candidates, manifest.RuntimeImageRefs()...)
	}
	if len(candidates) == 0 {
		return imagePlan{}
	}

	remover, ok := d.Runtime.(ports.ImageRemover)
	if !ok {
		return imagePlan{skipped: "this runtime cannot remove images"}
	}

	referenced, skipped := d.referencedImages(pruned)
	if skipped != "" {
		return imagePlan{skipped: skipped}
	}

	plan := imagePlan{remover: remover}
	slices.Sort(candidates)
	for _, ref := range slices.Compact(candidates) {
		key, ok := domain.ImageKey(ref)
		switch {
		case !ok:
			plan.kept = append(plan.kept, KeptImage{Ref: ref, Why: "it pins no digest"})
		case !referenced[key]:
			plan.remove = append(plan.remove, ref)
		}
	}
	return plan
}

// referencedImages keys every image named by a release directory that stays
// on this machine, or says why that cannot be known.
//
// Every directory under every installation's release store counts, not only
// the ones `release list` shows: a `.staging-*` tree is a fetch whose images
// are about to be pulled, and one whose manifest does not load withholds
// removal rather than being skipped (RFC 0031 §4.2).
func (d *Deps) referencedImages(pruned []ReleaseEntry) (map[string]bool, string) {
	inv, err := DiscoverProducts(d.Paths.Root())
	if err != nil {
		return nil, fmt.Sprintf("cannot list the installations on this machine: %v", err)
	}
	if len(inv.Undecidable) > 0 {
		return nil, fmt.Sprintf("cannot read %s, which may be an installation whose releases name these images",
			filepath.Join(d.Paths.Root(), "/etc", inv.Undecidable[0]))
	}

	stores := []string{d.Paths.ReleasesDir()}
	for _, product := range inv.Products {
		stores = append(stores, domain.PathsUnder(d.Paths.Root(), product).ReleasesDir())
	}
	slices.Sort(stores)

	gone := map[string]bool{}
	for _, e := range pruned {
		gone[e.Root] = true
	}

	referenced := map[string]bool{}
	for _, store := range slices.Compact(stores) {
		entries, err := os.ReadDir(store)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Sprintf("cannot list %s: %v", store, err)
		}
		for _, entry := range entries {
			root := filepath.Join(store, entry.Name())
			if !entry.IsDir() || gone[root] {
				continue
			}
			manifest, err := release.LoadManifest(filepath.Join(root, release.ManifestFileName))
			if err != nil {
				return nil, fmt.Sprintf("cannot read the manifest of %s; remove the directory "+
					"if it is not a release", root)
			}
			// A reference with no key is a tag, and no removal here
			// takes a tag: removing an alias takes only its
			// repository's digest references.
			for _, ref := range manifest.RuntimeImageRefs() {
				if key, ok := domain.ImageKey(ref); ok {
					referenced[key] = true
				}
			}
		}
	}
	return referenced, ""
}

// keptBecause is the runtime's reason in an operator's words.
func keptBecause(err error) string {
	if errors.Is(err, domain.ErrImageInUse) {
		return "a container uses it"
	}
	if e := domain.AsError(err); e != nil {
		return e.Message
	}
	return err.Error()
}

// retentionReleases resolves the effective retention: the installation's policy
// over the manifest's, per the precedence rules.
func (d *Deps) retentionReleases(ctx context.Context) (int, error) {
	inst, err := d.State.LoadInstallation(ctx)
	if err != nil {
		return 0, err
	}
	current, err := d.State.CurrentRelease(ctx)
	if err != nil {
		return 0, err
	}
	if !current.IsZero() {
		if rel, err := release.Load(current.Root); err == nil {
			return inst.RetentionReleases(rel.Manifest), nil
		}
	}
	// Still through the installation, with nothing from a manifest to read:
	// the precedence rule puts the operator's policy above the vendor's
	// declaration, so a release that will not load must not also lose them
	// the number they configured. RetentionReleases falls through to the
	// default on its own when no policy is set.
	return inst.RetentionReleases(domain.Manifest{}), nil
}
