// Package signed implements ports.ManagerUnpacker: it decides whether a
// downloaded manager release is one this project published, and only then
// hands out the binary inside it (RFC 0046 §5.3).
//
// The key is a copy of `morzer.pub` compiled into the binary, the one
// `install.sh` embeds, and it is the only key: no flag, setting or environment
// variable replaces it or makes the signature optional (decision 6). A test
// asserts the copy is the repository's file byte for byte. The signature is
// checked in-process, so unlike `install.sh` this needs no `minisign` tool and
// has no branch that skips it.
package signed

import (
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	gominisign "github.com/jedisct1/go-minisign"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// ReleaseKey is `morzer.pub`, the key the release pipeline signs with.
//
//go:embed morzer.pub
var ReleaseKey []byte

// versionTimeout bounds asking a binary its version, which reads no state.
const versionTimeout = 30 * time.Second

// Unpacker verifies manager releases against one key.
type Unpacker struct {
	key    string
	runner exec.Runner
}

// New verifies against ReleaseKey.
func New(runner exec.Runner) *Unpacker { return ForKey(string(ReleaseKey), runner) }

// ForKey verifies against another key, which is what tests sign with. Nothing
// outside tests calls it: decision 6 is that a self-update trusts the one key
// this binary was built with.
func ForKey(key string, runner exec.Runner) *Unpacker {
	return &Unpacker{key: key, runner: runner}
}

var _ ports.ManagerUnpacker = (*Unpacker)(nil)

// Target implements ports.ManagerUnpacker.
func (u *Unpacker) Target(dir string) (domain.Version, error) {
	lines, err := sums(dir)
	if err != nil {
		return domain.Version{}, err
	}
	suffix := "_linux_" + runtime.GOARCH + ".tar.zst"
	var found []domain.Version
	for name := range lines {
		middle, ok := strings.CutPrefix(name, "morzer_")
		if !ok {
			continue
		}
		if middle, ok = strings.CutSuffix(middle, suffix); !ok {
			continue
		}
		if v, err := domain.ParseVersion(middle); err == nil && ports.ManagerArchive(v) == name {
			found = append(found, v)
		}
	}
	switch len(found) {
	case 0:
		return domain.Version{}, domain.ValidationError(nil,
			"%s names no morzer archive for linux/%s", filepath.Join(dir, ports.SumsFileName), runtime.GOARCH).
			WithHint("download the release's morzer_<version>%s, %s and %s into one directory",
				suffix, ports.SumsFileName, ports.SignatureFileName)
	case 1:
		return found[0], nil
	default:
		return domain.Version{}, domain.ValidationError(nil,
			"%s names more than one morzer archive for linux/%s, so it is not one release's",
			filepath.Join(dir, ports.SumsFileName), runtime.GOARCH).
			WithHint("use the %s published with the release", ports.SumsFileName)
	}
}

// Unpack implements ports.ManagerUnpacker.
func (u *Unpacker) Unpack(ctx context.Context, dir string, v domain.Version, dst string) error {
	// Read once, and the bytes the signature covers are the bytes the
	// archive is checked against: dir may be one somebody else can write.
	signed, err := u.verifiedSums(dir)
	if err != nil {
		return err
	}

	archive := ports.ManagerArchive(v)
	// The archive's own line, never the whole file: SHA256SUMS covers every
	// platform's archive, and only this one was fetched.
	want, ok := parseSums(signed)[archive]
	if !ok {
		return domain.ValidationError(nil, "%s has no line for %s", ports.SumsFileName, archive).
			WithHint("the release does not carry an archive for linux/%s", runtime.GOARCH)
	}

	work, err := os.MkdirTemp("", "morzer-self-update-")
	if err != nil {
		return domain.Internal(err, "cannot make a directory to unpack %s into", archive)
	}
	defer func() { _ = os.RemoveAll(work) }()

	// Copied before it is checked, for the same reason: what is unpacked is
	// the copy that was checked, not whatever is in dir by then.
	private := filepath.Join(work, archive)
	if err := copyFile(filepath.Join(dir, archive), private, 0o600); err != nil {
		return err
	}
	got, err := atomicfs.DigestFile(private)
	if err != nil {
		return err
	}
	if !atomicfs.SameDigest(got, want) {
		return domain.ValidationError(domain.ErrDigestMismatch, "%s does not match its line in %s", archive, ports.SumsFileName).
			WithHint("do not install it; download the release again from https://github.com/morzecrew/morzer/releases")
	}

	unpacked := filepath.Join(work, "unpacked")
	if err := atomicfs.ExtractTarZst(private, unpacked, atomicfs.DefaultExtractLimits()); err != nil {
		return err
	}
	binary := filepath.Join(unpacked, "morzer")
	if _, err := os.Stat(binary); err != nil {
		return domain.ValidationError(err, "the archive does not contain a morzer binary")
	}
	return copyFile(binary, dst, 0o755)
}

// verifiedSums reads SumsFileName and checks SignatureFileName over it with
// the one key. Never skipped: a release without a signature is refused.
func (u *Unpacker) verifiedSums(dir string) ([]byte, error) {
	key, err := gominisign.NewPublicKey(strings.TrimSpace(lastLine(u.key)))
	if err != nil {
		return nil, domain.Internal(err, "the release key this binary carries does not parse")
	}
	sigPath := filepath.Join(dir, ports.SignatureFileName)
	sig, err := gominisign.NewSignatureFromFile(sigPath)
	if err != nil {
		return nil, domain.ValidationError(err, "cannot read the signature at %s", sigPath).
			WithHint("a release carries %s beside %s; a self-update never installs without it",
				ports.SignatureFileName, ports.SumsFileName)
	}
	signed, err := os.ReadFile(filepath.Join(dir, ports.SumsFileName)) //nolint:gosec // the release directory this command was given
	if err != nil {
		return nil, domain.ValidationError(err, "cannot read %s", filepath.Join(dir, ports.SumsFileName))
	}
	if ok, err := key.Verify(signed, sig); !ok || err != nil {
		return nil, domain.ValidationError(domain.ErrValidation,
			"%s is not signed by the key this manager carries", ports.SumsFileName).
			WithHint("do not install it. The release was signed by a different key, or the files were " +
				"changed; check the release page before installing anything: https://github.com/morzecrew/morzer/releases")
	}
	return signed, nil
}

// Version implements ports.ManagerUnpacker.
func (u *Unpacker) Version(ctx context.Context, path string) (domain.Version, error) {
	res, err := u.runner.Run(ctx, exec.Command{
		Argv:          []string{path, "version", "--json"},
		Timeout:       versionTimeout,
		CaptureOutput: true,
	})
	if err != nil {
		return domain.Version{}, domain.ValidationError(err, "%s does not run on this machine", path)
	}
	var out struct {
		Data struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
		return domain.Version{}, domain.ValidationError(err, "%s does not answer `version --json`", path)
	}
	v, err := domain.ParseVersion(out.Data.Version)
	if err != nil {
		return domain.Version{}, domain.ValidationError(err, "%s reports %q, which is not a release version", path, out.Data.Version)
	}
	return v, nil
}

// sums reads dir's SumsFileName, unverified, which is enough to name a
// version that Unpack then verifies.
func sums(dir string) (map[string]string, error) {
	path := filepath.Join(dir, ports.SumsFileName)
	data, err := os.ReadFile(path) //nolint:gosec // the release directory this command was given
	if err != nil {
		return nil, domain.ValidationError(err, "cannot read %s", path)
	}
	return parseSums(data), nil
}

// parseSums reads `sha256sum` output as name to digest.
func parseSums(data []byte) map[string]string {
	out := map[string]string{}
	for line := range strings.Lines(string(data)) {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		// "<hash>  <name>" in text mode, "<hash> *<name>" in binary mode.
		out[strings.TrimPrefix(fields[1], "*")] = fields[0]
	}
	return out
}

// lastLine is the key line of a `.pub` file, past its untrusted comment.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// copyFile writes src to dst, which must not exist, and syncs it.
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src) //nolint:gosec // a release file this command was given, or one this package unpacked
	if err != nil {
		return domain.ValidationError(err, "cannot read %s", src)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode) //nolint:gosec // the binary is an executable, which is the point
	if err != nil {
		return domain.Internal(err, "cannot write %s", dst)
	}
	// The umask applied to the mode above; a root shell's 077 would leave a
	// manager nobody else can run.
	if err := out.Chmod(mode); err != nil {
		_ = out.Close()
		return domain.Internal(err, "cannot set the mode of %s", dst)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return domain.Internal(err, "cannot write %s", dst)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return domain.Internal(err, "cannot write %s", dst)
	}
	return out.Close()
}
