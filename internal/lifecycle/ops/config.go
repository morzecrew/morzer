package ops

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/engine"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

// ConfigEntry is one parameter as an operator sees it.
type ConfigEntry struct {
	Name        string               `json:"name"`
	Type        domain.ParameterType `json:"type"`
	Value       string               `json:"value"`
	Default     string               `json:"default,omitempty"`
	Description string               `json:"description,omitempty"`

	// Source is where Value came from: "installation" when the operator
	// set it, "release" when it is the declared default.
	Source string `json:"source"`

	Values   []string `json:"values,omitempty"`
	Services []string `json:"services,omitempty"`
}

// ConfigReport is the whole parameter surface of an installation.
type ConfigReport struct {
	Product    string        `json:"product"`
	Release    string        `json:"release"`
	Parameters []ConfigEntry `json:"parameters"`

	// Stale are recorded values the current release no longer declares.
	//
	// Reported rather than refused: dropping a parameter is the vendor's
	// decision, and blocking every command over one helps nobody. They are
	// what `config unset` exists to clear.
	Stale []string `json:"stale,omitempty"`
}

// ConfigList reads the effective parameters. Read-only: no lock, no journal.
func ConfigList(ctx context.Context, d *Deps) (ConfigReport, error) {
	inst, rel, err := d.currentInstallationAndRelease(ctx)
	if err != nil {
		return ConfigReport{}, err
	}

	out := ConfigReport{Product: inst.Product, Release: rel.Version().String()}

	for _, name := range sortedNames(rel.Manifest.Parameters) {
		spec := rel.Manifest.Parameters[name]

		value, source := spec.Default, "release"
		if set, ok := inst.Parameters[name]; ok {
			// Parsed rather than echoed, so `list` shows what the
			// deployment will actually receive.
			parsed, err := spec.Parse(set)
			if err != nil {
				return ConfigReport{}, domain.ValidationError(err, "parameter %q", name)
			}
			value, source = parsed, "installation"
		}

		out.Parameters = append(out.Parameters, ConfigEntry{
			Name: name, Type: spec.Type, Value: value, Default: spec.Default,
			Description: spec.Description, Source: source,
			Values: spec.Values, Services: spec.Services,
		})
	}

	for name := range inst.Parameters {
		if _, declared := rel.Manifest.Parameters[name]; !declared {
			out.Stale = append(out.Stale, name)
		}
	}
	sort.Strings(out.Stale)

	return out, nil
}

// ConfigGet reads one parameter.
func ConfigGet(ctx context.Context, d *Deps, name string) (ConfigEntry, error) {
	report, err := ConfigList(ctx, d)
	if err != nil {
		return ConfigEntry{}, err
	}
	for _, entry := range report.Parameters {
		if entry.Name == name {
			return entry, nil
		}
	}
	return ConfigEntry{}, undeclared(name, report)
}

// ConfigSetOptions is a change to the recorded parameters.
type ConfigSetOptions struct {
	Options

	// Set are the values to record, as given by the operator.
	Set map[string]string

	// Unset are the names to return to the release's default.
	Unset []string
}

// ConfigSet records new parameter values and makes the deployment match them.
//
// An engine operation rather than a file edit: it takes the deployment lock,
// plans under --dry-run, journals before and after each step, and unwinds what
// it changed if a step fails. A parameter change alters what is running, so it
// gets the same treatment as anything else that does.
func ConfigSet(ctx context.Context, d *Deps, opts ConfigSetOptions) (Result, error) {
	if len(opts.Set) == 0 && len(opts.Unset) == 0 {
		return Result{}, domain.Usage("nothing to change").
			WithHint("pass name=value to set one, or `config unset <name>` to clear one")
	}

	inst, rel, err := d.currentInstallationAndRelease(ctx)
	if err != nil {
		return Result{}, err
	}

	next, changed, err := mergeParameters(inst, rel, opts)
	if err != nil {
		return Result{}, err
	}
	if len(changed) == 0 {
		return Result{Summary: "no change: every value already matches"}, nil
	}

	// Asked once, before anything changes, because it decides both which
	// steps run and what the summary may claim. Asking again afterwards
	// would report on a deployment the operation had already altered.
	running := d.projectRunning(ctx, inst, rel, opts.Options)

	opID := d.newOpID()
	op := engine.Operation{
		ID:          opID,
		Type:        domain.OpTypeConfig,
		Description: "set " + strings.Join(changed, ", "),
		Steps:       configSteps(d, inst, rel, next, changed, running),
	}

	var result engine.Result
	runErr := d.withLock(ctx, opID, domain.OpTypeConfig, opts.Options, func(ctx context.Context) error {
		var err error
		result, err = d.Engine.Run(ctx, op, d.engineOptions(opts.Options, inst.ID, nil))
		return err
	})

	out := Result{
		Record: result.Record,
		Summary: configSummary(changed, affectedServices(rel, changed),
			opts.DryRun, running),
		Data: next,
	}
	d.notifyFinished(ctx, opID, domain.OpTypeConfig, result.Record, runErr)
	if runErr != nil {
		return out, runErr
	}

	return out, nil
}

// mergeParameters applies the requested changes and validates the outcome.
//
// Validated as a whole rather than per value: the recorded set is what every
// later operation resolves, so a change must leave it entirely valid, not
// merely add a valid entry to an invalid set.
func mergeParameters(
	inst domain.Installation, rel domain.Release, opts ConfigSetOptions,
) (map[string]string, []string, error) {
	next := make(map[string]string, len(inst.Parameters)+len(opts.Set))
	for name, value := range inst.Parameters {
		next[name] = value
	}

	var changed []string

	for _, name := range opts.Unset {
		if _, recorded := next[name]; !recorded {
			// Not an error: unsetting something already at its
			// default is what the operator asked for, and it holds.
			continue
		}
		delete(next, name)
		changed = append(changed, name)
	}

	for name, raw := range opts.Set {
		spec, declared := rel.Manifest.Parameters[name]
		if !declared {
			return nil, nil, undeclaredIn(name, rel)
		}
		value, err := spec.Parse(raw)
		if err != nil {
			return nil, nil, domain.ValidationError(err, "parameter %q", name).
				WithHint("%s", domain.DescribeParameter(name, spec))
		}
		if current, ok := next[name]; ok && current == value {
			continue
		}
		next[name] = value
		changed = append(changed, name)
	}

	// The whole recorded set must resolve, so a stale value left by an
	// earlier release surfaces here rather than at the next apply.
	if _, err := domain.ResolveParameters(rel.Manifest.Parameters, next); err != nil {
		return nil, nil, err
	}

	// A declaration with no default is the manifest saying "you must choose
	// this", and a `config unset` of one would leave the deployment with no
	// value for it -- resolvable, because an absent value resolves as empty,
	// and wrong. This is the other command that can be told a value, so it
	// is the other command that refuses.
	if missing := domain.MissingValues(rel.Manifest.Parameters, next); len(missing) > 0 {
		return nil, nil, domain.Usage(
			"release %s declares no default for %s, so it cannot be left unset",
			rel.Version(), strings.Join(missing, ", ")).
			WithHint("give it a value with `morzer config set %s=<value>`", missing[0])
	}

	sort.Strings(changed)
	return next, changed, nil
}

// configSteps is the change, ordered so a failure leaves the deployment
// running what it was running.
func configSteps(
	d *Deps, inst domain.Installation, rel domain.Release,
	next map[string]string, changed []string, running bool,
) []engine.Step {
	updated := inst
	updated.Parameters = next

	steps := []engine.Step{
		stepRecordParameters(d, inst, updated),
		stepRenderConfiguration(d, updated, rel, Options{}),
	}

	// Only when the release says which services depend on the value, and
	// only when something is running to re-create. A parameter with no
	// declared services needs a full `apply`, which the summary says out
	// loud rather than guessing at.
	if services := affectedServices(rel, changed); len(services) > 0 && running {
		steps = append(steps, stepRecreateServices(d, updated, rel, services))
	}
	return steps
}

// stepRecordParameters writes the new set, and puts the old one back if a
// later step fails.
func stepRecordParameters(d *Deps, previous, updated domain.Installation) engine.Step {
	return engine.Step{
		ID:          "record-parameters",
		Description: "record parameters",
		Idempotent:  true,
		OnFailure:   engine.Compensate,
		Timeout:     time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			return d.saveInstallation(ctx, updated)
		},
		Compensate: func(ctx context.Context, st *engine.State) error {
			return d.saveInstallation(ctx, previous)
		},
	}
}

// stepRecreateServices brings the affected services up with the new values.
//
// Up, not Restart -- and this is the whole reason the step exists. `docker
// compose restart` restarts the *existing* containers, and a published port is
// baked into a container at creation: restarting after changing a port leaves
// the old mapping in place and reports success. Only `up` re-creates.
//
// Secret rotation legitimately uses Restart, because a secret reaches a
// container as a mounted file and restarting re-reads it. A parameter reaches
// it as part of the container's definition, so the container has to be
// replaced.
func stepRecreateServices(
	d *Deps, inst domain.Installation, rel domain.Release, services []string,
) engine.Step {
	return engine.Step{
		ID:          "recreate-services",
		Description: "re-create " + strings.Join(services, ", "),
		Idempotent:  true,
		OnFailure:   engine.Compensate,
		Timeout:     10 * time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			return d.recreate(ctx, inst, rel, services)
		},
		Compensate: func(ctx context.Context, st *engine.State) error {
			// The previous step has already restored the old
			// values, so re-creating again returns the containers
			// to what they were.
			prior, err := d.State.LoadInstallation(ctx)
			if err != nil {
				return err
			}
			return d.recreate(ctx, prior, rel, services)
		},
	}
}

func (d *Deps) recreate(
	ctx context.Context, inst domain.Installation, rel domain.Release, services []string,
) error {
	cfg, err := d.runtimeConfig(rel, inst, "")
	if err != nil {
		return err
	}
	return d.Runtime.Up(ctx, cfg, ports.UpOptions{
		Services: services, Wait: true, WaitTimeout: 5 * time.Minute,
	})
}

// affectedServices is every service the changed parameters declare.
func affectedServices(rel domain.Release, changed []string) []string {
	seen := map[string]bool{}
	var out []string

	for _, name := range changed {
		for _, svc := range rel.Manifest.Parameters[name].Services {
			if !seen[svc] {
				seen[svc] = true
				out = append(out, svc)
			}
		}
	}
	sort.Strings(out)
	return out
}

// configSummary says what happened, in the tense it happened in.
//
// A plan that reports "re-created app" has told the operator something untrue
// about their deployment, and a change to a stopped deployment that reports the
// same has told them the containers are already running the new value.
func configSummary(changed, services []string, dryRun, running bool) string {
	names := strings.Join(changed, ", ")

	switch {
	case dryRun && len(services) > 0 && running:
		return "would set " + names + " and re-create " + strings.Join(services, ", ")
	case dryRun:
		return "would set " + names + "; it would take effect on the next `morzer apply`"
	case len(services) > 0 && running:
		return "set " + names + "; re-created " + strings.Join(services, ", ")
	case len(services) > 0:
		return "set " + names + "; nothing is running, so it takes effect on the next `morzer apply`"
	default:
		return "set " + names + "; the release declares no dependent services, " +
			"so it takes effect on the next `morzer apply`"
	}
}

// currentInstallationAndRelease loads what every config command needs.
func (d *Deps) currentInstallationAndRelease(ctx context.Context) (domain.Installation, domain.Release, error) {
	inst, err := d.State.LoadInstallation(ctx)
	if err != nil {
		return domain.Installation{}, domain.Release{}, err
	}

	current, err := d.State.CurrentRelease(ctx)
	if err != nil {
		return domain.Installation{}, domain.Release{}, err
	}
	if current.IsZero() {
		return domain.Installation{}, domain.Release{}, domain.InstallationError(nil,
			"no release is installed, so nothing declares any parameters").
			WithHint("install one with `morzer update <bundle>`")
	}

	rel, err := release.Load(current.Root)
	if err != nil {
		return domain.Installation{}, domain.Release{}, err
	}
	return inst, rel, nil
}

func undeclared(name string, report ConfigReport) error {
	declared := make([]string, len(report.Parameters))
	for i, entry := range report.Parameters {
		declared[i] = entry.Name
	}
	return notDeclared(name, declared)
}

func undeclaredIn(name string, rel domain.Release) error {
	return notDeclared(name, sortedNames(rel.Manifest.Parameters))
}

func notDeclared(name string, declared []string) error {
	err := domain.ValidationError(nil, "the release declares no parameter %q", name)
	if len(declared) == 0 {
		return err.WithHint("this release declares no parameters at all")
	}
	return err.WithHint("it declares: %s", strings.Join(declared, ", "))
}

func sortedNames(m map[string]domain.ParameterSpec) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
