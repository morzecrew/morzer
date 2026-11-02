package ops

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/hooks"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	infraexec "github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// checkRelease is a release whose bundle holds one check script, check.
func checkRelease(t *testing.T, script string) domain.Release {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "hooks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "hooks", "check"),
		[]byte("#!/bin/sh\n"+script+"\n"), 0o755))
	return domain.Release{Root: root, Manifest: domain.Manifest{Metadata: domain.Metadata{
		Name: "demo", Version: domain.MustParseVersion("1.2.0"),
	}}}
}

// What a check exits and writes maps onto a result (RFC 0045 §5.2): exit 0
// passes, exit 2 passes as not applicable, anything else fails quoting what
// the check said, and the vendor's remedy goes with a failure.
func TestACheckRunMapsOntoAResult(t *testing.T) {
	d := &Deps{Paths: domain.PathsUnder(t.TempDir(), "demo"), Hooks: hooks.NewRunner(infraexec.New())}
	inst := domain.Installation{ID: "inst_1", Product: "demo"}
	const remedy = "raise vm.max_map_count"

	cases := map[string]struct {
		script  string
		timeout time.Duration
		status  events.CheckStatus
		message string
	}{
		"passes":                      {"exit 0", 0, events.CheckOK, "passed"},
		"passes saying why":           {`printf '{"message":"262144 maps"}' >&3`, 0, events.CheckOK, "262144 maps"},
		"is told its phase":           {`printf '{"message":"%s %s"}' "$DEMO_PHASE" "$DEMO_OPERATION_TYPE" >&3`, 0, events.CheckOK, "preflight apply"},
		"does not apply":              {"exit 2", 0, events.CheckOK, "not applicable here"},
		"does not apply, saying why":  {`printf '{"message":"no GPU build"}' >&3; exit 2`, 0, events.CheckOK, "not applicable here: no GPU build"},
		"fails saying why on fd 3":    {`echo noise >&2; printf '{"message":"65530 maps"}' >&3; exit 1`, 0, events.CheckFail, "65530 maps"},
		"fails quoting stderr":        {"echo one >&2; echo two >&2; exit 1", 0, events.CheckFail, "one; two"},
		"fails quoting stdout":        {"echo out; exit 3", 0, events.CheckFail, "out"},
		"fails saying nothing":        {"exit 1", 0, events.CheckFail, "exited with code 1"},
		"times out, naming its limit": {"sleep 5", 200 * time.Millisecond, events.CheckFail, "did not finish within 200ms"},
		"writes garbage on fd 3":      {"printf 'not json' >&3", 0, events.CheckFail, "not a result object"},
		"writes garbage and exits 2":  {"printf 'null' >&3; exit 2", 0, events.CheckFail, "not a result object"},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			rel := checkRelease(t, tc.script)
			spec := domain.PreflightCheck{
				Description: "the kernel allows enough maps", Command: []string{"hooks/check"},
				Timeout: domain.Duration(tc.timeout), Remedy: remedy,
			}
			env := d.preflightEnv(inst, rel, domain.Version{}, "op_1", domain.OpTypeApply)

			got := d.runVendorCheck(context.Background(), rel, spec, env)
			assert.Equal(t, tc.status, got.Status, got.Message)
			if tc.status == events.CheckOK {
				assert.Equal(t, tc.message, got.Message)
				return
			}
			assert.Contains(t, got.Message, tc.message)
			assert.Equal(t, remedy, got.Remedy, "a failure carries the vendor's remedy")
		})
	}
}

// A check runs before anything is rendered, so it is not told where the
// secrets, the configuration file or the backups are, and never that it is in a
// plan (decision 10). In doctor it is told so, and no operation is named.
func TestACheckIsToldItsPhaseAndNotTheRenderedPaths(t *testing.T) {
	d := &Deps{Paths: domain.PathsUnder(t.TempDir(), "demo")}
	inst := domain.Installation{ID: "inst_1", Product: "demo"}
	rel := domain.Release{Root: "/var/lib/demo/releases/1.3.0", Manifest: domain.Manifest{Metadata: domain.Metadata{
		Name: "demo", Version: domain.MustParseVersion("1.3.0"),
	}}}

	vars := ports.HookEnvVars(d.preflightEnv(inst, rel, domain.MustParseVersion("1.2.0"), "op_1", domain.OpTypeUpdate))
	assert.Equal(t, "preflight", vars["DEMO_PHASE"])
	assert.Equal(t, "update", vars["DEMO_OPERATION_TYPE"])
	assert.Equal(t, "op_1", vars["DEMO_OPERATION_ID"])
	assert.Equal(t, "1.3.0", vars["DEMO_RELEASE_VERSION"])
	assert.Equal(t, rel.Root, vars["DEMO_RELEASE_DIR"])
	assert.Equal(t, "1.2.0", vars["DEMO_PREVIOUS_VERSION"])
	assert.Equal(t, d.Paths.DataDir(), vars["DEMO_DATA_DIR"])
	assert.Equal(t, "0", vars["DEMO_DRY_RUN"])
	for _, absent := range []string{"DEMO_SECRETS_DIR", "DEMO_CONFIG_FILE", "DEMO_BACKUP_DIR"} {
		assert.NotContains(t, vars, absent)
	}

	doctor := ports.HookEnvVars(d.preflightEnv(inst, rel, domain.Version{}, "", ports.HookOperationDoctor))
	assert.Equal(t, "doctor", doctor["DEMO_OPERATION_TYPE"])
	assert.NotContains(t, doctor, "DEMO_OPERATION_ID")
	assert.NotContains(t, doctor, "DEMO_PREVIOUS_VERSION")
}

// A fatal check refuses only where the operation lets it (decision 4); one the
// vendor did not mark fatal never does.
func TestAFatalCheckRefusesOnlyWhereTheOperationLetsIt(t *testing.T) {
	d := &Deps{}
	rel := domain.Release{Manifest: domain.Manifest{Preflight: map[string]domain.PreflightCheck{
		"kernel": {Description: "x", Command: []string{"hooks/check"}, Fatal: true},
		"dns":    {Description: "y", Command: []string{"hooks/check"}},
	}}}
	for refuse, want := range map[bool]map[string]bool{
		true:  {"vendor.dns": false, "vendor.kernel": true},
		false: {"vendor.dns": false, "vendor.kernel": false},
	} {
		checks := d.vendorChecks(rel, ports.HookEnv{}, refuse)
		require.Len(t, checks, 2)
		assert.Equal(t, "vendor.dns", checks[0].ID, "checks run in name order")
		for _, c := range checks {
			assert.Equal(t, want[c.ID], c.Fatal, "%s with refuse=%t", c.ID, refuse)
			assert.Equal(t, "vendor", c.Category)
		}
	}
}

// The release's checks get their declared time on top of the manager's three
// minutes, never a share of them.
func TestThePreflightStepGrantsTheChecksTheirTime(t *testing.T) {
	rel := domain.Release{Manifest: domain.Manifest{Preflight: map[string]domain.PreflightCheck{
		"kernel": {Timeout: domain.Duration(2 * time.Minute)},
		"dns":    {},
	}}}
	step := stepPreflight(&Deps{}, domain.Installation{}, rel, Options{})
	assert.Equal(t, 3*time.Minute+2*time.Minute+domain.DefaultPreflightTimeout, step.Timeout)
	assert.Equal(t, 3*time.Minute, stepPreflight(&Deps{}, domain.Installation{}, domain.Release{}, Options{}).Timeout)
}
