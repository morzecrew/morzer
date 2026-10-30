package cli

import (
	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// newNotifyCommand builds `morzer notify`.
//
// Targets are configured here rather than in `config`, for the reason backup
// targets are (RFC 0015 decision 2): no vendor declares where an operator's
// alerts go, and the value is a URL with a credential behind it.
func newNotifyCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notify",
		Short: "Manage where operation outcomes are reported",
		Long: "Where this deployment reports what happened with nobody watching: the\n" +
			"outcome of every operation, a release staged by the update timer, and\n" +
			"doctor's findings. Off until a target is added.\n\n" +
			"Docs: https://morzecrew.github.io/morzer/latest/operating/notifications/",
	}
	target := &cobra.Command{
		Use:   "target",
		Short: "Manage the webhooks this deployment reports to",
		Long: "A target is an https webhook that receives each operation's outcome,\n" +
			"news that a release was staged, and doctor's findings at or above its\n" +
			"minimum level. The payload is morzer's event JSON.\n\n" +
			"Delivery is at most once: a target that is down when an operation\n" +
			"finishes is not told, and the operation's outcome never depends on it.\n" +
			"The journal is the record; a notification is how somebody learns to\n" +
			"read it.",
	}
	target.AddCommand(
		newNotifyTargetAddCommand(app),
		newNotifyTargetRemoveCommand(app),
		newNotifyTargetListCommand(app),
	)
	cmd.AddCommand(target)
	return cmd
}

func newNotifyTargetAddCommand(app *App) *cobra.Command {
	var cfg domain.NotifyTargetConfig

	cmd := &cobra.Command{
		Use:   "add [url]",
		Short: "Report to a webhook",
		Long: "Adds a target after checking what it names: an https URL, and any\n" +
			"secret it reads, set and well formed. Nothing is sent to it.\n\n" +
			"When the URL itself is a credential, as a Slack or Teams incoming\n" +
			"webhook is, keep it in a secret and name that instead, with a name to\n" +
			"identify the target by:\n\n" +
			"    morzer secret set chat_webhook\n" +
			"    morzer notify target add --name chat --url-secret chat_webhook\n\n" +
			"--credentials names a secret holding a header to send, a small YAML\n" +
			"document:\n\n" +
			"    header: Authorization\n" +
			"    value: Bearer ...",
		Example: "  morzer notify target add https://hooks.example/morzer\n" +
			"  morzer notify target add --name chat --url-secret chat_webhook --min-level warn",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				cfg.URL = args[0]
			}
			result, err := ops.NotifyTargetAdd(cmd.Context(), app.Deps, ops.NotifyTargetAddOptions{
				Options: app.operationOptions(),
				Target:  cfg,
			})
			if err != nil {
				return err
			}
			app.finish(result)
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&cfg.Name, "name", "", "what diagnostics call this target; required with --url-secret")
	f.StringVar(&cfg.URLSecret, "url-secret", "", "name of a secret holding the whole URL, in place of [url]")
	f.StringVar(&cfg.Credentials, "credentials", "", "name of a secret holding a header to send")
	f.StringVar(&cfg.MinLevel, "min-level", "", "lowest doctor finding sent here: warn or error (default error)")
	return cmd
}

func newNotifyTargetRemoveCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <url|name>",
		Short: "Stop reporting to a webhook",
		Long: "Names the target by its URL, or by its name: a target whose URL is in\n" +
			"a secret has only its name.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := ops.NotifyTargetRemove(cmd.Context(), app.Deps, app.operationOptions(), args[0])
			if err != nil {
				return err
			}
			app.finish(result)
			return nil
		},
	}
}

func newNotifyTargetListCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the webhooks this deployment reports to",
		Long:  "Every configured target, named without any value read from a secret.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			targets, err := ops.NotifyTargetList(cmd.Context(), app.Deps)
			if err != nil {
				return err
			}
			return app.render(targets)
		},
	}
}
