package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui"
)

// newConfigApplyCommand applies a change document as one operation (RFC
// 0056).
func newConfigApplyCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "apply",
		Short: "Change settings, parameters and secrets together, as one operation",
		Long: "Reads a change document on stdin and applies it as one operation: one\n" +
			"record, one render, one re-create of what it touches, and the release's\n" +
			"health checks. If a step fails, every value in it is put back.\n\n" +
			"The document has settings, parameters and secrets to set, and an unset\n" +
			"block. It is read from stdin only, never from argv, so a secret value in\n" +
			"it never reaches the process table.\n\n" +
			"Not `morzer apply`, which converges the installation as it is recorded.",
		Example: "  morzer config apply < switch-to-openvpn.yaml\n" +
			"  morzer config apply --dry-run < switch-to-openvpn.yaml",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// A document is not typed at a prompt, and reading would wait
			// for an end of file nobody sends.
			if ui.IsTerminal(app.Stream.In) {
				return domain.Usage("config apply reads the change document from stdin").
					WithHint("redirect or pipe it: `morzer config apply < change.yaml`")
			}
			data, err := readAllCtx(cmd.Context(), app.Stream.In)
			if err != nil {
				return err
			}
			change, err := ops.ParseConfigChange(data)
			if err != nil {
				return err
			}
			return app.runOperation(cmd.Context(), func(ctx context.Context) (ops.Result, error) {
				return ops.ConfigApply(ctx, app.Deps, ops.ConfigApplyOptions{
					Options: app.operationOptions(),
					Change:  change,
				})
			})
		},
	}
}
