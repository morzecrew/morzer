package compose

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// statsEntry is the shape `docker stats --format json` emits, one object per
// line.
//
// Every figure arrives as a *formatted* string -- "1.234MiB / 7.628GiB",
// "12.34%" -- because that format was designed for a terminal. Parsing it back
// is unpleasant and unavoidable: there is no numeric form of `docker stats`,
// and the alternative is reading cgroup files, which is wrong under a rootless
// daemon and impossible against a remote one.
type statsEntry struct {
	Name     string `json:"Name"`
	ID       string `json:"ID"`
	CPUPerc  string `json:"CPUPerc"`
	MemUsage string `json:"MemUsage"`
	NetIO    string `json:"NetIO"`
	BlockIO  string `json:"BlockIO"`
}

// Stats samples resource use for the project's running containers.
//
// Two calls rather than one: `docker stats` names containers and knows nothing
// about services, so the project's containers are listed first and the sample
// is scoped to exactly those names. Passing no name would sample every
// container on the host, which for a machine holding two installations means
// reporting the neighbour's load as this deployment's.
func (r *Runtime) Stats(ctx context.Context, cfg ports.RuntimeConfig) ([]ports.ServiceStats, error) {
	running, err := r.runningContainers(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if len(running) == 0 {
		// Nothing running is a real and complete answer, and it costs no
		// subprocess. `docker stats` with no arguments would sample the
		// whole host instead, which is the one thing this must not do.
		return nil, nil
	}

	names := make([]string, 0, len(running))
	for name := range running {
		names = append(names, name)
	}
	slices.Sort(names)

	// `--no-stream` matters: the streaming form emits a first sample of
	// zeros before its first interval has elapsed, and a `stats` that
	// printed 0% CPU would be reporting an idle machine that is on fire.
	argv := append([]string{r.docker, "stats", "--no-stream", "--format", "json"}, names...)
	cmd := r.command(cfg, 60*time.Second, argv...)
	// A sample is data, not progress: streaming it into the live view would
	// flood it with numbers nobody is reading there.
	cmd.OnLine = nil

	res, err := r.runner.Run(ctx, cmd)
	if err != nil {
		return nil, wrapExit(err, "cannot read container statistics",
			"check that the Docker daemon is running: `docker info`")
	}
	return parseStats(res.Stdout, running)
}

// runningContainers maps this project's container names to their services.
//
// Without `--all`, so it is the running ones: a stopped container has no
// resource use to report, and `docker stats` on one blocks or reports zeros
// depending on the daemon's version.
func (r *Runtime) runningContainers(ctx context.Context, cfg ports.RuntimeConfig) (map[string]string, error) {
	cmd := r.command(cfg, 60*time.Second, r.args(cfg, "ps", "--format", "json")...)
	cmd.OnLine = nil

	res, err := r.runner.Run(ctx, cmd)
	if err != nil {
		return nil, wrapExit(err, "cannot list the project's containers",
			"check that the Docker daemon is running: `docker info`")
	}

	entries, err := parsePSEntries(res.Stdout)
	if err != nil {
		return nil, err
	}

	out := make(map[string]string, len(entries))
	for _, e := range entries {
		if e.Name == "" {
			continue
		}
		out[e.Name] = e.Service
	}
	return out, nil
}

// parseStats turns the sample into rows, attributing each container to the
// service that owns it.
func parseStats(raw string, services map[string]string) ([]ports.ServiceStats, error) {
	entries, err := decodeJSONLines[statsEntry](raw, "docker stats")
	if err != nil {
		return nil, err
	}

	out := make([]ports.ServiceStats, 0, len(entries))
	for _, e := range entries {
		row := ports.ServiceStats{
			Service:   services[e.Name],
			Container: e.Name,
			Replica:   replicaOf(e.Name),
		}
		if row.Container == "" {
			row.Container = e.ID
		}

		cpu, err := parsePercent(e.CPUPerc)
		if err != nil {
			return nil, err
		}
		row.CPUPercent = cpu

		used, limit, err := parsePair(e.MemUsage)
		if err != nil {
			return nil, err
		}
		// Memory is the one figure with no "not reported" form: every
		// container runtime accounts for it, so a cell that will not
		// parse is a format this adapter no longer understands, and
		// reporting zero would be the reading that looks like an idle
		// container.
		if used == nil {
			return nil, domain.RuntimeError(nil,
				"docker stats reported no memory figure for %s", row.Container)
		}
		row.MemoryBytes = *used
		if limit != nil {
			row.MemoryLimit = *limit
		}

		if row.NetRxBytes, row.NetTxBytes, err = parsePair(e.NetIO); err != nil {
			return nil, err
		}
		if row.BlockRead, row.BlockWrite, err = parsePair(e.BlockIO); err != nil {
			return nil, err
		}
		out = append(out, row)
	}

	// Sorted by service then container, so two samples of an unchanged
	// deployment are the same table: `--watch` redraws this, and rows that
	// swapped places between frames would be unreadable.
	sortStats(out)
	return out, nil
}

// replicaOf reads the instance number out of a Compose container name.
//
// Compose names containers `<project>-<service>-<n>`, so the trailing number is
// the replica. A name that does not end in one -- a service declaring
// `container_name:` -- reports 0, which the port documents as "the runtime does
// not name one" rather than as replica zero.
func replicaOf(container string) int {
	i := strings.LastIndexByte(container, '-')
	if i < 0 || i == len(container)-1 {
		return 0
	}
	n, err := strconv.Atoi(container[i+1:])
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// parsePercent reads "12.34%".
func parsePercent(s string) (float64, error) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%"))
	if s == "" || s == "--" {
		// Unlike the IO counters, there is no honest zero for a missing
		// CPU reading: 0% is what an idle container reports.
		return 0, domain.RuntimeError(nil, "docker stats reported no CPU figure")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, domain.RuntimeError(err, "cannot read the CPU figure %q from docker stats", s)
	}
	return v, nil
}

// parsePair reads one of the "A / B" cells: memory used and limit, bytes in and
// out, blocks read and written.
//
// Either half may be `--`, which is what a host without block-IO accounting
// reports -- rootless daemons and cgroup v2 without the io controller delegated,
// which is an ordinary configuration rather than a fault. That half comes back
// nil, and nil travels all the way to the operator's terminal as a dash: zero is
// a real reading, and a container that has written nothing is a different fact
// from a host that cannot say.
func parsePair(s string) (*int64, *int64, error) {
	left, right, found := strings.Cut(s, "/")
	if !found {
		// A single value, not a pair. Read as the first half so a
		// runtime that reports only one figure is not thrown away.
		left, right = s, ""
	}

	first, err := parseSize(left)
	if err != nil {
		return nil, nil, err
	}
	second, err := parseSize(right)
	if err != nil {
		return nil, nil, err
	}
	return first, second, nil
}

// sizeUnits are the suffixes Docker's own formatters produce.
//
// Both families, because `docker stats` uses both in one line: memory goes
// through units.BytesSize (binary, KiB) and the IO counters through
// units.HumanSize (decimal, kB). Reading a kB as 1024 would overstate every
// network figure by 2.4%, which is the kind of wrong that never looks wrong.
var sizeUnits = []struct {
	suffix string
	scale  float64
}{
	{"kib", 1 << 10}, {"mib", 1 << 20}, {"gib", 1 << 30},
	{"tib", 1 << 40}, {"pib", 1 << 50},
	{"kb", 1e3}, {"mb", 1e6}, {"gb", 1e9}, {"tb", 1e12}, {"pb", 1e15},
	{"b", 1},
}

// parseSize reads one formatted byte figure, or nil when the runtime reported
// none.
func parseSize(s string) (*int64, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || s == "--" || s == "-" || s == "n/a" {
		return nil, nil
	}

	for _, u := range sizeUnits {
		digits, ok := strings.CutSuffix(s, u.suffix)
		if !ok {
			continue
		}
		digits = strings.TrimSpace(digits)
		v, err := strconv.ParseFloat(digits, 64)
		if err != nil {
			return nil, domain.RuntimeError(err,
				"cannot read the size %q from docker stats", s)
		}
		// Rounded rather than truncated: 0.9 KiB is 922 bytes, and a
		// truncating read reports 921 for every figure Docker rounded up
		// when it formatted it.
		n := int64(v*u.scale + 0.5)
		return &n, nil
	}

	// A bare number with no unit -- not a form Docker emits, but reading it
	// as bytes is the only interpretation that could be right.
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, domain.RuntimeError(err, "cannot read the size %q from docker stats", s)
	}
	n := int64(v)
	return &n, nil
}

// sortStats orders rows by service then container.
//
// So that two samples of an unchanged deployment are the same table: `--watch`
// redraws this, and rows that swapped places between frames would be
// unreadable.
func sortStats(rows []ports.ServiceStats) {
	slices.SortFunc(rows, func(a, b ports.ServiceStats) int {
		if c := strings.Compare(a.Service, b.Service); c != 0 {
			return c
		}
		return strings.Compare(a.Container, b.Container)
	})
}
