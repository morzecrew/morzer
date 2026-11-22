package clitest_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	signer "github.com/morzecrew/morzer/internal/adapters/sign/minisign"
	"github.com/morzecrew/morzer/internal/adapters/source/oci"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/clitest"
	"github.com/morzecrew/morzer/test/fakes"
)

// `release index` (RFC 0037 decisions 24, 27 and 33) against an in-process
// registry. The index is read back through the adapter and checked with the
// real minisign verifier, since what matters is what a machine will read.

// indexFixture pushes the example bundle to the channel stable and mints a
// key, returning the channel reference, the bundle's content digest, the key
// file and its public half.
func indexFixture(t *testing.T) (*clitest.Runner, *fakes.Registry, string, string, string, string) {
	t.Helper()
	r, reg, archive, repo := pushFixture(t)
	out := r.Run("--json", "release", "push", archive, repo, "--channel", "stable").ExitCode(0)
	contentDigest, _ := out.Field("data.content_digest").(string)
	keyPath, pub := indexKey(t)
	return r, reg, repo + ":stable", contentDigest, keyPath, pub
}

func indexKey(t *testing.T) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "release.key")
	pub, err := signer.New(path, "test").EnsureKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return path, pub.Line
}

// publishedIndex reads the index under the channel's index tag and checks its
// signature against pub, as a machine would.
func publishedIndex(t *testing.T, channel, pub string) domain.ChannelIndex {
	t.Helper()
	src := oci.New()
	defer func() { _ = src.Close() }()
	got, found, err := src.FetchIndex(context.Background(), indexRef(channel))
	if err != nil || !found || got.Problem != "" {
		t.Fatalf("no readable index: found %v, problem %q, err %v", found, got.Problem, err)
	}
	if !signer.NewChecker().Check(got.Index, got.Signature, pub) {
		t.Fatal("the index does not verify against the key")
	}
	idx, err := domain.ParseChannelIndex(got.Index)
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

func indexRef(channel string) ports.Ref {
	return ports.Ref{Scheme: oci.Scheme, Location: strings.TrimPrefix(channel, "oci://")}
}

// plantIndex publishes idx signed with the key at keyPath, as another run or
// anyone who can push to the repository might.
func plantIndex(t *testing.T, channel, keyPath string, idx domain.ChannelIndex) {
	t.Helper()
	ctx := context.Background()
	data, err := idx.Encode()
	if err != nil {
		t.Fatal(err)
	}
	sig, err := signer.New(keyPath, "test").Sign(ctx, data, idx.Comment())
	if err != nil {
		t.Fatal(err)
	}
	src := oci.New()
	defer func() { _ = src.Close() }()
	if _, err := src.PushIndex(ctx, indexRef(channel), data, sig.Encoded); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseIndexSignsWhatTheChannelPointsAt(t *testing.T) {
	r, reg, channel, contentDigest, keyPath, pub := indexFixture(t)

	out := r.Run("--json", "release", "index", channel, "--key", keyPath).ExitCode(0)
	out.FieldEquals("data.channel", "stable").
		FieldEquals("data.reference", channel+".index").
		FieldEquals("data.release.version", "1.2.0").
		FieldEquals("data.release.digest", contentDigest)
	if _, ok := reg.Tagged("stable.index"); !ok {
		t.Fatal("no index under stable.index")
	}

	first := publishedIndex(t, channel, pub)
	if first.Channel != "stable" || first.Release.Digest != contentDigest {
		t.Errorf("index = %+v", first)
	}
	if got := first.ExpiresAt.Sub(first.IssuedAt.Time); got != 7*24*time.Hour {
		t.Errorf("valid for %s, want the default of 168h", got)
	}

	// Re-signing is the same command again: a higher sequence, a new
	// expiry, nothing to warn about.
	r.Run("release", "index", channel, "--key", keyPath, "--valid-for", "72h").ExitCode(0).
		StderrContains("published " + channel + ".index").NoOutputContains("warning")
	second := publishedIndex(t, channel, pub)
	if second.Sequence <= first.Sequence {
		t.Errorf("sequence %d after %d", second.Sequence, first.Sequence)
	}
	if got := second.ExpiresAt.Sub(second.IssuedAt.Time); got != 72*time.Hour {
		t.Errorf("valid for %s, want 72h", got)
	}
}

// Only an index this key signed counts against the sequence. One planted by
// another key at a sequence far ahead is replaced, and the vendor is told.
func TestReleaseIndexReplacesAnIndexAnotherKeySigned(t *testing.T) {
	r, _, channel, contentDigest, keyPath, pub := indexFixture(t)
	otherKey, _ := indexKey(t)
	plantIndex(t, channel, otherKey, domain.NewChannelIndex("stable", domain.MustParseVersion("1.2.0"),
		contentDigest, time.Now().Add(240*time.Hour), time.Hour))

	r.Run("release", "index", channel, "--key", keyPath).ExitCode(0).
		StderrContains("is not signed by this key, so it is replaced")
	if idx := publishedIndex(t, channel, pub); idx.Sequence > time.Now().UnixMilli() {
		t.Errorf("the planted index is still there: sequence %d", idx.Sequence)
	}
}

// An index this key signed at a sequence not below this run's is refused
// before anything is pushed: every machine that accepted it would refuse the
// new one as a replay (decision 16).
func TestReleaseIndexRefusesASequenceNotHigherThanItsOwn(t *testing.T) {
	r, reg, channel, contentDigest, keyPath, _ := indexFixture(t)
	plantIndex(t, channel, keyPath, domain.NewChannelIndex("stable", domain.MustParseVersion("1.2.0"),
		contentDigest, time.Now().Add(time.Hour), time.Hour))
	planted, _ := reg.Tagged("stable.index")

	r.Run("release", "index", channel, "--key", keyPath).ExitCode(2).
		OutputContains("is not higher than", "check this machine's clock")
	if now, _ := reg.Tagged("stable.index"); now != planted {
		t.Error("the refused run moved the index tag")
	}
}

func TestReleaseIndexRefusesWhatItCannotPublishBeforeAnyRequest(t *testing.T) {
	r, reg, _, repo := pushFixture(t)
	keyPath, _ := indexKey(t)

	cases := []struct {
		name     string
		args     []string
		mentions string
	}{
		{"no channel tag", []string{repo, "--key", keyPath}, "names no channel"},
		{"a digest", []string{repo + "@sha256:" + strings.Repeat("a", 64), "--key", keyPath}, "names no channel"},
		{"a version", []string{repo + ":1.2.0", "--key", keyPath}, "names the version 1.2.0"},
		{"an index tag", []string{repo + ":stable.index", "--key", keyPath}, "names an index tag"},
		{"another scheme", []string{"https://example.com/demo.tar.zst", "--key", keyPath}, "OCI registry"},
		{"no key", []string{repo + ":stable"}, "needs --key"},
		{"no file at the key's path", []string{repo + ":stable", "--key", keyPath + ".missing"}, "no key at"},
		{"too short a validity", []string{repo + ":stable", "--key", keyPath, "--valid-for", "30s"}, "at least 1m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r.Run(append([]string{"release", "index"}, tc.args...)...).
				ExitCode(2).OutputContains(tc.mentions)
		})
	}
	if n := reg.Requests(); n != 0 {
		t.Errorf("the registry received %d requests for runs that were refused", n)
	}
}

func TestReleaseIndexDryRunPushesNothing(t *testing.T) {
	r, reg, channel, _, keyPath, _ := indexFixture(t)
	writes := reg.Writes()

	r.Run("--dry-run", "release", "index", channel, "--key", keyPath).ExitCode(0).
		StderrContains("would publish " + channel + ".index")
	if n := reg.Writes() - writes; n != 0 {
		t.Errorf("a dry run made %d write requests", n)
	}
	if _, ok := reg.Tagged("stable.index"); ok {
		t.Error("a dry run published an index")
	}
}
