// Package systemd implements ports.Supervisor over systemctl.
//
// The unit files are generated from templates rather than shipped, because
// they have to embed the manager's own path and the product name. The critical
// property they encode is that exit code 12 -- requires-manual-intervention --
// must not produce a restart loop: a system that needs a human is a system
// that must stop and wait for one, not one that retries every ten seconds
// while filling the journal.
package systemd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// Name is the provider name.
const Name = "systemd"

// UnitDir is where generated units are written. /etc rather than
// /usr/lib because these are machine-specific, and an operator overriding one
// should find it where local configuration lives.
const UnitDir = "/etc/systemd/system"

type Supervisor struct {
	runner  exec.Runner
	unitDir string

	// systemctl is the binary path, injectable for tests.
	systemctl string
}

type Option func(*Supervisor)

// WithUnitDir relocates unit installation, which is what makes this adapter
// testable without root.
func WithUnitDir(dir string) Option {
	return func(s *Supervisor) { s.unitDir = dir }
}

func WithSystemctl(path string) Option {
	return func(s *Supervisor) { s.systemctl = path }
}

func New(runner exec.Runner, opts ...Option) *Supervisor {
	s := &Supervisor{runner: runner, unitDir: UnitDir, systemctl: "systemctl"}
	for _, o := range opts {
		o(s)
	}
	return s
}

var _ ports.Supervisor = (*Supervisor)(nil)

// Available reports whether systemd is usable on this host.
//
// A machine without systemd is not an error: `init` skips unit installation
// and says so. Containers and minimal images are legitimate targets.
func (s *Supervisor) Available(ctx context.Context) bool {
	if _, err := s.runner.Look(s.systemctl); err != nil {
		return false
	}
	// systemctl exists on hosts where systemd is not PID 1 (a container
	// with the package installed). This is the standard probe for whether
	// it is actually running the system.
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return false
	}
	return true
}

func (s *Supervisor) InstallUnits(ctx context.Context, units []ports.Unit, scope ports.EnableScope) error {
	if err := atomicfs.MkdirAll(s.unitDir, 0o755); err != nil {
		return err
	}

	// Which units this call is about to create, decided before anything is
	// written, because writing is what destroys the evidence.
	//
	// The file's presence is the question and not `is-enabled`: a unit
	// switched off is still a unit this machine has, and a unit whose file
	// was never written is one nobody has had the chance to decide about.
	// Asking systemd instead would also mean a daemon round trip per unit
	// on a host where the daemon may not be running.
	fresh := make(map[string]bool, len(units))
	for _, u := range units {
		if err := validateUnitName(u.Name); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(s.unitDir, u.Name)); os.IsNotExist(err) {
			fresh[u.Name] = true
		}
	}

	for _, u := range units {
		path := filepath.Join(s.unitDir, u.Name)
		if err := atomicfs.WriteFile(path, u.Contents, 0o644); err != nil {
			return err
		}
	}

	// One reload after all units are written, not one per unit: systemd
	// would otherwise briefly see a half-installed set.
	if err := s.daemonReload(ctx); err != nil {
		return err
	}

	for _, u := range units {
		// Never a Disable, in either scope. A unit whose spec does not
		// ask for enablement is left as the machine has it -- the
		// oneshots must never be enabled, and if one somehow is, that is
		// a state to report rather than one to correct behind somebody's
		// back (RFC 0030 §8.1).
		if !u.Enable {
			continue
		}
		if scope == ports.EnableNew && !fresh[u.Name] {
			continue
		}
		if err := s.Enable(ctx, u.Name); err != nil {
			return err
		}
	}
	return nil
}

func (s *Supervisor) RemoveUnits(ctx context.Context, names []string) error {
	for _, name := range names {
		if err := validateUnitName(name); err != nil {
			return err
		}
		// Stop and disable before deleting: removing the file from
		// under a running unit leaves systemd holding a unit it can no
		// longer describe.
		_ = s.Stop(ctx, name)
		_ = s.Disable(ctx, name)

		if err := os.Remove(filepath.Join(s.unitDir, name)); err != nil && !os.IsNotExist(err) {
			return domain.Internal(err, "cannot remove unit %s", name)
		}
	}
	return s.daemonReload(ctx)
}

// InstalledUnits reports which managed units are present on disk.
//
// The filesystem rather than `systemctl list-units`: this must answer on a host
// where the daemon is not running, and the question is what this manager has
// written rather than what systemd currently knows about.
func (s *Supervisor) InstalledUnits(ctx context.Context, product string) ([]string, error) {
	var out []string
	for _, name := range UnitNames(product) {
		switch _, err := os.Stat(filepath.Join(s.unitDir, name)); {
		case err == nil:
			out = append(out, name)
		case os.IsNotExist(err):
		default:
			return nil, domain.Internal(err, "cannot read unit %s", name)
		}
	}
	return out, nil
}

func (s *Supervisor) Enable(ctx context.Context, unit string) error {
	return s.run(ctx, "enable", unit)
}
func (s *Supervisor) Disable(ctx context.Context, unit string) error {
	return s.run(ctx, "disable", unit)
}
func (s *Supervisor) Start(ctx context.Context, unit string) error { return s.run(ctx, "start", unit) }
func (s *Supervisor) Stop(ctx context.Context, unit string) error  { return s.run(ctx, "stop", unit) }

func (s *Supervisor) daemonReload(ctx context.Context) error {
	return s.run(ctx, "daemon-reload")
}

func (s *Supervisor) run(ctx context.Context, args ...string) error {
	_, err := s.runner.Run(ctx, exec.Command{
		Argv:          append([]string{s.systemctl}, args...),
		Timeout:       2 * time.Minute,
		CaptureOutput: true,
	})
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return domain.Internal(err, "systemctl %s failed: %s",
				strings.Join(args, " "), firstLine(exitErr.Stderr)).
				WithHint("this usually means the manager is not running as root")
		}
		return domain.Internal(err, "cannot run systemctl %s", strings.Join(args, " "))
	}
	return nil
}

// Status reports a unit's state.
//
// `systemctl show` is used rather than `status`, because show emits key=value
// pairs meant for machines while status emits a human-readable block whose
// format changes between systemd versions.
func (s *Supervisor) Status(ctx context.Context, unit string) (ports.UnitState, error) {
	if err := validateUnitName(unit); err != nil {
		return ports.UnitState{}, err
	}

	res, err := s.runner.Run(ctx, exec.Command{
		Argv: []string{s.systemctl, "show", unit,
			"--property=LoadState,ActiveState,SubState,UnitFileState,ExecMainStatus"},
		Timeout:       30 * time.Second,
		CaptureOutput: true,
	})
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// An unknown unit is a state to report, not a failure
			// of the query: doctor asks about units that may never
			// have been installed.
			return ports.UnitState{Name: unit, Loaded: false}, nil
		}
		return ports.UnitState{}, domain.Internal(err, "cannot query unit %s", unit)
	}

	props := parseProperties(res.Stdout)
	exitCode, _ := strconv.Atoi(props["ExecMainStatus"])

	return ports.UnitState{
		Name:     unit,
		Loaded:   props["LoadState"] == "loaded",
		Active:   props["ActiveState"],
		Sub:      props["SubState"],
		Enabled:  props["UnitFileState"] == "enabled" || props["UnitFileState"] == "enabled-runtime",
		ExitCode: exitCode,
	}, nil
}

func parseProperties(out string) map[string]string {
	props := make(map[string]string, 8)
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			props[k] = v
		}
	}
	return props
}

// validateUnitName keeps a product name from becoming a path traversal.
// The name reaches both the filesystem and systemctl's argv.
func validateUnitName(name string) error {
	if name == "" {
		return domain.Internal(nil, "empty unit name")
	}
	if strings.ContainsAny(name, "/\\ ") || strings.Contains(name, "..") {
		return domain.Internal(nil, "invalid unit name %q", name)
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

// UnitParams is what the unit templates are rendered against.
type UnitParams struct {
	Product string

	// ManagerPath is the absolute path of the morzer binary. Units must
	// not rely on PATH: systemd's environment is not a login shell's.
	ManagerPath string

	// ConfigPath is passed as --config so a unit keeps working if the
	// default location ever changes.
	ConfigPath string

	Description string

	// BackupSchedule is an OnCalendar expression for the backup timer.
	BackupSchedule string

	// UpdateSchedule is an OnCalendar expression for the update timer.
	//
	// It *is* the maintenance window, which is worth stating because "add a
	// maintenance window" is the obvious next feature request: an operator
	// who wants updates only on Sunday mornings writes that expression, and
	// systemd expresses it better than a config field would.
	UpdateSchedule string

	// UpdateTimer generates the update pair at all. A machine that follows
	// no channel gets no timer -- an installed unit that has nothing to poll
	// would contact nothing on a schedule and read, in `systemctl
	// list-timers`, as though it did.
	UpdateTimer bool

	// FleetSchedule is an OnCalendar expression for the fleet timer.
	FleetSchedule string

	// FleetTimer generates the fleet pair at all. False on a machine with
	// no target to publish to, for the reason UpdateTimer is false on one
	// with no channel: the unit would fail on every tick, and a unit that
	// fails on every tick is one an operator stops reading.
	FleetTimer bool
}

// ServiceUnitName and friends derive unit names from the product.
func ServiceUnitName(product string) string       { return product + ".service" }
func BackupServiceUnitName(product string) string { return product + "-backup.service" }
func BackupTimerUnitName(product string) string   { return product + "-backup.timer" }
func UpdateServiceUnitName(product string) string { return product + "-update.service" }
func UpdateTimerUnitName(product string) string   { return product + "-update.timer" }
func FleetServiceUnitName(product string) string  { return product + "-fleet.service" }
func FleetTimerUnitName(product string) string    { return product + "-fleet.timer" }

// serviceTemplate is the main unit.
//
// Three decisions are load-bearing:
//
//   - Type=oneshot with RemainAfterExit: `apply` converges and exits, it does
//     not stay resident. The unit represents "the product is applied", not "a
//     process is running".
//   - RestartPreventExitStatus=12: exit 12 means a human must look. Restarting
//     would spin, fill the journal, and bury the one message that matters.
//   - --plain and JSON logs: there is no TTY under systemd, and the journal
//     indexes structured fields.
const serviceTemplate = `[Unit]
Description={{.Description}}
Documentation=man:{{.Product}}(1)
After=network-online.target docker.service
Wants=network-online.target
Requires=docker.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart={{.ManagerPath}} apply --startup --plain --log-format json{{if .ConfigPath}} --config {{.ConfigPath}}{{end}}
ExecReload={{.ManagerPath}} apply --plain --log-format json{{if .ConfigPath}} --config {{.ConfigPath}}{{end}}

# Exit 12 is requires-manual-intervention. Restarting on it would loop
# forever and bury the reason in the journal.
Restart=on-failure
RestartPreventExitStatus=12
RestartSec=30s

TimeoutStartSec=1800
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
`

const backupServiceTemplate = `[Unit]
Description={{.Description}}
After={{.Product}}.service
Requires=docker.service

[Service]
Type=oneshot
ExecStart={{.ManagerPath}} backup --yes --plain --log-format json{{if .ConfigPath}} --config {{.ConfigPath}}{{end}}
TimeoutStartSec=7200
StandardOutput=journal
StandardError=journal
`

// backupTimerTemplate schedules backups.
//
// RandomizedDelaySec spreads load when several machines share a backup target,
// and Persistent catches up a run missed while the machine was off -- a VM
// that was down overnight should still get its daily backup.
const backupTimerTemplate = `[Unit]
Description={{.Description}}

[Timer]
OnCalendar={{.BackupSchedule}}
Persistent=true
RandomizedDelaySec=900

[Install]
WantedBy=timers.target
`

// updateServiceTemplate runs one scheduled update tick.
//
// `--unattended` is the whole difference from what an operator types: it
// follows the channel, stages what it finds, and installs it only if the release
// declares that a failure cannot end needing a database restore.
//
// No Restart= at all, unlike the main unit. A tick that failed is a tick; the
// timer brings the next one, and restarting a oneshot that just refused an
// update would retry the refusal every thirty seconds until the journal is the
// only thing on the disk.
const updateServiceTemplate = `[Unit]
Description={{.Description}}
After=network-online.target {{.Product}}.service
Wants=network-online.target

[Service]
Type=oneshot
ExecStart={{.ManagerPath}} update --unattended --plain --log-format json{{if .ConfigPath}} --config {{.ConfigPath}}{{end}}
TimeoutStartSec=3600
StandardOutput=journal
StandardError=journal
`

// updateTimerTemplate schedules the poll.
//
// RandomizedDelaySec is not decoration here: without it every installation of a
// product asks the vendor's registry at the same second of the same minute, and
// the vendor discovers their customer base by watching their own rate limiter.
//
// Persistent catches up a tick missed while the machine was off, which is the
// case a laptop-shaped deployment hits daily.
const updateTimerTemplate = `[Unit]
Description={{.Description}}

[Timer]
OnCalendar={{.UpdateSchedule}}
Persistent=true
RandomizedDelaySec=1800

[Install]
WantedBy=timers.target
`

// fleetServiceTemplate publishes this installation's row (RFC 0026 P4).
//
// The last phase of that design, and deliberately so: a scheduled publisher
// built before the payload was stable would have put badly-shaped objects in
// twelve buckets, and objects in buckets are the one thing this design cannot
// recall.
//
// No Restart=, like the update tick and for the same reason. A publish that
// failed is a gap in a *view* whose subject -- the deployment -- is fine, and
// this machine still knows everything the row would have said. The next tick
// carries the current truth, which is better than a retry carrying the old one.
//
// After the product's own service so a machine that has just booted publishes
// what converged rather than what was still starting: the row's health counts
// come from the runtime, and "0/3 up" thirty seconds into a boot is a true
// statement about a moment nobody wants a fleet screen to be showing.
const fleetServiceTemplate = `[Unit]
Description={{.Description}}
After=network-online.target {{.Product}}.service
Wants=network-online.target

[Service]
Type=oneshot
ExecStart={{.ManagerPath}} fleet publish --plain --log-format json{{if .ConfigPath}} --config {{.ConfigPath}}{{end}}
TimeoutStartSec=300
StandardOutput=journal
StandardError=journal
`

// fleetTimerTemplate schedules it.
//
// Hourly, which is the one place this project's timers differ in kind rather
// than in hour. A backup's value does not decay between runs and a row's is
// nothing but its age: `fleet ls` calls a row stale after a day by default, so
// a daily publisher would sit exactly at the threshold and report healthy
// machines as stale whenever scheduler jitter went the wrong way.
//
// RandomizedDelaySec spreads twelve machines that share one prefix, and
// Persistent catches up a machine that was off -- which publishes a fresh row
// at boot, the moment somebody is most likely to be looking at the fleet.
const fleetTimerTemplate = `[Unit]
Description={{.Description}}

[Timer]
OnCalendar={{.FleetSchedule}}
Persistent=true
RandomizedDelaySec=900

[Install]
WantedBy=timers.target
`

// DefaultFleetSchedule publishes on the hour.
//
// See fleetTimerTemplate: hourly against a staleness default of a day gives a
// machine twenty-four chances to be current, so one missed tick is not a row
// somebody investigates.
const DefaultFleetSchedule = "*-*-* *:00:00"

// DefaultUpdateSchedule is a daily check at an hour when nobody is deploying.
//
// Daily rather than every few minutes: the cost of a tick belongs to the
// vendor's registry, and a default that polled aggressively would be this
// manager spending somebody else's budget by nobody's decision.
const DefaultUpdateSchedule = "*-*-* 03:30:00"

// DefaultBackupSchedule is a nightly backup at a quiet hour.
const DefaultBackupSchedule = "*-*-* 02:30:00"

// BuildUnits renders the unit set for a product.
func BuildUnits(p UnitParams) ([]ports.Unit, error) {
	// Each schedule is one line of a root-owned file, so this refuses a value
	// that could be two. The lifecycle layer validates the one an operator can
	// set, and this is deliberately a second check rather than a repeat of it:
	// the guard that matters is the one nearest the write, because it holds
	// for a caller that has not been written yet. A newline here is a second
	// directive, and `Unit=` in a [Timer] section names what the timer starts.
	for _, s := range []struct{ field, value string }{
		{"backup schedule", p.BackupSchedule},
		{"update schedule", p.UpdateSchedule},
		{"fleet schedule", p.FleetSchedule},
	} {
		if strings.ContainsAny(s.value, "\n\r") {
			return nil, domain.Internal(nil,
				"the %s carries a line break and would add a directive to the unit",
				s.field)
		}
	}

	// Trimmed, not just compared: a whitespace-only schedule is non-empty, so
	// an exact check on "" would skip the default and render `OnCalendar=`
	// with nothing after it, which systemd refuses to load.
	if strings.TrimSpace(p.BackupSchedule) == "" {
		p.BackupSchedule = DefaultBackupSchedule
	}
	if strings.TrimSpace(p.UpdateSchedule) == "" {
		p.UpdateSchedule = DefaultUpdateSchedule
	}
	if strings.TrimSpace(p.FleetSchedule) == "" {
		p.FleetSchedule = DefaultFleetSchedule
	}
	if p.Description == "" {
		p.Description = p.Product + " (managed by morzer)"
	}

	specs := []struct {
		name   string
		tmpl   string
		desc   string
		enable bool
	}{
		{ServiceUnitName(p.Product), serviceTemplate, p.Description, true},
		{BackupServiceUnitName(p.Product), backupServiceTemplate, p.Product + " backup", false},
		// The timer is enabled, not the backup service: enabling a
		// oneshot service would run it at every boot.
		{BackupTimerUnitName(p.Product), backupTimerTemplate, p.Product + " scheduled backup", true},
	}

	if p.UpdateTimer {
		specs = append(specs,
			struct {
				name   string
				tmpl   string
				desc   string
				enable bool
			}{UpdateServiceUnitName(p.Product), updateServiceTemplate,
				p.Product + " update check", false},
			// The timer is enabled, not the service, for the same
			// reason the backup pair is: enabling a oneshot would
			// run it at every boot.
			struct {
				name   string
				tmpl   string
				desc   string
				enable bool
			}{UpdateTimerUnitName(p.Product), updateTimerTemplate,
				p.Product + " scheduled update check", true},
		)
	}

	if p.FleetTimer {
		specs = append(specs,
			struct {
				name   string
				tmpl   string
				desc   string
				enable bool
			}{FleetServiceUnitName(p.Product), fleetServiceTemplate,
				p.Product + " fleet row", false},
			// The timer is enabled and the service is not, which is the
			// third time this file says so and the third time it is
			// load-bearing: enabling a oneshot runs it at every boot.
			struct {
				name   string
				tmpl   string
				desc   string
				enable bool
			}{FleetTimerUnitName(p.Product), fleetTimerTemplate,
				p.Product + " scheduled fleet publish", true},
		)
	}

	units := make([]ports.Unit, 0, len(specs))
	for _, spec := range specs {
		params := p
		params.Description = spec.desc

		rendered, err := renderUnit(spec.name, spec.tmpl, params)
		if err != nil {
			return nil, err
		}
		units = append(units, ports.Unit{Name: spec.name, Contents: rendered, Enable: spec.enable})
	}
	return units, nil
}

func renderUnit(name, tmpl string, p UnitParams) ([]byte, error) {
	t, err := template.New(name).Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return nil, domain.Internal(err, "cannot parse the %s unit template", name)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, p); err != nil {
		return nil, domain.Internal(err, "cannot render the %s unit", name)
	}
	return buf.Bytes(), nil
}

// UnitNames lists the units this supervisor manages for a product.
// UnitNames lists every unit this supervisor may own for a product, whether or
// not this installation generates it.
//
// Deliberately the superset. It is what removal walks, and a machine that once
// followed a channel and then stopped must still have its update timer taken
// away -- a list narrowed to what the *current* configuration generates would
// leave the orphan running.
func UnitNames(product string) []string {
	return []string{
		ServiceUnitName(product),
		BackupServiceUnitName(product),
		BackupTimerUnitName(product),
		UpdateServiceUnitName(product),
		UpdateTimerUnitName(product),
		FleetServiceUnitName(product),
		FleetTimerUnitName(product),
	}
}

// ManagerPath resolves the running binary's absolute path, for embedding in
// units. A relative path would break the moment systemd ran it from /.
func ManagerPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "morzer"
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return exe
	}
	return resolved
}

// Units renders the unit set for a product, satisfying ports.Supervisor.
func (s *Supervisor) Units(params ports.UnitParams) ([]ports.Unit, error) {
	return BuildUnits(UnitParams{
		Product:        params.Product,
		ManagerPath:    params.ManagerPath,
		ConfigPath:     params.ConfigPath,
		Description:    params.Description,
		BackupSchedule: params.BackupSchedule,
		UpdateSchedule: params.UpdateSchedule,
		UpdateTimer:    params.UpdateTimer,
		FleetSchedule:  params.FleetSchedule,
		FleetTimer:     params.FleetTimer,
	})
}

// ManagedUnitNames lists the units this supervisor owns for a product.
func (s *Supervisor) ManagedUnitNames(product string) []string { return UnitNames(product) }
