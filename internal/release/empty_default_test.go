package release_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/release"
)

// withParameter adds one parameter declaration to the example bundle.
func withParameter(t *testing.T, decl string) string {
	t.Helper()
	return bundle(t, func(dir string) {
		edit(t, filepath.Join(dir, "manifest.yaml"), func(s string) string {
			return strings.Replace(s, "parameters:\n", "parameters:\n  listen:\n"+decl, 1)
		})
	})
}

// `default: ""` is a default, the empty one, and not the absence of one: an
// empty host address is what Compose reads as every address, IPv4 and IPv6
// alike, and a bundle that had to choose a narrower one cost installations
// one of the two (#368).
func TestAnEmptyDefaultIsADefault(t *testing.T) {
	declared := withParameter(t, "    type: string\n    default: \"\"\n")
	rel, err := release.Load(declared)
	if err != nil {
		t.Fatalf("a bundle declaring an empty default did not load: %v", err)
	}
	if missing := domain.MissingValues(rel.Manifest.Parameters, nil); len(missing) != 0 {
		t.Errorf("a parameter with an empty default is asked for as if it had none: %v", missing)
	}

	// An enum that lists the empty value can default to it.
	if _, err := release.Load(withParameter(t, "    type: enum\n    values: [\"\", auto]\n    default: \"\"\n")); err != nil {
		t.Errorf("an enum listing the empty value cannot default to it: %v", err)
	}

	absent := withParameter(t, "    type: string\n")
	rel, err = release.Load(absent)
	if err != nil {
		t.Fatal(err)
	}
	if missing := domain.MissingValues(rel.Manifest.Parameters, nil); len(missing) != 1 || missing[0] != "listen" {
		t.Errorf("a parameter with no default is not asked for: %v", missing)
	}
}

// An empty default is held to its declaration as any other is: empty is no
// port and no member of an enum, and beside `required` it is a second,
// contradictory statement.
func TestAnEmptyDefaultIsCheckedAsAnyDefaultIs(t *testing.T) {
	cases := map[string]struct {
		decl, want string
	}{
		"a port":   {"    type: port\n    default: \"\"\n", "parameters.listen.default"},
		"an enum":  {"    type: enum\n    values: [a, b]\n    default: \"\"\n", "parameters.listen.default"},
		"required": {"    type: string\n    required: true\n    default: \"\"\n", "cannot be both required and have a default"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := release.Load(withParameter(t, tc.decl))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want a refusal naming %q, got: %v", tc.want, err)
			}
		})
	}
}
