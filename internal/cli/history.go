package cli

import (
	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// defaultHistoryLimit is one screen of a timeline. The journal is never pruned,
// so an installation's whole history only grows.
const defaultHistoryLimit = 20

func newHistoryCommand(app *App) *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "history [operation-id]",
		Short: "List the operations this installation ran, newest first",
		Long: "Reads the operation journal: every apply, update, rollback, backup,\n" +
			"restore, task and the rest, with what each did to the installed\n" +
			"release and how it ended. Given an operation's id, shows that one\n" +
			"with its steps, the flags it ran with and its error.\n\n" +
			"Reads the journal and nothing else, so it answers on a machine whose\n" +
			"Docker daemon is down. `--json` carries the journal's own records.",
		Example: "  morzer history\n" +
			"  morzer history op_01M6889EFABD5SS2M3M97KYTTF\n" +
			"  morzer history --limit 0 --json | jq -r '.data.operations[] | select(.status != \"succeeded\") | .id'",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: onlyFirst(app.completeValues(operations)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				rec, err := ops.ShowOperation(cmd.Context(), app.Deps, args[0])
				if err != nil {
					return err
				}
				return app.render(rec)
			}
			if limit < 0 {
				return domain.Usage("--limit must be 0 or more, not %d", limit)
			}
			history, err := ops.ListHistory(cmd.Context(), app.Deps, limit)
			if err != nil {
				return err
			}
			return app.render(history)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", defaultHistoryLimit, "list at most this many operations; 0 lists them all")
	return cmd
}
