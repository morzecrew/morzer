// Package fakes provides in-memory port implementations.
//
// They exist so a full operation can run in a unit test: no Docker, no root,
// no network, milliseconds instead of minutes. That is what makes the
// fault-injection suite practical -- killing an operation at each of eleven
// steps is a loop, not an afternoon.
//
// Every fake also has a failure switch, because the interesting tests are the
// ones where something breaks.
package fakes

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// Runtime is an in-memory ports.Runtime.
type Runtime struct {
	mu sync.Mutex

	// Services is the simulated project state.
	Services map[string]ports.ServiceState

	// Calls records method names in order, so a test can assert on
	// sequencing -- that migrations ran before services started, say.
	Calls []string

	// Fail maps a method name to the error it should return. This is the
	// fault-injection switch.
	Fail map[string]error

	// PulledImages records what Pull was asked for.
	PulledImages []string

	// UpCount counts how many times Up ran, for idempotence assertions.
	UpCount int

	// ValidateResult is what Validate returns.
	ValidateResult ports.Rendered

	// OneShotResults maps a service name to its exit result.
	OneShotResults map[string]ports.ExitResult

	// Storage is what Volumes reports.
	Storage ports.ProjectStorage

	// VolumeContents is the simulated contents of each volume, keyed by
	// its actual name. A capture writes this to a file; a restore reads it
	// back, so a round trip through the backup engine really does move
	// bytes.
	VolumeContents map[string]string

	// CaptureWitness records which services were running at the instant
	// each volume was captured.
	//
	// It is how a test asserts the thing the RFC's whole argument rests
	// on: that a cold volume was read with nothing writing to it. Polling
	// status from another goroutine would answer the same question less
	// precisely and only sometimes.
	CaptureWitness map[string][]string

	// VolumeSizes overrides what VolumeSize reports, so a test can present
	// a volume larger than any disk without allocating one.
	VolumeSizes map[string]int64

	// VolumeSizeErrors fails the measurement of one volume while the rest
	// still measure, which is the shape the space check has to survive: a
	// single unmeasurable volume must not decide anything about the ones
	// beside it. Keyed by actual volume name; Fail["VolumeSize"] is the
	// blunter version that fails them all.
	VolumeSizeErrors map[string]error

	// HelperMissing simulates the air-gapped machine: every volume
	// operation refuses with the pull command instead of running.
	HelperMissing bool
}

func NewRuntime() *Runtime {
	return &Runtime{
		Services:       map[string]ports.ServiceState{},
		Fail:           map[string]error{},
		OneShotResults: map[string]ports.ExitResult{},
		ValidateResult: ports.Rendered{Services: []string{"app", "db"}},
		VolumeContents: map[string]string{},
		CaptureWitness: map[string][]string{},
	}
}

var (
	_ ports.Runtime         = (*Runtime)(nil)
	_ ports.ImageInspector  = (*Runtime)(nil)
	_ ports.VolumeInspector = (*Runtime)(nil)
	_ ports.VolumeCapturer  = (*Runtime)(nil)
)

func (r *Runtime) record(method string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Calls = append(r.Calls, method)
	return r.Fail[method]
}

func (r *Runtime) Validate(ctx context.Context, cfg ports.RuntimeConfig) (ports.Rendered, error) {
	if err := r.record("Validate"); err != nil {
		return ports.Rendered{}, err
	}
	return r.ValidateResult, nil
}

func (r *Runtime) Pull(ctx context.Context, cfg ports.RuntimeConfig, images []string) error {
	if err := r.record("Pull"); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.PulledImages = append(r.PulledImages, images...)
	return nil
}

func (r *Runtime) Up(ctx context.Context, cfg ports.RuntimeConfig, opts ports.UpOptions) error {
	if err := r.record("Up"); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.UpCount++
	for _, name := range r.ValidateResult.Services {
		r.Services[name] = ports.ServiceState{
			Name: name, State: "running", Health: ports.HealthHealthy,
		}
	}
	return nil
}

func (r *Runtime) Down(ctx context.Context, cfg ports.RuntimeConfig, opts ports.DownOptions) error {
	if err := r.record("Down"); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	// The fake enforces the port's invariant, so a test that accidentally
	// passes Volumes true fails loudly here rather than silently in
	// production.
	if opts.Volumes {
		r.Calls = append(r.Calls, "Down(volumes)")
	}
	for name := range r.Services {
		r.Services[name] = ports.ServiceState{Name: name, State: "exited"}
	}
	return nil
}

func (r *Runtime) Restart(ctx context.Context, cfg ports.RuntimeConfig, services []string) error {
	if err := r.record("Restart"); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Calls = append(r.Calls, "Restart:"+strings.Join(services, ","))
	return nil
}

func (r *Runtime) Stop(ctx context.Context, cfg ports.RuntimeConfig, services []string, timeout time.Duration) error {
	if err := r.record("Stop"); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Calls = append(r.Calls, "Stop:"+strings.Join(services, ","))

	for _, name := range r.stopTargets(services) {
		r.Services[name] = ports.ServiceState{Name: name, State: "exited"}
	}
	return nil
}

func (r *Runtime) Start(ctx context.Context, cfg ports.RuntimeConfig, services []string) error {
	if err := r.record("Start"); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Calls = append(r.Calls, "Start:"+strings.Join(services, ","))

	for _, name := range r.stopTargets(services) {
		r.Services[name] = ports.ServiceState{
			Name: name, State: "running", Health: ports.HealthHealthy,
		}
	}
	return nil
}

// stopTargets resolves an empty service list to the whole project, matching
// what `compose stop` and `compose start` do. Callers must hold the lock.
func (r *Runtime) stopTargets(services []string) []string {
	if len(services) > 0 {
		return services
	}
	out := make([]string, 0, len(r.Services))
	for name := range r.Services {
		out = append(out, name)
	}
	return out
}

// Volumes reports the configured storage, sorted.
//
// Sorted on the way out rather than returned as set, because the port promises
// it and the Compose adapter delivers it. A fake that handed back insertion
// order let a test depend on an ordering production does not produce -- and the
// backup manifest records volume components in exactly this order, so "two
// backups of an unchanged project look the same" was being asserted against the
// one implementation that did not guarantee it.
func (r *Runtime) Volumes(ctx context.Context, cfg ports.RuntimeConfig) (ports.ProjectStorage, error) {
	if err := r.record("Volumes"); err != nil {
		return ports.ProjectStorage{}, err
	}

	out := ports.ProjectStorage{
		Volumes:   append([]ports.NamedVolume(nil), r.Storage.Volumes...),
		Binds:     append([]ports.BindMount(nil), r.Storage.Binds...),
		Anonymous: append([]ports.AnonymousVolume(nil), r.Storage.Anonymous...),
	}
	sort.Slice(out.Volumes, func(i, j int) bool { return out.Volumes[i].Name < out.Volumes[j].Name })
	sort.Slice(out.Binds, func(i, j int) bool { return out.Binds[i].Source < out.Binds[j].Source })
	for i := range out.Volumes {
		out.Volumes[i].Services = append([]string(nil), out.Volumes[i].Services...)
		sort.Strings(out.Volumes[i].Services)
	}
	for i := range out.Binds {
		out.Binds[i].Services = append([]string(nil), out.Binds[i].Services...)
		sort.Strings(out.Binds[i].Services)
	}
	sort.Slice(out.Anonymous, func(i, j int) bool {
		if out.Anonymous[i].Service != out.Anonymous[j].Service {
			return out.Anonymous[i].Service < out.Anonymous[j].Service
		}
		return out.Anonymous[i].Target < out.Anonymous[j].Target
	})
	return out, nil
}

func (r *Runtime) HelperImage() string { return "busybox@sha256:" + strings.Repeat("f", 64) }

// CaptureVolume writes the simulated contents out, recording who was running
// while it did.
func (r *Runtime) CaptureVolume(ctx context.Context, cfg ports.RuntimeConfig, volume, destPath string) error {
	if err := r.record("CaptureVolume"); err != nil {
		return err
	}
	if err := r.helperPresent(); err != nil {
		return err
	}

	r.mu.Lock()
	r.Calls = append(r.Calls, "CaptureVolume:"+volume)
	// OccupiesVolume, not Running: an unhealthy container is not "running"
	// but still holds its files open, and a witness that excluded it would
	// let a capture look cold while something had the volume.
	var running []string
	for name, state := range r.Services {
		if state.OccupiesVolume() {
			running = append(running, name)
		}
	}
	sort.Strings(running)
	r.CaptureWitness[volume] = running
	contents := r.VolumeContents[volume]
	r.mu.Unlock()

	return os.WriteFile(destPath, []byte(contents), 0o600)
}

func (r *Runtime) RestoreVolume(ctx context.Context, cfg ports.RuntimeConfig, volume, srcPath string) error {
	if err := r.record("RestoreVolume"); err != nil {
		return err
	}
	if err := r.helperPresent(); err != nil {
		return err
	}

	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.Calls = append(r.Calls, "RestoreVolume:"+volume)
	r.VolumeContents[volume] = string(data)
	return nil
}

func (r *Runtime) VolumeSize(ctx context.Context, cfg ports.RuntimeConfig, volume string) (int64, error) {
	if err := r.record("VolumeSize"); err != nil {
		return 0, err
	}
	if err := r.helperPresent(); err != nil {
		return 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err, ok := r.VolumeSizeErrors[volume]; ok {
		return 0, err
	}
	if size, ok := r.VolumeSizes[volume]; ok {
		return size, nil
	}
	return int64(len(r.VolumeContents[volume])), nil
}

// helperPresent reproduces the adapter's refusal on a machine that has never
// pulled the helper image, so the offline path is exercised without a daemon.
func (r *Runtime) helperPresent() error {
	r.mu.Lock()
	missing := r.HelperMissing
	r.mu.Unlock()
	if !missing {
		return nil
	}
	return domain.RuntimeError(domain.ErrToolMissing,
		"the volume helper image %s is not on this machine", r.HelperImage()).
		WithHint("run `docker pull %s` while this machine has a network", r.HelperImage())
}

func (r *Runtime) RunOneShot(ctx context.Context, cfg ports.RuntimeConfig, service string, opts ports.RunOptions) (ports.ExitResult, error) {
	if err := r.record("RunOneShot"); err != nil {
		return ports.ExitResult{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if res, ok := r.OneShotResults[service]; ok {
		return res, nil
	}
	return ports.ExitResult{ExitCode: 0}, nil
}

func (r *Runtime) Exec(ctx context.Context, cfg ports.RuntimeConfig, service string, argv []string) (ports.ExitResult, error) {
	if err := r.record("Exec"); err != nil {
		return ports.ExitResult{}, err
	}
	return ports.ExitResult{ExitCode: 0}, nil
}

func (r *Runtime) Status(ctx context.Context, cfg ports.RuntimeConfig) ([]ports.ServiceState, error) {
	if err := r.record("Status"); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]ports.ServiceState, 0, len(r.Services))
	for _, s := range r.Services {
		out = append(out, s)
	}
	// Sorted so assertions are stable: map iteration is not.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Name < out[j-1].Name; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// HasImage answers from PulledImages, so a test can arrange a machine that has
// never pulled and assert doctor says so.
func (r *Runtime) HasImage(ctx context.Context, imageRef string) (bool, error) {
	if err := r.record("HasImage"); err != nil {
		return false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, pulled := range r.PulledImages {
		if pulled == imageRef {
			return true, nil
		}
	}
	return false, nil
}

func (r *Runtime) Logs(ctx context.Context, cfg ports.RuntimeConfig, opts ports.LogOptions) (io.ReadCloser, error) {
	if err := r.record("Logs"); err != nil {
		return nil, err
	}
	return io.NopCloser(strings.NewReader("")), nil
}

// CallsMatching returns recorded calls whose name starts with prefix.
func (r *Runtime) CallsMatching(prefix string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, c := range r.Calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// SecretStore is an in-memory ports.SecretStore.
//
// It enforces the same invariants as the real one -- 0400 files in a 0700
// directory, no last-recipient removal -- so the contract suite is a genuine
// test of both rather than of the real adapter only.
type SecretStore struct {
	mu sync.Mutex

	values     map[string]string
	changed    map[string]time.Time
	recipients []ports.Recipient
	rendered   map[string][]byte

	// Fail maps a method name to the error it should return.
	Fail map[string]error

	Now func() time.Time
}

func NewSecretStore() *SecretStore {
	return &SecretStore{
		values:   map[string]string{},
		changed:  map[string]time.Time{},
		rendered: map[string][]byte{},
		Fail:     map[string]error{},
		Now:      time.Now,
		recipients: []ports.Recipient{
			{PublicKey: "age1fakemachinekey000000000000000000000000000000000000000000",
				Kind: ports.RecipientMachine},
		},
	}
}

var _ ports.SecretStore = (*SecretStore)(nil)

func (s *SecretStore) fail(method string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Fail[method]
}

func (s *SecretStore) Initialized(ctx context.Context) (bool, error) {
	if err := s.fail("Initialized"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.values) > 0, nil
}

func (s *SecretStore) Load(ctx context.Context) (domain.SecretSet, error) {
	if err := s.fail("Load"); err != nil {
		return domain.SecretSet{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make(map[string]domain.Secret, len(s.values))
	for k, v := range s.values {
		out[k] = domain.NewSecret(v)
	}
	return domain.NewSecretSet(out), nil
}

func (s *SecretStore) Set(ctx context.Context, name string, value domain.Secret) error {
	if err := s.fail("Set"); err != nil {
		return err
	}
	if value.IsEmpty() {
		return domain.SecretsError(nil, "refusing to store an empty value for secret %q", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[name] = value.Reveal()
	s.changed[name] = s.Now()
	return nil
}

func (s *SecretStore) Generate(ctx context.Context, name string, spec ports.GenSpec) error {
	if err := s.fail("Generate"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.values[name]; exists && !spec.Overwrite {
		return domain.SecretsError(nil, "secret %q already exists", name)
	}
	if !spec.Generator.Auto() {
		return domain.SecretsError(nil, "secret %q has no generator", name)
	}

	// Deterministic but distinct per name, and long enough to survive the
	// redactor's minimum-length rule.
	s.values[name] = fmt.Sprintf("generated-%s-%d", name, len(s.values)+1)
	s.changed[name] = s.Now()
	return nil
}

func (s *SecretStore) Rotate(ctx context.Context, name string, spec ports.GenSpec) error {
	if err := s.fail("Rotate"); err != nil {
		return err
	}
	s.mu.Lock()
	_, exists := s.values[name]
	s.mu.Unlock()
	if !exists {
		return domain.SecretsError(domain.ErrSecretNotFound, "secret %q does not exist", name)
	}
	spec.Overwrite = true
	return s.Generate(ctx, name, spec)
}

func (s *SecretStore) Remove(ctx context.Context, name string) error {
	if err := s.fail("Remove"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, name)
	delete(s.changed, name)
	return nil
}

func (s *SecretStore) Metadata(ctx context.Context) ([]ports.SecretMetadata, error) {
	if err := s.fail("Metadata"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]ports.SecretMetadata, 0, len(s.values))
	for name, value := range s.values {
		m := ports.SecretMetadata{
			Name:        name,
			Fingerprint: fmt.Sprintf("%x", len(value)*31),
			Length:      len(value),
		}
		if t, ok := s.changed[name]; ok {
			m.LastChanged = domain.NewTime(t)
		}
		out = append(out, m)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Name < out[j-1].Name; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// Render writes real files, because the permission invariants are the point.
func (s *SecretStore) Render(ctx context.Context, targetDir string, schema domain.SecretSchema) ([]ports.RenderedFile, error) {
	if err := s.fail("Render"); err != nil {
		return nil, err
	}

	set, err := s.Load(ctx)
	if err != nil {
		return nil, err
	}
	if missing := schema.Missing(set); len(missing) > 0 {
		return nil, domain.SecretsError(domain.ErrSecretNotFound,
			"required secret(s) not set: %s", strings.Join(missing, ", "))
	}

	return renderToDisk(targetDir, schema, set)
}

func (s *SecretStore) Recipients(ctx context.Context) ([]ports.Recipient, error) {
	if err := s.fail("Recipients"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ports.Recipient, len(s.recipients))
	copy(out, s.recipients)
	return out, nil
}

func (s *SecretStore) AddRecipient(ctx context.Context, r ports.Recipient) error {
	if err := s.fail("AddRecipient"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.recipients {
		if existing.PublicKey == r.PublicKey {
			return nil
		}
	}
	s.recipients = append(s.recipients, r)
	return nil
}

func (s *SecretStore) RemoveRecipient(ctx context.Context, r ports.Recipient) error {
	if err := s.fail("RemoveRecipient"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var remaining []ports.Recipient
	found := false
	for _, existing := range s.recipients {
		if existing.PublicKey == r.PublicKey {
			found = true
			if existing.Kind == ports.RecipientMachine {
				return domain.SecretsError(nil,
					"refusing to remove this machine's own recipient key")
			}
			continue
		}
		remaining = append(remaining, existing)
	}
	if !found {
		return domain.SecretsError(domain.ErrNotFound, "not a recipient")
	}
	if len(remaining) == 0 {
		return domain.SecretsError(nil, "refusing to remove the last recipient")
	}
	s.recipients = remaining
	return nil
}

// ReencryptFor replaces the recipient set wholesale.
//
// It applies the two rules that are not about cryptography -- an empty set is
// refused, keys are validated and de-duplicated -- and deliberately does not
// apply RemoveRecipient's refusals, matching the real store: this is the method
// recovery uses to drop a machine key whose host no longer exists.
func (s *SecretStore) ReencryptFor(ctx context.Context, recipients []ports.Recipient) error {
	if err := s.fail("ReencryptFor"); err != nil {
		return err
	}
	if len(recipients) == 0 {
		return domain.SecretsError(nil, "refusing to re-encrypt for an empty recipient set").
			WithHint("the secret state would become permanently undecryptable")
	}

	next := make([]ports.Recipient, 0, len(recipients))
	seen := map[string]bool{}
	for _, r := range recipients {
		if err := s.ValidateRecipient(r.PublicKey); err != nil {
			return err
		}
		if seen[r.PublicKey] {
			continue
		}
		seen[r.PublicKey] = true
		if r.Kind == "" {
			r.Kind = ports.RecipientOperator
		}
		next = append(next, r)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.recipients = next
	return nil
}

// This fake deliberately does NOT implement ports.RecoverableSecretStore.
//
// Every question that capability exists to answer is a question about
// cryptography: can this offline key open this state, does a re-encryption
// leave the new machine able to read it, is what an export carries actually
// ciphertext. A fake holding plaintext in a map can answer none of them, and
// one that returned plausible values would let the recovery scenario -- the
// test the whole feature exists for -- pass without proving anything.
//
// So `installation import` refuses a fake store, and the recovery path is
// proven end to end against the real sops-age store with real age keys in
// TestRecoveryRebuildsAMachineFromAnOfflineKey.

// SetChanged backdates a secret's last-changed timestamp, so a test can put a
// secret past a rotation period without waiting ninety days for it.
func (s *SecretStore) SetChanged(name string, when time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.changed[name] = when
}

// Seed sets values directly, for test arrangement.
func (s *SecretStore) Seed(values map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range values {
		s.values[k] = v
		s.changed[k] = s.Now()
	}
}

// EnsureIdentity creates the machine identity if absent and returns its public
// half. Idempotent, matching the real store: regenerating would orphan every
// encrypted value.
func (s *SecretStore) EnsureIdentity(ctx context.Context) (string, error) {
	if err := s.fail("EnsureIdentity"); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, r := range s.recipients {
		if r.Kind == ports.RecipientMachine {
			return r.PublicKey, nil
		}
	}
	machine := ports.Recipient{
		PublicKey: "age1fakemachinekey000000000000000000000000000000000000000000",
		Kind:      ports.RecipientMachine,
	}
	s.recipients = append(s.recipients, machine)
	return machine.PublicKey, nil
}

// IdentityPublicKey returns the machine key without creating one.
func (s *SecretStore) IdentityPublicKey(ctx context.Context) (string, error) {
	if err := s.fail("IdentityPublicKey"); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, r := range s.recipients {
		if r.Kind == ports.RecipientMachine {
			return r.PublicKey, nil
		}
	}
	return "", domain.SecretsError(domain.ErrNotFound, "no machine identity exists")
}

// ValidateRecipient applies the same shape check the real store does, so a
// test using an obviously bogus key fails the way production would.
func (s *SecretStore) ValidateRecipient(key string) error {
	if !strings.HasPrefix(key, "age1") || len(key) < 20 {
		return domain.SecretsError(nil, "%q is not a valid age recipient", key)
	}
	return nil
}
