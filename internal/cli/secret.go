package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"github.com/morzecrew/morzer/internal/adapters/secrets/sopsage"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
	"github.com/morzecrew/morzer/internal/ui/views"
)

func newSecretCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage the encrypted secret state",
		Long: "Secrets live encrypted in secrets.sops.yaml and are rendered to tmpfs\n" +
			"for the product to read. Values are never printed, never passed in argv,\n" +
			"and never written to the journal.",
	}

	cmd.AddCommand(
		newSecretListCommand(app),
		newSecretSetCommand(app),
		newSecretGenerateCommand(app),
		newSecretRemoveCommand(app),
		newSecretRotateCommand(app),
		newSecretEditCommand(app),
		newSecretRenderCommand(app),
		newSecretRecipientsCommand(app),
	)
	return cmd
}

func newSecretListCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List secret names, fingerprints and metadata — never values",
		Long: "Names, fingerprints, lengths and when each was last changed. Never a\n" +
			"value: the fingerprint is a truncated hash, which is what lets two\n" +
			"installations be compared without either of them printing a secret.\n\n" +
			"There is no flag that prints a value. `secret render` writes them to the\n" +
			"tmpfs directory the product reads, and that is the only way out.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			metadata, err := app.Deps.Secrets.Metadata(cmd.Context())
			if err != nil {
				return err
			}

			return app.render(metadata)
		},
	}
}

func newSecretSetCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "set <name>",
		Short: "Set a secret, reading the value from the terminal or stdin",
		Long: "The value is read without echo from the terminal, or from stdin when it\n" +
			"is piped. There is no flag for the value: argv is world-readable through\n" +
			"/proc, so a credential passed that way is a credential published.",
		Example: "  morzer secret set db_password                  # prompts, without echo\n" +
			"  printf %s \"$PASSWORD\" | morzer secret set db_password",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			value, err := app.readSecretValue(cmd.Context(), fmt.Sprintf("value for %s: ", name))
			if err != nil {
				return err
			}
			if value.IsEmpty() {
				return domain.Usage("the value is empty").
					WithHint("use `morzer secret remove %s` to delete a secret", name)
			}

			if err := app.Deps.Secrets.Set(cmd.Context(), name, value); err != nil {
				return err
			}

			// Only the services that declare a dependency on this
			// secret are restarted. Restarting the whole project
			// would turn a credential rotation into a full outage.
			restarted, err := app.restartDependents(cmd.Context(), []string{name})
			if err != nil {
				return err
			}

			app.finish(ops.Result{Summary: secretChangeSummary("set", name, restarted)})
			return nil
		},
	}
}

func newSecretGenerateCommand(app *App) *cobra.Command {
	var (
		length   int
		kind     string
		alphabet string
	)

	cmd := &cobra.Command{
		Use:   "generate <name>",
		Short: "Generate a secret using the release's declared generator",
		Long: "Generates a value of the kind, length and alphabet the release declares\n" +
			"for this secret, and restarts only the services that depend on it.\n\n" +
			"The flags override the declaration for one run. Overriding the length of\n" +
			"a secret whose consumer expects a fixed width is how a deployment starts\n" +
			"failing on a value that looks correct.",
		Example: "  morzer secret generate db_password\n" +
			"  morzer secret generate api_token --kind hex --length 32",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			gen, err := app.generatorFor(cmd.Context(), name, kind, length, alphabet)
			if err != nil {
				return err
			}

			if err := app.Deps.Secrets.Generate(cmd.Context(), name,
				ports.GenSpec{Generator: gen}); err != nil {
				return err
			}

			restarted, err := app.restartDependents(cmd.Context(), []string{name})
			if err != nil {
				return err
			}

			app.finish(ops.Result{Summary: secretChangeSummary("generated", name, restarted)})
			return nil
		},
	}

	f := cmd.Flags()
	f.IntVar(&length, "length", 0, "override the declared length")
	f.StringVar(&kind, "kind", "", "override the generator: password, hex, base64, uuid, age-key")
	f.StringVar(&alphabet, "alphabet", "", "override the password alphabet")

	return cmd
}

func newSecretRotateCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "rotate <name>",
		Short: "Replace a secret with a freshly generated value",
		Long: "Generates a new value of the same shape and restarts only the services\n" +
			"the release declares as depending on it.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			gen, err := app.generatorFor(cmd.Context(), name, "", 0, "")
			if err != nil {
				return err
			}

			if err := app.Deps.Secrets.Rotate(cmd.Context(), name,
				ports.GenSpec{Generator: gen, Overwrite: true}); err != nil {
				return err
			}

			restarted, err := app.restartDependents(cmd.Context(), []string{name})
			if err != nil {
				return err
			}

			app.finish(ops.Result{Summary: secretChangeSummary("rotated", name, restarted)})
			return nil
		},
	}
}

func newSecretRemoveCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Delete a secret",
		Long: "Removes the value from the encrypted state.\n\n" +
			"Refuses when the installed release declares the secret required, because\n" +
			"the next `apply` would fail on a machine that was working a moment ago.\n" +
			"`--force` is how you say you mean it.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			// Removing a secret the release requires would make the
			// next `apply` fail, so it needs an explicit --force.
			schema, err := app.secretSchema(cmd.Context())
			if err == nil {
				if decl, ok := schema.Declaration(name); ok && decl.Required && !app.Flags.force {
					return domain.Usage(
						"secret %q is required by the installed release", name).
						WithHint("removing it will make the next `apply` fail; pass --force if you mean to")
				}
			}

			if err := app.Deps.Secrets.Remove(cmd.Context(), name); err != nil {
				return err
			}
			app.finish(ops.Result{Summary: "removed secret " + name})
			return nil
		},
	}
}

func newSecretRenderCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "render",
		Short: "Render secrets to the tmpfs directory the product reads",
		Long: "Decrypts the secret state and writes each value to its own file under\n" +
			"/run, with the mode the release declares. This is the only path by which\n" +
			"a secret leaves the encrypted state, and /run is a tmpfs: a reboot takes\n" +
			"the plaintext with it.\n\n" +
			"Run by `apply` before the deployment starts. Running it by hand is for\n" +
			"diagnosing a service that cannot read what it expects.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			schema, err := app.secretSchema(cmd.Context())
			if err != nil {
				return err
			}

			files, err := app.Deps.Secrets.Render(cmd.Context(),
				app.Deps.Paths.SecretsRenderDir(), schema)
			if err != nil {
				return err
			}

			return app.render(files)
		},
	}
}

func newSecretRecipientsCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "recipients",
		Short: "Manage who can decrypt the secret state",
		Long: "Every recipient is an age public key that can decrypt this installation's\n" +
			"secrets: the machine's own key, any offline recovery key, and whichever\n" +
			"operator keys have been added.\n\n" +
			"Adding a recipient re-encrypts the state; removing one re-encrypts it\n" +
			"without them. Neither reaches a copy of the state that has already been\n" +
			"taken, so a removed recipient can still read yesterday's backup.",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List recipients",
		Long: "The public keys that can decrypt this installation's secrets, and what\n" +
			"each one is for. An empty list means the secret state has not been\n" +
			"created yet, not that nobody can read it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			recipients, err := app.Deps.Secrets.Recipients(cmd.Context())
			if err != nil {
				return err
			}

			return app.render(recipients)
		},
	}

	var (
		kind    string
		comment string
	)
	add := &cobra.Command{
		Use:   "add <age-public-key>",
		Short: "Add a recipient and re-encrypt the state for it",
		Long: "Re-encrypts the secret state so this key can decrypt it too.\n\n" +
			"Takes effect from now on and not before: a backup taken yesterday was\n" +
			"encrypted for yesterday's recipients, so adding a key does not make an\n" +
			"older backup readable by it.",
		Example: "  morzer secret recipients add age1... --kind operator --comment \"alice\"",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			recipientKind := ports.RecipientKind(kind)
			switch recipientKind {
			case ports.RecipientRecovery, ports.RecipientOperator:
			case "":
				recipientKind = ports.RecipientOperator
			case ports.RecipientMachine:
				return domain.Usage("the machine recipient is managed by `morzer init`").
					WithHint("use --kind recovery or --kind operator")
			default:
				return domain.Usage("unknown recipient kind %q", kind).
					WithHint("valid kinds: recovery, operator")
			}

			if err := app.Deps.Secrets.AddRecipient(cmd.Context(), ports.Recipient{
				PublicKey: strings.TrimSpace(args[0]),
				Kind:      recipientKind,
				Comment:   comment,
			}); err != nil {
				return err
			}

			app.finish(ops.Result{Summary: "added " + string(recipientKind) + " recipient"})
			return nil
		},
	}
	add.Flags().StringVar(&kind, "kind", "operator", "recipient kind: recovery or operator")
	add.Flags().StringVar(&comment, "comment", "", "note recorded alongside the key")

	remove := &cobra.Command{
		Use:   "remove <age-public-key>",
		Short: "Remove a recipient and re-encrypt without it",
		Long: "Re-encrypts the secret state without this key.\n\n" +
			"Not a revocation of what the holder has already seen, and not of any\n" +
			"backup they could already read. Rotate the secrets themselves when a key\n" +
			"is removed because it was compromised.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.Deps.Secrets.RemoveRecipient(cmd.Context(),
				ports.Recipient{PublicKey: strings.TrimSpace(args[0])}); err != nil {
				return err
			}
			app.finish(ops.Result{Summary: "removed recipient"})
			return nil
		},
	}

	keygen := &cobra.Command{
		Use:   "generate-recovery-key <path>",
		Short: "Create an offline recovery identity; show its public key",
		Long: "Writes a new age identity to <path> with 0400 permissions and prints its\n" +
			"public key. Move the file off this machine: a recovery key stored on the\n" +
			"machine it is meant to recover protects nothing.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			if _, err := os.Stat(path); err == nil {
				return domain.Usage("%s already exists", path).
					WithHint("refusing to overwrite an existing identity")
			}

			pub, err := sopsage.GenerateIdentity(path)
			if err != nil {
				return err
			}

			return app.render(views.KeyPair{PublicKey: pub, Path: path})
		},
	}

	cmd.AddCommand(list, add, remove, keygen)
	return cmd
}

// generatorFor resolves the generator for a secret: the release's declaration,
// overridden by any flags.
func (a *App) generatorFor(ctx context.Context, name, kind string, length int, alphabet string) (domain.Generator, error) {
	gen := domain.Generator{Kind: domain.GeneratorPassword}

	if schema, err := a.secretSchema(ctx); err == nil {
		if decl, ok := schema.Declaration(name); ok {
			gen = decl.Generator
			if !decl.Generator.Auto() && kind == "" {
				return domain.Generator{}, domain.Usage(
					"the release declares no generator for secret %q", name).
					WithHint("supply the value with `morzer secret set %s`, "+
						"or pass --kind to generate one anyway", name)
			}
		}
	}

	if kind != "" {
		gen.Kind = domain.GeneratorKind(kind)
	}
	if length > 0 {
		gen.Length = length
	}
	if alphabet != "" {
		gen.Alphabet = alphabet
	}
	// The flags can produce a generator the manifest never could: --length 4
	// is below the redaction floor, so the value would be handed over and
	// then printed by the first tool that echoes it.
	if err := gen.Validate(); err != nil {
		return domain.Generator{}, err
	}
	return gen, nil
}

// secretSchema loads the installed release's secret declarations.
func (a *App) secretSchema(ctx context.Context) (domain.SecretSchema, error) {
	current, err := a.Deps.State.CurrentRelease(ctx)
	if err != nil || current.IsZero() {
		return domain.SecretSchema{}, domain.InstallationError(domain.ErrReleaseNotFound,
			"no release is installed, so no secret schema is known")
	}
	rel, err := release.Load(current.Root)
	if err != nil {
		return domain.SecretSchema{}, err
	}
	return release.LoadSecretSchema(rel)
}

// restartDependents restarts only the services that declare a dependency on
// the changed secrets.
//
// The alternative -- restarting everything -- turns a credential rotation into
// a full outage, which is why the release declares the dependency in the first
// place.
func (a *App) restartDependents(ctx context.Context, names []string) ([]string, error) {
	schema, err := a.secretSchema(ctx)
	if err != nil {
		// No release installed: nothing is running to restart.
		return nil, nil
	}

	services := schema.ServicesFor(names)
	if len(services) == 0 {
		return nil, nil
	}

	current, err := a.Deps.State.CurrentRelease(ctx)
	if err != nil || current.IsZero() {
		return nil, nil
	}
	rel, err := release.Load(current.Root)
	if err != nil {
		return nil, err
	}
	inst, err := a.Deps.State.LoadInstallation(ctx)
	if err != nil {
		return nil, err
	}

	// The rendered files must be updated before the restart, or the
	// services would come back holding the old value.
	if _, err := a.Deps.Secrets.Render(ctx, a.Deps.Paths.SecretsRenderDir(), schema); err != nil {
		return nil, err
	}

	cfg, err := a.Deps.RuntimeConfigFor(rel, inst)
	if err != nil {
		return nil, err
	}
	if err := a.Deps.Runtime.Restart(ctx, cfg, services); err != nil {
		return nil, err
	}
	return services, nil
}

func secretChangeSummary(verb, name string, restarted []string) string {
	summary := verb + " secret " + name
	switch len(restarted) {
	case 0:
		return summary
	default:
		return summary + "; restarted " + strings.Join(restarted, ", ")
	}
}

// readPassword reads from the terminal without echo.
//
// The reader is passed rather than taken from os.Stdin so that the refusal
// below -- the path every non-interactive caller hits -- is reachable by
// something other than a person at a keyboard. Suppressing the echo still
// needs a real file descriptor, which is why the type assertion is the check.
//
// The read races the context: signal.NotifyContext consumes the SIGINT a
// Ctrl-C raises, so a blocking read would otherwise sit there unaware and the
// prompt could only be left through Enter or an external kill. The echo flip
// is performed *here*, not in the reader goroutine: the one ioctl that
// changes the terminal mode happens before the reader starts, and the restore
// happens where the reader can no longer contradict it -- x/term.ReadPassword
// does its flip inside the reading goroutine, which is exactly the ordering
// race this replaces. The goroutine itself only reads; canonical mode makes
// the kernel assemble the line. An abandoned reader stays blocked on the fd
// until the process exit that follows cancellation releases it.
func readPassword(ctx context.Context, in io.Reader, out io.Writer, prompt string) (string, error) {
	f, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return "", domain.Usage("no terminal available to read the value").
			WithHint("pipe the value on stdin instead: `printf %%s 'value' | morzer secret set <name>`")
	}
	fd := int(f.Fd())

	saved, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return "", domain.Internal(err, "cannot read the terminal state")
	}
	// The same mode x/term.ReadPassword sets: no echo, canonical line
	// assembly in the kernel, signals still delivered -- a Ctrl-C at the
	// prompt raises the SIGINT that cancels the context handed in.
	noEcho := *saved
	noEcho.Lflag &^= unix.ECHO
	noEcho.Lflag |= unix.ICANON | unix.ISIG
	noEcho.Iflag |= unix.ICRNL
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &noEcho); err != nil {
		return "", domain.Internal(err, "cannot prepare the terminal")
	}
	restore := func() error { return unix.IoctlSetTermios(fd, unix.TCSETS, saved) }

	fmt.Fprint(out, prompt)
	type outcome struct {
		line []byte
		err  error
	}
	read := make(chan outcome, 1)
	go func() {
		var line []byte
		buf := make([]byte, 256)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				line = append(line, buf[:n]...)
				if line[len(line)-1] == '\n' {
					read <- outcome{line, nil}
					return
				}
			}
			if err != nil {
				// EOF with content is a completed value typed
				// without a final newline (Ctrl-D); with none it
				// is the error it looks like.
				if errors.Is(err, io.EOF) && len(line) > 0 {
					err = nil
				}
				read <- outcome{line, err}
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
		fmt.Fprintln(out)
		if rerr := restore(); rerr != nil {
			return "", domain.Internal(rerr,
				"cancelled, but the terminal could not be restored").
				WithHint("run `reset` to repair the terminal")
		}
		return "", domain.Interrupted("cancelled at the prompt")
	case r := <-read:
		fmt.Fprintln(out)
		rerr := restore()
		if r.err != nil {
			return "", domain.Internal(r.err, "cannot read the value")
		}
		if rerr != nil {
			return "", domain.Internal(rerr, "the value was read but the terminal could not be restored").
				WithHint("run `reset` to repair the terminal")
		}
		return strings.TrimSpace(string(r.line)), nil
	}
}
