package suite

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/release"
)

// RFC 0045: checks a release ships run beside the manager's own in the
// preflight step, and in doctor. A fatal one refuses a forward move and only
// warns where a release that already ran is brought back.

// kernelCheck passes while the data directory holds kernel-ok, so a test can
// apply a baseline and then break the machine under it.
const kernelCheck = `[ -f "$DEMO_DATA_DIR/kernel-ok" ] && exit 0
echo "vm.max_map_count is 65530" >&2
exit 1`

func kernelChecks(fatal bool) string {
	out := "  kernel:\n" +
		"    description: the kernel allows enough memory maps\n" +
		"    command: [hooks/preflight]\n" +
		"    remedy: raise vm.max_map_count to 262144\n"
	if fatal {
		out += "    fatal: true\n"
	}
	return out
}

// withChecks declares checks in the bundle at dir, at the manager version that
// runs them, and ships hooks/preflight with script as its body.
func withChecks(t *testing.T, dir, checks, script string) {
	t.Helper()
	path := filepath.Join(dir, "manifest.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	patched := strings.Replace(string(raw), `min_manager_version: "0.0.0"`,
		`min_manager_version: "`+domain.PreflightMinManagerVersion+`"`, 1)
	require.NotEqual(t, string(raw), patched, "the fixture no longer declares the manager version this rewrites")
	patched += "\npreflight:\n" + checks
	require.NoError(t, os.WriteFile(path, []byte(patched), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks", "preflight"),
		[]byte("#!/bin/sh\n"+script+"\n"), 0o755))
}

// preflightHarness is an installation whose release declares checks, patched
// before install so the recorded digest is the patched bundle's.
func preflightHarness(t *testing.T, checks string) *harness {
	t.Helper()
	h := newHarness(t)
	withChecks(t, h.Release.Root, checks, kernelCheck)
	var err error
	h.Release, err = release.Load(h.Release.Root)
	require.NoError(t, err)
	h.install()
	h.setHookEnv()
	return h
}

func (h *harness) kernel(ok bool) {
	h.t.Helper()
	marker := filepath.Join(h.Paths.DataDir(), "kernel-ok")
	if ok {
		require.NoError(h.t, os.MkdirAll(h.Paths.DataDir(), 0o755))
		require.NoError(h.t, os.WriteFile(marker, nil, 0o644))
		return
	}
	require.NoError(h.t, os.Remove(marker))
}

// vendorResults is what the vendor's checks reported, newest last.
func (h *harness) vendorResults() []events.CheckResult {
	var out []events.CheckResult
	for _, e := range h.Events.OfKind(events.KindCheck) {
		if e.Check != nil && e.Check.Category == "vendor" {
			out = append(out, *e.Check)
		}
	}
	return out
}

func TestAFatalCheckRefusesApplyBeforeAnythingChanges(t *testing.T) {
	h := preflightHarness(t, kernelChecks(true))
	ctx := context.Background()
	before, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)

	_, err = ops.Apply(ctx, h.Deps, ops.Options{})
	require.Error(t, err)
	assert.Equal(t, domain.ExitPreflight, domain.ExitCode(err))
	for _, want := range []string{
		"the kernel allows enough memory maps", "vm.max_map_count is 65530", "raise vm.max_map_count to 262144",
	} {
		assert.Contains(t, err.Error(), want)
	}
	// Asking what is running is how preflight decides on the port check;
	// anything else would be a change.
	for _, call := range h.Runtime.Calls {
		assert.Equal(t, "Status", call, "a refused apply asked the runtime for more than its status")
	}
	after, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, before, after, "a refused apply changed the recorded release")
}

// One report lists every blocker, the manager's and the vendor's, so an
// operator fixing one does not discover the next on the retry.
func TestAVendorBlockerIsListedBesideTheManagers(t *testing.T) {
	h := newHarness(t)
	withChecks(t, h.Release.Root, kernelChecks(true), kernelCheck)
	path := filepath.Join(h.Release.Root, "manifest.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	other := "s390x"
	if runtime.GOARCH == other {
		other = "ppc64le"
	}
	patched := strings.Replace(string(raw), "architectures: [amd64, arm64]", "architectures: ["+other+"]", 1)
	require.NotEqual(t, string(raw), patched, "the fixture no longer declares the architectures this rewrites")
	require.NoError(t, os.WriteFile(path, []byte(patched), 0o644))
	h.Release, err = release.Load(h.Release.Root)
	require.NoError(t, err)
	h.install()
	h.setHookEnv()

	_, err = ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.Error(t, err)
	assert.Equal(t, domain.ExitPreflight, domain.ExitCode(err))
	assert.Contains(t, err.Error(), "CPU architecture is supported by the release")
	assert.Contains(t, err.Error(), "the kernel allows enough memory maps: vm.max_map_count is 65530")
}

func TestACheckThatIsNotFatalWarnsAndTheApplyGoesOn(t *testing.T) {
	h := preflightHarness(t, kernelChecks(false))

	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)
	results := h.vendorResults()
	require.Len(t, results, 1)
	assert.Equal(t, "vendor.kernel", results[0].ID)
	assert.Equal(t, events.CheckWarn, results[0].Status)
	assert.Equal(t, "vm.max_map_count is 65530", results[0].Message)
}

// Boot and start bring back a release that already ran: a fatal check warns
// there, because a refused start would leave a stopped product no way back
// (decision 4).
func TestBootAndStartOnlyWarnOnAFatalCheck(t *testing.T) {
	h := preflightHarness(t, kernelChecks(true))
	h.kernel(true)
	applyBaseline(t, h)
	h.kernel(false)
	ctx := context.Background()

	_, err := ops.Apply(ctx, h.Deps, ops.Options{Startup: true})
	require.NoError(t, err, "boot was refused by a vendor's check")

	_, err = ops.Stop(ctx, h.Deps, ops.StopOptions{})
	require.NoError(t, err)
	_, err = ops.Start(ctx, h.Deps, ops.Options{})
	require.NoError(t, err, "start was refused by a vendor's check")

	results := h.vendorResults()
	require.Len(t, results, 3, "baseline, boot and start each ran the check")
	assert.Equal(t, events.CheckOK, results[0].Status)
	for _, res := range results[1:] {
		assert.Equal(t, events.CheckWarn, res.Status)
	}

	// The same machine still refuses a plain apply.
	_, err = ops.Apply(ctx, h.Deps, ops.Options{})
	require.Error(t, err)
	assert.Equal(t, domain.ExitPreflight, domain.ExitCode(err))
}

// Rollback is the way back, and a vendor's check does not withhold it.
func TestRollbackOnlyWarnsOnAFatalCheck(t *testing.T) {
	h := preflightHarness(t, kernelChecks(true))
	h.kernel(true)
	applyBaseline(t, h)
	ctx := context.Background()
	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: stageUpgradeSource(t, h)})
	require.NoError(t, err)
	setSchema(t, h, 12)
	h.kernel(false)

	_, err = ops.Rollback(ctx, h.Deps, ops.RollbackOptions{})
	require.NoError(t, err, "rollback was refused by the previous release's check")
	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.2.0", current.Version.String())
	results := h.vendorResults()
	require.NotEmpty(t, results)
	assert.Equal(t, events.CheckWarn, results[len(results)-1].Status)
}

// An update runs the new release's checks: a fatal one refuses it, and the
// current release keeps running.
func TestTheNewReleasesCheckRefusesAnUpdate(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	src := stageUpgradeSource(t, h)
	withChecks(t, src, kernelChecks(true), kernelCheck)
	ctx := context.Background()
	ups := h.Runtime.UpCount

	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: src})
	require.Error(t, err)
	assert.Equal(t, domain.ExitPreflight, domain.ExitCode(err))
	assert.Contains(t, err.Error(), "vm.max_map_count is 65530")

	current, err := h.Deps.State.CurrentRelease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "1.2.0", current.Version.String(), "the refused release became current")
	assert.Equal(t, ups, h.Runtime.UpCount, "a refused update re-created services")
}

// doctor runs the installed release's checks under their own group: a fatal
// one failing fails doctor, and one that is not fatal only warns. Counted
// against the same machine with the check passing, because the harness has
// failures of its own (it holds no age identity).
func TestDoctorRunsTheInstalledReleasesChecks(t *testing.T) {
	for fatal, want := range map[bool]events.CheckStatus{true: events.CheckFail, false: events.CheckWarn} {
		h := preflightHarness(t, kernelChecks(fatal))
		h.kernel(true)
		applyBaseline(t, h)
		ctx := context.Background()

		passing, err := ops.Doctor(ctx, h.Deps)
		require.NoError(t, err)
		kernel := resultOf(t, passing.Results, "vendor.kernel")
		assert.Equal(t, events.CheckOK, kernel.Status)
		assert.Equal(t, "vendor", kernel.Category)
		assert.Equal(t, "the kernel allows enough memory maps", kernel.Description)

		h.kernel(false)
		failing, err := ops.Doctor(ctx, h.Deps)
		require.NoError(t, err)
		kernel = resultOf(t, failing.Results, "vendor.kernel")
		assert.Equal(t, want, kernel.Status, "fatal=%t", fatal)
		assert.Equal(t, "vm.max_map_count is 65530", kernel.Message)
		assert.Equal(t, "raise vm.max_map_count to 262144", kernel.Remedy)
		if fatal {
			assert.Equal(t, passing.Summary.Fail+1, failing.Summary.Fail)
			assert.Equal(t, domain.ExitPreflight, failing.ExitCode())
		} else {
			assert.Equal(t, passing.Summary.Fail, failing.Summary.Fail, "a check that is not fatal failed doctor")
			assert.Equal(t, passing.Summary.Warn+1, failing.Summary.Warn)
		}
	}
}

func resultOf(t *testing.T, results []events.CheckResult, id string) events.CheckResult {
	t.Helper()
	for _, res := range results {
		if res.ID == id {
			return res
		}
	}
	t.Fatalf("no %s among %d results", id, len(results))
	return events.CheckResult{}
}

// An operator's Ctrl-C during a check is an interruption, not the check
// failing: a refusal would blame the vendor's check for what the operator did.
func TestACancelledCheckIsAnInterruption(t *testing.T) {
	h := newHarness(t)
	withChecks(t, h.Release.Root, kernelChecks(true), "sleep 5")
	var err error
	h.Release, err = release.Load(h.Release.Root)
	require.NoError(t, err)
	h.install()
	h.setHookEnv()

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	_, err = ops.Apply(ctx, h.Deps, ops.Options{})
	require.Error(t, err)
	assert.Equal(t, domain.ExitInterrupted, domain.ExitCode(err), "%v", err)
	assert.NotContains(t, err.Error(), "preflight checks failed")

	// doctor too: a cut-off run is not a report, and must not exit 0.
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	_, err = ops.Doctor(cancelled, h.Deps)
	assert.Equal(t, domain.ExitInterrupted, domain.ExitCode(err), "%v", err)
}
