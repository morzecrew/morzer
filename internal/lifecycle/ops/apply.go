package ops

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/adapters/hooks"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/lifecycle/engine"
	"github.com/morzecrew/morzer/internal/lifecycle/preflight"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

// Apply converges the system to the state described by the current release and
// the installation.
//
// It is idempotent: every step has a Check that reports whether its
// postcondition already holds, so a second `apply` on an unchanged system runs
// nothing and says so. That property is what lets systemd call it at every
// boot without it being a deployment.
func Apply(ctx context.Context, d *Deps, opts Options) (Result, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return Result{}, err
	}

	current, err := d.State.CurrentRelease(ctx)
	if err != nil {
		return Result{}, err
	}

	rel, err := d.resolveCurrentRelease(ctx, current)
	if err != nil {
		return Result{}, err
	}

	opID := d.newOpID()
	var prior *domain.OperationRecord
	if opts.Resume {
		if prior, err = d.findResumable(ctx, domain.OpTypeApply); err != nil {
			return Result{}, err
		}
	}

	op := engine.Operation{
		ID:          opID,
		Type:        domain.OpTypeApply,
		Description: describe(domain.OpTypeApply, domain.Version{}, rel.Version()),
		To:          rel.Version(),
		Steps:       applySteps(d, inst, rel, opts),
		Flags:       applyFlags(opts),
	}

	var result engine.Result
	runErr := d.withLock(ctx, opID, domain.OpTypeApply, opts, func(ctx context.Context) error {
		var err error
		result, err = d.Engine.Run(ctx, op, d.engineOptions(opts, inst.ID, prior))
		return err
	})

	out := Result{Record: result.Record, Summary: applySummary(result.Record, rel)}
	if runErr != nil {
		return out, runErr
	}

	d.notify(ctx, events.OperationFinished(opID, domain.OpTypeApply,
		result.Record.Status, result.Record.Duration(), nil))
	return out, nil
}

func applyFlags(opts Options) map[string]string {
	flags := map[string]string{}
	if opts.Startup {
		flags["startup"] = "true"
	}
	if opts.Profile != "" {
		flags["profile"] = opts.Profile
	}
	if len(flags) == 0 {
		return nil
	}
	return flags
}

func applySummary(rec domain.OperationRecord, rel domain.Release) string {
	skipped := 0
	for _, s := range rec.Steps {
		if s.Status == domain.StepSkipped {
			skipped++
		}
	}
	if skipped == len(rec.Steps) && len(rec.Steps) > 0 {
		return fmt.Sprintf("%s %s is already applied; nothing changed", rel.Name(), rel.Version())
	}
	return fmt.Sprintf("%s %s applied", rel.Name(), rel.Version())
}

// applySteps is the apply pipeline from the spec, one step per stage.
//
// Every step is idempotent, which is what makes the whole operation resumable.
// The mutating ones carry a Compensate where undoing is meaningful; the ones
// that only read do not, because there is nothing to undo.
func applySteps(d *Deps, inst domain.Installation, rel domain.Release, opts Options) []engine.Step {
	return []engine.Step{
		stepPreflight(d, inst, rel, opts),
		stepLoadSecrets(d, inst, rel),
		stepRenderSecrets(d, inst, rel),
		stepRenderConfiguration(d, inst, rel, opts),
		stepValidateRuntime(d, inst, rel, opts),
		stepPullImages(d, inst, rel, opts),
		stepMigrate(d, inst, rel, opts),
		stepStartServices(d, inst, rel, opts),
		stepHealthChecks(d, inst, rel),
		stepSmokeTest(d, inst, rel, opts),
		stepRecordState(d, inst, rel),
	}
}

// stepPreflight runs the checks that must pass before anything mutates.
func stepPreflight(d *Deps, inst domain.Installation, rel domain.Release, opts Options) engine.Step {
	return engine.Step{
		ID:          "preflight",
		Description: "preflight checks",
		Idempotent:  true,
		OnFailure:   engine.Abort,
		Timeout:     3 * time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			checks := []preflight.Check{
				preflight.Architecture(rel.Manifest.Requirements),
				preflight.OperatingSystem(rel.Manifest.Requirements),
			}
			checks = append(checks, preflight.Tools(d.Tools, rel.Manifest.Requirements)...)
			checks = append(checks,
				preflight.Disk(d.Paths.VarDir, rel.Manifest.Requirements.Disk),
				preflight.Memory(rel.Manifest.Requirements.Memory),
				preflight.Directories(d.Paths),
			)

			// The port check only makes sense when the project is not
			// already running: on a converged system the product's
			// own listeners would be reported as conflicts.
			if !d.projectRunning(ctx, inst, rel, opts) {
				checks = append(checks, preflight.Ports(rel.Manifest.Requirements.Ports))
			}

			report := preflight.NewRunner(d.Bus).Run(ctx, checks)
			return report.Err()
		},
	}
}

// projectRunning reports whether any service of the project is up. Errors are
// treated as "not running", because this only selects which checks to run.
func (d *Deps) projectRunning(ctx context.Context, inst domain.Installation, rel domain.Release, opts Options) bool {
	cfg, err := d.runtimeConfig(rel, inst, opts.Profile)
	if err != nil {
		return false
	}
	states, err := d.Runtime.Status(ctx, cfg)
	if err != nil {
		return false
	}
	for _, s := range states {
		if s.Running() {
			return true
		}
	}
	return false
}

// stepLoadSecrets decrypts the secret state into memory.
//
// It also registers every value with the redactor, so from this point on the
// log handler and the exec runner scrub them. Doing it here rather than at
// each use means a secret cannot leak through a code path that forgot.
func stepLoadSecrets(d *Deps, inst domain.Installation, rel domain.Release) engine.Step {
	return engine.Step{
		ID:          "load-secrets",
		Description: "decrypt secrets",
		Idempotent:  true,
		OnFailure:   engine.Abort,
		Timeout:     2 * time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			schema, err := release.LoadSecretSchema(rel)
			if err != nil {
				return err
			}
			st.Set(engine.KeySecretSchema, schema)

			set, err := d.Secrets.Load(ctx)
			if err != nil {
				return err
			}
			if d.Redactor != nil {
				d.Redactor.RegisterSet(set)
			}
			st.Set(engine.KeySecrets, set)

			// Failing here, before anything is rendered or started,
			// is the whole reason this is a separate step from
			// rendering.
			if missing := schema.Missing(set); len(missing) > 0 {
				return domain.SecretsError(domain.ErrSecretNotFound,
					"required secret(s) not set: %s", strings.Join(missing, ", ")).
					WithHint("run `morzer secret set <name>` for each, " +
						"or `morzer secret generate <name>` where the release declares a generator")
			}

			st.Detail("%d secret(s) loaded", set.Len())
			return nil
		},
	}
}

// stepRenderSecrets writes decrypted secrets to tmpfs.
//
// Compensation removes them. If a later step fails, leaving plaintext
// credentials on a filesystem that may not be tmpfs on every host is not a
// state to walk away from.
func stepRenderSecrets(d *Deps, inst domain.Installation, rel domain.Release) engine.Step {
	return engine.Step{
		ID:          "render-secrets",
		Description: "render secrets to " + d.Paths.SecretsRenderDir(),
		Idempotent:  true,
		OnFailure:   engine.Compensate,
		Timeout:     time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			schema := engine.MustGet[domain.SecretSchema](st, engine.KeySecretSchema)

			files, err := d.Secrets.Render(ctx, d.Paths.SecretsRenderDir(), schema)
			if err != nil {
				return err
			}
			st.Set(engine.KeyRenderedFiles, files)
			st.Detail("%d secret file(s)", len(files))
			return nil
		},
		Verify: func(ctx context.Context, st *engine.State) error {
			// The permissions are the security property, so they are
			// verified rather than assumed: a 0644 secret file is a
			// leak whether or not the write reported success.
			ok, detail, err := atomicfs.CheckMode(d.Paths.SecretsRenderDir(), os.ModeDir|0o700)
			if err != nil {
				return err
			}
			if !ok {
				return domain.SecretsError(nil,
					"the secret directory %s is not 0700: %s", d.Paths.SecretsRenderDir(), detail)
			}

			files := engine.MustGet[[]ports.RenderedFile](st, engine.KeyRenderedFiles)
			for _, f := range files {
				ok, detail, err := atomicfs.CheckMode(f.Path, 0o400)
				if err != nil {
					return err
				}
				if !ok {
					return domain.SecretsError(nil,
						"rendered secret %s is not 0400: %s", f.Name, detail)
				}
			}
			return nil
		},
		Compensate: func(ctx context.Context, st *engine.State) error {
			return atomicfs.RemoveAll(d.Paths.SecretsRenderDir())
		},
	}
}

// stepRenderConfiguration renders the release's configuration templates.
//
// The previous contents are captured before writing, so compensation can put
// them back: a failed apply must not leave the product configured for a
// release that is not running.
func stepRenderConfiguration(d *Deps, inst domain.Installation, rel domain.Release, opts Options) engine.Step {
	const keyBackups = "config-backups"

	renderAll := func(ctx context.Context, st *engine.State) (map[string][]byte, error) {
		// The schema is normally left in state by the load-secrets step.
		// During a dry run that step never executes, so it is loaded
		// here instead -- the schema is release metadata, not secret
		// values, so reading it has no side effects and reveals nothing.
		// Without this the plan could not show a configuration diff,
		// which is most of what a plan is for.
		schema, err := engine.GetTyped[domain.SecretSchema](st, engine.KeySecretSchema)
		if err != nil {
			if schema, err = release.LoadSecretSchema(rel); err != nil {
				return nil, err
			}
		}
		data := d.templateData(inst, rel, opts.Profile, schema)

		out := make(map[string][]byte, len(rel.Manifest.Configuration))
		for _, cfg := range rel.Manifest.Configuration {
			tmplPath, err := rel.Path(cfg.Template)
			if err != nil {
				return nil, err
			}
			rendered, err := d.Renderer.Render(ctx,
				ports.TemplateRef{Path: tmplPath, Name: cfg.Template}, data)
			if err != nil {
				return nil, err
			}
			out[d.configTarget(cfg.Target)] = rendered
		}
		return out, nil
	}

	return engine.Step{
		ID:          "render-configuration",
		Description: "render configuration",
		Idempotent:  true,
		OnFailure:   engine.Compensate,
		Timeout:     time.Minute,
		Check: func(ctx context.Context, st *engine.State) (bool, error) {
			if len(rel.Manifest.Configuration) == 0 {
				return true, nil
			}
			rendered, err := renderAll(ctx, st)
			if err != nil {
				return false, err
			}
			// Byte-identical output means there is nothing to do.
			// Rewriting an unchanged file would churn its mtime and
			// make `apply` look like it changed something.
			for target, want := range rendered {
				got, err := os.ReadFile(target)
				if err != nil || string(got) != string(want) {
					return false, nil
				}
			}
			return true, nil
		},
		PlanDetail: func(ctx context.Context, st *engine.State) (string, string) {
			rendered, err := renderAll(ctx, st)
			if err != nil {
				return "cannot render: " + domain.AsError(err).Message, ""
			}
			var diffs []string
			for target, want := range rendered {
				existing, _ := os.ReadFile(target)
				if d := unifiedDiff(target, string(existing), string(want)); d != "" {
					diffs = append(diffs, d)
				}
			}
			return fmt.Sprintf("%d file(s)", len(rendered)), strings.Join(diffs, "\n")
		},
		Execute: func(ctx context.Context, st *engine.State) error {
			rendered, err := renderAll(ctx, st)
			if err != nil {
				return err
			}

			backups := make(map[string][]byte, len(rendered))
			modes := make(map[string]uint32, len(rendered))
			for _, cfg := range rel.Manifest.Configuration {
				modes[d.configTarget(cfg.Target)] = cfg.Mode.Perm()
			}

			for target, content := range rendered {
				if existing, err := os.ReadFile(target); err == nil {
					backups[target] = existing
				}
				mode := modes[target]
				if mode == 0 {
					mode = domain.DefaultConfigMode.Perm()
				}
				if err := atomicfs.WriteFile(target, content, os.FileMode(mode)); err != nil {
					return err
				}
			}

			st.Set(keyBackups, backups)
			st.Detail("%d file(s)", len(rendered))
			return nil
		},
		Compensate: func(ctx context.Context, st *engine.State) error {
			backups, ok := st.Get(keyBackups)
			if !ok {
				return nil
			}
			previous, ok := backups.(map[string][]byte)
			if !ok {
				return nil
			}
			for target, content := range previous {
				// Best effort: a compensation that fails part
				// way should still restore what it can.
				_ = atomicfs.WriteFile(target, content, os.FileMode(domain.DefaultConfigMode.Perm()))
			}
			return nil
		},
	}
}

// stepValidateRuntime parses and checks the Compose configuration.
func stepValidateRuntime(d *Deps, inst domain.Installation, rel domain.Release, opts Options) engine.Step {
	return engine.Step{
		ID:          "validate-runtime",
		Description: "validate compose configuration",
		Idempotent:  true,
		OnFailure:   engine.Compensate,
		Timeout:     2 * time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			cfg, err := d.runtimeConfig(rel, inst, opts.Profile)
			if err != nil {
				return err
			}
			st.Set(engine.KeyRuntimeConfig, cfg)

			rendered, err := d.Runtime.Validate(ctx, cfg)
			if err != nil {
				return err
			}
			st.Detail("%d service(s)", len(rendered.Services))
			return nil
		},
	}
}

// stepPullImages fetches images by digest.
func stepPullImages(d *Deps, inst domain.Installation, rel domain.Release, opts Options) engine.Step {
	return engine.Step{
		ID:          "pull-images",
		Description: "pull images",
		Idempotent:  true,
		OnFailure:   engine.Compensate,
		Timeout:     45 * time.Minute,
		Check: func(ctx context.Context, st *engine.State) (bool, error) {
			// --startup runs at boot, possibly before the network is
			// up. Skipping the pull when images are already local is
			// what lets a machine come back after a reboot without
			// connectivity.
			if opts.Startup {
				return true, nil
			}
			return false, nil
		},
		Execute: func(ctx context.Context, st *engine.State) error {
			cfg, err := engine.GetTyped[ports.RuntimeConfig](st, engine.KeyRuntimeConfig)
			if err != nil {
				return err
			}
			images := rel.Manifest.ImageRefs()
			st.Detail("%d image(s)", len(images))
			return d.Runtime.Pull(ctx, cfg, images)
		},
	}
}

// stepMigrate runs the release's migration operation.
//
// It is deliberately not compensable. A migration that partially applied
// cannot be undone by re-running anything the manager knows about, so a
// failure here escalates to requires-manual-intervention rather than
// pretending a rollback happened.
func stepMigrate(d *Deps, inst domain.Installation, rel domain.Release, opts Options) engine.Step {
	spec, declared := rel.Manifest.Operation(domain.OpMigrate)

	return engine.Step{
		ID:          "migrate",
		Description: "run migrations",
		Idempotent:  true,
		OnFailure:   engine.Compensate,
		// A migration that partially applied cannot be undone by
		// anything the manager knows about, so a failure here stops and
		// asks for a human rather than pretending a rollback happened.
		RequiresInterventionOnFailure: true,
		Timeout:                       spec.Timeout.Or(30 * time.Minute),
		Check: func(ctx context.Context, st *engine.State) (bool, error) {
			return !declared, nil
		},
		Execute: func(ctx context.Context, st *engine.State) error {
			switch spec.Kind {
			case domain.OperationKindRuntimeService:
				cfg, err := engine.GetTyped[ports.RuntimeConfig](st, engine.KeyRuntimeConfig)
				if err != nil {
					return err
				}
				res, err := d.Runtime.RunOneShot(ctx, cfg, spec.Service, ports.RunOptions{
					Timeout: spec.Timeout.Or(30 * time.Minute),
					Remove:  true,
				})
				if err != nil {
					return err
				}
				if !res.OK() {
					return domain.RuntimeError(nil,
						"the migration service %q exited with code %d", spec.Service, res.ExitCode).
						WithHint("inspect the migration output in the log; " +
							"the database may be partially migrated")
				}
				return nil

			case domain.OperationKindHook:
				env := d.hookEnv(inst, rel, domain.Version{}, st.OpID,
					domain.OpTypeApply, hooks.PhaseMigrate, st.DryRun)

				outcome, err := d.Hooks.Run(ctx, rel, spec.Command, env,
					spec.Timeout.Or(30*time.Minute))
				if err != nil {
					return err
				}
				if outcome.Skipped {
					st.Detail("schema is already current")
				}
				if outcome.Result.SchemaVersion > 0 {
					// Recorded so rollback can reason about
					// schema compatibility without re-running
					// the product's tooling to ask.
					st.Set(engine.KeySchemaVersion, outcome.Result.SchemaVersion)
				}
				return nil

			default:
				return domain.ValidationError(nil,
					"the migrate operation has an unsupported kind %q", spec.Kind)
			}
		},
	}
}

// stepStartServices brings the project up and waits for container health.
func stepStartServices(d *Deps, inst domain.Installation, rel domain.Release, opts Options) engine.Step {
	return engine.Step{
		ID:          "start-services",
		Description: "start services",
		Idempotent:  true,
		OnFailure:   engine.Compensate,
		Timeout:     20 * time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			cfg, err := engine.GetTyped[ports.RuntimeConfig](st, engine.KeyRuntimeConfig)
			if err != nil {
				return err
			}
			return d.Runtime.Up(ctx, cfg, ports.UpOptions{
				Wait:          true,
				WaitTimeout:   10 * time.Minute,
				RemoveOrphans: true,
			})
		},
		Verify: func(ctx context.Context, st *engine.State) error {
			cfg, err := engine.GetTyped[ports.RuntimeConfig](st, engine.KeyRuntimeConfig)
			if err != nil {
				return err
			}
			states, err := d.Runtime.Status(ctx, cfg)
			if err != nil {
				return err
			}
			var down []string
			for _, s := range states {
				// A one-shot service that exited cleanly is not
				// a failure: migration containers legitimately
				// finish and stay finished.
				if s.State == "exited" && s.ExitCode == 0 {
					continue
				}
				if !s.Running() {
					down = append(down, fmt.Sprintf("%s (%s)", s.Name, s.State))
				}
			}
			if len(down) > 0 {
				return domain.RuntimeError(nil,
					"service(s) not running after start: %s", strings.Join(down, ", ")).
					WithHint("run `docker compose logs` for the failing service")
			}
			return nil
		},
		// No Compensate: stopping the services would take the product
		// down, which on an `apply` over a running deployment is worse
		// than leaving the previous version serving traffic. `update`
		// handles reverting the release pointer separately.
	}
}

// stepHealthChecks waits for the application to report ready.
func stepHealthChecks(d *Deps, inst domain.Installation, rel domain.Release) engine.Step {
	return engine.Step{
		ID:          "health-checks",
		Description: "wait for health checks",
		Idempotent:  true,
		OnFailure:   engine.Compensate,
		Timeout:     15 * time.Minute,
		Check: func(ctx context.Context, st *engine.State) (bool, error) {
			return len(rel.Manifest.Health.Checks) == 0, nil
		},
		Execute: func(ctx context.Context, st *engine.State) error {
			specs := d.checkSpecs(inst, rel, st.OpID, domain.OpTypeApply)

			results, err := d.Health.WaitReady(ctx, specs)
			st.Set(engine.KeyHealthResults, results)
			if err != nil {
				return err
			}
			st.Detail("%d check(s) passing", len(results))
			return nil
		},
	}
}

// checkSpecs resolves manifest health checks into runnable specs.
func (d *Deps) checkSpecs(inst domain.Installation, rel domain.Release, opID string, opType domain.OperationType) []ports.CheckSpec {
	env := d.hookEnv(inst, rel, domain.Version{}, opID, opType, hooks.PhaseHealthCheck, false)

	specs := make([]ports.CheckSpec, 0, len(rel.Manifest.Health.Checks))
	for _, check := range rel.Manifest.Health.Checks {
		specs = append(specs, ports.CheckSpec{
			Check:      check,
			WorkingDir: rel.Root,
			Env:        env.Vars(),
		})
	}
	return specs
}

// stepSmokeTest runs the release's end-to-end check.
func stepSmokeTest(d *Deps, inst domain.Installation, rel domain.Release, opts Options) engine.Step {
	spec, declared := rel.Manifest.Operation(domain.OpSmokeTest)

	return engine.Step{
		ID:          "smoke-test",
		Description: "smoke test",
		Idempotent:  true,
		OnFailure:   engine.Compensate,
		Timeout:     spec.Timeout.Or(10 * time.Minute),
		Check: func(ctx context.Context, st *engine.State) (bool, error) {
			return !declared, nil
		},
		Execute: func(ctx context.Context, st *engine.State) error {
			env := d.hookEnv(inst, rel, domain.Version{}, st.OpID,
				domain.OpTypeApply, hooks.PhaseSmokeTest, st.DryRun)

			outcome, err := d.Hooks.Run(ctx, rel, spec.Command, env, spec.Timeout.Or(10*time.Minute))
			if err != nil {
				return domain.HealthError(err, "the smoke test failed").
					WithHint("the product started but does not behave correctly; " +
						"the test's output is in the log")
			}
			if outcome.Result.Message != "" {
				st.Detail("%s", outcome.Result.Message)
			}
			return nil
		},
	}
}

// stepRecordState writes the release pointer and the current symlink.
func stepRecordState(d *Deps, inst domain.Installation, rel domain.Release) engine.Step {
	return engine.Step{
		ID:          "record-state",
		Description: "record installed release",
		Idempotent:  true,
		OnFailure:   engine.Abort,
		Timeout:     time.Minute,
		Execute: func(ctx context.Context, st *engine.State) error {
			record := domain.ReleaseRecord{
				SchemaVersion:   domain.InstallationSchemaVersion,
				Name:            rel.Name(),
				Version:         rel.Version(),
				Digest:          rel.Digest,
				Root:            rel.Root,
				InstalledAt:     domain.NewTime(d.now()),
				OperationID:     st.OpID,
				SchemaAtInstall: engine.MustGet[int](st, engine.KeySchemaVersion),
			}
			if err := d.State.SetCurrentRelease(ctx, record); err != nil {
				return err
			}
			// The symlink swap is what makes `current` atomic for
			// anything reading the filesystem directly.
			return atomicfs.ReplaceSymlink(rel.Root, d.Paths.CurrentLink())
		},
	}
}

// resolveCurrentRelease loads the release the installation points at.
func (d *Deps) resolveCurrentRelease(ctx context.Context, record domain.ReleaseRecord) (domain.Release, error) {
	root := record.Root
	if root == "" {
		// Fall back to the current symlink: a state file lost to a
		// partial restore should not make the deployment unreadable
		// when /opt still says what is installed.
		target, err := atomicfs.ReadSymlink(d.Paths.CurrentLink())
		if err != nil {
			return domain.Release{}, err
		}
		root = target
	}
	if root == "" {
		return domain.Release{}, domain.InstallationError(domain.ErrReleaseNotFound,
			"no release is installed").
			WithHint("run `morzer update <bundle>` to install one")
	}

	rel, err := release.Load(root)
	if err != nil {
		return domain.Release{}, err
	}

	// The same version appearing with a different digest is an error, not a
	// warning: it means the release directory was modified after
	// installation, and nothing downstream can be trusted to be what was
	// verified.
	if record.Digest != "" && !atomicfs.SameDigest(rel.Digest, record.Digest) {
		return domain.Release{}, domain.ValidationError(domain.ErrDigestMismatch,
			"the release at %s no longer matches the digest recorded at install time", root).
			WithHint("the release directory has been modified; reinstall the bundle")
	}
	return rel, nil
}

// findResumable locates the operation --resume should continue.
func (d *Deps) findResumable(ctx context.Context, opType domain.OperationType) (*domain.OperationRecord, error) {
	unfinished, err := d.State.UnfinishedOperations(ctx)
	if err != nil {
		return nil, err
	}
	for _, rec := range unfinished {
		if rec.Type == opType {
			return &rec, nil
		}
	}
	return nil, domain.Usage("no interrupted %s operation to resume", opType).
		WithHint("run the command without --resume")
}
