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

func (s *Supervisor) InstallUnits(ctx context.Context, units []ports.Unit) error {
	if err := atomicfs.MkdirAll(s.unitDir, 0o755); err != nil {
		return err
	}

	for _, u := range units {
		if err := validateUnitName(u.Name); err != nil {
			return err
		}
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
		if u.Enable {
			if err := s.Enable(ctx, u.Name); err != nil {
				return err
			}
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
}

// ServiceUnitName and friends derive unit names from the product.
func ServiceUnitName(product string) string       { return product + ".service" }
func BackupServiceUnitName(product string) string { return product + "-backup.service" }
func BackupTimerUnitName(product string) string   { return product + "-backup.timer" }

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

// DefaultBackupSchedule is a nightly backup at a quiet hour.
const DefaultBackupSchedule = "*-*-* 02:30:00"

// BuildUnits renders the unit set for a product.
func BuildUnits(p UnitParams) ([]ports.Unit, error) {
	if p.BackupSchedule == "" {
		p.BackupSchedule = DefaultBackupSchedule
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
func UnitNames(product string) []string {
	return []string{
		ServiceUnitName(product),
		BackupServiceUnitName(product),
		BackupTimerUnitName(product),
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
