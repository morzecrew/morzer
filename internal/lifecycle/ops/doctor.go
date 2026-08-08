package ops

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

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
		d.checkInstallationFileMatchesState(),
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
		if d.Runtime != nil {
			checks = append(checks,
				d.checkVolumeHelperImage(inst, rel),
				d.checkVolumeCoverage(inst, rel),
			)
			if d.Backup != nil {
				checks = append(checks, d.checkBackupGrowth(inst, rel))
			}
		}
	}

	// Registered outside the release-loaded branch on purpose. CheckForUpdate
	// needs the persisted release record and its source ref, not a bundle
	// that parses -- so an installation whose release root is broken or
	// temporarily unreachable should still be told a fixed version exists.
	// Inside that branch, a broken release silently removed the check.
	checks = append(checks, d.checkUpdateAvailable(inst))

	if d.Supervisor != nil {
		checks = append(checks, d.checkUnits(inst))
	}
	if d.Backup != nil {
		checks = append(checks, d.checkLastBackup(inst))
	}
	if d.Targets != nil {
		checks = append(checks, d.checkBackupTargets(inst))
		if inst.Backup.HasTargets() && d.Backup != nil {
			checks = append(checks, d.checkBackupTargetFreshness(inst))
		}
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

// checkInstallationFileMatchesState catches the one confusing failure this
// layout allows: an operator edits /etc/<product>/installation.yaml, sees no
// effect, and cannot tell whether the edit was wrong, the file is wrong, or the
// manager is broken.
//
// Nothing reads that file -- the manager reads its own JSON state -- so an edit
// changes nothing. Reporting the disagreement by name is what turns a silent
// no-op into a diagnosis.
func (d *Deps) checkInstallationFileMatchesState() preflight.Check {
	return preflight.Check{
		ID:          "config.installation-file",
		Category:    preflight.CategoryConfig,
		Description: "installation.yaml matches the recorded state",
		Run: func(ctx context.Context) events.CheckResult {
			recorded, err := d.State.LoadInstallation(ctx)
			if err != nil {
				// checkInstallationReadable already reports this,
				// and saying it twice helps nobody.
				return preflight.OK("not checked: the installation state is unreadable")
			}

			raw, err := os.ReadFile(d.Paths.InstallationFile())
			if err != nil {
				if os.IsNotExist(err) {
					return preflight.Warn(
						"run `morzer init --repair` to write it",
						"%s is missing", d.Paths.InstallationFile())
				}
				return preflight.Warn("check the permissions on "+d.Paths.EtcDir,
					"cannot read %s: %v", d.Paths.InstallationFile(), err)
			}

			var onDisk domain.Installation
			if err := yaml.Unmarshal(raw, &onDisk); err != nil {
				return preflight.Warn(
					"the manager will rewrite it on the next `morzer config set`",
					"%s is not valid YAML", d.Paths.InstallationFile())
			}

			if diffs := installationDifferences(onDisk, recorded); len(diffs) > 0 {
				return preflight.Warn(
					"the file is a report, not a control: nothing reads it back. "+
						"Use `morzer config set` to change a parameter, or "+
						"`morzer init --repair` to rewrite the file from the state",
					"%s disagrees with the recorded state (%s); the recorded state is what runs",
					d.Paths.InstallationFile(), strings.Join(diffs, ", "))
			}
			return preflight.OK("in step with the recorded state")
		},
	}
}

// installationDifferences names the fields that disagree.
//
// Only what an operator would plausibly hand-edit. Comparing whole structs
// would report a timestamp's formatting as configuration drift.
func installationDifferences(onDisk, recorded domain.Installation) []string {
	var out []string

	if onDisk.Profile != recorded.Profile {
		out = append(out, "profile")
	}
	if strings.Join(onDisk.Domains, ",") != strings.Join(recorded.Domains, ",") {
		out = append(out, "domains")
	}
	if !policyEqual(onDisk.Policy, recorded.Policy) {
		out = append(out, "policy")
	}
	for _, name := range differingKeys(onDisk.Parameters, recorded.Parameters) {
		out = append(out, "parameters."+name)
	}
	return out
}

// policyEqual compares field by field, because Policy holds a slice and is not
// comparable with ==.
func policyEqual(a, b domain.Policy) bool {
	return a.RequireSignature == b.RequireSignature &&
		a.RetainReleases == b.RetainReleases &&
		a.RetainBackups == b.RetainBackups &&
		a.SkipBackupBeforeUpdate == b.SkipBackupBeforeUpdate &&
		a.StaleBackupAfter == b.StaleBackupAfter &&
		strings.Join(a.SigningKeys, ",") == strings.Join(b.SigningKeys, ",")
}

func differingKeys(a, b map[string]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range []map[string]string{a, b} {
		for name := range m {
			if !seen[name] && a[name] != b[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out
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

// checkUpdateAvailable reports whether a newer release exists.
//
// Info rather than warn when one does: being behind is not a fault, and a check
// that warns forever until an operator updates is how a green report stops
// being read. The only warning here is about the check itself failing.
//
// Gated by `update.check`, because this runs unprompted -- under a timer, in a
// script, in somebody's dashboard -- and contacting the vendor's registry from
// those is the phone-home an operator has to opt into. `morzer update --check`
// is the path that does not need permission.
func (d *Deps) checkUpdateAvailable(inst domain.Installation) preflight.Check {
	return preflight.Check{
		ID:          "release.update-available",
		Category:    preflight.CategoryNetwork,
		Description: "a newer release is available",
		Fatal:       false,
		Run: func(ctx context.Context) events.CheckResult {
			if !inst.Update.CheckAllowed(false) {
				return preflight.OK("update checking is off")
			}

			probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			res, err := CheckForUpdate(probeCtx, d, UpdateCheckOptions{})
			if err != nil {
				// Never "up to date". A check that could not run
				// says so, because the alternative is an
				// operator acting on an answer nobody gave.
				return preflight.Warn(
					"run `morzer update --check` for the detail",
					"cannot check for updates: %s", domain.AsError(err).Message)
			}
			if res.Available {
				return preflight.OK("%s is available (installed %s)",
					res.Latest, res.Installed)
			}
			return preflight.OK("%s is installed; nothing newer is offered", res.Installed)
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

			specs, err := d.checkSpecs(inst, rel, "", domain.OpTypeApply)
			if err != nil {
				// A health URL that cannot be resolved is a
				// finding, not a crash: doctor's job is to
				// report what is wrong with the installation.
				return preflight.Fail("check the release's parameters with `morzer release show`",
					"%s", domain.AsError(err).Message)
			}

			results, err := d.Health.CheckOnce(probeCtx, specs)
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

// checkBackupTargets reports whether every configured target answers.
//
// The question is "would tonight's backup leave this machine", and the moment to
// ask is while the machine is still here. An unmounted disk or an expired access
// key is invisible until a push fails -- and a push failing fails the backup, so
// an operator would rather be told now than at 03:00.
//
// A target that cannot be reached is a `fail` rather than a `warn`. This is not
// the registry-reachability case, where needing the network is normal: an
// unreachable target means the deployment's data is on one disk, which is the
// state configuring a target was meant to end.
func (d *Deps) checkBackupTargets(inst domain.Installation) preflight.Check {
	return preflight.Check{
		ID:          "backup.target-reachable",
		Category:    preflight.CategoryBackup,
		Description: "every configured backup target is reachable",
		// Fatal, so the runner does not downgrade the refusal to a
		// warning. An unreachable target means every backup from now on
		// fails at the push step, and a monitoring system watching
		// doctor's exit code should hear about that before the backup
		// does.
		Fatal: true,
		Run: func(ctx context.Context) events.CheckResult {
			if !inst.Backup.HasTargets() {
				// Not a finding. Plenty of deployments keep
				// backups on one machine on purpose, and a
				// warning nobody can act on is a warning
				// everybody learns to ignore. checkLastBackup
				// already covers having no backups at all.
				return preflight.OK("no off-machine target is configured")
			}

			// Scaled by the number of targets, because the probe walks
			// them in turn: one shared budget meant a slow first target
			// spent it, and every healthy target after was reported as
			// unreachable for no reason of its own.
			probeCtx, cancel := context.WithTimeout(ctx,
				time.Duration(max(1, len(inst.Backup.Targets)))*30*time.Second)
			defer cancel()

			statuses := d.TargetStatuses(probeCtx, inst)
			var problems, reached []string
			for _, s := range statuses {
				if !s.Reachable {
					problems = append(problems, fmt.Sprintf("%s (%s)", s.URL, s.Error))
					continue
				}
				reached = append(reached, fmt.Sprintf("%s: %d backup(s)", s.URL, s.Backups))
			}

			if len(problems) > 0 {
				return preflight.Fail(
					"until this is fixed every backup will fail at the push step, "+
						"because a backup that did not leave the machine is not "+
						"the backup that was asked for",
					"cannot reach %s", strings.Join(problems, "; "))
			}
			return preflight.OK("%s", strings.Join(reached, "; "))
		},
	}
}

// checkBackupTargetFreshness reports a local backup that never reached a
// target.
//
// This is the failure the whole targets mechanism exists to prevent, and it is
// the one that hides: the backup ran, the backup succeeded, the file is there,
// and the copy that would survive the machine is not. `fail` rather than `warn`,
// for the same reason.
func (d *Deps) checkBackupTargetFreshness(inst domain.Installation) preflight.Check {
	return preflight.Check{
		ID:          "backup.target-freshness",
		Category:    preflight.CategoryBackup,
		Description: "the most recent backup reached a target",
		// Fatal for the same reason, and a stronger one: this is the
		// failure that hides. The backup ran, it succeeded, the file is
		// there -- and the copy that would survive the machine is not.
		Fatal: true,
		Run: func(ctx context.Context) events.CheckResult {
			local, err := d.Backup.List(ctx)
			if err != nil {
				return preflight.Warn("", "cannot list local backups: %s",
					domain.AsError(err).Message)
			}
			if len(local) == 0 {
				// checkLastBackup already says there are none,
				// and saying it twice helps nobody.
				return preflight.OK("not checked: there are no backups yet")
			}
			newest := local[0]

			// Per target, for the same reason as the check above.
			probeCtx, cancel := context.WithTimeout(ctx,
				time.Duration(max(1, len(inst.Backup.Targets)))*30*time.Second)
			defer cancel()

			remote, err := ListRemote(probeCtx, d, TargetOptions{})
			if err != nil {
				// A failure, not a warning, even though the
				// reachability check above usually says the same
				// thing first. This check is fatal because it is
				// the failure that hides, and "could not tell"
				// downgraded to a warning is a green report for a
				// question nobody answered -- which is the one
				// outcome this check exists to prevent.
				return preflight.Fail(
					"see the target-reachable check above for why",
					"cannot tell whether %s left this machine: %s",
					newest.ID, domain.AsError(err).Message)
			}

			// Every configured target, not any of them. A push writes
			// to all of them, and a push that failed part-way leaves the
			// backup on the targets it reached -- so "it is on one of
			// them" is exactly the state that needs reporting, not the
			// state that clears the check.
			// Keyed on the canonical form rather than on the string
			// either side happened to render, so a target does not read
			// as missing its own backup over a trailing slash.
			present := make(map[string]bool, len(remote))
			for _, r := range remote {
				if r.Manifest.ID != newest.ID {
					continue
				}
				if ref, perr := ports.TargetURL(r.Target); perr == nil {
					present[ref.Canonical()] = true
				}
			}

			var missing, holding []string
			for _, cfg := range inst.Backup.Targets {
				ref, perr := ports.TargetURL(cfg.URL)
				if perr != nil {
					// A target whose URL does not parse is
					// already reported by the check above.
					continue
				}
				if present[ref.Canonical()] {
					holding = append(holding, ref.String())
					continue
				}
				missing = append(missing, ref.String())
			}

			if len(missing) == 0 {
				return preflight.OK("%s is on %s", newest.ID, strings.Join(holding, ", "))
			}
			if len(holding) == 0 {
				return preflight.Fail(
					fmt.Sprintf("run `morzer backup push %s`", newest.ID),
					"the most recent backup (%s) is on no target, so every copy of "+
						"this deployment's data is on this machine", newest.ID)
			}
			return preflight.Fail(
				fmt.Sprintf("run `morzer backup push %s`", newest.ID),
				"the most recent backup (%s) reached %s but not %s",
				newest.ID, strings.Join(holding, ", "), strings.Join(missing, ", "))
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

// checkVolumeHelperImage reports the volume helper image when it is not local.
//
// It exists for the machine that is about to lose its network, not the one that
// already has: volumes are read through a container, so an air-gapped install
// whose helper image was never pulled discovers it on backup night. Asking
// while there is still a network to answer with is the entire point.
//
// A warning rather than a failure, matching runtime.images-local: needing a
// pull is the normal state of a machine that has just been installed.
// volumeHelperImageEnv is the variable an operator sets to override the image
// volumes are read through.
//
// Spelled here rather than imported: the CLI owns the name and imports this
// package, and the adapter that reads it is below this layer. Named in the
// remedy regardless -- a diagnostic that says an image is wrong without saying
// which knob set it leaves the operator hunting.
const volumeHelperImageEnv = "MORZER_VOLUME_HELPER_IMAGE"

func (d *Deps) checkVolumeHelperImage(inst domain.Installation, rel domain.Release) preflight.Check {
	return preflight.Check{
		ID:          "backup.volume-helper",
		Category:    preflight.CategoryBackup,
		Description: "the volume helper image is available offline",
		Fatal:       false,
		Run: func(ctx context.Context) events.CheckResult {
			capturer, ok := d.Runtime.(ports.VolumeCapturer)
			if !ok {
				return preflight.OK("the configured runtime does not capture volumes")
			}
			inspector, ok := d.Runtime.(ports.ImageInspector)
			if !ok {
				return preflight.OK("the configured runtime cannot inspect local images")
			}

			ref := capturer.HelperImage()

			// The same rule the capture enforces, applied here so it
			// is found now rather than during a backup. A runtime that
			// cannot answer is not interrogated -- the pinning rule is
			// this adapter's, not the port's.
			if pinner, ok := d.Runtime.(interface{ HelperImagePinned() bool }); ok && !pinner.HelperImagePinned() {
				return preflight.Fail(
					fmt.Sprintf("unset %s to use the image this manager ships, or "+
						"pin the one you want: `docker image inspect --format "+
						"'{{index .RepoDigests 0}}' %s`", volumeHelperImageEnv, ref),
					"the volume helper image %s is not pinned by digest, so every "+
						"backup will refuse to capture volumes", ref)
			}

			present, err := inspector.HasImage(ctx, ref)
			if err != nil {
				return preflight.Warn("check that the Docker daemon is running: `docker info`",
					"cannot tell whether %s is here: %s", shortRef(ref), domain.AsError(err).Message)
			}
			if !present {
				return preflight.Warn(
					fmt.Sprintf("run `docker pull %s` -- do it now rather than "+
						"during a backup on a machine that has lost its network", ref),
					"%s is not on this machine, so a backup cannot capture volumes",
					shortRef(ref))
			}
			// The full pinned reference, not shortRef. Every other
			// check abbreviates because it names several images for
			// orientation; this one exists so an operator can copy the
			// single identifier they must pull before going offline,
			// and a digest they have to reconstruct is not that.
			return preflight.OK("%s is local", ref)
		},
	}
}

// checkVolumeCoverage reports project storage no backup would capture.
//
// The question it answers is the one the whole volumes component exists for: is
// there data in this deployment that a restore would not bring back. A vendor
// who excluded a volume meant to, and a bind mount was never a candidate -- but
// an operator should know both before they need to know them.
func (d *Deps) checkVolumeCoverage(inst domain.Installation, rel domain.Release) preflight.Check {
	return preflight.Check{
		ID:          "backup.volume-coverage",
		Category:    preflight.CategoryBackup,
		Description: "every named volume is covered by a backup",
		Fatal:       false,
		Run: func(ctx context.Context) events.CheckResult {
			inspector, ok := d.Runtime.(ports.VolumeInspector)
			if !ok {
				return preflight.OK("the configured runtime does not report volumes")
			}

			cfg, err := d.runtimeConfig(rel, inst, "")
			if err != nil {
				return preflight.Warn("", "cannot resolve the project: %s",
					domain.AsError(err).Message)
			}
			storage, err := inspector.Volumes(ctx, cfg)
			if err != nil {
				return preflight.Warn("", "cannot read the project's volumes: %s",
					domain.AsError(err).Message)
			}
			// Anonymous volumes and declarations naming a volume the
			// project does not have are both reported below, so a
			// project holding only those must not short-circuit here:
			// a deployment whose data lives entirely on anonymous
			// volumes would otherwise get a clean bill of health.
			if len(storage.Volumes) == 0 && len(storage.Binds) == 0 &&
				len(storage.Anonymous) == 0 && len(rel.Manifest.Backup.Volumes) == 0 {
				return preflight.OK("the project declares no volumes")
			}

			var excluded, binds []string
			for _, vol := range storage.Volumes {
				if rel.Manifest.Backup.Consistency(vol.Name) == domain.VolumeExclude {
					excluded = append(excluded, vol.Name)
				}
			}
			for _, bind := range storage.Binds {
				binds = append(binds, bind.Source)
			}

			var notes []string
			if len(excluded) > 0 {
				notes = append(notes, fmt.Sprintf("%s excluded by the release",
					strings.Join(excluded, ", ")))
			}
			if len(binds) > 0 {
				notes = append(notes, fmt.Sprintf("%s are bind mounts and are never captured",
					strings.Join(binds, ", ")))
			}

			// An anonymous volume cannot be captured at all, and the
			// remedy belongs to the vendor rather than the operator --
			// so the operator has to be able to see it in order to ask.
			var anon []string
			for _, a := range storage.Anonymous {
				anon = append(anon, fmt.Sprintf("%s at %s", a.Service, a.Target))
			}
			if len(anon) > 0 {
				notes = append(notes, fmt.Sprintf(
					"%s mount anonymous volumes, which no backup can capture",
					strings.Join(anon, ", ")))
			}

			// A declaration naming a volume the project does not have
			// is a vendor typo that silently does nothing: `uplods:
			// {consistency: exclude}` leaves the real pgdata being
			// captured, and nothing says so.
			declared := map[string]bool{}
			for _, vol := range storage.Volumes {
				declared[vol.Name] = true
			}
			var phantom []string
			for name := range rel.Manifest.Backup.Volumes {
				if name != "" && !declared[name] {
					phantom = append(phantom, name)
				}
			}
			sort.Strings(phantom)
			if len(phantom) > 0 {
				notes = append(notes, fmt.Sprintf(
					"the release declares backup.volumes for %s, which this project "+
						"does not define", strings.Join(phantom, ", ")))
			}

			captured := len(storage.Volumes) - len(excluded)
			if len(notes) == 0 {
				return preflight.OK("%d named volume(s) captured", captured)
			}

			// "0 of 0 named volumes captured" is what a project with
			// nothing but bind mounts used to report, which reads as a
			// failure to capture rather than as nothing to capture.
			coverage := fmt.Sprintf("%d of %d named volume(s) captured",
				captured, len(storage.Volumes))
			if len(storage.Volumes) == 0 {
				coverage = "this project declares no named volumes"
			}

			return preflight.Warn(
				"an excluded volume is the vendor saying its backup hook owns that "+
					"data; a bind mount is yours to copy. Make sure something does.",
				"%s -- %s", coverage, strings.Join(notes, "; "))
		},
	}
}

// checkBackupGrowth warns when the retention count will not fit.
//
// Retention counts backups, not bytes, and that was fine for a directory of
// database dumps. A hundred gigabytes of uploads copied nightly is a different
// shape of problem, and the first sign of it should not be a backup that fails
// on ENOSPC at 3am.
func (d *Deps) checkBackupGrowth(inst domain.Installation, rel domain.Release) preflight.Check {
	return preflight.Check{
		ID:          "backup.growth",
		Category:    preflight.CategoryBackup,
		Description: "the retention policy fits on this disk",
		Fatal:       false,
		Run: func(ctx context.Context) events.CheckResult {
			backups, err := d.Backup.List(ctx)
			if err != nil || len(backups) == 0 {
				return preflight.OK("no backups to measure yet")
			}

			// The largest, not the mean. Retention keeps N backups and
			// the question is whether N of them fit; averaging over a
			// history that predates the volumes component would answer
			// a question about the past.
			var largest int64
			for _, b := range backups {
				if b.Size > largest {
					largest = b.Size
				}
			}
			if largest == 0 {
				return preflight.OK("no backups to measure yet")
			}

			keep := inst.RetentionBackups(rel.Manifest)

			// Saturating, the way checkVolumeSpace does it. A
			// hundred-gigabyte backup times a retention count in the
			// hundreds overflows int64, and a wrapped product comes out
			// negative -- which makes the shortfall below negative too,
			// so a policy no disk on earth could satisfy compares as
			// satisfied and this check reports ok. That is the one
			// direction it must never fail in.
			required := int64(math.MaxInt64)
			if keep > 0 && largest <= math.MaxInt64/int64(keep) {
				required = largest * int64(keep)
			}

			var held int64
			for _, b := range backups {
				if b.Size <= 0 {
					continue
				}
				if held > math.MaxInt64-b.Size {
					held = math.MaxInt64
					break
				}
				held += b.Size
			}
			free, err := d.freeSpace(d.Paths.BackupsDir())
			if err != nil {
				// Not OK: nothing was measured, so nothing was
				// checked, and a green line here reads as "retention
				// fits" to whoever is deciding whether to intervene.
				return preflight.Warn(
					"check the backup directory is readable; until it is, "+
						"nothing is watching this disk fill up",
					"cannot measure free space on %s: %s",
					d.Paths.BackupsDir(), err)
			}

			// Two ways this disk runs out, and they are different
			// questions.
			//
			// The first is the policy as a whole: what retention still
			// has to make room for, beyond what is already here.
			//
			// The second is the very next backup on its own. Pruning
			// happens after a backup is written and never before -- the
			// new copy has to be on the disk beside the old ones before
			// retention can remove any of them -- so a retention set
			// that is already full needs a whole backup of headroom
			// regardless. Reporting only the first meant that the
			// steady state, which is where an installation spends its
			// entire life, was the state this check could not see: it
			// said ok the night before ENOSPC.
			remaining := required - held
			switch {
			case remaining > free:
				return preflight.Warn(
					"lower retention (`policy.retain_backups`), push to a target and "+
						"prune locally, or exclude a large volume in the release manifest",
					"keeping %d backups of %s needs about %s more than the %s free on %s",
					keep, domain.ByteSize(largest), domain.ByteSize(remaining-free),
					domain.ByteSize(free), d.Paths.BackupsDir())
			case free < largest:
				return preflight.Warn(
					"free space, push to a target and prune locally, or exclude a "+
						"large volume in the release manifest",
					"the %d backups retained already fit, but the next backup of about "+
						"%s does not: only %s is free on %s, and a backup is written "+
						"in full before the oldest one is pruned",
					keep, domain.ByteSize(largest), domain.ByteSize(free),
					d.Paths.BackupsDir())
			default:
				return preflight.OK("%d backups of up to %s fit in the %s free",
					keep, domain.ByteSize(largest), domain.ByteSize(free))
			}
		},
	}
}
