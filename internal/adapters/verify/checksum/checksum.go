// Package checksum implements ports.Verifier using SHA-256.
//
// This is the v1 verifier. It answers "is this the bundle I was told to
// expect", not "did the vendor sign this" -- signature verification arrives as
// a second Verifier behind the same port, and installation policy decides
// whether one is required.
package checksum

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
)

// Name identifies this verifier in journal records and doctor output.
const Name = "sha256"

// SumsFileName is the checksum file a bundle may ship alongside itself. It is
// named in ports because the signature verifier reads the same file, and
// because a vendor's pipeline and `sha256sum -c` both depend on the name.
const SumsFileName = ports.SumsFileName

type Verifier struct{}

func New() *Verifier { return &Verifier{} }

var _ ports.Verifier = (*Verifier)(nil)

func (v *Verifier) Name() string { return Name }

// Verify checks a bundle against an expectation.
//
// Signatures are not this verifier's concern. It used to refuse when one was
// required, because nothing else could -- now the minisign verifier answers
// that question and this one answers only "is this the artifact I was told to
// expect". Keeping a policy decision in two adapters would mean two places
// could disagree about it.
func (v *Verifier) Verify(ctx context.Context, bundle ports.BundlePath, expect ports.Expectation) error {
	path := string(bundle)

	// A bundle that ships its own per-file checksums is checked against
	// them whether or not a digest was pinned. It is the link between a
	// signature -- which covers this file -- and the contents.
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		if err := VerifySumsFile(path); err != nil {
			return err
		}
	}

	actual, err := digestOf(path)
	if err != nil {
		return err
	}

	if expect.Digest == "" {
		// Nothing to compare against. The digest is still computed and
		// recorded by the caller, so a future run can detect a change
		// even though this one had no baseline.
		return nil
	}

	if !atomicfs.SameDigest(actual, expect.Digest) {
		return domain.ValidationError(domain.ErrDigestMismatch,
			"bundle verification failed: expected %s, got %s",
			shortDigest(expect.Digest), shortDigest(actual)).
			WithHint("the bundle does not match the digest it was published with. " +
				"Do not install it; re-download from the vendor and check the source.")
	}
	return nil
}

// digestOf hashes a bundle, whether it is a directory or a single archive.
func digestOf(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", domain.ValidationError(err, "cannot read %s for verification", path)
	}
	if info.IsDir() {
		return atomicfs.DigestTree(path)
	}
	return atomicfs.DigestFile(path)
}

// VerifySumsFile checks a bundle directory against a SHA256SUMS file it ships.
//
// This complements the tree digest: the sums file lets a vendor publish
// per-file checksums that a third party can verify with `sha256sum -c`,
// without needing the manager at all.
//
// The list must also be complete -- see unlisted. A sums file that names only
// some of the bundle proves only that those files are unmodified, which is not
// the claim the signature chain is documented to make.
func VerifySumsFile(dir string) error {
	sumsPath := filepath.Join(dir, SumsFileName)
	data, err := os.ReadFile(sumsPath)
	if err != nil {
		// Absent is fine: the file is optional.
		return nil
	}

	listed := make(map[string]bool)
	var problems []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Format: "<hex>  <path>", the second space possibly a "*" for
		// binary mode, as produced by sha256sum.
		fields := strings.Fields(line)
		if len(fields) < 2 {
			problems = append(problems, "malformed line: "+truncate(line, 60))
			continue
		}
		want := fields[0]
		name := strings.TrimPrefix(strings.Join(fields[1:], " "), "*")

		// A checksum entry pointing outside the bundle would have the
		// verifier reading host files on the bundle's instruction.
		clean := filepath.Clean(name)
		if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
			problems = append(problems, name+": path escapes the bundle")
			continue
		}

		listed[clean] = true

		got, err := atomicfs.DigestFile(filepath.Join(dir, clean))
		if err != nil {
			problems = append(problems, name+": missing or unreadable")
			continue
		}
		if !atomicfs.SameDigest(got, want) {
			problems = append(problems, name+": checksum mismatch")
		}
	}

	problems = append(problems, unlisted(dir, listed)...)

	if len(problems) > 0 {
		return domain.ValidationError(domain.ErrDigestMismatch,
			"%s does not match the bundle contents:\n  - %s",
			SumsFileName, strings.Join(problems, "\n  - ")).
			WithHint("every file in the bundle must be listed in %s and match it; "+
				"a file the list does not cover is a file the signature does not cover",
				SumsFileName)
	}
	return nil
}

// unlisted reports bundle files that SHA256SUMS does not mention.
//
// Checking only the listed files makes the sums file prove something much
// weaker than the documented chain -- signature → SHA256SUMS → every file. A
// mirror that *adds* a file rather than editing one passes every other check:
// the signature covers the sums file and still verifies, the listed files all
// match, and in the default posture the digest "expectation" is the bundle's
// own self-computed digest. The added file can be the manifest naming a
// root-run hook.
//
// So completeness is enforced, and enforced fail-closed. It only binds bundles
// that ship a sums file at all -- publishing one is the vendor's claim that it
// covers the bundle, and the publishing documentation has always described
// producing it over everything.
//
// Directories carry no content of their own and are not listed by `sha256sum`;
// everything else in the tree must appear, symlinks included, because a symlink
// is read through by whatever opens it.
func unlisted(dir string, listed map[string]bool) []string {
	var problems []string
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		// The list cannot list itself, and the signature over it is
		// what makes the list trustworthy in the first place.
		if rel == SumsFileName || rel == ports.SignatureFileName {
			return nil
		}
		if !listed[filepath.Clean(rel)] {
			problems = append(problems, filepath.ToSlash(rel)+
				": present in the bundle but not listed")
		}
		return nil
	})
	if err != nil {
		// An unreadable tree cannot be shown to be covered, which is the
		// same answer as not being covered.
		problems = append(problems, "the bundle cannot be read in full: "+err.Error())
	}
	return problems
}

func shortDigest(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) <= 16 {
		return d
	}
	return "sha256:" + d[:16] + "…"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
