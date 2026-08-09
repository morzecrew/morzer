package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

func newInstallationCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "installation",
		Short: "Export and rebuild an installation's identity",
		Long: "An installation export carries the identity of a deployment and its\n" +
			"encrypted secret state, so a lost machine can be rebuilt from an offline\n" +
			"recovery key. It carries no application data: `morzer backup` owns that.",
	}

	cmd.AddCommand(
		newInstallationExportCommand(app),
		newInstallationImportCommand(app),
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
		identity   string
		fromBackup bool
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
				found, err := ops.ExportFromBackup(app.Deps.Paths,
					ops.ExportFromBackupOptions{
						BackupID:     id,
						IdentityFile: identity,
					})
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

			err := app.runOperation(cmd.Context(), func(ctx context.Context) (ops.Result, error) {
				return ops.Import(ctx, app.Deps, ops.ImportOptions{
					Options:      app.operationOptions(),
					SourcePath:   source,
					Export:       export,
					IdentityFile: identity,
				})
			})
			if err == nil && app.json == nil && !app.Flags.quiet {
				// Where the identity came from, before the next
				// steps: an operator who imported the wrong
				// backup's identity has to learn it here rather
				// than from a product that will not start.
				for _, note := range notes {
					fmt.Fprintf(app.Stream.Err, "%s\n", note)
				}
				printImportNextSteps(app, export)
			}
			return err
		},
	}

	cmd.Flags().StringVar(&identity, "identity", "",
		"private age identity that can decrypt the export")
	cmd.Flags().BoolVar(&fromBackup, "from-backup", false,
		"read the identity out of a backup rather than an export file; "+
			"with no id, the newest backup that carries one")
	return cmd
}

// printImportNextSteps says what an operator must do next, and the one thing
// they must not forget.
//
// A recovery is the moment an operator is least able to reconstruct a sequence
// from documentation, so the sequence is printed where they already are.
func printImportNextSteps(app *App, export domain.InstallationExport) {
	out := app.Stream.Err

	fmt.Fprintf(out, "\nthe installation id %s was assumed from %s.\n",
		export.Installation.ID, hostLabel(export))
	fmt.Fprintf(out, "decommission that machine: two live hosts sharing an "+
		"installation id will confuse every backup you take.\n")

	fmt.Fprintf(out, "\nnext:\n")
	if !export.Release.IsZero() {
		fmt.Fprintf(out, "  1. morzer update <bundle>   # %s %s was running\n",
			export.Release.Name, export.Release.Version)
	} else {
		fmt.Fprintf(out, "  1. morzer update <bundle>   # no release was recorded\n")
	}
	fmt.Fprintf(out, "  2. morzer restore --force --confirm %s\n", export.Installation.ID)
	fmt.Fprintf(out, "  3. morzer doctor\n")
}

func hostLabel(export domain.InstallationExport) string {
	if export.SourceHost == "" {
		return "the exported installation"
	}
	return export.SourceHost
}
