package compose

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/test/fakes"
)

const (
	appDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000001"
	appRef    = "registry.example/demo/app@" + appDigest
	appAlias  = "registry.example/demo/app:morzer-" +
		"sha256-0000000000000000000000000000000000000000000000000000000000000001"
)

// emptyLayout writes just enough of an OCI layout for the server to start.
//
// No blobs: what these tests are about is the commands the adapter issues, and
// a scripted runner does not pull anything. The bytes moving is the container
// suite's job, against a real daemon, because that is the only place the claim
// can be settled.
func emptyLayout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	index, err := json.Marshal(map[string]any{"schemaVersion": 2, "manifests": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.json"), index, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "oci-layout"),
		[]byte(`{"imageLayoutVersion":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestIngestPullsFromLoopbackAndLeavesTheAliasBehind.
//
// The three commands are the whole mechanism, and each one is there because
// something simpler was measured and does not work: the pull, because
// `docker load` will not take an OCI layout and a save tarball loses the
// registry digest; the tag, because the pull records the loopback repository
// and nothing can add a digest reference for the vendor's; and the untag,
// because the loopback reference names a port that stops listening the moment
// this returns.
func TestIngestPullsFromLoopbackAndLeavesTheAliasBehind(t *testing.T) {
	runner := fakes.NewScripted()
	// Absent locally, so there is something to do.
	runner.OnExit("image inspect", 1, "Error: No such image")

	r := New(runner)
	if err := r.IngestImages(context.Background(), emptyLayout(t), []string{appRef}); err != nil {
		t.Fatalf("ingest failed: %v", err)
	}

	var pull, tag, untag string
	for _, cmd := range runner.Calls() {
		line := strings.Join(cmd.Argv, " ")
		switch {
		case strings.Contains(line, " pull "):
			pull = line
		case strings.Contains(line, " tag "):
			tag = line
		case strings.Contains(line, " rmi "):
			untag = line
		}
	}

	if pull == "" {
		t.Fatalf("nothing was pulled:\n%s", runner.CommandLines())
	}
	// Loopback, by digest, under the vendor's repository path minus its
	// registry host -- so an operator watching sees `demo/app` rather than
	// an invented name.
	if !strings.Contains(pull, "127.0.0.1:") {
		t.Errorf("the pull did not go to loopback: %s", pull)
	}
	if !strings.Contains(pull, "/demo/app@"+appDigest) {
		t.Errorf("the pull did not address the image by digest: %s", pull)
	}
	if strings.Contains(pull, "registry.example") {
		t.Errorf("the pull reached for the vendor's registry: %s", pull)
	}

	if !strings.HasSuffix(tag, " "+appAlias) {
		t.Errorf("the alias was not created; the tag command was: %q", tag)
	}
	if strings.Contains(tag, "@sha256:0000000000000000000000000000000000000000000000000000000000000001 "+
		"registry.example/demo/app@") {
		t.Error("a digest reference was attempted, which docker refuses outright")
	}

	if !strings.Contains(untag, "127.0.0.1:") {
		t.Errorf("the loopback reference was left behind: %q", untag)
	}
	if strings.Contains(untag, appAlias) {
		t.Fatal("the untag removed the alias the deployment resolves through")
	}
}

// TestIngestSkipsWhatIsAlreadyHere.
//
// Idempotence is what makes a failed update retryable without re-reading
// gigabytes, and the check happens before the layout is opened -- so an ingest
// with nothing to do binds no port and reads no directory.
func TestIngestSkipsWhatIsAlreadyHere(t *testing.T) {
	runner := fakes.NewScripted() // every command succeeds: the image is present

	r := New(runner)
	// A directory that is not a layout: if this is opened, Start fails and
	// the test says so. Nothing should reach it.
	if err := r.IngestImages(context.Background(), t.TempDir(), []string{appRef}); err != nil {
		t.Fatalf("ingest of an image already present failed: %v", err)
	}

	if runner.Ran("pull") {
		t.Errorf("an image already present was pulled again:\n%s", runner.CommandLines())
	}
	if !runner.Ran("image inspect " + appAlias) {
		t.Errorf("presence was decided by something other than the alias:\n%s",
			runner.CommandLines())
	}
}

// TestIngestOfNothingDoesNothing.
//
// The overwhelmingly common case -- a release that bundles no images at all --
// must not open a layout that is not there.
func TestIngestOfNothingDoesNothing(t *testing.T) {
	runner := fakes.NewScripted()
	r := New(runner)

	if err := r.IngestImages(context.Background(), "/nonexistent", nil); err != nil {
		t.Fatalf("ingesting nothing failed: %v", err)
	}
	if len(runner.Calls()) != 0 {
		t.Errorf("ingesting nothing ran:\n%s", runner.CommandLines())
	}
}

// TestAFailedPullReportsTheBundleRatherThanTheNetwork.
//
// The bytes are served from this machine, so a failure here is the local
// daemon refusing them -- and an operator sent to check their network by a
// message about a pull would be looking in the one place the problem cannot
// be.
func TestAFailedPullReportsTheBundleRatherThanTheNetwork(t *testing.T) {
	runner := fakes.NewScripted()
	runner.OnExit("image inspect", 1, "Error: No such image")
	runner.OnExit("pull", 1, "filesystem layer verification failed for digest sha256:0000")

	r := New(runner)
	err := r.IngestImages(context.Background(), emptyLayout(t), []string{appRef})
	if err == nil {
		t.Fatal("a daemon that refused the image was reported as success")
	}

	// The remedy lives in the Hint, not in Error(). Asserting on err.Error()
	// reads every advice string as absent and passes whatever the hint
	// actually says -- which is how a message sending an operator to check
	// their registry credentials, for a pull that never left the machine,
	// would go unnoticed.
	hint := domain.AsError(err).Hint
	if hint == "" {
		t.Fatal("the refusal carries no remedy at all")
	}
	if strings.Contains(hint, "credential") || strings.Contains(hint, "docker login") {
		t.Errorf("the failure was blamed on a registry that was never contacted: %s", hint)
	}
	if !strings.Contains(hint, "this machine") {
		t.Errorf("the remedy does not say the bytes were local: %s", hint)
	}
}

// TestRepositoryPathIsCosmeticButHasToBeLegal.
//
// The layout is addressed by digest, so the repository exists only because the
// reference grammar demands one -- but the daemon parses what it is given, and
// a path with a registry host still in it would make the pull address a
// registry rather than the loopback server.
func TestRepositoryPathIsCosmeticButHasToBeLegal(t *testing.T) {
	cases := []struct{ ref, want string }{
		{"registry.example/demo/app@" + appDigest, "demo/app"},
		{"registry.example:5000/demo/app@" + appDigest, "demo/app"},
		{"localhost:5000/demo/app@" + appDigest, "demo/app"},
		{"ghcr.io/org/team/app@" + appDigest, "org/team/app"},
		// No host: Docker Hub's implicit one, and the whole reference
		// is the repository.
		{"postgres@" + appDigest, "postgres"},
		{"library/postgres@" + appDigest, "library/postgres"},
		// A tag is not part of the path.
		{"postgres:17@" + appDigest, "postgres"},
		// A daemon refuses an uppercase repository name, so the path is
		// lowered. Without a case here, deleting strings.ToLower would
		// leave this table green.
		{"registry.example/Demo/App@" + appDigest, "demo/app"},
		// Not a host with a port: a registry host needs a path after it
		// to be one, so by Docker's own grammar this is the repository
		// `registry.example` with the tag `5000`. Kept because the
		// reading is not obvious and the alternative -- treating it as
		// a host and producing an empty path -- is what a naive split
		// would do.
		{"registry.example:5000@" + appDigest, "registry.example"},
		// Nothing left to address it by. Unreachable through a validated
		// manifest, and answered rather than left to produce an empty
		// URL segment the daemon would refuse in its own vocabulary.
		{":5000@" + appDigest, "bundled"},
		{"registry.example/demo/app:v2@" + appDigest, "demo/app"},
	}

	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			got := repositoryPath(tc.ref)
			if got != tc.want {
				t.Errorf("repositoryPath(%q) = %q, want %q", tc.ref, got, tc.want)
			}
			if strings.ContainsAny(got, "@: ") || got == "" {
				t.Errorf("%q is not a repository path a daemon would accept", got)
			}
		})
	}
}

// TestIngestRefusesAnUnpinnedBundledImage.
//
// A bundled image is addressed by the digest its manifest pins, and there is
// nothing else to look it up by: no digest means no blob to fetch and no alias
// to leave behind. Unreachable through a validated manifest, and reachable
// through the port, which is where this is asserted.
func TestIngestRefusesAnUnpinnedBundledImage(t *testing.T) {
	runner := fakes.NewScripted()
	runner.OnExit("image inspect", 1, "Error: No such image")

	r := New(runner)
	err := r.IngestImages(context.Background(), emptyLayout(t),
		[]string{"registry.example/demo/app:latest"})
	if err == nil {
		t.Fatal("an unpinned bundled image was ingested")
	}
	if !strings.Contains(err.Error(), "pinned by digest") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}
	if runner.Ran("pull") {
		t.Errorf("something was pulled for an image with no digest:\n%s",
			runner.CommandLines())
	}
}

// TestAFailedTagIsReportedRatherThanSwallowed.
//
// The alias is the only name the deployment can resolve, so an image loaded
// but not named is an image nothing can use -- and the bytes being safely in
// the store is exactly what would make this easy to ignore.
func TestAFailedTagIsReportedRatherThanSwallowed(t *testing.T) {
	runner := fakes.NewScripted()
	runner.OnExit("image inspect", 1, "Error: No such image")
	runner.OnExit(" tag ", 1, "Error response from daemon: no such image")

	r := New(runner)
	err := r.IngestImages(context.Background(), emptyLayout(t), []string{appRef})
	if err == nil {
		t.Fatal("an image that was loaded but never named was reported as ingested")
	}
	if !strings.Contains(err.Error(), "locally") {
		t.Errorf("the failure does not say naming was what went wrong: %v", err)
	}
}

// TestARemoteDaemonIsRefusedBeforeAnythingIsServed.
//
// A bundle's images are served from this machine's loopback, so a daemon on
// another host cannot fetch them however healthy everything else is. The
// refusal has to arrive before a port is bound and before the operator has
// waited: a connection error minutes later gives them no reason to suspect
// their Docker context.
func TestARemoteDaemonIsRefusedBeforeAnythingIsServed(t *testing.T) {
	remote := map[string]bool{
		"tcp://10.0.0.5:2376":                       true,
		"ssh://deploy@build.example":                true,
		"npipe:////./pipe/dockerDesktopLinuxEngine": true,

		// A local socket proves nothing either way, so it must not be
		// refused: it is what an ordinary installation has.
		"unix:///var/run/docker.sock": false,
		"fd://":                       false,
		"":                            false,
	}

	for host, refuse := range remote {
		t.Run(host, func(t *testing.T) {
			t.Setenv("DOCKER_HOST", host)

			runner := fakes.NewScripted()
			runner.OnExit("image inspect", 1, "Error: No such image")
			r := New(runner)

			// A directory that is not a layout: reaching it at all
			// means the refusal did not come first.
			err := r.IngestImages(context.Background(), t.TempDir(), []string{appRef})

			if !refuse {
				// It still fails -- there is no layout -- but not
				// for this reason.
				if err != nil && strings.Contains(err.Error(), "another host") {
					t.Errorf("a local socket was refused as remote: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("a remote daemon was accepted")
			}
			if !strings.Contains(err.Error(), "another host") {
				t.Errorf("the refusal does not name the problem: %v", err)
			}
			if !strings.Contains(domain.AsError(err).Hint, "DOCKER_HOST") {
				t.Errorf("the remedy does not name the variable that set this: %q",
					domain.AsError(err).Hint)
			}
			if runner.Ran("pull") {
				t.Error("a pull was attempted against a daemon that cannot reach us")
			}
		})
	}
}
