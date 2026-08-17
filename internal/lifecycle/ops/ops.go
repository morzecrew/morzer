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
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/oklog/ulid/v2"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
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

	// StateFor opens the state of *another* installation on this machine.
	//
	// A function rather than a second store, because the set is not known
	// until the machine is read: `ls` derives one layout per product it
	// finds. It stays a seam rather than a direct constructor call so the
	// adapter is still named in exactly one place -- and so a test can
	// answer for an installation that has no files at all.
	//
	// Nil in a build that never lists: every use checks, and the one
	// operation that needs it refuses by name rather than reporting an
	// unreadable machine.
	StateFor func(domain.Paths) ports.StateStore

	Runtime ports.Runtime
	Secrets ports.SecretStore
	Backup  ports.BackupEngine

	// Signer is this machine's own signing identity.
	//
	// Nil in a build or a test that signs nothing, and every use checks --
	// the same discipline as Targets and Supervisor. An installation that
	// has never signed must keep working exactly as it did before signing
	// existed, which is every installation that reached schema 6 by
	// migration (RFC 0028 decision 9).
	Signer ports.Signer

	// Checker verifies a detached signature against a public key. Separate
	// from Signer because verification needs no private key: `attest
	// verify` must work on a machine that cannot produce new statements.
	Checker ports.SignatureChecker

	// Targets is the registry of places a backup can be kept that are not
	// this machine. Nil in a build or a test that configures none, which is
	// why every use checks: an installation with no targets must keep
	// working exactly as it did before they existed.
	Targets ports.BackupTarget

	// Objects is the same registry seen through the half of it that holds
	// things which are not backups -- attestations, and fleet rows.
	//
	// A second field rather than a type assertion on Targets, so that a
	// build which wires one and not the other is a wiring decision somebody
	// made rather than an interface check failing at three in the morning.
	// Nil in a build that pushes nothing, like every field around it.
	Objects ports.ObjectStore

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

	// FreeSpace reports the bytes available on the filesystem holding a
	// path. Injectable for the same reason as the clock: a diagnostic whose
	// verdict depends on the host's real disk is a diagnostic whose test
	// passes or fails on which machine ran it.
	FreeSpace func(string) (int64, error)

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

	// MachineProducts is every product with installation state on this
	// machine, as the caller found it, sorted. Empty means either a bare
	// machine or a caller that did not look -- an embedder, a test -- and
	// both take the same branch below, because "run init" is the right
	// answer to the first and a harmless one to the second.
	//
	// ProductNamed says whether the installation this Deps points at was
	// chosen by the operator (`--product`, `--config`) or inferred.
	//
	// The pair exists for one refusal. A lookup that finds no installation
	// *here* means several different things, and neither layer can tell them
	// apart alone: the caller knows what the machine holds and what the
	// operator asked for, this layer knows the lookup failed. Without
	// ProductNamed the refusal told an operator who had just typed
	// `--product` that `--product` was required.
	MachineProducts []string
	ProductNamed    bool
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

	// SourceRef is where the release being installed came from. Set by
	// `update`, which is the only operation that introduces one: `apply`
	// re-converges what is already installed and `rollback` returns to a
	// release that arrived earlier, so neither changes the answer.
	SourceRef string
}

// NewOperationID returns a ULID: lexicographically sortable and
// timestamp-prefixed, which is what makes the append-only journal readable in
// file order.
func NewOperationID(t time.Time) string {
	return "op_" + ulid.MustNew(ulid.Timestamp(t), rand.Reader).String()
}

// freeSpace reports the bytes available where backups are written.
func (d *Deps) freeSpace(path string) (int64, error) {
	if d.FreeSpace != nil {
		return d.FreeSpace(path)
	}
	return atomicfs.FreeSpace(path)
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

// warnDeprecations announces what the bundle being installed is written with
// that this manager will stop reading.
//
// Called from `init` and `update` and from nowhere else, which is the whole of
// the decision. A deprecation warning is only useful at the moment somebody is
// choosing this bundle -- they can decline it, or ask the vendor for one
// written the new way. Every other operator command meets the same manifest
// again and again with no such choice available, and `release.Load` says why it
// refuses to be the place this happens: a warning about a file the operator did
// not write and cannot change is how a project teaches people to ignore its
// warnings.
//
// Both kinds are published here. The api_version warning used to be on the
// update path alone, so a *first install* of a deprecated bundle said nothing
// at all -- found while adding the second kind, and fixed rather than
// reproduced.
func (d *Deps) warnDeprecations(m domain.Manifest) {
	if d.Bus == nil {
		return
	}
	if warning, deprecated := m.DeprecationWarning(); deprecated {
		d.Bus.Publish(events.Message(events.LevelWarn,
			"this bundle's api_version %s is deprecated: %s", m.APIVersion, warning))
	}
	for _, f := range m.DeprecatedFields() {
		d.Bus.Publish(events.Message(events.LevelWarn,
			"this bundle uses %s", f.Message()))
	}
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

// NotifyDeadline bounds the whole fan-out, not one request.
//
// Notifiers delivers sequentially, so a per-target timeout alone lets N
// unreachable targets add N × that timeout to every operation while each
// individual request looks well behaved. What an operator experiences is the
// sum, so the sum is what is bounded.
const NotifyDeadline = 15 * time.Second

// forwardedKinds is the allowlist of events that may leave the machine.
//
// An allowlist rather than a denylist, so a Kind added later is not forwarded
// until somebody decides it should be. The two here are the outcome an operator
// wants and the diagnostics nobody has looked at.
//
// KindStepOutput is the reason this is a list at all. It carries raw
// subprocess output -- hook stdout, compose stderr, whatever a vendor's
// migration script prints -- and the engine's claim that events carry no
// secrets rests on a redaction handler that has been wrong once already. The
// terminal and the JSONL log get everything; the network gets two kinds.
// Every kind is classified explicitly, including the ones that stay. A map
// holding only the true entries cannot tell "decided against" from "never
// considered", so a Kind added later would default to not-forwarded and look
// deliberate. TestEveryEventKindIsClassified fails until someone chooses.
var forwardedKinds = map[events.Kind]bool{
	events.KindOperationFinished: true,
	events.KindCheck:             true,

	// A staged release is the one event here that is not an outcome, and it
	// is forwarded for that reason: it is a decision waiting for a person,
	// and the person is not at the terminal where the timer ran.
	events.KindUpdateStaged: true,

	events.KindOperationStarted: false, // the outcome is the news, not the start
	events.KindStepStarted:      false, // narration
	events.KindStepProgress:     false, // narration
	events.KindStepFinished:     false, // narration
	events.KindPlan:             false, // a dry run is not an event to wake up for
	events.KindMessage:          false, // engine narration
	events.KindStepOutput:       false, // see above: raw vendor-controlled output
}

// notifyFinished reports an operation's outcome, whatever the outcome was.
//
// Every call site used to sit *after* the `if runErr != nil { return }` guard,
// so the only operations ever reported were the ones that succeeded — which is
// the half nobody needs to be told about. A channel that goes quiet exactly
// when something breaks is worse than no channel, because silence reads as
// "nothing happened".
//
// The status comes from the record where the engine set one: a compensated
// update and an interrupted one are different things to wake up for, and
// flattening both to "failed" would throw that away.
func (d *Deps) notifyFinished(
	ctx context.Context,
	opID string,
	opType domain.OperationType,
	rec domain.OperationRecord,
	runErr error,
) {
	// A plan is not an outcome. Every operation reaches here, including
	// `--dry-run`, which mutates nothing and whose "finished" would be a
	// webhook announcing that somebody looked. Checked on the record rather
	// than on Options because this is the value the engine actually wrote.
	if rec.DryRun {
		return
	}

	status := rec.Status
	var failure *domain.Error
	if runErr != nil {
		failure = domain.AsError(runErr)
		if status == "" || status == domain.StatusRunning {
			status = domain.StatusFailed
		}
	}
	d.notify(ctx, events.OperationFinished(opID, opType, status, rec.Duration(), failure))
}

// notify sends an event to the configured notifier, if the allowlist admits it.
//
// Failures are logged and dropped. A webhook being down must never change the
// outcome of a deployment, and an operator must not learn about a Slack outage
// by way of a rolled-back update.
func (d *Deps) notify(ctx context.Context, ev events.Event) {
	if d.Notifier == nil || !forwardedKinds[ev.Kind] {
		return
	}

	// Bounded, and *not* detached from the operation's context.
	//
	// Detaching was the first instinct -- a failed operation is exactly when
	// the notification matters, and a cancelled parent drops it. But the
	// contexts that are cancelled here are the ones an operator just
	// cancelled: Ctrl-C would then leave the CLI apparently wedged for the
	// whole deadline while it posted to an endpoint nobody is waiting for.
	// Fifteen seconds of unresponsiveness after Ctrl-C is a worse failure
	// than a missing message about a run the operator watched themselves
	// interrupt.
	//
	// The case that matters is unaffected: an operation that *fails* has a
	// live context, so its notification goes out. What is lost is the
	// cancelled and timed-out runs, and the journal is the record for those
	// -- which is what at-most-once delivery already meant.
	ctx, cancel := context.WithTimeout(ctx, NotifyDeadline)
	defer cancel()

	if err := d.Notifier.Notify(ctx, ev); err != nil {
		logging.FromContext(ctx).Warn("notifier failed",
			"notifier", d.Notifier.Name(), "error", err)
	}
}

// loadInstallation reads the installation, producing an actionable error when
// there is none.
func (d *Deps) loadInstallation(ctx context.Context) (domain.Installation, error) {
	if err := d.noSelection(); err != nil {
		return domain.Installation{}, err
	}
	exists, err := d.State.InstallationExists(ctx)
	if err != nil {
		return domain.Installation{}, err
	}
	if !exists {
		return domain.Installation{}, d.noInstallation()
	}
	return d.State.LoadInstallation(ctx)
}

// noSelection refuses to act on a machine whose installations nobody chose
// between.
//
// Asked before the state is read rather than after the read fails, because the
// product path resolution falls back to when nothing selected one is `morzer` --
// the manager's own name, and so the likeliest name for a real installation. Two
// installations where one is called `morzer` made the placeholder a silent
// choice: `status` loaded it and reported on it, having asked nobody. Refusing
// only when the read *fails* cannot catch that, because the read succeeds.
//
// It stays inside the lookup rather than moving to path resolution: `version`,
// `release verify` and `init` never reach here, and they are the commands an
// operator needs on exactly this machine.
func (d *Deps) noSelection() error {
	if len(d.MachineProducts) < 2 || d.ProductNamed {
		return nil
	}
	// The config path is derived from the layout this command actually
	// resolved, not from the production one: under a relocated root the
	// hint named a file that is not there, which is advice an operator
	// cannot follow.
	first := domain.PathsUnder(d.Paths.Root(), d.MachineProducts[0])
	return domain.Usage("this machine has %d installations, so --product is required",
		len(d.MachineProducts)).
		WithHint("%s — pass `--product %s`, or `--config %s`; `morzer ls` lists them",
			strings.Join(d.MachineProducts, ", "),
			d.MachineProducts[0],
			first.InstallationFile())
}

// RequireInstallationChosen exposes the ambiguity refusal to the CLI layer,
// which asks it once per command rather than once per lookup.
//
// The lookup keeps asking too. This is the same question at two altitudes: the
// command layer knows which commands are about one installation, and the lookup
// knows when one is about to be read -- and an embedder that assembles Deps
// itself only passes the second.
func (d *Deps) RequireInstallationChosen() error { return d.noSelection() }

// noInstallation explains a lookup that found nothing here.
//
// Two answers, because there are two situations and they need opposite advice.
// A machine with no installation is a machine to run `init` on. A machine that
// has installations, none of them the one that was named, is a machine where
// `init` would create one more -- and the operator is not missing an
// installation, they are missing the right name. Reporting the second as the
// first is advice that makes the problem worse, which is what this manager did
// until now.
//
// Nothing here answers the unnamed ambiguous machine: noSelection has already
// refused it, before the read that got us here.
//
// The refusal is a usage error rather than an installation one: nothing about
// the machine is wrong, and the fix is on the command line.
func (d *Deps) noInstallation() error {
	missing := domain.InstallationError(domain.ErrInstallation,
		"no installation found at %s", d.Paths.EtcDir).
		WithHint("run `morzer init` to create one")

	switch {
	case len(d.MachineProducts) == 0:
		// Nothing on the machine, or nobody looked. `init` either way.
		return missing

	case slices.Contains(d.MachineProducts, d.Paths.Product):
		// This installation is the one being asked for and discovery
		// saw its configuration, so what is missing is its recorded
		// state -- a half-created or half-removed installation rather
		// than an absent one. The message is unchanged from before this
		// pair existed; diagnosing the difference is a separate job.
		return missing

	default:
		// Named, and nobody has it. Only a name reaches here: an
		// unnamed product is either the machine's single installation,
		// which the case above matched, or an ambiguity noSelection
		// refused before the state was read.
		return domain.Usage("this machine has no installation named %q", d.Paths.Product).
			WithHint("it has %s", strings.Join(d.MachineProducts, ", "))
	}
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
		DryRun:          dryRun,
		LogLevel:        "info",
		// Best effort: a hook environment is built on paths that do
		// not fail, and a parameter that cannot resolve is already
		// refused by the steps that matter -- render, apply, health.
		// Failing here would turn a bad parameter into a crash in the
		// middle of building a log line.
		Parameters: d.parametersOrEmpty(rel, inst),
		Extra:      d.runtimeHookVars(rel, inst),
	}
}

// runtimeHookVars asks the runtime what it adds to the hook ABI.
//
// `<PRODUCT>_COMPOSE_PROJECT` comes from here now rather than from a field on
// HookEnv. The variable is unchanged for a Compose installation; what changed
// is that a runtime with no project supplies nothing instead of the core ABI
// promising a value it cannot mean (RFC 0023 §2.2).
//
// Best effort, like the parameters beside it: a hook environment is built for a
// log line as often as for an execution, and a release whose files cannot be
// resolved is already refused by the steps that matter. Returning nothing here
// costs a variable; failing would cost the diagnostic that says why.
func (d *Deps) runtimeHookVars(rel domain.Release, inst domain.Installation) map[string]string {
	supplier, ok := d.Runtime.(ports.HookVarSupplier)
	if !ok {
		return nil
	}
	cfg, err := d.runtimeConfig(rel, inst, "")
	if err != nil {
		return nil
	}
	supplied := supplier.HookVars(cfg)
	if len(supplied) == 0 {
		return nil
	}
	// Namespaced here, because the namespace is the manager's. An adapter
	// that built `DEMO_COMPOSE_PROJECT` itself would be a second
	// implementation of HookEnv.Var, and the two would drift the first time
	// a product name needed a character escaped.
	named := make(map[string]string, len(supplied))
	for suffix, value := range supplied {
		named[ports.HookEnv{Product: inst.Product}.Var(suffix)] = value
	}
	return named
}

// parametersOrEmpty resolves parameters, returning nothing on failure.
func (d *Deps) parametersOrEmpty(rel domain.Release, inst domain.Installation) map[string]string {
	params, err := d.parameters(rel, inst)
	if err != nil {
		return nil
	}
	return params
}

// saveInstallation records operator intent in both places it lives.
//
// installation.yaml is the operator-facing report and the JSON state file is
// what the manager reads. One writer, because two of them is how the file an
// operator looks at stops matching the deployment they are looking at.
func (d *Deps) saveInstallation(ctx context.Context, inst domain.Installation) error {
	data, err := yaml.Marshal(inst)
	if err != nil {
		return domain.Internal(err, "cannot serialise the installation")
	}

	// The header says what the file is. It used to claim edits took
	// effect, which was never true: nothing reads it back. `config` is the
	// editor, and `doctor` reports when the two disagree.
	const header = "# Managed by morzer. This file is a report, not a control:\n" +
		"# the manager reads its own state, so editing this changes nothing.\n" +
		"# Change parameters with `morzer config set name=value`.\n"

	if err := atomicfs.WriteFile(d.Paths.InstallationFile(),
		append([]byte(header), data...), 0o640); err != nil {
		return err
	}
	return d.State.SaveInstallation(ctx, inst)
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

// drivesRuntime reports whether the wired adapter is the runtime an
// installation is fixed to, and which one it is when it is not.
//
// Two values compared, neither of which this layer has to recognise: one comes
// out of installation state, the other out of the adapter (decision 7). One
// implementation, because the comparison is now asked in three places -- every
// operation, `doctor`, and `installation import` -- and three spellings of it
// is how one of them comes to disagree.
//
// No runtime wired is not a mismatch. A Deps without one cannot contradict
// anything, and the paths that need a runtime fail on their own terms; deciding
// here that they cannot proceed would make `installation import` refuse on a
// machine that has yet to be given an adapter.
func (d *Deps) drivesRuntime(inst domain.Installation) (drives string, ok bool) {
	if d.Runtime == nil {
		return "", true
	}
	have := d.Runtime.Name()
	return have, inst.RuntimeName() == have
}

// runtimeBaseline resolves what this installation is running under: the options
// it recorded, or -- for one created before schema 10, which recorded none --
// the ones the release it is currently running declares.
//
// Derived and persisted separately, and the split is the point. Three findings
// came out of doing both at once. A plan must not write, so a dry run that
// skipped the write also skipped the baseline and then accepted a target it
// would refuse for real. The write happened before the deployment lock, so it
// could clobber a concurrent `config set`. And a release that could not be
// resolved silently produced *no* baseline, which reads as "created before
// schema 10" and waves the change through -- on the update path, which is how a
// rename actually arrives.
//
// So this answers the question and nothing else. `running` is the release the
// deployment is on, never the one being introduced: adopting from a candidate
// would record the change as the baseline and refuse nothing.
func runtimeBaseline(inst domain.Installation, running domain.Release) map[string]string {
	if inst.RuntimeOptions != nil {
		return inst.RuntimeOptions
	}
	declared, _ := running.Manifest.DeclaredRuntimes()

	baseline := map[string]string{}
	for key, value := range declared[inst.RuntimeName()].Options {
		baseline[key] = value
	}
	return baseline
}

// persistRuntimeBaseline writes a derived baseline into installation state.
//
// Called under the deployment lock and nowhere else, because it is a
// read-modify-write of a record other operations also write: `config set` and
// `init --repair` change fields this one does not, and a save built on a copy
// read minutes earlier would put them back as they were.
//
// Re-reads under the lock and writes only when the field is still absent, so
// two operations racing to adopt the same installation agree rather than
// overwrite. Never called on a dry run: a plan that writes is not a plan.
func (d *Deps) persistRuntimeBaseline(ctx context.Context, baseline map[string]string) error {
	inst, err := d.State.LoadInstallation(ctx)
	if err != nil {
		return err
	}
	if inst.RuntimeOptions != nil {
		return nil
	}
	inst.RuntimeOptions = baseline
	return d.saveInstallation(ctx, inst)
}

// refuseRuntimeOptionChange is checkRuntimeOptions asked about a whole release,
// before an operation begins.
//
// The same comparison runs inside runtimeConfig, which is what makes it
// unbypassable -- every path that touches the runtime goes through it. This one
// exists because a precondition discovered *inside* a step is reported as a
// step failure: the engine compensates, the operation exits 11 ("back where it
// started"), and the exit code and remedy that say what is actually wrong are
// buried in a record. Asked here, it is a refusal with nothing started.
func (d *Deps) refuseRuntimeOptionChange(inst domain.Installation, rel domain.Release) error {
	declared, _ := rel.Manifest.DeclaredRuntimes()
	return d.checkRuntimeOptions(inst, declared[inst.RuntimeName()].Options)
}

// resolveRuntimeOptions returns options as the runtime will actually read them.
//
// The adapter's answer when it has one, and the map unchanged when it does not.
// Declining is a supported answer (ports.OptionResolver): a runtime with no
// defaults to fill in leaves the comparison exactly where it was before this
// existed, which over-refuses rather than under-refuses.
//
// Both sides of the comparison go through here, and that is the whole trick.
// The recorded baseline stays as the vendor declared it -- no schema change, no
// migration, and `installation describe` keeps publishing what was written --
// while the values actually compared are the ones the runtime will use.
func (d *Deps) resolveRuntimeOptions(inst domain.Installation, options map[string]string) map[string]string {
	resolver, ok := d.Runtime.(ports.OptionResolver)
	if !ok {
		return options
	}
	return resolver.ResolveOptions(ports.RuntimeConfig{
		Product: inst.Product,
		Options: options,
	})
}

// checkRuntimeOptions refuses a release that would change what the runtime was
// told when this installation was created.
//
// The options name durable things. Under Compose, `project` is the prefix on
// every volume, network and container — measured: `--project-name alpha`
// resolves a volume named `alpha_data`, and `beta` resolves `beta_data`. So a
// release that changes one does not reconfigure a deployment, it points it at
// storage nothing has ever written to, brings the product up empty, and leaves
// the real data on the disk with nothing referring to it. Nothing else in this
// manager would notice: the backup that runs afterwards captures the new empty
// volumes, and `doctor` reports them covered.
//
// Every option is treated as durable, because the manager cannot tell which are
// — only the adapter knows what any of them mean, and asking it "is this one
// dangerous" would be a second question with a second wrong answer available.
// Refusing a harmless change costs a message; permitting a harmful one costs
// the data.
//
// A nil record means the installation predates schema 10, and there is nothing
// to compare against: those adopt on the next operation that knows both halves
// rather than being refused for a baseline nobody wrote down.
//
// Both maps are resolved through the adapter before they meet, so what is
// compared is what the runtime will read rather than what the vendor typed. A
// release that writes out a value already in force -- `project: demo` on a
// deployment whose product is `demo` -- changes nothing and is applied; the
// same key with a different value is the rename this exists to stop.
func (d *Deps) checkRuntimeOptions(inst domain.Installation, declared map[string]string) error {
	if inst.RuntimeOptions == nil {
		return nil
	}

	was := d.resolveRuntimeOptions(inst, inst.RuntimeOptions)
	now := d.resolveRuntimeOptions(inst, declared)

	changed := make([]string, 0, len(now)+len(was))
	for key, before := range was {
		if after, ok := now[key]; !ok || after != before {
			changed = append(changed, key)
		}
	}
	for key := range now {
		if _, ok := was[key]; !ok {
			changed = append(changed, key)
		}
	}
	if len(changed) == 0 {
		return nil
	}
	sort.Strings(changed)

	return domain.IncompatibleError(nil,
		"this release changes runtime options this installation was created with: %s",
		strings.Join(changed, ", ")).
		WithHint("these name what the runtime creates -- under compose the project is the " +
			"prefix on every volume -- so applying this would deploy against storage " +
			"that does not exist yet and leave the current data unreferenced. Either " +
			"the release restores the values, or take a backup, `init` a fresh " +
			"installation and `restore` into it")
}

// runtimeConfig builds the runtime configuration for a release and profile.
func (d *Deps) runtimeConfig(rel domain.Release, inst domain.Installation, profile string) (ports.RuntimeConfig, error) {
	if profile == "" {
		profile = inst.Profile
	}

	// Decision 5: an installation whose runtime this manager cannot drive is
	// refused, never quietly driven with the adapter that happens to be
	// wired. Without this the recorded runtime is a label -- `init` would
	// store "quadlet", every later operation would hand `.container` files
	// to Compose, and the deployment would fail somewhere far away from the
	// sentence explaining why.
	if have, ok := d.drivesRuntime(inst); !ok {
		return ports.RuntimeConfig{}, domain.InstallationError(nil,
			"this installation runs on the %s runtime and this manager is configured for %s",
			inst.RuntimeName(), have).
			WithHint("the runtime is fixed when an installation is created and does not " +
				"transition; a manager built for a different runtime cannot operate it")
	}

	files, err := rel.RuntimeFilePaths(inst.RuntimeName(), profile)
	if err != nil {
		return ports.RuntimeConfig{}, err
	}

	// The variables Compose files may interpolate. The names are declared in
	// ports.ComposeVars, which is the published contract and what
	// `just docs-check` gates; a test asserts this builder produces exactly
	// that set.
	//
	// Secrets are absent by design: they reach containers as files under
	// /run, never as environment, so that `docker inspect` cannot print
	// them.
	values := map[string]string{
		"DATA_DIR":    d.Paths.DataDir(),
		"SECRETS_DIR": d.Paths.SecretsRenderDir(),
		"CONFIG_FILE": d.Paths.ApplicationFile(),
		"RELEASE_DIR": rel.Root,
		"VERSION":     rel.Version().String(),
		"PROFILE":     profile,
	}
	if len(inst.Domains) > 0 {
		values["DOMAIN"] = inst.Domains[0]
	}

	env := make(map[string]string, len(values))
	for suffix, value := range values {
		env[envName(inst.Product, suffix)] = value
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
	// The reference the *daemon* can resolve, which for an image travelling
	// in the bundle is not the one the manifest pins. Nothing can make a
	// local store answer to a digest reference for a repository it never
	// pulled from -- measured, and recorded as RFC 0011 decision 19 -- so a
	// bundled image is deployed under the alias ingest leaves behind.
	//
	// `ref` remains the identity: it is what the signature covers, what
	// `release show` reports, and what the alias is derived from. This is
	// the one place the two part company, because this is the only place
	// the value is handed to something that has to look it up.
	for name, spec := range rel.Manifest.Images {
		env[envName(inst.Product, "IMAGE_"+imageVarName(name))] = spec.RuntimeRef()
	}

	declared, _ := rel.Manifest.DeclaredRuntimes()

	if err := d.checkRuntimeOptions(inst, declared[inst.RuntimeName()].Options); err != nil {
		return ports.RuntimeConfig{}, err
	}

	return ports.RuntimeConfig{
		// The product this deployment is, and the settings the vendor
		// declared for this runtime -- carried, never read. What the
		// adapter names things is its own decision from these two.
		Product:    inst.Product,
		Options:    declared[inst.RuntimeName()].Options,
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
	}, nil
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

// recordedSourceRef returns the source ref already stored for a release root.
//
// Checks both pointers because the release being recorded may be either: a
// re-converge records the current one again, and a rollback records the
// previous one. Absent is not an error -- a release installed from a path, or
// before this was recorded, simply has none.
func (d *Deps) recordedSourceRef(ctx context.Context, root string) string {
	if rec, err := d.State.CurrentRelease(ctx); err == nil && rec.Root == root {
		return rec.SourceRef
	}
	if rec, err := d.State.PreviousRelease(ctx); err == nil && rec.Root == root {
		return rec.SourceRef
	}
	return ""
}
