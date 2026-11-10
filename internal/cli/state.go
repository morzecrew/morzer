package cli

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	signer "github.com/morzecrew/morzer/internal/adapters/sign/minisign"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui/views"
)

// `morzer state` — RFC 0057 P4, the authority's side of desired state.
//
// Commands that need no installation, as `fleet ls` needs none: they run where
// the authority's key is, read the rows the machines publish, and write the
// documents those machines pull.

func newStateCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "state",
		Short: "Publish desired state for installations that follow an authority",
		Long: "The authority's side of desired state. `state publish` encrypts a\n" +
			"document to one installation and signs it with the authority's key,\n" +
			"where that installation pulls it from; `state ls` shows what each\n" +
			"installation applied and refused. A document reaches only a machine\n" +
			"whose fleet row verifies against the key the roster lists for it.\n\n" +
			"Docs: https://morzecrew.github.io/morzer/latest/reference/desired-state/",
	}
	key := &cobra.Command{
		Use:   "key",
		Short: "Make the authority's signing key",
		Long: "The authority signs every document with a minisign key, and each\n" +
			"machine pins its public half as sync.authority. A key made with\n" +
			"`minisign -G -W` works too; one with a password does not, since\n" +
			"`state publish` runs without a prompt.",
	}
	key.AddCommand(newStateKeyGenerateCommand(app))
	cmd.AddCommand(key, newStatePublishCommand(app), newStateListCommand(app))
	return cmd
}

func newStateKeyGenerateCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "generate <path>",
		Short: "Write a new authority signing key; show its public key",
		Long: "Writes a new minisign secret key to <path>, unencrypted and readable\n" +
			"only by you, and prints the public key the machines pin as\n" +
			"sync.authority. Keep the file off the machines it signs for: a key\n" +
			"held by a managed machine lets that machine manage the rest.",
		Example: "  morzer state key generate ./authority.key",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			if _, err := os.Stat(path); err == nil {
				return domain.Usage("%s already exists", path).
					WithHint("refusing to overwrite an existing key")
			}
			pub, err := signer.New(path, "morzer desired-state authority").EnsureKey(cmd.Context())
			if err != nil {
				return err
			}
			return app.render(views.KeyPair{PublicKey: pub.Line, Path: path})
		},
	}
}

func newStatePublishCommand(app *App) *cobra.Command {
	var (
		targetURL       string
		credentialsFile string
		rosterFile      string
		product         string
		installation    string
		file            string
		keyPath         string
		serial          int64
		validFor        time.Duration
		recipients      []string
	)
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "Encrypt and sign a document for one installation, where it pulls it",
		Long: "Reads the installation's fleet row from the target and uses it only\n" +
			"when it verifies against the key the roster lists for it. The document\n" +
			"is encrypted to the age recipient that row carries, stamped with a\n" +
			"serial and an expiry, and signed with the authority's key. The\n" +
			"ciphertext is written first and its signature last, and all but the\n" +
			"newest three documents are removed.\n\n" +
			"The document is a change document: settings, parameters, secrets and\n" +
			"unset. It is cumulative, every value the authority manages for this\n" +
			"installation, since a machine applies only the newest it finds.\n" +
			"`--file -` reads it from stdin.",
		Example: "  morzer state publish --target s3://acme-state/demo --expect ./roster.yaml \\\n" +
			"      --product demo --installation inst_01... --file ./demo.yaml --key ./authority.key\n" +
			"  morzer state publish ... --dry-run --json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			creds, err := readCredentialsFile(credentialsFile)
			if err != nil {
				return err
			}
			roster, err := readRosterFile(rosterFile)
			if err != nil {
				return err
			}
			doc, err := readStateDocument(cmd, app, file)
			if err != nil {
				return err
			}
			if keyPath == "" {
				return domain.Usage("state publish needs --key, the authority's signing key").
					WithHint("`morzer state key generate ./authority.key` makes one")
			}
			report, err := ops.StatePublish(cmd.Context(), app.Deps, ops.StatePublishOptions{
				TargetOptions: ops.TargetOptions{
					Options: app.operationOptions(), URL: targetURL, Credentials: creds,
				},
				Roster: roster, Product: product, InstallationID: installation,
				Document: doc, Signer: signer.New(keyPath, "authority"),
				Serial: serial, ValidFor: validFor, Recipients: recipients,
			})
			if err != nil {
				return err
			}
			app.finish(ops.Result{Summary: statePublishSummary(report), Data: report})
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&targetURL, "target", "", "target the installation publishes its fleet row to and pulls from")
	f.StringVar(&credentialsFile, "credentials-file", "", "YAML file holding the target's credentials")
	f.StringVar(&rosterFile, "expect", "", "roster of the installations, and the key each signs its row with")
	f.StringVar(&product, "product", "", "the installation's product")
	f.StringVar(&installation, "installation", "", "the installation's id")
	f.StringVar(&file, "file", "", "the document to publish, or - for stdin")
	f.StringVar(&keyPath, "key", "", "the authority's minisign secret key, unencrypted")
	f.Int64Var(&serial, "serial", 0, "the document's serial (default: the time in milliseconds)")
	f.DurationVar(&validFor, "valid-for", 0, "how long the document stays valid (default 168h)")
	f.StringArrayVar(&recipients, "recipient", nil,
		"age recipient to encrypt to as well as the machine, such as the authority's own; repeat for several")
	return cmd
}

// readStateDocument reads the document --file names, bounded as a change
// document is.
func readStateDocument(cmd *cobra.Command, app *App, path string) ([]byte, error) {
	var r io.Reader
	switch path {
	case "":
		return nil, domain.Usage("state publish needs --file, the document to publish")
	case "-":
		data, err := readAllCtx(cmd.Context(), app.Stream.In)
		if err != nil {
			return nil, err
		}
		return data, nil
	default:
		f, err := os.Open(path)
		if err != nil {
			return nil, domain.Usage("cannot read %s: %v", path, err)
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	data, err := io.ReadAll(io.LimitReader(r, ops.ChangeDocumentLimit+1))
	if err != nil {
		return nil, domain.Usage("cannot read %s: %v", path, err)
	}
	return data, nil
}

func statePublishSummary(r ops.StatePublishReport) string {
	verb := "published"
	if r.DryRun {
		verb = "would publish"
	}
	line := fmt.Sprintf("%s desired-state document %d for %s %s to %s, valid until %s",
		verb, r.Serial, r.Product, r.InstallationID, r.Target, r.ExpiresAt.Format(time.RFC3339))
	switch {
	case r.PruneProblem != "":
		line += "; pruning stopped: " + r.PruneProblem
	case len(r.Pruned) > 0:
		line += fmt.Sprintf("; removed %d older document(s)", len(r.Pruned))
	}
	return line
}

func newStateListCommand(app *App) *cobra.Command {
	var (
		targetURL       string
		credentialsFile string
		rosterFile      string
	)
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "Show what each installation applied and refused",
		Long: "Reads every fleet row on the target, as `fleet ls` does, with the newest\n" +
			"document published for each installation beside the serials its row\n" +
			"says it applied and refused, and why. A row the roster does not list is\n" +
			"shown as unexpected. Without `--expect`, nothing a row says is\n" +
			"authenticated, and the signature column says so.",
		Example: "  morzer state ls --target s3://acme-state/demo --expect ./roster.yaml\n" +
			"  morzer state ls --target s3://acme-state/demo --expect ./roster.yaml --json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			creds, err := readCredentialsFile(credentialsFile)
			if err != nil {
				return err
			}
			roster, err := readRosterFile(rosterFile)
			if err != nil {
				return err
			}
			if targetURL == "" {
				return domain.Usage("state ls needs --target")
			}
			report, err := ops.StateList(cmd.Context(), app.Deps, ops.StateListOptions{
				TargetOptions: ops.TargetOptions{
					Options: app.operationOptions(), URL: targetURL, Credentials: creds,
				},
				Roster: roster,
			})
			if err != nil {
				return err
			}
			return app.render(stateView(report))
		},
	}
	f := cmd.Flags()
	f.StringVar(&targetURL, "target", "", "target the installations publish their fleet rows to")
	f.StringVar(&credentialsFile, "credentials-file", "", "YAML file holding the target's credentials")
	f.StringVar(&rosterFile, "expect", "", "roster of the installations, and the key each signs its row with")
	return cmd
}

// stateView maps the report onto the view that draws it, the seam fleetView
// keeps.
func stateView(r ops.StateReport) views.State {
	out := views.State{Targets: r.Targets, Expected: r.Expected, Limitations: r.Limitations,
		Rows: make([]views.StateRow, 0, len(r.Rows))}
	for _, row := range r.Rows {
		out.Rows = append(out.Rows, views.StateRow{
			Target: row.Target, Product: row.Product, InstallationID: row.InstallationID,
			Signature: string(row.Signature), Expected: row.Expected, Absent: row.Absent,
			Published: row.Published, Sync: row.Sync, Problem: row.Problem,
		})
	}
	return out
}
