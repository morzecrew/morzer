package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/internal/ui/plain"
	"github.com/morzecrew/morzer/internal/ui/tty"
)

func newInitCommand(app *App) *cobra.Command {
	var (
		product        string
		releasePath    string
		profile        string
		domains        []string
		recoveryKey    string
		noRecoveryKey  bool
		installUnits   bool
		backupSchedule string
		generate       bool
		repair         bool
		requireSig     bool
		signingKeys    []string
		set            []string
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create a new installation",
		Long: "Creates the directory layout, the machine's age identity, the\n" +
			"installation configuration, the encrypted secret state and, optionally,\n" +
			"the systemd units.\n\n" +
			"It never overwrites an existing installation, and it does not start the\n" +
			"product: run `morzer apply` afterwards.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// The product name may come from the bundle, so it is
			// resolved before the paths are finalised.
			if product == "" && releasePath != "" {
				manifest, err := release.LoadManifest(
					releasePath + "/" + release.ManifestFileName)
				if err != nil {
					return err
				}
				product = manifest.Metadata.Name
			}
			// A missing product name is not fatal at a terminal: the
			// wizard below asks for it. It is fatal everywhere else,
			// and the check moved after the wizard for that reason.
			if product == "" && !isInteractive() {
				return domain.Usage("a product name is required").
					WithHint("pass --product <name>, or --release <bundle> to take it from the manifest")
			}

			// Rebuild the layout now that the name is known. Every
			// managed path derives from it, and the adapters wired
			// during the pre-run captured the placeholder paths --
			// so they are rebuilt too, not merely re-pointed.
			if product != "" {
				app.Flags.product = product
				if err := app.rewireForProduct(cmd.Context(), product); err != nil {
					return err
				}
			}

			opts := ops.InitOptions{
				Options:           app.operationOptions(),
				Product:           product,
				ReleasePath:       releasePath,
				Profile:           profile,
				Domains:           domains,
				RecoveryRecipient: recoveryKey,
				NoRecoveryKey:     noRecoveryKey,
				InstallUnits:      installUnits,
				BackupSchedule:    backupSchedule,
				GenerateSecrets:   generate,
				RequireSignature:  requireSig,
				SigningKeys:       signingKeys,
				Repair:            repair,
			}

			// Parsed before anything is created, so a typo fails
			// with nothing on disk rather than after a deployment
			// is running on a default the operator did not intend.
			params, err := domain.ParseAssignments(set)
			if err != nil {
				return err
			}
			opts.Parameters = params

			// Only at a terminal, only without --yes, and only when
			// something it collects is actually missing. Everything
			// else -- CI, a systemd unit, a provisioning script --
			// runs the flags it was given, untouched.
			if wizardApplies(app, opts) {
				filled, err := runInitWizard(cmd.Context(), app, opts)
				if err != nil {
					return err
				}
				opts = filled

				// Printed every time: it is what stops the
				// wizard becoming the only way anyone knows how
				// to do this.
				fmt.Fprintf(app.Stream.Err, "\nequivalent to:\n  %s\n\n",
					EquivalentCommand(opts))

				// The product may have been chosen just now, and
				// every managed path derives from it.
				if opts.Product != product {
					app.Flags.product = opts.Product
					if err := app.rewireForProduct(cmd.Context(), opts.Product); err != nil {
						return err
					}
				}
			}

			return app.runOperation(cmd.Context(), func(ctx context.Context) (ops.Result, error) {
				return ops.Init(ctx, app.Deps, opts)
			})
		},
	}

	f := cmd.Flags()
	f.StringArrayVar(&set, "set", nil,
		"set a release parameter, as name=value; repeat for several")
	f.StringVar(&product, "product", "", "product name; taken from the release manifest when --release is given")
	f.StringVar(&releasePath, "release", "", "release bundle to stage during init")
	f.StringVar(&profile, "profile", "", "deployment profile from the release manifest")
	f.StringSliceVar(&domains, "domain", nil, "public domain; repeat for several, the first is canonical")
	f.StringVar(&recoveryKey, "recovery-recipient", "", "offline age public key that can decrypt the secret state")
	f.BoolVar(&noRecoveryKey, "no-recovery-recipient", false,
		"proceed without an offline recovery key (losing this machine then loses its secrets)")
	f.BoolVar(&installUnits, "install-units", true, "install systemd units when systemd is available")
	f.StringVar(&backupSchedule, "backup-schedule", "", "systemd OnCalendar expression for scheduled backups")
	f.BoolVar(&generate, "generate-secrets", true, "generate every secret the release declares a generator for")
	f.StringArrayVar(&signingKeys, "signing-key", nil,
		"minisign public key a release signature must verify against; repeat for several")
	f.BoolVar(&requireSig, "require-signature", false,
		"refuse any release that is not signed by a configured signing key")
	f.BoolVar(&repair, "repair", false, "restore missing directories on an existing installation")

	return cmd
}

func newApplyCommand(app *App) *cobra.Command {
	var (
		startup bool
		profile string
	)

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Converge the system to the installed release",
		Long: "Renders configuration and secrets, pulls images, runs migrations,\n" +
			"starts services and waits for health.\n\n" +
			"Idempotent: applying an unchanged system runs nothing and says so.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := app.operationOptions()
			opts.Startup = startup
			opts.Profile = profile

			return app.runOperation(cmd.Context(), func(ctx context.Context) (ops.Result, error) {
				return ops.Apply(ctx, app.Deps, opts)
			})
		},
	}

	f := cmd.Flags()
	f.BoolVar(&startup, "startup", false,
		"boot-time mode: skip pulls when images are local, skip migrations when the schema is current")
	f.StringVar(&profile, "profile", "", "override the installation's deployment profile")

	return cmd
}

func newUpdateCommand(app *App) *cobra.Command {
	var (
		skipBackup bool
		digest     string
		profile    string
		to         string
	)

	cmd := &cobra.Command{
		Use:   "update [bundle]",
		Short: "Install a new release over the current one",
		Long: "Verifies the bundle, checks it against the compatibility the manifest\n" +
			"declares, takes a pre-update backup, stages the release and converges\n" +
			"to it.\n\n" +
			"A failed update rolls back to the release that was running. The database\n" +
			"is never rolled back automatically: when a migration cannot be undone the\n" +
			"release says so, and the answer is a restore from the backup taken here.\n\n" +
			"Takes a bundle path, or --to <version> for a release already fetched into\n" +
			"the release store.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := app.operationOptions()
			opts.Profile = profile
			opts.SkipBackup = skipBackup

			// Skipping the backup is the one choice here that removes
			// a safety net rather than adding a risk, so it needs the
			// same explicit authorisation a destructive action does.
			if skipBackup && !opts.Force {
				return domain.Usage("--skip-backup also requires --force").
					WithHint("the pre-update backup is what a failed update is recovered from")
			}

			// The backup engine is built from the *current* release,
			// which is what is being backed up. Its absence is only
			// fatal when a backup is actually going to be taken, so
			// the error is left to the step.
			_ = app.attachBackupEngine(cmd.Context())

			ref := ""
			if len(args) == 1 {
				ref = args[0]
			}

			return app.runOperation(cmd.Context(), func(ctx context.Context) (ops.Result, error) {
				return ops.Update(ctx, app.Deps, ops.UpdateOptions{
					Options:      opts,
					Ref:          ref,
					To:           to,
					ExpectDigest: digest,
				})
			})
		},
	}

	f := cmd.Flags()
	f.BoolVar(&skipBackup, "skip-backup", false,
		"skip the pre-update backup; requires --force and is recorded in the journal")
	f.StringVar(&digest, "digest", "",
		"expected bundle content digest; a mismatch refuses the update")
	f.StringVar(&profile, "profile", "", "override the installation's deployment profile")
	f.StringVar(&to, "to", "", "install a version already in the release store, instead of a bundle path")

	return cmd
}

func newRollbackCommand(app *App) *cobra.Command {
	var to string

	cmd := &cobra.Command{
		Use:   "rollback",
		Short: "Return to the previous release",
		Long: "Reports three things separately before acting: whether the containers\n" +
			"can be reversed, whether the current database schema is compatible with\n" +
			"the previous release, and whether a restore is required.\n\n" +
			"Refuses when the answers do not permit a safe return, naming the backup\n" +
			"to restore from instead. The database is never rolled back automatically,\n" +
			"and --force does not override a refusal: it authorises destructive\n" +
			"actions, not incorrect ones.\n\n" +
			"Returns to the immediate previous release by default. Use --to to reach\n" +
			"an older one: each rollback promotes the release it displaced, so a\n" +
			"second rollback without --to returns to where the first started.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Wired so a refusal can name the most recent backup. Its
			// absence only costs the operator a more specific hint.
			_ = app.attachBackupEngine(cmd.Context())

			return app.runOperation(cmd.Context(), func(ctx context.Context) (ops.Result, error) {
				result, err := ops.Rollback(ctx, app.Deps, ops.RollbackOptions{
					Options: app.operationOptions(),
					To:      to,
				})

				// The assessment is the point of the command, so
				// it reaches --json output on the refusal path
				// too.
				if app.json != nil {
					app.jsonData = result.Data
				}
				return result, err
			})
		},
	}

	cmd.Flags().StringVar(&to, "to", "",
		"roll back to this installed version rather than the immediate previous one")

	return cmd
}

func newStatusCommand(app *App) *cobra.Command {
	var (
		clearIntervention string
		watch             bool
		interval          time.Duration
	)

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show what is deployed and whether it is working",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("clear-intervention") {
				// The bare form arrives as NoOptDefVal, a single
				// space (pflag treats "" as "value required").
				// Trimming maps it onto the documented "empty
				// selects the only one" contract.
				id := strings.TrimSpace(clearIntervention)
				result, err := ops.ClearIntervention(cmd.Context(), app.Deps, id)
				app.finish(result)
				return err
			}

			// A missing backup engine must not make status fail:
			// the command has to work on a machine with no release
			// installed.
			_ = app.attachBackupEngine(cmd.Context())

			if watch {
				return app.watchStatus(cmd.Context(), interval)
			}

			status, err := ops.GetStatus(cmd.Context(), app.Deps)
			if err != nil {
				return err
			}

			switch {
			case app.json != nil:
				app.jsonData = status
			case app.rich():
				tty.RenderStatus(app.Stream.Out, app.theme(), status)
			default:
				plain.RenderStatus(app.Stream.Out, status)
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&clearIntervention, "clear-intervention", "",
		"acknowledge a requires-manual-intervention operation (empty selects the only one)")
	f.Lookup("clear-intervention").NoOptDefVal = " "
	f.BoolVar(&watch, "watch", false,
		"refresh the status until interrupted; requires a terminal")
	f.DurationVar(&interval, "interval", 2*time.Second,
		"how often --watch refreshes")

	return cmd
}

func newDoctorCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Run read-only diagnostics",
		Long: "Checks the host, the tools, the installation, secrets, the runtime and\n" +
			"backups. Every non-ok result carries a suggested remedy.\n\n" +
			"Exits 3 when any check fails; warnings exit 0.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = app.attachBackupEngine(cmd.Context())

			report, err := ops.Doctor(cmd.Context(), app.Deps)
			if err != nil {
				return err
			}

			switch {
			case app.json != nil:
				app.jsonData = report
			case app.rich():
				// The plain presenter already streamed each
				// check as it ran; the table is the summary.
				tty.RenderDoctor(app.Stream.Out, app.theme(), report, ui.TerminalWidth())
			default:
				plain.RenderDoctor(app.Stream.Out, report)
			}

			// The exit code reflects the worst result, which is what
			// makes `doctor` usable as a monitoring probe.
			if report.Worst == "fail" {
				return domain.Preflight(nil, "%d diagnostic check(s) failed", report.Summary.Fail).
					WithHint("see the remedies above")
			}
			return nil
		},
	}
}

func newBackupCommand(app *App) *cobra.Command {
	var (
		reason        string
		components    []string
		noVerify      bool
		noPrune       bool
		noPush        bool
		noPruneRemote bool
		noDowntime    bool
	)

	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Back up the database, volumes, configuration and secret state",
		Long: "Backs up what the release's hook produces, the project's named\n" +
			"volumes, the configuration and the encrypted secret state.\n\n" +
			"A volume the release has not declared safe to read live is captured\n" +
			"with the services that mount it stopped, because a copy taken while\n" +
			"something is writing is crash-consistent rather than\n" +
			"application-consistent -- what a power cut would have left. Use\n" +
			"--no-downtime to skip those volumes instead; they are then named in\n" +
			"the backup's manifest as uncaptured, so nothing is lost silently.\n\n" +
			"This does not replace the backup hook for anything with a\n" +
			"transaction log. A volume copy of a running database is not a\n" +
			"database backup.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.attachBackupEngine(cmd.Context(), withDowntime(!noDowntime)); err != nil {
				return err
			}

			parsed, err := parseComponents(components)
			if err != nil {
				return err
			}

			return app.runOperation(cmd.Context(), func(ctx context.Context) (ops.Result, error) {
				return ops.Backup(ctx, app.Deps, ops.BackupOptions{
					Options:     app.operationOptions(),
					Reason:      reason,
					Components:  parsed,
					Verify:      !noVerify,
					Prune:       !noPrune,
					Push:        !noPush,
					PruneRemote: !noPruneRemote,
				})
			})
		},
	}

	f := cmd.Flags()
	f.StringVar(&reason, "reason", "manual", "why this backup was taken; recorded in its manifest")
	f.StringSliceVar(&components, "component", nil,
		"limit the backup to these components: database, files, config, secrets, manifest, volumes")
	// Named for what it costs, like --no-push: an operator reaching for
	// this is choosing a backup that omits volumes over one that briefly
	// stops services, and the help text should say so rather than leaving
	// them to find out from the manifest afterwards.
	f.BoolVar(&noDowntime, "no-downtime", false,
		"never stop a service to read a volume; volumes that would need it are skipped and reported")
	f.BoolVar(&noVerify, "no-verify", false, "skip re-reading the backup to check its checksums")
	f.BoolVar(&noPrune, "no-prune", false, "skip applying the retention policy afterwards")
	// Named for what it costs rather than for what it does: an operator
	// reaching for this flag should see, in the help text, that they are
	// choosing to leave the backup on the machine it is meant to outlive.
	f.BoolVar(&noPush, "no-push", false,
		"do not copy the backup to the configured targets; it stays only on this machine")
	f.BoolVar(&noPruneRemote, "no-prune-remote", false,
		"skip applying the retention policy on the targets")

	cmd.AddCommand(
		newBackupListCommand(app),
		newBackupVerifyCommand(app),
		newBackupTargetCommand(app),
		newBackupPushCommand(app),
		newBackupFetchCommand(app),
	)
	return cmd
}

func newBackupListCommand(app *App) *cobra.Command {
	var (
		remote          bool
		targetURL       string
		credentialsFile string
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List backups, newest first",
		Long: "Lists this machine's backups.\n\n" +
			"With --remote, lists what is on the configured targets instead. That\n" +
			"reads only each backup's manifest, which is the one file in a backup\n" +
			"that is not encrypted -- so it works from a machine that has lost every\n" +
			"key it ever had, which is the machine most likely to be running it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireRemoteMode(remote, targetURL, credentialsFile); err != nil {
				return err
			}
			if remote || targetURL != "" {
				return app.listRemoteBackups(cmd, targetURL, credentialsFile)
			}
			if err := app.attachBackupEngine(cmd.Context()); err != nil {
				return err
			}

			backups, err := app.Deps.Backup.List(cmd.Context())
			if err != nil {
				return err
			}

			if app.json != nil {
				app.jsonData = backups
				return nil
			}
			if len(backups) == 0 {
				_, _ = fmt.Fprintln(app.Stream.Out, "no backups")
				return nil
			}
			for _, b := range backups {
				fmt.Fprintf(app.Stream.Out, "%-24s  %s  %s\n",
					b.ID, b.At.Format("2006-01-02 15:04:05Z"), domain.ByteSize(b.Size))
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.BoolVar(&remote, "remote", false, "list what is on the configured backup targets")
	f.StringVar(&targetURL, "target", "",
		"list one target by URL, whether or not this installation configures it")
	f.StringVar(&credentialsFile, "credentials-file", "",
		"YAML file holding the target's credentials, for a machine whose secret state is not readable yet")
	return cmd
}

// requireRemoteMode refuses a credentials file with nothing to apply it to.
//
// Silently ignoring it is the worse failure: an operator who passed credentials
// and got a local listing would reasonably conclude the target holds what this
// machine holds.
func requireRemoteMode(remote bool, targetURL, credentialsFile string) error {
	if credentialsFile == "" || remote || targetURL != "" {
		return nil
	}
	return domain.Usage("--credentials-file needs a target to apply to").
		WithHint("add --remote for the configured targets, or --target <url> for one")
}

// listRemoteBackups prints what is on a target.
func (a *App) listRemoteBackups(cmd *cobra.Command, targetURL, credentialsFile string) error {
	creds, err := readCredentialsFile(credentialsFile)
	if err != nil {
		return err
	}

	backups, err := ops.ListRemote(cmd.Context(), a.Deps, ops.TargetOptions{
		Options:     a.operationOptions(),
		URL:         targetURL,
		Credentials: creds,
	})
	if err != nil {
		return err
	}

	if a.json != nil {
		a.jsonData = backups
		return nil
	}
	if len(backups) == 0 {
		_, _ = fmt.Fprintln(a.Stream.Out, "no backups on the target")
		return nil
	}
	for _, b := range backups {
		fmt.Fprintf(a.Stream.Out, "%-24s  %s  %-12s  %s\n",
			b.Manifest.ID, b.Manifest.CreatedAt.Format("2006-01-02 15:04:05Z"),
			b.Manifest.ReleaseVersion, b.Target)
	}
	return nil
}

func newBackupVerifyCommand(app *App) *cobra.Command {
	var (
		remote          bool
		targetURL       string
		credentialsFile string
	)

	cmd := &cobra.Command{
		Use:   "verify [backup-id]",
		Short: "Re-read a backup and check its checksums",
		Long: "Re-reads a backup and checks it against the checksums in its manifest.\n\n" +
			"With --remote, checks the copy on a target instead. That is a full\n" +
			"transfer and writes nothing: a backup nobody has read back is a hope,\n" +
			"and copying one to a bucket does not change that. Worth running on a\n" +
			"schedule against your oldest retained backup.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireRemoteMode(remote, targetURL, credentialsFile); err != nil {
				return err
			}
			id := ""
			if len(args) == 1 {
				id = args[0]
			}
			if remote || targetURL != "" {
				return app.verifyRemoteBackups(cmd, id, targetURL, credentialsFile)
			}
			if err := app.attachBackupEngine(cmd.Context()); err != nil {
				return err
			}

			ref, err := app.Deps.ResolveBackup(cmd.Context(), id)
			if err != nil {
				return err
			}
			if err := app.Deps.Backup.Verify(cmd.Context(), ref); err != nil {
				return err
			}

			app.finish(ops.Result{Summary: "backup " + ref.ID + " verified"})
			return nil
		},
	}

	f := cmd.Flags()
	f.BoolVar(&remote, "remote", false,
		"check the copy on the configured backup targets; a full transfer")
	f.StringVar(&targetURL, "target", "",
		"verify on one target by URL, whether or not this installation configures it")
	f.StringVar(&credentialsFile, "credentials-file", "",
		"YAML file holding the target's credentials, for a machine whose secret state is not readable yet")
	return cmd
}

// verifyRemoteBackups checks the copies on a target.
func (a *App) verifyRemoteBackups(cmd *cobra.Command, id, targetURL, credentialsFile string) error {
	creds, err := readCredentialsFile(credentialsFile)
	if err != nil {
		return err
	}

	result, err := ops.VerifyRemote(cmd.Context(), a.Deps, ops.FetchOptions{
		TargetOptions: ops.TargetOptions{
			Options:     a.operationOptions(),
			URL:         targetURL,
			Credentials: creds,
		},
		BackupID: id,
	})
	if err != nil {
		return err
	}
	a.finish(result)
	return nil
}

func newRestoreCommand(app *App) *cobra.Command {
	var (
		backupID   string
		confirm    string
		components []string
		crossInst  bool
		identity   string
	)

	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore from a backup",
		Long: "Verifies the backup, stops writers, restores the volumes and then the\n" +
			"database and files, re-applies the release and runs the smoke test.\n\n" +
			"A restored volume holds exactly what the backup held: anything written\n" +
			"to it since is gone, rather than left beside data restored to an\n" +
			"earlier moment. Writing into a volume is refused while a service that\n" +
			"mounts it is running, which is why the services are stopped first.\n\n" +
			"Destructive: requires --force and --confirm <installation-id>.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.attachBackupEngine(cmd.Context()); err != nil {
				return err
			}

			parsed, err := parseComponents(components)
			if err != nil {
				return err
			}

			return app.runOperation(cmd.Context(), func(ctx context.Context) (ops.Result, error) {
				return ops.Restore(ctx, app.Deps, ops.RestoreOptions{
					Options:                 app.operationOptions(),
					BackupID:                backupID,
					Components:              parsed,
					ConfirmedInstallationID: confirm,
					AllowCrossInstallation:  crossInst,
					IdentityFile:            identity,
				})
			})
		},
	}

	f := cmd.Flags()
	f.StringVar(&backupID, "backup", "", "backup id; the most recent when omitted")
	f.StringVar(&confirm, "confirm", "", "the installation id, typed to confirm a destructive restore")
	f.StringSliceVar(&components, "component", nil,
		"limit the restore to these components: database, files, config, secrets, manifest, volumes")
	f.StringVar(&identity, "identity", "",
		"age identity that can decrypt the backup; defaults to this machine's own key")
	f.BoolVar(&crossInst, "allow-cross-installation", false,
		"restore a backup belonging to a different installation")

	return cmd
}

func newVersionCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version, commit, and supported manifest API versions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			info := map[string]any{
				"version":                app.Build.Version,
				"commit":                 app.Build.Commit,
				"built":                  app.Build.Date,
				"supported_api_versions": apiVersionStrings(),
			}

			if app.json != nil {
				app.jsonData = info
				return nil
			}

			fmt.Fprintf(app.Stream.Out, "morzer %s\n", app.Build.Version)
			if app.Build.Commit != "" {
				fmt.Fprintf(app.Stream.Out, "commit  %s\n", app.Build.Commit)
			}
			if app.Build.Date != "" {
				fmt.Fprintf(app.Stream.Out, "built   %s\n", app.Build.Date)
			}
			fmt.Fprintf(app.Stream.Out, "manifest api versions: %s\n",
				strings.Join(apiVersionStrings(), ", "))
			return nil
		},
	}
}

// parseComponents validates --component values, naming what is accepted rather
// than silently ignoring a typo.
func parseComponents(names []string) ([]ports.Component, error) {
	if len(names) == 0 {
		return nil, nil
	}

	valid := map[string]ports.Component{}
	for _, c := range ports.AllComponents {
		valid[string(c)] = c
	}

	out := make([]ports.Component, 0, len(names))
	for _, name := range names {
		c, ok := valid[strings.TrimSpace(strings.ToLower(name))]
		if !ok {
			known := make([]string, 0, len(valid))
			for k := range valid {
				known = append(known, k)
			}
			sort.Strings(known)
			return nil, domain.Usage("unknown backup component %q", name).
				WithHint("valid components: %s", strings.Join(known, ", "))
		}
		out = append(out, c)
	}
	return out, nil
}

// readSecretValue reads a secret without echoing it and without putting it in
// argv.
//
// stdin is the only supported channel. A --value flag would place the
// credential in the process table, where any local user can read it.
func (a *App) readSecretValue(ctx context.Context, prompt string) (domain.Secret, error) {
	// Not a terminal means piped: read it whole and strip exactly one
	// trailing newline, which is what `echo secret | morzer secret set x`
	// produces.
	//
	// The question used to be asked of `os.Stdin` directly, by way of its
	// mode bits. Asking the App's own reader answers the same question and
	// gives the piped path -- the one every script takes -- something that
	// can drive it.
	if !ui.IsTerminal(a.Stream.In) {
		data, err := readAll(a.Stream.In)
		if err != nil {
			return domain.Secret{}, err
		}
		return domain.NewSecret(strings.TrimSuffix(string(data), "\n")), nil
	}

	value, err := readPassword(ctx, a.Stream.In, a.Stream.Err, prompt)
	if err != nil {
		return domain.Secret{}, err
	}
	return domain.NewSecret(value), nil
}

// readAll reads a piped secret, bounded.
//
// The bound is not politeness: without it, `morzer secret set x < /dev/zero`
// is the manager filling its own memory. A secret larger than a megabyte is
// not a secret.
func readAll(r io.Reader) ([]byte, error) {
	const max = 1 << 20

	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, domain.Internal(err, "cannot read the value")
	}
	if len(data) > max {
		return nil, domain.Usage("the value on stdin is unreasonably large")
	}
	return data, nil
}
