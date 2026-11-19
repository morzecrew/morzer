// Package launchd implements ports.Supervisor with a Mac's LaunchDaemons (RFC
// 0055 §5.6).
//
// One job per systemd service: the boot job, which runs `apply --startup` and
// is started again every 30 seconds until it succeeds, and one job per
// schedule, which carries its timer's calendar in its own plist
// (logs/macos-host.md). The property the boot job keeps from the systemd unit
// is that exit 12 ends the retries; launchd cannot be told so, so the manager
// records it and the next start exits 0 (internal/lifecycle/ops).
package launchd

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/adapters/supervisor/systemd"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// Name is the provider name.
const Name = "launchd"

// DaemonDir is where a Mac keeps the system jobs it loads at boot, as root.
const DaemonDir = "/Library/LaunchDaemons"

// NewsyslogDir is where newsyslog reads the logs it rotates beside its own.
const NewsyslogDir = "/etc/newsyslog.d"

// labelPrefix makes a job's label a reverse domain name, as launchd's are.
const labelPrefix = "dev.morzer."

// jobPath is every job's PATH. launchd gives a job /usr/bin:/bin:/usr/sbin:/sbin
// (S4), which leaves out /usr/local/bin, where the install page puts sops; the
// manager then keeps only the directories root alone can change (RFC 0055
// decision 25).
const jobPath = "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

// throttle is how long launchd waits before starting the boot job again, the
// systemd unit's RestartSec.
const throttle = 30

// rollbackBudget bounds the undo of a failed InstallUnits, which runs whether
// or not the caller's context is still live.
const rollbackBudget = time.Minute

type Supervisor struct {
	runner exec.Runner

	// product and logDir are the installation's, whose jobs this supervisor
	// writes: each job logs to logDir, which newsyslog then rotates.
	product string
	logDir  string

	// stamp is where a scheduled run records that it reached the engine,
	// which the catch-up reads.
	stamp func(job string) string

	daemonDir    string
	newsyslogDir string

	// launchctl and plutil by their paths: a job's PATH is not the manager's.
	launchctl string
	plutil    string

	isRoot func() bool

	// now, poll and startBudget are the catch-up's clock: when it is, how
	// often it asks launchd whether a job it started has exited, and how
	// long launchd has to start one.
	now         func() time.Time
	poll        time.Duration
	startBudget time.Duration
}

type Option func(*Supervisor)

// WithDaemonDir and WithNewsyslogDir relocate what this adapter writes, which
// is what makes it testable without root.
func WithDaemonDir(dir string) Option    { return func(s *Supervisor) { s.daemonDir = dir } }
func WithNewsyslogDir(dir string) Option { return func(s *Supervisor) { s.newsyslogDir = dir } }

func withRoot(isRoot func() bool) Option { return func(s *Supervisor) { s.isRoot = isRoot } }

func withClock(now func() time.Time, poll, startBudget time.Duration) Option {
	return func(s *Supervisor) { s.now, s.poll, s.startBudget = now, poll, startBudget }
}

// New returns the supervisor for the installation at paths.
func New(runner exec.Runner, paths domain.Paths, opts ...Option) *Supervisor {
	s := &Supervisor{
		runner:       runner,
		product:      paths.Product,
		logDir:       filepath.Join(paths.VarDir, "logs"),
		stamp:        paths.StampFile,
		daemonDir:    DaemonDir,
		newsyslogDir: NewsyslogDir,
		launchctl:    "/bin/launchctl",
		plutil:       "/usr/bin/plutil",
		isRoot:       func() bool { return os.Geteuid() == 0 },
		now:          time.Now,
		poll:         2 * time.Second,
		startBudget:  time.Minute,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

var (
	_ ports.Supervisor = (*Supervisor)(nil)
	_ ports.CatchUp    = (*Supervisor)(nil)
)

// Available is true where launchctl runs and the manager is root, since only
// root writes /Library/LaunchDaemons and loads a job into the system domain.
func (s *Supervisor) Available(context.Context) bool {
	if _, err := os.Stat(s.launchctl); err != nil {
		return false
	}
	return s.isRoot()
}

// Label and friends name a product's jobs.
func Label(product string) string        { return labelPrefix + product }
func BackupLabel(product string) string  { return Label(product) + "-backup" }
func UpdateLabel(product string) string  { return Label(product) + "-update" }
func FleetLabel(product string) string   { return Label(product) + "-fleet" }
func SyncLabel(product string) string    { return Label(product) + "-sync" }
func DoctorLabel(product string) string  { return Label(product) + "-doctor" }
func MetricsLabel(product string) string { return Label(product) + "-metrics" }

// Labels lists every job this supervisor may own for a product, whether or not
// this installation has it: the superset removal walks, as systemd.UnitNames
// is.
func Labels(product string) []string {
	return []string{
		Label(product), BackupLabel(product), UpdateLabel(product), FleetLabel(product),
		SyncLabel(product), DoctorLabel(product), MetricsLabel(product),
	}
}

// ManagedUnitNames lists the jobs this supervisor owns for a product.
func (s *Supervisor) ManagedUnitNames(product string) []string { return Labels(product) }

// job is one LaunchDaemon to render.
type job struct {
	label string
	args  []string

	// boot is the boot job: run when loaded, and again after every failure.
	boot bool

	// calendar is when a scheduled job runs, and every is the period of one
	// that runs on an interval instead.
	calendar []interval
	every    time.Duration
}

// Units renders the jobs for a product: those systemd.BuildUnits renders, one
// per service, each with its timer's schedule.
func (s *Supervisor) Units(p ports.UnitParams) ([]ports.Unit, error) {
	jobs, err := s.jobs(p)
	if err != nil {
		return nil, err
	}
	units := make([]ports.Unit, 0, len(jobs))
	for _, j := range jobs {
		units = append(units, ports.Unit{Name: j.label, Contents: s.plist(j), Enable: true})
	}
	return units, nil
}

func (s *Supervisor) jobs(p ports.UnitParams) ([]job, error) {
	// The schedules the systemd units would run, defaults included, so a Mac
	// and a Linux machine set alike run alike.
	schedule := func(field, expr, fallback string) ([]interval, error) {
		if strings.TrimSpace(expr) == "" {
			expr = fallback
		}
		times, err := calendar(expr)
		if err != nil {
			return nil, domain.Internal(refuseSchedule(expr, err), "the %s cannot be a launchd schedule", field)
		}
		return times, nil
	}
	command := func(args ...string) []string {
		argv := append([]string{p.ManagerPath}, args...)
		argv = append(argv, "--plain", "--log-format", "json")
		if p.ConfigPath != "" {
			argv = append(argv, "--config", p.ConfigPath)
		}
		return argv
	}

	jobs := []job{{label: Label(p.Product), args: command("apply", "--startup"), boot: true}}
	if !p.SkipBackupTimer {
		times, err := schedule("backup schedule", p.BackupSchedule, systemd.DefaultBackupSchedule)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job{label: BackupLabel(p.Product),
			args: command("backup", "--unattended", "--yes"), calendar: times})
	}
	if p.UpdateTimer {
		times, err := schedule("update schedule", p.UpdateSchedule, systemd.DefaultUpdateSchedule)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job{label: UpdateLabel(p.Product),
			args: command("update", "--unattended"), calendar: times})
	}
	if p.FleetTimer {
		times, err := schedule("fleet schedule", p.FleetSchedule, systemd.DefaultFleetSchedule)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job{label: FleetLabel(p.Product),
			args: command("fleet", "publish"), calendar: times})
	}
	if p.SyncTimer {
		every := p.SyncInterval
		if every <= 0 {
			every = domain.DefaultSyncInterval
		}
		jobs = append(jobs, job{label: SyncLabel(p.Product),
			args: command("sync", "now", "--unattended"), every: every})
	}
	if p.DoctorTimer {
		times, err := schedule("doctor schedule", p.DoctorSchedule, systemd.DefaultDoctorSchedule)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job{label: DoctorLabel(p.Product),
			args: command("doctor", "--unattended"), calendar: times})
	}
	if p.MetricsDir != "" {
		if err := domain.ValidateMetricsTextfile(p.MetricsDir); err != nil {
			return nil, domain.Internal(err, "the metrics directory cannot be written into a job")
		}
		jobs = append(jobs, job{label: MetricsLabel(p.Product),
			args: command("metrics", "--textfile", p.MetricsDir), calendar: []interval{{}}})
	}
	return jobs, nil
}

// plist renders a job. Its output goes to one log under the installation's
// VarDir, JSON as the manager writes to the journal on Linux.
func (s *Supervisor) plist(j job) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	str := func(indent, value string) {
		b.WriteString(indent + "<string>")
		_ = xml.EscapeText(&b, []byte(value)) // a bytes.Buffer does not fail
		b.WriteString("</string>\n")
	}
	key := func(indent, name string) { fmt.Fprintf(&b, "%s<key>%s</key>\n", indent, name) }

	key("\t", "Label")
	str("\t", j.label)
	key("\t", "ProgramArguments")
	b.WriteString("\t<array>\n")
	for _, arg := range j.args {
		str("\t\t", arg)
	}
	b.WriteString("\t</array>\n")
	key("\t", "EnvironmentVariables")
	b.WriteString("\t<dict>\n")
	key("\t\t", "PATH")
	str("\t\t", jobPath)
	// launchd times no line of the log below, so the manager does.
	key("\t\t", domain.StampLinesEnv)
	str("\t\t", "1")
	b.WriteString("\t</dict>\n")

	switch {
	case j.boot:
		// RunAtLoad starts it at boot, and KeepAlive on a failed exit
		// starts it again every ThrottleInterval, the unit's
		// Restart=on-failure, until the engine answers.
		key("\t", "RunAtLoad")
		b.WriteString("\t<true/>\n")
		key("\t", "KeepAlive")
		b.WriteString("\t<dict>\n")
		key("\t\t", "SuccessfulExit")
		b.WriteString("\t\t<false/>\n")
		b.WriteString("\t</dict>\n")
		key("\t", "ThrottleInterval")
		fmt.Fprintf(&b, "\t<integer>%d</integer>\n", throttle)
	case j.every > 0:
		key("\t", "StartInterval")
		fmt.Fprintf(&b, "\t<integer>%d</integer>\n", int64(j.every.Seconds()))
	default:
		key("\t", "StartCalendarInterval")
		if len(j.calendar) == 1 {
			writeInterval(&b, "\t", j.calendar[0])
		} else {
			b.WriteString("\t<array>\n")
			for _, i := range j.calendar {
				writeInterval(&b, "\t\t", i)
			}
			b.WriteString("\t</array>\n")
		}
	}

	log := filepath.Join(s.logDir, j.label+".log")
	key("\t", "StandardOutPath")
	str("\t", log)
	key("\t", "StandardErrorPath")
	str("\t", log)
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes()
}

func writeInterval(b *bytes.Buffer, indent string, i interval) {
	if len(i) == 0 {
		b.WriteString(indent + "<dict/>\n")
		return
	}
	b.WriteString(indent + "<dict>\n")
	for _, k := range intervalKeys {
		if v, ok := i[k]; ok {
			fmt.Fprintf(b, "%s\t<key>%s</key>\n%s\t<integer>%d</integer>\n", indent, k, indent, v)
		}
	}
	b.WriteString(indent + "</dict>\n")
}

// runsAtLoad is whether loading a job runs it, which is the boot job.
func runsAtLoad(contents []byte) bool {
	return bytes.Contains(contents, []byte("<key>RunAtLoad</key>"))
}

func (s *Supervisor) plistPath(label string) string {
	return filepath.Join(s.daemonDir, label+".plist")
}

// newsyslogConf is the file that rotates this installation's job logs.
func (s *Supervisor) newsyslogConf() string {
	return filepath.Join(s.newsyslogDir, Label(s.product)+".conf")
}

// newsyslog caps each job's log at 10 MB and keeps five, compressed. N because
// no process holds a log open to be signalled: a job opens it when it starts.
func (s *Supervisor) newsyslog(units []ports.Unit) []byte {
	var b strings.Builder
	b.WriteString("# The logs of " + s.product + "'s jobs, written by morzer.\n")
	b.WriteString("# logfilename\t[owner:group]\tmode\tcount\tsize\twhen\tflags\n")
	for _, u := range units {
		fmt.Fprintf(&b, "%s\troot:wheel\t600\t5\t10240\t*\tNZ\n", filepath.Join(s.logDir, u.Name+".log"))
	}
	return []byte(b.String())
}

// InstallUnits writes the jobs, loads each scheduled one it enables, and
// reloads a loaded one whose plist changed, so a schedule set by `config set`
// holds at once, as a reloaded systemd timer does.
//
// It never loads the boot job: launchd runs it when loaded, and installing the
// units must not converge an installation the operator has not applied. launchd
// loads it from DaemonDir at the next boot, as systemd starts the enabled
// service then (logs/macos-host.md).
func (s *Supervisor) InstallUnits(ctx context.Context, units []ports.Unit, scope ports.EnableScope) error {
	for _, dir := range []struct {
		path string
		mode os.FileMode
	}{{s.daemonDir, 0o755}, {s.newsyslogDir, 0o755}, {s.logDir, 0o700}} {
		if err := atomicfs.MkdirAll(dir.path, dir.mode); err != nil {
			return err
		}
	}

	// Decided before anything is written, as systemd's adapter decides which
	// units are fresh: only a definite absence counts as one, so an
	// unreadable file is not taken as new and enabled behind somebody's back.
	fresh := make(map[string]bool, len(units))
	changed := make(map[string]bool, len(units))
	// What a changed plist held, so a failure can put it back: launchd still
	// runs it, and a retry has to see the change again to make it.
	previous := make(map[string][]byte, len(units))
	for _, u := range units {
		if err := validateLabel(u.Name); err != nil {
			return err
		}
		switch old, err := os.ReadFile(s.plistPath(u.Name)); {
		case os.IsNotExist(err):
			fresh[u.Name] = true
		case err != nil:
			changed[u.Name] = true
		case !bytes.Equal(old, u.Contents):
			changed[u.Name] = true
			previous[u.Name] = old
		}
	}
	oldConf, confErr := os.ReadFile(s.newsyslogConf())

	// A failure undoes what this call did, for the reason systemd's adapter
	// does: a job left behind is not fresh on the retry, so EnableNew would
	// skip it, and a transient failure would become a schedule that never
	// runs. Unlike systemd's, it has the overwritten plists to put back, and
	// a job unloaded to be reloaded is loaded again as it was.
	var enabled, loaded, reloaded []string
	rollback := func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackBudget)
		defer cancel()
		for _, label := range loaded {
			_ = s.Stop(ctx, label)
		}
		for _, label := range enabled {
			_ = s.Disable(ctx, label)
		}
		for label := range fresh {
			_ = os.Remove(s.plistPath(label))
		}
		for label, old := range previous {
			_ = atomicfs.WriteFile(s.plistPath(label), old, 0o644)
		}
		// Its old plist is back, and launchd has the new one or none.
		for _, label := range reloaded {
			_ = s.Stop(ctx, label)
			_ = s.bootstrap(ctx, label)
		}
		switch {
		case os.IsNotExist(confErr):
			_ = os.Remove(s.newsyslogConf())
		case confErr == nil:
			_ = atomicfs.WriteFile(s.newsyslogConf(), oldConf, 0o644)
		}
	}

	for _, u := range units {
		if err := atomicfs.WriteFile(s.plistPath(u.Name), u.Contents, 0o644); err != nil {
			rollback()
			return err
		}
	}
	if err := atomicfs.WriteFile(s.newsyslogConf(), s.newsyslog(units), 0o644); err != nil {
		rollback()
		return err
	}

	var disabled map[string]bool
	disabledJobs := func() (map[string]bool, error) {
		if disabled == nil {
			var err error
			disabled, err = s.disabledJobs(ctx)
			return disabled, err
		}
		return disabled, nil
	}
	for _, u := range units {
		enable := u.Enable && (scope == ports.EnableAll || fresh[u.Name])
		if enable {
			// Only a job that was off is switched off again by the undo,
			// so a failed repair leaves on what it found on.
			wasOff := fresh[u.Name]
			if !wasOff {
				off, err := disabledJobs()
				if err != nil {
					rollback()
					return err
				}
				wasOff = off[u.Name]
			}
			if err := s.Enable(ctx, u.Name); err != nil {
				rollback()
				return err
			}
			if wasOff {
				enabled = append(enabled, u.Name)
			}
			if disabled != nil {
				disabled[u.Name] = false
			}
		}
		if runsAtLoad(u.Contents) {
			continue
		}
		isLoaded, err := s.loaded(ctx, u.Name)
		if err != nil {
			rollback()
			return err
		}
		switch {
		case !isLoaded && enable:
			if err := s.bootstrap(ctx, u.Name); err != nil {
				rollback()
				return err
			}
			loaded = append(loaded, u.Name)
		case isLoaded && changed[u.Name]:
			// launchd reads a plist when it loads it, so a new schedule
			// needs the job loaded again, which stops a run in progress.
			// A job an operator disabled stays as they left it.
			off, err := disabledJobs()
			if err != nil {
				rollback()
				return err
			}
			if off[u.Name] {
				continue
			}
			if err := s.Stop(ctx, u.Name); err != nil {
				rollback()
				return err
			}
			reloaded = append(reloaded, u.Name)
			if err := s.bootstrap(ctx, u.Name); err != nil {
				rollback()
				return err
			}
		}
	}
	return nil
}

// RemoveUnits unloads each job and deletes its plist, and the logs' rotation
// with the boot job, which goes only when every job does. The logs stay, as a
// journal does.
func (s *Supervisor) RemoveUnits(ctx context.Context, names []string) error {
	for _, label := range names {
		if err := validateLabel(label); err != nil {
			return err
		}
		// Unloaded first: a job whose plist is gone stays loaded until
		// the next boot otherwise. A failure is an answer only when
		// launchd no longer has the job, which is also what unloading a
		// job it never had looks like; otherwise the plist stays, so a
		// retry finds the job to remove.
		if err := s.Stop(ctx, label); err != nil {
			if loaded, lerr := s.loaded(ctx, label); lerr != nil || loaded {
				return err
			}
		}
		if err := os.Remove(s.plistPath(label)); err != nil && !os.IsNotExist(err) {
			return domain.Internal(err, "cannot remove job %s", label)
		}
		if label == Label(s.product) {
			if err := os.Remove(s.newsyslogConf()); err != nil && !os.IsNotExist(err) {
				return domain.Internal(err, "cannot remove %s", s.newsyslogConf())
			}
		}
	}
	return nil
}

// InstalledUnits reports which of a product's jobs have a plist, read from the
// directory rather than launchd, which may not have loaded one yet.
func (s *Supervisor) InstalledUnits(_ context.Context, product string) ([]string, error) {
	var out []string
	for _, label := range Labels(product) {
		switch _, err := os.Stat(s.plistPath(label)); {
		case err == nil:
			out = append(out, label)
		case os.IsNotExist(err):
		default:
			return nil, domain.Internal(err, "cannot read job %s", label)
		}
	}
	return out, nil
}

// UnitsManagerPaths is every manager binary a product's jobs run: the one each
// plist names, and the one launchd loaded, which is the older until the job is
// loaded again.
func (s *Supervisor) UnitsManagerPaths(ctx context.Context, product string) ([]string, error) {
	var out []string
	add := func(bin string) {
		if bin != "" && !slices.Contains(out, bin) {
			out = append(out, bin)
		}
	}
	for _, label := range Labels(product) {
		path := s.plistPath(label)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		}
		// plutil reads a plist in any of its formats, an operator's edit
		// in Xcode included.
		res, err := s.runner.Run(ctx, exec.Command{
			Argv:          []string{s.plutil, "-extract", "ProgramArguments.0", "raw", "-o", "-", path},
			Timeout:       30 * time.Second,
			CaptureOutput: true,
		})
		if err != nil {
			return nil, domain.Internal(err, "cannot read what %s runs", path)
		}
		add(strings.TrimSpace(res.Stdout))
		if props, ok, err := s.print(ctx, label); err != nil {
			return nil, err
		} else if ok {
			add(props["program"])
		}
	}
	return out, nil
}

func (s *Supervisor) Enable(ctx context.Context, label string) error {
	return s.run(ctx, "enable", "system/"+label)
}

func (s *Supervisor) Disable(ctx context.Context, label string) error {
	return s.run(ctx, "disable", "system/"+label)
}

// Start runs a loaded job now.
func (s *Supervisor) Start(ctx context.Context, label string) error {
	return s.run(ctx, "kickstart", "system/"+label)
}

// Stop unloads a job until it is loaded again, at the next boot if nothing
// sooner, which stops a run in progress, as `systemctl stop` does.
func (s *Supervisor) Stop(ctx context.Context, label string) error {
	return s.run(ctx, "bootout", "system/"+label)
}

func (s *Supervisor) bootstrap(ctx context.Context, label string) error {
	return s.run(ctx, "bootstrap", "system", s.plistPath(label))
}

func (s *Supervisor) run(ctx context.Context, args ...string) error {
	_, err := s.runner.Run(ctx, exec.Command{
		Argv:          append([]string{s.launchctl}, args...),
		Timeout:       2 * time.Minute,
		CaptureOutput: true,
	})
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return domain.Internal(err, "launchctl %s failed: %s",
				strings.Join(args, " "), firstLine(exitErr.Stderr)).
				WithHint("this usually means the manager is not running as root")
		}
		return domain.Internal(err, "cannot run launchctl %s", strings.Join(args, " "))
	}
	return nil
}

// loaded is whether launchd has the job in the system domain.
func (s *Supervisor) loaded(ctx context.Context, label string) (bool, error) {
	_, ok, err := s.print(ctx, label)
	return ok, err
}

// print reads `launchctl print system/<label>`'s own properties, the lines one
// tab in; those deeper describe the job's endpoints and the like. A job
// launchd does not have exits non-zero, which is an answer, not a failure.
func (s *Supervisor) print(ctx context.Context, label string) (map[string]string, bool, error) {
	res, err := s.runner.Run(ctx, exec.Command{
		Argv:          []string{s.launchctl, "print", "system/" + label},
		Timeout:       30 * time.Second,
		CaptureOutput: true,
	})
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, false, nil
		}
		return nil, false, domain.Internal(err, "cannot ask launchd about %s", label)
	}
	props := map[string]string{}
	for line := range strings.Lines(res.Stdout) {
		rest, ok := strings.CutPrefix(line, "\t")
		if !ok || strings.HasPrefix(rest, "\t") {
			continue
		}
		if k, v, ok := strings.Cut(strings.TrimSpace(rest), " = "); ok {
			if _, seen := props[k]; !seen {
				props[k] = v
			}
		}
	}
	return props, true, nil
}

// disabledJobs is the system domain's jobs an operator switched off, which
// stay off across boots until enabled. Older releases print `=> true` for a
// disabled one, newer `=> disabled`.
func (s *Supervisor) disabledJobs(ctx context.Context) (map[string]bool, error) {
	res, err := s.runner.Run(ctx, exec.Command{
		Argv:          []string{s.launchctl, "print-disabled", "system"},
		Timeout:       30 * time.Second,
		CaptureOutput: true,
	})
	if err != nil {
		return nil, domain.Internal(err, "cannot ask launchd which jobs are disabled")
	}
	out := map[string]bool{}
	for line := range strings.Lines(res.Stdout) {
		name, state, ok := strings.Cut(strings.TrimSpace(line), "=>")
		if !ok {
			continue
		}
		state = strings.TrimSpace(state)
		out[strings.Trim(strings.TrimSpace(name), `"`)] = state == "disabled" || state == "true"
	}
	return out, nil
}

// Status reports a job's state in systemd's words, which is what `doctor`
// reads: loaded means the plist is there, failed means its last run exited
// non-zero and launchd will not start it again before its schedule.
func (s *Supervisor) Status(ctx context.Context, label string) (ports.UnitState, error) {
	if err := validateLabel(label); err != nil {
		return ports.UnitState{}, err
	}
	contents, err := os.ReadFile(s.plistPath(label))
	switch {
	case os.IsNotExist(err):
		return ports.UnitState{Name: label}, nil
	case err != nil:
		return ports.UnitState{}, domain.Internal(err, "cannot read job %s", label)
	}
	disabled, err := s.disabledJobs(ctx)
	if err != nil {
		return ports.UnitState{}, err
	}
	state := ports.UnitState{Name: label, Loaded: true, Enabled: !disabled[label], Active: "inactive"}

	props, ok, err := s.print(ctx, label)
	if err != nil {
		return ports.UnitState{}, err
	}
	if !ok {
		// The boot job waits for the next boot to be loaded, as an
		// enabled service does under systemd. A schedule launchd does not
		// have runs at no time until then, which `doctor` reports.
		state.Sub = "unloaded"
		if runsAtLoad(contents) {
			state.Sub = "dead"
		}
		return state, nil
	}
	// "78: EX_CONFIG", or "(never exited)" before a first run.
	code, _, _ := strings.Cut(props["last exit code"], ":")
	state.ExitCode, _ = strconv.Atoi(strings.TrimSpace(code))
	switch {
	case props["state"] == "running":
		state.Active, state.Sub = "active", "running"
	case state.ExitCode != 0 && runsAtLoad(contents):
		// The boot job is started again after a failed exit, as
		// Restart=on-failure has it between attempts.
		state.Active, state.Sub = "activating", "auto-restart"
	case state.ExitCode != 0:
		state.Active, state.Sub = "failed", "exited"
	default:
		state.Sub = "waiting"
	}
	return state, nil
}

// ValidateSchedule accepts what launchd runs at exactly the times systemd
// would, and refuses the rest naming the forms it takes (RFC 0055 §5.6).
func (s *Supervisor) ValidateSchedule(_ context.Context, expr string) error {
	if strings.TrimSpace(expr) == "" {
		return nil
	}
	if _, err := calendar(expr); err != nil {
		return refuseSchedule(expr, err)
	}
	return nil
}

// validateLabel keeps a label from becoming a path or a second argument: it
// reaches both the filesystem and launchctl's argv.
func validateLabel(label string) error {
	if !strings.HasPrefix(label, labelPrefix) || strings.ContainsAny(label, "/\\ \n") ||
		strings.Contains(label, "..") {
		return domain.Internal(nil, "invalid job label %q", label)
	}
	return nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
