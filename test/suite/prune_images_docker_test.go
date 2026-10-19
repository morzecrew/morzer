//go:build docker

package suite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/runtime/compose"
	"github.com/morzecrew/morzer/internal/domain"
	infraexec "github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/test/dockerlab"
)

// RemoveImage against the daemon RFC 0031 P1 measured. Only the adapter is
// driven here, never a prune: a prune discovers installations under a root,
// and this machine may hold real ones the test must not touch (§7).

// pulledImage pushes an image of its own to a throwaway registry, drops every
// local name for it, and pulls it back by digest. What is left is one digest
// reference and no other name, which is how a non-bundled release image sits
// in the store -- and why the image is its own: a second repository's name on
// it would let the in-use refusal pass the name through (§2.2's table).
func pulledImage(t *testing.T, addr, repo string) string {
	t.Helper()
	seed := addr + "/morzer-test/" + repo + ":seed"
	c := strings.TrimSpace(dockerRun(t, "create", dockerlab.ImageBusybox, "true"))
	dockerRun(t, "commit", "--change", "LABEL morzer-test="+t.Name(), c, seed)
	dockerRun(t, "rm", c)
	dockerRun(t, "push", seed)

	var ref string
	for _, line := range strings.Split(dockerRun(t, "inspect", "--format",
		"{{range .RepoDigests}}{{println .}}{{end}}", seed), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), addr+"/morzer-test/"+repo+"@") {
			ref = strings.TrimSpace(line)
		}
	}
	require.NotEmpty(t, ref, "the registry issued no digest for %s", seed)

	// Removing the tag takes the push's digest reference with it, and with
	// no name left the image goes: the pull below is a real one.
	dockerRun(t, "rmi", seed)
	dockerRun(t, "pull", ref)
	t.Cleanup(func() { _, _ = dockerTry(t, "rmi", ref) })
	return ref
}

func TestRemoveImageAgainstARealDaemon(t *testing.T) {
	dockerlab.Require(t)
	dockerlab.Pull(t, dockerlab.ImageBusybox)
	reg := dockerlab.Start(t, dockerlab.ImageRegistry, []int{5000}, nil)
	addr := reg.HostPort(t, 5000)
	waitForRegistry(t, addr)

	r := compose.New(infraexec.New())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	gone := func(t *testing.T, ref string) {
		t.Helper()
		present, err := r.HasImage(ctx, ref)
		require.NoError(t, err)
		assert.False(t, present, "%s is still in the store", ref)
	}

	t.Run("a pulled digest reference", func(t *testing.T) {
		ref := pulledImage(t, addr, "pulled")
		require.NoError(t, r.RemoveImage(ctx, ref))
		gone(t, ref)
		require.NoError(t, r.RemoveImage(ctx, ref), "an absent name is already removed")
	})

	t.Run("a bundled alias", func(t *testing.T) {
		layoutDir, vendorRef := bundleWithImage(t)
		alias, _ := domain.ImageSpec{Ref: vendorRef, From: domain.ImageFromBundle}.LocalAlias()
		t.Cleanup(func() { _, _ = dockerTry(t, "rmi", alias) })
		require.NoError(t, r.IngestImages(ctx, layoutDir, []string{vendorRef}))

		require.NoError(t, r.RemoveImage(ctx, alias))
		gone(t, alias)
	})

	t.Run("a name a stopped container uses", func(t *testing.T) {
		ref := pulledImage(t, addr, "inuse")
		c := strings.TrimSpace(dockerRun(t, "create", ref, "true"))
		t.Cleanup(func() { _, _ = dockerTry(t, "rm", c) })

		err := r.RemoveImage(ctx, ref)
		assert.True(t, errors.Is(err, domain.ErrImageInUse),
			"the refusal came back as %v, so a prune would report the daemon's text rather than why", err)
		present, err := r.HasImage(ctx, ref)
		require.NoError(t, err)
		assert.True(t, present, "an unforced removal took a name a container needs")
	})
}
