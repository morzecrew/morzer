package ops

import (
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
)

// A bundle URL can carry a password, and the operation's flags go into the
// journal and into every notification about it.
func TestAnUpdateRecordsItsReferenceWithoutThePassword(t *testing.T) {
	flags := updateFlags(domain.Installation{}, UpdateOptions{
		Ref: "https://deploy:s3cret-token@bundles.example/demo-1.3.0.tar.zst",
	})
	if strings.Contains(flags["ref"], "s3cret-token") {
		t.Errorf("the journalled reference carries the password: %q", flags["ref"])
	}
	if !strings.Contains(flags["ref"], "bundles.example/demo-1.3.0.tar.zst") {
		t.Errorf("the reference itself was lost: %q", flags["ref"])
	}
}
