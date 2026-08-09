package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/adapters/imagepack"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

func newReleasePackCommand(app *App) *cobra.Command {
	var (
		platform string
		force    bool
	)

	cmd := &cobra.Command{
		Use:   "pack <bundle-dir>",
		Short: "Copy the images a manifest marks `from: bundle` into the bundle",
		Long: "Copies every image marked `from: bundle` out of its registry into an OCI\n" +
			"layout under <bundle-dir>/images/, then regenerates SHA256SUMS over the\n" +
			"result.\n\n" +
			"Credentials come from the ambient Docker configuration, exactly as a\n" +
			"`docker pull` on this machine would — so this runs on your build\n" +
			"machine, and your customers never need them.\n\n" +
			"Idempotent: the layout is content-addressed, so running it twice copies\n" +
			"nothing the second time.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := args[0]

			packer, err := imagepack.New(platform)
			if err != nil {
				return err
			}

			// The manifest alone, not release.Load: a bundle whose
			// images have not been copied yet fails the layout
			// completeness check, and refusing to pack because the
			// tree is not packed is the one refusal this command
			// cannot make.
			manifest, err := release.LoadManifest(filepath.Join(dir, release.ManifestFileName))
			if err != nil {
				return err
			}
			if len(manifest.BundledImages()) == 0 {
				return domain.Usage(
					"no image in %s is marked `from: bundle`", dir).
					WithHint("mark the images that should travel in the bundle, " +
						"or publish them to a registry your customers can reach")
			}

			if err := clearStaleSignature(app, dir, force); err != nil {
				return err
			}

			if app.Flags.dryRun {
				bundled := manifest.BundledImages()
				app.finish(ops.Result{
					// Both, because `finish` carries Data in
					// JSON mode and Summary in plain: a dry
					// run that sets only the second answers
					// `--json` with an envelope containing
					// nothing about what it would do.
					Data: map[string]any{"root": dir, "images": bundled},
					Summary: fmt.Sprintf("would copy %d image(s) into %s",
						len(bundled), dir),
				})
				return nil
			}

			packed, err := packer.Pack(cmd.Context(), dir, manifest)
			if err != nil {
				return err
			}

			// Sums last, and only on success. A half-populated layout
			// then fails `release verify` until a later pack
			// completes, which is the reviewable guarantee -- `pack`
			// writes in place, so "a partially packed bundle cannot
			// exist" is a promise it could not keep.
			if err := release.WriteSums(dir); err != nil {
				return err
			}
			rel, err := release.Load(dir)
			if err != nil {
				return err
			}
			// The same checks `verify` runs, not merely a load: a
			// mismatch belongs on the vendor's machine rather than
			// the customer's, and this is the command that has just
			// rewritten the tree the checksum list describes.
			if err := checkBundleIntegrity(rel); err != nil {
				return err
			}

			if app.json != nil {
				app.jsonData = map[string]any{
					"root": rel.Root, "images": packed, "digest": rel.Digest,
				}
				return nil
			}
			app.finish(ops.Result{
				Summary: fmt.Sprintf("packed %d image(s) into %s; sign it with "+
					"`minisign -Sm %s`", len(packed), dir,
					filepath.Join(rel.Root, ports.SumsFileName))})
			return nil
		},
	}

	cmd.Flags().StringVar(&platform, "platform", "",
		"platform to select from a multi-platform image, e.g. linux/amd64")
	cmd.Flags().BoolVar(&force, "force", false,
		"discard an existing signature, which repacking invalidates")
	return cmd
}
