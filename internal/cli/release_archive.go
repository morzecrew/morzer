package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

func newReleaseArchiveCommand(app *App) *cobra.Command {
	var out string

	cmd := &cobra.Command{
		Use:   "archive <bundle-dir>",
		Short: "Pack a bundle directory into a release archive",
		Long: "Writes the tar.zst every transport reads, after checking the bundle the\n" +
			"same way `release verify` does.\n\n" +
			"This is the last of the three steps that publish a release: build,\n" +
			"sign, archive. Signing sits in the middle because the signature is a\n" +
			"file inside the bundle, so it has to exist before the bundle is packed —\n" +
			"and the manager does not sign, because a manager that signs is a\n" +
			"manager that handles signing keys.\n\n" +
			"Entry order is part of the format, not an implementation detail: the\n" +
			"manifest comes first so a reader can size its extraction budget before\n" +
			"extracting anything, and entries are otherwise sorted, so two archives\n" +
			"of one tree are byte-identical.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rel, err := release.Load(args[0])
			if err != nil {
				return err
			}
			// Where it goes is settled before whether it may be
			// written: a mistyped -o is the operator's own argument
			// and should be reported as such, rather than behind a
			// complaint about the bundle they pointed at.
			dest, err := archiveDestination(rel, out)
			if err != nil {
				return err
			}
			if err := checkArchivable(app, rel); err != nil {
				return err
			}

			// The commit date is the middle step of the timestamp
			// precedence. `build` and `archive` are separate
			// commands, so nothing carries "the version came from
			// git" between them -- the bundle sitting in a
			// repository is the fact this command can actually
			// observe, and it gives the same answer for the case the
			// step exists for. Outside a repository it is the epoch.
			modTime, err := release.ArchiveModTime(release.CommitTime(rel.Root))
			if err != nil {
				return err
			}

			if app.Flags.dryRun {
				app.finish(ops.Result{
					Summary: fmt.Sprintf("would write %s", dest)})
				return nil
			}
			if err := release.WriteArchive(rel.Root, dest, modTime); err != nil {
				return err
			}

			if app.json != nil {
				app.jsonData = map[string]any{
					"archive": dest,
					"version": rel.Version(),
					"digest":  rel.Digest,
				}
				return nil
			}
			app.finish(ops.Result{Summary: fmt.Sprintf("wrote %s", dest)})
			return nil
		},
	}

	cmd.Flags().StringVarP(&out, "output", "o", "",
		"archive path to write; defaults to <name>-<version>.tar.zst beside the bundle")
	return cmd
}

// checkArchivable is the set of refusals between a bundle directory and an
// archive.
//
// Each one guards the same thing from a different side: an archive is the
// artifact a customer receives, and every property it lacks at this moment it
// lacks permanently. A tree that changed after it was summed, or was signed
// before it changed, produces a bundle whose integrity evidence describes a
// different bundle -- and the customer's manager cannot tell the difference,
// because both are internally consistent.
func checkArchivable(app *App, rel domain.Release) error {
	sums := filepath.Join(rel.Root, ports.SumsFileName)
	sumsInfo, err := os.Stat(sums)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return domain.ValidationError(domain.ErrNotFound,
			"the bundle has no %s, so its contents cannot be attested", ports.SumsFileName).
			WithHint("run `morzer release build %s` to write one", rel.Root)
	case err != nil:
		// Distinguished from absence: a file this command cannot read
		// is not a file that is not there, and reporting the second
		// sends the vendor to write one that already exists.
		return domain.ValidationError(err, "cannot read %s", sums)
	}

	if err := checkBundleIntegrity(rel); err != nil {
		return err
	}

	signature := filepath.Join(rel.Root, ports.SignatureFileName)
	sigInfo, sigErr := os.Stat(signature)
	switch {
	case errors.Is(sigErr, fs.ErrNotExist):
		// A warning rather than a refusal: whether a signature is
		// required is the operator's policy (`require_signature`), and
		// a vendor whose customers do not require one is not doing
		// anything wrong. The cost is that "the vendor forgot to sign"
		// is caught on the operator's machine rather than the vendor's.
		fmt.Fprintf(app.Stream.Err,
			"warning: the bundle carries no %s, so operators requiring a signature "+
				"will refuse it\n", ports.SignatureFileName)
		return nil
	case sigErr != nil:
		// Not the warning: a signature that cannot be read is not a
		// signature that is absent, and packing past it would ship an
		// archive whose integrity evidence nobody checked.
		return domain.ValidationError(sigErr, "cannot read %s", signature)
	}

	if sigInfo.ModTime().Before(sumsInfo.ModTime()) {
		return domain.ValidationError(nil,
			"%s is older than %s, so it signs a list that has since changed",
			ports.SignatureFileName, ports.SumsFileName).
			WithHint("sign again: `minisign -Sm %s`", sums)
	}
	return nil
}

// archiveDestination resolves where the archive is written.
func archiveDestination(rel domain.Release, out string) (string, error) {
	if out == "" {
		out = filepath.Join(filepath.Dir(rel.Root), release.ArchiveName(rel.Manifest))
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		return "", domain.ValidationError(err, "cannot resolve %s", out)
	}

	// Writing into the bundle would add a file to the tree that SHA256SUMS
	// does not list, so the next `verify` on that directory fails the
	// completeness rule -- for a file this command created.
	if within(abs, rel.Root) {
		return "", domain.Usage(
			"the archive would be written inside the bundle at %s", rel.Root).
			WithHint("choose a path outside the bundle directory")
	}
	return abs, nil
}

// within reports whether path lies inside dir.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
