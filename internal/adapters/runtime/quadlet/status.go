package quadlet

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// unitState is what `systemctl show` says about one unit.
type unitState struct {
	active, sub, result string
	exitStatus          int
}

// unitStates reads the units' states in one call. systemctl prints one block
// a unit, in the order asked, separated by a blank line.
func (r *Runtime) unitStates(ctx context.Context, units []string) ([]unitState, error) {
	args := append([]string{"show", "--property", "ActiveState,SubState,Result,ExecMainStatus"}, units...)
	res, err := r.systemctlUser(ctx, time.Minute, args...)
	if err != nil {
		return nil, wrapExit(err, "cannot read the units' state", "check that the user's systemd manager is running")
	}
	out := make([]unitState, 0, len(units))
	cur := unitState{}
	flush := func() {
		out = append(out, cur)
		cur = unitState{}
	}
	seen := false
	for _, line := range strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			if seen {
				flush()
				seen = false
			}
			continue
		}
		key, value, _ := strings.Cut(line, "=")
		seen = true
		switch key {
		case "ActiveState":
			cur.active = value
		case "SubState":
			cur.sub = value
		case "Result":
			cur.result = value
		case "ExecMainStatus":
			cur.exitStatus, _ = strconv.Atoi(value)
		}
	}
	if seen {
		flush()
	}
	if len(out) != len(units) {
		return nil, domain.RuntimeError(nil, "`systemctl show` described %d units, not %d", len(out), len(units))
	}
	return out, nil
}

// Status reports every installed service Up starts. A service whose container
// exists reports the container's state; one with none, as after a stop,
// reports its unit's, so a stopped service is still listed and reads as
// exited, which the backup engine takes as not holding its volume. Nothing
// installed is nothing to report, before the first Up included.
func (r *Runtime) Status(ctx context.Context, cfg ports.RuntimeConfig) ([]ports.ServiceState, error) {
	dir, err := r.unitDir(cfg.Product)
	if err != nil {
		return nil, err
	}
	b, err := installed(dir, cfg.Product)
	if err != nil {
		return nil, err
	}
	services := b.started()
	if len(services) == 0 {
		return nil, nil
	}
	var units, names []string
	for _, s := range services {
		u, _ := b.service(s)
		units = append(units, u.unitName())
		names = append(names, b.containerName(u))
	}
	states, err := r.unitStates(ctx, units)
	if err != nil {
		return nil, err
	}
	containers, err := r.inspect(ctx, names)
	if err != nil {
		return nil, err
	}

	out := make([]ports.ServiceState, 0, len(services))
	for i, s := range services {
		u, _ := b.service(s)
		image, _ := u.last("Container", "Image")
		st := ports.ServiceState{Name: s, Container: names[i], Image: image, Health: ports.HealthNone}
		if _, ok := u.last("Container", "HealthCmd"); ok {
			st.Health = ports.HealthUnknown
		}
		if c, ok := containers[names[i]]; ok {
			st.State = strings.ToLower(c.State.Status)
			st.ExitCode = c.State.ExitCode
			if c.Image != "" {
				st.Image = c.Image
			}
			if c.State.Health != nil {
				switch c.State.Health.Status {
				case "healthy":
					st.Health = ports.HealthHealthy
				case "unhealthy":
					st.Health = ports.HealthUnhealthy
				case "starting":
					st.Health = ports.HealthStarting
				}
			}
		} else {
			st.State, st.ExitCode = stateOfUnit(states[i])
		}
		st.Status = states[i].active + " (" + states[i].sub + ")"
		out = append(out, st)
	}
	return out, nil
}

// stateOfUnit maps a unit with no container onto the container states the
// port reasons about. Inactive and failed both mean nothing runs: exited.
// A unit on its way up or down still may, and is reported as restarting or
// removing, which a restore reads as occupying the volume.
func stateOfUnit(u unitState) (string, int) {
	switch u.active {
	case "inactive", "failed":
		return ports.StateExited, u.exitStatus
	case "deactivating":
		return ports.StateRemoving, 0
	case "activating", "reloading":
		return ports.StateRestarting, 0
	default:
		return ports.StateCreated, 0
	}
}

// containerStats is the part of `podman stats` the adapter reads: the numbers,
// not the display strings.
type containerStats struct {
	Name        string  `json:"Name"`
	CPU         float64 `json:"CPU"`
	MemUsage    int64   `json:"MemUsage"`
	MemLimit    int64   `json:"MemLimit"`
	BlockInput  int64   `json:"BlockInput"`
	BlockOutput int64   `json:"BlockOutput"`
	Network     map[string]struct {
		RxBytes int64 `json:"RxBytes"`
		TxBytes int64 `json:"TxBytes"`
	} `json:"Network"`
}

// Stats samples each running container once.
//
// Block IO is reported only when the user's cgroup has the io controller.
// Without it Podman reports zero, which reads as a container that wrote
// nothing rather than a host that cannot say.
func (r *Runtime) Stats(ctx context.Context, cfg ports.RuntimeConfig) ([]ports.ServiceStats, error) {
	states, err := r.Status(ctx, cfg)
	if err != nil {
		return nil, err
	}
	service := map[string]string{}
	var running []string
	for _, s := range states {
		if s.State == ports.StateRunning {
			running = append(running, s.Container)
			service[s.Container] = s.Name
		}
	}
	if len(running) == 0 {
		return nil, nil
	}
	cmd := r.command(time.Minute, append([]string{r.podman, "stats", "--no-stream", "--format", "{{json .ContainerStats}}"}, running...)...)
	cmd.OnLine = nil
	res, err := r.runner.Run(ctx, cmd)
	if err != nil {
		return nil, wrapExit(err, "cannot sample the containers' resource use", "")
	}
	io := r.accountsIO(ctx)
	var out []ports.ServiceStats
	sc := bufio.NewScanner(strings.NewReader(res.Stdout))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var c containerStats
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, domain.RuntimeError(err, "cannot read `podman stats`")
		}
		row := ports.ServiceStats{
			Service: service[c.Name], Container: c.Name,
			CPUPercent: c.CPU, MemoryBytes: c.MemUsage, MemoryLimit: c.MemLimit,
		}
		if len(c.Network) > 0 {
			var rx, tx int64
			for _, n := range c.Network {
				rx += n.RxBytes
				tx += n.TxBytes
			}
			row.NetRxBytes, row.NetTxBytes = &rx, &tx
		}
		if io {
			read, write := c.BlockInput, c.BlockOutput
			row.BlockRead, row.BlockWrite = &read, &write
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Container < out[j].Container })
	return out, nil
}

// accountsIO reports whether Podman can account for block IO here.
func (r *Runtime) accountsIO(ctx context.Context) bool {
	cmd := r.command(30*time.Second, r.podman, "info", "--format", "{{range .Host.CgroupControllers}}{{.}} {{end}}")
	cmd.OnLine = nil
	res, err := r.runner.Run(ctx, cmd)
	return err == nil && strings.Contains(" "+res.Stdout+" ", " io ")
}

// journalEntry is the part of a journal record the adapter reads. MESSAGE is
// a string, or an array of bytes when it is not valid UTF-8.
type journalEntry struct {
	Message   json.RawMessage `json:"MESSAGE"`
	Container string          `json:"CONTAINER_NAME"`
	Realtime  string          `json:"__REALTIME_TIMESTAMP"`
}

func (e journalEntry) text() string {
	var s string
	if json.Unmarshal(e.Message, &s) == nil {
		return s
	}
	var nums []int
	if json.Unmarshal(e.Message, &nums) != nil {
		return ""
	}
	b := make([]byte, 0, len(nums))
	for _, n := range nums {
		if n < 0 || n > 255 {
			return ""
		}
		b = append(b, byte(n))
	}
	return string(b)
}

// Logs streams what the services' containers wrote, from the user's journal,
// framed as the port fixes: `<container> | [<instant> ]<text>`. Podman's own
// lines about a container, and systemd's about its unit, carry no container
// name and are left out, as Compose leaves out its own.
//
// The journal is read for the services' container names rather than their
// units, so Tail counts container lines and not the unit's own, and with
// `--all`, so a line longer than 4096 bytes is not replaced by a null. Tail
// counts the merged stream, not each container, because journalctl reads them
// as one journal. With no service installed, as after a reboot before the
// first apply, there is nothing to read, and an unfiltered journal would be
// every other service's.
func (r *Runtime) Logs(ctx context.Context, cfg ports.RuntimeConfig, opts ports.LogOptions) (io.ReadCloser, error) {
	dir, err := r.unitDir(cfg.Product)
	if err != nil {
		return nil, err
	}
	b, err := installed(dir, cfg.Product)
	if err != nil {
		return nil, err
	}
	services := opts.Services
	if len(services) == 0 {
		services = b.started()
	}
	if len(services) == 0 {
		return io.NopCloser(strings.NewReader("")), nil
	}
	args := []string{r.journalctl, "--user", "--output", "json", "--all", "--no-pager", "--quiet"}
	for _, s := range services {
		u, ok := b.service(s)
		if !ok {
			return nil, domain.ValidationError(nil, "no installed Quadlet file defines the service %q", s)
		}
		// Matches on one field are alternatives.
		args = append(args, "CONTAINER_NAME="+b.containerName(u))
	}
	if opts.Tail > 0 {
		args = append(args, "--lines", strconv.Itoa(opts.Tail))
	}
	if !opts.Since.IsZero() {
		args = append(args, "--since", opts.Since.UTC().Format("2006-01-02 15:04:05")+" UTC")
	}
	if opts.Follow {
		args = append(args, "--follow")
	}
	return r.stream(ctx, args, func(line string) (string, bool) {
		var e journalEntry
		if json.Unmarshal([]byte(line), &e) != nil || e.Container == "" {
			return "", false
		}
		text := strings.TrimRight(e.text(), "\n")
		if opts.Timestamps {
			if us, err := strconv.ParseInt(e.Realtime, 10, 64); err == nil {
				text = time.UnixMicro(us).UTC().Format(time.RFC3339Nano) + " " + text
			}
		}
		return e.Container + " | " + text, true
	})
}

// stream runs a command and returns its output, each line passed through
// frame, as a stream the caller closes. Closing cancels the command, which the
// runner stops, so a followed journal does not outlive its reader.
func (r *Runtime) stream(ctx context.Context, argv []string, frame func(string) (string, bool)) (io.ReadCloser, error) {
	ctx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	lines := &lineWriter{frame: frame, out: pw}
	go func() {
		_, err := r.runner.Run(ctx, exec.Command{Argv: argv, Stdout: lines, Redact: r.redact})
		lines.flush()
		if ctx.Err() != nil {
			err = nil
		}
		if err != nil {
			err = wrapExit(err, "cannot read the services' journal", "run `journalctl --user` to see why")
		}
		_ = pw.CloseWithError(err)
	}()
	return &cancelReader{PipeReader: pr, cancel: cancel}, nil
}

type cancelReader struct {
	*io.PipeReader
	cancel context.CancelFunc
}

func (c *cancelReader) Close() error {
	c.cancel()
	return c.PipeReader.Close()
}

// lineWriter splits what it is given into lines and writes each framed one.
type lineWriter struct {
	frame   func(string) (string, bool)
	out     io.Writer
	partial []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.partial = append(w.partial, p...)
	for {
		i := strings.IndexByte(string(w.partial), '\n')
		if i < 0 {
			return len(p), nil
		}
		line := string(w.partial[:i])
		w.partial = w.partial[i+1:]
		if framed, ok := w.frame(line); ok {
			if _, err := io.WriteString(w.out, framed+"\n"); err != nil {
				return 0, err
			}
		}
	}
}

func (w *lineWriter) flush() {
	if len(w.partial) > 0 {
		if framed, ok := w.frame(string(w.partial)); ok {
			_, _ = io.WriteString(w.out, framed+"\n")
		}
		w.partial = nil
	}
}
