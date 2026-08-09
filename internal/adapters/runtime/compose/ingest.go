package compose

import (
	"context"
	"os"
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

	// Refused before a port is bound, when the daemon is demonstrably
	// somewhere else. The images are served on *this* machine's loopback,
	// so a daemon that does not share it cannot fetch them however well
	// everything else works.
	if err := requireLocalDaemon(); err != nil {
		return err
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
		// A listener that died is a different failure with a different
		// remedy, and reporting it as the daemon's refusal would send an
		// operator to inspect a bundle that is fine.
		if serveErr := srv.ServeError(); serveErr != nil {
			return serveErr
		}
		return wrapExit(err, "cannot load "+domain.ShortImageRef(ref)+" out of the bundle",
			"the image is served from this machine's loopback, so nothing was "+
				"fetched over a network: either the layout is damaged, which "+
				"`morzer release verify` checks, or the container runtime cannot "+
				"reach this machine -- a daemon in a VM or behind a socket that "+
				"fronts one has its own loopback, and this needs the daemon to "+
				"share ours")
	}

	// The alias is what the deployment resolves through, so a failure here
	// leaves an image nothing can name -- reported rather than swallowed,
	// even though the bytes are safely in the store.
	tag := r.command(ports.RuntimeConfig{}, 2*time.Minute, r.docker, "tag", loopback, alias)
	if _, err := r.runner.Run(ctx, tag); err != nil {
		return wrapExit(err, "cannot name "+domain.ShortImageRef(ref)+" locally", "")
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
	// The digest and the tag come off in domain, which is where the alias
	// builder takes the same two apart. Two copies of that arithmetic
	// agree until one is edited.
	repo := domain.RepositoryOf(ref)

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

// requireLocalDaemon refuses a daemon that provably cannot reach this machine.
//
// Provably is the whole of the claim, and it is a one-way test. DOCKER_HOST
// with a `tcp://` or `ssh://` scheme names a daemon on another host, which
// cannot fetch from a server bound to this one -- so that is refused here,
// clearly, rather than a few minutes later as a connection error the operator
// has no reason to attribute to their Docker context.
//
// The absence of that variable proves nothing in return. A unix socket can
// front a daemon inside a VM with its own loopback, and a `docker context` can
// point anywhere without touching the environment. Those cases still fail, and
// they fail at the pull, where the message says so instead of blaming the
// bundle. Refusing everything that cannot be *proved* local would refuse the
// ordinary installation, which is the one this has to keep working.
func requireLocalDaemon() error {
	host := strings.TrimSpace(os.Getenv("DOCKER_HOST"))
	scheme, _, ok := strings.Cut(host, "://")
	if !ok || host == "" {
		return nil
	}
	switch scheme {
	case "unix", "fd":
		return nil
	}
	return domain.RuntimeError(domain.ErrUnsupported,
		"the images this release carries cannot be loaded into a daemon on another host").
		WithHint("DOCKER_HOST is %q, and a bundle's images are served from this "+
			"machine's loopback -- a remote daemon cannot fetch them. Load the "+
			"release on the machine running the daemon, or point DOCKER_HOST at "+
			"a local socket", host)
}

func unpinned(ref string) error {
	return domain.ValidationError(nil,
		"the bundled image %q is not pinned by digest", ref).
		WithHint("an image that travels in the bundle is addressed by the digest " +
			"its manifest pins; there is nothing else to look it up by")
}
