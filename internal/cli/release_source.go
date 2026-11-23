package cli

import (
	"io"
	"os"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// newReleaseSourceCommand builds `morzer release source` (RFC 0038).
//
// Under `release` beside `trust`, because both govern where releases come
// from. Not `config set`: an entry is a server, a scheme and a secret's name,
// and the list of them is not one value.
func newReleaseSourceCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "source",
		Short: "Manage the credentials sent to HTTPS servers that serve bundles",
		Long: "A vendor that serves bundles only to customers who log in gives you a\n" +
			"token or a login. Store it as a secret, then name the server and the\n" +
			"secret here; every fetch from that server sends it, and nothing else\n" +
			"does.\n\n" +
			"A credential is sent only to the server it names, over TLS, and never\n" +
			"across a redirect to another host, or to the same host on another port.\n" +
			"An OCI registry is not configured here: it uses the Docker credentials.",
	}

	var bearer, basic bool
	var secret string
	add := &cobra.Command{
		Use:   "add https://<host>[:port] (--bearer | --basic) --secret <name>",
		Short: "Send a server a credential from a secret",
		Long: "Names the server and the secret its credential is in. With --bearer the\n" +
			"secret holds a token; with --basic it holds `user:password`. A server\n" +
			"that already has an entry has it replaced. Journaled.",
		Example: "  morzer secret set vendor_download_token\n" +
			"  morzer release source add https://downloads.vendor.example --bearer --secret vendor_download_token",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if bearer == basic {
				return domain.Usage("give one of --bearer and --basic")
			}
			auth := domain.SourceAuthBearer
			if basic {
				auth = domain.SourceAuthBasic
			}
			result, err := ops.SourceAdd(cmd.Context(), app.Deps, ops.SourceAddOptions{
				Options: app.operationOptions(), URL: args[0], Auth: auth, Secret: secret,
			})
			if err != nil {
				return err
			}
			app.finish(result)
			return nil
		},
	}
	add.Flags().BoolVar(&bearer, "bearer", false, "send the secret as a bearer token")
	add.Flags().BoolVar(&basic, "basic", false, "send the secret, user:password, as basic authentication")
	add.Flags().StringVar(&secret, "secret", "", "the secret holding the credential")
	_ = add.MarkFlagRequired("secret")

	remove := &cobra.Command{
		Use:   "remove https://<host>[:port]",
		Short: "Stop sending a server a credential",
		Long:  "The secret stays in the store; `morzer secret remove` removes it. Journaled.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := ops.SourceRemove(cmd.Context(), app.Deps, app.operationOptions(), args[0])
			if err != nil {
				return err
			}
			app.finish(result)
			return nil
		},
	}

	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the servers that are sent a credential",
		Long:    "Each server, how its credential is sent, and the secret it is in. Never a value.",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := ops.SourceList(cmd.Context(), app.Deps)
			if err != nil {
				return err
			}
			return app.render(report)
		},
	}

	cmd.AddCommand(add, remove, list)
	return cmd
}

// maxSourceSecretFile bounds `--source-secret-file`: a token or a login, not a
// document.
const maxSourceSecretFile = 64 << 10

// initSource reads `init`'s credential flags, and holds the credential for
// every fetch from its server from here on, the one that reads the product's
// name included (RFC 0038 decision 12).
func (a *App) initSource(cmd *cobra.Command, release string, repair bool, auth, secret, file string) (*ops.InitSource, error) {
	if file == "" {
		if cmd.Flags().Changed("source-auth") || cmd.Flags().Changed("source-secret") {
			return nil, domain.Usage("--source-auth and --source-secret go with --source-secret-file")
		}
		return nil, nil
	}
	if repair {
		return nil, domain.Usage("--source-secret-file does not apply to --repair").
			WithHint("configure the server with `morzer release source add`")
	}
	if !domain.SourceAuth(auth).Valid() {
		return nil, domain.Usage("--source-auth %q is neither bearer nor basic", auth)
	}
	value, err := readSourceSecretFile(file)
	if err != nil {
		return nil, err
	}
	src, err := ops.NewInitSource(release, domain.SourceAuth(auth), secret, value, file)
	if err != nil {
		return nil, err
	}
	if a.sourceCredentials != nil {
		a.sourceCredentials.Hold(src)
	}
	return src, nil
}

// readSourceSecretFile reads a credential from a regular file, bounded as it
// is read. Opened without blocking and checked through the descriptor, so a
// pipe cannot hang init and a file swapped after a check is the one checked;
// read through a limit, because a file can grow after its size is looked at.
func readSourceSecretFile(file string) (string, error) {
	f, err := os.OpenFile(file, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", domain.Usage("cannot read --source-secret-file %s: %s", file, err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", domain.Usage("cannot read --source-secret-file %s: %s", file, err)
	}
	if !info.Mode().IsRegular() {
		return "", domain.Usage("--source-secret-file %s is not a regular file", file)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSourceSecretFile+1))
	if err != nil {
		return "", domain.Usage("cannot read --source-secret-file %s: %s", file, err)
	}
	if len(data) > maxSourceSecretFile {
		return "", domain.Usage("--source-secret-file %s is larger than %d KiB, which no credential is",
			file, maxSourceSecretFile>>10)
	}
	return string(data), nil
}
