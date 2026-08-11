package suite

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	infraexec "github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/infra/state"
	"github.com/morzecrew/morzer/internal/infra/tools"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
	"github.com/morzecrew/morzer/test/fakes"
)

// A machine holding more than one installation.
//
// The path layout has always supported it and nothing ever reported it, so
// these drive the reader RFC 0020 adds: what `ls` says, what it says about an
// installation it cannot read, and what `doctor` says about the sharing this
// project declines to prevent.
//
// Driven at the lifecycle layer rather than through the CLI because the
// interesting cases are the failures -- a state file from the future, a wedged
// daemon, an /etc nobody may read -- and each of those is a fixture here and a
// hazard to arrange anywhere else.

// machine is several installations under one root.
type host struct {
	t    *testing.T
	Root string

	Deps       *ops.Deps
	Runtime    *perProjectRuntime
	Supervisor *fakes.Supervisor
}

func newHost(t *testing.T, products ...string) *host {
	t.Helper()

	root := t.TempDir()
	m := &host{
		t:          t,
		Root:       root,
		Runtime:    &perProjectRuntime{Runtime: fakes.NewRuntime()},
		Supervisor: fakes.NewSupervisor(),
	}

	// Pointed at the first product, the way a command on this machine is:
	// everything `ls` reads about the others is derived from the root this
	// layout sits in.
	paths := domain.PathsUnder(root, products[0])
	m.Deps = &ops.Deps{
		Paths:      paths,
		State:      state.New(paths),
		StateFor:   func(p domain.Paths) ports.StateStore { return state.New(p) },
		Runtime:    m.Runtime,
		Secrets:    fakes.NewSecretStore(),
		Supervisor: m.Supervisor,
		Tools:      tools.NewRegistry(infraexec.New()),
		Now:        func() time.Time { return time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC) },
	}

	for _, product := range products {
		m.add(product)
	}
	return m
}

// add creates an installation the way `init` leaves one: discoverable in /etc,
// with recorded state under /var/lib.
func (m *host) add(product string) domain.Installation {
	m.t.Helper()
	ctx := context.Background()

	paths := domain.PathsUnder(m.Root, product)
	for _, dir := range paths.ManagedDirs() {
		require.NoError(m.t, os.MkdirAll(dir.Path, os.FileMode(dir.Mode)))
	}
	// The marker discovery reads. Its contents are a report nothing reads
	// back, which is exactly why the state below is what the tests corrupt.
	require.NoError(m.t, os.WriteFile(paths.InstallationFile(), []byte("# managed\n"), 0o640))

	inst := domain.Installation{
		SchemaVersion: domain.InstallationSchemaVersion,
		ID:            "inst_" + product,
		Product:       product,
		CreatedAt:     domain.NewTime(m.Deps.Now()),
		Policy:        domain.DefaultPolicy(),
	}
	require.NoError(m.t, state.New(paths).SaveInstallation(ctx, inst))
	return inst
}

// install copies the example bundle in as product's current release, with the
// host port it publishes set to the given value.
func (m *host) install(product string, hostPort int) {
	m.t.Helper()
	ctx := context.Background()

	paths := domain.PathsUnder(m.Root, product)
	releaseRoot := filepath.Join(paths.ReleasesDir(), "1.2.0")
	copyBundle(m.t, testBundlePath(m.t), releaseRoot)
	retargetForProduct(m.t, releaseRoot, m.Root, product)

	rel, err := release.Load(releaseRoot)
	require.NoError(m.t, err)

	store := state.New(paths)
	require.NoError(m.t, store.SetCurrentRelease(ctx, domain.ReleaseRecord{
		SchemaVersion: domain.InstallationSchemaVersion,
		Name:          rel.Name(),
		Version:       rel.Version(),
		Digest:        rel.Digest,
		Root:          rel.Root,
		InstalledAt:   domain.NewTime(m.Deps.Now()),
	}))

	// The port is a declared parameter, so setting it is how an operator
	// moves one installation out of another's way -- and how this test
	// arranges a machine where nobody has.
	inst, err := store.LoadInstallation(ctx)
	require.NoError(m.t, err)
	if inst.Parameters == nil {
		inst.Parameters = map[string]string{}
	}
	inst.Parameters["http_port"] = itoa(hostPort)
	require.NoError(m.t, store.SaveInstallation(ctx, inst))
}

// retargetForProduct rewrites the example bundle so it belongs to a second
// installation.
//
// The paths, and also the product name and the Compose project: two
// installations on one machine have two project names by construction -- the
// name is the product -- and a fixture that left both called `demo` would be
// asking the runtime one question and reading it as two answers.
func retargetForProduct(t *testing.T, releaseRoot, testRoot, product string) {
	t.Helper()

	path := filepath.Join(releaseRoot, "manifest.yaml")
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	s := string(data)
	s = strings.ReplaceAll(s, " /etc/demo/", " "+testRoot+"/etc/"+product+"/")
	s = strings.ReplaceAll(s, " /run/demo/", " "+testRoot+"/run/"+product+"/")
	s = strings.ReplaceAll(s, "name: demo", "name: "+product)
	s = strings.ReplaceAll(s, "project: demo", "project: "+product)

	require.NoError(t, os.WriteFile(path, []byte(s), 0o644))
}

func (m *host) list(t *testing.T, opts ops.ListOptions) []ops.InstallationEntry {
	t.Helper()
	entries, err := ops.ListInstallations(context.Background(), m.Deps, opts)
	require.NoError(t, err)
	return entries
}

func entryFor(t *testing.T, entries []ops.InstallationEntry, product string) ops.InstallationEntry {
	t.Helper()
	for _, e := range entries {
		if e.Product == product {
			return e
		}
	}
	t.Fatalf("no row for %q in %+v", product, entries)
	return ops.InstallationEntry{}
}

// perProjectRuntime answers Status differently per Compose project.
//
// The shared fake answers for one project, which is precisely the assumption
// under test: `ls --status` asks each installation separately, and a fake that
// could not tell them apart would pass whether or not it did.
type perProjectRuntime struct {
	*fakes.Runtime

	// FailProject names a project whose query fails, and Block one whose
	// query never returns until the context says so.
	FailProject  string
	BlockProject string
	Running      map[string]int
	Total        map[string]int
}

func (r *perProjectRuntime) Status(ctx context.Context, cfg ports.RuntimeConfig) ([]ports.ServiceState, error) {
	if cfg.Project == r.BlockProject {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if cfg.Project == r.FailProject {
		return nil, domain.RuntimeError(nil, "cannot connect to the Docker daemon")
	}

	out := make([]ports.ServiceState, 0, r.Total[cfg.Project])
	for i := range r.Total[cfg.Project] {
		s := ports.ServiceState{Name: "svc" + itoa(i), State: "exited"}
		if i < r.Running[cfg.Project] {
			s.State = "running"
		}
		out = append(out, s)
	}
	return out, nil
}

func TestAListingNamesEveryInstallationTheMachineHolds(t *testing.T) {
	m := newHost(t, "demo", "sandbox")

	entries := m.list(t, ops.ListOptions{})
	require.Len(t, entries, 2)

	// Sorted, so a listing an operator pastes into a ticket twice is the
	// same listing twice.
	assert.Equal(t, "demo", entries[0].Product)
	assert.Equal(t, "sandbox", entries[1].Product)
	assert.Equal(t, filepath.Join(m.Root, "etc", "sandbox"), entries[1].Path)
	assert.Empty(t, entries[0].Problem)
}

// TestTheUnitsColumnCountsWhatIsActuallyInstalled.
//
// One installation with units and one without, because a column that reported a
// constant would pass every single-installation fixture there is -- and
// `--install-units=false` is a supported choice, so zero is a real answer rather
// than a broken one.
func TestTheUnitsColumnCountsWhatIsActuallyInstalled(t *testing.T) {
	m := newHost(t, "demo", "sandbox")

	units, err := m.Supervisor.Units(ports.UnitParams{Product: "demo", ManagerPath: "/usr/bin/morzer"})
	require.NoError(t, err)
	require.NoError(t, m.Supervisor.InstallUnits(context.Background(), units))

	entries := m.list(t, ops.ListOptions{})
	assert.Positive(t, entryFor(t, entries, "demo").Units)
	assert.Equal(t, 0, entryFor(t, entries, "sandbox").Units,
		"an installation that manages no units was reported as managing some")
}

// TestAnInstallationFromTheFutureIsReportedRatherThanInterpreted is decision 5c.
//
// The row exists, it names what it could not read, and it carries nothing this
// manager half-understood. Reporting a newer schema's fields as fact is worse
// than reporting that they cannot be read: the operator would act on them.
func TestAnInstallationFromTheFutureIsReportedRatherThanInterpreted(t *testing.T) {
	m := newHost(t, "demo", "sandbox")

	paths := domain.PathsUnder(m.Root, "sandbox")
	future := map[string]any{
		"schema_version": domain.InstallationSchemaVersion + 40,
		"installation": map[string]any{
			"schema_version": domain.InstallationSchemaVersion + 40,
			"id":             "inst_sandbox", "product": "sandbox",
			"mode": "dev", "profile": "embedded",
		},
	}
	raw, err := json.Marshal(future)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(paths.InstallationState(), raw, 0o640))

	entries := m.list(t, ops.ListOptions{})
	require.Len(t, entries, 2, "an installation it cannot read was dropped, so it looks absent")

	broken := entryFor(t, entries, "sandbox")
	assert.Contains(t, broken.Problem, "newer manager")
	assert.Contains(t, broken.Problem, itoa(domain.InstallationSchemaVersion+40),
		"the problem does not name the version, so nobody can tell how far ahead it is")
	assert.Zero(t, broken.SchemaVersion)
	assert.Empty(t, string(broken.Mode), "a mode this manager did not validate was reported as fact")

	// And the neighbour is untouched, which is the half a single-installation
	// fixture cannot check.
	assert.Empty(t, entryFor(t, entries, "demo").Problem)
}

// TestAnEtcNobodyMayReadIsAnErrorNotAnEmptyMachine.
//
// "I cannot look" and "there is nothing there" are different answers, and a
// listing that conflated them would tell an operator their machine is bare at
// the moment its permissions are wrong.
func TestAnEtcNobodyMayReadIsAnErrorNotAnEmptyMachine(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 directory, so this arrangement proves nothing")
	}
	m := newHost(t, "demo")

	etc := filepath.Join(m.Root, "etc")
	require.NoError(t, os.Chmod(etc, 0o000))
	t.Cleanup(func() { _ = os.Chmod(etc, 0o755) })

	_, err := ops.ListInstallations(context.Background(), m.Deps, ops.ListOptions{})
	require.Error(t, err, "an unreadable /etc was reported as a machine with nothing on it")
	assert.Contains(t, domain.AsError(err).Message, etc)
}

// TestAMissingEtcIsABareMachine is the other side of the same question: a host
// that has never been written to is not a fault, and `ls` on one exits zero.
func TestAMissingEtcIsABareMachine(t *testing.T) {
	root := t.TempDir()
	paths := domain.PathsUnder(root, "demo")
	deps := &ops.Deps{
		Paths: paths, State: state.New(paths),
		StateFor: func(p domain.Paths) ports.StateStore { return state.New(p) },
	}

	entries, err := ops.ListInstallations(context.Background(), deps, ops.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// TestOneWedgedDaemonCostsOneRow is the reason --status reports per row.
//
// A listing that failed as a whole because one installation's runtime would not
// answer is a listing that stops being run, which is the failure mode this
// column's design is entirely about.
func TestOneWedgedDaemonCostsOneRow(t *testing.T) {
	m := newHost(t, "demo", "sandbox")
	m.install("demo", 18080)
	m.install("sandbox", 18081)

	m.Runtime.Total = map[string]int{"demo": 3}
	m.Runtime.Running = map[string]int{"demo": 3}
	m.Runtime.FailProject = "sandbox"

	entries := m.list(t, ops.ListOptions{Status: true})

	demo := entryFor(t, entries, "demo")
	require.NotNil(t, demo.Services, "a working installation lost its count to its neighbour")
	assert.Equal(t, ops.ServiceCounts{Running: 3, Total: 3}, *demo.Services)
	assert.Empty(t, demo.ServicesProblem)

	broken := entryFor(t, entries, "sandbox")
	assert.Nil(t, broken.Services)
	assert.Contains(t, broken.ServicesProblem, "Docker daemon")
}

// TestAStatusQueryIsBoundedPerInstallation is decision 5a.
//
// The bound is what keeps one unresponsive daemon from turning a machine
// listing into a hang. Driven with a runtime that never answers: without the
// per-row deadline this test does not fail, it stops.
func TestAStatusQueryIsBoundedPerInstallation(t *testing.T) {
	m := newHost(t, "demo", "sandbox")
	m.install("demo", 18080)
	m.install("sandbox", 18081)

	m.Runtime.Total = map[string]int{"sandbox": 2}
	m.Runtime.Running = map[string]int{"sandbox": 2}
	m.Runtime.BlockProject = "demo"

	started := time.Now()
	entries := m.list(t, ops.ListOptions{Status: true, StatusTimeout: 50 * time.Millisecond})

	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("the listing waited %s on a daemon that never answers", elapsed)
	}
	assert.Contains(t, entryFor(t, entries, "demo").ServicesProblem, "timed out",
		"the row that timed out does not say so, so the reader cannot tell it from a broken release")

	// The rest of the machine is unaffected, which is the claim the bound
	// exists to keep.
	sandbox := entryFor(t, entries, "sandbox")
	require.NotNil(t, sandbox.Services)
	assert.Equal(t, 2, sandbox.Services.Running)
}

// TestAListingWithoutAStateReaderRefusesByName.
//
// An embedder that assembles Deps itself and wires no reader gets an internal
// error rather than a listing in which every installation looks unreadable --
// which would read as a broken machine instead of a broken binary.
func TestAListingWithoutAStateReaderRefusesByName(t *testing.T) {
	m := newHost(t, "demo")
	m.Deps.StateFor = nil

	_, err := ops.ListInstallations(context.Background(), m.Deps, ops.ListOptions{})
	require.Error(t, err)
	assert.Equal(t, domain.ExitInternal, domain.ExitCode(err))
}

func TestTwoInstallationsWantingOnePortIsAWarning(t *testing.T) {
	m := newHost(t, "demo", "sandbox")
	m.install("demo", 18080)
	m.install("sandbox", 18080)

	report, err := ops.Doctor(context.Background(), m.Deps)
	require.NoError(t, err)

	found := findResult(t, report, "machine.ports")
	// A warning and never a failure: two installations on one machine is
	// supported, and a `doctor` that failed on a supported arrangement
	// teaches operators that a red doctor is normal.
	assert.Equal(t, "warn", found.Status)
	assert.Contains(t, found.Message, "18080")
	assert.Contains(t, found.Message, "demo")
	assert.Contains(t, found.Message, "sandbox")
	assert.Contains(t, found.Remedy, "config set")

	// Nothing in this category may fail. Other checks legitimately do on a
	// fixture with no secrets rendered and nothing running; what this pins
	// is that sharing a machine is never itself reported as broken.
	for _, r := range report.Results {
		if strings.HasPrefix(r.ID, "machine.") && r.Status == "fail" {
			t.Errorf("%s failed, so a supported arrangement makes `doctor` exit non-zero: %s",
				r.ID, r.Message)
		}
	}
}

// TestTwoInstallationsOnDifferentPortsCollideWithNobody is the half that decides
// whether the check is worth having: one that warned about any two installations
// is one an operator learns to ignore.
func TestTwoInstallationsOnDifferentPortsCollideWithNobody(t *testing.T) {
	m := newHost(t, "demo", "sandbox")
	m.install("demo", 18080)
	m.install("sandbox", 18081)

	report, err := ops.Doctor(context.Background(), m.Deps)
	require.NoError(t, err)

	assert.Equal(t, "ok", findResult(t, report, "machine.ports").Status)

	installations := findResult(t, report, "machine.installations")
	assert.Equal(t, "ok", installations.Status)
	assert.Contains(t, installations.Message, "demo")
	assert.Contains(t, installations.Message, "sandbox")
}

// TestUnitsWithoutReadableStateIsWorthSaying.
//
// The arrangement nothing else reports: systemd starts this installation on
// every boot and the manager cannot tell it anything, because its state will
// not load.
func TestUnitsWithoutReadableStateIsWorthSaying(t *testing.T) {
	m := newHost(t, "demo", "sandbox")

	units, err := m.Supervisor.Units(ports.UnitParams{Product: "sandbox", ManagerPath: "/usr/bin/morzer"})
	require.NoError(t, err)
	require.NoError(t, m.Supervisor.InstallUnits(context.Background(), units))

	paths := domain.PathsUnder(m.Root, "sandbox")
	require.NoError(t, os.WriteFile(paths.InstallationState(), []byte("{not json"), 0o640))

	report, err := ops.Doctor(context.Background(), m.Deps)
	require.NoError(t, err)

	found := findResult(t, report, "machine.installations")
	assert.Equal(t, "warn", found.Status)
	assert.Contains(t, found.Message, "sandbox")
	assert.Contains(t, found.Remedy, "morzer ls")
}
