package quadlet

import (
	"context"

	"github.com/morzecrew/morzer/internal/adapters/runtime/compose"
	"github.com/morzecrew/morzer/internal/infra/ociserve"
	"github.com/morzecrew/morzer/internal/ports"
)

var _ ports.ImageIngester = (*Runtime)(nil)

// IngestImages loads a bundle's images into the user's Podman store, the way
// Compose loads them into Docker's: the manager serves the layout on loopback,
// Podman pulls each image out of it, verifying every blob against its digest,
// and the image is named by its local alias.
//
// Podman, unlike Docker, does not exempt loopback from TLS, so that one pull
// carries `--tls-verify=false` (RFC 0023 §12 item 6). It is set here and
// nowhere else: on Pull it would turn the verification off against a real
// registry.
//
// No relay and no refusal of a remote engine, both of which Compose has.
// Rootless Podman on Linux runs as this user on this machine, so it reaches
// this machine's loopback.
func (r *Runtime) IngestImages(ctx context.Context, layoutDir string, refs []string) error {
	loader := r.images()
	// Asked before the layout is opened, so an ingest with nothing to do
	// reads no directory and binds no port.
	pending, err := loader.Pending(ctx, refs)
	if err != nil || len(pending) == 0 {
		return err
	}

	srv, err := ociserve.Start(layoutDir)
	if err != nil {
		return err
	}
	defer func() { _ = srv.Close() }()

	for _, ref := range pending {
		if err := loader.Load(ctx, srv, srv.Addr(), ref); err != nil {
			return err
		}
	}
	return nil
}

// images is the loader a bundle's images come in through, shared with Compose
// and run with Podman.
func (r *Runtime) images() compose.ImageLoader {
	return compose.ImageLoader{
		Runner:    r.runner,
		Bin:       r.podman,
		PullFlags: []string{"--tls-verify=false"},
		HasImage:  r.HasImage,
		Command:   r.helperCommand,
		PullHint: "the image is served from this machine's loopback, so nothing was " +
			"fetched over a network: the layout is damaged, which " +
			"`morzer release verify` checks, or this user's Podman cannot reach " +
			"this machine's loopback",
	}
}
