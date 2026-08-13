//go:build docker

package suite

import (
	"context"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	signer "github.com/morzecrew/morzer/internal/adapters/sign/minisign"
	"github.com/morzecrew/morzer/test/dockerlab"
)

// The minisign secret-key file format is written by hand -- go-minisign parses
// and signs but cannot generate or encode a key -- so it is the one piece of
// RFC 0028 with nothing but our own reading behind it.
//
// Checking it with our own parser would prove only that we are self-consistent:
// a layout wrong in the same way on both sides passes every such test. So these
// run the **real `minisign` binary**, in both directions. If the two ever
// disagree, this is where it surfaces, rather than in an operator's terminal
// when they try to verify an artifact this machine signed.

const minisignImage = "alpine:3.20"

// minisignRun runs the real minisign binary over a directory of files.
func minisignRun(t *testing.T, dir string, argv ...string) (string, error) {
	t.Helper()
	args := []string{
		"run", "--rm", "-v", dir + ":/w", "-w", "/w", minisignImage,
		"sh", "-c",
		"apk add --no-cache minisign >/dev/null 2>&1 && " + strings.Join(argv, " "),
	}
	out, err := osexec.CommandContext(context.Background(), "docker", args...).CombinedOutput()
	return string(out), err
}

// TestTheRealMinisignVerifiesWhatWeSign is the assertion the format work would
// be wrong without.
func TestTheRealMinisignVerifiesWhatWeSign(t *testing.T) {
	dockerlab.Require(t)

	dir := t.TempDir()
	s := signer.New(filepath.Join(dir, "identity.key"), "demo")

	pub, err := s.EnsureKey(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, pub.Line)

	payload := []byte("the bytes this machine is attesting to\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "artifact"), payload, 0o644))

	sig, err := s.Sign(context.Background(), payload, "morzer demo apply 2026-08-13")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "artifact.minisig"), sig.Encoded, 0o644))

	out, err := minisignRun(t, dir, "minisign", "-Vm", "artifact", "-P", pub.Line)
	require.NoError(t, err, "the real minisign rejected our signature:\n%s", out)
	require.Contains(t, out, "Signature and comment signature verified", out)

	// The trusted comment travels inside the signature and minisign prints
	// it, which is what makes it usable to somebody holding only the
	// .minisig file.
	require.Contains(t, out, "morzer demo apply", out)
}

// TestTheRealMinisignRejectsATamperedArtifact is the verified-red half: the
// test above passes for a signature that verifies nothing if the verification
// is not actually happening.
func TestTheRealMinisignRejectsATamperedArtifact(t *testing.T) {
	dockerlab.Require(t)

	dir := t.TempDir()
	s := signer.New(filepath.Join(dir, "identity.key"), "demo")
	pub, err := s.EnsureKey(context.Background())
	require.NoError(t, err)

	payload := []byte("the bytes this machine is attesting to\n")
	sig, err := s.Sign(context.Background(), payload, "morzer demo")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "artifact.minisig"), sig.Encoded, 0o644))

	// One byte of the signed content.
	tampered := append([]byte(nil), payload...)
	tampered[0] ^= 0x01
	require.NoError(t, os.WriteFile(filepath.Join(dir, "artifact"), tampered, 0o644))

	out, err := minisignRun(t, dir, "minisign", "-Vm", "artifact", "-P", pub.Line)
	require.Error(t, err, "minisign accepted a tampered artifact:\n%s", out)
}

// TestTheRealMinisignRejectsATamperedSignature corrupts the other side.
func TestTheRealMinisignRejectsATamperedSignature(t *testing.T) {
	dockerlab.Require(t)

	dir := t.TempDir()
	s := signer.New(filepath.Join(dir, "identity.key"), "demo")
	pub, err := s.EnsureKey(context.Background())
	require.NoError(t, err)

	payload := []byte("the bytes this machine is attesting to\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "artifact"), payload, 0o644))

	sig, err := s.Sign(context.Background(), payload, "morzer demo")
	require.NoError(t, err)

	// Flip a character inside the base64 signature line, keeping the file's
	// shape so minisign fails on the signature rather than on the parse.
	lines := strings.Split(string(sig.Encoded), "\n")
	require.GreaterOrEqual(t, len(lines), 2)
	body := []byte(lines[1])
	if body[10] == 'A' {
		body[10] = 'B'
	} else {
		body[10] = 'A'
	}
	lines[1] = string(body)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "artifact.minisig"),
		[]byte(strings.Join(lines, "\n")), 0o644))

	out, err := minisignRun(t, dir, "minisign", "-Vm", "artifact", "-P", pub.Line)
	require.Error(t, err, "minisign accepted a tampered signature:\n%s", out)
}

// TestWeCanUseAKeyTheRealMinisignGenerated checks the format from the other
// direction.
//
// Verifying our signatures proves our *encoder* agrees with minisign's decoder.
// It does not prove our decoder agrees with minisign's encoder, and a machine
// whose key was minted by an operator running `minisign -G` is a machine this
// manager should still be able to sign with.
func TestWeCanUseAKeyTheRealMinisignGenerated(t *testing.T) {
	dockerlab.Require(t)

	dir := t.TempDir()
	// chmod after generating: minisign writes the secret key 0400 owned by
	// the container's root, and the test process is not root on the host.
	// The mode is what this manager asserts elsewhere; here it is in the way.
	out, err := minisignRun(t, dir,
		"minisign", "-G", "-W", "-p", "minisign.pub", "-s", "minisign.key",
		"&&", "chmod", "0644", "minisign.key", "minisign.pub")
	require.NoError(t, err, out)

	// The public key file's second line is the key itself.
	pubFile, err := os.ReadFile(filepath.Join(dir, "minisign.pub"))
	require.NoError(t, err)
	pubLines := strings.Split(strings.TrimSpace(string(pubFile)), "\n")
	require.Len(t, pubLines, 2)
	theirPub := strings.TrimSpace(pubLines[1])

	s := signer.New(filepath.Join(dir, "minisign.key"), "demo")

	// It must read as an existing key, not mint over it.
	ours, err := s.PublicKey(context.Background())
	require.NoError(t, err)
	require.Equal(t, theirPub, ours.Line,
		"we derived a different public key than minisign wrote")

	payload := []byte("signed with a key minisign generated\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "artifact"), payload, 0o644))
	sig, err := s.Sign(context.Background(), payload, "morzer demo")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "artifact.minisig"), sig.Encoded, 0o644))

	out, err = minisignRun(t, dir, "minisign", "-Vm", "artifact", "-P", theirPub)
	require.NoError(t, err, out)
	require.Contains(t, out, "Signature and comment signature verified", out)
}

// TestEnsureKeyDoesNotReplaceAnExistingKey pins the idempotence that makes the
// step safe to run before every signing operation.
//
// The failure it guards is silent: a second mint produces a working key and a
// working signature, and only an *old* artifact stops verifying.
func TestEnsureKeyDoesNotReplaceAnExistingKey(t *testing.T) {
	dockerlab.Require(t)

	dir := t.TempDir()
	s := signer.New(filepath.Join(dir, "identity.key"), "demo")

	first, err := s.EnsureKey(context.Background())
	require.NoError(t, err)

	payload := []byte("signed before the second call\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "artifact"), payload, 0o644))
	sig, err := s.Sign(context.Background(), payload, "morzer demo")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "artifact.minisig"), sig.Encoded, 0o644))

	second, err := s.EnsureKey(context.Background())
	require.NoError(t, err)
	require.Equal(t, first.Line, second.Line, "EnsureKey minted a second key")

	// The artifact signed before the second call still verifies, which is
	// the consequence the equality above is standing in for.
	out, err := minisignRun(t, dir, "minisign", "-Vm", "artifact", "-P", second.Line)
	require.NoError(t, err, out)
}
