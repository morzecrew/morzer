package quadlet

import (
	"context"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// Pull fetches images into the user's Podman store. References are digests,
// so a pull is reproducible.
func (r *Runtime) Pull(ctx context.Context, _ ports.RuntimeConfig, images []string) error {
	for _, ref := range images {
		res, err := r.podmanRun(ctx, 30*time.Minute, "pull", ref)
		if err != nil {
			var exitErr *exec.ExitError
			detail := firstLine(res.Stderr)
			if asExit(err, &exitErr) {
				detail = firstLine(res.Stderr + exitErr.Stderr)
			}
			return wrapExit(err, "cannot pull "+domain.ShortImageRef(ref)+": "+detail,
				"check that this machine reaches the registry, and that the user's Podman can log in to it")
		}
	}
	return nil
}

// RemoveImage removes one name from the user's Podman store, never forced. A
// name a container still uses is domain.ErrImageInUse; one already absent is
// not an error, because the name is gone either way.
func (r *Runtime) RemoveImage(ctx context.Context, ref string) error {
	res, err := r.podmanRun(ctx, time.Minute, "image", "rm", ref)
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if !asExit(err, &exitErr) {
		return domain.RuntimeError(err, "cannot remove %s", ref)
	}
	stderr := strings.ToLower(res.Stderr + exitErr.Stderr)
	switch {
	case strings.Contains(stderr, "image not known"):
		return nil
	case strings.Contains(stderr, "in use"):
		return domain.RuntimeError(domain.ErrImageInUse, "%s is in use by a container", domain.ShortImageRef(ref))
	}
	return domain.RuntimeError(err, "cannot remove %s: %s", ref, firstLine(exitErr.Stderr))
}

var _ ports.RegistryProber = (*Runtime)(nil)

// ProbeRegistry checks that an image's registry answers for it, pulling
// nothing. `podman manifest inspect` fetches the manifest alone, and asks the
// registry even when the image is in the store, so a local copy cannot answer
// for a registry this machine no longer reaches (measured on Podman 6.1.1).
//
// A refusal is read as missing credentials, whichever words the registry
// uses: Docker Hub says "requested access to the resource is denied", quay.io
// "unauthorized".
func (r *Runtime) ProbeRegistry(ctx context.Context, imageRef string) error {
	cmd := r.command(30*time.Second, r.podman, "manifest", "inspect", imageRef)
	cmd.OnLine = nil // the manifest itself, which nobody reads
	res, err := r.runner.Run(ctx, cmd)
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if !asExit(err, &exitErr) {
		return domain.RuntimeError(err, "cannot probe the registry")
	}
	lower := strings.ToLower(res.Stderr + exitErr.Stderr)
	switch {
	// A digest pinned to one image's manifest rather than to an index:
	// the registry served it, and the command, which reads only lists,
	// refused what it got. Checked first, since the text carries the
	// manifest itself.
	case strings.Contains(lower, "treating single images as manifest lists is not implemented"):
		return nil
	case strings.Contains(lower, "unauthorized"),
		strings.Contains(lower, "authentication required"),
		strings.Contains(lower, "requested access to the resource is denied"):
		return domain.RuntimeError(err, "the registry refused access").
			WithHint("run `podman login <registry>` as the user the manager runs as -- " +
				"a registry may answer the same for a repository that does not exist")
	// "manifest unknown" alone: an endpoint's 404 also says "not found",
	// and says nothing about the digest.
	case strings.Contains(lower, "manifest unknown"):
		return domain.RuntimeError(err, "the image does not exist in the registry").
			WithHint("the release may reference a digest that was never published")
	}
	return domain.RuntimeError(err, "%s", firstLine(exitErr.Stderr))
}

// ServiceImages maps each service to the image its unit runs, with the
// variables in `Image=` filled in from cfg, as systemd fills them in. With no
// names it covers the services Up starts; a named one-shot is covered too.
func (r *Runtime) ServiceImages(_ context.Context, cfg ports.RuntimeConfig, services ...string) (map[string]string, error) {
	b, err := checkBundle(cfg)
	if err != nil {
		return nil, err
	}
	if len(services) == 0 {
		services = b.started()
	} else if err := b.defines(services); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(services))
	for _, s := range services {
		u, _ := b.service(s)
		image, _ := u.last("Container", "Image")
		out[s] = expand(image, cfg.Env)
	}
	return out, nil
}

// DefinitionDrift compares the definition each service would be created from
// with the one its container carries in its label. A stopped service has no
// container under Quadlet, so it has nothing to differ.
func (r *Runtime) DefinitionDrift(ctx context.Context, cfg ports.RuntimeConfig) ([]ports.ServiceDefinition, error) {
	b, err := checkBundle(cfg)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, s := range b.started() {
		u, _ := b.service(s)
		names = append(names, b.containerName(u))
	}
	containers, err := r.inspect(ctx, names)
	if err != nil {
		return nil, err
	}
	var out []ports.ServiceDefinition
	for _, s := range b.started() {
		u, _ := b.service(s)
		def := ports.ServiceDefinition{Service: s, Declared: definition(u, cfg.Env)}
		if c, ok := containers[b.containerName(u)]; ok {
			def.Running = []string{c.Config.Labels[definitionLabel]}
		}
		out = append(out, def)
	}
	return out, nil
}
