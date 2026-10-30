package ops

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/engine"
	"github.com/morzecrew/morzer/internal/ports"
)

// Tasks are commands a release declares for an operator to run by name (RFC
// 0035): a one-shot container of a release service, run as an operation.
//
// The counterpart of `exec`, and different from it where the vendor's knowledge
// makes a difference. `exec` runs a command the manager knows nothing about, so
// it takes no lock and refuses nothing but a stopped service. A task is the
// vendor saying what a command is for -- including which machines it must
// never touch -- so it runs under the deployment lock, through the entrypoint,
// and is refused by name where the vendor said so.

// TaskOptions names the task and what the operator appended to it.
type TaskOptions struct {
	Options

	Name string

	// Args follow the task's own command. Only a task declaring `args:
	// true` accepts any.
	Args []string

	// Inputs are the files the operator passed, by input name, as typed
	// (RFC 0041). Every input the task declares is required.
	Inputs map[string]string
}

// TaskRun is what a task's container did: the `--json` payload of `task run`,
// and the streams the CLI passes through.
//
// The exit code is the task's own, carried out so a script can branch on it as
// it can on `exec`'s. Output is returned to the caller and never journalled.
type TaskRun struct {
	Task     string `json:"task"`
	Service  string `json:"service"`
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`

	// Truncated says the runtime kept only the start of the output.
	Truncated bool `json:"truncated,omitempty"`
}

// TaskEntry is one row of `task list`.
type TaskEntry struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Service     string   `json:"service"`
	Args        bool     `json:"args"`
	Modes       []string `json:"modes,omitempty"`

	// Allowed says whether this machine's mode is one the task lists, so
	// the refusal `task run` would give is visible before anybody types it.
	Allowed bool `json:"allowed"`

	// Inputs are the files the task needs, each passed with `--input`.
	Inputs []TaskInputEntry `json:"inputs,omitempty"`
}

// TaskInputEntry is one input `task list` describes.
type TaskInputEntry struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	MaxSize     domain.ByteSize `json:"max_size,omitempty"`
}

// keyRuntimeServices is the service list validate-runtime got from the
// runtime, which a task's service is checked against.
const keyRuntimeServices = "runtime-services"

// keyTaskRun is what the run-task step's container did.
const keyTaskRun = "task-run"

// ListTasks describes the tasks the current release declares.
func ListTasks(ctx context.Context, d *Deps) ([]TaskEntry, error) {
	inst, _, rel, err := d.taskRelease(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]TaskEntry, 0, len(rel.Manifest.Tasks))
	for _, name := range rel.Manifest.TaskNames() {
		task := rel.Manifest.Tasks[name]
		out = append(out, TaskEntry{
			Name:        name,
			Description: task.Description,
			Service:     task.Service,
			Args:        task.Args,
			Modes:       task.Modes,
			Allowed:     task.AllowedOn(inst.Mode),
			Inputs:      taskInputEntries(task),
		})
	}
	return out, nil
}

func taskInputEntries(task domain.TaskSpec) []TaskInputEntry {
	var out []TaskInputEntry
	for _, name := range task.InputNames() {
		in := task.Inputs[name]
		out = append(out, TaskInputEntry{Name: name, Description: in.Description, MaxSize: in.MaxSize})
	}
	return out
}

// RunTask runs a declared task as the operation `task`.
//
// Every refusal of the request comes before the lock, so a task that cannot run
// on this machine takes nothing and journals nothing: a refused request is not
// something that happened to the deployment. The one refusal of the release --
// a service its runtime configuration does not define -- needs that
// configuration, so it is the run step's and ends a journalled operation.
func RunTask(ctx context.Context, d *Deps, opts TaskOptions) (Result, error) {
	// A task's command is the vendor's, and running it again after an
	// interruption is not the same as running it once -- a seed seeds
	// twice. Whether that is safe is the vendor's to say, so the manager
	// offers no road it cannot vouch for.
	if opts.Resume {
		return Result{}, domain.Usage("--resume cannot continue a task").
			WithHint("run `morzer task run %s` again if running it twice is safe; "+
				"the task's description or its vendor says whether it is", opts.Name)
	}

	inst, current, rel, err := d.taskRelease(ctx)
	if err != nil {
		return Result{}, err
	}
	// A one-off starts the services it depends on and leaves them running
	// (RFC 0035 decision 6), which would bring part of a stopped product
	// back up (RFC 0040 decision 5).
	if err := refuseWhileStopped(inst, "`morzer task run`"); err != nil {
		return Result{}, err
	}

	task, ok := rel.Manifest.Tasks[opts.Name]
	if !ok {
		names := rel.Manifest.TaskNames()
		if len(names) == 0 {
			return Result{}, domain.Usage("%s %s declares no tasks", rel.Name(), rel.Version())
		}
		return Result{}, domain.Usage("%s %s declares no task named %q", rel.Name(), rel.Version(), opts.Name).
			WithHint("it declares %s; `morzer task list` describes them", strings.Join(names, ", "))
	}

	// Refused rather than dropped: an operator who typed arguments meant
	// them, and running the task without them would do something they did
	// not ask for.
	if len(opts.Args) > 0 && !task.Args {
		return Result{}, domain.Usage("task %s takes no arguments", opts.Name).
			WithHint("the release does not declare `args: true` for it; run `morzer task run %s` alone",
				opts.Name)
	}

	// The refusal the vendor asked for, and not one --force overrides: a
	// demo-data seed on production is the accident `modes` exists to stop,
	// and the flag confirms destructive actions, not incorrect ones.
	if !task.AllowedOn(inst.Mode) {
		return Result{}, domain.Preflight(nil, "task %s may run only on %s machines, and this one is %s",
			opts.Name, strings.Join(task.Modes, " and "), inst.Mode.Describe()).
			WithHint("the release restricts it with `modes`; a machine's mode is fixed when it is created")
	}

	// Opened and checked before the lock like every other refusal of the
	// request, and the space they need with them: a task that cannot be
	// given its files takes nothing and journals nothing.
	inputs, err := openTaskInputs(opts.Name, task, opts.Inputs)
	if err != nil {
		return Result{}, err
	}
	defer closeInputs(inputs)
	var budget int64
	if len(inputs) > 0 {
		if budget, err = d.inputBudget(inputs); err != nil {
			return Result{}, err
		}
	}

	if err := d.gateUnfinished(ctx, ""); err != nil {
		return Result{}, err
	}

	// Armed only when there are arguments to scrub, because arming it
	// decrypts the secret state, and a task with none has nothing of the
	// operator's to write down. exec's reasoning for the rest (execRecord).
	armed := len(opts.Args) > 0 && d.armRedaction(ctx)

	opID := d.newOpID()
	op := engine.Operation{
		ID:          opID,
		Type:        domain.OpTypeTask,
		Description: "run task " + opts.Name,
		To:          rel.Version(),
		Steps:       taskSteps(d, inst, rel, opts, task, armed, inputs, budget),
		Flags:       d.taskFlags(opts, task, armed),
	}

	var result engine.Result
	runErr := d.withLock(ctx, opID, domain.OpTypeTask, opts.Options, func(ctx context.Context) error {
		// Again under the lock, as apply asks: with --wait, the holder
		// we queued behind may have left a wreck a task must not run on.
		if err := d.gateUnfinished(ctx, ""); err != nil {
			return err
		}
		if err := d.refuseIfStoppedNow(ctx, "`morzer task run`"); err != nil {
			return err
		}
		if err := d.refuseMovedRelease(ctx, current); err != nil {
			return err
		}
		var err error
		result, err = d.Engine.Run(ctx, op, d.engineOptions(opts.Options, inst.ID, nil))
		return err
	})

	// The copies go when the operation ends, however it ended. A failure
	// here leaves them for the next task's sweep, and doctor names them.
	if len(inputs) > 0 && !opts.DryRun {
		if err := removeInputs(filepath.Join(d.Paths.InputsDir(), opID)); err != nil {
			d.warnf("%s", domain.AsError(err).Message)
		}
	}

	d.notifyFinished(ctx, opID, domain.OpTypeTask, result.Record, runErr)
	if !opts.DryRun {
		// A record of a task that failed as well as one that succeeded,
		// for the reason Apply gives.
		in := attestationInputs(inst, rel, current, domain.Version{}, renderedConfigFor(result.State))
		if result.State != nil {
			if staged, err := engine.GetTyped[stagedInputs](result.State, keyStagedInputs); err == nil {
				in.Inputs = staged.Inputs
			}
		}
		emitAttestation(ctx, d, result.Record, in)
	}

	out := Result{Record: result.Record}
	if opts.DryRun {
		out.Summary = fmt.Sprintf("would run task %s in %s", opts.Name, task.Service)
	}
	if result.State != nil {
		if run, err := engine.GetTyped[TaskRun](result.State, keyTaskRun); err == nil {
			out.Data = run
		}
	}
	return out, runErr
}

// taskRelease loads what every task command needs: the installation and the
// release it is running.
func (d *Deps) taskRelease(ctx context.Context) (domain.Installation, domain.ReleaseRecord, domain.Release, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return domain.Installation{}, domain.ReleaseRecord{}, domain.Release{}, err
	}
	current, err := d.State.CurrentRelease(ctx)
	if err != nil {
		return domain.Installation{}, domain.ReleaseRecord{}, domain.Release{}, err
	}
	if current.IsZero() {
		return domain.Installation{}, domain.ReleaseRecord{}, domain.Release{},
			domain.InstallationError(domain.ErrReleaseNotFound,
				"no release is installed, so there are no tasks to run").
				WithHint("install one with `morzer update <bundle>`")
	}
	rel, err := d.resolveCurrentRelease(ctx, current)
	if err != nil {
		return domain.Installation{}, domain.ReleaseRecord{}, domain.Release{}, err
	}
	return inst, current, rel, nil
}

// taskFlags is what the journal keeps of a task: which one, where, and what the
// operator added -- scrubbed of this installation's secret values, or left out
// with the reason when those could not be loaded. Never the output.
func (d *Deps) taskFlags(opts TaskOptions, task domain.TaskSpec, armed bool) map[string]string {
	flags := map[string]string{"task": opts.Name, "service": task.Service}
	if len(opts.Args) == 0 {
		return flags
	}
	if armed {
		flags["args"] = d.redactArgv(opts.Args)
	} else {
		flags["args_omitted"] = "this installation's secret values could not be " +
			"loaded, so the arguments could not be scrubbed before writing them down"
	}
	return flags
}

// taskSteps renders what a converge renders, then runs the task.
//
// apply's own steps, so a task after a reboot -- secrets and generated
// configuration live on tmpfs -- finds the files the service's Compose file
// mounts, and a task on an unchanged machine rewrites nothing. No pull and no
// migration: the release was applied, and a task is not a converge.
func taskSteps(
	d *Deps, inst domain.Installation, rel domain.Release, opts TaskOptions, task domain.TaskSpec, armed bool,
	inputs []openInput, budget int64,
) []engine.Step {
	var steps []engine.Step
	if len(inputs) > 0 {
		steps = append(steps, stepStageInputs(d, inputs, budget))
	}
	return append(steps,
		stepLoadSecrets(d, inst, rel),
		stepRenderSecrets(d, inst, rel, opts.Options),
		stepRenderConfiguration(d, inst, rel, opts.Options),
		stepValidateRuntime(d, inst, rel, opts.Options),
		stepRunTask(d, inst, rel, opts, task, armed),
	)
}

// stepRunTask runs the task's container to completion.
func stepRunTask(
	d *Deps, inst domain.Installation, rel domain.Release, opts TaskOptions, task domain.TaskSpec, armed bool,
) engine.Step {
	// The release's own command with its input placeholders filled; the
	// operator's arguments follow and are never templated.
	argv := append(task.CommandWithInputs(), opts.Args...)
	timeout := task.Timeout.Or(domain.DefaultOperationTimeout)

	return engine.Step{
		ID:          "run-task",
		Description: fmt.Sprintf("run task %s in %s", opts.Name, task.Service),
		// Running a task twice is not running it once.
		Idempotent: false,
		// Nothing to compensate and nobody to flag. The task's effects
		// are the vendor's and the manager cannot undo them, and
		// flagging every failed report for a human would train operators
		// to clear the flag without looking (RFC 0035 decision 9).
		OnFailure: engine.Abort,
		Timeout:   timeout,
		// The operator's arguments by the journal's rule (decision 13):
		// scrubbed, or left out when nothing could scrub them. The
		// release's own command is the vendor's and holds no operator
		// value.
		PlanDetail: func(ctx context.Context, st *engine.State) (string, string) {
			if armed || len(opts.Args) == 0 {
				return d.redactArgv(argv), ""
			}
			return fmt.Sprintf("%s, and %d argument(s) not shown: this installation's "+
				"secret values could not be loaded to scrub them",
				d.redactArgv(task.CommandWithInputs()), len(opts.Args)), ""
		},
		Execute: func(ctx context.Context, st *engine.State) error {
			cfg, err := engine.GetTyped[ports.RuntimeConfig](st, engine.KeyRuntimeConfig)
			if err != nil {
				return err
			}
			if err := d.confirmTaskService(ctx, st, cfg, opts.Name, task.Service); err != nil {
				return err
			}

			env := ports.ServiceEnvVars(d.hookEnv(inst, rel, domain.Version{}, st.OpID, st.OpType, ports.PhaseTask, st.DryRun))
			var mounts []ports.Mount
			if staged, err := engine.GetTyped[stagedInputs](st, keyStagedInputs); err == nil {
				var inputEnv map[string]string
				mounts, inputEnv = inputMounts(staged)
				maps.Copy(env, inputEnv)
			}
			res, err := d.Runtime.RunOneShot(ctx, cfg, task.Service, ports.RunOptions{
				Argv:    argv,
				Env:     env,
				Timeout: timeout,
				Remove:  true,
				Mounts:  mounts,
			})
			if err != nil {
				return err
			}
			st.Set(keyTaskRun, TaskRun{
				Task: opts.Name, Service: task.Service, ExitCode: res.ExitCode,
				Stdout: res.Stdout, Stderr: res.Stderr, Truncated: res.Truncated,
			})

			// Any non-zero exit, 2 included: "nothing to do" is the
			// migration ABI's, and nothing reads it from a task.
			//
			// No output quoted, unlike a migration service's error. That
			// error is the only place a migration's output survives; a
			// task's reaches the operator whole, and the error is
			// journalled, which would make a quote the task's output in
			// the journal (RFC 0035 decision 20).
			if !res.OK() {
				return domain.RuntimeError(nil, "task %s, the service %q, exited with code %d",
					opts.Name, task.Service, res.ExitCode).
					WithHint("what the task printed says why: it follows the step list, and is under `data` with --json")
			}
			return nil
		},
	}
}

// confirmTaskService refuses a task whose service the runtime configuration
// does not define, naming those it does.
//
// Asked here rather than left to the runtime, which reports a missing service
// through the exit code of the one-shot -- and that would pass for the task's
// own. The validated list leaves out a service behind a profile that is not
// active, which is where a one-off tool usually lives and which the one-shot
// starts when named, so a service missing from it is asked about by name before
// it is refused. A runtime that cannot be asked gets no check.
func (d *Deps) confirmTaskService(ctx context.Context, st *engine.State, cfg ports.RuntimeConfig, name, service string) error {
	services, _ := engine.GetTyped[[]string](st, keyRuntimeServices)
	if slices.Contains(services, service) {
		return nil
	}
	imager, ok := d.Runtime.(ports.ServiceImager)
	if !ok {
		return nil
	}
	images, err := imager.ServiceImages(ctx, cfg, service)
	if _, defined := images[service]; err == nil && defined {
		return nil
	}
	hint := "the release's runtime configuration defines no services"
	if len(services) > 0 {
		hint = "it defines " + strings.Join(services, ", ")
	}
	return domain.ValidationError(err,
		"task %s runs the service %q, which the release's runtime configuration does not define",
		name, service).WithHint("%s; the release's manifest names a service its runtime files do not", hint)
}
