package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui/plain"
	"github.com/morzecrew/morzer/internal/ui/tty"
)

func newConfigCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Read and change the release parameters",
		Long: "A release declares which parameters exist, what each accepts and which\n" +
			"services depend on it. This reads and changes the values for this\n" +
			"installation.\n\n" +
			"Parameters are not secrets: their values are visible in `docker inspect`,\n" +
			"in `status --json` and in the journal. Use `morzer secret` for anything\n" +
			"that must not be.",
	}
	cmd.AddCommand(
		newConfigListCommand(app),
		newConfigGetCommand(app),
		newConfigSetCommand(app),
		newConfigUnsetCommand(app),
	)
	return cmd
}

func newConfigListCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "Show every parameter the release declares",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := ops.ConfigList(cmd.Context(), app.Deps)
			if err != nil {
				return err
			}

			switch {
			case app.json != nil:
				app.jsonData = report
			case app.rich():
				tty.RenderConfig(app.Stream.Out, app.theme(), report)
			default:
				plain.RenderConfig(app.Stream.Out, report)
			}
			return nil
		},
	}
}

func newConfigGetCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "get <name>",
		Short: "Print one parameter's effective value",
		Long: "Prints the value alone, so it can be used directly:\n\n" +
			"    port=$(morzer config get http_port)",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			entry, err := ops.ConfigGet(cmd.Context(), app.Deps, args[0])
			if err != nil {
				return err
			}
			if app.json != nil {
				app.jsonData = entry
				return nil
			}
			// The value alone on stdout: this is the form a script
			// substitutes, and decoration would break every one.
			fmt.Fprintln(app.Stream.Out, entry.Value)
			return nil
		},
	}
}

func newConfigSetCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <name=value> [name=value ...]",
		Short: "Change one or more parameters",
		Long: "Validates each value against what the release declares, records it, and\n" +
			"re-creates the services the release says depend on it.\n\n" +
			"Re-creates rather than restarts: a published port is fixed when a\n" +
			"container is created, so restarting one would report success and leave\n" +
			"the old port in place.\n\n" +
			"A parameter that declares no dependent services is recorded and takes\n" +
			"effect on the next `apply`, which the summary says.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			set, err := domain.ParseAssignments(args)
			if err != nil {
				return err
			}
			return app.runOperation(cmd.Context(), func(ctx context.Context) (ops.Result, error) {
				return ops.ConfigSet(ctx, app.Deps, ops.ConfigSetOptions{
					Options: app.operationOptions(),
					Set:     set,
				})
			})
		},
	}
	return cmd
}

func newConfigUnsetCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "unset <name> [name ...]",
		Short: "Return parameters to the release's defaults",
		Long: "Removes the recorded values, so the release's own defaults apply again,\n" +
			"and re-creates the dependent services.\n\n" +
			"This is also how a value left behind by an older release is cleared:\n" +
			"`config list` reports those as stale.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.runOperation(cmd.Context(), func(ctx context.Context) (ops.Result, error) {
				return ops.ConfigSet(ctx, app.Deps, ops.ConfigSetOptions{
					Options: app.operationOptions(),
					Unset:   args,
				})
			})
		},
	}
}
