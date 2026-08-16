// Package suite runs the shared contract suites against every implementation.
//
// The fakes and the real adapters run the same tests. That is the point: a
// fake that passes tests the real adapter would fail is a fake that makes the
// integration tests lie.
package suite

import (
	"context"
	"io/fs"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/secrets/sopsage"
	"github.com/morzecrew/morzer/internal/adapters/source"
	"github.com/morzecrew/morzer/internal/adapters/source/local"
	"github.com/morzecrew/morzer/internal/adapters/target"
	"github.com/morzecrew/morzer/internal/adapters/target/localdir"
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

// The release-source suite runs against every shape a local bundle can arrive
// in. What it is really asserting is that the shape does not matter: an
// operator who records a digest from an unpacked bundle must be able to pin the
// archive of it, and vice versa.

func TestReleaseSourceContract_Directory(t *testing.T) {
	contract.RunReleaseSourceSuite(t, testBundlePath(t),
		func(t *testing.T, bundleDir string) (ports.ReleaseSource, ports.Ref) {
			return local.New(), ports.Ref{Scheme: "file", Location: bundleDir}
		})
}

func TestReleaseSourceContract_Archive(t *testing.T) {
	contract.RunReleaseSourceSuite(t, testBundlePath(t),
		func(t *testing.T, bundleDir string) (ports.ReleaseSource, ports.Ref) {
			archive := filepath.Join(t.TempDir(), "bundle.tar.zst")
			writeTarZst(t, bundleDir, archive)
			return local.New(), ports.Ref{Scheme: "file", Location: archive}
		})
}

// The registry is itself a source, so it runs the same suite. A dispatcher that
// passed its own tests but mangled a delegation would otherwise look correct.
func TestReleaseSourceContract_Registry(t *testing.T) {
	contract.RunReleaseSourceSuite(t, testBundlePath(t),
		func(t *testing.T, bundleDir string) (ports.ReleaseSource, ports.Ref) {
			registry, err := source.NewRegistry(local.New())
			require.NoError(t, err)
			return registry, ports.Ref{Scheme: "file", Location: bundleDir}
		})
}

func TestSourceRegistryRefusesAnUnbuiltScheme(t *testing.T) {
	registry, err := source.NewRegistry(local.New())
	require.NoError(t, err)

	_, err = registry.Resolve(context.Background(),
		ports.Ref{Scheme: "oci", Location: "registry.example/demo:1.0.0"})
	require.Error(t, err)

	// The refusal has to name what this build *can* do. An operator asking
	// for oci:// on a binary without it is asking for something reasonable,
	// and "no" alone leaves them nowhere.
	assert.Contains(t, err.Error(), "oci")
	assert.Contains(t, domain.AsError(err).Hint, "file",
		"the hint must list the schemes that are available")
}

func TestSourceRegistryRefusesDuplicateSchemes(t *testing.T) {
	// A wiring mistake with no sensible resolution: last-wins would make
	// behaviour depend on argument order, first-wins would silently ignore
	// an adapter someone deliberately added.
	_, err := source.NewRegistry(local.New(), local.New())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "file")
}

// The backup-target suite runs against the fake and against every shipped
// adapter. What it asserts is that the transport does not matter: a backup
// pushed anywhere comes back byte for byte, and a transfer interrupted anywhere
// leaves something nobody can restore rather than something they can.

func TestBackupTargetContract_Fake(t *testing.T) {
	contract.RunBackupTargetSuite(t, func(t *testing.T) contract.BackupTargetHarness {
		fake := fakes.NewBackupTarget()
		return contract.BackupTargetHarness{
			Target: fake,
			Ref:    ports.TargetRef{Scheme: "memory", Path: "/backups"},
			Keys:   fake.Objects,
		}
	})
}

func TestBackupTargetContract_LocalDir(t *testing.T) {
	contract.RunBackupTargetSuite(t, func(t *testing.T) contract.BackupTargetHarness {
		root := filepath.Join(t.TempDir(), "offsite")
		ref, err := ports.TargetURL("file://" + root)
		require.NoError(t, err)

		return contract.BackupTargetHarness{
			Target: localdir.New(),
			Ref:    ref,
			Keys:   func() []string { return walkKeys(t, root) },
		}
	})
}

func TestBackupTargetContract_Registry(t *testing.T) {
	// The registry is itself a BackupTarget, so it has to pass the same
	// suite. A dispatcher that lost a credential or mangled a ref on the way
	// through would fail here rather than in production.
	contract.RunBackupTargetSuite(t, func(t *testing.T) contract.BackupTargetHarness {
		registry, err := target.NewRegistry(localdir.New())
		require.NoError(t, err)

		root := filepath.Join(t.TempDir(), "offsite")
		ref, err := ports.TargetURL("file://" + root)
		require.NoError(t, err)

		return contract.BackupTargetHarness{
			Target: registry,
			Ref:    ref,
			Keys:   func() []string { return walkKeys(t, root) },
		}
	})
}

// walkKeys lists every file under a directory target, so the suite can see what
// a push actually put there.
func walkKeys(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		// Returned rather than skipped. The keys are the only witness the
		// suite has for what a push left on the target, so an entry the
		// walk could not read would drop out of the list silently and a
		// pushed plaintext component would pass as "not there".
		if err != nil {
			return err
		}
		if e.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	require.NoError(t, err)
	return out
}

func TestTargetRegistryRefusesAnUnbuiltScheme(t *testing.T) {
	registry, err := target.NewRegistry(localdir.New())
	require.NoError(t, err)

	_, err = registry.List(context.Background(), ports.TargetRef{Scheme: "s3", Path: "/bucket"})
	require.Error(t, err)

	// The refusal names what this build does have. An operator asking for
	// something reasonable should be told what to do instead, not only that
	// they are wrong.
	assert.Contains(t, err.Error(), "s3")
	assert.Contains(t, domain.AsError(err).Hint, "file")
}

func TestTargetRegistryRefusesDuplicateSchemes(t *testing.T) {
	_, err := target.NewRegistry(localdir.New(), localdir.New())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "file")
}

func TestTargetRegistryRefusesAnEmptyBuild(t *testing.T) {
	// A build with no targets would fail every configured push at push
	// time, during the nightly backup. Failing at startup instead is the
	// whole reason this is checked at all.
	_, err := target.NewRegistry()
	require.Error(t, err)
}

func TestStateStoreContract_Filesystem(t *testing.T) {
	contract.RunStateStoreSuite(t, func(t *testing.T) ports.StateStore {
		return state.New(domain.PathsUnder(t.TempDir(), "demo"))
	})
}

func TestRuntimeContract_Fake(t *testing.T) {
	contract.RunRuntimeSuite(t, func(t *testing.T) (ports.Runtime, ports.RuntimeConfig) {
		rt := fakes.NewRuntime()

		// Named volumes and bind mounts, mirroring the Compose project
		// the same suite runs against under the `docker` tag. Without
		// them the volume legs would have nothing to assert, and the
		// suite says so rather than passing empty.
		//
		// Deliberately in the wrong order, and with unsorted service
		// lists. The port promises sorted output and the Compose adapter
		// sorts; a fixture already in order would let the sortedness leg
		// pass without the fake doing anything, which is how that
		// divergence survived in the first place.
		rt.Storage = ports.ProjectStorage{
			Volumes: []ports.NamedVolume{
				{Name: "uploads", Actual: "demo_uploads", Services: []string{"web", "cache"}},
				{Name: "data", Actual: "demo_data", Services: []string{"web"}},
			},
			Binds: []ports.BindMount{
				{Source: "/var/run/docker.sock", Services: []string{"web"}},
				{Source: "/etc/hostname", Services: []string{"web"}},
			},
		}
		rt.VolumeContents = map[string]string{
			"demo_data":    "the-quarterly-report.pdf",
			"demo_uploads": "invoice-0000-4471.pdf",
		}

		// The log framing and one sample, arranged rather than
		// synthesised. The inspection legs assert the *shape* both
		// implementations must produce -- a container name in front of
		// every line, a memory figure a running container really has --
		// and a fake that invented them from its own service list would
		// agree with the parser by construction instead of by contract.
		rt.LogOutput = "demo-app-1  | 2026-08-11T09:12:33.123456789Z app is up\n" +
			"demo-db-1  | 2026-08-11T09:12:34.000000000Z ready to accept connections\n"
		rt.StatsResult = []ports.ServiceStats{
			{Service: "app", Container: "demo-app-1", Replica: 1,
				CPUPercent: 1.5, MemoryBytes: 64 << 20},
		}

		return rt, ports.RuntimeConfig{Product: "demo"}
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
