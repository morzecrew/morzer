package clitest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/test/clitest"
)

const (
	appImage     = "registry.example/demo/app@sha256:0000000000000000000000000000000000000000000000000000000000000001"
	appImageNext = "registry.example/demo/app@sha256:0000000000000000000000000000000000000000000000000000000000000003"
)

// A plan for an update says what the update risks, not only what it runs: the
// rollback it leaves possible, the images it moves and the vendor's notes.
func TestAPlannedUpdateShowsWhatItRisks(t *testing.T) {
	r := clitest.NewInstalled(t)

	irreversible := r.BundleAt("1.3.0")
	rewrite(t, filepath.Join(irreversible, "manifest.yaml"), "rollback_safe: true", "rollback_safe: false")
	rewrite(t, filepath.Join(irreversible, "manifest.yaml"), appImage, appImageNext)
	r.Run("update", irreversible, "--dry-run").ExitCode(0).OutputContains(
		"would update demo from 1.2.0 to 1.3.0",
		"rollback: once 1.3.0 is installed, going back to 1.2.0 needs a restore: "+
			"the installed release declares rollback_safe: false",
		"images:\n  app  "+appImage+" -> "+appImageNext+"\n",
		"The release notes a bundle ships",
	).NoOutputContains("  db  ")
	r.Run("update", irreversible, "--dry-run", "--json").ExitCode(0).
		FieldEquals("data.preview.rollback.restore_required", true).
		FieldEquals("data.preview.rollback.to", "1.2.0").
		FieldLen("data.preview.image_changes", 1).
		FieldEquals("data.preview.image_changes.0.from", appImage)

	// An image the release drops, and one it adds.
	renamed := r.BundleAt("1.4.0")
	rewrite(t, filepath.Join(renamed, "manifest.yaml"), "  db: registry.example/demo/db@", "  database: registry.example/demo/db@")
	r.Run("update", renamed, "--dry-run").ExitCode(0).OutputContains(
		"  database  added: registry.example/demo/db@",
		"  db  removed: registry.example/demo/db@").NoOutputContains("  app  ")

	// Migrations that leave a schema the installed release cannot read.
	ahead := r.BundleAt("1.3.1")
	rewrite(t, filepath.Join(ahead, "manifest.yaml"), "rollback_safe: true", "rollback_safe: true\n  database_schema_produces: 13")
	r.Run("update", ahead, "--dry-run").ExitCode(0).
		OutputContains("going back to 1.2.0 needs a restore: database schema is at 13 but the previous release supports at most 12")

	// And ones it can.
	compatible := r.BundleAt("1.2.1")
	rewrite(t, filepath.Join(compatible, "manifest.yaml"), "rollback_safe: true", "rollback_safe: true\n  database_schema_produces: 12")
	r.Run("update", compatible, "--dry-run").ExitCode(0).
		OutputContains("`morzer rollback` returns to 1.2.0 without a restore, as far as the releases declare",
			"images: unchanged").
		NoOutputContains("needs a restore")

	// A release that declares no schema is not said to keep it readable.
	r.Run("update", r.Bundle, "--dry-run").ExitCode(0).
		OutputContains("1.2.0 declares rollback_safe: true; the database schema is not checked").
		NoOutputContains("without a restore")
}

// An update that drops a parameter the installed release requires leaves a
// rollback the releases' declarations permit and preflight refuses: the update
// retires the value, and `config set` takes no name the new release does not
// declare. The preview says so rather than promising the way back.
func TestAPlannedUpdateSaysWhichDroppedParameterBlocksARollback(t *testing.T) {
	r := clitest.New(t)
	const declared = "  admin_email:\n    type: string\n    required: true\n    services: [app]\n"
	rewrite(t, filepath.Join(r.Bundle, "manifest.yaml"), "parameters:\n", "parameters:\n"+declared)
	r.Run("init", "--release", r.Bundle, "--profile", "embedded", "--domain", "demo.example",
		"--no-recovery-recipient", "--install-units=false", "--set", "admin_email=ops@demo.example").ExitCode(0)

	next := r.BundleAt("1.2.1")
	rewrite(t, filepath.Join(next, "manifest.yaml"), declared, "")
	rewrite(t, filepath.Join(next, "manifest.yaml"), "rollback_safe: true", "rollback_safe: true\n  database_schema_produces: 12")
	r.Run("update", next, "--dry-run").ExitCode(0).
		OutputContains("rollback: once 1.2.1 is installed, `morzer rollback` cannot return to 1.2.0: " +
			"the update drops admin_email, which 1.2.0 requires and 1.2.1 does not declare").
		NoOutputContains("without a restore")
	r.Run("update", next, "--dry-run", "--json").ExitCode(0).
		FieldLen("data.preview.rollback.dropped_parameters", 1).
		FieldEquals("data.preview.rollback.dropped_parameters.0", "admin_email")
}

func rewrite(t *testing.T, path, old, replacement string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), old) {
		t.Fatalf("%s does not contain %q", path, old)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), old, replacement, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
}
