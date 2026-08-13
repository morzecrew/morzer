package cli

import (
	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui/views"
)

func newAttestCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "attest",
		Short: "Read back the signed record of what this machine did",
		Long: "After each lifecycle operation the manager writes a signed statement of\n" +
			"what it did: the release and its digest, what was verified, which images\n" +
			"ran, and every step with its outcome. The statements are in-toto\n" +
			"documents signed with this installation's own key, so anybody holding\n" +
			"one can check it with `minisign -Vm` and nothing else.\n\n" +
			"What a signature proves is deliberately narrow, and every document says\n" +
			"so in its own text: that a process holding this installation's key\n" +
			"produced those bytes. Not that the bytes are true, and not that the\n" +
			"machine was uncompromised when it signed.",
	}
	cmd.AddCommand(
		installationScope(newAttestLogCommand(app)),
		installationScope(newAttestVerifyCommand(app)),
		installationScope(newAttestPushCommand(app)),
	)
	return cmd
}

func newAttestLogCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "log [path]",
		Short: "List what this machine has recorded, newest first",
		Long: "What the statements say, without asking whether to believe them.\n\n" +
			"That is `attest verify`'s question, and it is kept separate on\n" +
			"purpose: an operator reading a timeline during an incident should\n" +
			"not have it withheld because a signature is missing, and one asking\n" +
			"whether a record can be trusted should not be answered by a listing.\n\n" +
			"The `signed` column says a signature is *there*, never that it\n" +
			"checks out.\n\n" +
			"With no path, the installation's own statements are listed.",
		Example: "  morzer attest log\n" +
			"  morzer attest log --json | jq -r '.data.entries[0].operation'",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var path string
			if len(args) == 1 {
				path = args[0]
			}
			entries, err := ops.AttestLog(cmd.Context(), app.Deps, ops.VerifyOptions{
				Options: app.operationOptions(),
				Path:    path,
			})
			if err != nil {
				return err
			}
			return app.render(attestationLogView(entries))
		},
	}
	return cmd
}

func newAttestPushCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Copy statements that are only on this machine to the targets",
		Long: "Statements are pushed as they are written, to the same targets this\n" +
			"installation keeps its backups on. A push that fails does **not**\n" +
			"fail the operation being recorded -- the inverse of what a backup\n" +
			"does, because a backup that did not leave is a data-loss risk that\n" +
			"has already materialised, while a record that did not leave is a gap\n" +
			"whose local copy is still here.\n\n" +
			"This is what closes that gap afterwards, and what `doctor` names.\n" +
			"It sends only what is not already there, so it is safe to run from\n" +
			"cron.",
		Example: "  morzer attest push\n" +
			"  morzer attest push --dry-run",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := ops.AttestPush(cmd.Context(), app.Deps, app.operationOptions())
			if err != nil {
				return err
			}
			app.finish(result)

			// Non-zero when a target did not answer, so a cron job
			// finds out. Statements that are simply still missing
			// are not a failure of this command: it pushed what it
			// could, and `doctor` is what keeps saying so.
			report, ok := result.Data.(ops.AttestPushReport)
			if ok && report.Unreachable() {
				return domain.Preflight(nil, "some targets did not answer")
			}
			return nil
		},
	}
	return cmd
}

func newAttestVerifyCommand(app *App) *cobra.Command {
	var againstLive bool

	cmd := &cobra.Command{
		Use:   "verify [path]",
		Short: "Check the signatures, the chain, and what is running now",
		Long: "Three questions, answered separately because an operator acts on them\n" +
			"differently.\n\n" +
			"**Signature.** Did a key this installation knows about produce these\n" +
			"bytes? A signature made by a key the machine has since retired is\n" +
			"reported as *signed by a predecessor* rather than as valid: a rebuilt\n" +
			"machine is honestly a different signer, and folding the two together\n" +
			"would mean a rotation after a suspected compromise still accepted\n" +
			"whatever the old key signs.\n\n" +
			"**Chain.** Do the statements that moved between releases join up? A gap\n" +
			"means a release was installed by something that filed no record.\n\n" +
			"**Live** (`--against-live`). Does the deployment running right now match\n" +
			"the newest successful statement? This is the one that can fail because\n" +
			"somebody changed something by hand -- an image swapped, a container\n" +
			"started outside the manager -- which is the question an audit is\n" +
			"actually asking.\n\n" +
			"With no path, the installation's own statements are checked.",
		Example: "  morzer attest verify\n" +
			"  morzer attest verify --against-live\n" +
			"  morzer attest verify /var/lib/demo/attestations/op_01K2Z9.json\n" +
			"  morzer attest verify --json | jq -e '.data.live_mismatches | length == 0'",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var path string
			if len(args) == 1 {
				path = args[0]
			}

			report, err := ops.AttestVerify(cmd.Context(), app.Deps, ops.VerifyOptions{
				Options:     app.operationOptions(),
				Path:        path,
				AgainstLive: againstLive,
			})
			if err != nil {
				return err
			}

			if err := app.render(verificationView(report)); err != nil {
				return err
			}

			// A non-zero exit for a real finding, so this is usable in a
			// cron job or a CI step without parsing the output. A
			// predecessor signature is not a finding -- see
			// VerifyReport.Problems.
			if report.Problems() > 0 {
				return domain.Preflight(nil,
					"%d attestation problem(s)", report.Problems())
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&againstLive, "against-live", false,
		"compare the newest successful statement against what is running now")
	return cmd
}

// attestationLogView maps the listing onto the view that draws it, the same
// seam and for the same reason as verificationView.
func attestationLogView(entries []ops.LogEntry) views.AttestationLog {
	out := views.AttestationLog{Entries: make([]views.LogRow, 0, len(entries))}
	for _, e := range entries {
		out.Entries = append(out.Entries, views.LogRow{
			Operation:  e.Operation,
			Kind:       e.Kind,
			Outcome:    e.Outcome,
			Started:    e.Started,
			From:       e.From,
			To:         e.To,
			Signed:     e.Signed,
			File:       e.File,
			Unreadable: e.Unreadable,
		})
	}
	return out
}

// verificationView maps the operation's report onto the view that draws it.
//
// A mapping rather than the same struct twice: the report is what the
// operation computed, and the view is the published `--json` shape. They agree
// today, and keeping the seam means the terminal output can change without
// moving a monitoring contract.
func verificationView(r ops.VerifyReport) views.Verification {
	out := views.Verification{
		Chain:       r.Chain,
		Live:        r.Live,
		LiveChecked: r.LiveChecked,
		LiveAgainst: r.LiveAgainst,
		Problems:    r.Problems(),
	}
	for _, s := range r.Statements {
		out.Statements = append(out.Statements, views.StatementVerdict{
			File:       s.File,
			Operation:  s.Operation,
			Kind:       s.Kind,
			Outcome:    s.Outcome,
			Signature:  s.Signature,
			Unreadable: s.Unreadable,
		})
	}
	return out
}
