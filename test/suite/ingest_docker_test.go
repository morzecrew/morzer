//go:build docker

package suite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2/registry/remote"

	"github.com/morzecrew/morzer/internal/adapters/imagepack"
	"github.com/morzecrew/morzer/internal/adapters/runtime/compose"
	"github.com/morzecrew/morzer/internal/domain"
	infraexec "github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/dockerlab"
)

// This is the file RFC 0011 P2 exists for. Every claim ingest rests on is
// about what the Docker daemon believes, and a fake believes whatever it is
// told -- which is exactly how the original design's central claim survived
// being written down and turned out to be false.
//
// The vendor's repository, which no daemon in this test ever contacts. It has
// to be a name that cannot resolve: the whole question is whether the image
// answers to a reference nothing pulled it from.
const vendorRepo = "registry.invalid/morzer-test/app"

// bundleWithImage pushes an image to a throwaway registry, copies it into an
// OCI layout with the production packer, and returns the bundle directory and
// the reference a manifest would pin.
//
// The registry is torn down before ingest runs. That ordering is the point:
// what ingest reads afterwards is the layout on disk, and a test that left the
// registry up could not tell the two apart.
func bundleWithImage(t *testing.T) (layoutDir, vendorRef string) {
	t.Helper()
	dockerlab.Require(t)
	dockerlab.Pull(t, dockerlab.ImageBusybox)

	reg := dockerlab.Start(t, dockerlab.ImageRegistry, []int{5000}, nil)
	addr := reg.HostPort(t, 5000)
	waitForRegistry(t, addr)

	seed := addr + "/morzer-test/app:seed"
	dockerRun(t, "tag", dockerlab.ImageBusybox, seed)
	t.Cleanup(func() { _, _ = dockerTry(t, "rmi", seed) })
	dockerRun(t, "push", seed)

	// The digest the registry issued for this repository, which is what a
	// manifest pins. Selected by prefix: one image can sit in several
	// repositories, and taking element 0 pins the wrong one.
	out := dockerRun(t, "inspect", "--format",
		"{{range .RepoDigests}}{{println .}}{{end}}", seed)
	var digest string
	for _, line := range strings.Split(out, "\n") {
		if after, ok := strings.CutPrefix(strings.TrimSpace(line), addr+"/morzer-test/app@"); ok {
			digest = after
			break
		}
	}
	require.NotEmpty(t, digest, "the registry issued no digest for %s:\n%s", seed, out)

	vendorRef = vendorRepo + "@" + digest

	// The production packer, against the throwaway registry standing in for
	// the vendor's. Building the fixture with the code that builds real
	// bundles is what keeps this test honest about the layout's shape.
	bundle := t.TempDir()
	packer, err := imagepack.New("")
	require.NoError(t, err)
	packer = packer.WithSource(func(string) (imagepack.Source, error) {
		repo, err := remote.NewRepository(addr + "/morzer-test/app")
		if err != nil {
			return nil, err
		}
		repo.PlainHTTP = true
		return repo, nil
	})

	manifest := domain.Manifest{Images: map[string]domain.ImageSpec{
		"app": {Ref: vendorRef, From: domain.ImageFromBundle},
	}}
	_, err = packer.Pack(context.Background(), bundle, manifest)
	require.NoError(t, err, "the packer could not write the layout")

	// Every local trace of the seeding is dropped before ingest runs: the
	// tag this test made and the digest reference the push recorded. What
	// remains on the machine is the layout on disk, so an assertion about
	// what ingest left behind cannot be answered by what seeding left
	// behind -- and "the image was not already here" becomes a fact rather
	// than an assumption.
	_, _ = dockerTry(t, "rmi", seed, addr+"/morzer-test/app@"+digest)

	return filepath.Join(bundle, "images"), vendorRef
}

func waitForRegistry(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := dockerTry(t, "run", "--rm", "--network", "host",
			dockerlab.ImageBusybox, "wget", "-q", "-O", "-",
			"http://"+addr+"/v2/"); err == nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("the throwaway registry at %s never became ready", addr)
}

func dockerRun(t *testing.T, args ...string) string {
	t.Helper()
	out, err := dockerTry(t, args...)
	require.NoError(t, err, "docker %s:\n%s", strings.Join(args, " "), out)
	return out
}

func dockerTry(t *testing.T, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	return string(out), err
}

// TestIngestMakesTheAliasResolveAndLeavesTheVendorReferenceUnresolvable.
//
// Both halves, in one test, because either alone is misleading. That the alias
// resolves is what makes the deployment work; that the vendor's reference does
// *not* is the fact the original design got wrong, and an implementation that
// quietly went back to relying on it would pass any test asserting only the
// first.
func TestIngestMakesTheAliasResolveAndLeavesTheVendorReferenceUnresolvable(t *testing.T) {
	layoutDir, vendorRef := bundleWithImage(t)

	alias, ok := domain.ImageSpec{Ref: vendorRef, From: domain.ImageFromBundle}.LocalAlias()
	require.True(t, ok)
	t.Cleanup(func() { _, _ = dockerTry(t, "rmi", alias) })

	r := compose.New(infraexec.New())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Nothing is here beforehand, so the assertions below are about what
	// ingest did rather than about what the machine already had.
	before, err := r.HasImage(ctx, alias)
	require.NoError(t, err)
	require.False(t, before, "the alias was already present before ingest")

	require.NoError(t, r.IngestImages(ctx, layoutDir, []string{vendorRef}))

	present, err := r.HasImage(ctx, alias)
	require.NoError(t, err)
	assert.True(t, present,
		"the alias does not resolve after ingest, so the deployment cannot start")

	vendorPresent, err := r.HasImage(ctx, vendorRef)
	require.NoError(t, err)
	assert.False(t, vendorPresent,
		"the vendor's reference resolved, which contradicts the measurement the "+
			"whole alias scheme is built on -- if this ever passes, decision 19 "+
			"can be revisited rather than worked around")

	// And the loopback reference is gone: it names a port that stopped
	// listening when ingest returned, so leaving it would put a reference
	// to nothing in `docker images` for the life of the machine.
	//
	// Narrowed to this test's own repository. A developer's machine carries
	// loopback references from every other suite that ever ran, so an
	// assertion about `docker images` as a whole says nothing about this
	// ingest -- it fails on somebody else's leftovers and passes when the
	// untag is deleted, which is both directions wrong at once.
	for _, repo := range strings.Split(
		dockerRun(t, "images", "--format", "{{.Repository}}"), "\n") {
		repo = strings.TrimSpace(repo)
		if strings.HasSuffix(repo, "/morzer-test/app") && strings.HasPrefix(repo, "127.0.0.1:") {
			t.Errorf("a loopback reference outlived the ingest that created it: %s", repo)
		}
	}
}

// TestIngestIsIdempotentAgainstARealDaemon.
//
// The property that makes a failed update retryable without re-reading
// gigabytes. Asserted against the daemon rather than against a call count,
// because "already present" is the daemon's judgement.
func TestIngestIsIdempotentAgainstARealDaemon(t *testing.T) {
	layoutDir, vendorRef := bundleWithImage(t)
	alias, _ := domain.ImageSpec{Ref: vendorRef, From: domain.ImageFromBundle}.LocalAlias()
	t.Cleanup(func() { _, _ = dockerTry(t, "rmi", alias) })

	r := compose.New(infraexec.New())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	require.NoError(t, r.IngestImages(ctx, layoutDir, []string{vendorRef}))
	require.NoError(t, r.IngestImages(ctx, layoutDir, []string{vendorRef}),
		"a second ingest of the same bundle failed")

	present, err := r.HasImage(ctx, alias)
	require.NoError(t, err)
	assert.True(t, present, "the second ingest removed what the first one loaded")
}

// TestABlobThatIsNotItsDigestIsRefusedByTheDaemon.
//
// RFC 0011 decision 6 requires that a bundled image be verified rather than
// merely recorded, and decision 21 moved that obligation onto the daemon's own
// pull when the fallback ingest was withdrawn. This is the test that says the
// obligation is actually discharged -- an assertion nothing but a real daemon
// can settle, since a fake would accept whatever bytes it was handed.
func TestABlobThatIsNotItsDigestIsRefusedByTheDaemon(t *testing.T) {
	layoutDir, vendorRef := bundleWithImage(t)
	alias, _ := domain.ImageSpec{Ref: vendorRef, From: domain.ImageFromBundle}.LocalAlias()
	t.Cleanup(func() { _, _ = dockerTry(t, "rmi", alias) })

	// Corrupting a *layer* was tried first and the ingest succeeded: the
	// daemon already held that layer, answered "Already exists", and never
	// fetched the corrupted bytes. So "the daemon verifies every blob" holds
	// only for every blob it downloads -- not a hole, because the layer it
	// used was the correct one it already had, but it does mean a
	// corruption test has to make the daemon actually fetch something.
	vendorRef = unknownDigestForCorruptBytes(t, layoutDir, vendorRef)
	alias, _ = domain.ImageSpec{Ref: vendorRef, From: domain.ImageFromBundle}.LocalAlias()

	r := compose.New(infraexec.New())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	err := r.IngestImages(ctx, layoutDir, []string{vendorRef})
	require.Error(t, err, "a bundle whose bytes are not what its index claims was accepted")
	t.Logf("the refusal reads: %v\n  hint: %s", err, domain.AsError(err).Hint)

	// The daemon refuses; the manager diagnoses. Without the server's own
	// report an operator gets "filesystem layer verification failed", which
	// names no file and no bundle -- so the message has to name the digest
	// that is wrong.
	assert.Contains(t, err.Error(), "sha256:",
		"the refusal does not say which blob is not what the index claims")

	present, hasErr := r.HasImage(ctx, alias)
	require.NoError(t, hasErr)
	assert.False(t, present,
		"the alias was created for an image whose bytes were refused")
}

// unknownDigestForCorruptBytes stores the layout's manifest under a digest it
// does not hash to, and returns the reference that now addresses it.
//
// Two problems are solved at once, and the second is why this is not simply a
// byte flip. Flipping bytes under the existing digest was tried and the ingest
// succeeded: the daemon already knew that manifest digest -- from the seeding
// push earlier in this same test -- and resolved it out of its own distribution
// metadata without fetching anything at all. Removing every local reference
// does not clear that memory.
//
// A digest nothing has ever computed cannot be short-circuited, so the daemon
// has to ask for it, and the bytes it gets back do not hash to what it asked
// for. That is exactly the condition decision 21 says must be refused.
func unknownDigestForCorruptBytes(t *testing.T, layoutDir, vendorRef string) string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(layoutDir, "index.json"))
	require.NoError(t, err)

	var index struct {
		SchemaVersion int              `json:"schemaVersion"`
		MediaType     string           `json:"mediaType"`
		Manifests     []map[string]any `json:"manifests"`
	}
	require.NoError(t, json.Unmarshal(raw, &index))
	require.NotEmpty(t, index.Manifests, "the layout's index names nothing")

	real, ok := index.Manifests[0]["digest"].(string)
	require.True(t, ok, "the index entry carries no digest")
	algorithm, encoded, ok := strings.Cut(real, ":")
	require.True(t, ok)

	data, err := os.ReadFile(filepath.Join(layoutDir, "blobs", algorithm, encoded))
	require.NoError(t, err)

	// Derived from the content, so the same bundle produces the same wrong
	// digest on every run -- a test that needs a fresh identity each time is
	// a test that leaves a trail on the machine that runs it.
	sum := sha256.Sum256(append(data, []byte("not this manifest")...))
	wrong := "sha256:" + hex.EncodeToString(sum[:])

	blob := filepath.Join(layoutDir, "blobs", "sha256", strings.TrimPrefix(wrong, "sha256:"))
	require.NoError(t, os.WriteFile(blob, data, 0o444))

	index.Manifests[0]["digest"] = wrong
	rewritten, err := json.Marshal(index)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(layoutDir, "index.json"), rewritten, 0o644))

	return vendorRef[:strings.LastIndex(vendorRef, "@")] + "@" + wrong
}

var _ ports.ImageIngester = (*compose.Runtime)(nil)
