package state

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// TestASchemaThreeInstallationStillLoads is the guard for the 3 -> 4 arm.
//
// migrateInstallation is a forward-only loop whose default returns "no
// migration path". Raising InstallationSchemaVersion without adding `case 3:`
// therefore fails *every* installation on disk -- one missing line, the widest
// possible blast radius, and nothing else in the suite notices.
func TestASchemaThreeInstallationStillLoads(t *testing.T) {
	root := t.TempDir()
	paths := domain.PathsUnder(root, "demo")
	store := New(paths)

	if err := os.MkdirAll(filepath.Dir(paths.InstallationState()), 0o750); err != nil {
		t.Fatal(err)
	}

	// Written by hand at schema 3, the way a manager from before
	// notification existed left it -- with a backup target, because that is
	// what schema 3 was bumped for and it must survive.
	legacy := map[string]any{
		"schema_version": 3,
		"installation": map[string]any{
			"schema_version": 3,
			"id":             "inst_three",
			"product":        "demo",
			"created_at":     "2026-01-01T00:00:00Z",
			"backup": map[string]any{
				"targets": []map[string]any{{"url": "file:///media/backup"}},
			},
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
		t.Fatalf("a schema-3 installation no longer loads: %v", err)
	}
	if inst.SchemaVersion != domain.InstallationSchemaVersion {
		t.Errorf("schema_version = %d, want %d after migration",
			inst.SchemaVersion, domain.InstallationSchemaVersion)
	}
	if !inst.Backup.HasTargets() {
		t.Error("the migration lost the backup target schema 3 was bumped for")
	}
	if inst.Notify.HasTargets() {
		t.Error("an installation written before notification existed acquired a target")
	}
}

// TestASchemaFiveInstallationStillLoadsAndMintsNothing is the guard for the
// 5 -> 6 arm, and for the decision behind it.
//
// Two claims, and the second is the one worth a test. The first is the usual
// one: without `case 5:` the forward-only loop's default fails every
// installation on disk. The second is RFC 0028 decision 9 -- the migration
// bumps the number and mints **nothing**, so a machine that reaches schema 6
// this way has an empty signing block and no salt.
//
// That is not a gap being tolerated, it is the design: minting needs a CSPRNG,
// a directory and a 0400 file, and doing it here would make *loading state* a
// thing that writes to disk. A future edit that "helpfully" generates a key in
// the migration fails here, which is the point.
func TestASchemaFiveInstallationStillLoadsAndMintsNothing(t *testing.T) {
	root := t.TempDir()
	paths := domain.PathsUnder(root, "demo")
	store := New(paths)

	if err := os.MkdirAll(filepath.Dir(paths.InstallationState()), 0o750); err != nil {
		t.Fatal(err)
	}

	// Written by hand at schema 5, the way a manager from before signing
	// existed left it -- with a mode, because that is what schema 5 was
	// bumped for and it must survive.
	legacy := map[string]any{
		"schema_version": 5,
		"installation": map[string]any{
			"schema_version": 5,
			"id":             "inst_five",
			"product":        "demo",
			"created_at":     "2026-01-01T00:00:00Z",
			"mode":           "dev",
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
		t.Fatalf("a schema-5 installation no longer loads: %v", err)
	}
	if inst.SchemaVersion != domain.InstallationSchemaVersion {
		t.Errorf("schema_version = %d, want %d after migration",
			inst.SchemaVersion, domain.InstallationSchemaVersion)
	}
	if inst.Mode != domain.ModeDev {
		t.Error("the migration lost the mode schema 5 was bumped for")
	}

	if inst.Signing.HasKey() {
		t.Errorf("the migration minted a signing key: %q", inst.Signing.PublicKey)
	}
	if len(inst.Signing.PreviousKeys) != 0 {
		t.Error("the migration invented a signing history")
	}
	if inst.AttestationSalt != "" {
		t.Errorf("the migration minted an attestation salt: %q", inst.AttestationSalt)
	}

	// And the migrated machine is usable: loading it again must not fail
	// validation on the empty block it was just given.
	if err := inst.Validate(); err != nil {
		t.Errorf("a migrated installation does not validate: %v", err)
	}
}

// Every migratable schema reaches the current one, and does so by returning.
//
// This replaces a test that started at schema 1, which falls straight to the
// switch default and returns an error before the loop body runs twice -- so it
// never reached the guard it claimed to cover, and a case that stopped
// advancing would have hung real installation loads while it stayed green. Its
// own comment said it took the default path, which is the tell.
//
// Starting from each supported schema is what actually exercises the loop. A
// case that fails to raise the version cannot finish, so the timeout is the
// assertion: the failure mode being guarded against is a hang, and a hang is
// not observable by comparing return values.
func TestEverySupportedSchemaMigratesForwardAndTerminates(t *testing.T) {
	for from := 2; from < domain.InstallationSchemaVersion; from++ {
		t.Run(fmt.Sprintf("schema %d", from), func(t *testing.T) {
			done := make(chan domain.Installation, 1)
			errs := make(chan error, 1)
			go func() {
				got, err := migrateInstallation(domain.Installation{SchemaVersion: from})
				done <- got
				errs <- err
			}()

			select {
			case got := <-done:
				require.NoError(t, <-errs)
				assert.Equal(t, domain.InstallationSchemaVersion, got.SchemaVersion,
					"migration from %d stopped short", from)
			case <-time.After(10 * time.Second):
				t.Fatalf("migrating from schema %d never returned: a case raised no "+
					"version, and every load of installation state goes through here", from)
			}
		})
	}
}

// A schema with no path forward refuses by name rather than spinning.
func TestAnUnmigratableSchemaRefuses(t *testing.T) {
	_, err := migrateInstallation(domain.Installation{SchemaVersion: 1})

	require.Error(t, err, "schema 1 has no migration path and must say so")
	assert.Contains(t, err.Error(), "no migration path")
}
