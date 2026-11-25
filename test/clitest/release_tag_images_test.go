package clitest_test

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/morzecrew/morzer/test/clitest"
	"github.com/morzecrew/morzer/test/fakes"
)

// `release push --tag-images` (RFC 0039 P2): each image the release pins under
// a prefix is tagged in its own repository before the bundle is written, so a
// registry's retention keeps it.

// tagged is the example bundle with its two images pinned in an in-process
// registry: app to a single image manifest, db to an index.
type tagged struct {
	r             *clitest.Runner
	reg           *fakes.Registry
	archive, repo string
	prefix        string
	app, db       string // the pinned digests
	appTag, dbTag string // the tags push writes
}

func tagFixture(t *testing.T) tagged {
	t.Helper()
	f := tagged{r: clitest.New(t), reg: fakes.NewRegistry()}
	srv := httptest.NewServer(f.reg)
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	f.repo, f.prefix = "oci://"+host+"/demo/bundle", host+"/demo"

	manifest := `{"schemaVersion":2,"mediaType":"` + ocispec.MediaTypeImageManifest + `",` +
		`"config":{"mediaType":"` + ocispec.MediaTypeImageConfig + `","digest":"sha256:` + strings.Repeat("c", 64) + `","size":2},` +
		`"layers":[]}`
	f.app = f.reg.Put("demo/app", "", ocispec.MediaTypeImageManifest, []byte(manifest))
	index := `{"schemaVersion":2,"mediaType":"` + ocispec.MediaTypeImageIndex + `",` +
		`"manifests":[{"mediaType":"` + ocispec.MediaTypeImageManifest + `","digest":"sha256:` + strings.Repeat("d", 64) + `",` +
		`"size":2,"platform":{"os":"linux","architecture":"amd64"}}]}`
	f.db = f.reg.Put("demo/db", "", ocispec.MediaTypeImageIndex, []byte(index))
	f.appTag, f.dbTag = "morzer-demo-1.2.0-"+f.app[7:19], "morzer-demo-1.2.0-"+f.db[7:19]

	for _, name := range []string{"manifest.yaml", "compose/compose.yaml", "compose/compose.embedded.yaml"} {
		path := filepath.Join(f.r.Bundle, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := strings.NewReplacer(
			"registry.example/demo/app@sha256:"+strings.Repeat("0", 63)+"1", host+"/demo/app@"+f.app,
			"registry.example/demo/db@sha256:"+strings.Repeat("0", 63)+"2", host+"/demo/db@"+f.db,
		).Replace(string(data))
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f.r.Run("release", "build", f.r.Bundle).ExitCode(0)
	f.archive = filepath.Join(t.TempDir(), "demo-1.2.0.tar.zst")
	f.r.Run("release", "archive", f.r.Bundle, "-o", f.archive).ExitCode(0)
	return f
}

// images is `data.images` by name.
func images(t *testing.T, res clitest.Result) map[string]map[string]any {
	t.Helper()
	list, _ := res.Field("data.images").([]any)
	out := map[string]map[string]any{}
	for _, entry := range list {
		image, _ := entry.(map[string]any)
		name, _ := image["name"].(string)
		out[name] = image
	}
	return out
}

func TestReleasePushTagsTheImagesItPins(t *testing.T) {
	f := tagFixture(t)

	out := f.r.Run("--json", "release", "push", f.archive, f.repo, "--tag-images", f.prefix).ExitCode(0)
	got := images(t, out)
	if got["app"]["status"] != "tagged" || got["db"]["status"] != "tagged" || got["app"]["tag"] != f.appTag {
		t.Errorf("images = %v", got)
	}
	// Each in its own repository, at the digest the release pins: the index
	// as it is, not a platform's manifest picked out of it (decision 26).
	if d, _ := f.reg.TaggedIn("demo/app", f.appTag); d != f.app {
		t.Errorf("demo/app:%s = %q, want %s", f.appTag, d, f.app)
	}
	if d, _ := f.reg.TaggedIn("demo/db", f.dbTag); d != f.db {
		t.Errorf("demo/db:%s = %q, want %s", f.dbTag, d, f.db)
	}
	if _, ok := f.reg.Tagged("1.2.0"); !ok {
		t.Error("the version was not pushed")
	}

	// The re-run converges and writes nothing: the version is published and
	// each image already tagged at its digest (decision 24).
	writes := f.reg.Writes()
	f.r.Run("release", "push", f.archive, f.repo, "--tag-images", f.prefix).ExitCode(0).
		StdoutContains("already published", "image app", "already tagged "+f.appTag)
	if n := f.reg.Writes() - writes; n != 0 {
		t.Errorf("the re-run made %d writes", n)
	}
}

// A push without the flag answers as it did before P2.
func TestReleasePushWithoutTagImagesReportsNoImages(t *testing.T) {
	f := tagFixture(t)

	data, _ := f.r.Run("--json", "release", "push", f.archive, f.repo).ExitCode(0).JSON()["data"].(map[string]any)
	if _, ok := data["images"]; ok {
		t.Errorf("images reported without --tag-images: %v", data["images"])
	}
	if _, ok := f.reg.TaggedIn("demo/app", f.appTag); ok {
		t.Error("an image was tagged without --tag-images")
	}
}

// Matched at a `/`: naming one repository tags that image, and the other is
// listed with the reason it is not tagged (decision 22).
func TestReleasePushTagsOnlyTheImagesUnderAPrefix(t *testing.T) {
	f := tagFixture(t)

	got := images(t, f.r.Run("--json", "release", "push", f.archive, f.repo, "--tag-images", f.prefix+"/app/").ExitCode(0))
	if got["app"]["status"] != "tagged" || got["db"]["status"] != "skipped" || got["db"]["reason"] == nil {
		t.Errorf("images = %v", got)
	}
	if _, ok := f.reg.TaggedIn("demo/db", f.dbTag); ok {
		t.Error("an image outside the prefix was tagged")
	}
}

// A prefix that would protect less than the vendor asked is refused before the
// registry hears a word.
func TestReleasePushRefusesATagImagesPrefixBeforeAnyRequest(t *testing.T) {
	f := tagFixture(t)

	for _, tc := range []struct{ prefix, mentions string }{
		{f.prefix[:len(f.prefix)-1], "selects no image"},
		{"oci://" + f.prefix, "names a scheme"},
		{f.prefix + "/app:1.2.0", "names a tag"},
	} {
		f.r.Run("release", "push", f.archive, f.repo, "--tag-images", tc.prefix).
			ExitCode(2).OutputContains(tc.mentions)
	}
	if n := f.reg.Requests(); n != 0 {
		t.Errorf("the registry received %d requests for pushes that were refused", n)
	}
}

// Decision 24: a tag that names another digest is not moved, and nothing of
// the bundle is pushed. The images after it are not tried, nor the channels.
func TestReleasePushRefusesAnImageTagThatNamesAnotherDigest(t *testing.T) {
	f := tagFixture(t)
	other := f.reg.Put("demo/app", f.appTag, ocispec.MediaTypeImageManifest, []byte(`{"schemaVersion":2}`))

	out := f.r.Run("--json", "release", "push", f.archive, f.repo, "--tag-images", f.prefix, "--channel", "stable").
		ExitCode(2).FieldEquals("data.published", false)
	if msg, _ := out.Field("error.message").(string); !strings.Contains(msg, other) || !strings.Contains(msg, f.app) {
		t.Errorf("the refusal does not name both digests: %s", msg)
	}
	got := images(t, out)
	if got["app"]["status"] != "failed" || got["db"]["status"] != "not-tried" {
		t.Errorf("images = %v", got)
	}
	if d, _ := f.reg.TaggedIn("demo/app", f.appTag); d != other {
		t.Errorf("the tag was moved to %s", d)
	}
	if _, ok := f.reg.Tagged("1.2.0"); ok {
		t.Error("the version was pushed although an image was not tagged")
	}
	if _, ok := f.reg.Tagged("stable"); ok {
		t.Error("a channel moved although an image was not tagged")
	}
}

// What P2 exists to catch: the registry deleted an image the release pins, and
// the vendor hears it from the push rather than a customer from an install.
func TestReleasePushStopsAtAPinnedImageTheRegistryDeleted(t *testing.T) {
	f := tagFixture(t)
	f.reg.Delete(f.db)

	f.r.Run("release", "push", f.archive, f.repo, "--tag-images", f.prefix).
		ExitCode(2).OutputContains("did not push", "image db", "no longer has")
	if _, ok := f.reg.Tagged("1.2.0"); ok {
		t.Error("the version was pushed although an image it pins is gone")
	}
}

// Decision 28: a version already published keeps its bundle when tagging
// fails, and no channel moves.
func TestAPublishedVersionKeepsItsBundleWhenTaggingFails(t *testing.T) {
	f := tagFixture(t)
	f.r.Run("release", "push", f.archive, f.repo).ExitCode(0)
	version, _ := f.reg.Tagged("1.2.0")
	f.reg.Delete(f.db)

	f.r.Run("release", "push", f.archive, f.repo, "--tag-images", f.prefix, "--channel", "stable").
		ExitCode(2).StdoutContains("already published", "channel stable", "not tried")
	if now, ok := f.reg.Tagged("1.2.0"); !ok || now != version {
		t.Errorf("the published version now names %q, was %s", now, version)
	}
	if _, ok := f.reg.Tagged("stable"); ok {
		t.Error("a channel moved although an image was not tagged")
	}
}

// The images are tagged and the bundle write then fails: the report lists the
// tagged images and says the version was not pushed.
func TestReleasePushReportsABundleWriteThatFailedAfterTagging(t *testing.T) {
	f := tagFixture(t)
	f.reg.FailTag = "1.2.0"

	out := f.r.Run("--json", "release", "push", f.archive, f.repo, "--tag-images", f.prefix).
		Failed().FieldEquals("data.published", false)
	if got := images(t, out); got["app"]["status"] != "tagged" || got["db"]["status"] != "tagged" {
		t.Errorf("images = %v", got)
	}
	f.r.Run("release", "push", f.archive, f.repo, "--tag-images", f.prefix).
		Failed().StdoutContains("did not push", "image app")
}

// Decisions 17 and 25: a dry run resolves every tag and writes none.
func TestReleasePushDryRunTagsNothing(t *testing.T) {
	f := tagFixture(t)

	f.r.Run("--dry-run", "release", "push", f.archive, f.repo, "--tag-images", f.prefix).
		ExitCode(0).StdoutContains("would push", "would tag "+f.appTag, "would tag "+f.dbTag)
	if n := f.reg.Writes(); n != 0 {
		t.Errorf("a dry run made %d write requests", n)
	}
}
