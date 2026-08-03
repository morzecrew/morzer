// Package contract holds the shared test suites every port implementation
// must pass.
//
// The point is that adding an adapter is a bounded amount of work with a
// bounded amount of risk: implement the interface, pass this suite, register
// the name. A suite that only the fake passes would be worthless, so every
// assertion here is about behaviour the lifecycle layer actually relies on --
// idempotence, permission bits, refusal to lock oneself out.
package contract

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// SecretStoreFactory builds a store for one test, plus a cleanup function.
//
// Each test gets a fresh store: a suite whose cases depend on each other's
// leftovers cannot tell you which case actually broke.
type SecretStoreFactory func(t *testing.T) ports.SecretStore

// RunSecretStoreSuite runs every SecretStore contract test.
func RunSecretStoreSuite(t *testing.T, newStore SecretStoreFactory) {
	t.Helper()

	t.Run("empty store loads without error", func(t *testing.T) {
		store := newStore(t)
		set, err := store.Load(context.Background())
		require.NoError(t, err, "an uninitialised store must load as empty, not error")
		assert.Equal(t, 0, set.Len())
	})

	t.Run("set then load round-trips the value", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()

		require.NoError(t, store.Set(ctx, "db_password", domain.NewSecret("hunter2-and-then-some")))

		set, err := store.Load(ctx)
		require.NoError(t, err)

		got, ok := set.Get("db_password")
		require.True(t, ok, "the secret must be present after Set")
		assert.Equal(t, "hunter2-and-then-some", got.Reveal())
	})

	t.Run("set refuses an empty value", func(t *testing.T) {
		store := newStore(t)
		err := store.Set(context.Background(), "empty", domain.NewSecret(""))
		require.Error(t, err, "an empty value must be refused; removal is a separate operation")
	})

	t.Run("set overwrites an existing value", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()

		require.NoError(t, store.Set(ctx, "key", domain.NewSecret("first-value-here")))
		require.NoError(t, store.Set(ctx, "key", domain.NewSecret("second-value-here")))

		set, err := store.Load(ctx)
		require.NoError(t, err)
		got, _ := set.Get("key")
		assert.Equal(t, "second-value-here", got.Reveal())
	})

	t.Run("remove is idempotent", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()

		require.NoError(t, store.Set(ctx, "gone", domain.NewSecret("temporary-value")))
		require.NoError(t, store.Remove(ctx, "gone"))

		// Removing an absent secret must not error: the postcondition
		// the caller wanted already holds.
		require.NoError(t, store.Remove(ctx, "gone"),
			"removing an absent secret must be a no-op, not an error")

		set, err := store.Load(ctx)
		require.NoError(t, err)
		assert.False(t, set.Has("gone"))
	})

	t.Run("generate refuses to overwrite without permission", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		spec := ports.GenSpec{Generator: domain.Generator{Kind: domain.GeneratorPassword, Length: 24}}

		require.NoError(t, store.Generate(ctx, "api_key", spec))

		err := store.Generate(ctx, "api_key", spec)
		require.Error(t, err,
			"silently regenerating a live credential is how a product goes down at the next restart")

		spec.Overwrite = true
		require.NoError(t, store.Generate(ctx, "api_key", spec))
	})

	t.Run("generated values are distinct", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		spec := ports.GenSpec{Generator: domain.Generator{Kind: domain.GeneratorPassword, Length: 32}}

		require.NoError(t, store.Generate(ctx, "a", spec))
		require.NoError(t, store.Generate(ctx, "b", spec))

		set, err := store.Load(ctx)
		require.NoError(t, err)
		a, _ := set.Get("a")
		b, _ := set.Get("b")

		assert.NotEqual(t, a.Reveal(), b.Reveal(),
			"two generated secrets sharing a value would mean the generator is not random")
	})

	t.Run("rotate replaces an existing value and refuses a missing one", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		spec := ports.GenSpec{Generator: domain.Generator{Kind: domain.GeneratorPassword, Length: 24}}

		require.NoError(t, store.Generate(ctx, "rotating", spec))

		before, err := store.Load(ctx)
		require.NoError(t, err)
		oldValue, _ := before.Get("rotating")

		require.NoError(t, store.Rotate(ctx, "rotating", spec))

		after, err := store.Load(ctx)
		require.NoError(t, err)
		newValue, _ := after.Get("rotating")

		assert.NotEqual(t, oldValue.Reveal(), newValue.Reveal(), "rotation must change the value")

		require.Error(t, store.Rotate(ctx, "never-existed", spec),
			"rotating a secret that does not exist must fail rather than create one")
	})

	t.Run("render writes 0400 files inside a 0700 directory", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()

		require.NoError(t, store.Set(ctx, "db_password", domain.NewSecret("a-real-password")))
		require.NoError(t, store.Set(ctx, "session_key", domain.NewSecret("a-real-session-key")))

		schema := domain.SecretSchema{Secrets: []domain.SecretDeclaration{
			{Name: "db_password", Required: true},
			{Name: "session_key", Required: true},
		}}

		targetDir := filepath.Join(t.TempDir(), "secrets")
		files, err := store.Render(ctx, targetDir, schema)
		require.NoError(t, err)
		require.Len(t, files, 2)

		// The permissions are the security property, so they are the
		// assertion. Everything else about rendering is convenience.
		dirInfo, err := os.Stat(targetDir)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm(),
			"the render directory must be 0700: anything wider exposes every secret in it")

		for _, f := range files {
			info, err := os.Stat(f.Path)
			require.NoError(t, err, "rendered file %s must exist", f.Path)
			assert.Equal(t, os.FileMode(0o400), info.Mode().Perm(),
				"rendered secret %s must be 0400", f.Name)
		}

		content, err := os.ReadFile(filepath.Join(targetDir, "db_password"))
		require.NoError(t, err)
		assert.Equal(t, "a-real-password", string(content))
	})

	t.Run("render prunes files no declaration backs", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		targetDir := filepath.Join(t.TempDir(), "secrets")

		require.NoError(t, store.Set(ctx, "kept", domain.NewSecret("still-needed-value")))
		require.NoError(t, store.Set(ctx, "dropped", domain.NewSecret("no-longer-needed")))

		full := domain.SecretSchema{Secrets: []domain.SecretDeclaration{
			{Name: "kept", Required: true},
			{Name: "dropped", Required: true},
		}}
		_, err := store.Render(ctx, targetDir, full)
		require.NoError(t, err)
		require.FileExists(t, filepath.Join(targetDir, "dropped"))

		// The release drops a secret. Its rendered file must go with
		// it: a stale credential on disk with nobody responsible for
		// rotating it is a leak waiting to happen.
		reduced := domain.SecretSchema{Secrets: []domain.SecretDeclaration{
			{Name: "kept", Required: true},
		}}
		_, err = store.Render(ctx, targetDir, reduced)
		require.NoError(t, err)

		assert.NoFileExists(t, filepath.Join(targetDir, "dropped"),
			"a secret file with no backing declaration must be pruned")
		assert.FileExists(t, filepath.Join(targetDir, "kept"))
	})

	t.Run("render fails when a required secret is missing", func(t *testing.T) {
		store := newStore(t)
		schema := domain.SecretSchema{Secrets: []domain.SecretDeclaration{
			{Name: "absolutely_required", Required: true},
		}}

		_, err := store.Render(context.Background(), filepath.Join(t.TempDir(), "s"), schema)
		require.Error(t, err, "a missing required secret must fail before anything starts")
		assert.Equal(t, domain.ExitSecrets, domain.ExitCode(err),
			"the error must map to the secrets exit code")
	})

	t.Run("render skips optional secrets that are not set", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		require.NoError(t, store.Set(ctx, "present", domain.NewSecret("a-present-value")))

		schema := domain.SecretSchema{Secrets: []domain.SecretDeclaration{
			{Name: "present", Required: true},
			{Name: "absent", Required: false},
		}}

		targetDir := filepath.Join(t.TempDir(), "secrets")
		files, err := store.Render(ctx, targetDir, schema)
		require.NoError(t, err)
		assert.Len(t, files, 1, "an unset optional secret must be skipped, not rendered empty")
	})

	t.Run("metadata never carries values", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()

		const value = "a-very-distinctive-secret-value"
		require.NoError(t, store.Set(ctx, "watched", domain.NewSecret(value)))

		metadata, err := store.Metadata(ctx)
		require.NoError(t, err)
		require.NotEmpty(t, metadata)

		for _, m := range metadata {
			assert.NotContains(t, m.Fingerprint, value,
				"a fingerprint that contains the value is not a fingerprint")
			assert.NotEmpty(t, m.Fingerprint, "every secret needs a fingerprint to be comparable")
		}
	})

	t.Run("refuses to remove the machine recipient", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()

		require.NoError(t, store.Set(ctx, "anything", domain.NewSecret("a-value-to-encrypt")))

		recipients, err := store.Recipients(ctx)
		require.NoError(t, err)
		require.NotEmpty(t, recipients, "an initialised store must have at least one recipient")

		var machine ports.Recipient
		for _, r := range recipients {
			if r.Kind == ports.RecipientMachine {
				machine = r
			}
		}
		require.NotEmpty(t, machine.PublicKey, "the machine's own key must be identifiable")

		err = store.RemoveRecipient(ctx, machine)
		require.Error(t, err,
			"removing the machine's own key would lock the manager out of its own state")
	})

	t.Run("refuses to remove the last recipient", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		require.NoError(t, store.Set(ctx, "anything", domain.NewSecret("a-value-to-encrypt")))

		recipients, err := store.Recipients(ctx)
		require.NoError(t, err)

		// Remove everything removable; the machine key and the last one
		// must both survive.
		for _, r := range recipients {
			_ = store.RemoveRecipient(ctx, r)
		}

		remaining, err := store.Recipients(ctx)
		require.NoError(t, err)
		assert.NotEmpty(t, remaining,
			"the secret state must never become undecryptable through recipient removal")
	})

	t.Run("adding a recipient twice is a no-op", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		require.NoError(t, store.Set(ctx, "anything", domain.NewSecret("a-value-to-encrypt")))

		recipient := ports.Recipient{
			PublicKey: "age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p",
			Kind:      ports.RecipientRecovery,
		}

		require.NoError(t, store.AddRecipient(ctx, recipient))
		before, err := store.Recipients(ctx)
		require.NoError(t, err)

		require.NoError(t, store.AddRecipient(ctx, recipient),
			"re-adding an existing recipient must be idempotent")

		after, err := store.Recipients(ctx)
		require.NoError(t, err)
		assert.Len(t, after, len(before), "the recipient must not be duplicated")
	})

	// ReencryptFor is the method recovery is built on. These cases assert
	// the recipient *set* it produces; that the named keys can actually
	// decrypt afterwards is a question about cryptography, which a fake
	// cannot answer, and is asserted end to end against real age keys by
	// TestRecoveryRebuildsAMachineFromAnOfflineKey in test/suite.

	t.Run("reencrypt replaces the recipient set exactly", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()

		require.NoError(t, store.Set(ctx, "kept", domain.NewSecret("a-value-to-encrypt")))

		machine, err := store.EnsureIdentity(ctx)
		require.NoError(t, err)

		// Two keys the store has never seen, so a set that merely
		// merged would be visibly larger than one that replaced.
		require.NoError(t, store.AddRecipient(ctx, ports.Recipient{
			PublicKey: "age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p",
			Kind:      ports.RecipientOperator,
		}))

		recovery := ports.Recipient{
			PublicKey: "age1d369794a45rmk3s5kt2s7wn99m2q2zxnxcltshmxt5ydvhafysaq6r63rm",
			Kind:      ports.RecipientRecovery,
			Comment:   "offline key",
		}
		require.NoError(t, store.ReencryptFor(ctx, []ports.Recipient{
			{PublicKey: machine, Kind: ports.RecipientMachine},
			recovery,
		}))

		after, err := store.Recipients(ctx)
		require.NoError(t, err)

		keys := make([]string, 0, len(after))
		for _, r := range after {
			keys = append(keys, r.PublicKey)
		}
		assert.ElementsMatch(t, []string{machine, recovery.PublicKey}, keys,
			"ReencryptFor replaces the set; a recipient absent from the list must lose access")

		// The state must still be readable by the machine, which is in
		// the new set. A re-encryption that lost the values would be a
		// far worse bug than one that kept the wrong recipients.
		set, err := store.Load(ctx)
		require.NoError(t, err)
		got, ok := set.Get("kept")
		require.True(t, ok, "re-encryption must preserve the values")
		assert.Equal(t, "a-value-to-encrypt", got.Reveal())
	})

	t.Run("reencrypt refuses an empty recipient set", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		require.NoError(t, store.Set(ctx, "anything", domain.NewSecret("a-value-to-encrypt")))

		err := store.ReencryptFor(ctx, nil)
		require.Error(t, err,
			"an empty recipient set would make the state permanently undecryptable")

		after, err := store.Recipients(ctx)
		require.NoError(t, err)
		assert.NotEmpty(t, after, "a refused re-encryption must leave the recipients untouched")
	})

	t.Run("reencrypt refuses a malformed key before writing anything", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		require.NoError(t, store.Set(ctx, "anything", domain.NewSecret("a-value-to-encrypt")))

		before, err := store.Recipients(ctx)
		require.NoError(t, err)

		machine, err := store.EnsureIdentity(ctx)
		require.NoError(t, err)

		err = store.ReencryptFor(ctx, []ports.Recipient{
			{PublicKey: machine, Kind: ports.RecipientMachine},
			{PublicKey: "not-an-age-key", Kind: ports.RecipientOperator},
		})
		require.Error(t, err, "a malformed recipient must be caught before the state is rewritten")

		after, err := store.Recipients(ctx)
		require.NoError(t, err)
		assert.Len(t, after, len(before),
			"a validation failure must not partially apply the new recipient set")
	})

	t.Run("reencrypt may drop the machine key, unlike RemoveRecipient", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		require.NoError(t, store.Set(ctx, "anything", domain.NewSecret("a-value-to-encrypt")))

		machine, err := store.EnsureIdentity(ctx)
		require.NoError(t, err)

		// This is the asymmetry the method exists for: rebuilding a
		// machine means encrypting for keys that do not include the one
		// belonging to the host that is gone.
		recovery := ports.Recipient{
			PublicKey: "age1d369794a45rmk3s5kt2s7wn99m2q2zxnxcltshmxt5ydvhafysaq6r63rm",
			Kind:      ports.RecipientRecovery,
		}
		require.NoError(t, store.ReencryptFor(ctx, []ports.Recipient{recovery}),
			"recovery must be able to drop a machine key whose host no longer exists")

		after, err := store.Recipients(ctx)
		require.NoError(t, err)
		for _, r := range after {
			assert.NotEqual(t, machine, r.PublicKey,
				"the old machine key must be gone from the recipient set")
		}
	})
}
