package clitest_test

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/release"
	"github.com/morzecrew/morzer/test/clitest"
	"github.com/morzecrew/morzer/test/fakes"
)

// `release push` (RFC 0039) against an in-process registry on loopback, which
// the OCI adapter reaches over plain HTTP by its own rule. The registry
// semantics are covered at the adapter in test/suite; what is here is the
// command: the archive checks that run before anything is sent, the reference
// it takes, what it prints, and the exit codes.

// pushFixture builds and archives the example bundle and serves an empty
// registry, returning the archive and the repository reference.
func pushFixture(t *testing.T) (*clitest.Runner, *fakes.Registry, string, string) {
	t.Helper()
	r := clitest.New(t)
	r.Run("release", "build", r.Bundle).ExitCode(0)

	archive := filepath.Join(t.TempDir(), "demo-1.2.0.tar.zst")
	r.Run("release", "archive", r.Bundle, "-o", archive).ExitCode(0)

	reg := fakes.NewRegistry()
	srv := httptest.NewServer(reg)
	t.Cleanup(srv.Close)
	return r, reg, archive, "oci://" + strings.TrimPrefix(srv.URL, "http://") + "/demo/bundle"
}

func TestReleasePushPublishesAndNamesBothDigests(t *testing.T) {
	r, reg, archive, repo := pushFixture(t)

	r.Run("release", "push", archive, repo, "--channel", "stable").
		ExitCode(0).
		StdoutContains("pushed demo 1.2.0", "pin with "+repo+"@sha256:", "morzer update --digest sha256:").
		// Unsigned is the operator's policy to refuse, not push's.
		StderrContains("no SHA256SUMS.minisig")
	if _, ok := reg.Tagged("stable"); !ok {
		t.Error("the channel was not moved")
	}

	// The re-run a pipeline makes after a failure further on: the same
	// content is already published, and that is success.
	out := r.Run("--json", "release", "push", archive, repo, "--channel", "stable").ExitCode(0)
	out.FieldEquals("data.published", false).FieldEquals("data.version", "1.2.0")
	if d, _ := out.Field("data.manifest_digest").(string); !strings.HasPrefix(d, "sha256:") {
		t.Errorf("manifest_digest = %q", d)
	}
	if d, _ := out.Field("data.content_digest").(string); !strings.HasPrefix(d, "sha256:") {
		t.Errorf("content_digest = %q", d)
	}
}

// TestReleasePushChecksTheArchiveBeforeSendingAnything. An archive whose
// SHA256SUMS disagrees with its contents is refused as `release verify` would
// refuse it, before the registry hears a word.
func TestReleasePushChecksTheArchiveBeforeSendingAnything(t *testing.T) {
	r, reg, _, repo := pushFixture(t)

	notes := filepath.Join(r.Bundle, "RELEASE.md")
	data, err := os.ReadFile(notes)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notes, append(data, "\nchanged after it was summed\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	// Written directly: `release archive` would refuse this tree, which is
	// the point -- push must not depend on how the archive was made.
	corrupt := filepath.Join(t.TempDir(), "demo-1.2.0.tar.zst")
	if err := release.WriteArchive(r.Bundle, corrupt, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}

	r.Run("release", "push", corrupt, repo).ExitCode(2).OutputContains("RELEASE.md")
	if n := reg.Requests(); n != 0 {
		t.Errorf("the registry received %d requests for an archive that was refused", n)
	}
}

func TestReleasePushRefusesWhatItCannotPublish(t *testing.T) {
	r, reg, archive, repo := pushFixture(t)

	cases := []struct {
		name     string
		args     []string
		mentions string
	}{
		{"a tag other than the archive's version", []string{archive, repo + ":9.9.9"}, "9.9.9"},
		{"a channel named like a version", []string{archive, repo, "--channel", "1.4.0"}, "1.4.0"},
		{"a directory", []string{r.Bundle, repo}, "release archive"},
		{"another scheme", []string{archive, "https://example.com/demo.tar.zst"}, "OCI registry"},
		{"a signature that does not verify", []string{archive, repo, "--signing-key",
			"RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3"}, "signature"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r.Run(append([]string{"release", "push"}, tc.args...)...).
				ExitCode(2).OutputContains(tc.mentions)
		})
	}
	if n := reg.Requests(); n != 0 {
		t.Errorf("the registry received %d requests for pushes that were refused", n)
	}
}

// A reference refused for its scheme may carry a password; the refusal names
// the reference without it.
func TestReleasePushRefusalDoesNotRepeatAPassword(t *testing.T) {
	r, _, archive, _ := pushFixture(t)

	res := r.Run("release", "push", archive, "https://vendor:s3cret@example.com/demo.tar.zst").
		ExitCode(2).OutputContains("OCI registry", "example.com")
	if strings.Contains(res.Stdout+res.Stderr, "s3cret") {
		t.Errorf("the refusal repeats the password:\n--- stdout ---\n%s--- stderr ---\n%s",
			res.Stdout, res.Stderr)
	}
}

func TestReleasePushDryRunWritesNothing(t *testing.T) {
	r, reg, archive, repo := pushFixture(t)

	r.Run("--dry-run", "release", "push", archive, repo, "--channel", "stable").
		ExitCode(0).StdoutContains("would push demo 1.2.0", "would move to 1.2.0")
	if n := reg.Writes(); n != 0 {
		t.Errorf("a dry run made %d write requests", n)
	}
}

// TestAFailedChannelMoveReportsWhatMoved. The version is in place and one
// channel is not, and the re-run needs to know which: `--json` carries the
// result beside the error.
func TestAFailedChannelMoveReportsWhatMoved(t *testing.T) {
	r, reg, archive, repo := pushFixture(t)
	reg.FailTag = "edge"

	out := r.Run("--json", "release", "push", archive, repo, "--channel", "stable", "--channel", "edge").
		Failed()
	out.FieldEquals("ok", false).FieldEquals("data.published", true)

	channels, _ := out.Field("data.channels").([]any)
	if len(channels) != 2 {
		t.Fatalf("channels = %v", channels)
	}
	stable, _ := channels[0].(map[string]any)
	edge, _ := channels[1].(map[string]any)
	if stable["moved"] != true || edge["moved"] != false || edge["error"] == nil {
		t.Errorf("channels = %v", channels)
	}
}
