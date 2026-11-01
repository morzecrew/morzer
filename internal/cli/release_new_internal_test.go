package cli

import (
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
)

// TestOnlyTheScaffoldsOwnTODOIsAPlaceholder: `build` refuses the description
// `release new` writes, and not a real one that happens to start with the
// letters.
func TestOnlyTheScaffoldsOwnTODOIsAPlaceholder(t *testing.T) {
	for desc, want := range map[string]bool{
		"TODO -- one line, shown by `release show`": true,
		"TODOist, a task list for teams":            false,
		"Tracks TODO items":                         false,
	} {
		var m domain.Manifest
		m.Metadata.Description = desc
		if got := len(scaffoldPlaceholders(m)) > 0; got != want {
			t.Errorf("description %q: placeholder = %v, want %v", desc, got, want)
		}
	}
}
