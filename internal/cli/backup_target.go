package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// newBackupTargetCommand builds `morzer backup target`.
//
// Targets are configured here rather than in `config`, which edits the
// parameters a release declares. Where an operator's backups go is not one of
// those: no vendor declares it, and it is the one setting whose value is a URL
// with a credential behind it.
func newBackupTargetCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "target",
		Short: "Manage where backups are kept besides this machine",
		Long: "A target is somewhere a backup goes that this machine's disk failing\n" +
			"does not take with it: another host over SSH, an object store, or a\n" +
			"directory on separate media.\n\n" +
			"Every backup is pushed to every configured target after it is verified,\n" +
			"and a push that fails fails the backup -- reporting success for data that\n" +
			"is still only on the machine that will die is what targets exist to end.",
	}
	cmd.AddCommand(
		newBackupTargetAddCommand(app),
		newBackupTargetRemoveCommand(app),
		newBackupTargetListCommand(app),
	)
	return cmd
}

func newBackupTargetAddCommand(app *App) *cobra.Command {
	var credentials string

	cmd := &cobra.Command{
		Use:   "add <url>",
		Short: "Add a backup target",
		Long: "Adds a target and checks it can be reached before recording it.\n\n" +
			"URLs are file:///mnt/usb/backups, ssh://user@host/srv/backups or\n" +
			"s3://bucket/prefix. A file:// target needs no credential, which is why\n" +
			"it is the one a recovery can always reach.\n\n" +
			"For the others, --credentials names a secret holding a small YAML\n" +
			"document:\n\n" +
			"    access_key_id: AKIA...\n" +
			"    secret_access_key: ...\n\n" +
			"or, for ssh://:\n\n" +
			"    private_key: |\n" +
			"      -----BEGIN OPENSSH PRIVATE KEY-----\n" +
			"      ...\n" +
			"    known_hosts: |\n" +
			"      backups.example ssh-ed25519 AAAA...\n\n" +
			"Set it first with `morzer secret set <name>`. The host key is required\n" +
			"and there is no flag to skip verifying it.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := ops.TargetAdd(cmd.Context(), app.Deps, ops.TargetAddOptions{
				Options:     app.operationOptions(),
				URL:         args[0],
				Credentials: credentials,
			})
			if err != nil {
				return err
			}
			app.finish(result)
			return nil
		},
	}

	cmd.Flags().StringVar(&credentials, "credentials", "",
		"name of a secret holding this target's credential document")
	return cmd
}

func newBackupTargetRemoveCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <url>",
		Short: "Stop keeping backups at a target",
		Long: "Removes the target from the installation. Nothing on the target is\n" +
			"deleted: backups already there stay there, which is what an operator\n" +
			"retiring one medium for another almost always wants.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := ops.TargetRemove(cmd.Context(), app.Deps, args[0])
			if err != nil {
				return err
			}
			app.finish(result)
			return nil
		},
	}
}

func newBackupTargetListCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List backup targets and whether they can be reached",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			statuses, err := ops.TargetList(cmd.Context(), app.Deps)
			if err != nil {
				return err
			}

			if app.json != nil {
				app.jsonData = statuses
				return nil
			}
			if len(statuses) == 0 {
				fmt.Fprintln(app.Stream.Out,
					"no backup targets: every copy of this deployment's data is on this machine")
				return nil
			}

			for _, s := range statuses {
				state := fmt.Sprintf("%d backup(s)", s.Backups)
				if !s.Reachable {
					state = "unreachable: " + s.Error
				}
				fmt.Fprintf(app.Stream.Out, "%-40s  %s\n", s.URL, state)
			}
			return nil
		},
	}
}

// newBackupPushCommand builds `morzer backup push`.
func newBackupPushCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "push [backup-id]",
		Short: "Copy an existing backup to every configured target",
		Long: "The retry for a push that failed. A backup whose push failed is still on\n" +
			"this machine, verified and correct -- what failed was the network or the\n" +
			"medium, and the remedy should not be taking another backup.\n\n" +
			"The backup is verified again before it is copied.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.attachBackupEngine(cmd.Context()); err != nil {
				return err
			}
			id := ""
			if len(args) == 1 {
				id = args[0]
			}

			result, err := ops.Push(cmd.Context(), app.Deps, ops.PushOptions{
				Options:  app.operationOptions(),
				BackupID: id,
			})
			if err != nil {
				return err
			}
			app.finish(result)
			return nil
		},
	}
}

// newBackupFetchCommand builds `morzer backup fetch`.
func newBackupFetchCommand(app *App) *cobra.Command {
	var (
		targetURL       string
		credentialsFile string
	)

	cmd := &cobra.Command{
		Use:   "fetch [backup-id]",
		Short: "Copy a backup down from a target onto this machine",
		Long: "Fetches a backup from a target into this machine's backup store and\n" +
			"verifies its checksums. Restoring is a separate command on purpose: a\n" +
			"backup that has come back from a bucket is one an operator should be able\n" +
			"to look at before it overwrites a database.\n\n" +
			"With no id, the newest backup on the target is fetched.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Deliberately tolerant of a missing backup engine. On a
			// rebuilt machine there is no release installed yet, and
			// fetching the backup is what comes *before* installing
			// one.
			_ = app.attachBackupEngine(cmd.Context())

			creds, err := readCredentialsFile(credentialsFile)
			if err != nil {
				return err
			}
			id := ""
			if len(args) == 1 {
				id = args[0]
			}

			result, err := ops.FetchRemote(cmd.Context(), app.Deps, ops.FetchOptions{
				TargetOptions: ops.TargetOptions{
					Options:     app.operationOptions(),
					URL:         targetURL,
					Credentials: creds,
				},
				BackupID: id,
			})
			if err != nil {
				return err
			}
			app.finish(result)
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&targetURL, "target", "",
		"target URL to fetch from; the installation's targets when omitted")
	f.StringVar(&credentialsFile, "credentials-file", "",
		"YAML file holding the target's credentials, for a machine whose secret state is not readable yet")
	return cmd
}

// readCredentialsFile reads a credential document supplied out of band.
//
// This is the escape hatch for the credential circle: on a rebuilt machine the
// bucket's keys are in the secret state, the secret state is in the backup, and
// the backup is in the bucket. A file the operator brought with them breaks it.
//
// A file rather than a flag, because a flag is argv and argv is visible in `ps`
// to every user on the machine.
func readCredentialsFile(path string) (ports.TargetCredentials, error) {
	if path == "" {
		return ports.TargetCredentials{}, nil
	}

	data, err := os.ReadFile(path) //nolint:gosec // a path the operator named
	if err != nil {
		return ports.TargetCredentials{}, domain.Usage(
			"cannot read the credentials file %s: %v", path, err)
	}
	creds, err := ops.ParseTargetCredentials(strings.TrimSpace(string(data)))
	if err != nil {
		return ports.TargetCredentials{}, domain.Usage(
			"%s is not a credential document: %s", path, domain.AsError(err).Message).
			WithHint("%s", domain.AsError(err).Hint)
	}
	return creds, nil
}
