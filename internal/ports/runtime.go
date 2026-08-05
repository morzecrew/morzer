// Package ports declares the interfaces the lifecycle layer speaks to.
//
// Interfaces are declared here by the consumer, not by the implementation:
// adapters satisfy them implicitly. The lifecycle layer imports only this
// package, so replacing Compose with another runtime never touches lifecycle
// logic.
//
// Every method takes a context as its first argument, and no method may block
// indefinitely -- cancellation must always reach the external tool.
package ports

import (
	"context"
	"io"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
)

// Runtime is the container runtime: bringing services up, running one-shot
// jobs, and reporting what is running.
//
// v1 is compose. Later: podman-compose, systemd-quadlet, single-node k3s.
type Runtime interface {
	// Validate parses and checks the runtime configuration without any
	// side effects, returning the fully resolved form for the plan view.
	Validate(ctx context.Context, cfg RuntimeConfig) (Rendered, error)

	// Pull fetches images. References are digests, so a successful pull is
	// reproducible.
	Pull(ctx context.Context, cfg RuntimeConfig, images []string) error

	// Up converges the project to running and waits for health. It is
	// idempotent: calling it on an already-converged project is a no-op.
	Up(ctx context.Context, cfg RuntimeConfig, opts UpOptions) error

	// Down stops the project. It never removes volumes unless
	// DownOptions.Volumes is set, and that flag may only originate at the
	// CLI layer behind an explicit operator confirmation.
	Down(ctx context.Context, cfg RuntimeConfig, opts DownOptions) error

	// Restart restarts the named services, or all of them when empty.
	Restart(ctx context.Context, cfg RuntimeConfig, services []string) error

	// Stop halts the named services without removing their containers,
	// networks or volumes. Empty stops the whole project.
	//
	// Distinct from Down, which tears the project down: this is the half of
	// the pair that a backup uses to quiesce writers before reading their
	// storage, and it has to be reversible by Start without recreating
	// anything.
	Stop(ctx context.Context, cfg RuntimeConfig, services []string, timeout time.Duration) error

	// Start starts services Stop halted, without reconciling against the
	// declared configuration.
	//
	// Deliberately not Up: Up converges, which may recreate a container
	// whose definition has drifted. Resuming a stack after a backup must
	// put back exactly what was stopped -- an operation that quietly
	// recreated a container as a side effect of taking a backup would be a
	// backup that changed the deployment.
	Start(ctx context.Context, cfg RuntimeConfig, services []string) error

	// RunOneShot runs a service to completion -- migrations, admin jobs.
	RunOneShot(ctx context.Context, cfg RuntimeConfig, service string, opts RunOptions) (ExitResult, error)

	// Exec runs a command inside an already-running service.
	Exec(ctx context.Context, cfg RuntimeConfig, service string, argv []string) (ExitResult, error)

	// Status reports the state of every service in the project.
	Status(ctx context.Context, cfg RuntimeConfig) ([]ServiceState, error)

	// Logs streams service logs. The caller closes the reader.
	Logs(ctx context.Context, cfg RuntimeConfig, opts LogOptions) (io.ReadCloser, error)
}

// RuntimeConfig identifies which project the operation acts on. It is passed
// per call rather than held in the adapter so one adapter instance can serve
// several projects, and so the adapter holds no mutable state.
type RuntimeConfig struct {
	// Project is the Compose project name -- the namespace for containers,
	// networks and volumes.
	Project string

	// Files are absolute paths to the Compose files, in merge order.
	Files []string

	// WorkingDir is the release root. Relative paths inside Compose files
	// resolve against it.
	WorkingDir string

	// Env is passed to the runtime process, typically carrying the
	// variables Compose files interpolate. It never contains secret
	// values: secrets reach containers as files, not environment.
	Env map[string]string

	// Profiles are runtime-level service profiles, distinct from the
	// manifest's deployment profiles (which select files, not services).
	Profiles []string
}

// Rendered is the resolved configuration Validate produces.
type Rendered struct {
	// Config is the merged configuration in canonical form, used for the
	// dry-run diff.
	Config []byte

	// Services are the service names the merged configuration defines.
	Services []string
}

type UpOptions struct {
	// Services limits the operation; empty means the whole project.
	Services []string

	// Wait blocks until containers report healthy.
	Wait bool

	// WaitTimeout bounds that wait. Zero means the context's deadline
	// governs.
	WaitTimeout time.Duration

	// RemoveOrphans deletes containers belonging to the project but no
	// longer declared -- normal after a release changes its service list.
	RemoveOrphans bool
}

type DownOptions struct {
	// Volumes destroys named volumes. This deletes application data, so
	// the flag must trace back to an explicit operator confirmation.
	Volumes bool

	RemoveOrphans bool
	Timeout       time.Duration
}

type RunOptions struct {
	Argv    []string
	Env     map[string]string
	Timeout time.Duration

	// Remove deletes the container after it exits. Off when the caller
	// wants to inspect a failed job.
	Remove bool
}

type LogOptions struct {
	Services []string
	Follow   bool
	Tail     int
	Since    time.Time
}

// ExitResult is the outcome of a process the runtime ran on the caller's
// behalf. A non-zero ExitCode is data, not an error: the caller decides
// whether it means failure.
type ExitResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Duration time.Duration
}

func (r ExitResult) OK() bool { return r.ExitCode == 0 }

// ServiceHealth is the health a runtime reports for a container.
type ServiceHealth string

const (
	HealthUnknown   ServiceHealth = "unknown"
	HealthStarting  ServiceHealth = "starting"
	HealthHealthy   ServiceHealth = "healthy"
	HealthUnhealthy ServiceHealth = "unhealthy"
	HealthNone      ServiceHealth = "none" // no healthcheck declared
)

type ServiceState struct {
	Name     string        `json:"name"`
	Image    string        `json:"image,omitempty"`
	State    string        `json:"state"` // running, exited, created, ...
	Health   ServiceHealth `json:"health"`
	ExitCode int           `json:"exit_code,omitempty"`
	Status   string        `json:"status,omitempty"` // human-readable, e.g. "Up 3 hours"
}

// Running reports whether the service is up and not unhealthy. A service with
// no healthcheck counts as running when its state says so -- absence of a
// probe is not evidence of illness.
func (s ServiceState) Running() bool {
	return s.State == "running" && s.Health != HealthUnhealthy
}

// RegistryProber is an optional capability a Runtime may implement: checking
// that an image's registry is reachable without transferring any layers.
//
// It is a separate interface rather than a Runtime method because not every
// runtime has a registry to probe, and a mandatory method would force every
// adapter to implement a stub. Callers type-assert and degrade gracefully when
// the assertion fails.
type RegistryProber interface {
	ProbeRegistry(ctx context.Context, imageRef string) error
}

// ImageInspector reports whether an image is already on this machine.
//
// It exists for the question an operator has to answer *before* losing network
// access, not after: will this deployment come up on a host that cannot reach a
// registry. `apply --startup` already skips pulls when images are local, so the
// capability is there — what was missing was any way to find out beforehand,
// which meant discovering it at the moment nothing could be done about it.
//
// Optional for the same reason as RegistryProber: a runtime with no local image
// store has nothing to answer, and a mandatory method would make it lie.
type ImageInspector interface {
	// HasImage reports whether imageRef is present locally. An image that
	// cannot be checked is reported as an error rather than as absent:
	// "not here" and "cannot tell" lead an operator to different actions.
	HasImage(ctx context.Context, imageRef string) (bool, error)
}

// VolumeInspector reports the storage a project's resolved configuration
// declares.
//
// Read-only and cheap, so `doctor` can ask it on every invocation. It is what
// makes "which of this deployment's volumes is no backup covering" an
// answerable question rather than something an operator discovers during a
// restore.
type VolumeInspector interface {
	Volumes(ctx context.Context, cfg RuntimeConfig) (ProjectStorage, error)
}

// VolumeCapturer reads and writes a volume's contents.
//
// Optional for the same reason as RegistryProber: a runtime with no volume
// concept has nothing to answer. Callers type-assert and say plainly what they
// cannot do rather than failing obscurely.
//
// The implementation must not depend on the host's storage layout.
// /var/lib/docker/volumes is an implementation detail, and it is unreadable
// under a rootless daemon or a remote one -- so a volume is read the way
// anything else reads one, through a container that mounts it.
type VolumeCapturer interface {
	// CaptureVolume writes the volume's contents to destPath as an
	// uncompressed tar. The volume is mounted read-only: a helper that
	// misbehaves must not be able to write into the product's data.
	CaptureVolume(ctx context.Context, cfg RuntimeConfig, volume, destPath string) error

	// RestoreVolume replaces the volume's contents with the tar at
	// srcPath.
	//
	// Replaces, not merges. A restore that left files the backup does not
	// contain would produce a volume matching no point in time, which
	// beside a database restored to an exact one is how dangling
	// references are made.
	RestoreVolume(ctx context.Context, cfg RuntimeConfig, volume, srcPath string) error

	// VolumeSize reports how many bytes the volume holds, so a backup that
	// will not fit can be refused before it is started rather than
	// discovered halfway through.
	VolumeSize(ctx context.Context, cfg RuntimeConfig, volume string) (int64, error)

	// HelperImage is the image the three methods above run. Reported by
	// `doctor` so an operator preparing an air-gapped machine learns which
	// image to cache before backup night rather than during it.
	HelperImage() string
}

// ProjectStorage is everything a project's resolved configuration mounts.
type ProjectStorage struct {
	// Volumes are the named volumes, sorted by name so plans and manifests
	// do not shuffle between runs.
	Volumes []NamedVolume

	// Binds are host paths mounted into containers. Reported, never
	// captured -- see UncapturedVolume.
	Binds []BindMount
}

// NamedVolume is one named volume and who writes to it.
type NamedVolume struct {
	// Name is the key in the project's `volumes:` block.
	Name string

	// Actual is the volume's name in the runtime, normally the project
	// name and Name joined -- and something else entirely when the volume
	// is external or names itself.
	Actual string

	// External marks a volume the project uses but does not own. It is
	// still captured: the data is the deployment's whether or not Compose
	// created the volume.
	External bool

	// Services are the services that mount it, sorted.
	Services []string
}

// BindMount is a host path a service mounts.
type BindMount struct {
	// Source is the host path.
	Source string

	// Services are the services that mount it, sorted.
	Services []string
}

// ToolInfo is a resolved external binary. The lifecycle layer uses it for
// preflight and doctor; adapters expose it so version checks are not
// re-implemented per adapter.
type ToolInfo struct {
	Name    string         `json:"name"`
	Path    string         `json:"path"`
	Version domain.Version `json:"version"`
	Raw     string         `json:"raw,omitempty"` // unparsed version output
}
