package compose

import (
	"context"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/ociserve"
	"github.com/morzecrew/morzer/internal/ports"
)

var _ ports.ImageIngester = (*Runtime)(nil)

// IngestImages makes a bundle's own images resolvable by the local daemon.
//
// The mechanism, and why it is this one rather than something simpler. A
// bundled image has to end up somewhere `docker compose up` can find it, and
// the reference the manifest pins names a registry the customer cannot reach.
// Three ways to put an image into the local store were measured, and two do
// not work:
//
//   - `docker load` of the bundle's layout is refused outright on a daemon
//     with the classic image store ("invalid archive: does not contain a
//     manifest.json"), and of a `docker save` tarball it discards the registry
//     digest, which is the identity the manifest pins.
//   - `docker tag` cannot produce a digest reference at all: "refusing to
//     create a tag with a digest reference". So no amount of tagging makes an
//     image answer to `registry.example/demo/app@sha256:...`.
//
// What remains is a pull, which needs something to pull from. The manager
// serves the layout itself, on loopback, and the daemon does an ordinary V2
// pull -- ordinary in the load-bearing sense that it verifies every blob
// against the digest the manifest names, so a bundle whose bytes are not what
// its index claims is refused here rather than discovered later.
//
// Afterwards the image answers to the alias, not to the manifest's reference.
// That is not a shortcut: the daemon records the repository it pulled from,
// and nothing can add a second digest reference for a repository it never
// contacted. RFC 0011 decision 19.
func (r *Runtime) IngestImages(ctx context.Context, layoutDir string, refs []string) error {
	if len(refs) == 0 {
		return nil
	}

	// Asked before the layout is opened: an ingest with nothing to do
	// should not read a directory, bind a port, or say it did anything.
	// This is what makes the step cheap to re-run, which is what makes a
	// failed update retryable.
	pending, err := r.pendingIngest(ctx, refs)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}

	srv, err := ociserve.Start(layoutDir)
	if err != nil {
		return err
	}
	defer func() { _ = srv.Close() }()

	for _, ref := range pending {
		if err := r.ingestOne(ctx, srv, ref); err != nil {
			return err
		}
	}
	return nil
}

// pendingIngest drops the images that are already here.
func (r *Runtime) pendingIngest(ctx context.Context, refs []string) ([]string, error) {
	var pending []string
	for _, ref := range refs {
		alias, ok := domain.ImageSpec{Ref: ref}.LocalAlias()
		if !ok {
			return nil, unpinned(ref)
		}
		present, err := r.HasImage(ctx, alias)
		if err != nil {
			// "Cannot tell" is not "already here": ingesting again
			// is idempotent and cheap to be wrong about, while
			// skipping on a daemon that could not answer leaves the
			// converge to discover it.
			present = false
		}
		if !present {
			pending = append(pending, ref)
		}
	}
	return pending, nil
}

// ingestOne pulls one image out of the served layout and names it locally.
func (r *Runtime) ingestOne(ctx context.Context, srv *ociserve.Server, ref string) error {
	spec := domain.ImageSpec{Ref: ref}
	digest, ok := spec.Digest()
	if !ok {
		return unpinned(ref)
	}
	alias, _ := spec.LocalAlias()
	loopback := srv.Addr() + "/" + repositoryPath(ref) + "@" + digest

	// 45 minutes, matching the pull step: the bytes are local, but the
	// daemon unpacks every layer as it arrives and a multi-gigabyte image
	// on a slow disk is not quick.
	pull := r.command(ports.RuntimeConfig{}, 45*time.Minute, r.docker, "pull", loopback)
	if _, err := r.runner.Run(ctx, pull); err != nil {
		// The server's own diagnosis first, when it has one. The
		// daemon refuses a blob that does not match its digest -- that
		// is the guarantee -- but it says so as "filesystem layer
		// verification failed", which names no file and no cause.
		if mismatch := srv.Mismatch(); mismatch != nil {
			return mismatch
		}
		return wrapExit(err, "cannot load "+shortImage(ref)+" out of the bundle",
			"the image is served from this machine, so this is the local daemon "+
				"refusing it rather than a network failure; `morzer release verify` "+
				"checks whether the bundle's layout is intact")
	}

	// The alias is what the deployment resolves through, so a failure here
	// leaves an image nothing can name -- reported rather than swallowed,
	// even though the bytes are safely in the store.
	tag := r.command(ports.RuntimeConfig{}, 2*time.Minute, r.docker, "tag", loopback, alias)
	if _, err := r.runner.Run(ctx, tag); err != nil {
		return wrapExit(err, "cannot name "+shortImage(ref)+" locally", "")
	}

	// The loopback reference names a port that stops listening when this
	// ingest ends, so leaving it would put a reference to nothing in
	// `docker images` for the life of the machine. Untagging one of an
	// image's several names does not remove the image, and best-effort
	// because tidiness is not worth failing an install over.
	untag := r.command(ports.RuntimeConfig{}, 2*time.Minute, r.docker, "rmi", loopback)
	_, _ = r.runner.Run(ctx, untag)
	return nil
}

// repositoryPath is the part of a reference the loopback server is addressed
// by.
//
// The registry host is dropped and the rest kept, so an operator watching the
// pull sees `demo/app` rather than an invented name. It is cosmetic and
// deliberately so: the layout is addressed by digest, and the repository
// exists only because the reference grammar requires one.
//
// A host is the first path segment when it looks like one -- it carries a dot
// or a port, or it is localhost. The same rule the daemon applies, and the
// reason `postgres@sha256:...` keeps `postgres` while
// `registry.example/demo/app@sha256:...` keeps `demo/app`.
func repositoryPath(ref string) string {
	repo := ref
	if at := strings.LastIndex(repo, "@"); at > 0 {
		repo = repo[:at]
	}
	// A tag is not part of the path either.
	if slash := strings.LastIndex(repo, "/"); slash >= 0 {
		if colon := strings.LastIndex(repo[slash+1:], ":"); colon >= 0 {
			repo = repo[:slash+1+colon]
		}
	} else if colon := strings.LastIndex(repo, ":"); colon >= 0 {
		repo = repo[:colon]
	}

	first, rest, ok := strings.Cut(repo, "/")
	if ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
		repo = rest
	}
	if repo == "" {
		// Nothing left to address it by. Unreachable through a
		// validated manifest, and a name is better than an empty path
		// segment the daemon would reject with its own vocabulary.
		return "bundled"
	}
	return strings.ToLower(repo)
}

func unpinned(ref string) error {
	return domain.ValidationError(nil,
		"the bundled image %q is not pinned by digest", ref).
		WithHint("an image that travels in the bundle is addressed by the digest " +
			"its manifest pins; there is nothing else to look it up by")
}
