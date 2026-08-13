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
	cmd.AddCommand(installationScope(newAttestVerifyCommand(app)))
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
