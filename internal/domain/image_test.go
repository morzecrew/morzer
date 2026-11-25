package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

// Digest answers a question three call sites used to answer for themselves, two
// of them cutting at the first "@" and one at the last. The manifest's pinning
// rule makes those indistinguishable for anything it validates, which is
// exactly why the decision is pinned here rather than left to the call sites
// that currently happen to agree.
func TestImageSpecDigest(t *testing.T) {
	const sha = "sha256:0000000000000000000000000000000000000000000000000000000000000001"

	cases := []struct {
		name string
		ref  string
		want string
		ok   bool
	}{
		{"an ordinary pinned reference", "registry.example/demo/app@" + sha, sha, true},
		{"a reference with a port", "registry.example:5000/demo/app@" + sha, sha, true},
		{"no digest at all", "postgres:17", "", false},
		{"a tag that looks like one", "postgres:sha256-abc", "", false},

		// Malformed, and refused rather than half-read. The manifest's
		// pinning rule rejects all three before they reach a caller, so
		// what is being fixed here is the behaviour of the helper itself
		// -- callable from anywhere, including from `pack`, which reads
		// a manifest the caller may have assembled by hand.
		{"a trailing @", "registry.example/demo/app@", "", false},
		{"a leading @", "@" + sha, "", false},
		{"two @, the digest last", "registry.example/a@b@" + sha, sha, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ImageSpec{Ref: tc.ref}.Digest()
			if ok != tc.ok {
				t.Fatalf("ok = %t, want %t (got %q)", ok, tc.ok, got)
			}
			if got != tc.want {
				t.Errorf("digest = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestImageSpecJSONHasOneShape.
//
// `release show --json` is a supported contract, and a field that is sometimes
// a string and sometimes an object is a field every consumer has to branch on.
// So the marshaller always writes the mapping form, whichever spelling the
// manifest used -- and reading it back gives the same value, which is what
// makes the envelope round-trippable rather than merely printable.
func TestImageSpecJSONHasOneShape(t *testing.T) {
	const sha = "sha256:0000000000000000000000000000000000000000000000000000000000000001"

	for _, spec := range []ImageSpec{
		{Ref: "registry.example/demo/db@" + sha},                           // scalar in YAML
		{Ref: "registry.example/demo/app@" + sha, From: ImageFromBundle},   // mapping
		{Ref: "registry.example/demo/api@" + sha, From: ImageFromRegistry}, // stated default
	} {
		raw, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(raw), "{") {
			t.Errorf("marshalled as %s, which a consumer would have to branch on", raw)
		}

		var back ImageSpec
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatalf("cannot read back %s: %v", raw, err)
		}
		if back != spec {
			t.Errorf("round trip changed %+v into %+v", spec, back)
		}
	}

	// And the scalar form is still accepted on the way in, so a consumer
	// that hand-writes the shorthand is not refused by a decoder that only
	// ever emits the long one.
	var back ImageSpec
	if err := json.Unmarshal([]byte(`"registry.example/demo/db@`+sha+`"`), &back); err != nil {
		t.Fatal(err)
	}
	if back.Ref == "" || back.From != "" {
		t.Errorf("the scalar form decoded as %+v", back)
	}
}

// TestImageNamesNormaliseInjectively.
//
// The property the naming rule exists for, asserted as the property rather than
// as the regex: no two accepted names may produce the same environment
// variable. Before the rule, `web-ui` and `web.ui` both became IMAGE_WEB_UI --
// one pinned reference overwrote the other, and Go's randomised map iteration
// decided which one the deployment ran.
func TestImageNamesNormaliseInjectively(t *testing.T) {
	accepted := []string{"app", "web-ui", "db2", "a", "some-long-name-9"}

	seen := map[string]string{}
	for _, name := range accepted {
		m := validManifest()
		m.Images = map[string]ImageSpec{
			name: {Ref: "r/a@sha256:" + strings.Repeat("a", 64)},
		}
		if err := m.Validate(); err != nil {
			t.Errorf("%q was refused: %v", name, err)
			continue
		}

		// The same fold the runtime applies: upper-case, "-" and "." to
		// "_". Restated here rather than imported, because domain must
		// not depend on the lifecycle layer -- and a drift between the
		// two would make this test pass while the collision returned.
		env := strings.ToUpper(name)
		env = strings.ReplaceAll(env, "-", "_")
		env = strings.ReplaceAll(env, ".", "_")

		if other, clash := seen[env]; clash {
			t.Errorf("%q and %q both normalise to %q", name, other, env)
		}
		seen[env] = name
	}
}

// TestLocalAliasIsDerivedFromTheDigest.
//
// The alias exists because the daemon will not resolve a bundled image by the
// reference its manifest pins -- `docker image inspect` reports no such image,
// and `docker tag` refuses to make one. So Compose is handed a name the
// manager creates, and two properties of that name are what make it safe to
// hand over: it is derived from the digest rather than invented, so every
// apply renders the identical configuration; and it keeps the vendor's
// repository, so an operator reading `docker images` can tell whose it is.
func TestLocalAliasIsDerivedFromTheDigest(t *testing.T) {
	const sha = "sha256:0000000000000000000000000000000000000000000000000000000000000001"
	const tag = "morzer-sha256-0000000000000000000000000000000000000000000000000000000000000001"

	cases := []struct {
		name string
		ref  string
		want string
	}{
		{
			name: "an ordinary reference",
			ref:  "registry.example/demo/app@" + sha,
			want: "registry.example/demo/app:" + tag,
		},
		{
			// The colon in the host is a port, not a tag, and
			// cutting at it would name the registry itself.
			name: "a registry with a port",
			ref:  "registry.example:5000/demo/app@" + sha,
			want: "registry.example:5000/demo/app:" + tag,
		},
		{
			// Legal, and permitted by the pinning rule. Appending
			// a second tag would produce `postgres:17:morzer-…`,
			// which names nothing.
			name: "a reference carrying a tag as well as a digest",
			ref:  "postgres:17@" + sha,
			want: "postgres:" + tag,
		},
		{
			name: "a bare repository",
			ref:  "postgres@" + sha,
			want: "postgres:" + tag,
		},
		{
			name: "a tag on a repository with a port",
			ref:  "registry.example:5000/demo/app:v2@" + sha,
			want: "registry.example:5000/demo/app:" + tag,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ImageSpec{Ref: tc.ref}.LocalAlias()
			if !ok {
				t.Fatalf("no alias for %q", tc.ref)
			}
			if got != tc.want {
				t.Errorf("alias = %q, want %q", got, tc.want)
			}
			// The property the whole scheme rests on: same input,
			// same alias, for ever. A rendered configuration that
			// moved between applies would report a change on every
			// run.
			again, _ := ImageSpec{Ref: tc.ref}.LocalAlias()
			if again != got {
				t.Errorf("alias is not stable: %q then %q", got, again)
			}
			// One colon after the last slash, or it is not a
			// reference the daemon accepts.
			last := got
			if slash := strings.LastIndex(got, "/"); slash >= 0 {
				last = got[slash+1:]
			}
			if strings.Count(last, ":") != 1 {
				t.Errorf("alias %q does not name exactly one tag", got)
			}
		})
	}

	// An unpinned reference has nothing to derive from. Unreachable through
	// a validated manifest, and answered here rather than left to whatever
	// the string arithmetic happens to produce.
	if alias, ok := (ImageSpec{Ref: "postgres:17"}).LocalAlias(); ok {
		t.Errorf("an unpinned reference produced the alias %q", alias)
	}
}

// TestRuntimeRefSwitchesOnSourceAndNothingElse.
//
// Which reference the runtime is handed is the one thing `from` changes, and
// getting it backwards fails in a way that looks like success: a bundled image
// handed its manifest reference sends the deployment to the registry the
// bundle exists to avoid, and a pulled image handed an alias names something
// no registry has ever heard of.
func TestRuntimeRefSwitchesOnSourceAndNothingElse(t *testing.T) {
	const sha = "sha256:0000000000000000000000000000000000000000000000000000000000000002"
	ref := "registry.example/demo/app@" + sha

	pulled := ImageSpec{Ref: ref}
	if got := pulled.RuntimeRef(); got != ref {
		t.Errorf("a pulled image is deployed as %q, want the manifest's %q", got, ref)
	}
	if got := (ImageSpec{Ref: ref, From: ImageFromRegistry}).RuntimeRef(); got != ref {
		t.Errorf("an explicitly-pulled image is deployed as %q, want %q", got, ref)
	}

	bundled := ImageSpec{Ref: ref, From: ImageFromBundle}
	alias, _ := bundled.LocalAlias()
	if got := bundled.RuntimeRef(); got != alias {
		t.Errorf("a bundled image is deployed as %q, want the alias %q", got, alias)
	}
	if strings.Contains(bundled.RuntimeRef(), "@") {
		t.Error("the alias carries a digest, which no daemon will create")
	}
}

// TestTheThreeImageQuestionsHaveDifferentAnswers.
//
// "What does this release consist of", "what does it fetch from a registry"
// and "what must the daemon resolve" were one function for as long as the
// answers agreed. They stop agreeing the moment an image travels in the
// bundle, and each wrong pairing fails differently: pulling a bundled image
// contacts the registry the bundle removes, and checking presence by the
// manifest's reference reports every bundled image missing on a machine where
// ingest has just succeeded.
func TestTheThreeImageQuestionsHaveDifferentAnswers(t *testing.T) {
	const (
		one = "sha256:0000000000000000000000000000000000000000000000000000000000000001"
		two = "sha256:0000000000000000000000000000000000000000000000000000000000000002"
	)
	m := &Manifest{Images: map[string]ImageSpec{
		"db":  {Ref: "postgres@" + one},
		"app": {Ref: "registry.example/demo/app@" + two, From: ImageFromBundle},
	}}

	appAlias, _ := m.Images["app"].LocalAlias()

	consists := m.ImageRefs()
	if len(consists) != 2 {
		t.Fatalf("the release consists of %v", consists)
	}
	for _, ref := range consists {
		if !strings.Contains(ref, "@sha256:") {
			t.Errorf("%q is not the pinned identity", ref)
		}
	}

	if got := m.PulledImageRefs(); len(got) != 1 || got[0] != "postgres@"+one {
		t.Errorf("pulled = %v, want only the registry-sourced image", got)
	}
	if got := m.BundledImageRefs(); len(got) != 1 || got[0] != "registry.example/demo/app@"+two {
		t.Errorf("bundled = %v, want only the bundled image, by its manifest reference", got)
	}

	runtime := m.RuntimeImageRefs()
	if len(runtime) != 2 {
		t.Fatalf("the daemon must resolve %v", runtime)
	}
	// Sorted by name: app, then db.
	if runtime[0] != appAlias {
		t.Errorf("the bundled image is deployed as %q, want the alias %q", runtime[0], appAlias)
	}
	if runtime[1] != "postgres@"+one {
		t.Errorf("the pulled image is deployed as %q, want its manifest reference", runtime[1])
	}
}

// TestAManifestWithNoBundledImagesAnswersAsItAlwaysDid.
//
// The overwhelmingly common case, and the one a regression here would be
// noticed in last: nothing is bundled, so every question has the same answer
// it had before bundling existed.
func TestAManifestWithNoBundledImagesAnswersAsItAlwaysDid(t *testing.T) {
	const sha = "sha256:0000000000000000000000000000000000000000000000000000000000000003"
	m := &Manifest{Images: map[string]ImageSpec{"db": {Ref: "postgres@" + sha}}}

	refs := m.ImageRefs()
	if got := m.PulledImageRefs(); len(got) != 1 || got[0] != refs[0] {
		t.Errorf("pulled = %v, want %v", got, refs)
	}
	if got := m.RuntimeImageRefs(); len(got) != 1 || got[0] != refs[0] {
		t.Errorf("runtime = %v, want %v", got, refs)
	}
	if got := m.BundledImageRefs(); len(got) != 0 {
		t.Errorf("bundled = %v, want nothing", got)
	}
}

// TestRuntimeRefFallsBackRatherThanReturningNothing.
//
// A bundled image with no digest has no alias to be deployed under. Returning
// the manifest's reference keeps the Compose environment builder total -- what
// the deployment then does with an unpinned reference is what it did before
// bundling existed, which is better than a service with an empty image.
//
// Unreachable through a validated manifest, and decided here rather than left
// to whatever the string arithmetic produced.
func TestRuntimeRefFallsBackRatherThanReturningNothing(t *testing.T) {
	spec := ImageSpec{Ref: "registry.example/demo/app:latest", From: ImageFromBundle}
	if got := spec.RuntimeRef(); got != spec.Ref {
		t.Errorf("RuntimeRef() = %q, want the manifest reference %q", got, spec.Ref)
	}
}

// TestShortImageRefLeavesAReferenceWithNoDigestAlone.
//
// The empty case of a "cut at the separator" helper, decided rather than
// inherited: a reference with no digest is already short, and returning ""
// would put a message in front of an operator that names no image at all.
func TestShortImageRefLeavesAReferenceWithNoDigestAlone(t *testing.T) {
	const sha = "sha256:0000000000000000000000000000000000000000000000000000000000000001"

	cases := map[string]string{
		"registry.example/demo/app@" + sha: "registry.example/demo/app",
		"registry.example/demo/app:v2":     "registry.example/demo/app:v2",
		"postgres":                         "postgres",
		"":                                 "",
		// A leading "@" leaves nothing in front of it, and the whole
		// string is the least misleading thing to print.
		"@" + sha: "@" + sha,
	}

	for ref, want := range cases {
		if got := ShortImageRef(ref); got != want {
			t.Errorf("ShortImageRef(%q) = %q, want %q", ref, got, want)
		}
	}
}

// ImageKey is the exclusion's whole correctness (RFC 0031 decision 14): two
// references the daemon resolves to one name must key the same, or a prune
// removes the name another release resolves by. Every row in the first group
// is a spelling P1 measured as the same name.
func TestImageKeyIsTheNameTheDaemonResolves(t *testing.T) {
	d := "sha256:" + strings.Repeat("a", 64)
	alias := "morzer-sha256-" + strings.Repeat("a", 64)

	same := map[string][]string{
		"docker.io/library/postgres@" + d: {
			"postgres@" + d,
			"postgres:17@" + d,
			"docker.io/library/postgres@" + d,
			"index.docker.io/library/postgres@" + d,
			"postgres:" + alias,
		},
		"localhost:34781/rfc31p1/app@" + d: {
			"localhost:34781/rfc31p1/app@" + d,
			"localhost:34781/rfc31p1/app:" + alias,
		},
		"docker.io/myorg/app@" + d:          {"myorg/app@" + d, "docker.io/myorg/app:" + alias},
		"localhost/app@" + d:                {"localhost/app@" + d},
		"registry.example/erp/backend@" + d: {"registry.example/erp/backend:t@" + d},
		"Registry/app@" + d:                 {"Registry/app@" + d},
	}
	for want, refs := range same {
		for _, ref := range refs {
			got, ok := ImageKey(ref)
			if !ok || got != want {
				t.Errorf("ImageKey(%q) = %q, %t; want %q", ref, got, ok, want)
			}
		}
	}

	// Different names stay different: another digest, another namespace.
	a, _ := ImageKey("postgres@" + d)
	for _, other := range []string{
		"postgres@sha256:" + strings.Repeat("b", 64),
		"myorg/postgres@" + d,
		"registry.example/postgres@" + d,
	} {
		if k, _ := ImageKey(other); k == a {
			t.Errorf("%q keys the same as postgres@%s", other, d)
		}
	}

	if got, ok := ImageKey("postgres:17"); ok {
		t.Errorf("an unpinned reference keyed as %q", got)
	}
}

// The tag `release push --tag-images` writes (RFC 0039 decisions 21 and 27):
// a `+` the tag cannot carry is written as `_`, the digest's first 12 hex
// digits end it, and one past 128 characters is refused.
func TestImageTagNamesTheProductTheVersionAndTheDigest(t *testing.T) {
	ref := "registry.example/demo/api@sha256:0123456789ab" + strings.Repeat("f", 52)
	for version, want := range map[string]string{
		"1.2.0":            "morzer-demo-1.2.0-0123456789ab",
		"1.2.0-rc.1":       "morzer-demo-1.2.0-rc.1-0123456789ab",
		"1.2.0-rc.1+build": "morzer-demo-1.2.0-rc.1_build-0123456789ab",
	} {
		got, err := ImageTag("demo", MustParseVersion(version), ref)
		if err != nil || got != want {
			t.Errorf("ImageTag(%s) = %q, %v; want %q", version, got, err, want)
		}
	}

	long := MustParseVersion("1.2.0-" + strings.Repeat("a", 100))
	if got, err := ImageTag("demo", long, ref); err == nil {
		t.Errorf("a tag of %d characters was accepted: %s", len(got), got)
	}
	// 128 exactly is a tag: "morzer-" + "demo-" + version + "-" + 12 hex.
	exact := MustParseVersion("1.2.0-" + strings.Repeat("a", 128-len("morzer-demo-1.2.0--0123456789ab")))
	if got, err := ImageTag("demo", exact, ref); err != nil || len(got) != 128 {
		t.Errorf("a tag of 128 characters: %q, %v", got, err)
	}
}

func TestImagesToTagSelectsRegistryImagesUnderAPrefix(t *testing.T) {
	d := "@sha256:" + strings.Repeat("a", 64)
	m := &Manifest{Images: map[string]ImageSpec{
		"api":     {Ref: "registry.example/demo/api" + d},
		"old":     {Ref: "registry.example/demo-old/api" + d},
		"worker":  {Ref: "registry.example/demo/jobs/worker:2" + d},
		"bundled": {Ref: "registry.example/demo/bundled" + d, From: ImageFromBundle},
		"db":      {Ref: "postgres" + d},
	}}
	m.Metadata.Name, m.Metadata.Version = "demo", MustParseVersion("1.2.0")

	got, err := m.ImagesToTag([]string{"registry.example/demo/"})
	if err != nil {
		t.Fatal(err)
	}
	tagged := map[string]bool{}
	for _, image := range got {
		if (image.Tag == "") == (image.Skip == "") {
			t.Errorf("%s has tag %q and skip %q; exactly one is set", image.Name, image.Tag, image.Skip)
		}
		tagged[image.Name] = image.Tag != ""
	}
	want := map[string]bool{"api": true, "worker": true, "old": false, "bundled": false, "db": false}
	if len(got) != len(want) {
		t.Fatalf("%d images listed, want %d: %+v", len(got), len(want), got)
	}
	for name, w := range want {
		if tagged[name] != w {
			t.Errorf("%s tagged = %t, want %t", name, tagged[name], w)
		}
	}
	if got[0].Name != "api" || got[len(got)-1].Name != "worker" {
		t.Errorf("not in key order: %+v", got)
	}

	// A whole repository is a prefix of itself.
	// api, bundled, db, old, worker: old is the fourth.
	if got, err := m.ImagesToTag([]string{"registry.example/demo-old/api"}); err != nil || got[3].Tag == "" {
		t.Errorf("a repository named in full: %+v, %v", got, err)
	}
}

// Each refusal happens before any registry is asked anything, so a mistyped
// prefix costs a message rather than a release whose images nobody protected.
func TestImagesToTagRefusesWhatWouldProtectLessThanAsked(t *testing.T) {
	d := "@sha256:" + strings.Repeat("a", 64)
	m := &Manifest{Images: map[string]ImageSpec{
		"api":     {Ref: "registry.example/demo/api" + d},
		"bundled": {Ref: "registry.example/kept/app" + d, From: ImageFromBundle},
		"hub":     {Ref: "vendor/api" + d},
	}}
	m.Metadata.Name, m.Metadata.Version = "demo", MustParseVersion("1.2.0")

	for _, tc := range []struct {
		name     string
		prefixes []string
		mentions string
	}{
		{"empty", []string{"/"}, "needs a repository prefix"},
		{"a scheme", []string{"oci://registry.example/demo"}, "names a scheme"},
		{"a tag", []string{"registry.example/demo:1.2.0"}, "names a tag"},
		{"a digest", []string{"registry.example/demo/api" + d}, "names a digest"},
		{"selects nothing", []string{"registry.example/dem"}, "registry.example/dem selects no image"},
		{"one of two selects nothing", []string{"registry.example/demo", "registry.example/typo"}, "typo selects no image"},
		{"covers only a bundled image", []string{"registry.example/kept"}, "kept selects no image"},
		{"Docker Hub", []string{"docker.io/vendor"}, "hub is on Docker Hub"},
		{"no host is not Docker Hub's namespace", []string{"vendor"}, "vendor selects no image"},
	} {
		_, err := m.ImagesToTag(tc.prefixes)
		switch {
		case err == nil:
			t.Errorf("%s: %v was accepted", tc.name, tc.prefixes)
		case AsError(err).Code != CodeUsage:
			t.Errorf("%s: exit code %v, want usage", tc.name, AsError(err).Code)
		case !strings.Contains(AsError(err).Message, tc.mentions):
			t.Errorf("%s: %q does not say %q", tc.name, AsError(err).Message, tc.mentions)
		}
	}

	// A port is not a tag.
	ported := &Manifest{Images: map[string]ImageSpec{"api": {Ref: "localhost:5000/demo/api" + d}}}
	ported.Metadata.Name, ported.Metadata.Version = "demo", MustParseVersion("1.2.0")
	if _, err := ported.ImagesToTag([]string{"localhost:5000"}); err != nil {
		t.Errorf("a registry with a port: %v", err)
	}
}
