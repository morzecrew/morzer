package sftp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// A real sshd in a container proves the handshake. This proves everything that
// happens before one and everything that happens after: what is refused, and
// what a component path is allowed to be.
//
// The in-process server below is not a fake of ssh -- it is the real x/crypto
// server side, so a handshake that succeeds here succeeded for the same reasons
// it would against OpenSSH. What it cannot prove is interoperability, which is
// what the container suite is for.

// testKey is one keypair in the two shapes needed.
type testKey struct {
	Signer  ssh.Signer
	Private string
	Public  ssh.PublicKey
}

func newKey(t *testing.T) testKey {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return testKey{
		Signer:  signer,
		Private: string(pem.EncodeToMemory(block)),
		Public:  signer.PublicKey(),
	}
}

func knownHostsLine(t *testing.T, addr string, key ssh.PublicKey) string {
	t.Helper()

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	return "[" + host + "]:" + port + " " + string(ssh.MarshalAuthorizedKey(key))
}

func TestAPrivateKeyIsRequired(t *testing.T) {
	_, _, err := New().clientConfig(ports.TargetRef{Scheme: "ssh", URL: "ssh://host/backups"})
	if err == nil {
		t.Fatal("a target with no key was accepted")
	}
	if !strings.Contains(domain.AsError(err).Hint, "private_key") {
		t.Errorf("the hint must name the field: %q", domain.AsError(err).Hint)
	}
}

// TestAnUnreadablePrivateKeyIsRefusedWithoutEchoingIt. A parse error from a key
// parser can quote the material it failed on, and that material is the key.
func TestAnUnreadablePrivateKeyIsRefusedWithoutEchoingIt(t *testing.T) {
	const material = "-----BEGIN OPENSSH PRIVATE KEY-----\nSECRETLOOKINGGARBAGE\n"

	_, _, err := New().clientConfig(ports.TargetRef{
		Scheme: "ssh", URL: "ssh://host/backups",
		Credentials: ports.TargetCredentials{PrivateKey: material},
	})
	if err == nil {
		t.Fatal("a key that cannot be parsed was accepted")
	}
	if strings.Contains(err.Error()+domain.AsError(err).Hint, "SECRETLOOKINGGARBAGE") {
		t.Error("the refusal echoed the key material it failed to read")
	}
}

// TestAHostKeyPinIsRequired, and there is no flag anywhere that reaches
// InsecureIgnoreHostKey.
func TestAHostKeyPinIsRequired(t *testing.T) {
	key := newKey(t)

	_, _, err := New().clientConfig(ports.TargetRef{
		Scheme: "ssh", URL: "ssh://host/backups",
		Credentials: ports.TargetCredentials{PrivateKey: key.Private},
	})
	if err == nil {
		t.Fatal("a target that pins no host key was accepted, so anything on the " +
			"path could accept every push and answer every listing")
	}

	hint := domain.AsError(err).Hint
	for _, want := range []string{"ssh-keyscan", "only as trustworthy as the network"} {
		if !strings.Contains(hint, want) {
			t.Errorf("the hint does not mention %q: %q", want, hint)
		}
	}
}

func TestAMalformedPinIsRefused(t *testing.T) {
	key := newKey(t)

	_, _, err := New().clientConfig(ports.TargetRef{
		Scheme: "ssh", URL: "ssh://host/backups",
		Credentials: ports.TargetCredentials{
			PrivateKey: key.Private,
			KnownHosts: "this is not a known_hosts line",
		},
	})
	if err == nil {
		t.Fatal("a known_hosts value that pins nothing was accepted")
	}
}

// TestThePinDecidesWhichAlgorithmsAreOffered is the fix for a bug a real sshd
// found.
//
// A host with both an ed25519 and an RSA key offers whichever it prefers. A
// client that accepts any algorithm but pins only ed25519 gets offered RSA and
// reports a mismatch -- a refusal identical to the one a real attack produces,
// for a server doing nothing wrong. An operator who sees that twice for no
// reason learns to ignore the one check that matters.
func TestThePinDecidesWhichAlgorithmsAreOffered(t *testing.T) {
	key := newKey(t)
	pin := knownHostsLine(t, "127.0.0.1:2222", key.Public)

	cfg, _, err := New().clientConfig(ports.TargetRef{
		Scheme: "ssh", URL: "ssh://host/backups",
		Credentials: ports.TargetCredentials{PrivateKey: key.Private, KnownHosts: pin},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.HostKeyAlgorithms) != 1 || cfg.HostKeyAlgorithms[0] != ssh.KeyAlgoED25519 {
		t.Errorf("HostKeyAlgorithms = %v, want just ed25519", cfg.HostKeyAlgorithms)
	}
}

// TestAnRSAPinAllowsTheSHA2Signatures. A key recorded as `ssh-rsa` is verified
// today with rsa-sha2-256 or rsa-sha2-512, because SHA-1 signatures are refused
// by every current OpenSSH. Pinning the recorded name alone would reject a
// modern server for being modern.
func TestAnRSAPinAllowsTheSHA2Signatures(t *testing.T) {
	got := algorithmsFor(ssh.KeyAlgoRSA)

	for _, want := range []string{ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSASHA512} {
		var found bool
		for _, algo := range got {
			if algo == want {
				found = true
			}
		}
		if !found {
			t.Errorf("an ssh-rsa pin does not allow %s, so a current OpenSSH would "+
				"be refused for not offering SHA-1", want)
		}
	}
}

func TestSeveralPinnedKeysAllowSeveralAlgorithms(t *testing.T) {
	first, second := newKey(t), newKey(t)
	pin := knownHostsLine(t, "127.0.0.1:22", first.Public) +
		knownHostsLine(t, "127.0.0.1:22", second.Public)

	algorithms, err := pinnedAlgorithms(pin)
	if err != nil {
		t.Fatal(err)
	}
	// Both are ed25519 here, so the point is that the list is deduplicated
	// rather than that it has two entries: a duplicate would be sent twice
	// in the handshake.
	if len(algorithms) != 1 {
		t.Errorf("algorithms = %v, want one deduplicated entry", algorithms)
	}
}

// TestARefusedKeyNamesAuthorizedKeys, because "unable to authenticate" alone
// leaves an operator guessing between the key, the user and the server.
func TestARefusedKeyNamesAuthorizedKeys(t *testing.T) {
	err := New().handshakeError(ports.TargetRef{Scheme: "ssh", URL: "ssh://host/x"},
		errNoAuth{})
	if !strings.Contains(domain.AsError(err).Hint, "authorized_keys") {
		t.Errorf("hint = %q", domain.AsError(err).Hint)
	}
}

type errNoAuth struct{}

func (errNoAuth) Error() string {
	return "ssh: handshake failed: unable to authenticate, attempted methods [none publickey]"
}

// TestAComponentPathCannotEscapeTheTarget.
//
// A manifest on a target is a file this manager may not have written -- that is
// the whole premise of a target being somewhere else. A component path of
// "../../.ssh/authorized_keys" must not be a way for whoever controls the target
// to decide what a fetch reads or a removal deletes.
func TestAComponentPathCannotEscapeTheTarget(t *testing.T) {
	store := &sftpStore{root: "/srv/backups"}

	for name, key := range map[string]string{
		"a parent reference":   "../../.ssh/authorized_keys",
		"one buried in a path": "20260101T000000Z/../../../etc/shadow",
		"an empty key":         "",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.resolve(key); err == nil {
				t.Fatalf("resolve(%q) was accepted", key)
			}
		})
	}

	got, err := store.resolve("20260101T000000Z/database.sql.age")
	if err != nil {
		t.Fatal(err)
	}
	if want := "/srv/backups/20260101T000000Z/database.sql.age"; got != want {
		t.Errorf("resolve = %q, want %q", got, want)
	}
}

// TestCloseIsSafeWithNothingOpen, because the CLI calls it at the end of every
// command whether or not a target was ever reached.
func TestCloseIsSafeWithNothingOpen(t *testing.T) {
	if err := New().Close(); err != nil {
		t.Fatalf("closing an unused target failed: %v", err)
	}
}

// TestATargetThatCannotBeDialledSaysSo, rather than surfacing a bare syscall
// error.
func TestATargetThatCannotBeDialledSaysSo(t *testing.T) {
	key := newKey(t)
	pin := knownHostsLine(t, "127.0.0.1:1", key.Public)

	ref := ports.TargetRef{
		Scheme: "ssh", Host: "127.0.0.1:1", Path: "/backups", User: "ops",
		URL:         "ssh://ops@127.0.0.1:1/backups",
		Credentials: ports.TargetCredentials{PrivateKey: key.Private, KnownHosts: pin},
	}

	target := New().WithDialer(func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, net.UnknownNetworkError("nothing is listening")
	})

	_, err := target.List(context.Background(), ref)
	if err == nil {
		t.Fatal("a target that could not be dialled reported success")
	}
	if !strings.Contains(domain.AsError(err).Hint, "ssh port") {
		t.Errorf("hint = %q", domain.AsError(err).Hint)
	}
}
