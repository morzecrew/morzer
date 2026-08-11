package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/internal/ui/views"
)

// newListCommand builds the machine listing, which is registered twice.
//
// `morzer ls` is what somebody types after logging into a machine they do not
// know; `morzer installation list` is where the noun hierarchy puts it. Both,
// because the cost is this parameter -- and one constructor rather than a cobra
// alias because an alias is a second name at the *same* level, and the whole
// point of the short one is that it is at the top.
func newListCommand(app *App, use string) *cobra.Command {
	var status bool

	cmd := machineScope(&cobra.Command{
		Use:   use,
		Short: "List the installations on this machine",
		Long: "A machine may hold several installations: every path, every systemd\n" +
			"unit and every lock is keyed by product name, so two of them never\n" +
			"share anything the manager owns.\n\n" +
			"Read from the state files alone — no Docker call, no lock, no network —\n" +
			"so it answers on a machine whose daemon is down, which is when somebody\n" +
			"is usually asking what is on the box. --status adds what is running, at\n" +
			"the cost of one runtime query per installation.\n\n" +
			"An installation whose state will not load is listed with the reason\n" +
			"rather than left out: the moment its state breaks is the moment it must\n" +
			"not look absent. Each installation is then operated on its own terms —\n" +
			"there is no --all, because three deployments have three releases,\n" +
			"three gates and three windows of downtime.",
		Example: "  morzer ls\n" +
			"  morzer ls --status\n" +
			"  morzer ls --json | jq -r '.data[] | select(.problem) | .product'",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			entries, err := ops.ListInstallations(cmd.Context(), app.Deps,
				ops.ListOptions{Status: status})
			if err != nil {
				return err
			}
			// --status selects a second view of the same listing,
			// the way `doctor --verbose` does: the extra column is a
			// presentation of what was asked for, and `--json`
			// carries the same array either way -- with a `services`
			// key on each row when it was asked for, absent when it
			// was not.
			if status {
				return app.render(views.WithServices(entries))
			}
			return app.render(entries)
		},
	})

	cmd.Flags().BoolVar(&status, "status", false,
		"ask each installation's runtime what is running (one Docker call per row)")

	return cmd
}

func newInstallationCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "installation",
		Short: "List, export and rebuild the installations on this machine",
		Long: "A machine may hold several installations, keyed by product name. `list`\n" +
			"says which — `morzer ls` is the same command, at the top level where\n" +
			"somebody on an unfamiliar host will look for it.\n\n" +
			"An installation export carries the identity of a deployment and its\n" +
			"encrypted secret state, so a lost machine can be rebuilt from an offline\n" +
			"recovery key. It carries no application data: `morzer backup` owns that.",
	}

	cmd.AddCommand(
		newListCommand(app, "list"),
		// `export` reads this installation and so needs one chosen.
		// `import` chooses its own: the product comes out of the export
		// file, and the graph is rebuilt around it mid-run.
		installationScope(newInstallationExportCommand(app)),
		machineScope(newInstallationImportCommand(app)),
	)
	return cmd
}

func newInstallationExportCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "export <path>",
		Short: "Write the installation identity and encrypted secrets to a file",
		Long: "The export contains the installation record, the encrypted secret state\n" +
			"and the list of who can decrypt it. It contains no plaintext secret and\n" +
			"no application data.\n\n" +
			"Store it somewhere the machine it came from cannot reach. An export kept\n" +
			"only on the host it describes protects nothing.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.runOperation(cmd.Context(), func(ctx context.Context) (ops.Result, error) {
				return ops.Export(ctx, app.Deps, ops.ExportOptions{
					Options: app.operationOptions(),
					Path:    args[0],
				})
			})
		},
	}
}

func newInstallationImportCommand(app *App) *cobra.Command {
	var (
		identity        string
		fromBackup      bool
		targetURL       string
		credentialsFile string
		mode            string
	)

	cmd := &cobra.Command{
		Use:   "import [<path> | --from-backup [<id>]]",
		Short: "Rebuild this machine from an export and an offline recovery key",
		Long: "Restores the installation with its ORIGINAL id, so backups taken by the\n" +
			"machine that was lost remain restorable. Generates a new machine key for\n" +
			"this host and revokes the old one.\n\n" +
			"The identity comes from an export file, or with --from-backup out of a\n" +
			"backup you already have — every backup carries one, encrypted to the\n" +
			"recovery keys alone, so a recovery key and a backup are enough.\n\n" +
			"It does not install a release and does not restore data. The sequence is:\n" +
			"import, then `morzer update <bundle>`, then `morzer restore`.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// With --from-backup the argument is a backup id; without
			// it, the path of an export file. One positional, two
			// meanings, disambiguated by the flag that is already
			// there — which is what keeps `--from-backup` usable with
			// no id at all, the case the design treats as ordinary.
			if !fromBackup && len(args) == 0 {
				return domain.Usage("no export was given").
					WithHint("pass the path to an export file, or --from-backup to " +
						"read the identity out of a backup you already have")
			}
			// Refused rather than ignored. An operator who mistyped
			// this during a recovery has to learn it from the
			// command, not from an import that quietly read a
			// different artifact than the one they named.
			if !fromBackup && (targetURL != "" || credentialsFile != "") {
				return domain.Usage(
					"--target and --credentials-file need --from-backup").
					WithHint("they say which *backup* to read the identity out of; " +
						"an export file is read from the path you gave")
			}
			if credentialsFile != "" && targetURL == "" {
				// An empty URL means "every target the installation
				// configures", and a machine being rebuilt has no
				// installation to read them from -- so this would
				// fail with a message about targets rather than
				// about the flag that is missing.
				return domain.Usage("--credentials-file needs --target").
					WithHint("name the target the credentials are for, e.g. " +
						"--target s3://backups.example/demo")
			}

			if identity == "" {
				return domain.Usage("an offline recovery identity is required").
					WithHint("pass --identity <file>, the private key printed by " +
						"`morzer secret recipients generate-recovery-key`")
			}

			// The export is read before the paths are rebuilt: every
			// managed directory derives from the product name, and
			// on a rebuilt machine the export is the only place it
			// is written down.
			var (
				export domain.InstallationExport
				source string
				notes  []string
			)
			if fromBackup {
				id := ""
				if len(args) == 1 {
					id = args[0]
				}
				found, err := readBackupIdentity(cmd.Context(), app,
					id, identity, targetURL, credentialsFile)
				if err != nil {
					return err
				}
				export, source = found.Export, "backup "+found.Backup.ID
				notes = ops.DescribeBackupExport(found)
			} else {
				var err error
				if export, err = ops.LoadExport(args[0]); err != nil {
					return err
				}
				source = args[0]
			}

			app.Flags.product = export.Installation.Product
			if err := app.rewireForProduct(cmd.Context(), export.Installation.Product); err != nil {
				return err
			}

			parsedMode, err := domain.ParseMode(mode)
			if err != nil {
				return err
			}

			err = app.runOperation(cmd.Context(), func(ctx context.Context) (ops.Result, error) {
				return ops.Import(ctx, app.Deps, ops.ImportOptions{
					Options:      app.operationOptions(),
					SourcePath:   source,
					Export:       export,
					IdentityFile: identity,
					Mode:         parsedMode,
					ModeSet:      cmd.Flags().Changed("mode"),
				})
			})
			if err != nil {
				return err
			}

			// Where the identity came from, on stderr in every mode.
			//
			// Not gated on plain output: the staleness note is a
			// warning, `--json` puts the result on stdout, and a
			// warning an operator loses by asking for machine-
			// readable output is a warning that disappears exactly
			// where a script cannot notice it either.
			if !app.Flags.quiet {
				for _, note := range notes {
					fmt.Fprintf(app.Stream.Err, "%s\n", note)
				}
			}
			if app.json == nil && !app.Flags.quiet {
				printImportNextSteps(app, export)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&identity, "identity", "",
		"private age identity that can decrypt the export")
	cmd.Flags().BoolVar(&fromBackup, "from-backup", false,
		"read the identity out of a backup rather than an export file; "+
			"with no id, the newest backup that carries one")
	cmd.Flags().StringVar(&targetURL, "target", "",
		"read the backup from this target rather than from this machine")
	cmd.Flags().StringVar(&credentialsFile, "credentials-file", "",
		"credentials for --target, when the secret store that held them is gone")
	cmd.Flags().StringVar(&mode, "mode", "",
		"rebuild as a sandbox with `dev`, which also drops the export's backup "+
			"targets. Omitted keeps whatever the export was; a sandbox can never "+
			"be imported as production")
	return cmd
}

// readBackupIdentity gets an export out of a backup, here or on a target.
//
// The remote path exists because of a circle RFC 0009 named: on a rebuilt
// machine the bucket credentials are in the secret state, the secret state is
// in the backup, and the backup is in the bucket. `--credentials-file` is how
// an operator breaks it from outside — and the whole point of fetching one
// named file is that breaking it costs kilobytes rather than the archive.
func readBackupIdentity(
	ctx context.Context, app *App, backupID, identity, targetURL, credentialsFile string,
) (ops.BackupExport, error) {
	if targetURL == "" && credentialsFile == "" {
		return ops.ExportFromBackup(app.Deps.Paths, ops.ExportFromBackupOptions{
			BackupID:     backupID,
			IdentityFile: identity,
		})
	}

	creds, err := readCredentialsFile(credentialsFile)
	if err != nil {
		return ops.BackupExport{}, err
	}
	return ops.ExportFromRemoteBackup(ctx, app.Deps, ops.TargetOptions{
		Options:     app.operationOptions(),
		URL:         targetURL,
		Credentials: creds,
	}, backupID, identity)
}

// printImportNextSteps says what an operator must do next, and the one thing
// they must not forget.
//
// A recovery is the moment an operator is least able to reconstruct a sequence
// from documentation, so the sequence is printed where they already are.
func printImportNextSteps(app *App, export domain.InstallationExport) {
	step := "1. morzer update <bundle>   # no release was recorded"
	if !export.Release.IsZero() {
		step = fmt.Sprintf("1. morzer update <bundle>   # %s %s was running",
			export.Release.Name, export.Release.Version)
	}

	app.notice(ui.Callout{
		Title: "next",
		Body: []string{
			fmt.Sprintf("The installation id %s was assumed from %s. Decommission "+
				"that machine: two live hosts sharing an installation id will "+
				"confuse every backup you take.", export.Installation.ID, hostLabel(export)),
			step,
			fmt.Sprintf("2. morzer restore --force --confirm %s", export.Installation.ID),
			"3. morzer doctor",
		},
	})
}

func hostLabel(export domain.InstallationExport) string {
	if export.SourceHost == "" {
		return "the exported installation"
	}
	return export.SourceHost
}
