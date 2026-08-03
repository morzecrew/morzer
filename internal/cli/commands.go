package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
	"github.com/morzecrew/morzer/internal/ui/plain"
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
			if product == "" {
				return domain.Usage("a product name is required").
					WithHint("pass --product <name>, or --release <bundle> to take it from the manifest")
			}

			// Rebuild the layout now that the name is known. Every
			// managed path derives from it, and the adapters wired
			// during the pre-run captured the placeholder paths --
			// so they are rebuilt too, not merely re-pointed.
			app.Flags.product = product
			if err := app.rewireForProduct(cmd.Context(), product); err != nil {
				return err
			}

			result, err := ops.Init(cmd.Context(), app.Deps, ops.InitOptions{
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
				Repair:            repair,
			})
			app.finish(result)
			return err
		},
	}

	f := cmd.Flags()
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

			result, err := ops.Apply(cmd.Context(), app.Deps, opts)
			app.finish(result)
			return err
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
	)

	cmd := &cobra.Command{
		Use:   "update <bundle>",
		Short: "Install a new release over the current one",
		Long: "Verifies the bundle, checks it against the compatibility the manifest\n" +
			"declares, takes a pre-update backup, stages the release and converges\n" +
			"to it.\n\n" +
			"A failed update rolls back to the release that was running. The database\n" +
			"is never rolled back automatically: when a migration cannot be undone the\n" +
			"release says so, and the answer is a restore from the backup taken here.",
		Args: cobra.ExactArgs(1),
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

			result, err := ops.Update(cmd.Context(), app.Deps, ops.UpdateOptions{
				Options:      opts,
				Ref:          args[0],
				ExpectDigest: digest,
			})
			app.finish(result)
			return err
		},
	}

	f := cmd.Flags()
	f.BoolVar(&skipBackup, "skip-backup", false,
		"skip the pre-update backup; requires --force and is recorded in the journal")
	f.StringVar(&digest, "digest", "",
		"expected bundle content digest; a mismatch refuses the update")
	f.StringVar(&profile, "profile", "", "override the installation's deployment profile")

	return cmd
}

func newStatusCommand(app *App) *cobra.Command {
	var clearIntervention string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show what is deployed and whether it is working",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("clear-intervention") {
				result, err := ops.ClearIntervention(cmd.Context(), app.Deps, clearIntervention)
				app.finish(result)
				return err
			}

			// A missing backup engine must not make status fail:
			// the command has to work on a machine with no release
			// installed.
			_ = app.attachBackupEngine(cmd.Context())

			status, err := ops.GetStatus(cmd.Context(), app.Deps)
			if err != nil {
				return err
			}

			if app.json != nil {
				app.jsonData = status
				return nil
			}
			plain.RenderStatus(app.Stream.Out, status)
			return nil
		},
	}

	cmd.Flags().StringVar(&clearIntervention, "clear-intervention", "",
		"acknowledge a requires-manual-intervention operation (empty selects the only one)")
	cmd.Flags().Lookup("clear-intervention").NoOptDefVal = " "

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

			if app.json != nil {
				app.jsonData = report
			} else {
				// The plain presenter already streamed each
				// check as it ran; the table is the summary.
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
		reason     string
		components []string
		noVerify   bool
		noPrune    bool
	)

	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Back up the database, files, configuration and secret state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.attachBackupEngine(cmd.Context()); err != nil {
				return err
			}

			parsed, err := parseComponents(components)
			if err != nil {
				return err
			}

			result, err := ops.Backup(cmd.Context(), app.Deps, ops.BackupOptions{
				Options:    app.operationOptions(),
				Reason:     reason,
				Components: parsed,
				Verify:     !noVerify,
				Prune:      !noPrune,
			})
			app.finish(result)
			return err
		},
	}

	f := cmd.Flags()
	f.StringVar(&reason, "reason", "manual", "why this backup was taken; recorded in its manifest")
	f.StringSliceVar(&components, "component", nil,
		"limit the backup to these components: database, files, config, secrets, manifest")
	f.BoolVar(&noVerify, "no-verify", false, "skip re-reading the backup to check its checksums")
	f.BoolVar(&noPrune, "no-prune", false, "skip applying the retention policy afterwards")

	cmd.AddCommand(newBackupListCommand(app), newBackupVerifyCommand(app))
	return cmd
}

func newBackupListCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List backups, newest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
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
}

func newBackupVerifyCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "verify [backup-id]",
		Short: "Re-read a backup and check its checksums",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.attachBackupEngine(cmd.Context()); err != nil {
				return err
			}

			id := ""
			if len(args) == 1 {
				id = args[0]
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
}

func newRestoreCommand(app *App) *cobra.Command {
	var (
		backupID   string
		confirm    string
		components []string
	)

	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore from a backup",
		Long: "Verifies the backup, stops writers, restores the database and files,\n" +
			"re-applies the release and runs the smoke test.\n\n" +
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

			result, err := ops.Restore(cmd.Context(), app.Deps, ops.RestoreOptions{
				Options:                 app.operationOptions(),
				BackupID:                backupID,
				Components:              parsed,
				ConfirmedInstallationID: confirm,
			})
			app.finish(result)
			return err
		},
	}

	f := cmd.Flags()
	f.StringVar(&backupID, "backup", "", "backup id; the most recent when omitted")
	f.StringVar(&confirm, "confirm", "", "the installation id, typed to confirm a destructive restore")
	f.StringSliceVar(&components, "component", nil, "limit the restore to these components")

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
func readSecretValue(prompt string) (domain.Secret, error) {
	info, err := os.Stdin.Stat()
	if err != nil {
		return domain.Secret{}, domain.Internal(err, "cannot inspect stdin")
	}

	// Piped input: read it whole and strip exactly one trailing newline,
	// which is what `echo secret | morzer secret set x` produces.
	if info.Mode()&os.ModeCharDevice == 0 {
		data, err := readAll(os.Stdin)
		if err != nil {
			return domain.Secret{}, domain.Internal(err, "cannot read stdin")
		}
		return domain.NewSecret(strings.TrimSuffix(string(data), "\n")), nil
	}

	value, err := readPassword(prompt)
	if err != nil {
		return domain.Secret{}, err
	}
	return domain.NewSecret(value), nil
}

func readAll(f *os.File) ([]byte, error) {
	var buf []byte
	chunk := make([]byte, 4096)
	for {
		n, err := f.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if err != nil {
			if err.Error() == "EOF" {
				return buf, nil
			}
			return buf, err
		}
		// A secret larger than this is not a secret.
		if len(buf) > 1<<20 {
			return nil, domain.Usage("the value on stdin is unreasonably large")
		}
	}
}
