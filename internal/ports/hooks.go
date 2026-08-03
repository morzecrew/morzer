package ports

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
)

// HookRunner executes the product-specific executables a release ships.
//
// Hooks are the only way to add product logic without changing the manager, so
// the ABI is a public contract. The types describing it live here rather than
// in the adapter because the lifecycle layer builds hook environments and reads
// hook results on every operation -- if they lived in the adapter, the layer
// that composes ports would depend on one.
type HookRunner interface {
	// Run executes a hook from a verified release. A non-zero exit that the
	// ABI gives a meaning to -- "nothing to do" -- is reported through the
	// outcome rather than as an error.
	Run(ctx context.Context, rel domain.Release, command []string, env HookEnv, timeout time.Duration) (HookOutcome, error)
}

// HookPhase is the lifecycle point a hook is invoked at.
type HookPhase string

const (
	PhasePreflight   HookPhase = "preflight"
	PhasePreUpdate   HookPhase = "pre-update"
	PhasePostUpdate  HookPhase = "post-update"
	PhaseMigrate     HookPhase = "migrate"
	PhaseSmokeTest   HookPhase = "smoke-test"
	PhaseBackup      HookPhase = "backup"
	PhaseRestore     HookPhase = "restore"
	PhaseHealthCheck HookPhase = "health-check"
)

// HookEnv is everything a hook is told about the world it runs in.
//
// The field set is the stable part of the ABI: adding a variable is a minor
// change, removing or repurposing one is not.
type HookEnv struct {
	Product        string
	InstallationID string
	OperationID    string
	OperationType  domain.OperationType
	Phase          HookPhase

	ReleaseVersion  domain.Version
	ReleaseDir      string
	PreviousVersion domain.Version

	DataDir    string
	BackupDir  string
	SecretsDir string
	ConfigFile string

	ComposeProject string

	DryRun   bool
	LogLevel string

	// Extra carries operation-specific variables, already fully named.
	Extra map[string]string
}

// Prefix is the environment-variable namespace for this product.
//
// Variables are namespaced per product rather than under a fixed prefix because
// hooks ship inside a product's own release: the author always knows the name,
// and namespacing keeps two products' hooks from colliding.
func (e HookEnv) Prefix() string {
	p := strings.ToUpper(e.Product)
	p = strings.ReplaceAll(p, "-", "_")
	p = strings.ReplaceAll(p, ".", "_")
	if p == "" {
		return "PRODUCT"
	}
	return p
}

// Var builds a namespaced variable name for this product.
func (e HookEnv) Var(key string) string { return e.Prefix() + "_" + key }

// HookEnvVars renders a hook environment as the variable map the ABI defines.
//
// It lives here rather than in the adapter because the same naming is used for
// Compose interpolation and for command health checks, which the lifecycle
// layer sets up directly. One definition means the two cannot drift.
func HookEnvVars(e HookEnv) map[string]string {
	out := make(map[string]string, 16)

	set := func(key, value string) {
		if value != "" {
			out[e.Var(key)] = value
		}
	}
	set("PRODUCT", e.Product)
	set("INSTALLATION_ID", e.InstallationID)
	set("OPERATION_ID", e.OperationID)
	set("OPERATION_TYPE", string(e.OperationType))
	set("PHASE", string(e.Phase))
	set("RELEASE_VERSION", e.ReleaseVersion.String())
	set("RELEASE_DIR", e.ReleaseDir)
	set("PREVIOUS_VERSION", e.PreviousVersion.String())
	set("DATA_DIR", e.DataDir)
	set("BACKUP_DIR", e.BackupDir)
	set("SECRETS_DIR", e.SecretsDir)
	set("CONFIG_FILE", e.ConfigFile)
	set("COMPOSE_PROJECT", e.ComposeProject)
	set("LOG_LEVEL", e.LogLevel)

	// DRY_RUN is always present, including as "0". A hook testing for the
	// variable's existence rather than its value would otherwise mutate
	// during a plan.
	out[e.Var("DRY_RUN")] = "0"
	if e.DryRun {
		out[e.Var("DRY_RUN")] = "1"
	}
	out[e.Var("RESULT_FD")] = strconv.Itoa(HookResultFD)

	for k, v := range e.Extra {
		out[k] = v
	}
	return out
}

// HookResultFD is the descriptor a hook writes its structured result to.
//
// Not stdout: stdout goes to the log and the live view, and a hook forced to
// keep its human output free of JSON would be one whose logging is constrained
// by the manager's parsing.
const HookResultFD = 3

// Hook exit codes. Anything not listed is a failure.
const (
	// HookExitSuccess means the hook did its work.
	HookExitSuccess = 0
	// HookExitSkipped means there was nothing to do. Distinct from success
	// so `apply` can report "migrations: nothing to run" rather than
	// implying work happened.
	HookExitSkipped = 2
)

// HookResult is what a hook may report through its result descriptor. Every
// field is optional: a hook that writes nothing is not in error.
type HookResult struct {
	// Message is a one-line summary for the operator.
	Message string `json:"message,omitempty"`

	// Skipped lets a hook say it did nothing while still exiting zero.
	Skipped bool `json:"skipped,omitempty"`

	// SchemaVersion is how a migrate hook reports the database schema it
	// left behind. Rollback needs it, and asking the product later would
	// mean running its tooling to pose a question it already answered.
	SchemaVersion int `json:"schema_version,omitempty"`

	// Artifacts are files the hook produced, e.g. a database dump.
	Artifacts []HookArtifact `json:"artifacts,omitempty"`

	// Data is free-form output for hooks with something else to say.
	Data map[string]any `json:"data,omitempty"`
}

// HookArtifact is a file a hook produced, with its checksum so a backup
// manifest can be self-describing.
type HookArtifact struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

// HookOutcome is the full result of running a hook.
type HookOutcome struct {
	ExitCode int
	Skipped  bool
	Result   HookResult
	Stdout   string
	Stderr   string
	Duration time.Duration
}
