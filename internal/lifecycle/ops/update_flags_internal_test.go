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

// A value given to an update is journalled as a digest per parameter, never as
// itself, and one value given to two parameters does not read as the same.
func TestAnUpdateJournalsADigestOfEachValue(t *testing.T) {
	flags := updateFlags(domain.Installation{}, UpdateOptions{
		Parameters: map[string]string{"admin_email": "ops@example.test", "billing_email": "ops@example.test"},
	})
	if strings.Contains(flags["set_digest"], "ops@example.test") {
		t.Fatalf("the journal quotes a value: %q", flags["set_digest"])
	}
	digests := strings.Split(flags["set_digest"], ",")
	if len(digests) != 2 || !strings.HasPrefix(digests[0], "admin_email=") || !strings.HasPrefix(digests[1], "billing_email=") {
		t.Fatalf("not one digest per parameter, by name: %q", flags["set_digest"])
	}
	_, first, _ := strings.Cut(digests[0], "=")
	_, second, _ := strings.Cut(digests[1], "=")
	if first == second {
		t.Errorf("one value given to two parameters reads as the same: %q", flags["set_digest"])
	}
}
