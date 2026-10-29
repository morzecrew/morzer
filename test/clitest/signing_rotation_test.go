package clitest_test

import (
	"testing"

	"github.com/morzecrew/morzer/test/clitest"
)

// `installation rotate-signing-key` through the binary: a plan that changes
// nothing, then a rotation that retires the key `init` minted.
func TestRotatingTheSigningKeyRetiresTheOneInitMinted(t *testing.T) {
	r := clitest.NewInstalled(t)

	before := r.Run("--json", "installation", "rotate-signing-key", "--dry-run").ExitCode(0)
	old, _ := before.Field("data.retired_key").(string)
	if old == "" {
		t.Fatalf("the plan names no key to retire:\n%s", before.Stdout)
	}
	before.FieldEquals("data.dry_run", true)

	done := r.Run("--json", "installation", "rotate-signing-key").ExitCode(0)
	done.FieldEquals("data.retired_key", old)
	current, _ := done.Field("data.current_key").(string)
	if current == "" || current == old {
		t.Fatalf("the rotation reports no new key, or the old one:\n%s", done.Stdout)
	}

	// A second rotation retires the key the first one minted.
	r.Run("--json", "installation", "rotate-signing-key").ExitCode(0).
		FieldEquals("data.retired_key", current)
}
