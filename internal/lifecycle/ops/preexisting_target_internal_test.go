package ops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/engine"
)

// No spelling of a target climbs out of the copies' directory, and a path is
// owned whatever its spelling.
func TestAKeptCopyStaysUnderItsDirectory(t *testing.T) {
	d := &Deps{Paths: domain.PathsUnder(t.TempDir(), "demo")}
	st := &engine.State{OpID: "op_1"}
	require.NoError(t, d.keepReplaced(st, "/../../escape/demo.conf", configBackup{content: []byte("x")}))
	assert.FileExists(t, filepath.Join(d.replacedDir("op_1"), "escape", "demo.conf"))

	owned := ownedTargets{declared: map[string]bool{"/etc/b.conf": true}}
	assert.True(t, owned.owns("/etc/a/../b.conf"))
}

// A resume is a new operation: the copies its interrupted run kept are
// carried into it, so its summary still says where they are.
func TestAResumeCarriesTheCopiesItsRunKept(t *testing.T) {
	prior := &domain.OperationRecord{Flags: map[string]string{
		replacedFlag + "/etc/x.conf": "/var/lib/demo/replaced/op_0/etc/x.conf mode 0644",
		"skip_backup":                "true",
	}}
	flags := carryReplaced(map[string]string{"ref": "b"}, prior)
	assert.NotContains(t, flags, "skip_backup")
	assert.NotPanics(t, func() { carryReplaced(nil, prior) }, "a plain apply has no flags of its own")
	assert.Contains(t, replacedNote(flags), "/var/lib/demo/replaced/op_0/etc/x.conf")
}

// A directory of copies rollback cannot list is said, not skipped.
func TestRollbackSaysWhenItCannotListTheCopies(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a directory whatever its mode")
	}
	d := &Deps{Paths: domain.PathsUnder(t.TempDir(), "demo")}
	locked := filepath.Join(d.replacedDir("op_1"), "etc")
	require.NoError(t, os.MkdirAll(locked, 0o700))
	require.NoError(t, os.Chmod(locked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	note := d.keptCopiesNote(domain.Release{})
	assert.True(t, strings.Contains(note, "could be listed"), note)
}

// Rollback compares the release's targets cleaned, as the copies are, so a
// target it renders is not reported as one it leaves.
func TestRollbackComparesTargetsCleaned(t *testing.T) {
	d := &Deps{Paths: domain.PathsUnder(t.TempDir(), "demo")}
	st := &engine.State{OpID: "op_1"}
	require.NoError(t, d.keepReplaced(st, "/etc/b.conf", configBackup{content: []byte("x")}))
	rel := domain.Release{Manifest: domain.Manifest{Configuration: []domain.ConfigurationFile{{Target: "/etc/a/../b.conf"}}}}
	assert.Empty(t, d.keptCopiesNote(rel))
}
