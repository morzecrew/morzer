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

// A recipient written into the state and not into the sidecar that says what it
// is for -- a crash between the two writes -- is labelled when it is added
// again, rather than staying a key `doctor` cannot count as the recovery key.
func TestAddingARecipientAgainLabelsOneACrashLeftUnlabelled(t *testing.T) {
	if _, err := osexec.LookPath("sops"); err != nil {
		t.Skip("sops is not installed")
	}
	ctx := context.Background()
	dir := t.TempDir()
	identity := filepath.Join(dir, "age", "identity")
	_, err := sopsage.GenerateIdentity(identity)
	require.NoError(t, err)
	store := sopsage.New(infraexec.New(), filepath.Join(dir, "secrets.sops.yaml"), identity)
	require.NoError(t, store.Set(ctx, "seed", domain.NewSecret("seed-value")))

	// A directory where the sidecar goes fails its write after the state's.
	sidecar := filepath.Join(dir, "secrets.recipients.yaml")
	require.NoError(t, os.Mkdir(sidecar, 0o700))
	recovery := ports.Recipient{
		PublicKey: "age1d369794a45rmk3s5kt2s7wn99m2q2zxnxcltshmxt5ydvhafysaq6r63rm",
		Kind:      ports.RecipientRecovery,
	}
	require.Error(t, store.AddRecipient(ctx, recovery))
	require.NoError(t, os.Remove(sidecar))

	require.NoError(t, store.AddRecipient(ctx, recovery))
	recipients, err := store.Recipients(ctx)
	require.NoError(t, err)
	var kind ports.RecipientKind
	for _, r := range recipients {
		if r.PublicKey == recovery.PublicKey {
			kind = r.Kind
		}
	}
	assert.Equal(t, ports.RecipientRecovery, kind, "the key is still not labelled as the recovery key")
}
