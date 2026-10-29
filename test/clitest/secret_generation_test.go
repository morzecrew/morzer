package clitest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/morzecrew/morzer/test/clitest"
)

// A plan of an update names the secret the new release adds, and
// --generate-secrets=false reaches the step (RFC 0036).
func TestAPlannedUpdateNamesTheSecretTheReleaseAdds(t *testing.T) {
	r := clitest.NewInstalled(t)
	bundle := r.BundleAt("1.3.0")

	schema := filepath.Join(bundle, "secrets.schema.yaml")
	f, err := os.OpenFile(schema, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("\n  - name: webhook_key\n    required: true\n" +
		"    generator: {kind: hex, length: 32}\n    services: [app]\n")
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}

	r.Run("update", bundle, "--dry-run").ExitCode(0).
		OutputContains("would generate webhook_key")
	r.Run("update", bundle, "--dry-run", "--generate-secrets=false").ExitCode(0).
		OutputContains("turned off by --generate-secrets=false").
		NoOutputContains("would generate webhook_key")
}
