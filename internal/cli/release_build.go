package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

func newReleaseBuildCommand(app *App) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "build <bundle-dir>",
		Short: "Regenerate a bundle's checksum list and check the result",
		Long: "Writes SHA256SUMS over every file in the bundle and then verifies the\n" +
			"bundle the same way `release verify` does, so a broken one fails on\n" +
			"the vendor's machine rather than on a customer's.\n\n" +
			"Writes in place. The next step is signing — `minisign -Sm\n" +
			"<bundle-dir>/SHA256SUMS` — and then `release archive`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := args[0]

			// Validated before anything is written. A bundle whose
			// manifest does not load is one this command must refuse
			// rather than sum: writing a checksum list over a broken
			// tree produces evidence that the tree is exactly as
			// broken as it is.
			if _, err := release.Load(dir); err != nil {
				return err
			}
			if err := clearStaleSignature(app, dir, force); err != nil {
				return err
			}

			if app.Flags.dryRun {
				app.finish(ops.Result{
					Summary: fmt.Sprintf("would regenerate %s in %s",
						ports.SumsFileName, dir)})
				return nil
			}
			if err := release.WriteSums(dir); err != nil {
				return err
			}

			// Reloaded rather than reused: the tree gained a file, so
			// the digest computed a moment ago describes a bundle
			// that no longer exists.
			rel, err := release.Load(dir)
			if err != nil {
				return err
			}
			if err := checkBundleIntegrity(rel); err != nil {
				return err
			}

			if app.json != nil {
				app.jsonData = map[string]any{
					"root":    rel.Root,
					"version": rel.Version(),
					"digest":  rel.Digest,
				}
				return nil
			}
			fmt.Fprintf(app.Stream.Out, "%s %s\n%s\n", rel.Name(), rel.Version(), rel.Digest)
			app.finish(ops.Result{
				Summary: fmt.Sprintf("wrote %s; sign it with `minisign -Sm %s`",
					ports.SumsFileName, filepath.Join(rel.Root, ports.SumsFileName))})
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false,
		"discard an existing signature, which regenerating the checksum list invalidates")
	return cmd
}

// clearStaleSignature refuses to leave a signature over a list that is about to
// change.
//
// Regenerating SHA256SUMS necessarily invalidates any signature over it, so the
// presence of one is the refusal -- not its age. Forcing past it *deletes* the
// signature rather than building around it: keeping a signature that no longer
// verifies produces exactly the artifact the chain exists to prevent, and one
// that fails on the customer's machine with no explanation the vendor can give.
func clearStaleSignature(app *App, dir string, force bool) error {
	signature := filepath.Join(dir, ports.SignatureFileName)
	if _, err := os.Stat(signature); err != nil {
		return nil
	}

	if !force {
		return domain.ValidationError(nil,
			"%s already carries a %s, and regenerating the checksum list would invalidate it",
			dir, ports.SignatureFileName).
			WithHint("pass --force to discard the signature and sign again afterwards")
	}
	if app.Flags.dryRun {
		fmt.Fprintf(app.Stream.Err, "would remove %s\n", signature)
		return nil
	}
	if err := os.Remove(signature); err != nil {
		return domain.ValidationError(err, "cannot remove %s", signature)
	}
	fmt.Fprintf(app.Stream.Err,
		"removed %s; sign the new checksum list before archiving\n", ports.SignatureFileName)
	return nil
}
