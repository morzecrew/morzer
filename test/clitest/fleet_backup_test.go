package clitest_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/morzecrew/morzer/test/clitest"
)

// The row's `last_at` read through the command as `main` wires it. The suite
// builds the row with a backup engine in hand; `fleet publish` and `sync now`
// have to attach one, or every row a machine publishes says it has no backups.
func TestAPublishedRowNamesTheNewestBackup(t *testing.T) {
	r := clitest.NewInstalled(t)
	target := "file://" + r.Path("fleet-target")
	r.Run("backup", "target", "add", target).ExitCode(0)
	r.Run("backup", "--no-push").ExitCode(0)
	taken, _ := r.Run("--json", "backup", "list").Field("data.0.at").(string)
	if taken == "" {
		t.Fatal("`backup list --json` names no backup")
	}

	r.Run("--json", "fleet", "publish", "--dry-run", "--target", target).ExitCode(0).
		FieldEquals("data.row.backup.last_at", taken)
}

// A sync cycle whose outcome is news publishes the row too, and that row is
// built the same way.
func TestARowASyncPublishesNamesTheNewestBackup(t *testing.T) {
	r := clitest.New(t)
	authority, _ := r.Run("--json", "state", "key", "generate", r.Path("authority.key")).
		ExitCode(0).Field("data.public_key").(string)
	if authority == "" {
		t.Fatal("`state key generate --json` names no public key")
	}
	r.Run("init",
		"--release", r.Bundle,
		"--profile", "embedded",
		"--no-recovery-recipient",
		"--install-units=false",
		"--sync-target", "file://"+r.Path("desired"),
		"--sync-authority", authority,
	).ExitCode(0)
	backups := r.Path("fleet-target")
	r.Run("backup", "target", "add", "file://"+backups).ExitCode(0)
	r.Run("backup", "--no-push").ExitCode(0)
	taken, _ := r.Run("--json", "backup", "list").Field("data.0.at").(string)

	r.Run("config", "set", "sync.enabled=true").ExitCode(0)
	r.Run("sync", "now").ExitCode(0)
	rows, err := filepath.Glob(filepath.Join(backups, "fleet", "demo", "*", "status.json"))
	if err != nil || len(rows) != 1 {
		t.Fatalf("the cycle published %d rows: %v", len(rows), err)
	}
	body, err := os.ReadFile(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	var row struct {
		Backup struct {
			LastAt string `json:"last_at"`
		} `json:"backup"`
	}
	if err := json.Unmarshal(body, &row); err != nil {
		t.Fatal(err)
	}
	if row.Backup.LastAt != taken {
		t.Errorf("the row a sync published says last_at %q, the newest backup was taken at %q", row.Backup.LastAt, taken)
	}
}
