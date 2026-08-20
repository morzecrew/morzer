package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/morzecrew/morzer/internal/domain"
)

// What a source tree carries that a release does not.
//
// The measured leak this closes: a bundle built inside a git repository had 42
// of its 55 SHA256SUMS entries under `.git/`, `.git/config` included, and the
// archive shipped all of them.
func TestWhatIsExcludedFromABundle(t *testing.T) {
	for _, rel := range []string{
		".git", ".git/config", ".git/objects/ab/cdef", "sub/.git/config",
		".hg/store", ".svn/entries", ".bzr/branch",
		".DS_Store", "compose/.DS_Store", "Thumbs.db",
	} {
		assert.True(t, domain.IsExcludedFromBundle(rel), "%q is source-tree litter", rel)
	}

	for _, rel := range []string{
		"manifest.yaml", "VERSION", "compose/compose.yaml", "hooks/backup",
		"templates/app.yaml", "images/index.json", "SHA256SUMS",
		// Named *like* the excluded set without being it. A rule that
		// matched on a prefix would take these, and a release missing a
		// declared file is the expensive direction.
		".gitignore", ".gitattributes", "gitconfig", "compose/.gitkeep",
		"docs/thumbs.db.md", "not.DS_Store.yaml",
	} {
		assert.False(t, domain.IsExcludedFromBundle(rel), "%q belongs in the release", rel)
	}

	// The empty path is the walk's own root, and excluding it would exclude
	// everything.
	assert.False(t, domain.IsExcludedFromBundle(""))
}
