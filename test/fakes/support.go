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

	// PassAfter makes checks fail until the Nth probe within a single
	// WaitReady, then pass -- a product that takes a few rounds to come up,
	// which is the case "polls until" exists for.
	PassAfter int

	// Patience bounds how long WaitReady polls before giving up.
	//
	// Zero -- the default -- is the port's actual promise: poll until the
	// context expires. A test that drives a whole operation through a
	// fifteen-minute step timeout has to shorten it, and has to do so
	// visibly: "the product never becomes healthy" really is a long wait in
	// production, and a fake that failed in a microsecond made that wait
	// disappear from every test that used it.
	Patience time.Duration

	// Interval is the gap between probes. Small, but not zero: a busy loop
	// would burn a core for the length of the wait.
	Interval time.Duration

	calls int

	// Err is returned instead of results when set.
	Err error
}

func NewHealth() *Health { return &Health{Healthy: true} }

var _ ports.HealthWaiter = (*Health)(nil)

// results renders one probe round. attempt is 1-based and is reported back, so
// a caller can see a check that passed on the third try -- the real waiter
// reports the same.
func (h *Health) results(specs []ports.CheckSpec, attempt int) []ports.HealthResult {
	out := make([]ports.HealthResult, 0, len(specs))
	for _, spec := range specs {
		ok := h.Healthy
		if h.FailAfter > 0 && h.calls > h.FailAfter {
			ok = false
		}
		if h.PassAfter > 0 && attempt < h.PassAfter {
			ok = false
		}
		msg := "ok"
		if !ok {
			msg = "simulated failure"
		}
		out = append(out, ports.HealthResult{
			Name: spec.Name(), OK: ok, Message: msg, Attempts: attempt,
		})
	}
	return out
}

// WaitReady polls until every check passes or the context expires, which is
// what the port promises and what the real waiter does.
//
// It used to probe once and return, which made every test that drove a product
// to "never healthy" prove something no implementation does. The difference
// between failing in a microsecond and failing at the deadline is the
// difference between a test that exercises cancellation during a wait and one
// that never waits at all.
func (h *Health) WaitReady(ctx context.Context, specs []ports.CheckSpec) ([]ports.HealthResult, error) {
	if len(specs) == 0 {
		return nil, nil
	}

	h.mu.Lock()
	patience, interval := h.Patience, h.Interval
	h.mu.Unlock()

	if patience > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, patience)
		defer cancel()
	}
	if interval <= 0 {
		interval = 5 * time.Millisecond
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for attempt := 1; ; attempt++ {
		h.mu.Lock()
		h.calls++
		err, results := h.Err, h.results(specs, attempt)
		h.mu.Unlock()

		if err != nil {
			return nil, err
		}

		failing := failingChecks(results)
		if len(failing) == 0 {
			return results, nil
		}

		select {
		case <-ctx.Done():
			return results, domain.HealthError(nil,
				"the product did not become healthy: %s", strings.Join(failing, ", "))
		case <-ticker.C:
		}
	}
}

func failingChecks(results []ports.HealthResult) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		if !r.OK {
			out = append(out, r.Name+" ("+r.Message+")")
		}
	}
	return out
}

func (h *Health) CheckOnce(ctx context.Context, specs []ports.CheckSpec) ([]ports.HealthResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls++
	if h.Err != nil {
		return nil, h.Err
	}
	return h.results(specs, 1), nil
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

	// Recorded only after the write succeeds. A failed Create that had already
	// registered the id would leave a backup that lists and cannot be read,
	// which is the state the real engine goes out of its way to avoid.
	path := "/fake/backups/" + id
	if b.Root != "" {
		written, dir, err := b.writeBackup(manifest)
		if err != nil {
			return ports.BackupRef{}, err
		}
		manifest, path = written, dir
	}

	b.backups[id] = manifest
	// Prepended so the list is newest-first without a sort.
	b.order = append([]string{id}, b.order...)

	return ports.BackupRef{ID: id, Path: path, At: at, Size: 1024}, nil
}

// writeBackup lays a backup out on disk the way hookbackup does: the components
// the manifest names, each with its size and the digest of the stored bytes,
// then the manifest.
//
// The checksums are not decoration. A manifest without them makes `verify` a
// no-op that reports success, so a fake that omitted them would let a test claim
// corruption is detected while nothing was ever compared.
func (b *Backup) writeBackup(m ports.BackupManifest) (ports.BackupManifest, string, error) {
	dir := filepath.Join(b.Root, m.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return m, "", err
	}

	for i, c := range m.Components {
		content := []byte("component " + string(c.Component) + " of " + m.ID)
		path := filepath.Join(dir, c.Path)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			return m, "", err
		}
		sum, err := atomicfs.DigestFile(path)
		if err != nil {
			return m, "", err
		}
		m.Components[i].Size = int64(len(content))
		m.Components[i].SHA256 = sum
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return m, "", err
	}
	if err := os.WriteFile(filepath.Join(dir, ports.BackupManifestFileName), data, 0o600); err != nil {
		return m, "", err
	}
	return m, dir, nil
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

// Verify checks the checksums when the backup is on disk.
//
// A fake that returned nil regardless would make every "verification catches
// this" test vacuous -- the assertion would pass because nothing was compared,
// which is indistinguishable from passing because the check works.
func (b *Backup) Verify(ctx context.Context, ref ports.BackupRef) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.Fail["Verify"]; err != nil {
		return err
	}
	if _, ok := b.backups[ref.ID]; !ok {
		return domain.BackupError(domain.ErrNotFound, "no backup with id %q", ref.ID)
	}

	// ref.Path rather than the recorded directory: a fetch verifies a staging
	// copy before promoting it, and pointing at the store instead would check
	// the wrong bytes.
	if dir := ref.Path; b.Root != "" && dir != "" {
		if err := verifyOnDisk(dir); err != nil {
			return err
		}
	}

	b.Verified[ref.ID]++
	return nil
}

// verifyOnDisk re-reads a backup directory and checks it against its own
// manifest, the way the real engine does.
func verifyOnDisk(dir string) error {
	data, err := os.ReadFile(filepath.Join(dir, ports.BackupManifestFileName))
	if err != nil {
		return domain.BackupError(err, "%s has no readable backup manifest", dir)
	}
	var m ports.BackupManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return domain.BackupError(err, "the manifest in %s is not valid JSON", dir)
	}

	for _, c := range m.Components {
		path := filepath.Join(dir, filepath.FromSlash(c.Path))
		info, err := os.Stat(path)
		if err != nil {
			return domain.BackupError(domain.ErrDigestMismatch,
				"backup %s is missing %s", m.ID, c.Path)
		}
		if c.Size > 0 && info.Size() != c.Size {
			return domain.BackupError(domain.ErrDigestMismatch,
				"backup %s: %s is %d bytes, manifest says %d",
				m.ID, c.Path, info.Size(), c.Size)
		}
		if c.SHA256 == "" {
			continue
		}
		sum, err := atomicfs.DigestFile(path)
		if err != nil {
			return domain.BackupError(err, "cannot read %s", c.Path)
		}
		if !atomicfs.SameDigest(sum, c.SHA256) {
			return domain.BackupError(domain.ErrDigestMismatch,
				"backup %s: %s failed its checksum", m.ID, c.Path)
		}
	}
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

	// KeepReasons used to be ignored here, which made every test involving
	// retention agree that a pre-update backup survives while proving
	// nothing about the engine that has to keep it -- and that backup is
	// the one an operator reaches for when the update they just ran went
	// wrong.
	exempt := make(map[string]bool, len(policy.KeepReasons))
	for _, r := range policy.KeepReasons {
		exempt[r] = true
	}

	var removed []ports.BackupRef
	kept := append([]string(nil), b.order[:keep]...)
	for _, id := range b.order[keep:] {
		if exempt[b.backups[id].Reason] {
			kept = append(kept, id)
			continue
		}
		removed = append(removed, ports.BackupRef{ID: id})
		delete(b.backups, id)
		if b.Root != "" {
			_ = os.RemoveAll(filepath.Join(b.Root, id))
		}
	}
	b.order = kept
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
		// The update pair is conditional, exactly as a real supervisor
		// makes it. A fake that returned every managed unit whatever it
		// was asked for would make any test about UpdateTimer vacuous --
		// it would pass whether the lifecycle layer set the field or
		// not, which is the only thing such a test is checking.
		if strings.Contains(name, "-update.") && !params.UpdateTimer {
			continue
		}
		out = append(out, ports.Unit{
			Name:     name,
			Contents: []byte("# fake unit for " + params.Product + "\n"),
			Enable:   !strings.HasSuffix(name, "-backup.service"),
		})
	}
	return out, nil
}

func (s *Supervisor) ManagedUnitNames(product string) []string {
	return []string{
		product + ".service",
		product + "-backup.service",
		product + "-backup.timer",
		product + "-update.service",
		product + "-update.timer",
	}
}

// InstalledUnits reports which managed units this fake has been given.
//
// It answers from what InstallUnits recorded rather than from ManagedUnitNames,
// because the distinction is the whole point of the method: a machine that ran
// `init --install-units=false` manages no units, and anything reconciling them
// later must be able to tell that from a machine that has them.
func (s *Supervisor) InstalledUnits(ctx context.Context, product string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.Fail["InstalledUnits"]; err != nil {
		return nil, err
	}
	var out []string
	for _, name := range s.ManagedUnitNames(product) {
		if _, ok := s.Installed[name]; ok {
			out = append(out, name)
		}
	}
	return out, nil
}
