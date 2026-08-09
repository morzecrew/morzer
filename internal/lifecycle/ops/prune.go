package ops

import (
	"context"
	"os"
	"path/filepath"
	"sort"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
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
	Removed  []string `json:"removed"`
	Retained int      `json:"retained"`
}

// PruneReleases removes releases beyond the retention policy.
//
// Never the current one, never the previous one -- rollback depends on both --
// and never a staged candidate, which was fetched ahead of a decision that is
// still waiting. keep is the number of non-active releases to retain; zero uses
// the installation's policy.
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
	out := PruneResult{Retained: retain}
	kept := 0
	for _, e := range entries {
		if e.Exempt() {
			continue
		}
		if kept < retain {
			kept++
			continue
		}
		if !dryRun {
			if err := atomicfs.RemoveAll(e.Root); err != nil {
				return out, err
			}
		}
		out.Removed = append(out.Removed, e.Version.String())
	}
	return out, nil
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
