package ops

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

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

	// What this deployment is running under, carried on the in-memory copy
	// so every later check -- including the one inside runtimeConfig -- asks
	// the same question the refusal below asked. `apply` converges the
	// release it is already on, so the baseline and the candidate are the
	// same manifest and this can only refuse a hand-edited record.
	baseline := runtimeBaseline(inst, rel)
	inst.RuntimeOptions = baseline
	if err := d.refuseRuntimeOptionChange(inst, rel); err != nil {
		return Result{}, err
	}

	opID := d.newOpID()
	var prior *domain.OperationRecord
	if opts.Resume {
		if prior, err = d.findResumable(ctx, domain.OpTypeApply); err != nil {
			return Result{}, err
		}
	}
	if err := d.gateUnfinished(ctx, excludeID(prior)); err != nil {
		return Result{}, err
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
		// Under the lock, because it is a read-modify-write of a record
		// `config set` and `init --repair` also write.
		if !opts.DryRun {
			if err := d.persistRuntimeBaseline(ctx, baseline); err != nil {
				return err
			}
		}
		// Re-checked under the lock: with --wait, another process can
		// journal a wreck between the early check and this acquisition,
		// then die -- and the whole point of the gate is not driving
		// over exactly that. A resume additionally re-reads its record:
		// the lock holder we waited behind may have been resuming the
		// same operation, and finishing it.
		if opts.Resume {
			if prior, err = d.refreshResumable(ctx, domain.OpTypeApply, prior); err != nil {
				return err
			}
		}
		if err := d.gateUnfinished(ctx, excludeID(prior)); err != nil {
			return err
		}
		if err := d.refuseMovedRelease(ctx, domain.OpTypeApply, current); err != nil {
			return err
		}
		result, err = d.Engine.Run(ctx, op, d.engineOptions(opts, inst.ID, prior))
		return err
	})

	out := Result{Record: result.Record, Summary: applySummary(result.Record, rel, opts.DryRun)}
	d.notifyFinished(ctx, opID, domain.OpTypeApply, result.Record, runErr)

	// After the operation, never inside it. A statement is a record *of*
	// what happened, so it cannot be a step whose own failure changes what
	// happened -- and emitAttestation warns rather than returning for the
	// same reason (RFC 0025 decision 6).
	//
	// **Before the error is returned, not after.** A successful update is
	// the least interesting event to an auditor; the failed one that rolled
	// back is what they ask about, and a system that attests only its
	// successes attests nothing. The record already carries `failed` or
	// `compensated` and a step list that stops where the failure did, so
	// the statement needs nothing the success path does not have.
	if !opts.DryRun {
		emitAttestation(ctx, d, result.Record,
			attestationInputs(inst, rel, current, domain.Version{}, renderedConfigFor(result.State)))
	}

	if runErr != nil {
		return out, runErr
	}
	return out, nil
}

// renderedConfigFor pulls the rendered configuration out of engine state so the
// attestation can digest it.
//
// Returns nil when the operation rendered nothing, which is a real case rather
// than a defect -- and produces an empty digest instead of a digest over
// nothing, which would be a constant that looks like evidence.
func renderedConfigFor(st *engine.State) []byte {
	if st == nil {
		return nil
	}
	// The encoding lives in the domain, because `attest verify` re-derives
	// this digest from the files on disk and a second encoder here would be
	// a second chance for the two to disagree.
	return domain.CanonicalConfig(
		engine.MustGet[map[string][]byte](st, engine.KeyRenderedConfig))
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

// applySummary says what happened, or what would, and the difference is the
// whole reason it takes dryRun.
//
// A plan runs no steps, so a summary that does not ask prints a completed
// operation in the past tense directly beneath the line saying nothing was
// changed. `init` grew `initVerb` when that was found there; the fix stopped at
// the command it was found in, and this is the same defect two commands over.
func applySummary(rec domain.OperationRecord, rel domain.Release, dryRun bool) string {
	// An operation that did not succeed gets no summary, which is what
	// `updateSummary` has always done and this one never did: asking only
	// what the steps did, it printed "demo 1.2.0 applied" between "earlier
	// changes were rolled back" and the error saying why.
	if rec.Status != domain.StatusSucceeded {
		return ""
	}

	skipped := 0
	for _, s := range rec.Steps {
		if s.Status == domain.StepSkipped {
			skipped++
		}
	}
	if skipped == len(rec.Steps) && len(rec.Steps) > 0 {
		if dryRun {
			return fmt.Sprintf("%s %s is already applied; there would be nothing to change",
				rel.Name(), rel.Version())
		}
		return fmt.Sprintf("%s %s is already applied; nothing changed", rel.Name(), rel.Version())
	}
	if dryRun {
		return fmt.Sprintf("would apply %s %s", rel.Name(), rel.Version())
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
		stepRenderSecrets(d, inst, rel, opts),
		stepRenderConfiguration(d, inst, rel, opts),
		stepValidateRuntime(d, inst, rel, opts),
		stepPullImages(d, inst, rel, opts),
		stepMigrate(d, inst, rel, opts),
		stepStartServices(d, inst, rel, opts),
		stepHealthChecks(d, inst, rel),
		stepSmokeTest(d, inst, rel, opts),
		stepRecordState(d, inst, rel, opts),
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
				preflight.CPUs(rel.Manifest.Requirements.CPUs),
				preflight.RequiredParameters(rel.Manifest.Parameters, inst.Parameters),
				preflight.Directories(d.Paths),
				preflight.BundledImages(rel.Manifest.BundledImageRefs(), d.imagePresence()),
			)

			// The port check only makes sense when the project is not
			// already running: on a converged system the product's
			// own listeners would be reported as conflicts. A runtime
			// that cannot answer gets the check.
			if running, _ := d.projectRunning(ctx, inst, rel, opts); !running {
				// Resolved, not read raw: `requirements.ports`
				// may name a parameter, and checking 18080
				// while the deployment publishes 9000 is the
				// incoherence this closes.
				params, err := d.parameters(rel, inst)
				if err != nil {
					return err
				}
				required, err := rel.Manifest.ResolvePorts(params)
				if err != nil {
					return err
				}
				checks = append(checks, preflight.Ports(required))
			}

			report := preflight.NewRunner(d.Bus).Run(ctx, checks)
			return report.Err()
		},
	}
}

// projectRunning reports whether any service of the project is up. An error
// leaves that unknown, and each caller decides which way unknown counts.
func (d *Deps) projectRunning(ctx context.Context, inst domain.Installation, rel domain.Release, opts Options) (bool, error) {
	cfg, err := d.runtimeConfig(rel, inst, opts.Profile)
	if err != nil {
		return false, err
	}
	states, err := d.Runtime.Status(ctx, cfg)
	if err != nil {
		return false, err
	}
	return anyRunning(states), nil
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
// Compensation removes them while no container reads them. If a later step
// fails, leaving plaintext credentials nothing uses on a filesystem that may
// not be tmpfs on every host is not a state to walk away from; removing them
// from under a running container only breaks its next restart.
func stepRenderSecrets(d *Deps, inst domain.Installation, rel domain.Release, opts Options) engine.Step {
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
		// Removed only while nothing was started from them, which the
		// runtime is asked rather than this operation remembering:
		// containers an earlier apply started read these files too, and
		// a start-services that failed may have started some or none. A
		// runtime that cannot answer may have them up. An apply has
		// nothing earlier to put back. An update that got as far as
		// start-services renders the previous release's secrets over
		// them on restore, which prunes whatever that release does not
		// declare; one that stopped earlier leaves the new release's.
		Compensate: func(ctx context.Context, st *engine.State) error {
			if running, err := d.projectRunning(ctx, inst, rel, opts); running || err != nil {
				return nil
			}
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
		data, err := d.templateData(inst, rel, opts.Profile, schema)
		if err != nil {
			return nil, err
		}

		out := make(map[string][]byte, len(rel.Manifest.Configuration))
		for _, cfg := range rel.Manifest.Configuration {
			// Checked here as well as inside the renderer: this refuses
			// the "../" spelling with a message about the manifest,
			// while os.Root refuses what only an open can see.
			if _, err := rel.Path(cfg.Template); err != nil {
				return nil, err
			}
			rendered, err := d.Renderer.Render(ctx,
				ports.TemplateRef{Root: rel.Root, Name: cfg.Template}, data)
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
			// Published from Check as well as Execute. A converged
			// apply skips Execute entirely, and an attestation
			// that read the configuration only from the executing
			// path digested nothing on exactly the runs where
			// nothing changed -- so two applies of identical
			// configuration produced two different digests, and a
			// drift detector with that property reports drift
			// every second run.
			st.Set(engine.KeyRenderedConfig, rendered)

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

			// Into state for the attestation, which digests it
			// under the installation's salt. The bytes stay here;
			// only the digest travels.
			st.Set(engine.KeyRenderedConfig, rendered)

			backups := make(map[string]configBackup, len(rendered))
			modes := make(map[string]uint32, len(rendered))
			for _, cfg := range rel.Manifest.Configuration {
				modes[d.configTarget(cfg.Target)] = cfg.Mode.Perm()
			}

			// Recorded before the first write, not after the last: a
			// write that fails part way leaves the files before it
			// replaced, and this step's own compensator, which runs
			// for the step that failed, has to find their backups.
			// The map is shared, so what the loop adds is seen.
			st.Set(keyBackups, backups)

			// In a fixed order, so which files a failure leaves
			// replaced does not depend on map iteration.
			for _, target := range slices.Sorted(maps.Keys(rendered)) {
				switch existing, err := os.ReadFile(target); {
				case err == nil:
					backup := configBackup{content: existing, mode: os.FileMode(domain.DefaultConfigMode.Perm())}
					if info, err := os.Stat(target); err == nil {
						backup.mode = info.Mode().Perm()
					}
					backups[target] = backup
				case errors.Is(err, fs.ErrNotExist):
					// Created by this render, so putting it back
					// means removing it.
					backups[target] = configBackup{created: true}
				default:
					// A file that exists and cannot be read would be
					// replaced with no backup, and nothing could put
					// it back: refused before it is touched.
					return domain.InstallationError(err,
						"cannot read %s to keep a copy before replacing it", target)
				}
				mode := modes[target]
				if mode == 0 {
					mode = domain.DefaultConfigMode.Perm()
				}
				if err := atomicfs.WriteFile(target, rendered[target], os.FileMode(mode)); err != nil {
					return err
				}
			}

			st.Detail("%d file(s)", len(rendered))
			return nil
		},
		Compensate: func(ctx context.Context, st *engine.State) error {
			backups, ok := st.Get(keyBackups)
			if !ok {
				return nil
			}
			previous, ok := backups.(map[string]configBackup)
			if !ok {
				return nil
			}
			var failed []string
			for target, backup := range previous {
				// Every file is attempted, so one that fails
				// does not strand the rest.
				//
				// In the file's own mode. Every restored file
				// used to come back 0640, and a failed update
				// now restarts the previous release, whose
				// containers may run as a user that only a
				// wider mode let read it.
				var err error
				if backup.created {
					if err = os.Remove(target); errors.Is(err, fs.ErrNotExist) {
						err = nil
					}
				} else {
					err = atomicfs.WriteFile(target, backup.content, backup.mode)
				}
				if err != nil {
					failed = append(failed, target)
				}
			}
			if len(failed) == 0 {
				return nil
			}
			// Reported rather than swallowed, and remembered: the
			// previous release is brought back up after this, and
			// must not be started against the new release's files.
			st.Set(keyConfigNotRestored, true)
			sort.Strings(failed)
			return domain.InstallationError(nil,
				"could not put back %s", strings.Join(failed, ", "))
		},
	}
}

// configBackup is a configuration file as it was before a render replaced it.
type configBackup struct {
	content []byte
	mode    os.FileMode

	// created is a file the render wrote where there was none.
	created bool
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
			// The images that come from a registry, not every image
			// the release names. A bundled one is already on this
			// machine under an alias, and asking for it here would
			// contact the vendor's registry -- the single thing
			// bundling exists to avoid -- under a reference that no
			// longer resolves anywhere the daemon can see.
			//
			// That its absence is not merely a redundant pull is
			// preflight's job: `images.bundled` refuses before this
			// step runs, so a bundled image missing from the store
			// never reaches here to be quietly pulled.
			images := rel.Manifest.PulledImageRefs()
			st.Detail("%d image(s)", len(images))
			return d.Runtime.Pull(ctx, cfg, images)
		},
	}
}

// stepMigrate runs the release's migration operation: one step, or several in
// the order the manifest lists them -- migrations, then the application's own
// bootstrap, say.
//
// It is deliberately not compensable. A migration that partially applied
// cannot be undone by re-running anything the manager knows about, so a
// failure here escalates to requires-manual-intervention rather than
// pretending a rollback happened. That holds for every step in the list: the
// steps before a failed one have run, and so has whatever they changed.
func stepMigrate(d *Deps, inst domain.Installation, rel domain.Release, opts Options) engine.Step {
	steps := rel.Manifest.OperationSteps(domain.OpMigrate)

	// The step's budget is the sum of its parts, each of which also bounds
	// itself, so one slow step cannot spend another's time.
	var budget time.Duration
	for _, spec := range steps {
		budget += spec.Timeout.Or(30 * time.Minute)
	}

	return engine.Step{
		ID:          "migrate",
		Description: "run migrations",
		Idempotent:  true,
		OnFailure:   engine.Compensate,
		// A migration that partially applied cannot be undone by
		// anything the manager knows about, so a failure here stops and
		// asks for a human rather than pretending a rollback happened.
		RequiresInterventionOnFailure: true,
		Timeout:                       budget,
		Check: func(ctx context.Context, st *engine.State) (bool, error) {
			return len(steps) == 0, nil
		},
		Execute: func(ctx context.Context, st *engine.State) error {
			env := d.hookEnv(inst, rel, opts.PreviousVersion, st.OpID,
				st.OpType, ports.PhaseMigrate, st.DryRun)
			reported := 0
			for i, spec := range steps {
				// Named by position only when there is more than
				// one, so a single migration reads as it always
				// has.
				which := "the migration"
				if len(steps) > 1 {
					which = fmt.Sprintf("migration step %d of %d", i+1, len(steps))
					st.Detail("%s", which)
				}
				schema, err := d.runMigrateStep(ctx, st, rel, env, spec, which)
				if err != nil {
					return err
				}
				if schema > 0 {
					reported = schema
				}
			}
			if reported > 0 {
				// Recorded so rollback can reason about schema
				// compatibility without re-running the product's
				// tooling to ask.
				st.Set(engine.KeySchemaVersion, reported)
			}
			passIfFallbackCannotRead(st, rel, reported)
			return nil
		},
	}
}

// runMigrateStep runs one step of the migrate operation and returns the schema
// version it reported, or zero.
func (d *Deps) runMigrateStep(
	ctx context.Context, st *engine.State, rel domain.Release, env ports.HookEnv,
	spec domain.OperationSpec, which string,
) (int, error) {
	switch spec.Kind {
	case domain.OperationKindRuntimeService:
		cfg, err := engine.GetTyped[ports.RuntimeConfig](st, engine.KeyRuntimeConfig)
		if err != nil {
			return 0, err
		}
		// A one-shot starts what it depends on, and on an update
		// recreates a dependency whose configuration changed, so a
		// failure from here on has services to put back, as one after
		// start-services does.
		st.Set(keyServicesTouched, true)
		res, err := d.Runtime.RunOneShot(ctx, cfg, spec.Service, ports.RunOptions{
			Env:     ports.ServiceEnvVars(env),
			Timeout: spec.Timeout.Or(30 * time.Minute),
			Remove:  true,
		})
		if err != nil {
			return 0, err
		}
		// Exit 2 is "nothing to do", as it is for a hook. The adapter
		// returns it as data for exactly this reading, and treating it
		// as a failure made a migration service that had nothing to
		// migrate stop the deployment for manual intervention.
		if res.ExitCode == 2 {
			st.Detail("schema is already current")
			return 0, nil
		}
		if !res.OK() {
			return 0, domain.RuntimeError(nil,
				"%s, the service %q, exited with code %d", which, spec.Service, res.ExitCode).
				WithHint("inspect the migration output in the log; " +
					"the database may be partially migrated")
		}
		return 0, nil

	case domain.OperationKindHook:
		outcome, err := d.Hooks.Run(ctx, rel, spec.Command, env,
			spec.Timeout.Or(30*time.Minute))
		if err != nil {
			return 0, err
		}
		if outcome.Skipped {
			st.Detail("schema is already current")
		}
		return outcome.Result.SchemaVersion, nil

	default:
		return 0, domain.ValidationError(nil,
			"%s has an unsupported kind %q", which, spec.Kind)
	}
}

// fallbackRelease is what a failed update's compensation would leave current:
// the release that was running when it began.
type fallbackRelease struct {
	Version       domain.Version
	Compatibility domain.Compatibility

	// Known is false when its manifest could not be read. Nothing is then
	// assumed about what it can run against.
	Known bool
}

// passIfFallbackCannotRead marks the point of no return once migrations have
// left a database the fallback release cannot run against.
//
// Without it, a failure after `migrate` -- a service that does not start, a
// health check that never passes -- compensated back to the old release over
// the migrated database and reported "earlier changes were rolled back": the
// old release current, recorded at the old schema, reading one it does not
// understand, and the next boot's `apply` running its migrations against it.
// That is the rollback RFC 0001 refuses to perform, performed by compensation.
//
// The judgement is AssessRollback's, as `rollback` and the unattended gate
// make it, so the three cannot disagree about what is recoverable. The schema
// is the one the hook reported, then the one the release declares it produces.
// A skipped migration is judged too: on a resumed update it is the attempt
// before the crash that moved the schema. An operation with no fallback --
// apply, rollback, a first install -- has nothing to protect.
func passIfFallbackCannotRead(st *engine.State, rel domain.Release, reported int) {
	fb, err := engine.GetTyped[fallbackRelease](st, engine.KeyFallbackRelease)
	if err != nil {
		return
	}
	if !fb.Known {
		st.PassPointOfNoReturn(fmt.Sprintf("the manifest of %s, which a rollback would "+
			"return to, could not be read, so whether it can run against the migrated "+
			"database is unknown", fb.Version))
		return
	}
	schema := reported
	if schema <= 0 {
		schema = rel.Manifest.Compatibility.DatabaseSchemaProduces
	}
	if a := domain.AssessRollback(rel.Manifest.Compatibility, fb.Compatibility, schema); a.RestoreRequired {
		st.PassPointOfNoReturn(fmt.Sprintf("returning to %s would need a database restore (%s). "+
			"%s's configuration and containers are left in place, and once the cause is fixed "+
			"`morzer update --to %s` finishes the update; restoring the pre-update backup is "+
			"the way back", fb.Version, a.Reason, rel.Version(), rel.Version()))
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
			// Before Up, not after: a failed Up may still have
			// replaced some of the containers.
			st.Set(keyServicesTouched, true)
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
		// brings the previous release back up from its restore point's
		// compensator, which runs after configuration and secrets have
		// been put back (restorePreviousRuntime).
	}
}

// keyServicesTouched says start-services began replacing containers, so a
// failed update has more to put back than files.
const keyServicesTouched = "services-touched"

// keyConfigNotRestored says the configuration compensator could not put every
// file back, so nothing may be started against what is there.
const keyConfigNotRestored = "config-not-restored"

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
			specs, err := d.checkSpecs(inst, rel, st.OpID, domain.OpTypeApply)
			if err != nil {
				return err
			}

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
//
// The URL is rendered against the parameters, so a probe follows the port the
// deployment actually publishes. Without this a changed port produces a
// deployment that works and an `apply` that fails at "wait for health".
func (d *Deps) checkSpecs(
	inst domain.Installation, rel domain.Release, opID string, opType domain.OperationType,
) ([]ports.CheckSpec, error) {
	env := d.hookEnv(inst, rel, domain.Version{}, opID, opType, ports.PhaseHealthCheck, false)

	params, err := d.parameters(rel, inst)
	if err != nil {
		return nil, err
	}
	checks, err := rel.Manifest.ResolveHealthChecks(params)
	if err != nil {
		return nil, err
	}

	specs := make([]ports.CheckSpec, 0, len(checks))
	for _, check := range checks {
		specs = append(specs, ports.CheckSpec{
			Check:      check,
			WorkingDir: rel.Root,
			Env:        ports.HookEnvVars(env),
		})
	}
	return specs, nil
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
			env := d.hookEnv(inst, rel, opts.PreviousVersion, st.OpID,
				st.OpType, ports.PhaseSmokeTest, st.DryRun)

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
func stepRecordState(d *Deps, inst domain.Installation, rel domain.Release, opts Options) engine.Step {
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
				SourceRef:       opts.SourceRef,
			}
			// apply and rollback pass none, and must not erase what
			// update recorded: a re-converge does not change where a
			// release came from, and rolling back to one that was
			// fetched from a registry leaves it still from there.
			if record.SourceRef == "" {
				record.SourceRef = d.recordedSourceRef(ctx, rel.Root)
			}
			d.adoptStagedCandidate(ctx, &record)
			if err := d.State.SetCurrentRelease(ctx, record); err != nil {
				return err
			}
			// Only after the release is recorded as current. A crash
			// between the two leaves a candidate for a release that
			// is already installed, which the next poll resolves by
			// finding the tag unmoved -- the other order would leave
			// a machine that has forgotten what it staged and has not
			// recorded installing it.
			//
			// Reported rather than returned. What follows it is the
			// symlink swap that makes `current` true for anything
			// reading the filesystem, and state has already been
			// written -- so a failure to forget a candidate would
			// leave the deployment naming one release and pointing
			// at another over a bookkeeping file whose entire cost
			// of loss is one fetch. An advisory write must not
			// outrank the outcome it trails.
			if err := d.retireStagedCandidate(ctx, record); err != nil {
				st.Warn("the staged update candidate could not be cleared: %s",
					domain.AsError(err).Message)
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

// gateUnfinished refuses to start a new operation while a previous one still
// needs a human, or was left journaled as running by a process that died.
//
// It is the one gate that guards intent rather than capacity -- a reboot's
// `apply --startup` proceeding over a state flagged for manual intervention is
// exactly how a recoverable failure becomes an unrecoverable one. It runs
// before the engine journals anything: a refusal that recorded an operation of
// its own would become the newest record of its type and shadow the very wreck
// `--resume` needs to find. A resume is exempt from exactly one record -- the
// one it is continuing -- and no other: a crashed update must still block an
// `apply --resume` that would mutate over it.
func (d *Deps) gateUnfinished(ctx context.Context, exclude string) error {
	unfinished, err := d.State.UnfinishedOperations(ctx)
	if err != nil {
		return err
	}
	others := make([]domain.OperationRecord, 0, len(unfinished))
	for _, rec := range unfinished {
		// Only a non-empty ID excludes: a corrupt record that
		// unmarshalled without one must still block, not silently match
		// the "nothing to exclude" sentinel.
		if exclude != "" && rec.ID == exclude {
			continue
		}
		others = append(others, rec)
	}
	if len(others) == 0 {
		return nil
	}
	res := preflight.NoUnfinishedOperation(others).Run(ctx)
	if res.Status != events.CheckFail {
		return nil
	}
	return domain.Preflight(nil, "%s", res.Message).WithHint("%s", res.Remedy)
}

// excludeID names the record a resume is continuing, or nothing.
func excludeID(prior *domain.OperationRecord) string {
	if prior == nil {
		return ""
	}
	return prior.ID
}

// refreshResumable re-reads the record a resume is about to continue, under
// the deployment lock.
//
// The record was found before the lock was taken; with --wait, the holder we
// queued behind may have been resuming the same operation -- and finishing
// it. Resuming from the stale snapshot would re-run an operation that already
// completed. The refusal when the record changed identity is deliberate: the
// operator asked to continue a specific wreck, and whatever is unfinished now
// is a different decision.
func (d *Deps) refreshResumable(ctx context.Context, opType domain.OperationType, prior *domain.OperationRecord) (*domain.OperationRecord, error) {
	fresh, err := d.findResumable(ctx, opType)
	if err != nil {
		return nil, domain.AsError(err).
			WithHint("the operation may have finished while waiting for the deployment lock; " +
				"run `morzer status` to see where things stand")
	}
	if prior != nil && fresh.ID != prior.ID {
		return nil, domain.Usage(
			"the resumable %s operation changed while waiting for the deployment lock: "+
				"found %s, was resuming %s", opType, fresh.ID, prior.ID).
			WithHint("re-run `morzer %s --resume` to continue the current one", opType)
	}
	return fresh, nil
}

// refuseMovedRelease refuses to run a plan made for a release that is no longer
// current, under the deployment lock.
//
// Every operation that acts on the current release reads it before the lock is
// taken, and plans against it: the version it journals as "from", the
// compatibility gate, the release whose operations and hooks it runs, the
// PREVIOUS_VERSION those hooks are told. With --wait, the holder we queued
// behind may have been an update or a rollback that moved it. Re-planning here
// is not an option -- a plan's checks ran outside the lock, some against a
// fetched bundle -- so the operator re-runs it against what is installed now.
//
// Root and digest, not the whole record: an apply rewrites the record of the
// release it is already on, and that moves nothing.
func (d *Deps) refuseMovedRelease(ctx context.Context, opType domain.OperationType, planned domain.ReleaseRecord) error {
	current, err := d.State.CurrentRelease(ctx)
	if err != nil {
		return err
	}
	if current.Root == planned.Root && current.Digest == planned.Digest {
		return nil
	}
	return domain.Usage(
		"the installed release changed from %s to %s while waiting for the deployment lock",
		describeRelease(planned), describeRelease(current)).
		WithHint("re-run `morzer %s` to plan against %s", opType, describeRelease(current))
}

// describeRelease names a release record for a message, including the absence
// of one, whose version renders as an empty string.
func describeRelease(r domain.ReleaseRecord) string {
	if r.IsZero() {
		return "none"
	}
	return r.Version.String()
}

// findResumable locates the operation --resume should continue.
//
// The newest record of the type decides. A crashed process leaves `running`,
// an operator's Ctrl-C leaves `interrupted`, an aborting step leaves `failed`
// -- each of those is the engine's definition of resumable. Anything that
// finished otherwise supersedes older wreckage: resuming an operation from
// before the last success would re-apply history. UnfinishedOperations is
// deliberately not the source here -- it carries only what needs *attention*
// (running, manual-intervention), and an interrupted operation is exactly the
// one the operator was told `--resume` would continue.
func (d *Deps) findResumable(ctx context.Context, opType domain.OperationType) (*domain.OperationRecord, error) {
	all, err := d.State.Operations(ctx, ports.Filter{})
	if err != nil {
		return nil, err
	}
	for _, rec := range all {
		if rec.Type != opType {
			continue
		}
		switch rec.Status {
		case domain.StatusRunning, domain.StatusInterrupted, domain.StatusFailed:
			return &rec, nil
		}
		break
	}
	return nil, domain.Usage("no interrupted %s operation to resume", opType).
		WithHint("run the command without --resume")
}
