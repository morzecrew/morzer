package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// `morzer sync` — RFC 0057 P3, the machine's side of desired state.

func newSyncCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Follow the desired state an authority publishes",
		Long: "An authority publishes documents for this installation to a target this\n" +
			"machine reads: each one signed with the authority's key and encrypted to\n" +
			"this machine. Each cycle fetches the newest, verifies its signature\n" +
			"before reading it, and applies it as one `config apply`, within the\n" +
			"names the release lets be managed from elsewhere. Nothing listens here:\n" +
			"the machine pulls, on a timer, while sync.enabled is on.\n\n" +
			"Configured with the sync.* settings, and only locally: a document can\n" +
			"never change who this machine follows.\n\n" +
			"Docs: https://morzecrew.github.io/morzer/latest/operating/managing-from-elsewhere/",
	}
	cmd.AddCommand(newSyncNowCommand(app), newSyncStatusCommand(app))
	return cmd
}

func newSyncNowCommand(app *App) *cobra.Command {
	var unattended bool
	cmd := &cobra.Command{
		Use:   "now",
		Short: "Run one sync cycle",
		Long: "Fetches the newest document, verifies and decrypts it, and applies it\n" +
			"as one operation. A document that is not the authority's, is older than\n" +
			"the one applied, has expired, or names what the release does not let\n" +
			"be managed changes nothing, and is reported. A refused document is not\n" +
			"tried again; a newer one is.\n\n" +
			"Exits non-zero when a document's operation failed and was undone.\n" +
			"`--unattended` is the timer's tick: when another operation holds the\n" +
			"lock it exits 0, and the next tick tries again.",
		Example: "  morzer sync now\n" +
			"  morzer sync now --dry-run",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return app.runOperation(cmd.Context(), func(ctx context.Context) (ops.Result, error) {
				report, err := ops.SyncNow(ctx, app.Deps, ops.SyncOptions{Options: app.operationOptions()})
				if unattended && errors.Is(err, domain.ErrLocked) {
					// As the update tick: a queued tick would start at
					// an unpredictable time, and the next one is soon.
					report.Skipped = "another operation holds the lock"
					err = nil
				}
				if err != nil && report.Result == "" {
					return ops.Result{}, err
				}
				return ops.Result{Summary: report.Summary(), Data: report}, err
			})
		},
	}
	cmd.Flags().BoolVar(&unattended, "unattended", false,
		"one scheduled tick: exit 0 when another operation holds the lock")
	return cmd
}

func newSyncStatusCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show what sync last applied, refused and fetched",
		Long: "Prints the authority this installation follows, the last document\n" +
			"applied and the last refused, with the step and the reason, and what the\n" +
			"last cycle found. The reason's full text is printed here and kept on\n" +
			"this machine: the fleet row carries the step and class only, since the\n" +
			"text can quote the value refused.",
		Example: "  morzer sync status\n" +
			"  morzer sync status --json | jq '.data.refused'",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			report, err := ops.SyncStatus(cmd.Context(), app.Deps)
			if err != nil {
				return err
			}
			if app.json != nil {
				app.jsonData = report
				return nil
			}
			printSyncStatus(app.Stream.Out, report)
			return nil
		},
	}
}

func printSyncStatus(w io.Writer, r ops.SyncStatusReport) {
	at := func(t domain.Time) string { return t.Format(time.RFC3339) }
	state := "off"
	if r.Enabled {
		state = "enabled"
	}
	line := func(label, value string) { fmt.Fprintf(w, "%-12s %s\n", label+":", value) }
	line("sync", state)
	if r.Target != "" {
		line("target", r.Target)
	}
	if r.AuthorityKeyID != "" {
		line("authority", "key "+r.AuthorityKeyID)
	}
	line("interval", r.Interval)
	if a := r.Applied; a != nil {
		value := fmt.Sprintf("document %d at %s", a.Serial, at(a.At))
		if !a.ExpiresAt.IsZero() {
			value += ", expires " + at(a.ExpiresAt)
		}
		line("applied", value)
	}
	if f := r.Refused; f != nil {
		value := fmt.Sprintf("document %d at %s, at %s: %s", f.Serial, at(f.At), f.Step, f.Class)
		if len(f.Names) > 0 {
			value += " (" + strings.Join(f.Names, ", ") + ")"
		}
		line("refused", value)
		if f.Message != "" {
			fmt.Fprintf(w, "%-12s %s\n", "", f.Message)
		}
	}
	if f := r.LastFetch; f != nil {
		line("last fetch", fmt.Sprintf("%s at %s", f.Result, at(f.At)))
	}
	if !r.LastReached.IsZero() {
		line("reached", at(r.LastReached))
	}
}
