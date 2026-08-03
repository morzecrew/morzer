package cli

import (
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
			result, err := ops.Export(cmd.Context(), app.Deps, ops.ExportOptions{
				Options: app.operationOptions(),
				Path:    args[0],
			})
			app.finish(result)
			return err
		},
	}
}

func newInstallationImportCommand(app *App) *cobra.Command {
	var identity string

	cmd := &cobra.Command{
		Use:   "import <path>",
		Short: "Rebuild this machine from an export and an offline recovery key",
		Long: "Restores the installation with its ORIGINAL id, so backups taken by the\n" +
			"machine that was lost remain restorable. Generates a new machine key for\n" +
			"this host and revokes the old one.\n\n" +
			"It does not install a release and does not restore data. The sequence is:\n" +
			"import, then `morzer update <bundle>`, then `morzer restore`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if identity == "" {
				return domain.Usage("an offline recovery identity is required").
					WithHint("pass --identity <file>, the private key printed by " +
						"`morzer secret recipients generate-recovery-key`")
			}

			// The export is read before the paths are built: every
			// managed directory derives from the product name, and
			// on a rebuilt machine this file is the only place it is
			// written down.
			export, err := ops.LoadExport(args[0])
			if err != nil {
				return err
			}

			app.Flags.product = export.Installation.Product
			if err := app.rewireForProduct(cmd.Context(), export.Installation.Product); err != nil {
				return err
			}

			result, err := ops.Import(cmd.Context(), app.Deps, ops.ImportOptions{
				Options:      app.operationOptions(),
				SourcePath:   args[0],
				Export:       export,
				IdentityFile: identity,
			})
			app.finish(result)
			if err == nil && app.json == nil && !app.Flags.quiet {
				printImportNextSteps(app, export)
			}
			return err
		},
	}

	cmd.Flags().StringVar(&identity, "identity", "",
		"private age identity that can decrypt the export")
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
