package cli

import (
	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// `morzer support bundle` — RFC 0024 P2.
//
// A verb of its own rather than `doctor --support-bundle`, which decision 1
// locks. `doctor`'s contract is "report on this machine, now": it takes no
// lock, writes nothing, and its output is a view. A support bundle has
// retention, a redaction policy, encryption recipients and a signature ahead of
// it, and hanging four new semantics off a diagnostic flag makes `doctor`'s
// contract a sentence with an exception in it. The same reasoning gave `release
// pack` its own verb rather than a flag on `build`.

func newSupportCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "support",
		Short: "Produce evidence somebody else can read",
		Long: "The manager already holds, in structured form, everything somebody\n" +
			"debugging this installation would ask for: the journal, `doctor`'s\n" +
			"results, the resolved manifest, configuration drift, the version\n" +
			"history and what each service is doing. What it has never had is an\n" +
			"export.\n\n" +
			"`bundle` is that export — one archive, safe to hand to a stranger,\n" +
			"whether that stranger is your vendor or a forum. What it never\n" +
			"contains is enumerated in the reference documentation and enforced by\n" +
			"the build, not promised in prose.",
	}
	cmd.AddCommand(installationScope(newSupportBundleCommand(app)))
	return cmd
}

func newSupportBundleCommand(app *App) *cobra.Command {
	var (
		preview bool
		noLogs  bool
		dir     string
	)

	cmd := &cobra.Command{
		Use:   "bundle",
		Short: "Write one archive describing this installation, safe to send",
		Long: "Collects what this installation is and what it did into a single\n" +
			"`.tar.zst` in the current directory.\n\n" +
			"It holds no age identity, no secret ciphertext, no signing key, no\n" +
			"backup credentials and nothing from the directory where secrets are\n" +
			"rendered. That list is not a promise in this help text: inclusion is\n" +
			"an allowlist in the code, the reference page is generated from it, and\n" +
			"a component cannot start being collected without a row appearing\n" +
			"there.\n\n" +
			"`--preview` writes nothing and prints exactly what would be\n" +
			"collected, with per-file sizes. An operator who cannot see what\n" +
			"leaves will either send nothing or send everything, and both are\n" +
			"failures of this command.\n\n" +
			"`--no-logs` leaves your containers' output out. It is not a way to\n" +
			"turn redaction off -- there is no such flag, deliberately -- it removes\n" +
			"a component rather than removing the filter from it, so it can only\n" +
			"ever send less.\n\n" +
			"The archive is plaintext today and says so. Encrypting it to a\n" +
			"vendor's recipients is RFC 0024 P4.",
		Example: "  morzer support bundle --preview\n" +
			"  morzer support bundle\n" +
			"  morzer support bundle --json | jq -e '.data.entries[].redactions'",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := ops.SupportBundle(cmd.Context(), app.Deps, ops.SupportOptions{
				Preview: preview,
				NoLogs:  noLogs,
				Dir:     dir,
			})
			if err != nil {
				return err
			}
			return app.render(report)
		},
	}

	cmd.Flags().BoolVar(&preview, "preview", false,
		"print what would be collected and write nothing")
	cmd.Flags().BoolVar(&noLogs, "no-logs", false,
		"leave container logs out of the archive")
	cmd.Flags().StringVar(&dir, "dir", "",
		"write the archive to this directory instead of the working directory")
	return cmd
}
