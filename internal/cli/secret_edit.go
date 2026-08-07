package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui"
)

// editFilePrelude is what the operator sees above their secrets.
//
// It states the two things they cannot see for themselves: that the file is
// temporary and on tmpfs, and that deleting a line deletes a secret. The rest
// of the file is theirs.
const editFilePrelude = `# morzer secret edit
#
# Values are plaintext here and re-encrypted when you save and exit. This file
# lives on tmpfs and is overwritten and removed when your editor exits, however
# it exits.
#
#   change a value   edit it
#   add a secret     add a line
#   remove a secret  delete its line
#
# Leaving without changes changes nothing. Only the services that declare a
# dependency on a secret you changed are restarted.
`

func newSecretEditCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "edit [name...]",
		Short: "Edit several secrets in one editor session",
		Long: "Decrypts into a temporary file on tmpfs, opens $VISUAL or $EDITOR, and\n" +
			"re-encrypts what changed. Rotating a related group of credentials is one\n" +
			"logical change, and doing it as several `secret set` calls is several\n" +
			"decrypt-modify-encrypt cycles and several chances to stop halfway.\n\n" +
			"Named secrets only, or all of them when no name is given.\n\n" +
			"The editor never sees the encryption metadata — only a plain name/value\n" +
			"mapping — so the envelope cannot be corrupted by editing it.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.editSecrets(cmd.Context(), args)
		},
	}
}

func (a *App) editSecrets(ctx context.Context, names []string) error {
	if !ui.IsTerminal(a.Stream.In) {
		return domain.Usage("`secret edit` needs a terminal").
			WithHint("there is no sensible non-interactive form of an editor session; " +
				"use `morzer secret set <name>`, which reads from stdin")
	}

	editor, err := editorCommand()
	if err != nil {
		return err
	}
	return a.editSecretsWith(ctx, names, editor)
}

// editSecretsWith is the session itself, with the editor already chosen.
//
// Separate from editSecrets so a test can drive it with a scripted editor:
// everything interesting here -- the diff, the refusals, whether the plaintext
// is gone afterwards -- is about what happens around the editor rather than
// about finding one.
func (a *App) editSecretsWith(ctx context.Context, names, editor []string) error {
	before, err := a.secretsToEdit(ctx, names)
	if err != nil {
		return err
	}
	if len(before) == 0 {
		return domain.Usage("there are no secrets to edit").
			WithHint("run `morzer secret set <name>` to create one")
	}

	// Registered before the values reach a file or an editor's argv, so
	// anything that gets logged from here on is scrubbed.
	a.registerSecretValues(before)

	dir, path, err := a.newEditSession()
	if err != nil {
		return err
	}
	// Unconditional: a panic, a signal, a non-zero editor exit and a clean
	// save all reach this. The whole directory goes, not just the file --
	// editors leave swap and backup files beside the one they were given,
	// and those would hold the same plaintext.
	defer func() { _ = atomicfs.RemoveWithOverwrite(dir) }()

	if err := writeEditFile(path, before); err != nil {
		return err
	}

	if err := runEditor(ctx, editor, path); err != nil {
		return err
	}

	after, err := readEditFile(path)
	if err != nil {
		return err
	}
	a.registerSecretValues(after)

	return a.applySecretEdits(ctx, before, after)
}

// secretsToEdit loads the values the session will show.
func (a *App) secretsToEdit(ctx context.Context, names []string) (map[string]string, error) {
	set, err := a.Deps.Secrets.Load(ctx)
	if err != nil {
		return nil, err
	}

	out := map[string]string{}
	if len(names) == 0 {
		for _, name := range set.Names() {
			value, _ := set.Get(name)
			out[name] = value.Reveal()
		}
		return out, nil
	}

	for _, name := range names {
		value, ok := set.Get(name)
		if !ok {
			return nil, domain.SecretsError(domain.ErrSecretNotFound,
				"secret %q does not exist", name).
				WithHint("run `morzer secret list` to see what is defined, "+
					"or `morzer secret set %s` to create it", name)
		}
		out[name] = value.Reveal()
	}
	return out, nil
}

func (a *App) registerSecretValues(values map[string]string) {
	if a.redactor == nil {
		return
	}
	for _, v := range values {
		a.redactor.Register(v)
	}
}

// newEditSession creates the directory the session lives in.
//
// Inside the render directory rather than os.TempDir(): /tmp is frequently not
// tmpfs, and a crash there would leave plaintext on a disk the operator
// believes is clean. A directory of its own rather than a bare file, because an
// editor writes more than the file it was handed.
func (a *App) newEditSession() (dir, path string, err error) {
	renderDir := a.Deps.Paths.SecretsRenderDir()
	if err := atomicfs.MkdirExact(renderDir, 0o700); err != nil {
		return "", "", err
	}

	dir, err = os.MkdirTemp(renderDir, ".edit-")
	if err != nil {
		return "", "", domain.SecretsError(err,
			"cannot create an edit session under %s", renderDir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", "", domain.SecretsError(err, "cannot secure the edit session directory")
	}
	return dir, filepath.Join(dir, "secrets.yaml"), nil
}

// writeEditFile renders the values as a plain mapping.
func writeEditFile(path string, values map[string]string) error {
	// Sorted, so a diff between two sessions is about what changed rather
	// than about map iteration order.
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)

	ordered := yaml.MapSlice{}
	for _, name := range names {
		ordered = append(ordered, yaml.MapItem{Key: name, Value: values[name]})
	}

	body, err := yaml.Marshal(ordered)
	if err != nil {
		return domain.Internal(err, "cannot render the secrets for editing")
	}

	// 0600, not the 0400 rendered secrets carry: the editor has to write it.
	return atomicfs.WriteFile(path, append([]byte(editFilePrelude), body...), 0o600)
}

// readEditFile parses what the editor left behind.
func readEditFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, domain.SecretsError(err,
			"the edited file is gone; no changes were made").
			WithHint("some editors write to a new file and rename; if yours does, " +
				"configure it not to, or use `morzer secret set`")
	}

	var out map[string]string
	if err := yaml.Unmarshal(data, &out); err != nil {
		// Nothing has been written yet, so the state is exactly as it
		// was. Saying so matters: an operator who has just lost an edit
		// needs to know whether they also broke something.
		return nil, domain.SecretsError(err,
			"the edited file is not valid YAML, so nothing was changed").
			WithHint("secrets are a flat `name: value` mapping; " +
				"a value with special characters needs quoting")
	}

	if out == nil {
		out = map[string]string{}
	}
	for name, value := range out {
		if strings.TrimSpace(name) == "" {
			return nil, domain.SecretsError(nil, "a secret with an empty name was added")
		}
		if value == "" {
			return nil, domain.SecretsError(nil,
				"secret %q was left empty, so nothing was changed", name).
				WithHint("delete the line to remove a secret; an empty value is not a value")
		}
	}
	return out, nil
}

// applySecretEdits writes what changed and restarts what depends on it.
func (a *App) applySecretEdits(ctx context.Context, before, after map[string]string) error {
	var changed, added, removed []string

	for name, value := range after {
		old, existed := before[name]
		switch {
		case !existed:
			added = append(added, name)
		case old != value:
			changed = append(changed, name)
		}
	}
	for name := range before {
		if _, kept := after[name]; !kept {
			removed = append(removed, name)
		}
	}
	sort.Strings(changed)
	sort.Strings(added)
	sort.Strings(removed)

	if len(changed)+len(added)+len(removed) == 0 {
		a.finish(ops.Result{Summary: "no secrets changed"})
		return nil
	}

	if err := a.checkRemovals(ctx, removed); err != nil {
		return err
	}

	// Writes first, removals second. A failure partway leaves more secrets
	// set than the operator asked for rather than fewer, and a surplus
	// secret breaks nothing while a missing one fails the next `apply`.
	for _, name := range append(append([]string{}, added...), changed...) {
		if err := a.Deps.Secrets.Set(ctx, name, domain.NewSecret(after[name])); err != nil {
			return err
		}
	}
	for _, name := range removed {
		if err := a.Deps.Secrets.Remove(ctx, name); err != nil {
			return err
		}
	}

	touched := append(append(append([]string{}, added...), changed...), removed...)
	restarted, err := a.restartDependents(ctx, touched)
	if err != nil {
		return err
	}

	a.finish(ops.Result{
		Summary: editSummary(added, changed, removed, restarted),
		Data: map[string]any{
			"added": added, "changed": changed, "removed": removed,
			"restarted": restarted,
		},
	})
	return nil
}

// checkRemovals refuses to delete a secret the installed release requires.
func (a *App) checkRemovals(ctx context.Context, removed []string) error {
	if len(removed) == 0 || a.Flags.force {
		return nil
	}

	schema, err := a.secretSchema(ctx)
	if err != nil {
		// No release installed: nothing declares anything required.
		return nil
	}

	var required []string
	for _, name := range removed {
		if decl, ok := schema.Declaration(name); ok && decl.Required {
			required = append(required, name)
		}
	}
	if len(required) == 0 {
		return nil
	}
	return domain.Usage("the installed release requires %s", strings.Join(required, ", ")).
		WithHint("removing them will make the next `apply` fail; " +
			"pass --force if you mean to")
}

func editSummary(added, changed, removed, restarted []string) string {
	var parts []string
	if len(added) > 0 {
		parts = append(parts, fmt.Sprintf("added %s", strings.Join(added, ", ")))
	}
	if len(changed) > 0 {
		parts = append(parts, fmt.Sprintf("changed %s", strings.Join(changed, ", ")))
	}
	if len(removed) > 0 {
		parts = append(parts, fmt.Sprintf("removed %s", strings.Join(removed, ", ")))
	}

	summary := strings.Join(parts, "; ")
	if len(restarted) > 0 {
		summary += "; restarted " + strings.Join(restarted, ", ")
	}
	return summary
}

// editorCommand finds the editor to run.
//
// $VISUAL before $EDITOR, which is the order git, crontab and sudoedit use: an
// operator with VISUAL=code and EDITOR=vi means the first for an interactive
// session. The RFC wrote it the other way round; this follows the convention
// every other tool an operator uses already established.
//
// `vi` is the fallback because POSIX requires it to exist.
func editorCommand() ([]string, error) {
	for _, env := range []string{"VISUAL", "EDITOR"} {
		if value := strings.TrimSpace(os.Getenv(env)); value != "" {
			// Split on spaces so `EDITOR="code --wait"` works. An
			// editor command needing quoting or a pipe should be a
			// script; solving that here would mean a shell, and a
			// shell would mean the editor variable choosing how it
			// is parsed.
			return strings.Fields(value), nil
		}
	}

	if _, err := osexec.LookPath("vi"); err == nil {
		return []string{"vi"}, nil
	}
	return nil, domain.Usage("no editor is configured").
		WithHint("set $EDITOR or $VISUAL, or use `morzer secret set <name>`")
}

// runEditor hands the terminal to the editor.
//
// Not through internal/infra/exec: that runner captures output and imposes a
// timeout, both of which are exactly wrong for a program the operator is
// looking at. An editor session is as long as it takes.
func runEditor(ctx context.Context, editor []string, path string) error {
	argv := append(append([]string{}, editor...), path)

	cmd := osexec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // the operator's own $EDITOR
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if err == nil {
		return nil
	}

	// An editor exiting non-zero is how an operator says "forget it" --
	// `:cq` in vim exists for exactly this. An editor that never started is
	// a different sentence with the same ending, and reporting the two
	// identically leaves someone whose $EDITOR is misspelled staring at a
	// message about an abort they did not perform.
	var exitErr *osexec.ExitError
	if errors.As(err, &exitErr) {
		return domain.Usage("the editor exited with an error; no secrets were changed").
			WithHint("nothing was written. The temporary file has been removed.")
	}
	if ctx.Err() != nil {
		return domain.Interrupted("the edit was cancelled; no secrets were changed")
	}
	return domain.Usage("cannot run the editor %q", argv[0]).
		WithHint("set $EDITOR or $VISUAL to something on PATH; " +
			"nothing was written and the temporary file has been removed")
}
