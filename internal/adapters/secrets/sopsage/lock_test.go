package sopsage_test

import (
	"context"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/secrets/sopsage"
	"github.com/morzecrew/morzer/internal/domain"
	infraexec "github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/infra/lock"
	"github.com/morzecrew/morzer/internal/ports"
)

// A write that waited for another records when it ran, not when it was asked
// for: a recipient's added_at is when it could first decrypt the state.
func TestAReencryptThatWaitedRecordsWhenItRan(t *testing.T) {
	if _, err := osexec.LookPath("sops"); err != nil {
		t.Skip("sops is not installed")
	}
	ctx := context.Background()
	dir := t.TempDir()
	identity := filepath.Join(dir, "age", "identity")
	machine, err := sopsage.GenerateIdentity(identity)
	require.NoError(t, err)

	var mu sync.Mutex
	clock := time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)
	now := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return clock
	}
	locker := lock.New(filepath.Join(dir, "locks")).WithPollInterval(5 * time.Millisecond)
	store := sopsage.New(infraexec.New(), filepath.Join(dir, "secrets.sops.yaml"), identity,
		sopsage.WithClock(now), sopsage.WithLocker(locker))
	require.NoError(t, store.Set(ctx, "seed", domain.NewSecret("seed-value")))

	release, err := locker.Acquire(ctx, "secrets", ports.LockOptions{})
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		done <- store.ReencryptFor(ctx, []ports.Recipient{{PublicKey: machine, Kind: ports.RecipientMachine}})
	}()
	time.Sleep(100 * time.Millisecond) // the write is now waiting
	mu.Lock()
	clock = clock.Add(time.Hour)
	mu.Unlock()
	require.NoError(t, release())
	require.NoError(t, <-done)

	annotations, err := os.ReadFile(filepath.Join(dir, "secrets.recipients.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(annotations), "added_at: \"2026-10-29T09:00:00Z\"",
		"the recipient was recorded as added before the write that added it ran")
}
