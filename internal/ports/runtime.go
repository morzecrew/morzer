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
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
)

// Runtime is the container runtime: bringing services up, running one-shot
// jobs, and reporting what is running.
//
// v1 is compose. Later: podman-compose, systemd-quadlet, single-node k3s.
type Runtime interface {
	// Name reports which runtime this adapter is, matching the key a
	// manifest declares it under.
	//
	// It exists so the lifecycle layer can refuse an installation whose
	// recorded runtime this adapter is not (RFC 0023 decision 5). The
	// alternative was comparing against a literal up there, which is the
	// branch on a runtime's name decision 7 forbids -- an adapter reporting
	// its own name is data, and the comparison stays a comparison of two
	// values neither of which the lifecycle layer has to recognise.
	//
	// Thirteenth method on this port. §6's escape hatch counts new methods
	// forced by the *second adapter*, and this one is forced by the refusal
	// rather than by Quadlet; recorded so the count is honest when that test
	// is applied.
	Name() string

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
	//
	// The stream is line-oriented and each line is framed:
	//
	//	<container><spaces>| [<RFC 3339 instant> ]<text>
	//
	// The container prefix is not decoration -- it is the only thing that
	// says which of a scaled service's replicas wrote a line, and the
	// manager parses it to build the structured form of `morzer logs`. The
	// instant is present exactly when LogOptions.Timestamps was set. A
	// runtime that framed its lines some other way would produce a
	// structured stream whose every record was attributed to nothing, so
	// the framing is part of this contract and the runtime contract suite
	// asserts it.
	Logs(ctx context.Context, cfg RuntimeConfig, opts LogOptions) (io.ReadCloser, error)

	// Stats reports resource use per container, sampled once.
	//
	// A sample, not a stream: the caller decides the cadence, and a port
	// that returned a channel would put the refresh policy in the adapter,
	// where a second implementation would choose differently.
	//
	// A runtime with no notion of resource accounting wraps
	// domain.ErrUnsupported rather than returning an empty slice. The two
	// look identical in a table and mean opposite things -- "this cannot be
	// answered here" and "nothing is running" -- and the second is the wrong
	// thing to show somebody diagnosing load.
	//
	// Mandatory rather than an optional capability like VolumeCapturer,
	// because every runtime this port targets is a container runtime and
	// every container runtime accounts for a container's memory. What varies
	// is whether the adapter can reach the accounting, which is a refusal
	// with a reason and not an interface it declines to implement.
	Stats(ctx context.Context, cfg RuntimeConfig) ([]ServiceStats, error)
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

	// Timestamps asks the runtime to prefix each line with the instant the
	// container emitted it.
	//
	// Off for the human stream, where the runtime's own layout is what an
	// operator is used to reading, and on for the structured one: a record
	// carrying a `ts` field has to get that instant from the container, and
	// the moment the manager happened to read the line is a different fact
	// wearing the same name.
	Timestamps bool
}

// LogLine is one framed line of a log stream, taken apart.
//
// The text is what the container wrote; everything else is the frame around it,
// which the manager reads so that a machine consumer does not have to.
type LogLine struct {
	// At is when the container emitted the line, and zero when the stream
	// was not asked for timestamps -- never the moment the manager read it,
	// which is a different fact wearing the same name.
	At time.Time `json:"ts,omitzero"`

	// Container is the instance that wrote the line. Service is which
	// service it belongs to, and empty when the line's container is not one
	// the project reported -- a container renamed by `container_name:`, or
	// a line the runtime itself wrote about the stream.
	Container string `json:"container,omitempty"`
	Service   string `json:"service,omitempty"`

	Text string `json:"line"`
}

// ServiceStats is one container's resource use at one instant.
//
// One row per container and never an aggregate. `docker stats` reports
// containers, so a scaled service is several rows -- and a row keyed by service
// alone would silently print one replica's numbers under the service's name.
// Summing is the caller's decision, and not every sum means anything: memory
// adds, a memory limit does not.
type ServiceStats struct {
	// Service is the Compose service; Container and Replica say which
	// instance of it this row is.
	Service   string `json:"service"`
	Container string `json:"container"`

	// Replica is the instance number a runtime that scales a service
	// assigns, and zero when the runtime does not name one. Omitted rather
	// than printed as 0, which would read as a replica index.
	Replica int `json:"replica,omitempty"`

	CPUPercent float64 `json:"cpu_percent"`

	// MemoryBytes is the working set. MemoryLimit is the ceiling the
	// runtime reports, which for a container with no limit of its own is
	// the host's memory rather than zero -- that is what the runtime
	// answers, and inventing "unlimited" from it would be this layer
	// guessing.
	MemoryBytes int64 `json:"memory_bytes"`
	MemoryLimit int64 `json:"memory_limit,omitempty"`

	// The four IO counters, and nil where the host does not account for
	// them -- block IO under a rootless daemon, above all, which is an
	// ordinary configuration and not a fault.
	//
	// Pointers because zero is a real reading: a container that has written
	// nothing reports 0, and a host that cannot say must not be reported as
	// one that can.
	NetRxBytes *int64 `json:"net_rx_bytes"`
	NetTxBytes *int64 `json:"net_tx_bytes"`
	BlockRead  *int64 `json:"block_read_bytes"`
	BlockWrite *int64 `json:"block_write_bytes"`
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
	Name string `json:"name"`

	// Container is the instance this state describes, and empty when the
	// runtime does not name one.
	//
	// A service is not one container: a scaled service is several, and
	// every one of them appears here under the same Name. Without the
	// instance there is nothing to tell two rows apart, and nothing to
	// attribute a log line to.
	Container string `json:"container,omitempty"`

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

// OccupiesVolume reports whether this service may still hold a volume open.
//
// Deliberately not Running(): an unhealthy container still holds its files, and
// a *paused* one is frozen mid-write with its handles open -- neither running
// nor stopped. A restore that asked Running() would untar straight over a
// volume a paused container was holding.
//
// Enumerated by what does *not* occupy, so a state this manager has never seen
// -- a new one from a later runtime -- refuses a restore rather than permitting
// one. Refusing is the safe direction.
//
// It lives on the port rather than in the backup engine because the states are
// the runtime's vocabulary, and every implementation of Runtime is held to the
// same reading of them by the contract suite.
func (s ServiceState) OccupiesVolume() bool {
	switch normaliseServiceState(s.State) {
	case StateExited, StateCreated, StateDead, "":
		// Nothing started, nothing left running, or no container at
		// all: no handles into the volume.
		return false
	default:
		// running, paused, restarting, removing -- and anything new.
		return true
	}
}

// Quiescible reports whether this service can be stopped for a backup *and
// started again afterwards*.
//
// Narrower than OccupiesVolume, and conservative in the opposite direction. The
// two questions are not the same one: "might this hold the volume open" must
// count an unrecognised state, while "can I stop this and put it back" must
// not.
//
// `removing` is why the pair exists. It occupies the volume, so a restore
// refuses against it -- but stopping it and starting it back fails, because by
// then there is no container to start. Collapsing the two turned a transient
// state into a failed nightly backup reporting that the deployment was down.
func (s ServiceState) Quiescible() bool {
	switch normaliseServiceState(s.State) {
	case StateRunning, StatePaused, StateRestarting:
		return true
	default:
		return false
	}
}

// The container states a runtime may report. Compose lowercases them; the
// vocabulary is Docker's and these are the ones the manager reasons about.
const (
	StateRunning    = "running"
	StatePaused     = "paused"
	StateRestarting = "restarting"
	StateRemoving   = "removing"
	StateExited     = "exited"
	StateCreated    = "created"
	StateDead       = "dead"
)

func normaliseServiceState(state string) string {
	return strings.ToLower(strings.TrimSpace(state))
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

// ImageIngester loads the images a bundle carries into the local image store.
//
// Optional and type-asserted, like the capabilities above, and for a sharper
// reason than theirs: ingest is the most runtime-specific operation in this
// interface. It exists because a local image store has an idea of which
// registry an image came from, and a bundle is not a registry -- so the way in
// is whatever that particular runtime's store will accept. A runtime with no
// local store has nothing to ingest into, and a mandatory method would make it
// answer a question it cannot have.
//
// The contract is about the outcome, not the mechanism: after IngestImages
// returns without error, every image named in refs resolves locally under the
// name domain.ImageSpec.LocalAlias derives for it. How the bytes got there is
// the adapter's business.
type ImageIngester interface {
	// IngestImages makes the images in an OCI layout resolvable locally.
	//
	// layoutDir is the layout -- the directory holding oci-layout,
	// index.json and blobs/. refs are the images to ingest, by the
	// reference the manifest pins; the implementation derives from each
	// the digest to fetch and the local name to leave behind, so that the
	// caller and the adapter cannot disagree about either.
	//
	// Idempotent: an image already present is not fetched again, which is
	// what lets a failed update be retried without re-reading gigabytes.
	IngestImages(ctx context.Context, layoutDir string, refs []string) error
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
	//
	// A bound rather than a measurement, and one that must err high. Only
	// one direction is dangerous: a size reported too small passes the
	// space check and fills the disk during the copy, which happens after
	// the services have been stopped for it.
	//
	// An error refuses the backup. That is the default because the
	// alternative is a backup admitted onto a disk nobody checked, and it
	// applies to every failure an implementation does not explicitly
	// exempt -- including ones it has not thought of. The one exemption is
	// domain.ErrMeasureIncomplete, which an implementation wraps when the
	// measurement did not *run*: nothing was learned about the volume, it
	// may work next time, and refusing every backup of a deployment over a
	// helper that exits non-zero on one awkward volume is its own failure.
	// A measurement that ran and produced something unusable is not that
	// case and must not be marked -- it is a property of this helper in
	// this environment, and it will produce the same nothing tomorrow.
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

	// Anonymous are volumes a service mounts without naming, sorted.
	//
	// Reported and never captured, for a reason worth stating: the runtime
	// invents a name that changes when the container is recreated, so there
	// is nothing a later restore could put the contents back into. Silence
	// would be worse than the limitation -- a vendor who wrote `- /data`
	// instead of `- data:/data` has a volume holding real data that no
	// backup covers and none can.
	Anonymous []AnonymousVolume
}

// AnonymousVolume is an unnamed mount: real storage with no stable identity.
type AnonymousVolume struct {
	// Service is the service that mounts it.
	Service string

	// Target is the path it is mounted at inside the container, which is
	// the only handle an operator has for finding it.
	Target string
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
