package suite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/state"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// parseMetrics reads the exposition with Prometheus's own parser, so a file
// the node exporter would refuse fails here and not at a scrape (RFC 0047
// decision 12).
func parseMetrics(t *testing.T, text string) map[string]*dto.MetricFamily {
	t.Helper()
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(text))
	require.NoError(t, err, "the exposition does not parse:\n%s", text)
	return families
}

// sample is one series' value, by its labels other than product; ok is false
// when no such series exists.
func sample(families map[string]*dto.MetricFamily, name string, labels ...string) (float64, bool) {
	f, found := families[name]
	if !found {
		return 0, false
	}
	for _, m := range f.GetMetric() {
		have := map[string]string{}
		for _, l := range m.GetLabel() {
			have[l.GetName()] = l.GetValue()
		}
		match := true
		for i := 0; i+1 < len(labels); i += 2 {
			match = match && have[labels[i]] == labels[i+1]
		}
		if match {
			return m.GetGauge().GetValue(), true
		}
	}
	return 0, false
}

// What `status` knows is exported, every series labelled with the product, and
// a finished job behind an inactive profile is neither running nor down
// (RFC 0047 §5.4, decision 19).
func TestMetricsExportWhatStatusKnows(t *testing.T) {
	h := appliedHarness(t)
	ctx := context.Background()
	var configured []string
	for name := range h.Runtime.Services {
		configured = append(configured, name)
	}
	h.Runtime.ValidateResult = ports.Rendered{Services: configured}
	h.Runtime.Services["reset-wipe"] = ports.ServiceState{Name: "reset-wipe", State: "exited", ExitCode: 0}

	report, err := ops.Metrics(ctx, h.Deps, ops.MetricsOptions{})
	require.NoError(t, err)
	assert.True(t, report.Up)
	families := parseMetrics(t, report.Text)

	for name, f := range families {
		for _, m := range f.GetMetric() {
			assert.Contains(t, m.GetLabel(), &dto.LabelPair{Name: ptr("product"), Value: ptr("demo")}, name)
		}
	}
	value := func(name string, labels ...string) float64 {
		t.Helper()
		v, ok := sample(families, name, labels...)
		require.True(t, ok, "no %s %v in:\n%s", name, labels, report.Text)
		return v
	}
	assert.Equal(t, 1.0, value("morzer_up"))
	assert.Equal(t, 1.0, value("morzer_release_info", "version", "1.2.0", "mode", "production"))
	assert.Equal(t, 1.0, value("morzer_runtime_up"))
	for _, name := range configured {
		assert.Equal(t, 1.0, value("morzer_service_containers", "service", name))
	}
	_, listed := sample(families, "morzer_service_running", "service", "reset-wipe")
	assert.False(t, listed, "a finished on-demand job is exported as a service that is down")
	assert.Equal(t, 0.0, value("morzer_stopped"))
	assert.Equal(t, 0.0, value("morzer_operations_needing_attention"))
	assert.Equal(t, 1.0, value("morzer_last_operation_success", "type", "apply"))
	assert.Positive(t, value("morzer_last_operation_timestamp_seconds", "type", "apply"))
	assert.Equal(t, 0.0, value("morzer_update_staged"))

	// No backup yet is no series, never a timestamp of zero.
	_, hasBackup := sample(families, "morzer_last_backup_timestamp_seconds")
	assert.False(t, hasBackup)

	// Nothing a scrape reads carries free text (decision 7).
	assert.NotContains(t, report.Text, "http://")
}

// A runtime that does not answer is `morzer_runtime_up 0` with no service
// series, and a state that does not load is `morzer_up 0` alone, written into
// the directory the unit names (decisions 9 and 15).
func TestMetricsSayWhatCouldNotBeRead(t *testing.T) {
	h := appliedHarness(t)
	ctx := context.Background()

	h.Runtime.Fail["Status"] = domain.RuntimeError(nil, "daemon gone")
	report, err := ops.Metrics(ctx, h.Deps, ops.MetricsOptions{})
	require.NoError(t, err)
	families := parseMetrics(t, report.Text)
	v, ok := sample(families, "morzer_runtime_up")
	require.True(t, ok)
	assert.Equal(t, 0.0, v)
	assert.NotContains(t, families, "morzer_service_running", "a runtime that did not answer was exported as zeros")

	require.NoError(t, os.WriteFile(h.Paths.InstallationState(), []byte("{not json"), 0o600))
	dir := t.TempDir()
	report, err = ops.Metrics(ctx, h.Deps, ops.MetricsOptions{Textfile: dir})
	require.NoError(t, err, "a state that does not load is an answer, not an error")
	assert.False(t, report.Up)
	written, err := os.ReadFile(filepath.Join(dir, "morzer-status-demo.prom"))
	require.NoError(t, err)
	families = parseMetrics(t, string(written))
	assert.Len(t, families, 1)
	v, ok = sample(families, "morzer_up")
	require.True(t, ok)
	assert.Equal(t, 0.0, v)
}

// The file appears whole or not at all, readable by the node exporter's user
// whatever the umask, and a symlink at its name is replaced, never written
// through (decision 8).
func TestTheMetricsFileIsWrittenAtomically(t *testing.T) {
	h := appliedHarness(t)
	ctx := context.Background()
	dir := t.TempDir()

	elsewhere := filepath.Join(t.TempDir(), "victim")
	require.NoError(t, os.WriteFile(elsewhere, []byte("keep\n"), 0o600))
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(dir, "morzer-status-demo.prom")))

	old := syscall.Umask(0o077)
	_, err := ops.Metrics(ctx, h.Deps, ops.MetricsOptions{Textfile: dir})
	syscall.Umask(old)
	require.NoError(t, err)

	path := filepath.Join(dir, "morzer-status-demo.prom")
	info, err := os.Lstat(path)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular(), "the symlink was written through rather than replaced")
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
	victim, err := os.ReadFile(elsewhere)
	require.NoError(t, err)
	assert.Equal(t, "keep\n", string(victim))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "a temporary file was left beside the metrics")

	// A directory that cannot be written is an error, and the last file
	// stays.
	if os.Geteuid() != 0 {
		require.NoError(t, os.Chmod(dir, 0o555)) //nolint:gosec // the point is a directory nobody may write
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		_, err = ops.Metrics(ctx, h.Deps, ops.MetricsOptions{Textfile: dir})
		require.Error(t, err)
		_, err = os.Stat(path)
		assert.NoError(t, err, "a failed write removed the last file")
	}
}

// Two products in one directory write four files, none of them another's: with
// the product first, `foo`'s doctor file would be `foo-doctor`'s status file
// (decision 14).
func TestTwoProductsNeverShareAMetricsFile(t *testing.T) {
	names := map[string]bool{}
	for _, product := range []string{"foo", "foo-doctor"} {
		for _, role := range []string{"status", "doctor"} {
			name := ops.MetricsFileName(role, product)
			assert.False(t, names[name], "%s is written by two installations", name)
			names[name] = true
		}
	}
}

// The setting installs the metrics pair carrying its directory, and the doctor
// pair with it even with no notify target; unsetting it takes both away and
// removes both files (decisions 2, 3, 15 and 16).
func TestTheMetricsSettingReconcilesTheUnitsAndTheFiles(t *testing.T) {
	h := appliedHarness(t)
	ctx := context.Background()
	h.Supervisor.Present = true
	h.Deps.Supervisor = h.Supervisor
	// The machine manages units, which is what makes reconciling them its
	// business: `init --install-units=false` is a supported choice.
	units, err := h.Supervisor.Units(ports.UnitParams{Product: "demo"})
	require.NoError(t, err)
	require.NoError(t, h.Supervisor.InstallUnits(ctx, units, ports.EnableAll))
	dir := t.TempDir()

	_, err = ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{Set: map[string]string{"metrics.textfile": dir}})
	require.NoError(t, err)
	installed, err := h.Supervisor.InstalledUnits(ctx, "demo")
	require.NoError(t, err)
	assert.Contains(t, installed, "demo-metrics.timer")
	assert.Contains(t, installed, "demo-doctor.timer", "metrics alone did not install the scheduled doctor")

	_, err = ops.Metrics(ctx, h.Deps, ops.MetricsOptions{Textfile: dir})
	require.NoError(t, err)
	_, err = ops.DoctorUnattended(ctx, h.Deps)
	require.NoError(t, err)
	for _, name := range []string{"morzer-status-demo.prom", "morzer-doctor-demo.prom"} {
		_, err := os.Stat(filepath.Join(dir, name))
		require.NoError(t, err, name)
	}

	// Turning the doctor timer off retires its file and keeps the status.
	_, err = ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{Set: map[string]string{"doctor.scheduled": "false"}})
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "morzer-doctor-demo.prom"))
	assert.ErrorIs(t, err, os.ErrNotExist, "a frozen doctor file was left to read as current")
	_, err = os.Stat(filepath.Join(dir, "morzer-status-demo.prom"))
	assert.NoError(t, err)

	_, err = ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{Unset: []string{"metrics.textfile"}})
	require.NoError(t, err)
	installed, err = h.Supervisor.InstalledUnits(ctx, "demo")
	require.NoError(t, err)
	assert.NotContains(t, installed, "demo-metrics.timer")
	_, err = os.Stat(filepath.Join(dir, "morzer-status-demo.prom"))
	assert.ErrorIs(t, err, os.ErrNotExist, "a file nothing writes any more was left to scrape")
}

// The setting refuses a directory that is not there, and one a unit's command
// line could not carry as one argument (decision 10).
func TestTheMetricsSettingRefusesWhatAUnitCannotCarry(t *testing.T) {
	h := appliedHarness(t)
	ctx := context.Background()
	for _, dir := range []string{
		"relative/dir",
		filepath.Join(t.TempDir(), "missing"),
		t.TempDir() + "/with space",
		t.TempDir() + "/100%",
	} {
		_, err := ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{Set: map[string]string{"metrics.textfile": dir}})
		assert.Error(t, err, dir)
	}
}

// Only the scheduled run writes the doctor file, one series per check valued
// 0, 1 or 2 (decisions 4 and 11).
func TestOnlyTheScheduledDoctorWritesItsFile(t *testing.T) {
	h := appliedHarness(t)
	ctx := context.Background()
	dir := t.TempDir()
	_, err := ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{Set: map[string]string{"metrics.textfile": dir}})
	require.NoError(t, err)
	path := filepath.Join(dir, "morzer-doctor-demo.prom")

	_, err = ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist, "an operator's doctor wrote the file")

	report, err := ops.DoctorUnattended(ctx, h.Deps)
	require.NoError(t, err)
	written, err := os.ReadFile(path)
	require.NoError(t, err)
	families := parseMetrics(t, string(written))
	for _, res := range report.Results {
		v, ok := sample(families, "morzer_doctor_check", "check", res.ID, "category", res.Category)
		require.True(t, ok, res.ID)
		want := map[events.CheckStatus]float64{events.CheckOK: 0, events.CheckWarn: 1, events.CheckFail: 2}[res.Status]
		assert.Equal(t, want, v, res.ID)
	}
	_, ok := sample(families, "morzer_doctor_last_run_timestamp_seconds")
	assert.True(t, ok)
}

// `doctor` says when nothing is writing the export it was asked for
// (decision 10).
func TestDoctorSaysWhenTheExportIsNotWritten(t *testing.T) {
	h := appliedHarness(t)
	ctx := context.Background()

	report, err := ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	assert.Equal(t, "ok", findCheck(t, report, "metrics.textfile").Status, "unset is not a fault")

	dir := t.TempDir()
	_, err = ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{Set: map[string]string{"metrics.textfile": dir}})
	require.NoError(t, err)
	report, err = ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	assert.Equal(t, "warn", findCheck(t, report, "metrics.textfile").Status, "a file nothing wrote reads as healthy")

	_, err = ops.Metrics(ctx, h.Deps, ops.MetricsOptions{Textfile: dir})
	require.NoError(t, err)
	report, err = ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	assert.Equal(t, "ok", findCheck(t, report, "metrics.textfile").Status)

	stale := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(dir, "morzer-status-demo.prom"), stale, stale))
	report, err = ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	assert.Equal(t, "warn", findCheck(t, report, "metrics.textfile").Status, "a stale file reads as current")

	require.NoError(t, os.RemoveAll(dir))
	report, err = ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	assert.Contains(t, findCheck(t, report, "metrics.textfile").Message, "missing")
}

func ptr(s string) *string { return &s }

// Removing the installation takes its files out of the node exporter's
// directory, and leaves the directory, which is not the installation's
// (decision 16).
func TestRemovingTheInstallationRemovesItsMetricsFiles(t *testing.T) {
	h, inst := removable(t)
	ctx := context.Background()
	dir := t.TempDir()
	_, err := ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{Set: map[string]string{"metrics.textfile": dir}})
	require.NoError(t, err)
	for _, name := range []string{"morzer-status-demo.prom", "morzer-doctor-demo.prom", "node.prom"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x 1\n"), 0o644)) //nolint:gosec // a collector reads it
	}

	removeIt(t, h, ops.RemoveOptions{ConfirmedInstallationID: inst.ID})
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "node.prom", entries[0].Name(), "a file another tool wrote went with the installation")
}

// `ls --status` counts a finished job behind an inactive profile as the
// metrics do: neither running nor down (decision 19).
func TestTheListingCountsAFinishedJobAsTheMetricsDo(t *testing.T) {
	h := appliedHarness(t)
	var configured []string
	for name := range h.Runtime.Services {
		configured = append(configured, name)
	}
	h.Runtime.ValidateResult = ports.Rendered{Services: configured}
	h.Runtime.Services["reset-wipe"] = ports.ServiceState{Name: "reset-wipe", State: "exited", ExitCode: 0}
	h.Deps.StateFor = func(p domain.Paths) ports.StateStore { return state.New(p) }

	entries, err := ops.ListInstallations(context.Background(), h.Deps, ops.ListOptions{Status: true})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.NotNil(t, entries[0].Services, entries[0].ServicesProblem)
	assert.Equal(t, len(configured), entries[0].Services.Total)
	assert.Equal(t, len(configured), entries[0].Services.Running)
}
