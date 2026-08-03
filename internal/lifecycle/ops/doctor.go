package ops

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/tools"
	"github.com/morzecrew/morzer/internal/lifecycle/preflight"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

// DoctorReport is the result of a diagnostic run. `doctor --json` is a
// supported monitoring contract, so this shape is stable.
type DoctorReport struct {
	Results []events.CheckResult `json:"results"`
	Worst   events.CheckStatus   `json:"worst"`

	Summary struct {
		OK   int `json:"ok"`
		Warn int `json:"warn"`
		Fail int `json:"fail"`
	} `json:"summary"`
}

// ExitCode maps the worst result onto the process exit status.
//
// A warning exits zero: warnings are advisory, and a monitoring system that
// paged on every "the release was tested on a different Ubuntu" would be
// turned off within a week.
func (r DoctorReport) ExitCode() int {
	if r.Worst == events.CheckFail {
		return domain.ExitPreflight
	}
	return domain.ExitSuccess
}

// Doctor runs read-only diagnostics.
//
// Nothing here mutates and nothing takes the lock: doctor must work while an
// operation is running, and must work when the installation is too broken for
// any other command to load it. Every check that cannot run reports itself as
// a failed check rather than aborting the run.
func Doctor(ctx context.Context, d *Deps) (DoctorReport, error) {
	// Deliberately not wired to the event bus. For `apply` the checks are
	// progress and streaming them is useful; for `doctor` they *are* the
	// result, and the command renders them grouped by category. Publishing
	// as well would print every check twice.
	runner := preflight.NewRunner(nil)
	checks := d.doctorChecks(ctx)

	report := runner.Run(ctx, checks)

	out := DoctorReport{Results: report.Results, Worst: report.Worst}
	for _, res := range report.Results {
		switch res.Status {
		case events.CheckOK:
			out.Summary.OK++
		case events.CheckWarn:
			out.Summary.Warn++
		case events.CheckFail:
			out.Summary.Fail++
		}
	}
	return out, nil
}

// doctorChecks assembles the check list, adapting to how much of the
// installation is readable.
func (d *Deps) doctorChecks(ctx context.Context) []preflight.Check {
	checks := []preflight.Check{
		d.checkInstallationReadable(),
	}

	inst, instErr := d.loadInstallation(ctx)
	if instErr != nil {
		// Without an installation there is nothing else worth checking:
		// every remaining question is about a deployment that does not
		// exist yet. Tool availability is still useful, though, because
		// it is what `init` will need next.
		return append(checks,
			preflight.Tool(d.Tools, tools.Docker, domain.Constraint{}),
			preflight.Tool(d.Tools, tools.SOPS, domain.Constraint{}),
		)
	}

	checks = append(checks,
		d.checkUnfinishedOperations(),
		preflight.Directories(d.Paths),
		d.checkIdentity(),
		d.checkSecretsDecryptable(),
		d.checkRecoveryRecipient(),
		d.checkSecretsOnEphemeralStorage(),
		d.checkDiskSpace(),
	)

	current, _ := d.State.CurrentRelease(ctx)
	if current.IsZero() {
		checks = append(checks, d.checkNoReleaseInstalled())
	} else if rel, err := d.resolveCurrentRelease(ctx, current); err != nil {
		checks = append(checks, d.checkReleaseBroken(current, err))
	} else {
		checks = append(checks, preflight.Architecture(rel.Manifest.Requirements))
		checks = append(checks, preflight.Tools(d.Tools, rel.Manifest.Requirements)...)
		checks = append(checks,
			d.checkRequiredSecrets(rel),
			d.checkSecretRotation(rel),
			d.checkRegistryReachable(rel),
			d.checkImagesLocal(rel),
			d.checkServices(inst, rel),
			d.checkHealth(inst, rel),
		)
	}

	if d.Supervisor != nil {
		checks = append(checks, d.checkUnits(inst))
	}
	if d.Backup != nil {
		checks = append(checks, d.checkLastBackup(inst))
	}

	return checks
}

func (d *Deps) checkInstallationReadable() preflight.Check {
	return preflight.Check{
		ID:          "config.installation",
		Category:    preflight.CategoryConfig,
		Description: "installation configuration is valid",
		Fatal:       true,
		Run: func(ctx context.Context) events.CheckResult {
			exists, err := d.State.InstallationExists(ctx)
			if err != nil {
				return preflight.Fail("check the permissions on "+d.Paths.ManagerDir(),
					"cannot read the installation state: %v", domain.AsError(err).Message)
			}
			if !exists {
				return preflight.Fail("run `morzer init` to create an installation",
					"no installation at %s", d.Paths.EtcDir)
			}
			inst, err := d.State.LoadInstallation(ctx)
			if err != nil {
				e := domain.AsError(err)
				return preflight.Fail(e.Hint, "%s", e.Message)
			}
			return preflight.OK("%s (%s)", inst.Product, inst.ID)
		},
	}
}

func (d *Deps) checkUnfinishedOperations() preflight.Check {
	return preflight.Check{
		ID:          "config.operations",
		Category:    preflight.CategoryConfig,
		Description: "no operation is unfinished or awaiting intervention",
		Fatal:       true,
		Run: func(ctx context.Context) events.CheckResult {
			unfinished, err := d.State.UnfinishedOperations(ctx)
			if err != nil {
				return preflight.Warn("", "cannot read the operation journal: %v",
					domain.AsError(err).Message)
			}
			if len(unfinished) == 0 {
				return preflight.OK("no unfinished operations")
			}

			var needsAttention, incomplete []string
			for _, rec := range unfinished {
				label := fmt.Sprintf("%s (%s)", rec.ID, rec.Type)
				if rec.Status.NeedsAttention() {
					needsAttention = append(needsAttention, label)
				} else {
					incomplete = append(incomplete, label)
				}
			}

			if len(needsAttention) > 0 {
				return preflight.Fail(
					"investigate, repair the system, then run "+
						"`morzer status --clear-intervention` to acknowledge",
					"operation(s) require manual intervention: %s",
					strings.Join(needsAttention, ", "))
			}
			return preflight.Fail(
				"resume with `morzer <command> --resume`, or start a fresh operation",
				"operation(s) did not finish: %s", strings.Join(incomplete, ", "))
		},
	}
}

func (d *Deps) checkIdentity() preflight.Check {
	return preflight.Check{
		ID:          "secrets.identity",
		Category:    preflight.CategorySecrets,
		Description: "machine age identity is present and protected",
		Fatal:       true,
		Run: func(ctx context.Context) events.CheckResult {
			path := d.Paths.AgeIdentityFile()

			info, err := os.Stat(path)
			if err != nil {
				return preflight.Fail(
					"restore it from your backup; without it the secret state cannot be read",
					"the age identity at %s is missing", path)
			}
			if mode := info.Mode().Perm(); mode != 0o400 {
				return preflight.Warn(
					fmt.Sprintf("run `chmod 400 %s`", path),
					"the age identity is mode %04o, expected 0400", mode)
			}

			pub, err := d.Secrets.IdentityPublicKey(ctx)
			if err != nil {
				e := domain.AsError(err)
				return preflight.Fail(e.Hint, "%s", e.Message)
			}
			return preflight.OK("%s", pub)
		},
	}
}

func (d *Deps) checkSecretsDecryptable() preflight.Check {
	return preflight.Check{
		ID:          "secrets.decryptable",
		Category:    preflight.CategorySecrets,
		Description: "secret state can be decrypted",
		Fatal:       true,
		Run: func(ctx context.Context) events.CheckResult {
			initialised, err := d.Secrets.Initialized(ctx)
			if err != nil {
				return preflight.Fail("", "%s", domain.AsError(err).Message)
			}
			if !initialised {
				return preflight.Warn(
					"secrets are created by `morzer init` or `morzer secret set`",
					"no secret state exists yet")
			}

			set, err := d.Secrets.Load(ctx)
			if err != nil {
				e := domain.AsError(err)
				return preflight.Fail(e.Hint, "%s", e.Message)
			}
			return preflight.OK("%d secret(s) decrypted", set.Len())
		},
	}
}

// checkRecoveryRecipient is the check that matters most on a machine that has
// not failed yet.
//
// Without an offline recipient, losing the VM means losing every secret in it.
// That is discoverable only in advance, which is exactly why doctor asks.
func (d *Deps) checkRecoveryRecipient() preflight.Check {
	return preflight.Check{
		ID:          "secrets.recovery-recipient",
		Category:    preflight.CategorySecrets,
		Description: "an offline recovery recipient is configured",
		Fatal:       false,
		Run: func(ctx context.Context) events.CheckResult {
			recipients, err := d.Secrets.Recipients(ctx)
			if err != nil {
				return preflight.Warn("", "cannot read recipients: %v", domain.AsError(err).Message)
			}
			if len(recipients) == 0 {
				return preflight.Warn(
					"secrets have not been initialised yet",
					"no recipients are configured")
			}

			var recovery, machine int
			for _, r := range recipients {
				switch r.Kind {
				case ports.RecipientRecovery:
					recovery++
				case ports.RecipientMachine:
					machine++
				}
			}

			if machine == 0 {
				return preflight.Fail(
					"this machine cannot decrypt its own secret state; "+
						"re-encrypt using the offline recovery key",
					"this machine's identity is not among the %d recipient(s)", len(recipients))
			}
			if recovery == 0 {
				return preflight.Warn(
					"add one with `morzer secret recipients add <age1...> --kind recovery`, "+
						"and keep the private key off this machine",
					"no offline recovery recipient: if this VM is lost, its secrets are lost")
			}
			return preflight.OK("%d recipient(s), %d for recovery", len(recipients), recovery)
		},
	}
}

// checkSecretsOnEphemeralStorage reports a render directory that is not tmpfs.
//
// Decrypted secrets are written there on every apply, and `secret edit` puts a
// whole plaintext session there. The design assumes those bytes are pages of
// RAM: that is what makes overwriting them meaningful and what makes a reboot
// a guaranteed cleanup. On a disk-backed filesystem neither holds -- the old
// contents can survive in a journal or an unreferenced extent that nothing will
// hand back and nothing has erased.
//
// A warning rather than a failure. A container image with no tmpfs mounted, or
// a `--root` used for testing, is a legitimate way to run this, and refusing to
// operate would help nobody. What an operator needs is to know.
func (d *Deps) checkSecretsOnEphemeralStorage() preflight.Check {
	return preflight.Check{
		ID:          "secrets.ephemeral-storage",
		Category:    preflight.CategorySecrets,
		Description: "decrypted secrets live on memory-backed storage",
		Fatal:       false,
		Run: func(ctx context.Context) events.CheckResult {
			dir := d.Paths.SecretsRenderDir()

			fstype := preflight.FilesystemType(dir)
			if fstype == "" {
				// "Cannot tell" is not "insecure". Reporting a
				// container that mounts /proc elsewhere as a
				// finding would be crying wolf.
				return preflight.OK("cannot determine the filesystem under %s", dir)
			}
			if preflight.IsEphemeralFilesystem(fstype) {
				return preflight.OK("%s is %s", dir, fstype)
			}

			return preflight.Warn(
				fmt.Sprintf("mount a tmpfs at %s, or accept that decrypted secrets "+
					"touch disk and are not reliably erasable there", d.Paths.RunDir),
				"%s is %s, not tmpfs: decrypted secrets are written to disk", dir, fstype)
		},
	}
}

// checkSecretRotation reports secrets older than the period the release
// declares for them.
//
// The period is the release author's recommendation, so this is a warning and
// never a failure: `doctor`'s exit code is something a monitoring system pages
// on, and paging over a recommendation is how a team learns to ignore it.
//
// Secrets with no declared period are not mentioned at all. A tool that
// invented a default rotation policy for a vendor who declined to state one
// would be inventing the vendor's opinion.
func (d *Deps) checkSecretRotation(rel domain.Release) preflight.Check {
	return preflight.Check{
		ID:          "secrets.rotation",
		Category:    preflight.CategorySecrets,
		Description: "secrets are within their declared rotation period",
		Fatal:       false,
		Run: func(ctx context.Context) events.CheckResult {
			schema, err := release.LoadSecretSchema(rel)
			if err != nil {
				return preflight.OK("the release declares no secret schema")
			}

			metadata, err := d.Secrets.Metadata(ctx)
			if err != nil {
				return preflight.Warn("", "cannot read secret metadata: %s",
					domain.AsError(err).Message)
			}
			changed := make(map[string]domain.Time, len(metadata))
			for _, m := range metadata {
				changed[m.Name] = m.LastChanged
			}

			now := d.now()
			var overdue, unknown []string
			var rotatable int

			for _, decl := range schema.Secrets {
				if decl.RotationPeriod <= 0 {
					continue
				}
				rotatable++

				last, ok := changed[decl.Name]
				if !ok {
					continue // not set; checkRequiredSecrets covers that
				}
				if last.IsZero() {
					// Present but never stamped -- written by
					// a manager that predates the metadata, or
					// restored from one.
					unknown = append(unknown, decl.Name)
					continue
				}

				age := now.Sub(last.Time)
				if age > time.Duration(decl.RotationPeriod) {
					overdue = append(overdue, fmt.Sprintf("%s is %s old (policy %s)",
						decl.Name, roundDays(age), roundDays(time.Duration(decl.RotationPeriod))))
				}
			}

			switch {
			case rotatable == 0:
				return preflight.OK("no secret declares a rotation period")
			case len(overdue) > 0:
				return preflight.Warn(rotationRemedy(schema, overdue),
					"%s", strings.Join(overdue, "; "))
			case len(unknown) > 0:
				return preflight.Warn(
					"rotate them to establish a date, or ignore this if they are known to be recent",
					"%d secret(s) have no recorded change date: %s",
					len(unknown), strings.Join(unknown, ", "))
			default:
				return preflight.OK("%d secret(s) within their period", rotatable)
			}
		},
	}
}

// rotationRemedy names the command that can actually fix it.
//
// A secret the release declares no generator for cannot be rotated by the
// manager: there is nothing to generate. Telling an operator to run `rotate`
// there would be telling them to run something that fails.
func rotationRemedy(schema domain.SecretSchema, overdue []string) string {
	name := strings.SplitN(overdue[0], " ", 2)[0]
	if decl, ok := schema.Declaration(name); ok && !decl.Generator.Auto() {
		return fmt.Sprintf("this release declares no generator for %s, "+
			"so supply a new value with `morzer secret set %s`", name, name)
	}
	return fmt.Sprintf("rotate with `morzer secret rotate %s`", name)
}

// roundDays renders an age the way an operator thinks about one.
func roundDays(d time.Duration) string {
	days := int(d.Hours() / 24)
	if days < 1 {
		return d.Round(time.Hour).String()
	}
	return fmt.Sprintf("%dd", days)
}

func (d *Deps) checkDiskSpace() preflight.Check {
	return preflight.Check{
		ID:          "storage.free-space",
		Category:    preflight.CategoryStorage,
		Description: "sufficient free disk space",
		Fatal:       false,
		Run: func(ctx context.Context) events.CheckResult {
			free, err := preflight.FreeSpace(d.Paths.VarDir)
			if err != nil {
				return preflight.Warn("", "cannot determine free space: %v", err)
			}

			// Thresholds the manager sets for itself, independent of
			// what a release asks for: below a couple of gigabytes,
			// a backup or an image pull will fail, and it is better
			// to say so before it does.
			const (
				critical = 2 * domain.GiB
				low      = 10 * domain.GiB
			)
			switch {
			case free < int64(critical):
				return preflight.Fail(
					"free space before the next update or backup; both will fail below this",
					"only %s free on %s", domain.ByteSize(free), d.Paths.VarDir)
			case free < int64(low):
				return preflight.Warn(
					"consider pruning old releases (`morzer release prune`) or backups",
					"%s free on %s", domain.ByteSize(free), d.Paths.VarDir)
			default:
				return preflight.OK("%s free on %s", domain.ByteSize(free), d.Paths.VarDir)
			}
		},
	}
}

func (d *Deps) checkNoReleaseInstalled() preflight.Check {
	return preflight.Check{
		ID:          "runtime.release",
		Category:    preflight.CategoryRuntime,
		Description: "a release is installed",
		Fatal:       false,
		Run: func(context.Context) events.CheckResult {
			return preflight.Warn(
				"install one with `morzer update <bundle>`",
				"no release is installed yet")
		},
	}
}

func (d *Deps) checkReleaseBroken(record domain.ReleaseRecord, cause error) preflight.Check {
	return preflight.Check{
		ID:          "runtime.release",
		Category:    preflight.CategoryRuntime,
		Description: "the installed release is readable",
		Fatal:       true,
		Run: func(context.Context) events.CheckResult {
			e := domain.AsError(cause)
			return preflight.Fail(e.Hint, "release %s at %s: %s",
				record.Version, record.Root, e.Message)
		},
	}
}

func (d *Deps) checkRequiredSecrets(rel domain.Release) preflight.Check {
	return preflight.Check{
		ID:          "secrets.required",
		Category:    preflight.CategorySecrets,
		Description: "all required secrets are set",
		Fatal:       true,
		Run: func(ctx context.Context) events.CheckResult {
			schema, err := release.LoadSecretSchema(rel)
			if err != nil {
				return preflight.Fail("", "%s", domain.AsError(err).Message)
			}
			if len(schema.Secrets) == 0 {
				return preflight.OK("the release declares no secrets")
			}

			set, err := d.Secrets.Load(ctx)
			if err != nil {
				return preflight.Fail("", "cannot read secrets: %s", domain.AsError(err).Message)
			}
			return preflight.SecretsPresent(schema, set).Run(ctx)
		},
	}
}

// checkRegistryReachable verifies the registry is reachable without pulling.
func (d *Deps) checkRegistryReachable(rel domain.Release) preflight.Check {
	return preflight.Check{
		ID:          "network.registry",
		Category:    preflight.CategoryNetwork,
		Description: "container registry is reachable",
		Fatal:       false,
		Run: func(ctx context.Context) events.CheckResult {
			refs := rel.Manifest.ImageRefs()
			if len(refs) == 0 {
				return preflight.OK("the release declares no images")
			}

			probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			// A manifest inspect touches the registry without
			// transferring layers, so this stays cheap enough to run
			// on every doctor invocation.
			prober, ok := d.Runtime.(ports.RegistryProber)
			if !ok {
				return preflight.OK("the configured runtime cannot probe registries")
			}

			if err := prober.ProbeRegistry(probeCtx, refs[0]); err != nil {
				return preflight.Warn(
					"check network access and registry credentials (`docker login`); "+
						"updates will fail while this is unreachable",
					"cannot reach the registry for %s: %s",
					shortRef(refs[0]), domain.AsError(err).Message)
			}
			return preflight.OK("reachable")
		},
	}
}

// checkImagesLocal reports which of the release's images are already on this
// machine.
//
// The question it answers is "would this deployment come up with no network",
// and the moment to ask is while there still is one. `apply --startup` skips
// pulls when images are present, so an installation whose images are all local
// survives a reboot in a datacentre that has lost its uplink -- and one whose
// images are not will sit there failing to pull, at the worst possible time.
//
// A warning rather than a failure: needing the network is the normal case, and
// failing `doctor` over it would make the exit code mean "this machine is
// online" instead of "this machine is healthy".
func (d *Deps) checkImagesLocal(rel domain.Release) preflight.Check {
	return preflight.Check{
		ID:          "runtime.images-local",
		Category:    preflight.CategoryRuntime,
		Description: "release images are available offline",
		Fatal:       false,
		Run: func(ctx context.Context) events.CheckResult {
			refs := rel.Manifest.ImageRefs()
			if len(refs) == 0 {
				return preflight.OK("the release declares no images")
			}

			inspector, ok := d.Runtime.(ports.ImageInspector)
			if !ok {
				return preflight.OK("the configured runtime has no local image store")
			}

			checkCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()

			var missing []string
			for _, ref := range refs {
				present, err := inspector.HasImage(checkCtx, ref)
				if err != nil {
					// "Cannot tell" is not "absent". Reporting
					// a stopped daemon as missing images would
					// send an operator to fix the wrong thing.
					return preflight.Warn(
						"check that the container runtime is running",
						"cannot check local images: %s", domain.AsError(err).Message)
				}
				if !present {
					missing = append(missing, shortRef(ref))
				}
			}

			if len(missing) == 0 {
				return preflight.OK("all %d image(s) are present locally", len(refs))
			}
			return preflight.Warn(
				"run `morzer apply` while online, or preload with "+
					"`docker load < images.tar`, if this machine has to come up "+
					"without network access",
				"%d of %d image(s) are not local: %s",
				len(missing), len(refs), strings.Join(missing, ", "))
		},
	}
}

func shortRef(ref string) string {
	if i := strings.Index(ref, "@"); i > 0 {
		return ref[:i]
	}
	return ref
}

func (d *Deps) checkServices(inst domain.Installation, rel domain.Release) preflight.Check {
	return preflight.Check{
		ID:          "runtime.services",
		Category:    preflight.CategoryRuntime,
		Description: "all services are running",
		Fatal:       false,
		Run: func(ctx context.Context) events.CheckResult {
			cfg, err := d.runtimeConfig(rel, inst, "")
			if err != nil {
				return preflight.Fail("", "%s", domain.AsError(err).Message)
			}

			services, err := d.Runtime.Status(ctx, cfg)
			if err != nil {
				// The adapter's message already names the failure;
				// prefixing it produced "cannot read service
				// status: cannot read service status".
				return preflight.Fail(
					"check that the Docker daemon is running: `docker info`",
					"%s", domain.AsError(err).Message)
			}
			if len(services) == 0 {
				return preflight.Warn(
					"run `morzer apply` to start the product",
					"no containers exist for project %q", cfg.Project)
			}

			var down []string
			for _, s := range services {
				if s.State == "exited" && s.ExitCode == 0 {
					continue // a completed one-shot job
				}
				if !s.Running() {
					down = append(down, fmt.Sprintf("%s (%s)", s.Name, s.State))
				}
			}
			if len(down) > 0 {
				return preflight.Fail(
					"inspect with `docker compose logs`, then `morzer apply` to reconverge",
					"service(s) not running: %s", strings.Join(down, ", "))
			}
			return preflight.OK("%d service(s) running", len(services))
		},
	}
}

func (d *Deps) checkHealth(inst domain.Installation, rel domain.Release) preflight.Check {
	return preflight.Check{
		ID:          "runtime.health",
		Category:    preflight.CategoryRuntime,
		Description: "health endpoints respond",
		Fatal:       false,
		Run: func(ctx context.Context) events.CheckResult {
			if len(rel.Manifest.Health.Checks) == 0 {
				return preflight.OK("the release declares no health checks")
			}

			// Probing a project that is not running would report a
			// wall of connection refusals, and a remedy ("check its
			// logs") that contradicts the service check directly
			// above. The service check already said what is wrong.
			cfg, cfgErr := d.runtimeConfig(rel, inst, "")
			if cfgErr == nil {
				services, statusErr := d.Runtime.Status(ctx, cfg)
				switch {
				case statusErr != nil:
					// The service check above already reported
					// why the runtime is unreachable. Probing
					// anyway would add a wall of connection
					// refusals that explain nothing.
					return preflight.Warn(
						"the runtime is unreachable; see the service check above",
						"not probed: cannot determine what is running")
				case !anyRunning(services):
					return preflight.Warn(
						"run `morzer apply` to start the product",
						"not probed: no services are running")
				}
			}

			probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			results, err := d.Health.CheckOnce(probeCtx, d.checkSpecs(inst, rel, "", domain.OpTypeApply))
			if err != nil {
				return preflight.Warn("", "%s", domain.AsError(err).Message)
			}

			var failed []string
			for _, r := range results {
				if !r.OK {
					failed = append(failed, fmt.Sprintf("%s (%s)", r.Name, r.Message))
				}
			}
			if len(failed) > 0 {
				return preflight.Fail(
					"the containers are up but the application is not ready; check its logs",
					"health check(s) failing: %s", strings.Join(failed, ", "))
			}
			return preflight.OK("%d check(s) passing", len(results))
		},
	}
}

func (d *Deps) checkUnits(inst domain.Installation) preflight.Check {
	return preflight.Check{
		ID:          "system.units",
		Category:    preflight.CategorySystem,
		Description: "systemd units are installed and enabled",
		Fatal:       false,
		Run: func(ctx context.Context) events.CheckResult {
			if !d.Supervisor.Available(ctx) {
				return preflight.OK("systemd is not in use on this host")
			}

			var problems []string
			for _, name := range d.Supervisor.ManagedUnitNames(inst.Product) {
				state, err := d.Supervisor.Status(ctx, name)
				if err != nil {
					problems = append(problems, name+": cannot query")
					continue
				}
				if !state.Loaded {
					problems = append(problems, name+": not installed")
					continue
				}
				if state.Failed() {
					// Exit 12 through systemd is the
					// requires-manual-intervention path, and
					// naming it here saves an operator from
					// decoding the number.
					detail := name + ": failed"
					if state.ExitCode == domain.ExitManualIntervention {
						detail += " (exit 12: manual intervention required)"
					}
					problems = append(problems, detail)
				}
			}

			if len(problems) > 0 {
				return preflight.Warn(
					"run `morzer init --repair --install-units`, or inspect with `systemctl status`",
					"%s", strings.Join(problems, "; "))
			}
			return preflight.OK("all units loaded")
		},
	}
}

func (d *Deps) checkLastBackup(inst domain.Installation) preflight.Check {
	return preflight.Check{
		ID:          "backup.freshness",
		Category:    preflight.CategoryBackup,
		Description: "a recent backup exists",
		Fatal:       false,
		Run: func(ctx context.Context) events.CheckResult {
			backups, err := d.Backup.List(ctx)
			if err != nil {
				return preflight.Warn("", "cannot list backups: %s", domain.AsError(err).Message)
			}
			if len(backups) == 0 {
				return preflight.Warn(
					"take one with `morzer backup`, and enable the backup timer",
					"no backups exist")
			}

			latest := backups[0]
			age := d.now().Sub(latest.At.Time)
			stale := inst.Policy.StaleBackupAfter.Or(48 * time.Hour)

			if age > stale {
				return preflight.Warn(
					"run `morzer backup`, and check that the backup timer is active "+
						"(`systemctl list-timers`)",
					"the most recent backup is %s old (threshold %s)",
					age.Round(time.Hour), stale)
			}
			return preflight.OK("%s, %s old", latest.ID, age.Round(time.Minute))
		},
	}
}
