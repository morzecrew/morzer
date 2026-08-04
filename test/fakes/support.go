package fakes

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
)

// Health is an in-memory ports.HealthWaiter.
type Health struct {
	mu sync.Mutex

	// Healthy controls whether checks pass.
	Healthy bool

	// FailAfter makes checks pass until the Nth call, then fail. Used to
	// simulate a product that comes up and then falls over.
	FailAfter int

	calls int

	// Err is returned instead of results when set.
	Err error
}

func NewHealth() *Health { return &Health{Healthy: true} }

var _ ports.HealthWaiter = (*Health)(nil)

func (h *Health) results(specs []ports.CheckSpec) []ports.HealthResult {
	out := make([]ports.HealthResult, 0, len(specs))
	for _, spec := range specs {
		ok := h.Healthy
		if h.FailAfter > 0 && h.calls > h.FailAfter {
			ok = false
		}
		msg := "ok"
		if !ok {
			msg = "simulated failure"
		}
		out = append(out, ports.HealthResult{
			Name: spec.Name(), OK: ok, Message: msg, Attempts: 1,
		})
	}
	return out
}

func (h *Health) WaitReady(ctx context.Context, specs []ports.CheckSpec) ([]ports.HealthResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls++

	if h.Err != nil {
		return nil, h.Err
	}
	results := h.results(specs)
	for _, r := range results {
		if !r.OK {
			return results, domain.HealthError(nil,
				"the product did not become healthy: %s (%s)", r.Name, r.Message)
		}
	}
	return results, nil
}

func (h *Health) CheckOnce(ctx context.Context, specs []ports.CheckSpec) ([]ports.HealthResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls++
	if h.Err != nil {
		return nil, h.Err
	}
	return h.results(specs), nil
}

// Backup is an in-memory ports.BackupEngine.
type Backup struct {
	mu sync.Mutex

	backups  map[string]ports.BackupManifest
	order    []string
	nextID   int
	Fail     map[string]error
	Now      func() time.Time
	Verified map[string]int

	// Root makes Create write a real backup directory rather than only
	// recording one.
	//
	// Set it when the test involves a backup target: pushing is a file
	// transfer, and a fake whose backups exist only in a map would make the
	// push step untestable at this level -- which is the level where "a
	// failed push fails the backup" actually lives.
	Root string
}

func NewBackup() *Backup {
	return &Backup{
		backups:  map[string]ports.BackupManifest{},
		Fail:     map[string]error{},
		Verified: map[string]int{},
		Now:      time.Now,
	}
}

var _ ports.BackupEngine = (*Backup)(nil)

func (b *Backup) Create(ctx context.Context, scope ports.Scope, labels map[string]string) (ports.BackupRef, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.Fail["Create"]; err != nil {
		return ports.BackupRef{}, err
	}

	b.nextID++
	id := "backup-" + itoa(b.nextID)
	at := domain.NewTime(b.Now())

	manifest := ports.BackupManifest{
		ID: id, CreatedAt: at, Reason: scope.Reason, Labels: labels,
		Product:    "demo",
		Components: []ports.ComponentRecord{{Component: ports.ComponentDatabase, Path: "db.sql.age"}},
	}
	b.backups[id] = manifest
	// Prepended so the list is newest-first without a sort.
	b.order = append([]string{id}, b.order...)

	path := "/fake/backups/" + id
	if b.Root != "" {
		var err error
		if path, err = b.writeBackup(manifest); err != nil {
			return ports.BackupRef{}, err
		}
	}
	return ports.BackupRef{ID: id, Path: path, At: at, Size: 1024}, nil
}

// writeBackup lays a backup out on disk the way hookbackup does: the components
// the manifest names, each with its size and the digest of the stored bytes,
// then the manifest.
//
// The checksums are not decoration. A manifest without them makes `verify` a
// no-op that reports success, so a fake that omitted them would let a test claim
// corruption is detected while nothing was ever compared.
func (b *Backup) writeBackup(m ports.BackupManifest) (string, error) {
	dir := filepath.Join(b.Root, m.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}

	for i, c := range m.Components {
		content := []byte("component " + string(c.Component) + " of " + m.ID)
		path := filepath.Join(dir, c.Path)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			return "", err
		}
		sum, err := atomicfs.DigestFile(path)
		if err != nil {
			return "", err
		}
		m.Components[i].Size = int64(len(content))
		m.Components[i].SHA256 = sum
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, ports.BackupManifestFileName), data, 0o600); err != nil {
		return "", err
	}
	return dir, nil
}

// Dir is where a backup lives on disk, empty when Root was not set.
func (b *Backup) Dir(id string) string {
	if b.Root == "" {
		return ""
	}
	return filepath.Join(b.Root, id)
}

func (b *Backup) List(ctx context.Context) ([]ports.BackupRef, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.Fail["List"]; err != nil {
		return nil, err
	}

	out := make([]ports.BackupRef, 0, len(b.order))
	for _, id := range b.order {
		m := b.backups[id]
		path := "/fake/backups/" + id
		if b.Root != "" {
			path = filepath.Join(b.Root, id)
		}
		out = append(out, ports.BackupRef{ID: id, Path: path, At: m.CreatedAt, Size: 1024})
	}
	return out, nil
}

func (b *Backup) Inspect(ctx context.Context, ref ports.BackupRef) (ports.BackupManifest, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.Fail["Inspect"]; err != nil {
		return ports.BackupManifest{}, err
	}
	m, ok := b.backups[ref.ID]
	if !ok {
		return ports.BackupManifest{}, domain.BackupError(domain.ErrNotFound,
			"no backup with id %q", ref.ID)
	}
	return m, nil
}

func (b *Backup) Verify(ctx context.Context, ref ports.BackupRef) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.Fail["Verify"]; err != nil {
		return err
	}
	if _, ok := b.backups[ref.ID]; !ok {
		return domain.BackupError(domain.ErrNotFound, "no backup with id %q", ref.ID)
	}
	b.Verified[ref.ID]++
	return nil
}

func (b *Backup) Restore(ctx context.Context, ref ports.BackupRef, opts ports.RestoreOptions) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.Fail["Restore"]; err != nil {
		return err
	}
	if _, ok := b.backups[ref.ID]; !ok {
		return domain.BackupError(domain.ErrNotFound, "no backup with id %q", ref.ID)
	}
	return nil
}

func (b *Backup) Prune(ctx context.Context, policy ports.RetentionPolicy) ([]ports.BackupRef, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.Fail["Prune"]; err != nil {
		return nil, err
	}

	keep := policy.Keep
	if keep < 1 {
		keep = 1
	}
	if len(b.order) <= keep {
		return nil, nil
	}

	var removed []ports.BackupRef
	for _, id := range b.order[keep:] {
		removed = append(removed, ports.BackupRef{ID: id})
		delete(b.backups, id)
		if b.Root != "" {
			_ = os.RemoveAll(filepath.Join(b.Root, id))
		}
	}
	b.order = b.order[:keep]
	return removed, nil
}

// Locker is an in-memory ports.Locker.
type Locker struct {
	mu    sync.Mutex
	held  bool
	owner ports.LockOwner

	// FailAcquire makes every acquisition report the lock as held, for
	// testing the exit-code-4 path.
	FailAcquire bool
}

func NewLocker() *Locker { return &Locker{} }

var _ ports.Locker = (*Locker)(nil)

func (l *Locker) Acquire(ctx context.Context, name string, opts ports.LockOptions) (func() error, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.FailAcquire || l.held {
		return nil, domain.Locked("the %s lock is held by %s operation %s",
			name, l.owner.Type, l.owner.OperationID)
	}

	l.held = true
	l.owner = opts.Owner

	return func() error {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.held = false
		l.owner = ports.LockOwner{}
		return nil
	}, nil
}

func (l *Locker) Owner(ctx context.Context, name string) (ports.LockOwner, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.owner, l.held, nil
}

// Renderer is a ports.Renderer that returns canned output, for tests that care
// about step sequencing rather than template semantics.
type Renderer struct {
	// Output maps a template name to what it renders to. A name absent
	// from the map renders to a deterministic placeholder.
	Output map[string][]byte
	Err    error
}

func NewRenderer() *Renderer { return &Renderer{Output: map[string][]byte{}} }

var _ ports.Renderer = (*Renderer)(nil)

func (r *Renderer) Render(ctx context.Context, tmpl ports.TemplateRef, data ports.TemplateData) ([]byte, error) {
	if r.Err != nil {
		return nil, r.Err
	}
	if out, ok := r.Output[tmpl.Name]; ok {
		return out, nil
	}
	return []byte("# rendered from " + tmpl.Name + "\nversion: " +
		data.Release.Version.String() + "\n"), nil
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

// Supervisor is an in-memory ports.Supervisor.
type Supervisor struct {
	mu sync.Mutex

	// Installed maps a unit name to its rendered contents.
	Installed map[string][]byte
	// States maps a unit name to the state Status reports.
	States map[string]ports.UnitState

	// Present controls whether the host is reported as having a supervisor.
	Present bool

	Fail map[string]error
}

func NewSupervisor() *Supervisor {
	return &Supervisor{
		Installed: map[string][]byte{},
		States:    map[string]ports.UnitState{},
		Present:   true,
		Fail:      map[string]error{},
	}
}

var _ ports.Supervisor = (*Supervisor)(nil)

func (s *Supervisor) Available(ctx context.Context) bool { return s.Present }

func (s *Supervisor) InstallUnits(ctx context.Context, units []ports.Unit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.Fail["InstallUnits"]; err != nil {
		return err
	}
	for _, u := range units {
		s.Installed[u.Name] = u.Contents
		s.States[u.Name] = ports.UnitState{Name: u.Name, Loaded: true, Active: "inactive", Enabled: u.Enable}
	}
	return nil
}

func (s *Supervisor) RemoveUnits(ctx context.Context, names []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.Fail["RemoveUnits"]; err != nil {
		return err
	}
	for _, n := range names {
		delete(s.Installed, n)
		delete(s.States, n)
	}
	return nil
}

func (s *Supervisor) Enable(ctx context.Context, unit string) error {
	return s.setActive(unit, "", true)
}
func (s *Supervisor) Disable(ctx context.Context, unit string) error {
	return s.setActive(unit, "", false)
}

func (s *Supervisor) Start(ctx context.Context, unit string) error {
	return s.setActive(unit, "active", true)
}

func (s *Supervisor) Stop(ctx context.Context, unit string) error {
	return s.setActive(unit, "inactive", true)
}

func (s *Supervisor) setActive(unit, active string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.States[unit]
	st.Name = unit
	st.Loaded = true
	if active != "" {
		st.Active = active
	}
	st.Enabled = enabled
	s.States[unit] = st
	return nil
}

func (s *Supervisor) Status(ctx context.Context, unit string) (ports.UnitState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.Fail["Status"]; err != nil {
		return ports.UnitState{}, err
	}
	if st, ok := s.States[unit]; ok {
		return st, nil
	}
	// An unknown unit is a state to report, not an error: doctor asks about
	// units that may never have been installed.
	return ports.UnitState{Name: unit, Loaded: false}, nil
}

// Units renders placeholder unit contents. The fake asserts the lifecycle
// layer asks for units rather than composing them, which is the boundary that
// matters; what a real supervisor puts in them is its own business.
func (s *Supervisor) Units(params ports.UnitParams) ([]ports.Unit, error) {
	if err := s.Fail["Units"]; err != nil {
		return nil, err
	}
	names := s.ManagedUnitNames(params.Product)
	out := make([]ports.Unit, 0, len(names))
	for _, name := range names {
		out = append(out, ports.Unit{
			Name:     name,
			Contents: []byte("# fake unit for " + params.Product + "\n"),
			Enable:   !strings.HasSuffix(name, "-backup.service"),
		})
	}
	return out, nil
}

func (s *Supervisor) ManagedUnitNames(product string) []string {
	return []string{product + ".service", product + "-backup.service", product + "-backup.timer"}
}
