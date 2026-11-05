package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

func newRestartCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "restart <service>...",
		Short: "Re-create services from the installed release",
		Long: "Re-creates the named services from the installed release, forced, and\n" +
			"waits for the release's health checks. A container changed by hand comes\n" +
			"back as the release defines it, and its writable layer does not survive,\n" +
			"as with any re-create `apply` does. A dependency that is down is started,\n" +
			"never re-created.\n\n" +
			"Journalled as a `restart` operation, under the deployment lock, and\n" +
			"refused while the product is stopped. A service the release does not\n" +
			"start, including one behind an inactive profile, is refused by name.\n\n" +
			"To restart everything, `morzer stop` and `morzer start`.\n\n" +
			"Docs: https://morzecrew.github.io/morzer/latest/operating/looking-inside/",
		Example: "  morzer restart app\n" +
			"  morzer restart app worker",
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: unnamed(app.completeValues(services)),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := ops.RestartOptions{Options: app.operationOptions(), Services: args}
			return app.runOperation(cmd.Context(), func(ctx context.Context) (ops.Result, error) {
				return ops.Restart(ctx, app.Deps, opts)
			})
		},
	}
}
