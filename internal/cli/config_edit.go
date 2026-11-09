package cli

import (
	"context"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui"
)

// parameterUnchanged is what an aborted parameter edit tells the operator.
const parameterUnchanged = "nothing was changed"

func newConfigEditCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "edit <name>",
		Short: "Edit a text parameter in $VISUAL or $EDITOR",
		Long: "Opens the parameter's value as plain text, in $VISUAL or $EDITOR, and sets\n" +
			"what you save the way `config set` does: recorded, and the services the\n" +
			"release says depend on it re-created.\n\n" +
			"Only a parameter of type `text`, one at a time. Saving the file unchanged\n" +
			"changes nothing, and so does emptying it.\n\n" +
			"Without a terminal, use `morzer config set --set-file <name>=<path>`.",
		Example:           "  morzer config edit rules",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: onlyFirst(app.completeValues(textParameterNames)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.editParameter(cmd.Context(), args[0])
		},
	}
}

func (a *App) editParameter(ctx context.Context, name string) error {
	if !ui.IsTerminal(a.Stream.In) {
		return domain.Usage("`config edit` needs a terminal").
			WithHint("`morzer config set --set-file %s=<path>` reads the value from a file, "+
				"or from standard input with `-`", name)
	}

	editor, err := editorCommand()
	if err != nil {
		return domain.AsError(err).
			WithHint("set $EDITOR or $VISUAL, or use `morzer config set --set-file %s=<path>`", name)
	}
	return a.editParameterWith(ctx, name, editor, a.setParameter)
}

// setParameter records one value through the `config set` operation: locked,
// planned under --dry-run, journaled, re-creating the declared services.
func (a *App) setParameter(ctx context.Context, name, value string) error {
	return a.runOperation(ctx, func(ctx context.Context) (ops.Result, error) {
		return ops.ConfigSet(ctx, a.Deps, ops.ConfigSetOptions{
			Options: a.operationOptions(),
			Set:     map[string]string{name: value},
		})
	})
}

// editParameterWith is the session itself, with the editor and what records a
// saved value already chosen, so a test can drive it with a scripted editor
// (RFC 0054 §5.3).
//
// The file holds the value alone, as it is delivered, plus a final newline. A
// mapping of name to value would need a block scalar, and the obvious edit, a
// second line under the first, folds into one (#301).
func (a *App) editParameterWith(
	ctx context.Context, name string, editor []string, set func(ctx context.Context, name, value string) error,
) error {
	// The effective value, parsed: the recorded one or the release's default.
	entry, err := ops.ConfigGet(ctx, a.Deps, name)
	if err != nil {
		return err
	}
	if entry.Type != domain.ParamText {
		return domain.Usage("`config edit` opens text parameters, and %s is a %s parameter",
			name, entry.Type).
			WithHint("set it with `morzer config set %s=<value>`", name)
	}

	// Not on tmpfs, unlike `secret edit`: the value is not secret. A
	// directory of its own rather than a bare file, removed however the
	// editor exits, because editors leave swap and backup files beside the
	// one they were given.
	dir, err := os.MkdirTemp("", "morzer-config-edit-")
	if err != nil {
		return domain.InstallationError(err, "cannot create a directory to edit %s in", name)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	path := filepath.Join(dir, name)
	content := entry.Value
	if content != "" {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return domain.InstallationError(err, "cannot write %s to edit", name)
	}

	if err := runEditor(ctx, editor, path, parameterUnchanged); err != nil {
		return err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return domain.Usage("the edited file is gone; %s", parameterUnchanged).
			WithHint("some editors write to a new file and rename; if yours does, "+
				"configure it not to, or use `morzer config set --set-file %s=<path>`", name)
	}

	value, err := domain.ParameterSpec{Type: domain.ParamText}.Parse(string(data))
	if err != nil {
		refused := domain.AsError(err)
		return domain.ValidationError(err, "parameter %q was not changed: %s", name, refused.Message).
			WithHint("%s", refused.Hint)
	}

	switch value {
	case entry.Value:
		a.finish(ops.Result{Summary: "no change: " + name + " already matches"})
		return nil

	// As in `secret edit` (RFC 0003 decision 20): an emptied file is more
	// often a slip than a choice, so it changes nothing, and the hint names
	// what an empty file might have meant -- where that can be done at all.
	// A declaration with no default refuses an empty value as missing.
	case "":
		refusal := domain.Usage("%s was left empty, so %s", name, parameterUnchanged)
		if entry.Default == "" {
			return refusal.WithHint("the release declares no default for %s, so it cannot be left empty", name)
		}
		return refusal.WithHint("`morzer config set %[1]s=` records an empty value, and "+
			"`morzer config unset %[1]s` returns it to the release's default", name)
	}

	return set(ctx, name, value)
}
