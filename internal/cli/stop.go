package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

func newStopCommand(app *App) *cobra.Command {
	var reason string

	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Take the product down and keep it down until it is started",
		Long: "Stops every service and records the stop, so the product stays down\n" +
			"across a reboot and an unattended update until `morzer start`.\n\n" +
			"Containers, networks and volumes are kept: nothing here can remove a\n" +
			"volume. While stopped, `apply`, `update`, `rollback` and `task run` are\n" +
			"refused, the update timer stages without installing, and the scheduled\n" +
			"backup is skipped. `restore` and an explicit `backup` still run.\n\n" +
			"Docs: https://morzecrew.github.io/morzer/latest/operating/taking-a-product-down/",
		Example: "  morzer stop --reason \"moving to a new disk\"\n" +
			"  morzer start",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := ops.StopOptions{Options: app.operationOptions(), Reason: reason}
			return app.runOperation(cmd.Context(), func(ctx context.Context) (ops.Result, error) {
				return ops.Stop(ctx, app.Deps, opts)
			})
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "",
		"why, in one line; shown by status, doctor and ls until morzer start")
	return cmd
}

func newStartCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Bring a stopped product back",
		Long: "Applies the current release and then clears the stop, so a start that\n" +
			"fails keeps boot and the update timer away until it succeeds.\n\n" +
			"A release staged while the product was stopped is not installed here;\n" +
			"`morzer update` or the next tick of the update timer installs it. On a\n" +
			"product that is not stopped, this is `morzer apply`.\n\n" +
			"Docs: https://morzecrew.github.io/morzer/latest/operating/taking-a-product-down/",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := app.operationOptions()
			return app.runOperation(cmd.Context(), func(ctx context.Context) (ops.Result, error) {
				return ops.Start(ctx, app.Deps, opts)
			})
		},
	}
}
