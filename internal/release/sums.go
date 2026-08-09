package release

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
)

// WriteSums regenerates a bundle's SHA256SUMS.
//
// One routine, called by every command that produces a bundle. The list is what
// a signature covers, and the verifier fails closed on a file the list does not
// mention -- so a bundle whose sums were written by one code path and checked
// by another is a bundle where the two can disagree about what "complete"
// means. There is exactly one answer here.
//
// The format is `sha256sum`'s, deliberately: a customer must be able to check a
// release with `sha256sum -c SHA256SUMS` and `minisign -Vm SHA256SUMS`, without
// this tool and without trusting it. That is what the whole chain is for.
//
// Line order matches the archive's entry order, for the same reason the archive
// has one: directory traversal order is unspecified, so an unsorted list is a
// file whose bytes differ between two runs over an identical tree.
func WriteSums(dir string) error {
	entries, err := ArchiveEntries(dir)
	if err != nil {
		return err
	}

	var b strings.Builder
	for _, rel := range entries {
		// The list cannot list itself, and the signature over it is
		// what makes the list trustworthy in the first place. This is
		// the same pair the verifier's completeness rule exempts, and
		// the two must name the same files or every bundle fails.
		if rel == ports.SumsFileName || rel == ports.SignatureFileName {
			continue
		}

		if err := checkSumsPath(rel); err != nil {
			return err
		}
		digest, err := atomicfs.DigestFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		// Bare hex, not the manager's "sha256:" spelling: `sha256sum
		// -c` is the reader this format exists for.
		fmt.Fprintf(&b, "%s  %s\n", strings.TrimPrefix(digest, "sha256:"), rel)
	}

	if b.Len() == 0 {
		return domain.ValidationError(domain.ErrNotFound,
			"%s contains nothing to check", dir)
	}
	return atomicfs.WriteFile(filepath.Join(dir, ports.SumsFileName), []byte(b.String()), 0o644)
}

// checkSumsPath refuses a name this format cannot carry unambiguously.
//
// `sha256sum`'s format is "<hex>  <path>", and readers of it -- this project's
// verifier included -- recover the path by splitting on whitespace. That
// round-trips every ordinary name, including one with single spaces, and
// silently changes names with a tab, a double space, a leading or trailing
// space, or a line break. GNU coreutils escapes those with a leading backslash
// and `\n`/`\r`/`\\` sequences; implementing that would mean the writer, the
// verifier and every third party's `sha256sum -c` agreeing on an escaping
// dialect for filenames nobody ships.
//
// So it refuses instead, and refuses on the vendor's machine. The alternative
// is not "it works": it is a bundle whose checksum list names a file that is
// not there, failing verification on a customer's machine with a message about
// a missing file the vendor can see perfectly well.
func checkSumsPath(rel string) error {
	if rel != strings.Join(strings.Fields(rel), " ") {
		return domain.ValidationError(nil,
			"%s cannot be listed in %s: its name has whitespace the format cannot carry",
			rel, ports.SumsFileName).
			WithHint("a single space is fine; tabs, line breaks, repeated spaces and " +
				"leading or trailing spaces are not. Rename the file")
	}
	// A leading "*" is how sha256sum marks binary mode, and readers strip
	// it -- including this project's, which would then look for a file
	// whose name is one character shorter.
	if strings.HasPrefix(rel, "*") || strings.Contains(rel, "\\") {
		return domain.ValidationError(nil,
			"%s cannot be listed in %s: its name starts with %q or contains a backslash",
			rel, ports.SumsFileName, "*").
			WithHint("both are meaningful to `sha256sum -c`; rename the file")
	}
	return nil
}
