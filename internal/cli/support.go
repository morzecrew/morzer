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
		// Machine-scoped, and the difference matters rather than being
		// bookkeeping: `inspect` acts on a file that was handed to
		// somebody. The reader this artifact exists for is a vendor on
		// their own laptop, where there is no installation to select and
		// refusing to run without one would take the command away from
		// exactly the audience it was written for.
		machineScope(newSupportInspectCommand(app)),
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
			"encrypted to them and named `.tar.zst.age`. Whoever holds one of\n" +
			"those keys can read it, and nothing it passes through on the way\n" +
			"to them can. This machine adds no key of its own, so it cannot\n" +
			"read a bundle back either -- unless your release names a key it\n" +
			"already holds.\n\n" +
			"`--preview` prints the recipients in full before anything is\n" +
			"written, which is the only moment checking them against what your\n" +
			"vendor published is worth anything.\n\n" +
			"A release that declares no recipients at all produces a plaintext\n" +
			"archive and says so on every run. A declaration that is there and\n" +
			"unusable -- naming nobody, or naming something this manager cannot\n" +
			"parse as a key -- is refused before a single component is\n" +
			"collected, rather than quietly falling back to writing everything\n" +
			"out in the clear.",
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

// `morzer support inspect <file>` — RFC 0024 P4b.
//
// Two answers rather than one: what is in the archive, and what its signature
// established. §3.5 asks for the listing; decision 11 shapes the verification,
// and the shape is a refusal — the key `meta.json` names is never the key the
// signature is checked against, because whoever wrote the archive wrote the
// name beside it.
func newSupportInspectCommand(app *App) *cobra.Command {
	var (
		identity string
		key      string
	)

	cmd := &cobra.Command{
		Use:   "inspect <file>",
		Short: "List what is in a support archive, and check its signature",
		Long: "Reads back an archive `morzer support bundle` wrote — yours, or one\n" +
			"somebody sent you. It prints the same component table the archive was\n" +
			"written with, and reports what the signature beside it established.\n\n" +
			"Nothing is extracted. The archive is read in memory and no part of it\n" +
			"is written to disk, so inspecting an encrypted bundle does not leave a\n" +
			"readable copy on the machine doing the inspecting.\n\n" +
			"An encrypted archive needs `--identity`, holding a key the release\n" +
			"named as a support recipient. The machine that produced the archive\n" +
			"does not have one, on purpose.\n\n" +
			"**The archive names the key that signed it, and that name proves\n" +
			"nothing.** Whoever wrote the archive wrote the name. So the signature\n" +
			"is checked against this installation's own record of its keys when\n" +
			"you run this on the machine that produced it, or against `--key` when\n" +
			"you do not — a key you got from the operator rather than from the\n" +
			"file. With neither, the claimed key is printed and the signature is\n" +
			"reported as unchecked, which is the honest answer rather than a tick\n" +
			"nobody earned.\n\n" +
			"`--key` takes the key itself or a file holding one.",
		Example: "  morzer support inspect support-demo-op_01M0-20260815T101500Z.tar.zst\n" +
			"  morzer support inspect bundle.tar.zst.age --identity ~/vendor.key\n" +
			"  morzer support inspect bundle.tar.zst --key RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := ops.SupportInspect(cmd.Context(), app.Deps,
				ops.SupportInspectOptions{
					Path:         args[0],
					IdentityFile: identity,
					ExpectedKey:  key,
				})
			if err != nil {
				return err
			}
			return app.render(report)
		},
	}

	cmd.Flags().StringVar(&identity, "identity", "",
		"age identity to decrypt an encrypted archive with")
	cmd.Flags().StringVar(&key, "key", "",
		"the signing key this archive should carry, or a file holding it")
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
