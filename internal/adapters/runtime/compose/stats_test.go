package compose_test

import (
	"context"
	"strings"
	"testing"
)

// `docker stats` has no numeric output. Every figure arrives formatted for a
// terminal -- "1.234MiB / 7.628GiB", "12.34%", "-- / --" -- in two different
// unit families in the same line, and this adapter's whole job for `stats` is
// reading them back. A real daemon proves the flags are accepted; only a
// scripted one can produce a host with no block-IO accounting, a scaled
// service, and the shapes Docker emits on machines nobody here owns.

const psRunning = `{"Name":"demo-app-1","Service":"app","State":"running"}
{"Name":"demo-app-2","Service":"app","State":"running"}
{"Name":"demo-db-1","Service":"db","State":"running"}`

func TestStatsAttributesEachContainerToItsServiceAndReplica(t *testing.T) {
	r, runner := newRuntime()
	runner.OnOutput("compose --project-name demo", psRunning)
	runner.OnOutput("stats --no-stream", `
{"Name":"demo-app-1","CPUPerc":"12.34%","MemUsage":"64.5MiB / 512MiB","NetIO":"1.2kB / 3.4kB","BlockIO":"8.19kB / 0B"}
{"Name":"demo-app-2","CPUPerc":"0.00%","MemUsage":"32MiB / 512MiB","NetIO":"0B / 0B","BlockIO":"0B / 0B"}
{"Name":"demo-db-1","CPUPerc":"3.10%","MemUsage":"128MiB / 1GiB","NetIO":"5kB / 6kB","BlockIO":"1kB / 2kB"}`)

	stats, err := r.Stats(context.Background(), cfg())
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 3 {
		t.Fatalf("got %d rows, want one per container: %+v", len(stats), stats)
	}

	// One row per container and never an aggregate: a scaled service is
	// several containers, and a row keyed by service alone would print one
	// replica's numbers under the whole service's name.
	first := stats[0]
	if first.Service != "app" || first.Container != "demo-app-1" || first.Replica != 1 {
		t.Errorf("the first row is %+v, want app/demo-app-1/1", first)
	}
	if stats[1].Replica != 2 {
		t.Errorf("the second replica is numbered %d, want 2", stats[1].Replica)
	}

	if first.CPUPercent != 12.34 {
		t.Errorf("CPU read as %v, want 12.34", first.CPUPercent)
	}
	// Memory is binary (units.BytesSize) and the IO counters are decimal
	// (units.HumanSize), in the same line. Reading a kB as 1024 would
	// overstate every network figure by 2.4%, which never looks wrong.
	if first.MemoryBytes != 64.5*1024*1024 {
		t.Errorf("memory read as %d, want %d", first.MemoryBytes, int64(64.5*1024*1024))
	}
	if first.MemoryLimit != 512*1024*1024 {
		t.Errorf("the memory limit read as %d", first.MemoryLimit)
	}
	if first.NetRxBytes == nil || *first.NetRxBytes != 1200 {
		t.Errorf("1.2kB read as %v, want 1200 -- a decimal suffix is not 1024", first.NetRxBytes)
	}
	if first.BlockRead == nil || *first.BlockRead != 8190 {
		t.Errorf("8.19kB read as %v, want 8190", first.BlockRead)
	}
}

func TestStatsReportsAnUnaccountedCounterAsUnknownRatherThanZero(t *testing.T) {
	r, runner := newRuntime()
	runner.OnOutput("compose --project-name demo", `{"Name":"demo-app-1","Service":"app","State":"running"}`)
	// What a rootless daemon reports: no block-IO accounting. Zero is a
	// real reading -- a container that has written nothing -- so reporting
	// one here would make a host that cannot say indistinguishable from an
	// idle disk.
	runner.OnOutput("stats --no-stream",
		`{"Name":"demo-app-1","CPUPerc":"1.00%","MemUsage":"8MiB / 8GiB","NetIO":"1kB / 2kB","BlockIO":"-- / --"}`)

	stats, err := r.Stats(context.Background(), cfg())
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 {
		t.Fatalf("got %d rows, want 1", len(stats))
	}
	if stats[0].BlockRead != nil || stats[0].BlockWrite != nil {
		t.Errorf("an unaccounted counter came back as %v/%v, want unknown",
			stats[0].BlockRead, stats[0].BlockWrite)
	}
	if stats[0].NetRxBytes == nil {
		t.Error("the counters the host does account for must still be reported")
	}
}

func TestStatsSamplesOnlyTheProjectsContainers(t *testing.T) {
	r, runner := newRuntime()
	runner.OnOutput("compose --project-name demo", psRunning)
	runner.OnOutput("stats --no-stream", `{"Name":"demo-app-1","CPUPerc":"0%","MemUsage":"1MiB / 2MiB","NetIO":"0B / 0B","BlockIO":"0B / 0B"}`)

	if _, err := r.Stats(context.Background(), cfg()); err != nil {
		t.Fatal(err)
	}

	line := ""
	for _, c := range runner.Calls() {
		if joined := strings.Join(c.Argv, " "); strings.Contains(joined, "stats") {
			line = joined
		}
	}
	// Named containers, always. `docker stats` with no argument samples
	// every container on the host, which on a machine holding two
	// installations reports the neighbour's load as this deployment's.
	for _, want := range []string{"demo-app-1", "demo-app-2", "demo-db-1"} {
		if !strings.Contains(line, want) {
			t.Errorf("the sample did not name %s: %s", want, line)
		}
	}
	// The streaming form emits a first sample of zeros before its first
	// interval, and a `stats` that printed 0% CPU would be reporting an
	// idle machine that is on fire.
	if !strings.Contains(line, "--no-stream") {
		t.Errorf("the sample was not bounded with --no-stream: %s", line)
	}
}

func TestStatsAsksTheDaemonNothingWhenNothingIsRunning(t *testing.T) {
	r, runner := newRuntime()
	runner.OnOutput("compose --project-name demo", "")

	stats, err := r.Stats(context.Background(), cfg())
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 0 {
		t.Errorf("got %+v, want nothing", stats)
	}
	// Not merely an empty answer: `docker stats` with no container names
	// would sample the whole host, so the call must not happen at all.
	if runner.Ran("stats") {
		t.Error("a project with nothing running still sampled the host")
	}
}

func TestStatsRefusesAMemoryFigureItCannotRead(t *testing.T) {
	r, runner := newRuntime()
	runner.OnOutput("compose --project-name demo", `{"Name":"demo-app-1","Service":"app","State":"running"}`)
	// Unlike the IO counters, there is no honest zero for memory: every
	// container runtime accounts for it, so a cell that will not parse is a
	// format this adapter no longer understands -- and 0 B would read as a
	// container using nothing.
	runner.OnOutput("stats --no-stream",
		`{"Name":"demo-app-1","CPUPerc":"1.00%","MemUsage":"-- / --","NetIO":"0B / 0B","BlockIO":"0B / 0B"}`)

	if _, err := r.Stats(context.Background(), cfg()); err == nil {
		t.Fatal("a sample with no memory figure was accepted")
	}
}

func TestStatsReadsTheArrayFormToo(t *testing.T) {
	r, runner := newRuntime()
	runner.OnOutput("compose --project-name demo", `[{"Name":"demo-app-1","Service":"app","State":"running"}]`)
	// Both shapes, for the same reason `ps` reads both: Compose and the
	// docker CLI have each emitted an array and newline-delimited objects
	// across versions, and supporting both is cheaper than pinning one.
	runner.OnOutput("stats --no-stream",
		`[{"Name":"demo-app-1","CPUPerc":"1.00%","MemUsage":"8MiB / 8GiB","NetIO":"1kB / 2kB","BlockIO":"0B / 0B"}]`)

	stats, err := r.Stats(context.Background(), cfg())
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].Service != "app" {
		t.Errorf("got %+v", stats)
	}
}

func TestStatusCarriesTheContainerBesideTheService(t *testing.T) {
	r, runner := newRuntime()
	runner.OnOutput("ps --all", psRunning)

	states, err := r.Status(context.Background(), cfg())
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 3 {
		t.Fatalf("got %d states, want 3", len(states))
	}
	// Two rows for one service, told apart by their containers. Without
	// this a scaled service is two identical rows, and a structured log
	// line has nothing to attribute itself to.
	if states[0].Name != "app" || states[0].Container != "demo-app-1" {
		t.Errorf("the first state is %+v", states[0])
	}
	if states[1].Container != "demo-app-2" {
		t.Errorf("the second replica reports container %q", states[1].Container)
	}
}
