package state

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
)

// TestASchemaTwoInstallationStillLoads. The bump to 3 must not lock an operator
// out of their own deployment: a new manager reads an old state, an old manager
// refuses a new one. Only the second half is a refusal.
func TestASchemaTwoInstallationStillLoads(t *testing.T) {
	root := t.TempDir()
	paths := domain.PathsUnder(root, "demo")
	store := New(paths)

	if err := os.MkdirAll(filepath.Dir(paths.InstallationState()), 0o750); err != nil {
		t.Fatal(err)
	}

	// Written by hand at schema 2, the way a manager from before targets
	// existed left it.
	legacy := map[string]any{
		"schema_version": 2,
		"installation": map[string]any{
			"schema_version": 2,
			"id":             "inst_legacy",
			"product":        "demo",
			"created_at":     "2026-01-01T00:00:00Z",
			"parameters":     map[string]string{"http_port": "8080"},
		},
	}
	data, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.InstallationState(), data, 0o640); err != nil {
		t.Fatal(err)
	}

	inst, err := store.LoadInstallation(context.Background())
	if err != nil {
		t.Fatalf("a schema-2 installation no longer loads: %v", err)
	}
	if inst.SchemaVersion != domain.InstallationSchemaVersion {
		t.Errorf("schema_version = %d, want %d after migration",
			inst.SchemaVersion, domain.InstallationSchemaVersion)
	}
	if inst.ID != "inst_legacy" || inst.Parameters["http_port"] != "8080" {
		t.Error("the migration lost the operator's own state")
	}
	if inst.Backup.HasTargets() {
		t.Error("an installation written before targets existed acquired one")
	}
}
