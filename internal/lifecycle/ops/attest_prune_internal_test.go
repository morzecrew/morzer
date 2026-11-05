package ops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/target"
	"github.com/morzecrew/morzer/internal/adapters/target/localdir"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// Two operations' prunes can overlap, so one finds a statement the other has
// removed, which is not an error (RFC 0049 decision 5).
func TestRemovingAStatementAlreadyGoneIsNotAnError(t *testing.T) {
	require.NoError(t, removeStatement(filepath.Join(t.TempDir(), "op_1.json")))
}

func statementsIn(t *testing.T, d *Deps, n int) {
	t.Helper()
	require.NoError(t, os.MkdirAll(d.Paths.AttestationsDir(), 0o755))
	for i := range n {
		require.NoError(t, os.WriteFile(
			filepath.Join(d.Paths.AttestationsDir(), fmt.Sprintf("op_%d.json", i)), []byte("{}"), 0o644))
	}
}

// A count that reached here negative removes nothing rather than panic after
// the operation has run.
func TestANegativeCountPrunesNothing(t *testing.T) {
	d := &Deps{Paths: domain.PathsUnder(t.TempDir(), "demo")}
	statementsIn(t, d, 3)
	inst := domain.Installation{Policy: domain.Policy{RetainAttestations: -5}}
	assert.NotPanics(t, func() { pruneStatements(context.Background(), d, inst) })
	left, err := localStatements(d.Paths.AttestationsDir())
	require.NoError(t, err)
	assert.Len(t, left, 3)
}

// A build with targets and no object store cannot tell what a target holds,
// so it keeps everything, as `attestationTargets` already pushes nothing.
func TestNoObjectStoreKeepsEveryStatement(t *testing.T) {
	registry, err := target.NewRegistry(localdir.New())
	require.NoError(t, err)
	d := &Deps{Paths: domain.PathsUnder(t.TempDir(), "demo"), Targets: registry}
	statementsIn(t, d, 3)
	inst := domain.Installation{
		Policy: domain.Policy{RetainAttestations: 1},
		Backup: domain.BackupConfig{Targets: []domain.BackupTargetConfig{{URL: "file://" + t.TempDir()}}},
	}
	assert.NotPanics(t, func() { pruneStatements(context.Background(), d, inst) })
	left, err := localStatements(d.Paths.AttestationsDir())
	require.NoError(t, err)
	assert.Len(t, left, 3)
}

// neverFreeLocker is held by somebody else for as long as anyone waits.
type neverFreeLocker struct{ ports.Locker }

func (neverFreeLocker) Acquire(ctx context.Context, name string, _ ports.LockOptions) (func() error, error) {
	<-ctx.Done()
	return nil, domain.Interrupted("gave up waiting for the %s lock", name)
}

// The wait for the attestation lock is bounded, so a holder stuck on a target
// that does not answer cannot hold an operation's end open.
func TestTheAttestationLockWaitIsBounded(t *testing.T) {
	defer func(was time.Duration) { attestationLockWait = was }(attestationLockWait)
	attestationLockWait = 10 * time.Millisecond

	d := &Deps{Paths: domain.PathsUnder(t.TempDir(), "demo"), Locker: neverFreeLocker{}}
	statementsIn(t, d, 3)
	done := make(chan struct{})
	go func() {
		defer close(done)
		pruneStatements(context.Background(), d, domain.Installation{Policy: domain.Policy{RetainAttestations: 1}})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the prune waited for the attestation lock without bound")
	}
	left, err := localStatements(d.Paths.AttestationsDir())
	require.NoError(t, err)
	assert.Len(t, left, 3, "the prune ran without the lock")
}
