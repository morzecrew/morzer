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
		newConfigSettingsCommand(app),
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
			// The same dispatch `set` makes, and for the same reason:
			// a dotted name is never a parameter, so reporting it as
			// an undeclared one sends the reader to the manifest.
			if ops.IsSettingName(args[0]) {
				setting, err := ops.GetSetting(cmd.Context(), app.Deps, args[0])
				if err != nil {
					return err
				}
				if app.json != nil {
					app.jsonData = setting
					return nil
				}
				fmt.Fprintln(app.Stream.Out, setting.Value)
				return nil
			}

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
		Short: "Change one or more parameters, or an installation setting",
		Long: "Validates each value against what the release declares, records it, and\n" +
			"re-creates the services the release says depend on it.\n\n" +
			"Re-creates rather than restarts: a published port is fixed when a\n" +
			"container is created, so restarting one would report success and leave\n" +
			"the old port in place.\n\n" +
			"A parameter that declares no dependent services is recorded and takes\n" +
			"effect on the next `apply`, which the summary says.\n\n" +
			"A dotted name is an installation setting rather than a release\n" +
			"parameter — `update.check`, `update.channel`. Those are the operator's\n" +
			"arrangement with the manager, change nothing that is running, and are\n" +
			"listed by `morzer config settings`.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			set, err := domain.ParseAssignments(args)
			if err != nil {
				return err
			}
			settings, parameters := splitSettings(set)
			// Refused rather than run as two operations. They have
			// different machinery -- one converges a deployment, one
			// writes a flag -- so a mixed command would half-apply on
			// a failure with no single thing to report.
			if len(settings) > 0 && len(parameters) > 0 {
				return domain.Usage(
					"parameters and installation settings are set separately").
					WithHint("a setting changes what the manager does; a parameter " +
						"changes what the deployment runs, and re-creates services " +
						"to do it")
			}
			if len(settings) > 0 {
				return app.runOperation(cmd.Context(), func(ctx context.Context) (ops.Result, error) {
					return ops.SetSettings(ctx, app.Deps, ops.SetSettingsOptions{
						Options: app.operationOptions(),
						Set:     settings,
					})
				})
			}
			return app.runOperation(cmd.Context(), func(ctx context.Context) (ops.Result, error) {
				return ops.ConfigSet(ctx, app.Deps, ops.ConfigSetOptions{
					Options: app.operationOptions(),
					Set:     parameters,
				})
			})
		},
	}
	return cmd
}

func newConfigUnsetCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "unset <name> [name ...]",
		Short: "Return parameters or a setting to their defaults",
		Long: "Removes the recorded values, so the release's own defaults apply again,\n" +
			"and re-creates the dependent services.\n\n" +
			"This is also how a value left behind by an older release is cleared:\n" +
			"`config list` reports those as stale.\n\n" +
			"Unsetting an installation setting returns it to its absent state, which\n" +
			"is always the conservative one: nothing here defaults to on.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var settings, parameters []string
			for _, name := range args {
				if ops.IsSettingName(name) {
					settings = append(settings, name)
					continue
				}
				parameters = append(parameters, name)
			}
			if len(settings) > 0 && len(parameters) > 0 {
				return domain.Usage(
					"parameters and installation settings are cleared separately")
			}
			if len(settings) > 0 {
				return app.runOperation(cmd.Context(), func(ctx context.Context) (ops.Result, error) {
					return ops.SetSettings(ctx, app.Deps, ops.SetSettingsOptions{
						Options: app.operationOptions(),
						Unset:   settings,
					})
				})
			}
			return app.runOperation(cmd.Context(), func(ctx context.Context) (ops.Result, error) {
				return ops.ConfigSet(ctx, app.Deps, ops.ConfigSetOptions{
					Options: app.operationOptions(),
					Unset:   parameters,
				})
			})
		},
	}
}

// newConfigSettingsCommand lists the installation's own knobs.
//
// A separate subcommand rather than a section of `config list`, because the two
// answer different questions: `list` is "what does this release expose", and a
// vendor's parameters would bury two manager settings at the bottom of it. It is
// also where an operator looks after reading a refusal that names one.
func newConfigSettingsCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "settings",
		Short: "Show the installation settings and their values",
		Long: "Installation settings are the operator's arrangement with the manager,\n" +
			"as opposed to the parameters a release declares. They change nothing\n" +
			"that is running.\n\n" +
			"Set one with `morzer config set update.check=true`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := ops.ListSettings(cmd.Context(), app.Deps)
			if err != nil {
				return err
			}
			if app.json != nil {
				app.jsonData = report
				return nil
			}
			fmt.Fprintln(app.Stream.Out, ops.DescribeSettings(report))
			return nil
		},
	}
}

// splitSettings separates installation settings from release parameters.
//
// An unknown dotted name goes to the settings side deliberately. It can never be
// a parameter -- parameter names carry no dots -- so reporting it as an
// undeclared parameter would send an operator to the manifest to look for
// something that was never going to be there.
func splitSettings(assignments map[string]string) (settings, parameters map[string]string) {
	settings, parameters = map[string]string{}, map[string]string{}
	for name, value := range assignments {
		if ops.IsSettingName(name) {
			settings[name] = value
			continue
		}
		parameters[name] = value
	}
	return settings, parameters
}
