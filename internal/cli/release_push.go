package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/adapters/source/local"
	"github.com/morzecrew/morzer/internal/adapters/source/oci"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
	"github.com/morzecrew/morzer/internal/ui/views"
)

func newReleasePushCommand(app *App) *cobra.Command {
	var (
		channels    []string
		signingKeys []string
	)

	cmd := &cobra.Command{
		Use:   "push <archive> <oci-reference>",
		Short: "Publish a release archive to an OCI registry",
		Long: "Publishes the archive `release archive` wrote, as the single-layer\n" +
			"artifact the OCI source reads, then moves each --channel tag to it.\n\n" +
			"The archive is checked first, the way `release verify` checks a bundle,\n" +
			"and with --signing-key its signature too. The version comes from the\n" +
			"archive's manifest; a tag on the reference must match it.\n\n" +
			"A version already published is not overwritten. With the same content\n" +
			"it is reported as already published and the command succeeds, so a\n" +
			"pipeline whose channel move failed can run the same step again; with\n" +
			"different content it is refused.\n\n" +
			"It prints two digests. The manifest digest pins oci://…@sha256:; the\n" +
			"content digest is what customers pass to `morzer update --digest`.\n\n" +
			"Credentials are the ones `docker login` stored. --dry-run checks and\n" +
			"reads everything and writes nothing.",
		Example: "  morzer release push demo-1.3.0.tar.zst oci://registry.example/demo/bundle --channel stable",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := ports.ParseRef(args[1])
			if err != nil {
				return err
			}
			if ref.Scheme != oci.Scheme {
				return domain.Usage("release push publishes to an OCI registry, not to %s", ref).
					WithHint("pass a reference like oci://registry.example/demo/bundle")
			}

			rel, archive, cleanup, err := openArchive(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			defer cleanup()

			if err := checkBundleIntegrity(rel); err != nil {
				return err
			}
			if len(signingKeys) > 0 {
				if err := app.Deps.Verifier.Verify(cmd.Context(),
					ports.BundlePath(rel.Root), ports.Expectation{
						PublicKeys: signingKeys,
						Required:   true,
					}); err != nil {
					return err
				}
			} else if _, err := os.Stat(filepath.Join(rel.Root, ports.SignatureFileName)); errors.Is(err, fs.ErrNotExist) {
				warnUnsigned(app)
			}

			source := oci.New()
			defer func() { _ = source.Close() }()

			res, pushErr := source.Push(cmd.Context(), oci.PushRequest{
				Archive:       archive,
				Reference:     ref.Location,
				Version:       rel.Version(),
				Product:       rel.Name(),
				ContentDigest: rel.Digest,
				Channels:      channels,
				DryRun:        app.Flags.dryRun,
			})
			for _, warning := range res.Warnings {
				fmt.Fprintf(app.Stream.Err, "warning: %s\n", warning)
			}
			// A push refused before anything was written has nothing to
			// report. One whose channel move failed does: the version is
			// in place, and which channels moved is what the re-run needs.
			if pushErr != nil && len(res.Channels) == 0 {
				return pushErr
			}

			report := views.Pushed{
				Name:           rel.Name(),
				VersionInfo:    rel.Version(),
				Reference:      res.Reference,
				ManifestDigest: res.ManifestDigest,
				ContentDigest:  rel.Digest,
				Published:      res.Published,
				DryRun:         app.Flags.dryRun,
				Channels:       make([]views.PushedChannel, 0, len(res.Channels)),
			}
			for _, c := range res.Channels {
				report.Channels = append(report.Channels, views.PushedChannel(c))
			}
			if err := app.render(report); err != nil {
				return err
			}
			return pushErr
		},
	}

	cmd.Flags().StringArrayVar(&channels, "channel", nil,
		"tag to move to the version once it is in place; repeat for several, moved in order")
	cmd.Flags().StringArrayVar(&signingKeys, "signing-key", nil,
		"minisign public key the bundle's signature must verify against; repeat for several")
	return cmd
}

// openArchive extracts an archive the way an installation would -- through the
// local source, under its limits and refusals -- and loads it under its own
// name. It returns the archive to push and a function that removes everything
// it made.
//
// The archive to push is a private copy, and the extraction reads the same
// copy. Reading the vendor's path once to check and again to upload would let
// a file replaced in between be published under the checked content digest.
//
// Only an archive: pushing a directory would mean archiving it here, and the
// refusals between a tree and an archive belong to `release archive`.
func openArchive(ctx context.Context, path string) (domain.Release, string, func(), error) {
	if !atomicfs.IsTarZst(path) {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return domain.Release{}, "", nil, domain.Usage("%s is a directory; release push takes an archive", path).
				WithHint("run `morzer release archive %s` and push the file it writes", path)
		}
		return domain.Release{}, "", nil, domain.Usage("%s is not a tar.zst archive", path).
			WithHint("push the file `morzer release archive` writes")
	}

	tmp, err := os.MkdirTemp("", "morzer-push-")
	if err != nil {
		return domain.Release{}, "", nil, domain.Internal(err, "cannot create a directory to check the archive in")
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	fail := func(err error) (domain.Release, string, func(), error) {
		cleanup()
		return domain.Release{}, "", nil, err
	}

	// Under its own name, which becomes the layer's title.
	archive := filepath.Join(tmp, filepath.Base(path))
	if err := copyFile(path, archive); err != nil {
		return fail(domain.ValidationError(err, "cannot read %s", path))
	}

	dest := filepath.Join(tmp, "bundle")
	if _, err := local.New().Fetch(ctx, ports.Ref{Scheme: local.Scheme, Location: archive}, dest); err != nil {
		return fail(err)
	}
	rel, err := release.LoadAs(dest, path)
	if err != nil {
		return fail(err)
	}
	return rel, archive, cleanup, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
