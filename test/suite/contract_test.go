// Package suite runs the shared contract suites against every implementation.
//
// The fakes and the real adapters run the same tests. That is the point: a
// fake that passes tests the real adapter would fail is a fake that makes the
// integration tests lie.
package suite

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/secrets/sopsage"
	"github.com/morzecrew/morzer/internal/domain"
	infraexec "github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/infra/state"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/contract"
	"github.com/morzecrew/morzer/test/fakes"
)

func TestSecretStoreContract_Fake(t *testing.T) {
	contract.RunSecretStoreSuite(t, func(t *testing.T) ports.SecretStore {
		return fakes.NewSecretStore()
	})
}

// TestSecretStoreContract_SOPSAge runs the same suite against the real
// adapter. It needs the sops binary, so it skips rather than fails when sops
// is absent: a developer without it should still get a green run for
// everything else.
func TestSecretStoreContract_SOPSAge(t *testing.T) {
	if _, err := exec.LookPath("sops"); err != nil {
		t.Skip("sops is not installed; skipping the real SecretStore contract suite")
	}

	contract.RunSecretStoreSuite(t, func(t *testing.T) ports.SecretStore {
		dir := t.TempDir()
		identity := filepath.Join(dir, "age", "identity")

		_, err := sopsage.GenerateIdentity(identity)
		require.NoError(t, err)

		return sopsage.New(infraexec.New(), filepath.Join(dir, "secrets.sops.yaml"), identity)
	})
}

func TestStateStoreContract_Filesystem(t *testing.T) {
	contract.RunStateStoreSuite(t, func(t *testing.T) ports.StateStore {
		return state.New(domain.PathsUnder(t.TempDir(), "demo"))
	})
}

func TestRuntimeContract_Fake(t *testing.T) {
	contract.RunRuntimeSuite(t, func(t *testing.T) (ports.Runtime, ports.RuntimeConfig) {
		return fakes.NewRuntime(), ports.RuntimeConfig{Project: "demo"}
	})
}

// TestSecretRedactionIsStructural asserts that a secret cannot be printed by
// accident. The type is the first line of defence; the log handler is the
// second.
func TestSecretRedactionIsStructural(t *testing.T) {
	secret := domain.NewSecret("super-secret-value")

	require.Equal(t, domain.Redacted, secret.String())
	require.Equal(t, domain.Redacted, secret.GoString())
	require.Equal(t, domain.Redacted, secret.LogValue().String())

	json, err := secret.MarshalJSON()
	require.NoError(t, err)
	require.NotContains(t, string(json), "super-secret-value")

	require.Equal(t, "super-secret-value", secret.Reveal(),
		"Reveal is the one deliberate way out")

	_ = context.Background()
}
