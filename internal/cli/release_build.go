package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

func newReleaseBuildCommand(app *App) *cobra.Command {
	var (
		force      bool
		version    string
		fromGit    bool
		allowDirty bool
	)

	cmd := &cobra.Command{
		Use:   "build <bundle-dir>",
		Short: "Stamp a version, regenerate the checksum list, and check the result",
		Long: "Resolves the version, stamps it into manifest.yaml and VERSION, writes\n" +
			"SHA256SUMS over every file in the bundle, and then verifies the bundle\n" +
			"the same way `release verify` does — so a broken one fails on the\n" +
			"vendor's machine rather than on a customer's.\n\n" +
			"With neither --version nor --version-from-git the manifest's own\n" +
			"version is used as-is and nothing is stamped.\n\n" +
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
			// The operator's own arguments are settled first, so a
			// typo is not reported behind a complaint about the
			// bundle they pointed at. A flag that quietly does
			// nothing is worse than one that refuses: whoever passed
			// --allow-dirty believes they permitted something.
			if allowDirty && !fromGit {
				return domain.Usage(
					"--allow-dirty only means something with --version-from-git")
			}

			loaded, err := release.Load(dir)
			if err != nil {
				return err
			}

			stamp, err := resolveBuildVersion(dir, loaded, version, fromGit, allowDirty)
			if err != nil {
				return err
			}
			if err := clearStaleSignature(app, dir, force); err != nil {
				return err
			}

			if app.Flags.dryRun {
				app.finish(ops.Result{
					Summary: fmt.Sprintf("would build %s at %s",
						dir, stampedVersion(stamp, loaded))})
				return nil
			}
			if !stamp.IsZero() {
				if err := release.Stamp(dir, stamp); err != nil {
					return err
				}
			}
			if err := release.WriteSums(dir); err != nil {
				return err
			}

			// Reloaded rather than reused: the tree gained a file and
			// possibly a version, so the release loaded a moment ago
			// describes a bundle that no longer exists.
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
	cmd.Flags().StringVar(&version, "version", "",
		"version to stamp into manifest.yaml and VERSION")
	cmd.Flags().BoolVar(&fromGit, "version-from-git", false,
		"derive the version from `git describe`: <next-patch>-dev.<distance>.g<sha>")
	cmd.Flags().BoolVar(&allowDirty, "allow-dirty", false,
		"with --version-from-git, allow a work tree with uncommitted changes")
	cmd.MarkFlagsMutuallyExclusive("version", "version-from-git")
	return cmd
}

// resolveBuildVersion answers what to stamp, and returns the zero version when
// the answer is "nothing -- use what the manifest already says".
//
// Three ways in, in precedence order: an explicit --version, a version derived
// from the repository, or the manifest's own. The first is the plumbing and the
// second is sugar on top of it; the third keeps `build` usable by a vendor who
// manages versions their own way.
func resolveBuildVersion(
	dir string, loaded domain.Release, explicit string, fromGit, allowDirty bool,
) (domain.Version, error) {
	switch {
	case explicit != "":
		v, err := domain.ParseVersion(explicit)
		if err != nil {
			return domain.Version{}, err
		}
		// Refused here as well as in Manifest.Validate, because the
		// message an operator needs is about the flag they just typed
		// rather than about a field in a file.
		if meta := v.Metadata(); meta != "" {
			return domain.Version{}, domain.Usage(
				"a release version may not carry build metadata, and %s carries %q",
				v, "+"+meta).
				WithHint("metadata is retained in the store's directory name and " +
					"ignored by every comparison, so two builds differing only " +
					"in metadata are distinct releases nothing can tell apart. " +
					"Use a prerelease identifier: 1.4.1-dev.7.gabc1234")
		}
		return v, nil

	case fromGit:
		described, err := release.DescribeRepository(dir)
		if err != nil {
			return domain.Version{}, err
		}
		return described.Version(allowDirty)

	default:
		// The null version is refused and nothing else is. It is legal
		// today -- IsZero tests "unset", not "zero" -- and it is
		// exactly what a scaffolded bundle carries, so a forgotten flag
		// in CI ships a bundle that is clean at every gate and whose
		// collision with the next forgetful build is guaranteed rather
		// than possible. A vendor deliberately managing versions their
		// own way never carries 0.0.0.
		if loaded.Version().Equal(domain.MustParseVersion("0.0.0")) {
			return domain.Version{}, domain.Usage(
				"the manifest still carries the placeholder version 0.0.0").
				WithHint("pass --version, or --version-from-git to derive one " +
					"from the repository")
		}
		return domain.Version{}, nil
	}
}

// stampedVersion is what a dry run reports it would produce.
func stampedVersion(stamp domain.Version, loaded domain.Release) domain.Version {
	if stamp.IsZero() {
		return loaded.Version()
	}
	return stamp
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
		// Only a confirmed absence means "there is no signature". Any
		// other error -- a permission denied on the directory, an I/O
		// failure -- would otherwise be read as "nothing to protect",
		// and the build would regenerate the checksum list while
		// leaving a signature that no longer covers it. That is the
		// exact artifact this guard exists to prevent, produced by the
		// guard itself.
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return domain.ValidationError(err, "cannot read %s", signature)
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
