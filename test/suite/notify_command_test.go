package suite

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/source/oci"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/test/clitest"
)

// notifyChildEnv marks the process TestANotifyTargetAddedByCommandIsTold runs
// its body in.
const notifyChildEnv = "MORZER_TEST_NOTIFY_CHILD"

// TestANotifyTargetAddedByCommandIsTold: from a fresh installation with no hand
// edits, a webhook added through `notify target add` receives the outcome of an
// operation and the news that a release was staged.
//
// Driven through the CLI rather than through Deps, because what was missing is
// the path from a command to a notifier: the suites that inject a recording
// notifier pass whether or not anything can configure a real one.
//
// The body runs in a process of its own. A webhook must be https (RFC 0015
// decision 3), the CLI trusts the system's certificates and nothing else, and Go
// reads SSL_CERT_FILE once per process, so the test's own server can be trusted
// only in a process that has verified no certificate yet.
func TestANotifyTargetAddedByCommandIsTold(t *testing.T) {
	if os.Getenv(notifyChildEnv) == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestANotifyTargetAddedByCommandIsTold$", "-test.v")
		cmd.Env = append(os.Environ(), notifyChildEnv+"=1")
		out, err := cmd.CombinedOutput()
		if strings.Contains(string(out), "--- SKIP") {
			t.Skipf("the child skipped:\n%s", out)
		}
		require.NoError(t, err, "%s", out)
		return
	}

	var (
		mu    sync.Mutex
		kinds []string
	)
	hook := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev struct {
			Kind string `json:"kind"`
		}
		_ = json.NewDecoder(r.Body).Decode(&ev)
		mu.Lock()
		kinds = append(kinds, ev.Kind)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer hook.Close()

	certFile := filepath.Join(t.TempDir(), "hook.pem")
	require.NoError(t, os.WriteFile(certFile,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: hook.Certificate().Raw}), 0o600))
	t.Setenv("SSL_CERT_FILE", certFile)

	r := clitest.NewInstalled(t)

	// The channel: a registry serving the next release, which `update
	// --stage` fetches and announces.
	archive := filepath.Join(t.TempDir(), "bundle.tar.zst")
	writeTarZst(t, r.BundleAt("1.3.0"), archive)
	data, err := os.ReadFile(archive)
	require.NoError(t, err)
	registry := newFakeRegistry(t, data, oci.MediaType, 0)

	r.Run("notify", "target", "add", hook.URL+"/morzer").ExitCode(0)
	r.Run("config", "set", "update.channel="+ociRef(registry, "demo/bundle", "stable").String()).ExitCode(0)
	r.Run("config", "set", "http_port=19000").ExitCode(0)
	r.Run("update", "--stage").ExitCode(0)

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, kinds, "operation.finished", "the outcome of `config set` did not arrive")
	assert.Contains(t, kinds, "update.staged", "the staged release was not announced")
}

// TestANotifyPasswordWrittenByHandIsNotShown. `notify target add` refuses a
// password in a url, but a state file written before the command existed may
// carry one, and list and status both print a target's url.
func TestANotifyPasswordWrittenByHandIsNotShown(t *testing.T) {
	h := newHarness(t)
	inst := h.install()
	inst.Notify.Targets = []domain.NotifyTargetConfig{{URL: "https://ops:hunter2@hooks.example/morzer"}}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	targets, err := ops.NotifyTargetList(context.Background(), h.Deps)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	assert.NotContains(t, targets[0].Target, "hunter2")

	status, err := ops.GetStatus(context.Background(), h.Deps)
	require.NoError(t, err)
	require.Len(t, status.Notify, 1)
	assert.NotContains(t, status.Notify[0], "hunter2")
}
