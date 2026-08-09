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
