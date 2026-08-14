package cli

import (
	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// `morzer support bundle` — RFC 0024 P2.
//
// A verb of its own rather than `doctor --support-bundle`, which decision 1
// locks. `doctor`'s contract is "report on this machine, now": it takes no
// lock, writes nothing, and its output is a view. A support bundle has a
// redaction policy and encryption recipients, with retention and a signature
// still ahead of it, and hanging four new semantics off a diagnostic flag makes
// `doctor`'s contract a sentence with an exception in it. The same reasoning
// gave `release pack` its own verb rather than a flag on `build`.

func newSupportCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "support",
		Short: "Produce evidence somebody else can read",
		Long: "The manager already holds, in structured form, everything somebody\n" +
			"debugging this installation would ask for: the journal, `doctor`'s\n" +
			"results, the resolved manifest, configuration drift, the version\n" +
			"history and what each service is doing. What it has never had is an\n" +
			"export.\n\n" +
			"`bundle` is that export — one archive, safe to hand to a stranger,\n" +
			"whether that stranger is your vendor or a forum. What it never\n" +
			"contains is enumerated in the reference documentation and enforced by\n" +
			"the build, not promised in prose.",
	}
	cmd.AddCommand(
		installationScope(newSupportBundleCommand(app)),
		installationScope(newSupportRedactCommand(app)),
	)
	return cmd
}

func newSupportBundleCommand(app *App) *cobra.Command {
	var (
		preview bool
		noLogs  bool
		dir     string
	)

	cmd := &cobra.Command{
		Use:   "bundle",
		Short: "Write one archive describing this installation, safe to send",
		Long: "Collects what this installation is and what it did into a single\n" +
			"`.tar.zst` in the current directory.\n\n" +
			"It holds no age identity, no secret ciphertext, no signing key, no\n" +
			"backup credentials and nothing from the directory where secrets are\n" +
			"rendered. That list is not a promise in this help text: inclusion is\n" +
			"an allowlist in the code, the reference page is generated from it, and\n" +
			"a component cannot start being collected without a row appearing\n" +
			"there.\n\n" +
			"`--preview` writes nothing and prints exactly what would be\n" +
			"collected, with per-file sizes. An operator who cannot see what\n" +
			"leaves will either send nothing or send everything, and both are\n" +
			"failures of this command.\n\n" +
			"`--no-logs` leaves your containers' output out. It is not a way to\n" +
			"turn redaction off -- there is no such flag, deliberately -- it removes\n" +
			"a component rather than removing the filter from it, so it can only\n" +
			"ever send less.\n\n" +
			"If your release declares support recipients, the archive is\n" +
			"encrypted to them and readable by nobody else -- not by this\n" +
			"machine, and not by whatever the file passes through on its way.\n" +
			"It is named `.tar.zst.age` when that happens, and `--preview`\n" +
			"prints the recipients in full before anything is written, which\n" +
			"is the only moment checking them against what your vendor\n" +
			"published is worth anything.\n\n" +
			"A release that declares nobody produces a plaintext archive and\n" +
			"says so on every run. A declaration this manager cannot use is\n" +
			"refused before a single component is collected, rather than\n" +
			"quietly falling back to writing everything out in the clear.",
		Example: "  morzer support bundle --preview\n" +
			"  morzer support bundle\n" +
			"  morzer support bundle --json | jq -e '.data.entries[].redactions'",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := ops.SupportBundle(cmd.Context(), app.Deps, ops.SupportOptions{
				Preview: preview,
				NoLogs:  noLogs,
				Dir:     dir,
				Build: ops.SupportBuild{
					Commit: app.Build.Commit,
					Date:   app.Build.Date,
				},
			})
			if err != nil {
				return err
			}
			return app.render(report)
		},
	}

	cmd.Flags().BoolVar(&preview, "preview", false,
		"print what would be collected and write nothing")
	cmd.Flags().BoolVar(&noLogs, "no-logs", false,
		"leave container logs out of the archive")
	cmd.Flags().StringVar(&dir, "dir", "",
		"write the archive to this directory instead of the working directory")
	return cmd
}

// `morzer support redact --check <file>` — RFC 0024 decision 7.
//
// The archive is safe by construction; the thing an operator pastes into a chat
// window is not, and this is the same redactor pointed at that. Decision 7 grades
// it LOCKED and says it ships alongside the bundle, which the phasing section
// contradicts by listing it as P5 -- the LOCKED row wins, and §12 A6 records
// which way that was resolved.
//
// `--check` is required rather than defaulted because it is the only mode that
// exists. The alternative -- printing the redacted file to stdout -- is a second
// output surface nothing has specified, and a command that quietly rewrote an
// operator's file would destroy the evidence they were about to send.
func newSupportRedactCommand(app *App) *cobra.Command {
	var check bool

	cmd := &cobra.Command{
		Use:   "redact --check <file>",
		Short: "Say whether a file holds any of this installation's secrets",
		Long: "Runs the same redactor the support bundle uses over a file you were\n" +
			"going to send anyway — a log you tailed into a file, a paste, a config\n" +
			"you exported by hand.\n\n" +
			"It reports and writes nothing. The file is yours, and rewriting it\n" +
			"would destroy what you were about to send.\n\n" +
			"A count of zero means no value this installation currently holds\n" +
			"appears in the file. That is a smaller claim than \"clean\": a\n" +
			"credential you rotated away, or one the manager was never told about,\n" +
			"is not something it can recognise. If the secret values cannot be\n" +
			"loaded at all, the report says so instead of reporting zero.",
		Example: "  morzer support redact --check /tmp/paste.txt\n" +
			"  morzer support redact --check app.log --json | jq -e '.data.redactions == 0'",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := ops.SupportRedactCheck(cmd.Context(), app.Deps, args[0])
			if err != nil {
				return err
			}
			return app.render(report)
		},
	}

	cmd.Flags().BoolVar(&check, "check", false,
		"report what would be redacted, changing nothing")
	_ = cmd.MarkFlagRequired("check")
	return cmd
}
