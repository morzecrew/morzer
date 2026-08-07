package suite

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/backup/hookbackup"
	"github.com/morzecrew/morzer/internal/adapters/hooks"
	"github.com/morzecrew/morzer/internal/adapters/secrets/sopsage"
	"github.com/morzecrew/morzer/internal/domain"
	infraexec "github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
	"github.com/morzecrew/morzer/test/contract"
	"github.com/morzecrew/morzer/test/fakes"
)

// The BackupEngine fake mirrors hookbackup's on-disk layout by hand -- a
// manifest, one file per component, a checksum for each -- and until this suite
// nothing held the copy to the original. That is the fake most worth checking:
// every test that asserts a corrupt backup is refused was passing because the
// fake refuses it.

// steppingClock advances a second per call, so backups taken in the same
// wall-clock second still order. The real ID generator has second granularity.
func steppingClock() func() time.Time {
	var n atomic.Int64
	base := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return base.Add(time.Duration(n.Add(1)) * time.Second) }
}

func TestBackupEngineContract_Fake(t *testing.T) {
	contract.RunBackupEngineSuite(t, func(t *testing.T) contract.BackupEngineHarness {
		b := fakes.NewBackup()
		// Root makes the fake write the layout it claims to mirror.
		// Without it there is nothing for the on-disk cases to check,
		// and it is the layout that drifts.
		b.Root = filepath.Join(t.TempDir(), "backups")
		b.Now = steppingClock()

		return contract.BackupEngineHarness{
			Engine: b,
			Dir:    func(ref ports.BackupRef) string { return b.Dir(ref.ID) },
		}
	})
}

// TestBackupEngineContract_Hookbackup runs the same suite against the real
// engine, with the example bundle's own backup hook. Hermetic: the hook is a
// shell script, so this needs no Docker.
func TestBackupEngineContract_Hookbackup(t *testing.T) {
	contract.RunBackupEngineSuite(t, func(t *testing.T) contract.BackupEngineHarness {
		root := t.TempDir()
		paths := domain.PathsUnder(root, "demo")

		for _, dir := range paths.ManagedDirs() {
			require.NoError(t, os.MkdirAll(dir.Path, os.FileMode(dir.Mode)))
		}

		releaseRoot := filepath.Join(paths.ReleasesDir(), "1.2.0")
		copyBundle(t, testBundlePath(t), releaseRoot)
		retargetManifest(t, releaseRoot, root)

		rel, err := release.Load(releaseRoot)
		require.NoError(t, err)

		// A real recipient: the components are genuinely encrypted, which
		// is what lets the suite check the claim rather than take it.
		public, err := sopsage.GenerateIdentity(paths.AgeIdentityFile())
		require.NoError(t, err)

		return contract.BackupEngineHarness{
			Engine: hookbackup.New(hookbackup.Config{
				Hooks:          hooks.NewRunner(infraexec.New()),
				Release:        rel,
				Installation:   domain.Installation{ID: "inst_contract", Product: "demo"},
				Paths:          paths,
				ManagerVersion: "0.0.0-test",
				Now:            steppingClock(),
				Recipients: func(context.Context) ([]string, error) {
					return []string{public}, nil
				},
			}),
			Dir:      func(ref ports.BackupRef) string { return filepath.Join(paths.BackupsDir(), ref.ID) },
			Encrypts: true,
		}
	})
}
