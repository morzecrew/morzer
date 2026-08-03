// Package ops implements the operations the CLI exposes, each as a sequence
// of engine steps.
//
// An operation here is a plan, not a procedure: it assembles steps that close
// over ports and hands them to the engine, which owns ordering, journaling and
// compensation. That separation is what makes adding a stage to `apply` a
// matter of adding a step rather than editing a monolith, and what lets the
// whole thing be tested against in-memory adapters with no Docker and no root.
package ops

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/logging"
	"github.com/morzecrew/morzer/internal/infra/tools"
	"github.com/morzecrew/morzer/internal/lifecycle/engine"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

// Deps is everything an operation needs. It is one struct rather than a
// parameter list because the set is stable and long, and because assembling it
// in exactly one place -- the CLI's wiring -- is what keeps adapters out of
// the lifecycle layer.
//
// Not every operation uses every field: `status` needs no BackupEngine, and
// `doctor` tolerates a nil Supervisor on a host without systemd. Operations
// check what they need.
type Deps struct {
	Paths domain.Paths

	State  ports.StateStore
	Locker ports.Locker

	Runtime    ports.Runtime
	Secrets    ports.SecretStore
	Backup     ports.BackupEngine
	Source     ports.ReleaseSource
	Verifier   ports.Verifier
	Health     ports.HealthWaiter
	Supervisor ports.Supervisor
	Renderer   ports.Renderer
	Notifier   ports.Notifier

	Hooks ports.HookRunner
	Tools *tools.Registry

	Bus    *events.Bus
	Engine *engine.Engine

	ManagerVersion domain.Version

	// Redactor receives secret values as they are loaded, so the log
	// handler and the exec runner scrub them from that point on.
	Redactor *logging.Redactor

	// NewOpID generates operation IDs. Injectable so tests get stable
	// journal output.
	NewOpID func(time.Time) string

	// Now is the clock. Injectable for the same reason.
	Now func() time.Time

	// ManagerPath is the absolute path of this binary, embedded in the
	// supervisor units so they do not depend on PATH.
	ManagerPath string

	// TargetPrefix relocates the absolute paths a manifest declares --
	// configuration targets above all -- underneath a prefix.
	//
	// It backs the hidden --root flag. Manifest targets are required to be
	// absolute, so without this a "relocated" installation would still
	// write its configuration to the real /etc, and --root would be a flag
	// that only half works. Empty in production.
	TargetPrefix string
}

// configTarget resolves a manifest configuration target, honouring the
// relocation prefix.
func (d *Deps) configTarget(target string) string {
	if d.TargetPrefix == "" {
		return target
	}
	return filepath.Join(d.TargetPrefix, target)
}

// Options are the flags an operation honours. They come from the CLI layer and
// are recorded in the journal where they are consequential.
type Options struct {
	DryRun bool
	Resume bool

	// Yes is non-interactive confirmation. It does not by itself authorise
	// a destructive action; Force does.
	Yes bool

	// Force authorises destructive operations. Every use is journaled.
	Force bool

	// Startup marks the run as systemd's boot-time apply, which skips
	// pulls when images are already local and skips migrations when the
	// schema is current.
	Startup bool

	// Wait blocks on the deployment lock rather than failing when it is
	// held.
	Wait bool

	// Timeout bounds the whole operation.
	Timeout time.Duration

	// SkipBackup omits the pre-update backup. Requires Force, and is
	// journaled so an incident review can see the choice.
	SkipBackup bool

	// Profile overrides the installation's deployment profile.
	Profile string
}

// NewOperationID returns a ULID: lexicographically sortable and
// timestamp-prefixed, which is what makes the append-only journal readable in
// file order.
func NewOperationID(t time.Time) string {
	return "op_" + ulid.MustNew(ulid.Timestamp(t), rand.Reader).String()
}

func (d *Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Deps) newOpID() string {
	if d.NewOpID != nil {
		return d.NewOpID(d.now())
	}
	return NewOperationID(d.now())
}

// engineOptions translates operation options into engine options.
func (d *Deps) engineOptions(opts Options, installationID string, prior *domain.OperationRecord) engine.Options {
	return engine.Options{
		DryRun:         opts.DryRun,
		Resume:         opts.Resume,
		Prior:          prior,
		ManagerVersion: d.ManagerVersion.String(),
		InstallationID: installationID,
		Timeout:        opts.Timeout,
	}
}

// withLock takes the deployment lock for the duration of fn.
//
// Every mutating command goes through here. A dry run does not: planning
// mutates nothing, and making `--dry-run` wait on a running update would
// defeat the point of being able to inspect a plan while one is in flight.
func (d *Deps) withLock(ctx context.Context, opID string, opType domain.OperationType, opts Options, fn func(context.Context) error) error {
	if opts.DryRun {
		return fn(ctx)
	}

	release, err := d.Locker.Acquire(ctx, "deployment", ports.LockOptions{
		Wait: opts.Wait,
		Owner: ports.LockOwner{
			PID:         os.Getpid(),
			OperationID: opID,
			Type:        string(opType),
			StartedAt:   domain.NewTime(d.now()),
		},
	})
	if err != nil {
		return err
	}
	defer func() {
		if err := release(); err != nil {
			logging.FromContext(ctx).Error("cannot release the deployment lock", "error", err)
		}
	}()

	return fn(ctx)
}

// notify sends an event to the configured notifier.
//
// Failures are logged and dropped. A webhook being down must never change the
// outcome of a deployment, and an operator must not learn about a Slack outage
// by way of a rolled-back update.
func (d *Deps) notify(ctx context.Context, ev events.Event) {
	if d.Notifier == nil {
		return
	}
	if err := d.Notifier.Notify(ctx, ev); err != nil {
		logging.FromContext(ctx).Warn("notifier failed",
			"notifier", d.Notifier.Name(), "error", err)
	}
}

// loadInstallation reads the installation, producing an actionable error when
// there is none.
func (d *Deps) loadInstallation(ctx context.Context) (domain.Installation, error) {
	exists, err := d.State.InstallationExists(ctx)
	if err != nil {
		return domain.Installation{}, err
	}
	if !exists {
		return domain.Installation{}, domain.InstallationError(domain.ErrInstallation,
			"no installation found at %s", d.Paths.EtcDir).
			WithHint("run `morzer init` to create one")
	}
	return d.State.LoadInstallation(ctx)
}

// hookEnv builds the hook ABI environment for an operation.
func (d *Deps) hookEnv(
	inst domain.Installation,
	rel domain.Release,
	previous domain.Version,
	opID string,
	opType domain.OperationType,
	phase ports.HookPhase,
	dryRun bool,
) ports.HookEnv {
	return ports.HookEnv{
		Product:         inst.Product,
		InstallationID:  inst.ID,
		OperationID:     opID,
		OperationType:   opType,
		Phase:           phase,
		ReleaseVersion:  rel.Version(),
		ReleaseDir:      rel.Root,
		PreviousVersion: previous,
		DataDir:         d.Paths.DataDir(),
		BackupDir:       d.Paths.BackupsDir(),
		SecretsDir:      d.Paths.SecretsRenderDir(),
		ConfigFile:      d.Paths.ApplicationFile(),
		ComposeProject:  rel.Manifest.Runtime.Project,
		DryRun:          dryRun,
		LogLevel:        "info",
		// Best effort: a hook environment is built on paths that do
		// not fail, and a parameter that cannot resolve is already
		// refused by the steps that matter -- render, apply, health.
		// Failing here would turn a bad parameter into a crash in the
		// middle of building a log line.
		Parameters: d.parametersOrEmpty(rel, inst),
	}
}

// parametersOrEmpty resolves parameters, returning nothing on failure.
func (d *Deps) parametersOrEmpty(rel domain.Release, inst domain.Installation) map[string]string {
	params, err := d.parameters(rel, inst)
	if err != nil {
		return nil
	}
	return params
}

// parameters resolves the release's declarations against the operator's
// choices.
//
// One resolution point, because every consumer -- Compose, the templates, the
// hooks, the port preflight, the health probes -- must see the same values.
// Resolving twice is how the published port and the probed port drift apart,
// which is the defect this closes.
func (d *Deps) parameters(rel domain.Release, inst domain.Installation) (domain.Parameters, error) {
	return domain.ResolveParameters(rel.Manifest.Parameters, inst.Parameters)
}

// runtimeConfig builds the runtime configuration for a release and profile.
func (d *Deps) runtimeConfig(rel domain.Release, inst domain.Installation, profile string) (ports.RuntimeConfig, error) {
	if profile == "" {
		profile = inst.Profile
	}

	files, err := rel.ComposeFilePaths(profile)
	if err != nil {
		return ports.RuntimeConfig{}, err
	}

	// These are the variables Compose files may interpolate. Secrets are
	// absent by design: they reach containers as files under /run, never
	// as environment, so that `docker inspect` cannot print them.
	env := map[string]string{
		envName(inst.Product, "DATA_DIR"):    d.Paths.DataDir(),
		envName(inst.Product, "SECRETS_DIR"): d.Paths.SecretsRenderDir(),
		envName(inst.Product, "CONFIG_FILE"): d.Paths.ApplicationFile(),
		envName(inst.Product, "RELEASE_DIR"): rel.Root,
		envName(inst.Product, "VERSION"):     rel.Version().String(),
		envName(inst.Product, "PROFILE"):     profile,
	}
	if len(inst.Domains) > 0 {
		env[envName(inst.Product, "DOMAIN")] = inst.Domains[0]
	}

	// Every declared parameter, as <PRODUCT>_PARAM_<NAME>, defaulted when
	// the operator has not set it.
	//
	// PARAM_ rather than a flat <PRODUCT>_<NAME>: the flat form lets a
	// parameter named `data_dir` shadow <PRODUCT>_DATA_DIR and take the
	// deployment's storage with it.
	params, err := d.parameters(rel, inst)
	if err != nil {
		return ports.RuntimeConfig{}, err
	}
	for name, value := range params {
		env[envName(inst.Product, "PARAM_"+parameterVarName(name))] = value
	}

	// Every image the manifest declares, as <PRODUCT>_IMAGE_<NAME>.
	//
	// This is what connects the two halves of a release: the manifest says
	// which images, pinned by digest, and the Compose file says which
	// services. Without it a Compose file's `${DEMO_IMAGE_APP:-...}` falls
	// back to whatever default it carries, and the manifest's pinning -- the
	// rule that makes a release immutable and rollback meaningful -- decides
	// nothing at all.
	for name, ref := range rel.Manifest.Images {
		env[envName(inst.Product, "IMAGE_"+imageVarName(name))] = ref
	}

	return ports.RuntimeConfig{
		Project:    rel.Manifest.Runtime.Project,
		Files:      files,
		WorkingDir: rel.Root,
		Env:        env,
	}, nil
}

// envName builds a product-namespaced variable name, matching the hook ABI.
func envName(product, key string) string {
	return ports.HookEnv{Product: product}.Var(key)
}

// parameterVarName turns a parameter name into its variable suffix. Names are
// already constrained to lowercase, digits and underscores, so this is only an
// upcase -- but going through a named function keeps the mapping in one place
// alongside imageVarName.
func parameterVarName(name string) string { return strings.ToUpper(name) }

// imageVarName turns a manifest image key into the variable suffix a Compose
// file interpolates: `app` becomes APP, `web-ui` becomes WEB_UI.
func imageVarName(name string) string {
	upper := strings.ToUpper(name)
	upper = strings.ReplaceAll(upper, "-", "_")
	return strings.ReplaceAll(upper, ".", "_")
}

// templateData assembles the documented render context.
func (d *Deps) templateData(
	inst domain.Installation,
	rel domain.Release,
	profile string,
	schema domain.SecretSchema,
) (ports.TemplateData, error) {
	// Secret *references*: a name to the path of its rendered file. The
	// values never enter the render context, so a configuration file
	// cannot accidentally embed one.
	secretPaths := make(map[string]string, len(schema.Secrets))
	for _, decl := range schema.Secrets {
		secretPaths[decl.Name] = d.Paths.SecretsRenderDir() + "/" + decl.FileName()
	}

	if profile == "" {
		profile = inst.Profile
	}

	params, err := d.parameters(rel, inst)
	if err != nil {
		return ports.TemplateData{}, err
	}

	return ports.TemplateData{
		Installation: inst,
		Release: ports.ReleaseInfo{
			Name:    rel.Name(),
			Version: rel.Version(),
			Digest:  rel.Digest,
			Root:    rel.Root,
			Vendor:  rel.Manifest.Metadata.Vendor,
		},
		Profile: profile,
		Paths: ports.PathInfo{
			Etc:       d.Paths.EtcDir,
			Var:       d.Paths.VarDir,
			Run:       d.Paths.RunDir,
			Opt:       d.Paths.OptDir,
			Data:      d.Paths.DataDir(),
			Backups:   d.Paths.BackupsDir(),
			Secrets:   d.Paths.SecretsRenderDir(),
			Generated: d.Paths.GeneratedDir(),
		},
		Secrets:    secretPaths,
		Domains:    inst.Domains,
		Parameters: params,
		Env:        productEnvOverrides(inst.Product),
	}, nil
}

// productEnvOverrides collects <PRODUCT>_* variables from the process
// environment, which sit between installation.yaml and command-line flags in
// the precedence order.
func productEnvOverrides(product string) map[string]string {
	prefix := ports.HookEnv{Product: product}.Prefix() + "_"
	out := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := cut(kv, '='); ok && len(k) > len(prefix) && k[:len(prefix)] == prefix {
			out[k[len(prefix):]] = v
		}
	}
	return out
}

func cut(s string, sep byte) (before, after string, found bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

// RuntimeConfigFor exposes runtime configuration assembly to the CLI layer,
// which needs it for the few commands that touch the runtime directly --
// restarting the services that depend on a rotated secret, above all.
func (d *Deps) RuntimeConfigFor(rel domain.Release, inst domain.Installation) (ports.RuntimeConfig, error) {
	return d.runtimeConfig(rel, inst, "")
}

// ResolveBackup exposes backup selection to the CLI layer.
func (d *Deps) ResolveBackup(ctx context.Context, id string) (ports.BackupRef, error) {
	return d.resolveBackupRef(ctx, id)
}

// Result is what an operation returns to the CLI layer.
type Result struct {
	Record domain.OperationRecord `json:"operation"`

	// Summary is a one-line human outcome.
	Summary string `json:"summary,omitempty"`

	// Data is operation-specific payload for --json.
	Data any `json:"data,omitempty"`
}

// describe builds the operation title shown in the live view.
func describe(opType domain.OperationType, from, to domain.Version) string {
	switch {
	case !from.IsZero() && !to.IsZero() && !from.Equal(to):
		return fmt.Sprintf("%s %s → %s", opType, from, to)
	case !to.IsZero():
		return fmt.Sprintf("%s %s", opType, to)
	default:
		return string(opType)
	}
}

// OperationFilterAll returns a filter matching every journal record. It exists
// so callers outside this package can read the journal without importing
// ports for one zero value.
func OperationFilterAll() ports.Filter { return ports.Filter{} }

// installedVersions lists the releases present in the release store, newest
// first.
//
// A directory whose manifest will not load is skipped rather than reported: the
// store accumulates half-fetched directories, and a stray one must not make
// `--to` unusable.
func (d *Deps) installedVersions() []domain.Version {
	entries, err := os.ReadDir(d.Paths.ReleasesDir())
	if err != nil {
		return nil
	}

	var out []domain.Version
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rel, err := release.Load(filepath.Join(d.Paths.ReleasesDir(), e.Name()))
		if err != nil {
			continue
		}
		out = append(out, rel.Version())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GreaterThan(out[j]) })
	return out
}

// resolveInstalled loads a release from the store by version, naming what is
// available when it is not there.
func (d *Deps) resolveInstalled(version string) (domain.Release, error) {
	parsed, err := domain.ParseVersion(version)
	if err != nil {
		return domain.Release{}, err
	}

	rel, loadErr := release.Load(d.Paths.ReleaseDir(parsed.String()))
	if loadErr == nil {
		return rel, nil
	}

	available := d.installedVersions()
	labels := make([]string, 0, len(available))
	for _, v := range available {
		labels = append(labels, v.String())
	}
	hint := "run `morzer release list` to see what is installed"
	if len(labels) > 0 {
		hint = "installed releases: " + strings.Join(labels, ", ")
	}

	return domain.Release{}, domain.ValidationError(domain.ErrReleaseNotFound,
		"release %s is not in the release store", parsed).WithHint("%s", hint)
}
