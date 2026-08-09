package ops

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/engine"
	"github.com/morzecrew/morzer/internal/lifecycle/preflight"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

// imagePresence exposes the runtime's local image store to preflight, or nil
// when the runtime has none.
//
// nil rather than a function that always answers false: "this runtime cannot
// tell" and "the image is not here" lead to different checks and different
// advice, and collapsing them would have a runtime with no image store failing
// every deployment that bundles one.
func (d *Deps) imagePresence() preflight.ImagePresence {
	inspector, ok := d.Runtime.(ports.ImageInspector)
	if !ok {
		return nil
	}
	return inspector.HasImage
}

// IngestImages loads the current release's bundled images into the local image
// store.
//
// An operator-facing operation as well as a lifecycle step, which is RFC 0011
// decision 12 and earns its place twice over now that a missing bundled image
// is a refusal rather than a pull: the operator who meets that refusal needs
// something to run, and re-staging a release to load images it already carries
// would be an update that changes nothing in order to fix something.
//
// Idempotent, and cheap when there is nothing to do: the adapter asks what is
// already present before it opens the layout.
func IngestImages(ctx context.Context, d *Deps, opts Options) (Result, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return Result{}, err
	}

	current, err := d.State.CurrentRelease(ctx)
	if err != nil {
		return Result{}, err
	}

	rel, err := d.resolveCurrentRelease(ctx, current)
	if err != nil {
		return Result{}, err
	}

	if len(rel.Manifest.BundledImages()) == 0 {
		return Result{Summary: fmt.Sprintf(
			"%s %s bundles no images; nothing to load",
			rel.Name(), rel.Version())}, nil
	}

	opID := d.newOpID()
	op := engine.Operation{
		ID:          opID,
		Type:        domain.OpTypeRelease,
		Description: fmt.Sprintf("load the images %s carries", rel.Version()),
		To:          rel.Version(),
		Steps:       []engine.Step{stepIngestImages(d, fixedRelease(rel))},
	}

	var result engine.Result
	runErr := d.withLock(ctx, opID, domain.OpTypeRelease, opts, func(ctx context.Context) error {
		result, err = d.Engine.Run(ctx, op, d.engineOptions(opts, inst.ID, nil))
		return err
	})
	if runErr != nil {
		return Result{Record: result.Record}, runErr
	}

	return Result{
		Record: result.Record,
		Summary: fmt.Sprintf("%d image(s) of %s %s are loaded",
			len(rel.Manifest.BundledImages()), rel.Name(), rel.Version()),
	}, nil
}

// stepIngestImages puts the images a bundle carries into the local store.
//
// It runs where a release is staged -- `init` and `update` -- rather than
// where one is converged, because that is where the bundle is on disk and
// where an operator is watching. `apply` then finds the images present, as it
// would after a pull.
//
// **Not compensable, deliberately.** RFC 0011 §5.4 originally said
// compensating meant removing what it loaded; it cannot. An ingested image is
// addressed by digest, so the alias a failed update would remove is the same
// alias any other release carrying that image resolves through -- including
// the one being rolled back to. Undoing the load would take the recovered
// deployment down with it. What compensation was protecting is disk space,
// which `docker` prunes and RFC 0011 §8 leaves to it.
//
// The release arrives through a function because the two callers know it at
// different moments: `update` has the staged release before the operation is
// built, while `init` only has one *after* the step that stages it, and reads
// it back out of the engine's state. Resolving at run time rather than at
// build time is what lets one step serve both.
func stepIngestImages(d *Deps, of releaseOf) engine.Step {
	return engine.Step{
		ID:          "ingest-images",
		Description: "load images from the bundle",
		Idempotent:  true,
		// Abort rather than Compensate: there is nothing to undo, and
		// continuing past a failure here would converge a deployment
		// whose images are not on the machine.
		OnFailure: engine.Abort,
		Timeout:   60 * time.Minute,
		Check: func(ctx context.Context, st *engine.State) (bool, error) {
			rel, ok := of(st)
			if !ok {
				// No release to read yet, which happens in
				// exactly one place: a dry run of `init`, where
				// the plan is built before staging has resolved
				// anything.
				//
				// "Will run" rather than "already satisfied".
				// The step is only in the list when a bundle
				// was named, so the honest answer to a question
				// that cannot be answered here is the one that
				// does not promise the operator a step will be
				// skipped.
				return false, nil
			}
			refs := rel.Manifest.BundledImageRefs()
			if len(refs) == 0 {
				return true, nil
			}
			// Every one present is the whole postcondition, and
			// asking is one local inspect per image. A runtime that
			// cannot answer is not treated as done: the ingest is
			// idempotent, so running it needlessly costs a re-read
			// at worst, while skipping it wrongly costs the
			// converge.
			inspector, ok := d.Runtime.(ports.ImageInspector)
			if !ok {
				return false, nil
			}
			for _, ref := range refs {
				alias, ok := domain.ImageSpec{Ref: ref}.LocalAlias()
				if !ok {
					return false, nil
				}
				present, err := inspector.HasImage(ctx, alias)
				if err != nil || !present {
					return false, nil
				}
			}
			return true, nil
		},
		Execute: func(ctx context.Context, st *engine.State) error {
			rel, ok := of(st)
			if !ok {
				return nil
			}
			refs := rel.Manifest.BundledImageRefs()
			if len(refs) == 0 {
				return nil
			}

			ingester, ok := d.Runtime.(ports.ImageIngester)
			if !ok {
				// A refusal, not a skip. The release says its
				// images travel in the bundle, and a runtime
				// that cannot load them will not find them
				// anywhere else -- so converging would fail
				// later and further from the cause.
				return domain.RuntimeError(domain.ErrUnsupported,
					"the configured runtime cannot load images out of a bundle").
					WithHint("this release marks %d image(s) `from: bundle`, which "+
						"needs a runtime with a local image store",
						len(refs))
			}
			st.Detail("%d image(s)", len(refs))
			return ingester.IngestImages(ctx,
				filepath.Join(rel.Root, release.ImagesDirName), refs)
		},
		PlanDetail: func(_ context.Context, st *engine.State) (string, string) {
			rel, ok := of(st)
			if !ok {
				return "", ""
			}
			refs := rel.Manifest.BundledImageRefs()
			if len(refs) == 0 {
				return "", ""
			}
			return fmt.Sprintf("load %d image(s) out of %s",
				len(refs), filepath.Join(rel.Root, release.ImagesDirName)), ""
		},
	}
}

// releaseOf resolves which release a step is acting on.
type releaseOf func(*engine.State) (domain.Release, bool)

// fixedRelease is the release the caller already has.
func fixedRelease(rel domain.Release) releaseOf {
	return func(*engine.State) (domain.Release, bool) { return rel, true }
}

// stagedRelease is the release an earlier step put into the engine's state.
//
// `init` builds its whole step list before it has resolved anything, so the
// release only exists once stage-release has run. A missing or wrongly-typed
// value reads as "no release", which is what an `init` with no bundle is.
func stagedRelease() releaseOf {
	return func(st *engine.State) (domain.Release, bool) {
		v, ok := st.Get(engine.KeyRelease)
		if !ok {
			return domain.Release{}, false
		}
		rel, ok := v.(domain.Release)
		return rel, ok
	}
}
