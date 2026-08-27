// Package preflight holds the checks that run before any mutation.
//
// Every check here answers a question that is cheaper to answer now than to
// discover halfway through an operation. A missing tool, a full disk, or an
// occupied port are all knowable before the deployment lock is even taken --
// and finding them at step nine of `apply`, after images have been pulled and
// migrations run, is the difference between a message and an incident.
package preflight

import (
	"context"
	"fmt"
	"maps"
	"math"
	"net"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/infra/tools"
)

// Check is one preflight question.
type Check struct {
	ID          string
	Category    string
	Description string

	// Fatal marks a check whose failure stops the operation. A non-fatal
	// failure becomes a warning: not every requirement a release states is
	// one the manager should refuse to proceed without.
	Fatal bool

	Run func(context.Context) events.CheckResult
}

// Report is the outcome of a preflight run.
type Report struct {
	Results []events.CheckResult `json:"results"`
	Worst   events.CheckStatus   `json:"worst"`
}

// Failed reports whether any fatal check failed.
func (r Report) Failed() bool { return r.Worst == events.CheckFail }

// Err converts a failed report into a typed preflight error listing every
// blocker, so an operator fixing one does not discover the next on the retry.
func (r Report) Err() error {
	if !r.Failed() {
		return nil
	}
	var problems []string
	for _, res := range r.Results {
		if res.Status == events.CheckFail {
			line := res.Description + ": " + res.Message
			if res.Remedy != "" {
				line += "\n      " + res.Remedy
			}
			problems = append(problems, line)
		}
	}
	return domain.Preflight(nil, "preflight checks failed:\n  - %s", strings.Join(problems, "\n  - ")).
		WithHint("run `morzer doctor` for the full diagnostic")
}

// Runner executes a set of checks.
type Runner struct {
	bus *events.Bus
}

func NewRunner(bus *events.Bus) *Runner { return &Runner{bus: bus} }

// Run executes checks in order, publishing each result.
//
// All checks run even after one fails: an operator wants the full list of what
// is wrong, not the first thing the manager happened to notice.
func (r *Runner) Run(ctx context.Context, checks []Check) Report {
	report := Report{Worst: events.CheckOK}

	for _, check := range checks {
		if err := ctx.Err(); err != nil {
			break
		}

		started := time.Now()
		result := check.Run(ctx)
		result.ID = check.ID
		result.Category = check.Category
		if result.Description == "" {
			result.Description = check.Description
		}
		result.Duration = time.Since(started)

		// A non-fatal check that failed is a warning: the release said
		// it wanted something, the machine does not have it, and the
		// operator gets to decide.
		if result.Status == events.CheckFail && !check.Fatal {
			result.Status = events.CheckWarn
		}

		report.Results = append(report.Results, result)
		report.Worst = report.Worst.Worse(result.Status)

		if r.bus != nil {
			r.bus.Publish(events.Check(result))
		}
	}
	return report
}

// Result constructors, so a check body reads as a statement rather than a
// struct literal.

func OK(format string, args ...any) events.CheckResult {
	return events.CheckResult{Status: events.CheckOK, Message: fmt.Sprintf(format, args...)}
}

func Warn(remedy string, format string, args ...any) events.CheckResult {
	return events.CheckResult{
		Status: events.CheckWarn, Message: fmt.Sprintf(format, args...), Remedy: remedy,
	}
}

func Fail(remedy string, format string, args ...any) events.CheckResult {
	return events.CheckResult{
		Status: events.CheckFail, Message: fmt.Sprintf(format, args...), Remedy: remedy,
	}
}

// Categories group results in the doctor view.
const (
	CategorySystem  = "system"
	CategoryTools   = "tools"
	CategoryStorage = "storage"
	CategoryNetwork = "network"
	CategoryConfig  = "configuration"
	CategorySecrets = "secrets"
	CategoryRuntime = "runtime"
	CategoryBackup  = "backup"

	// CategoryMachine is what the host holds besides this installation.
	//
	// Its own category because it is the only one whose subject is not the
	// installation being diagnosed: a machine with two deployments on it is
	// a supported arrangement, and the checks that describe the sharing
	// belong beside each other rather than scattered through the ones about
	// this deployment's own storage and network.
	CategoryMachine = "machine"
)

// Architecture checks that the release supports this CPU architecture.
func Architecture(req domain.Requirements) Check {
	return Check{
		ID:          "system.architecture",
		Category:    CategorySystem,
		Description: "CPU architecture is supported by the release",
		Fatal:       true,
		Run: func(context.Context) events.CheckResult {
			if len(req.Architectures) == 0 {
				return OK("the release states no architecture requirement")
			}
			for _, a := range req.Architectures {
				if a == runtime.GOARCH {
					return OK("%s", runtime.GOARCH)
				}
			}
			return Fail(
				"obtain a bundle built for "+runtime.GOARCH+", or run on a supported architecture",
				"this machine is %s; the release supports %s",
				runtime.GOARCH, strings.Join(req.Architectures, ", "))
		},
	}
}

// OperatingSystem checks the host distribution against the release.
//
// A mismatch is a warning rather than a failure: the OS list in a manifest is
// what the vendor tested, not a hard technical bound, and refusing to run on
// an untested-but-compatible distribution would be the manager overreaching.
func OperatingSystem(req domain.Requirements) Check {
	return Check{
		ID:          "system.os",
		Category:    CategorySystem,
		Description: "host OS is one the release was tested against",
		Fatal:       false,
		Run: func(context.Context) events.CheckResult {
			if len(req.OS) == 0 {
				return OK("the release states no OS requirement")
			}

			id, version := hostOS()
			if id == "" {
				return Warn("", "cannot identify the host OS from /etc/os-release")
			}

			var wanted []string
			for _, spec := range req.OS {
				wanted = append(wanted, spec.ID)
				if !strings.EqualFold(spec.ID, id) {
					continue
				}
				if spec.Version.IsZero() {
					return OK("%s %s", id, version)
				}
				v, err := domain.ParseVersion(normaliseOSVersion(version))
				if err != nil {
					return Warn("", "%s %s (version not comparable)", id, version)
				}
				if spec.Version.Allows(v) {
					return OK("%s %s", id, version)
				}
				return Warn(
					"the release was tested against "+spec.ID+" "+spec.Version.String(),
					"%s %s is outside the tested range %s", id, version, spec.Version)
			}
			return Warn(
				"the release was tested on "+strings.Join(wanted, ", "),
				"%s %s is not in the release's tested OS list", id, version)
		},
	}
}

// hostOS reads /etc/os-release.
func hostOS() (id, version string) {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "ID":
			id = v
		case "VERSION_ID":
			version = v
		}
	}
	return id, version
}

// normaliseOSVersion turns "22.04" into "22.04.0" so semver can compare it.
func normaliseOSVersion(v string) string {
	if v == "" {
		return "0.0.0"
	}
	parts := strings.Split(v, ".")
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	return strings.Join(parts[:3], ".")
}

// Tool checks one required binary and its version.
func Tool(registry *tools.Registry, name string, constraint domain.Constraint) Check {
	desc := "required tool: " + name
	if !constraint.IsZero() {
		desc = fmt.Sprintf("required tool: %s %s", name, constraint)
	}
	return Check{
		ID:          "tools." + name,
		Category:    CategoryTools,
		Description: desc,
		Fatal:       true,
		Run: func(ctx context.Context) events.CheckResult {
			info, err := registry.Require(ctx, name, constraint)
			if err != nil {
				e := domain.AsError(err)
				return Fail(e.Hint, "%s", e.Message)
			}
			return OK("%s", info.Version)
		},
	}
}

// Tools builds a check per entry in requirements.tools, in a stable order.
func Tools(registry *tools.Registry, req domain.Requirements) []Check {
	names := slices.Sorted(maps.Keys(req.Tools))

	checks := make([]Check, 0, len(names))
	for _, name := range names {
		checks = append(checks, Tool(registry, name, req.Tools[name]))
	}
	return checks
}

// Disk checks free space on the filesystem holding a path.
func Disk(path string, required domain.ByteSize) Check {
	return Check{
		ID:          "storage.disk",
		Category:    CategoryStorage,
		Description: "free disk space",
		Fatal:       true,
		Run: func(context.Context) events.CheckResult {
			if required <= 0 {
				return OK("the release states no disk requirement")
			}

			free, err := FreeSpace(path)
			if err != nil {
				return Warn("", "cannot determine free space on %s: %v", path, err)
			}
			if free < required.Bytes() {
				return Fail(
					"free up space, or move the data directory to a larger filesystem",
					"%s has %s free, the release needs %s",
					path, domain.ByteSize(free), required)
			}
			return OK("%s free on %s", domain.ByteSize(free), path)
		},
	}
}

// FreeSpace returns the bytes available to an unprivileged process.
//
// The implementation moved to atomicfs so an adapter can reach it -- a volume
// capture measures before it copies. This stays as the name preflight's own
// callers already use.
func FreeSpace(path string) (int64, error) { return atomicfs.FreeSpace(path) }

// Ports checks that the ports a release needs are free.
//
// A port held by the product's own containers is not a conflict -- `apply` on
// a running deployment must not report its own listener as a blocker -- so
// this check is only applied when the project is not already up.
func Ports(required []int) Check {
	return Check{
		ID:          "network.ports",
		Category:    CategoryNetwork,
		Description: "required ports are available",
		Fatal:       true,
		Run: func(context.Context) events.CheckResult {
			if len(required) == 0 {
				return OK("the release requires no specific ports")
			}

			var occupied []string
			for _, port := range required {
				if !portFree(port) {
					occupied = append(occupied, strconv.Itoa(port))
				}
			}
			if len(occupied) > 0 {
				return Fail(
					"stop whatever is listening (`ss -tlnp`), or change the release's port mapping",
					"port %s already in use", strings.Join(occupied, ", "))
			}
			return OK("%d port(s) available", len(required))
		},
	}
}

func portFree(port int) bool {
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// Directories checks that the managed directories exist with the right modes.
func Directories(paths domain.Paths) Check {
	return Check{
		ID:          "storage.directories",
		Category:    CategoryStorage,
		Description: "managed directories exist with correct permissions",
		Fatal:       false,
		Run: func(context.Context) events.CheckResult {
			var problems []string
			for _, dir := range paths.ManagedDirs() {
				info, err := os.Stat(dir.Path)
				if err != nil {
					problems = append(problems, dir.Path+": missing")
					continue
				}
				if !info.IsDir() {
					problems = append(problems, dir.Path+": not a directory")
					continue
				}
				if got := uint32(info.Mode().Perm()); got != dir.Mode {
					problems = append(problems,
						fmt.Sprintf("%s: mode %04o, expected %04o", dir.Path, got, dir.Mode))
				}
			}
			if len(problems) > 0 {
				return Warn(
					"run `morzer init --repair` to restore the expected layout",
					"%s", strings.Join(problems, "; "))
			}
			return OK("all %d managed directories are correct", len(paths.ManagedDirs()))
		},
	}
}

// SecretsPresent checks that every required secret is set.
func SecretsPresent(schema domain.SecretSchema, set domain.SecretSet) Check {
	return Check{
		ID:          "secrets.required",
		Category:    CategorySecrets,
		Description: "all required secrets are set",
		Fatal:       true,
		Run: func(context.Context) events.CheckResult {
			missing := schema.Missing(set)
			if len(missing) == 0 {
				return OK("%d secret(s) present", set.Len())
			}
			return Fail(
				"run `morzer secret set <name>`, or `morzer secret generate <name>` "+
					"for secrets the release can generate",
				"missing required secret(s): %s", strings.Join(missing, ", "))
		},
	}
}

// Memory checks total system memory against the release requirement.
//
// A warning rather than a failure: the check reads total RAM, which says
// nothing about swap or about how much the product actually needs at rest.
// Refusing to start on it would be the manager guessing.
func Memory(required domain.ByteSize) Check {
	return Check{
		ID:          "system.memory",
		Category:    CategorySystem,
		Description: "system memory meets the release requirement",
		Fatal:       false,
		Run: func(context.Context) events.CheckResult {
			if required <= 0 {
				return OK("the release states no memory requirement")
			}
			total, err := totalMemory()
			if err != nil {
				return Warn("", "cannot determine system memory: %v", err)
			}
			if total < required.Bytes() {
				return Warn(
					"the product may be unstable or fail to start under memory pressure",
					"this machine has %s, the release recommends %s",
					domain.ByteSize(total), required)
			}
			return OK("%s", domain.ByteSize(total))
		},
	}
}

// CPUs checks the machine's available parallelism against the release.
//
// A warning rather than a failure, matching Memory: a machine with fewer cores
// than the vendor recommends is slow, not broken, and refusing to start on it
// would be the manager overruling an operator who may know their workload
// better than the vendor's default guess.
//
// "Available" means logical CPUs, narrowed by a cgroup quota where one is in
// force. Those are the three things a machine can mean by "how many CPUs" --
// physical cores, logical CPUs, a quota -- and the one that decides how much
// parallelism the product gets is the last that applies.
func CPUs(required int) Check {
	return Check{
		ID:          "system.cpus",
		Category:    CategorySystem,
		Description: "available CPUs meet the release requirement",
		Fatal:       false,
		Run: func(context.Context) events.CheckResult {
			if required <= 0 {
				return OK("the release states no CPU requirement")
			}
			available := availableCPUs()
			if available < required {
				return Warn(
					"the product may be slow under load",
					"this machine offers %d CPU(s), the release recommends %d",
					available, required)
			}
			return OK("%d CPU(s)", available)
		},
	}
}

// cgroupCPUMax is where cgroup v2 records a CPU quota. A manager running inside
// a container sees every host CPU through runtime.NumCPU and is allowed to use
// far fewer.
const cgroupCPUMax = "/sys/fs/cgroup/cpu.max"

func availableCPUs() int {
	logical := runtime.NumCPU()

	data, err := os.ReadFile(cgroupCPUMax)
	if err != nil {
		return logical
	}
	allowed, ok := quotaCPUs(string(data))
	if !ok || allowed >= logical {
		return logical
	}
	return allowed
}

// quotaCPUs reads a cgroup v2 `cpu.max` value.
//
// Split out from availableCPUs so the rounding is testable: what a quota
// rounds to is a decision, and one that depends on how many cores the test
// machine happens to have is a decision nothing pins.
//
// The format is "<quota> <period>", or "max <period>" for no quota at all.
// Anything else reports false and the caller keeps the OS count -- a cgroup
// file it cannot parse is not evidence of a narrower limit.
func quotaCPUs(content string) (int, bool) {
	fields := strings.Fields(content)
	if len(fields) != 2 || fields[0] == "max" {
		return 0, false
	}
	// ParseFloat accepts "NaN", "Inf" and "Infinity" without error, and NaN
	// survives every ordering comparison below -- `NaN < 0` is false -- so
	// it reached int(math.Floor(NaN)) and came out as one CPU. The kernel
	// writes no such value, which is exactly why nothing downstream would
	// have questioned it.
	quota, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || math.IsNaN(quota) || math.IsInf(quota, 0) || quota < 0 {
		return 0, false
	}
	period, err := strconv.ParseFloat(fields[1], 64)
	if err != nil || math.IsNaN(period) || math.IsInf(period, 0) || period <= 0 {
		return 0, false
	}

	// Rounded down, and never below one. Down because a 1.5-CPU quota does
	// not give a product that asked for two cores what it asked for, and
	// rounding up would hide exactly the shortfall this check exists to
	// report; never below one because a 0.5-CPU quota is not zero CPUs, and
	// reporting zero would fail every requirement including `cpus: 1`.
	allowed := int(math.Floor(quota / period))
	if allowed < 1 {
		allowed = 1
	}
	return allowed, true
}

// RequiredParameters refuses to deploy with a required parameter unset.
//
// Fatal, and checked on every apply rather than only where a value can be
// supplied. An unset parameter that merely has no default is the operator's
// business; an unset *required* one is the vendor stating the product will not
// work -- so a release that introduces one fails to deploy rather than
// deploying with an empty value the product then misreads. The current release
// keeps running, which is the safe side of that trade.
func RequiredParameters(declared map[string]domain.ParameterSpec, set map[string]string) Check {
	return Check{
		ID:          "config.required-parameters",
		Category:    CategoryConfig,
		Description: "every required parameter has a value",
		Fatal:       true,
		Run: func(context.Context) events.CheckResult {
			missing := domain.MissingRequired(declared, set)
			if len(missing) == 0 {
				return OK("all required parameters are set")
			}
			return Fail(
				fmt.Sprintf("set them with `morzer config set %s=<value>`", missing[0]),
				"the release requires a value for %s", strings.Join(missing, ", "))
		},
	}
}

// NoUnfinishedOperation refuses to start while a previous operation is still
// flagged.
//
// Proceeding over an unfinished operation would layer new changes on a state
// nobody has confirmed, which is exactly how a recoverable failure becomes an
// unrecoverable one.
func NoUnfinishedOperation(records []domain.OperationRecord) Check {
	return Check{
		ID:          "config.unfinished-operation",
		Category:    CategoryConfig,
		Description: "no previous operation needs attention",
		Fatal:       true,
		Run: func(context.Context) events.CheckResult {
			if len(records) == 0 {
				return OK("no unfinished operations")
			}

			rec := records[0]
			if rec.Status.NeedsAttention() {
				return Fail(
					"run `morzer doctor` to see what happened, repair the system, "+
						"then clear the flag with `morzer status --clear-intervention`",
					"operation %s (%s) requires manual intervention", rec.ID, rec.Type)
			}
			// Only apply and update implement --resume; advertising it
			// for a crashed backup or restore would send the operator
			// down a road that does not exist.
			remedy := "investigate with `morzer doctor`, then acknowledge the record " +
				"with `morzer status --clear-intervention`"
			switch rec.Type {
			case domain.OpTypeApply, domain.OpTypeUpdate:
				remedy = fmt.Sprintf("resume it with `morzer %s --resume`, "+
					"or investigate with `morzer doctor`", rec.Type)
			}
			return Fail(remedy,
				"operation %s (%s) did not finish", rec.ID, rec.Type)
		},
	}
}

// ImagePresence answers whether an image is already in the local store.
//
// A function rather than a port interface, because preflight is the one layer
// below ports: it is handed the question, not the runtime that answers it.
type ImagePresence func(ctx context.Context, ref string) (bool, error)

// BundledImages refuses to converge when an image the bundle carries is not in
// the local store.
//
// A refusal, and fatal, and both are the point. An image that travels in the
// bundle is deployed under an alias the manager creates -- a tag, because a
// local store cannot be made to answer to a digest reference for a registry it
// never contacted. A tag is mutable. So if this check merely warned and let
// the converge proceed, Compose would resolve that tag the only way it can
// when the image is absent: by asking the vendor's registry for it. A
// digest-pinned deployment would then be running whatever that tag pointed at,
// nobody having verified any of it, and the manifest's pinning -- the rule
// that makes a release immutable -- would have decided nothing.
//
// The remedy is a command rather than advice, because there is exactly one:
// the bytes are in the bundle already, and something has to put them in the
// store.
// Takes the manifest's references and derives each alias from its digest, so
// there is no second list to fall out of step with the first -- the check
// would then report on an image nobody deployed, or miss the one they did.
func BundledImages(refs []string, has ImagePresence) Check {
	return Check{
		ID:          "images.bundled",
		Category:    CategoryRuntime,
		Description: "images the bundle carries are loaded",
		Fatal:       true,
		Run: func(ctx context.Context) events.CheckResult {
			if len(refs) == 0 {
				return OK("the release bundles no images")
			}
			if has == nil {
				// No inspector: a runtime with no local image
				// store has no answer, and inventing one in
				// either direction would be worse than saying
				// so. Not a failure, because the deployment may
				// well work -- this check simply cannot tell.
				return OK("the configured runtime has no local image store")
			}

			var missing []string
			for _, ref := range refs {
				alias, ok := domain.ImageSpec{Ref: ref}.LocalAlias()
				if !ok {
					// Unreachable through a validated
					// manifest, whose pinning rule refuses
					// an unpinned reference first. A
					// refusal rather than a skip, because
					// the alternative is passing this image
					// silently and letting Compose decide
					// what an unpinned bundled image means.
					return Fail(
						"pin it by digest, as every image in a manifest must be",
						"the bundled image %q is not pinned by digest", ref)
				}
				present, err := has(ctx, alias)
				if err != nil {
					// "Cannot tell" is not "absent", and the
					// difference decides whether an operator
					// starts the daemon or runs an ingest.
					return Fail(
						"check that the container runtime is running",
						"cannot check the local image store: %s",
						domain.AsError(err).Message)
				}
				if !present {
					missing = append(missing, domain.ShortImageRef(ref))
				}
			}

			if len(missing) == 0 {
				return OK("all %d bundled image(s) are loaded", len(refs))
			}
			return Fail(
				"run `morzer release ingest` to load them out of the bundle",
				"%d of %d bundled image(s) are not in the local image store: %s",
				len(missing), len(refs), strings.Join(missing, ", "))
		},
	}
}
