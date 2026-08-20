package atomicfs

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/morzecrew/morzer/internal/domain"
)

// randomSuffix names temporary files. crypto/rand rather than math/rand
// because these land in shared directories where a predictable name is a
// symlink-attack invitation.
func randomSuffix() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read does not fail on any supported platform; if it
		// somehow did, a constant suffix still works because creation
		// uses O_EXCL and would simply retry at a higher level.
		return "fallback"
	}
	return hex.EncodeToString(b[:])
}

// ExtractLimits bounds what may come out of an archive or a copied tree.
// Without them, a bundle can exhaust the disk or the inode table before
// anything validates it.
type ExtractLimits struct {
	MaxEntries   int
	MaxTotalSize int64
	MaxFileSize  int64

	// FirstEntry, when set, is the archive entry that must come first, and
	// Budget reads a declared uncompressed size out of its bytes.
	//
	// Together they turn ExtractTarZst from an extractor of arbitrary
	// archives into an extractor of *release bundles*, which is the only
	// thing it is used for -- but the knowledge of what a release bundle's
	// manifest is called and how to parse it belongs to the release
	// package, which depends on this one. Passing it in is what keeps the
	// dependency pointing the one way it can.
	//
	// Budget returns the declared size, or 0 for a manifest that declares
	// none. An error refuses the archive: a manifest this cannot read is
	// not one whose budget can be trusted.
	FirstEntry string
	Budget     func(manifest []byte) (int64, error)
}

// DefaultExtractLimits are generous for a real product bundle and still far
// below what it takes to fill a disk.
//
// They are also what a bundle carrying container images cannot live within: a
// single `postgres` layout is 115 M with a 110 M dominant blob, and a
// self-contained stack lands in the low gigabytes. Such a bundle raises its own
// ceiling by declaring `bundle.uncompressed_size`, bounded by the hard caps
// below -- see negotiateLimits.
func DefaultExtractLimits() ExtractLimits {
	return ExtractLimits{
		MaxEntries:   20_000,
		MaxTotalSize: 2 << 30, // 2 GiB
		MaxFileSize:  1 << 30, // 1 GiB
	}
}

// Hard caps on what a declared budget may ask for.
//
// Extraction happens *before* the signature is verified, so these limits are
// the only thing standing between a hostile archive and the disk, and the
// signature cannot be the mitigation because it is checked afterwards. A
// declaration read out of those same unverified bytes may therefore only ever
// *lower* the ceiling: an attacker who could raise it would simply declare
// whatever they needed.
//
// A bundle above these is refused and says so, which is the correct answer for
// a limit that exists to bound unverified input. Raising them is a change to
// the manager, made once, in the open -- not something a bundle can request.
const (
	HardMaxTotalSize = 50 << 30 // 50 GiB
	HardMaxFileSize  = 5 << 30  // 5 GiB
)

// CopyTree copies src into dst, refusing anything that is not a regular file
// or a directory.
//
// Symlinks, device nodes, sockets, and FIFOs are rejected rather than skipped:
// a bundle containing one is either broken or hostile, and copying "most of"
// such a bundle silently would produce a release that behaves differently from
// the one that was verified.
func CopyTree(src, dst string, limits ExtractLimits) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return domain.ValidationError(err, "cannot read source directory %s", src)
	}
	if !srcInfo.IsDir() {
		return domain.ValidationError(nil, "%s is not a directory", src)
	}
	if err := MkdirAll(dst, 0o755); err != nil {
		return err
	}

	root, err := OpenRoot(dst)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	// The read side is contained too. The walk decides "regular file" from
	// an lstat, and the copy opened the path again by name: a file swapped
	// for a symlink in between was followed, and the digest computed
	// afterwards blessed whatever came back.
	//
	// os.Root is not enough here. It refuses an *escape* and follows a
	// symlink that stays inside the tree, so a swapped entry pointing at
	// another file in the same bundle would still be opened as its target.
	// The descent below refuses a symlink at every component instead.
	srcDir, err := openDirNoFollow(src)
	if err != nil {
		return domain.ValidationError(err, "cannot read source directory %s", src)
	}
	defer func() { _ = unix.Close(srcDir) }()

	var entries int
	var total int64

	walkErr := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return domain.Internal(err, "cannot walk %s", path)
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return domain.Internal(relErr, "cannot relativise %s", path)
		}
		if rel == "." {
			return nil
		}
		// The source tree is not the release (RFC 0014 decision 18).
		// The one caller of this is the local bundle source, so a
		// working copy staged from a directory would otherwise carry
		// `.git` onto the operator's disk as well as into a published
		// archive. Skipped before the entry is counted: a repository's
		// object store is exactly the thing that would spend the
		// bundle's entry budget on files no release contains.
		if domain.IsExcludedFromBundle(filepath.ToSlash(rel)) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		entries++
		if limits.MaxEntries > 0 && entries > limits.MaxEntries {
			return domain.ValidationError(nil,
				"bundle exceeds the entry limit of %d files", limits.MaxEntries).
				WithHint("this bundle is unusually large; verify it came from the vendor you expect")
		}

		switch {
		case d.IsDir():
			return mkdirAllIn(root, rel, 0o755)

		case d.Type()&fs.ModeSymlink != 0:
			return domain.ValidationError(nil,
				"bundle contains a symlink at %q, which is not allowed", rel).
				WithHint("release bundles must contain only regular files and directories")

		case !d.Type().IsRegular():
			return domain.ValidationError(nil,
				"bundle contains a non-regular file at %q (%s), which is not allowed", rel, d.Type())

		default:
			info, infoErr := d.Info()
			if infoErr != nil {
				return domain.Internal(infoErr, "cannot stat %s", path)
			}
			if limits.MaxFileSize > 0 && info.Size() > limits.MaxFileSize {
				return domain.ValidationError(nil,
					"bundle file %q is %d bytes, over the per-file limit of %d",
					rel, info.Size(), limits.MaxFileSize)
			}
			total += info.Size()
			if limits.MaxTotalSize > 0 && total > limits.MaxTotalSize {
				return domain.ValidationError(nil,
					"bundle exceeds the total size limit of %d bytes", limits.MaxTotalSize)
			}
			return copyFileIn(srcDir, root, rel, info.Mode().Perm())
		}
	})

	return walkErr
}

func copyFileIn(srcDir int, root *os.Root, rel string, mode fs.FileMode) error {
	in, err := openFileNoFollow(srcDir, rel)
	if err != nil {
		if errors.Is(err, errSymlinkComponent) {
			return domain.ValidationError(nil,
				"bundle contains a symlink at %q, which is not allowed", rel).
				WithHint("release bundles must contain only regular files and directories")
		}
		return domain.Internal(err, "cannot open %s", rel)
	}
	defer func() { _ = in.Close() }()

	// Checked on the open descriptor rather than on the path, which is the
	// other half of the same race: what was walked and what was opened are
	// now provably the same file.
	info, err := in.Stat()
	if err != nil {
		return domain.Internal(err, "cannot stat %s", rel)
	}
	if !info.Mode().IsRegular() {
		return domain.ValidationError(nil,
			"bundle contains a non-regular file at %q (%s), which is not allowed",
			rel, info.Mode().Type())
	}

	if dir := filepath.Dir(rel); dir != "." {
		if err := mkdirAllIn(root, dir, 0o755); err != nil {
			return err
		}
	}

	out, err := root.OpenFile(rel, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return domain.Internal(err, "cannot create %s", rel)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return domain.Internal(err, "cannot copy %s", rel)
	}
	if err := out.Chmod(mode); err != nil {
		_ = out.Close()
		return domain.Internal(err, "cannot set mode on %s", rel)
	}
	// Same reasoning as extractFile: the digest blesses what the cache
	// holds, so the bytes are flushed before anything promotes the tree.
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return domain.Internal(err, "cannot flush %s to disk", rel)
	}
	if err := out.Close(); err != nil {
		return domain.Internal(err, "cannot close %s", rel)
	}
	return nil
}

// errSymlinkComponent marks a path whose descent stopped at a symlink.
//
// The errno alone cannot say so: O_NOFOLLOW on the last component reports
// ELOOP, while O_NOFOLLOW|O_DIRECTORY on an intermediate one reports ENOTDIR --
// which is also what a regular file in the middle of a path reports. The
// descent asks, with a no-follow stat, and says which it was.
var errSymlinkComponent = errors.New("a path component is a symlink")

// openDirNoFollow opens a directory, refusing it if it is a symlink.
func openDirNoFollow(dir string) (int, error) {
	return unix.Open(dir,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
}

// openFileNoFollow opens rel beneath an already-open directory, refusing a
// symlink at *every* component rather than only at the last one.
//
// This is the read side of the copy's containment, and it is a descent rather
// than one open because that is what closes the window: the walk decided this
// entry was a regular file, and between that decision and this open anything
// could have replaced any part of the path. Following even one link would mean
// copying bytes the walk never saw -- under the name it did see, and into a
// tree whose digest is then computed and trusted.
//
// It needs no separate containment check. A path that follows no symlink and
// contains no "..", starting from a descriptor for the source directory, cannot
// name anything outside it.
func openFileNoFollow(dir int, rel string) (*os.File, error) {
	parts := strings.Split(filepath.ToSlash(rel), "/")

	current := dir
	// Closed only if this function opened it: the caller owns the one it
	// passed in.
	opened := false
	defer func() {
		if opened {
			_ = unix.Close(current)
		}
	}()

	for _, part := range parts[:len(parts)-1] {
		if part == "" || part == "." || part == ".." {
			return nil, domain.ValidationError(nil,
				"bundle path %q cannot be resolved safely", rel)
		}
		next, err := unix.Openat(current, part,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, classifyDescent(current, part, err)
		}
		if opened {
			_ = unix.Close(current)
		}
		current, opened = next, true
	}

	name := parts[len(parts)-1]
	if name == "" || name == "." || name == ".." {
		return nil, domain.ValidationError(nil,
			"bundle path %q cannot be resolved safely", rel)
	}
	// O_NONBLOCK, because this open is the far side of a race and the walk's
	// verdict is not binding: if a regular file has been replaced by a FIFO
	// since, a blocking open would wait for a writer that never comes and
	// hang the copy outright. Non-blocking, the descriptor opens, the caller
	// stats it, and a non-regular file is refused as it always was. On a
	// regular file the flag does nothing at all.
	fd, err := unix.Openat(current, name,
		unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, classifyDescent(current, name, err)
	}
	return os.NewFile(uintptr(fd), rel), nil
}

// classifyDescent says whether a failed open was a symlink refusal.
func classifyDescent(dir int, name string, cause error) error {
	var st unix.Stat_t
	if statErr := unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW); statErr != nil {
		return cause
	}
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		return fmt.Errorf("%w: %w", errSymlinkComponent, cause)
	}
	return cause
}

// DigestTree computes a content digest over a directory tree.
//
// The digest covers every file's relative path, permission bits, and contents,
// hashed in sorted path order so it is stable across filesystems and machines.
// Paths are included because moving a file changes the release even when no
// byte of content does; the executable bit is included because whether a hook
// can run is part of what the bundle is.
func DigestTree(dir string) (string, error) {
	type entry struct {
		rel  string
		mode fs.FileMode
		path string
	}
	var entries []entry

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return domain.Internal(err, "cannot walk %s", path)
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return domain.ValidationError(nil, "cannot digest non-regular file %s", path)
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return domain.Internal(relErr, "cannot relativise %s", path)
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return domain.Internal(infoErr, "cannot stat %s", path)
		}
		entries = append(entries, entry{rel: filepath.ToSlash(rel), mode: info.Mode().Perm(), path: path})
		return nil
	})
	if err != nil {
		return "", err
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })

	h := sha256.New()
	for _, e := range entries {
		// The separators keep the hash unambiguous: without them, a file
		// named "a" with content "bc" and one named "ab" with content
		// "c" would hash identically.
		_, _ = h.Write([]byte(e.rel))
		_, _ = h.Write([]byte{0})
		// Only the executable bit is recorded. Group and world bits vary
		// with the umask of whoever unpacked the bundle, and folding
		// them in would make the digest depend on the unpacking
		// environment rather than on the bundle.
		if e.mode&0o100 != 0 {
			_, _ = h.Write([]byte("x"))
		}
		_, _ = h.Write([]byte{0})

		f, openErr := os.Open(e.path)
		if openErr != nil {
			return "", domain.Internal(openErr, "cannot open %s", e.path)
		}
		if _, copyErr := io.Copy(h, f); copyErr != nil {
			_ = f.Close()
			return "", domain.Internal(copyErr, "cannot read %s", e.path)
		}
		_ = f.Close()
		_, _ = h.Write([]byte{0})
	}

	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// DigestFile computes the SHA-256 of a single file.
func DigestFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", domain.Internal(err, "cannot open %s", path)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", domain.Internal(err, "cannot read %s", path)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// SameDigest compares two digests, tolerating a missing "sha256:" prefix on
// either side so a checksum pasted from `sha256sum` output compares equal to
// one the manager produced.
func SameDigest(a, b string) bool {
	norm := func(s string) string {
		s = strings.TrimSpace(strings.ToLower(s))
		if i := strings.IndexByte(s, ':'); i >= 0 {
			s = s[i+1:]
		}
		return s
	}
	na, nb := norm(a), norm(b)
	return na != "" && na == nb
}

// FingerprintSecret produces a short, non-reversible identifier for a secret
// value, so two installations can be compared without either printing one.
func FingerprintSecret(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}

// DirSize sums the regular files under a path. Used by doctor's disk checks.
func DirSize(path string) (int64, error) {
	var total int64
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable subtree should not fail a diagnostic
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total, err
}
