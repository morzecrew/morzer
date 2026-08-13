package domain_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

// The inventory is an ABI (RFC 0024 decision 2), and these are the properties
// the rest of the feature reads it for rather than re-deriving.
//
// They are asserted here rather than left to review because the table is the
// one place in this feature where a mistake is silent: a `never` row that
// acquires a name becomes collectable, and nothing else in the system would
// notice.

// A refusal must not be nameable, because the name is what a collector
// registers under.
//
// This is the hinge between the two halves of the policy. Inclusion is an
// allowlist keyed by name, so a `never` row with a name is not a refusal at
// all -- it is a component waiting for somebody to write its collector, and the
// build would not object. The `ops` half asserts the other direction: that
// every collector's name is a classified row.
func TestARefusalCannotBeCollected(t *testing.T) {
	for _, c := range domain.SupportComponents(domain.SupportNever) {
		require.Emptyf(t, c.Name,
			"%q is classified never and has an archive name, which is how a refusal "+
				"becomes a component", c.Title)
		require.Falsef(t, domain.SupportCollected(c.Name),
			"%q is collectable", c.Title)
	}
}

// Everything that is not a refusal has a name, and no two share one.
//
// A duplicate would be two collectors writing one archive entry, where the
// second silently wins and the page still lists both.
func TestEveryCollectedComponentHasADistinctName(t *testing.T) {
	seen := map[string]string{}

	for _, c := range domain.SupportInventory {
		if c.Class == domain.SupportNever {
			continue
		}
		require.NotEmptyf(t, c.Name, "%q is collected and has no archive name", c.Title)

		previous, clash := seen[c.Name]
		require.Falsef(t, clash, "%q and %q both write %s", previous, c.Title, c.Name)
		seen[c.Name] = c.Title

		require.Truef(t, domain.SupportCollected(c.Name), "%s is not collectable", c.Name)
	}
}

// Every row carries its reason, in both directions of the policy.
//
// The generated page prints these verbatim, so an empty one is a row on the
// operator's contract page with a blank cell where the argument should be --
// and for a refusal, the argument is the entire value of enumerating it.
func TestEveryRowSaysWhy(t *testing.T) {
	for _, c := range domain.SupportInventory {
		require.NotEmptyf(t, c.Title, "a row has no title")
		require.NotEmptyf(t, c.Reason, "%q gives no reason", c.Title)
	}
}

// The refused paths are real paths, resolved from the layout rather than
// written down beside the check.
//
// This is what makes the refusal an outcome-guard: a test can ask whether an
// archive entry lies under one of these, and the answer stays true when a path
// moves. A literal list would keep passing while pointing at a directory that
// no longer exists.
func TestRefusedPathsResolveAgainstTheLayout(t *testing.T) {
	paths := domain.Paths{
		EtcDir: "/etc/morzer/acme",
		VarDir: "/var/lib/morzer/acme",
		RunDir: "/run/morzer/acme",
		OptDir: "/opt/morzer/acme",
	}

	refused := domain.SupportRefusedPaths(paths)
	require.NotEmpty(t, refused, "nothing is refused, so the enumeration guards nothing")

	for _, path := range refused {
		require.Truef(t, filepath.IsAbs(path), "%s is not an absolute path", path)
	}

	// The four the RFC names by their location on disk. Named individually
	// rather than counted, so adding a refusal does not fail this test and
	// removing one of these does.
	require.Contains(t, refused, paths.AgeDir())
	require.Contains(t, refused, paths.SecretsFile())
	require.Contains(t, refused, paths.SigningDir())
	require.Contains(t, refused, paths.SecretsRenderDir())

	require.IsIncreasing(t, refused, "refused paths are not sorted, so failures name them in a different order each run")
}

// A refusal that names no path is still a refusal, and must not silently
// contribute an empty string to the guard.
//
// Two rows are deliberately path-less -- backup target credentials live inside
// the state this archive already reads selectively, and the recovery export is
// an artifact rather than a location -- so the guard has to tolerate a nil
// source without treating "" as a directory that every path is under.
func TestAPathlessRefusalContributesNothingToTheGuard(t *testing.T) {
	refused := domain.SupportRefusedPaths(domain.Paths{
		EtcDir: "/etc/morzer/acme",
		VarDir: "/var/lib/morzer/acme",
		RunDir: "/run/morzer/acme",
		OptDir: "/opt/morzer/acme",
	})

	for _, path := range refused {
		require.NotEmpty(t, path,
			"an empty path is refused, which makes every archive entry live under a refusal")
		require.Falsef(t, strings.HasSuffix(path, string(filepath.Separator)),
			"%s has a trailing separator, so a prefix test against it misses the directory itself", path)
	}

	pathless := 0
	for _, c := range domain.SupportInventory {
		if c.Class == domain.SupportNever && c.Sources == nil {
			pathless++
		}
	}
	require.NotZero(t, pathless,
		"every refusal names a path, so this test no longer covers the case it was written for")
}
