package ops

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/logging"
	"github.com/morzecrew/morzer/internal/lifecycle/preflight"
	"github.com/morzecrew/morzer/internal/ports"
)

// The two files an installation writes into the node exporter's textfile
// directory (RFC 0047 decision 14). The role comes first, so no product's file
// can carry another product's name.
const (
	metricsRoleStatus = "status"
	metricsRoleDoctor = "doctor"
)

// MetricsFileName is the file a role's metrics are written to.
func MetricsFileName(role, product string) string {
	return "morzer-" + role + "-" + product + ".prom"
}

// MetricsOptions is `morzer metrics`.
type MetricsOptions struct {
	// Textfile is the directory to write the status file into. Empty prints
	// the text instead. The unit passes it, so a run whose state does not
	// load still knows where to say so (decision 15).
	Textfile string
}

// MetricsReport is what `morzer metrics` produced.
type MetricsReport struct {
	// Text is the exposition, when it was printed rather than written.
	Text string `json:"text,omitempty"`
	// Path is the file written, when it was.
	Path string `json:"path,omitempty"`
	// Up is whether the installation's state was read.
	Up bool `json:"up"`
}

// Metrics renders what `status` knows in the Prometheus text format, and
// writes it to the textfile directory when asked (RFC 0047 §5.1).
//
// A state that does not load is not an error here: `morzer_up 0` is the answer
// the export exists to give, and the reason goes to the log. A file that cannot
// be written is one, so the unit fails and the file goes stale.
func Metrics(ctx context.Context, d *Deps, opts MetricsOptions) (MetricsReport, error) {
	text, up := d.statusMetrics(ctx)
	out := MetricsReport{Up: up}
	if opts.Textfile == "" {
		out.Text = text
		return out, nil
	}
	path := filepath.Join(opts.Textfile, MetricsFileName(metricsRoleStatus, d.Paths.Product))
	if err := writeMetricsFile(path, text); err != nil {
		return out, err
	}
	out.Path = path
	return out, nil
}

// statusMetrics collects and renders the status file, and says whether the
// installation's state was read.
func (d *Deps) statusMetrics(ctx context.Context) (string, bool) {
	product := d.Paths.Product
	m := newExposition(product)
	up := m.family("morzer_up", "1 when the installation's state was read.")

	inst, err := d.loadInstallation(ctx)
	if err != nil {
		logging.FromContext(ctx).Error("cannot read the installation for its metrics", "error", err)
		up.sample(0)
		return m.String(), false
	}
	up.sample(1)

	mode := string(inst.Mode)
	if mode == "" {
		mode = "production"
	}
	current, currentErr := d.State.CurrentRelease(ctx)
	if currentErr == nil && !current.IsZero() {
		m.family("morzer_release_info", "The installed release, as labels; always 1.").
			sample(1, "version", current.Version.String(), "mode", mode)
	}
	m.family("morzer_manager_info", "The manager binary that wrote this file, as labels; always 1.").
		sample(1, "version", d.ManagerVersion.String())

	if currentErr == nil && !current.IsZero() {
		d.runtimeMetrics(ctx, m, inst, current)
	}

	m.family("morzer_stopped", "1 while the product is stopped by `morzer stop`.").
		sample(boolValue(inst.Stopped != nil))

	if unfinished, err := d.State.UnfinishedOperations(ctx); err == nil {
		attention := 0
		for _, rec := range unfinished {
			if rec.Status.NeedsAttention() {
				attention++
			}
		}
		m.family("morzer_operations_needing_attention",
			"Operations flagged for a human; `morzer status --clear-intervention` acknowledges one.").
			sample(float64(attention))
	}

	d.lastOperationMetrics(ctx, m)

	if d.Backup != nil {
		if backups, err := d.Backup.List(ctx); err == nil && len(backups) > 0 {
			m.family("morzer_last_backup_timestamp_seconds", "When the newest backup was taken.").
				sample(unixSeconds(backups[0].At.Time))
		}
	}

	if candidate, err := d.State.UpdateCandidate(ctx); err == nil {
		m.family("morzer_update_staged", "1 while a release waits for approval.").
			sample(boolValue(candidate.IsStaged()))
	}
	return m.String(), true
}

// runtimeMetrics adds the runtime's families, or none when there is nothing to
// ask about, and `morzer_runtime_up 0` when it could not answer (decision 9).
func (d *Deps) runtimeMetrics(ctx context.Context, m *exposition, inst domain.Installation, current domain.ReleaseRecord) {
	if d.Runtime == nil {
		return
	}
	rel, err := d.resolveCurrentRelease(ctx, current)
	if err != nil {
		m.family("morzer_runtime_up", "1 when the runtime answered.").sample(0)
		return
	}
	// Nothing applied yet: the runtime has no project to describe.
	if d.neverApplied(ctx) {
		return
	}
	read := d.readRuntime(ctx, inst, rel)
	runtimeUp := m.family("morzer_runtime_up", "1 when the runtime answered.")
	if read.err != nil {
		runtimeUp.sample(0)
		return
	}
	runtimeUp.sample(1)

	// A finished job behind an inactive profile is not a service that is
	// down (decision 19). Unknown when the configuration cannot be read,
	// and then nothing is left out.
	var configured []string
	if rendered, err := d.Runtime.Validate(ctx, read.cfg); err == nil {
		configured = rendered.Services
	}
	running := m.family("morzer_service_running", "Containers of the service that are running and not unhealthy.")
	containers := m.family("morzer_service_containers", "Containers of the service.")
	counts := map[string][2]int{}
	var order []string
	for _, s := range read.services {
		if completedJob(s, configured) {
			continue
		}
		c, seen := counts[s.Name]
		if !seen {
			order = append(order, s.Name)
		}
		c[1]++
		if s.Running() {
			c[0]++
		}
		counts[s.Name] = c
	}
	for _, name := range order {
		running.sample(float64(counts[name][0]), "service", name)
		containers.sample(float64(counts[name][1]), "service", name)
	}

	health := m.family("morzer_health_check_ok", "1 when the release's health probe passed.")
	for _, h := range read.health {
		health.sample(boolValue(h.OK), "check", h.Name)
	}
}

// completedJob is a container of a service the configuration does not start,
// one behind an inactive profile, that exited 0: a dependency a task or a
// migration started and that finished (RFC 0047 decision 19). With the
// configuration unknown, nothing is.
func completedJob(s ports.ServiceState, configured []string) bool {
	return configured != nil && s.State == "exited" && s.ExitCode == 0 &&
		!slices.Contains(configured, s.Name)
}

// lastOperationMetrics adds, for each operation type the journal holds, the
// newest record that finished: when, and whether it succeeded.
func (d *Deps) lastOperationMetrics(ctx context.Context, m *exposition) {
	records, err := d.State.Operations(ctx, ports.Filter{})
	if err != nil {
		return
	}
	at := m.family("morzer_last_operation_timestamp_seconds", "When the newest finished operation of the type ended.")
	ok := m.family("morzer_last_operation_success",
		"1 when that operation succeeded; a compensated or interrupted one is 0.")
	seen := map[domain.OperationType]bool{}
	for _, rec := range records {
		if !rec.Status.Terminal() || seen[rec.Type] {
			continue
		}
		seen[rec.Type] = true
		end := rec.FinishedAt.Time
		if end.IsZero() {
			end = rec.StartedAt.Time
		}
		at.sample(unixSeconds(end), "type", string(rec.Type))
		ok.sample(boolValue(rec.Status == domain.StatusSucceeded), "type", string(rec.Type))
	}
}

// metricsStaleAfter is how old the status file may get before `doctor` says
// nothing is writing it: ten one-minute ticks (decision 10).
const metricsStaleAfter = 10 * time.Minute

// checkMetricsTextfile is `metrics.textfile`: whether the export this
// installation asked for is being written (decision 10). It writes nothing
// itself; access(2) answers whether the directory is writable.
func (d *Deps) checkMetricsTextfile(inst domain.Installation) preflight.Check {
	return preflight.Check{
		ID:          "metrics.textfile",
		Category:    preflight.CategoryConfig,
		Description: "the metrics export is being written",
		Fatal:       false,
		Run: func(ctx context.Context) events.CheckResult {
			dir := inst.Policy.MetricsTextfile
			if dir == "" {
				return preflight.OK("not exporting metrics")
			}
			if info, err := os.Stat(dir); err != nil || !info.IsDir() {
				return preflight.Warn("create it, or stop exporting with `morzer config unset metrics.textfile`",
					"the metrics directory %s is missing", dir)
			}
			if err := unix.Access(dir, unix.W_OK); err != nil {
				return preflight.Warn("let the user the units run as write it",
					"the metrics directory %s is not writable", dir)
			}
			path := filepath.Join(dir, MetricsFileName(metricsRoleStatus, inst.Product))
			info, err := os.Stat(path)
			if err != nil {
				return preflight.Warn(fmt.Sprintf("check the timer: `systemctl status %s-metrics.timer`", inst.Product),
					"nothing has written %s", path)
			}
			if age := time.Since(info.ModTime()); age > metricsStaleAfter {
				return preflight.Warn(fmt.Sprintf("check the timer: `systemctl status %s-metrics.timer`", inst.Product),
					"%s was last written %s ago", path, age.Round(time.Second))
			}
			return preflight.OK("written into %s", dir)
		},
	}
}

// doctorMetrics renders a scheduled doctor run (RFC 0047 §5.3, decision 4).
func doctorMetrics(product string, results []events.CheckResult, at time.Time) string {
	m := newExposition(product)
	checks := m.family("morzer_doctor_check", "The scheduled doctor's verdict on the check: 0 ok, 1 warn, 2 fail.")
	for _, r := range results {
		value := 0.0
		switch r.Status {
		case events.CheckWarn:
			value = 1
		case events.CheckFail:
			value = 2
		}
		checks.sample(value, "check", r.ID, "category", r.Category)
	}
	m.family("morzer_doctor_last_run_timestamp_seconds", "When the scheduled doctor last ran.").
		sample(unixSeconds(at))
	return m.String()
}

// writeDoctorMetrics writes the doctor file after a scheduled run, when the
// installation exports metrics. A write that fails is logged: the run's
// verdict is its checks, and `metrics.textfile` reports the stale file.
func (d *Deps) writeDoctorMetrics(ctx context.Context, inst domain.Installation, results []events.CheckResult) {
	dir := inst.Policy.MetricsTextfile
	if dir == "" {
		return
	}
	path := filepath.Join(dir, MetricsFileName(metricsRoleDoctor, inst.Product))
	if err := writeMetricsFile(path, doctorMetrics(inst.Product, results, d.now())); err != nil {
		logging.FromContext(ctx).Warn("cannot write the doctor's metrics", "path", path, "error", err)
	}
}

// retireMetricsFiles removes what an installation stopped writing, so a scrape
// never reads a file nothing updates (decisions 16 and 18): both files when the
// directory changes or goes, and the doctor file when the doctor timer does.
func (d *Deps) retireMetricsFiles(ctx context.Context, prior, next domain.Installation) {
	if prior.Policy.MetricsTextfile == "" {
		return
	}
	var stale []string
	if next.Policy.MetricsTextfile != prior.Policy.MetricsTextfile {
		stale = append(stale, metricsRoleStatus)
	}
	wroteDoctor := !prior.Policy.SkipScheduledDoctor
	writesDoctor := !next.Policy.SkipScheduledDoctor && next.Policy.MetricsTextfile == prior.Policy.MetricsTextfile
	if wroteDoctor && !writesDoctor {
		stale = append(stale, metricsRoleDoctor)
	}
	removeMetricsFiles(ctx, prior.Policy.MetricsTextfile, prior.Product, stale...)
}

// removeMetricsFiles removes the named roles' files from dir; a file already
// gone is not a failure.
func removeMetricsFiles(ctx context.Context, dir, product string, roles ...string) {
	for _, role := range roles {
		path := filepath.Join(dir, MetricsFileName(role, product))
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			logging.FromContext(ctx).Warn("cannot remove a metrics file nothing writes any more", "path", path, "error", err)
		}
	}
}

// writeMetricsFile replaces path with text atomically (decision 8). The
// temporary file is created exclusively in the same directory under a name that
// does not end in `.prom`, so the collector never reads half a file, and the
// rename replaces a symlink at path rather than writing through it.
func writeMetricsFile(path, text string) error {
	dir, name := filepath.Split(path)
	tmp, err := os.CreateTemp(dir, "."+name+".")
	if err != nil {
		return domain.ValidationError(err, "cannot write into %s", dir).
			WithHint("check that the directory exists and that this user can write it")
	}
	done := false
	defer func() {
		if !done {
			_ = os.Remove(tmp.Name())
		}
	}()
	// The umask applied to CreateTemp's 0600; the node exporter's user
	// reads this file.
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return domain.Internal(err, "cannot set the mode of %s", tmp.Name())
	}
	if _, err := tmp.WriteString(text); err != nil {
		_ = tmp.Close()
		return domain.Internal(err, "cannot write %s", tmp.Name())
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return domain.Internal(err, "cannot write %s", tmp.Name())
	}
	if err := tmp.Close(); err != nil {
		return domain.Internal(err, "cannot write %s", tmp.Name())
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return domain.Internal(err, "cannot replace %s", path)
	}
	done = true
	return nil
}

// exposition builds the Prometheus text format, version 0.0.4: each family's
// HELP and TYPE once, its samples together, and a family with no samples left
// out entirely.
type exposition struct {
	product  string
	families []*metricFamily
}

type metricFamily struct {
	name, help string
	samples    []string
	product    string
}

func newExposition(product string) *exposition { return &exposition{product: product} }

func (e *exposition) family(name, help string) *metricFamily {
	f := &metricFamily{name: name, help: help, product: e.product}
	e.families = append(e.families, f)
	return f
}

// sample adds one series; labels are name, value pairs after `product`.
func (f *metricFamily) sample(value float64, labels ...string) {
	var b strings.Builder
	b.WriteString(f.name)
	b.WriteString(`{product="`)
	b.WriteString(escapeLabel(f.product))
	b.WriteByte('"')
	for i := 0; i+1 < len(labels); i += 2 {
		fmt.Fprintf(&b, `,%s="%s"`, labels[i], escapeLabel(labels[i+1]))
	}
	b.WriteString("} ")
	b.WriteString(strconv.FormatFloat(value, 'f', -1, 64))
	f.samples = append(f.samples, b.String())
}

func (e *exposition) String() string {
	var b strings.Builder
	for _, f := range e.families {
		if len(f.samples) == 0 {
			continue
		}
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n", f.name, escapeHelp(f.help), f.name)
		for _, s := range f.samples {
			b.WriteString(s)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// escapeLabel escapes a label value as the text format requires (decision 7).
func escapeLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

func escapeHelp(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(s)
}

func boolValue(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func unixSeconds(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }
