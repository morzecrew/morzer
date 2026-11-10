package ops

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/engine"
	"github.com/morzecrew/morzer/internal/release"
)

// ConfigApplyOptions is one change document, applied as one operation.
type ConfigApplyOptions struct {
	Options

	Change ConfigChange
}

// ConfigApplied is what a change altered, by name only, for `--json`.
type ConfigApplied struct {
	Settings        []string `json:"settings,omitempty"`
	Parameters      []string `json:"parameters,omitempty"`
	Secrets         []string `json:"secrets,omitempty"`
	UnsetSettings   []string `json:"unset_settings,omitempty"`
	UnsetParameters []string `json:"unset_parameters,omitempty"`
	UnsetSecrets    []string `json:"unset_secrets,omitempty"`
	Recreated       []string `json:"recreated,omitempty"`
	Restarted       []string `json:"restarted,omitempty"`
}

// ConfigApply applies a change to settings, parameters and secrets as one
// `config` operation (RFC 0056): one record, one render, one converge and one
// health check, and on a failed step every value in it is put back.
func ConfigApply(ctx context.Context, d *Deps, opts ConfigApplyOptions) (Result, error) {
	run, err := d.runChange(ctx, opts.Options, opts.Change, changeExtras{})
	if err == nil && run.noChange {
		return Result{Summary: "no change: every value already matches"}, nil
	}
	out := Result{Record: run.record}
	if err != nil {
		return out, err
	}
	out.Summary = changeSummary(run, opts.DryRun)
	out.Data = run.applied()
	return out, nil
}

// changeNames is what a change alters, by section: only the names whose value
// it actually changes.
type changeNames struct {
	settings, parameters, secrets                []string
	unsetSettings, unsetParameters, unsetSecrets []string
}

func (n changeNames) empty() bool {
	return len(n.settings)+len(n.parameters)+len(n.secrets)+
		len(n.unsetSettings)+len(n.unsetParameters)+len(n.unsetSecrets) == 0
}

func (n changeNames) settingNames() []string   { return sortedUnion(n.settings, n.unsetSettings) }
func (n changeNames) parameterNames() []string { return sortedUnion(n.parameters, n.unsetParameters) }
func (n changeNames) secretNames() []string    { return sortedUnion(n.secrets, n.unsetSecrets) }

// list names each change once, as a summary and the journal's description
// read them.
func (n changeNames) list() []string {
	var out []string
	out = append(out, n.settings...)
	out = append(out, n.parameters...)
	for _, name := range n.secrets {
		out = append(out, "secret "+name)
	}
	for _, name := range append(slices.Clone(n.unsetSettings), n.unsetParameters...) {
		out = append(out, name+" (unset)")
	}
	for _, name := range n.unsetSecrets {
		out = append(out, "secret "+name+" (removed)")
	}
	return out
}

// flags is the journal's record of the change: names by section, never a
// value (RFC 0056 decision 11).
func (n changeNames) flags() map[string]string {
	flags := map[string]string{}
	for key, names := range map[string][]string{
		"settings": n.settings, "parameters": n.parameters, "secrets": n.secrets,
		"unset_settings": n.unsetSettings, "unset_parameters": n.unsetParameters, "unset_secrets": n.unsetSecrets,
	} {
		if len(names) > 0 {
			flags[key] = strings.Join(names, ",")
		}
	}
	return flags
}

// plannedChange is a change checked against an installation: the
// installation it leaves, and the names it changes.
type plannedChange struct {
	after  domain.Installation
	names  changeNames
	schema domain.SecretSchema
	hasRel bool
}

// changeRun is what one run of a change did, for its caller's summary.
type changeRun struct {
	before, after domain.Installation
	rel           domain.Release
	names         changeNames
	noChange      bool

	running, stopped bool
	// converged is a profile change applied under the new profiles.
	converged bool
	// declared is every service the change's names declare; services those
	// the change re-created or restarted, restarted which of the two.
	declared, services []string
	restarted          bool
	// rendered is whether the configuration was rendered, which is what a
	// `config` statement digests.
	rendered bool

	record domain.OperationRecord
}

func (r changeRun) applied() ConfigApplied {
	out := ConfigApplied{
		Settings: r.names.settings, Parameters: r.names.parameters, Secrets: r.names.secrets,
		UnsetSettings: r.names.unsetSettings, UnsetParameters: r.names.unsetParameters,
		UnsetSecrets: r.names.unsetSecrets,
	}
	if r.restarted {
		out.Restarted = r.services
	} else {
		out.Recreated = r.services
	}
	return out
}

// changeExtras is what a caller adds to a change's operation.
type changeExtras struct {
	// record writes into the installation the change leaves, in the record
	// step's one write, so the fields commit with the change and are undone
	// with it. A sync's serial goes here (RFC 0057 decision 25). Set, the
	// record step runs even for a change of secrets alone, and the fields
	// are written even when no value changes. Called under the lock, so an
	// error here, such as a serial another sync has since applied, stops
	// the change before anything runs.
	record func(*domain.Installation) error

	// check refuses the change against the release it would apply under,
	// before it is planned: a sync's managed names (RFC 0057 §5.6). Run
	// again under the lock, because a release installed while this waited
	// is the one the change applies under.
	check func(rel domain.Release, hasRel bool) error

	// desiredState is the document a sync applied, which its statement
	// names (RFC 0057 decision 15). Set, the statement is filed whether or
	// not the configuration was rendered.
	desiredState *domain.AttestedDesiredState
}

// runChange is the operation behind `config apply`, `config set` of a
// parameter (RFC 0056 decision 14) and a sync (RFC 0057).
func (d *Deps) runChange(ctx context.Context, opts Options, change ConfigChange, extras changeExtras) (changeRun, error) {
	var run changeRun
	if change.IsEmpty() && extras.record == nil {
		run.noChange = true
		return run, nil
	}
	inst, rel, hasRel, err := d.changeTarget(ctx, change)
	if err != nil {
		return run, err
	}
	if extras.check != nil {
		if err := extras.check(rel, hasRel); err != nil {
			return run, err
		}
	} else if err := refuseManagedLocally(inst, rel, hasRel, change, opts.Force); err != nil {
		return run, err
	}
	planned, err := d.planChange(ctx, inst, rel, hasRel, change, opts.Force)
	if err != nil {
		return run, err
	}
	if planned.names.empty() && extras.record == nil {
		run.noChange = true
		return run, nil
	}
	// Before anything runs that could print them: the values are only in
	// the store once the change is staged.
	if d.Redactor != nil && len(change.Secrets) > 0 {
		d.Redactor.RegisterSet(domain.NewSecretSet(change.Secrets))
	}

	opID := d.newOpID()
	op := engine.Operation{ID: opID, Type: domain.OpTypeConfig}
	var result engine.Result
	runErr := d.withLock(ctx, opID, domain.OpTypeConfig, opts, func(ctx context.Context) error {
		// Again from the installation as it stands under the lock, as
		// `config set` and `SetSettings` do: the steps save a whole
		// installation, and building them from the copy read above would
		// write away whatever landed while this waited.
		var err error
		if inst, rel, hasRel, err = d.changeTarget(ctx, change); err != nil {
			return err
		}
		if extras.check != nil {
			if err := extras.check(rel, hasRel); err != nil {
				return err
			}
		}
		if planned, err = d.planChange(ctx, inst, rel, hasRel, change, opts.Force); err != nil {
			return err
		}
		if extras.record != nil {
			if err := extras.record(&planned.after); err != nil {
				return err
			}
		}
		run.before, run.after, run.rel, run.names = inst, planned.after, rel, planned.names
		if planned.names.empty() {
			run.noChange = true
			// Nothing to apply, and still a document to record: its
			// serial, so the next tick does not fetch it again.
			if extras.record != nil && !opts.DryRun {
				return d.saveInstallation(ctx, planned.after)
			}
			return nil
		}
		if op.Steps, err = d.changeSteps(ctx, &run, planned, change, opts, extras.record != nil); err != nil {
			return err
		}
		// "set", as `config set` has always described itself in the
		// journal, whichever command ran it.
		op.Description = "set " + strings.Join(planned.names.list(), ", ")
		op.Flags = planned.names.flags()
		maps.Copy(op.Flags, settingsFlags(inst, planned.after))
		if result, err = d.Engine.Run(ctx, op, d.engineOptions(opts, inst.ID, nil)); err != nil {
			return err
		}
		if opts.DryRun || len(planned.names.settingNames()) == 0 {
			return nil
		}
		// After the state, as `SetSettings` does: the units are derived
		// from it (RFC 0056 decision 12).
		if err := d.refreshUnits(ctx, planned.after); err != nil {
			return err
		}
		d.retireMetricsFiles(ctx, inst, planned.after)
		return nil
	})
	if run.noChange && runErr == nil {
		return run, nil
	}
	run.record = result.Record
	d.notifyFinished(ctx, opID, domain.OpTypeConfig, result.Record, runErr)
	// A statement digests the rendered configuration, so a change that
	// rendered none files none, as a setting change that reaches no
	// service files none. A sync files one either way, to name the
	// document it applied, with no digest when it rendered nothing.
	if !opts.DryRun && (run.rendered || extras.desiredState != nil) {
		st := result.State
		if !run.rendered {
			st = nil
		}
		attestConfig(ctx, d, result.Record, run.before, run.rel, st, extras.desiredState)
	}
	return run, runErr
}

// changeTarget loads the installation and, when one is installed, the
// release. A change that names a parameter or a secret needs a release to
// declare it.
func (d *Deps) changeTarget(ctx context.Context, change ConfigChange) (domain.Installation, domain.Release, bool, error) {
	if len(change.Parameters)+len(change.Secrets)+len(change.Unset.Parameters)+len(change.Unset.Secrets) > 0 {
		inst, rel, err := d.currentInstallationAndRelease(ctx)
		return inst, rel, err == nil, err
	}
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return inst, domain.Release{}, false, err
	}
	current, err := d.State.CurrentRelease(ctx)
	if err != nil || current.IsZero() {
		return inst, domain.Release{}, false, err
	}
	rel, err := d.resolveCurrentRelease(ctx, current)
	return inst, rel, err == nil, err
}

// planChange checks a change against an installation and returns the
// installation it leaves. Every problem is collected, so a document is fixed
// in one pass (RFC 0056 decision 5).
func (d *Deps) planChange(
	ctx context.Context, inst domain.Installation, rel domain.Release, hasRel bool,
	change ConfigChange, force bool,
) (plannedChange, error) {
	planned := plannedChange{after: inst, hasRel: hasRel}
	var problems []error

	for _, section := range []struct {
		name       string
		set, unset []string
	}{
		{"settings", slices.Collect(maps.Keys(change.Settings)), change.Unset.Settings},
		{"parameters", slices.Collect(maps.Keys(change.Parameters)), change.Unset.Parameters},
		{"secrets", slices.Collect(maps.Keys(change.Secrets)), change.Unset.Secrets},
	} {
		for _, name := range sortedStrings(section.set) {
			if slices.Contains(section.unset, name) {
				problems = append(problems, domain.Usage("%s.%s is both set and unset", section.name, name))
			}
		}
	}

	// Settings, through each one's own Apply, as `config set` takes them.
	next := inst
	for _, name := range sortedStrings(change.Unset.Settings) {
		s, ok := settings[name]
		if !ok {
			problems = append(problems, unknownSetting(name))
			continue
		}
		before := s.stored(next)
		s.Clear(&next)
		if s.stored(next) != before {
			planned.names.unsetSettings = append(planned.names.unsetSettings, name)
		}
	}
	for _, name := range sortedSettingNames(change.Settings) {
		s, ok := settings[name]
		if !ok {
			problems = append(problems, unknownSetting(name))
			continue
		}
		before := s.stored(next)
		if err := s.Apply(ctx, d, &next, change.Settings[name]); err != nil {
			problems = append(problems, err)
			continue
		}
		if s.stored(next) != before {
			planned.names.settings = append(planned.names.settings, name)
		}
	}
	if err := checkSignaturePolicy(inst, next); err != nil {
		problems = append(problems, err)
	}
	if err := checkSyncPolicy(next); err != nil {
		problems = append(problems, err)
	}

	// Parameters: each name on its own first, so every bad one is named,
	// then the whole set through mergeParameters, which `config set` uses.
	if len(change.Parameters)+len(change.Unset.Parameters) > 0 {
		var bad bool
		for _, name := range sortedStrings(change.Unset.Parameters) {
			_, declared := rel.Manifest.Parameters[name]
			if _, recorded := inst.Parameters[name]; !declared && !recorded {
				problems, bad = append(problems, undeclaredIn(name, rel)), true
			}
		}
		for _, name := range sortedStrings(slices.Collect(maps.Keys(change.Parameters))) {
			spec, declared := rel.Manifest.Parameters[name]
			if !declared {
				problems, bad = append(problems, undeclaredIn(name, rel)), true
				continue
			}
			if _, err := spec.Parse(change.Parameters[name]); err != nil {
				problems, bad = append(problems, domain.RefusedParameterValue(err, name, spec)), true
			}
		}
		if !bad {
			params, changed, err := mergeParameters(next, rel, ConfigSetOptions{
				Set: change.Parameters, Unset: change.Unset.Parameters,
			})
			if err != nil {
				problems = append(problems, err)
			} else {
				next.Parameters = params
				for _, name := range changed {
					if slices.Contains(change.Unset.Parameters, name) {
						planned.names.unsetParameters = append(planned.names.unsetParameters, name)
					} else {
						planned.names.parameters = append(planned.names.parameters, name)
					}
				}
			}
		}
	}

	// Secrets, against the release's schema and the store.
	if len(change.Secrets)+len(change.Unset.Secrets) > 0 {
		schema, err := release.LoadSecretSchema(rel)
		if err != nil {
			return planned, err
		}
		planned.schema = schema
		stored, err := d.Secrets.Load(ctx)
		if err != nil {
			return planned, err
		}
		for _, name := range sortedStrings(slices.Collect(maps.Keys(change.Secrets))) {
			value := change.Secrets[name]
			if _, declared := schema.Declaration(name); !declared {
				problems = append(problems, domain.Usage("%s declares no secret named %q", rel.Version(), name).
					WithHint("`morzer secret list` shows the secrets it declares"))
				continue
			}
			if value.IsEmpty() {
				problems = append(problems, domain.Usage("the value for secret %q is empty", name).
					WithHint("list it under unset.secrets to remove it"))
				continue
			}
			if current, ok := stored.Get(name); !ok || current.Reveal() != value.Reveal() {
				planned.names.secrets = append(planned.names.secrets, name)
			}
		}
		for _, name := range sortedStrings(change.Unset.Secrets) {
			decl, declared := schema.Declaration(name)
			present := stored.Has(name)
			switch {
			case !declared && !present:
				problems = append(problems, domain.Usage("%s declares no secret named %q, and none is stored", rel.Version(), name))
			case declared && decl.Required && !force:
				problems = append(problems, domain.Usage("secret %q is required by the installed release", name).
					WithHint("removing it will make the next `apply` fail; pass --force if you mean to"))
			case present:
				planned.names.unsetSecrets = append(planned.names.unsetSecrets, name)
			}
		}
	}

	if len(problems) > 0 {
		return planned, refuseAll(problems)
	}
	planned.after = next
	return planned, nil
}

// refuseAll is one refusal for every problem a change has. One problem is
// returned as it is, with its hint and exit code.
func refuseAll(problems []error) error {
	if len(problems) == 1 {
		return problems[0]
	}
	lines := make([]string, len(problems))
	for i, p := range problems {
		lines[i] = "  - " + domain.AsError(p).Message
	}
	return domain.Usage("the change has %d problems:\n%s", len(problems), strings.Join(lines, "\n"))
}

// changeSteps is RFC 0056 §5.3 and §5.4: what the change touches, and the
// steps that touch it, each left out when the change does not need it.
func (d *Deps) changeSteps(
	ctx context.Context, run *changeRun, planned plannedChange, change ConfigChange, opts Options,
	recordAlways bool,
) ([]engine.Step, error) {
	before, after, rel, names := run.before, run.after, run.rel, planned.names

	var steps []engine.Step
	if recordAlways || len(names.settingNames())+len(names.parameterNames()) > 0 {
		steps = append(steps, stepRecordChange(d, before, after, names))
	}
	var stage []engine.Step
	if len(names.secretNames()) > 0 {
		stage = append(stage, stepStageSecrets(d, planned.schema, change.Secrets, names))
	}
	if !planned.hasRel {
		return append(steps, stage...), nil
	}

	run.stopped = before.Stopped != nil
	profileChange := !sameProfiles(before.Profiles, after.Profiles)
	domainChange := !slices.Equal(before.Domains, after.Domains)

	// A profile change on a running product is apply's steps under the new
	// profiles, as `config set installation.profile` runs them, with the
	// secrets staged after the restore point (decision 9).
	if profileChange && !run.stopped {
		services, err := d.runningServices(ctx, before, rel)
		if err != nil {
			// A runtime that cannot answer refuses a profile change, as
			// SetSettings refuses it.
			return nil, err
		}
		if len(services) > 0 {
			current, err := d.State.CurrentRelease(ctx)
			if err != nil {
				return nil, err
			}
			run.running, run.converged, run.rendered = true, true, true
			return append(steps, convergeUnderProfiles(d, before, after, current, rel, stage...)...), nil
		}
	}
	steps = append(steps, stage...)

	// A runtime that cannot answer counts as not running, as `config set`
	// counts it, and as holding containers, as `secret set` counts it: the
	// secret files are rendered for whatever comes back, and nothing is
	// converged.
	running, _ := d.projectRunning(ctx, before, rel, opts)
	run.running = running && !run.stopped
	if len(names.secretNames()) > 0 && d.containersExist(ctx, before, rel) {
		// `apply`'s load aborts, since nothing before it there needs
		// undoing. Here the record and the stage do: a release missing a
		// required secret, which a forced unset can leave, undoes them.
		load := stepLoadSecrets(d, after, rel)
		load.OnFailure = engine.Compensate
		// Marked before the render, which can write some files and fail
		// before it says what it wrote: the undo then renders again.
		render := stepRenderSecrets(d, after, rel, opts)
		write := render.Execute
		render.Execute = func(ctx context.Context, st *engine.State) error {
			st.Set(keyRenderTried, true)
			return write(ctx, st)
		}
		steps = append(steps, load, render)
	}
	if len(names.parameterNames()) > 0 || domainChange || profileChange {
		steps = append(steps, stepRenderConfiguration(d, after, rel, Options{}))
		run.rendered = true
	}

	var union []string
	if domainChange && run.running {
		services, err := d.runningServices(ctx, before, rel)
		if err != nil {
			return nil, err
		}
		union = append(union, services...)
	}
	union = append(union, affectedServices(rel, names.parameterNames())...)
	union = append(union, planned.schema.ServicesFor(names.secretNames())...)
	run.declared = sortedUnion(union)
	if !run.running || len(run.declared) == 0 {
		return steps, nil
	}
	// Only what the configuration after the change starts (#325, #329).
	started, err := d.startedServices(ctx, after, rel)
	if err != nil {
		return nil, err
	}
	run.services = slices.DeleteFunc(slices.Clone(run.declared), func(s string) bool {
		return !slices.Contains(started, s)
	})
	if len(run.services) == 0 {
		return steps, nil
	}
	if len(names.parameterNames()) == 0 && !domainChange && !profileChange {
		// A mounted file is re-read on restart, and no definition
		// changed, as `secret set` restarts.
		run.restarted = true
		steps = append(steps, stepRestartForSecrets(d, before, after, rel, planned.schema, run.services))
	} else {
		steps = append(steps, stepRecreateForChange(d, before, after, rel, planned.schema, run.services,
			len(names.secretNames()) > 0))
	}
	return append(steps, stepHealthChecks(d, after, rel)), nil
}

// stepRecordChange writes the settings and parameters in one write, named for
// what it records.
func stepRecordChange(d *Deps, before, after domain.Installation, names changeNames) engine.Step {
	switch params, settings := len(names.parameterNames()), len(names.settingNames()); {
	case params == 0 && settings > 0:
		return stepRecordSettings(d, before, after)
	case settings == 0 && params > 0:
		return stepRecordParameters(d, before, after)
	}
	step := stepRecordParameters(d, before, after)
	step.ID, step.Description = "record-installation", "record settings and parameters"
	if len(names.settingNames())+len(names.parameterNames()) == 0 {
		// A sync's change of secrets alone, which still records its serial.
		step.Description = "record the installation"
	}
	return step
}

// stagedSecret is a secret as it was before a change touched it.
type stagedSecret struct {
	value     domain.Secret
	changedAt time.Time
	present   bool
}

const (
	keyStagedForUndo = "config-apply-staged"
	keyPutBackDone   = "config-apply-put-back"
	keyRenderTried   = "config-apply-render-tried"
)

// stepStageSecrets stores the change's secrets, keeping what each name held.
func stepStageSecrets(d *Deps, schema domain.SecretSchema, set map[string]domain.Secret, names changeNames) engine.Step {
	return engine.Step{
		ID:          "stage-secrets",
		Description: "store " + strings.Join(names.secretNames(), ", "),
		Idempotent:  true,
		OnFailure:   engine.Compensate,
		Timeout:     2 * time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			prior, err := d.priorSecrets(ctx, names.secretNames())
			if err != nil {
				return err
			}
			st.Set(keyStagedForUndo, prior)
			for _, name := range names.secrets {
				if err := d.Secrets.Set(ctx, name, set[name]); err != nil {
					return err
				}
			}
			for _, name := range names.unsetSecrets {
				if err := d.Secrets.Remove(ctx, name); err != nil {
					return err
				}
			}
			st.Detail("%d secret(s) stored, %d removed", len(names.secrets), len(names.unsetSecrets))
			return nil
		},
		Compensate: func(ctx context.Context, st *engine.State) error {
			return d.putSecretsBack(ctx, st, schema)
		},
	}
}

func (d *Deps) priorSecrets(ctx context.Context, names []string) (map[string]stagedSecret, error) {
	set, err := d.Secrets.Load(ctx)
	if err != nil {
		return nil, err
	}
	meta, err := d.Secrets.Metadata(ctx)
	if err != nil {
		return nil, err
	}
	changed := make(map[string]time.Time, len(meta))
	for _, m := range meta {
		changed[m.Name] = m.LastChanged.Time
	}
	prior := make(map[string]stagedSecret, len(names))
	for _, name := range names {
		value, present := set.Get(name)
		prior[name] = stagedSecret{value: value, changedAt: changed[name], present: present}
	}
	return prior, nil
}

// putSecretsBack is the secret half of every undo: each name the change
// touched as it was, with its change time (RFC 0056 §5.5), then the files
// rendered again if this operation rendered them or began to. `apply`'s render-secrets
// undo keeps the files while the product runs and removes them when nothing
// runs, so without the render a failure would leave the store old and the
// files new (decision 9). Once per operation: the re-create's undo runs it
// before the stage's does.
func (d *Deps) putSecretsBack(ctx context.Context, st *engine.State, schema domain.SecretSchema) error {
	staged, ok := st.Get(keyStagedForUndo)
	if !ok {
		return nil
	}
	if _, done := st.Get(keyPutBackDone); done {
		return nil
	}
	prior, _ := staged.(map[string]stagedSecret)
	var errs []error
	for _, name := range sortedStrings(slices.Collect(maps.Keys(prior))) {
		p := prior[name]
		if p.present {
			errs = append(errs, d.Secrets.Restore(ctx, name, p.value, p.changedAt))
		} else {
			errs = append(errs, d.Secrets.Remove(ctx, name))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	_, rendered := st.Get(engine.KeyRenderedFiles)
	_, tried := st.Get(keyRenderTried)
	if rendered || tried {
		if _, err := d.Secrets.Render(ctx, d.Paths.SecretsRenderDir(), schema); err != nil {
			return err
		}
	}
	st.Set(keyPutBackDone, true)
	return nil
}

// stepRecreateForChange is `config set`'s re-create, whose undo also puts
// the secrets back first when the change holds any: the engine undoes the
// failed step before the stage, so the previous services would otherwise
// come back on the new secret files.
func stepRecreateForChange(
	d *Deps, before, after domain.Installation, rel domain.Release, schema domain.SecretSchema,
	services []string, secrets bool,
) engine.Step {
	step := stepRecreateServices(d, before, after, rel, services)
	if !secrets {
		return step
	}
	undo := step.Compensate
	step.Compensate = func(ctx context.Context, st *engine.State) error {
		if err := d.putSecretsBack(ctx, st, schema); err != nil {
			return err
		}
		return undo(ctx, st)
	}
	return step
}

// stepRestartForSecrets restarts the dependents of a change of secrets alone,
// and restarts them again on the old files if a later step fails.
func stepRestartForSecrets(
	d *Deps, before, after domain.Installation, rel domain.Release, schema domain.SecretSchema, services []string,
) engine.Step {
	restart := func(ctx context.Context, inst domain.Installation) error {
		cfg, err := d.runtimeConfig(rel, inst, nil)
		if err != nil {
			return err
		}
		return d.Runtime.Restart(ctx, cfg, services)
	}
	return engine.Step{
		ID:          "restart-services",
		Description: "restart " + strings.Join(services, ", "),
		Idempotent:  true,
		OnFailure:   engine.Compensate,
		Timeout:     10 * time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			return restart(ctx, after)
		},
		Compensate: func(ctx context.Context, st *engine.State) error {
			if err := d.putSecretsBack(ctx, st, schema); err != nil {
				return err
			}
			return restart(ctx, before)
		},
	}
}

// containersExist reports whether the project has any container in any state.
// A runtime that cannot answer may have them, so it counts as yes, as
// `secret set` counts it.
func (d *Deps) containersExist(ctx context.Context, inst domain.Installation, rel domain.Release) bool {
	cfg, err := d.runtimeConfig(rel, inst, nil)
	if err != nil {
		return true
	}
	states, err := d.Runtime.Status(ctx, cfg)
	return err != nil || len(states) > 0
}

// changeSummary says what a change did, in the tense it happened in.
func changeSummary(run changeRun, dryRun bool) string {
	tense := func(done, planned string) string {
		if dryRun {
			return planned
		}
		return done
	}
	head := tense("applied ", "would apply ") + strings.Join(run.names.list(), ", ")
	services := strings.Join(run.services, ", ")
	switch {
	case run.stopped:
		return head + "; the product is stopped, so it " + tense("takes", "would take") +
			" effect on `morzer start`"
	case run.converged:
		return head + tense(" and re-created every service under ", " and re-create every service under ") +
			profileName(run.after.Profiles)
	case len(run.services) > 0 && run.restarted:
		return head + "; " + tense("restarted ", "would restart ") + services
	case len(run.services) > 0:
		return head + "; " + tense("re-created ", "would re-create ") + services
	case len(run.declared) > 0 && run.running:
		return head + "; the services that read it are not in the active profiles, so nothing " +
			tense("was", "would be") + " re-created"
	case len(run.declared) > 0:
		return head + "; nothing is running, so it " + tense("takes", "would take") +
			" effect on the next `morzer apply`"
	}
	return head
}

func sortedStrings(names []string) []string {
	out := slices.Clone(names)
	sort.Strings(out)
	return out
}

func sortedUnion(lists ...[]string) []string {
	var out []string
	for _, list := range lists {
		for _, name := range list {
			if !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out
}
