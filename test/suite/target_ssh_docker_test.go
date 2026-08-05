//go:build docker

package suite

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/morzecrew/morzer/internal/adapters/target/sftp"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/contract"
	"github.com/morzecrew/morzer/test/dockerlab"
)

// A real sshd, because the thing worth testing here cannot be faked: that a
// *changed* host key is refused. Only a server with a real host key can present
// a different one, and only a real handshake can reject it.
//
// The rest follows from that. A fake SSH server would be a fake that agrees with
// whatever the adapter believes about authentication, which is the one subject
// on which agreement proves nothing.

// sshServer is a running sshd with a key the adapter is allowed to use.
type sshServer struct {
	Container *dockerlab.Container
	Addr      string
	User      string

	// PrivateKey authenticates the client.
	PrivateKey string

	// KnownHosts is the server's real host key, read out of the container
	// rather than scanned over the network -- so a test that passes proves
	// the pin matched the server and not merely that two network reads
	// agreed.
	KnownHosts string
}

func startSSH(t *testing.T) *sshServer {
	t.Helper()
	dockerlab.Require(t)
	dockerlab.Pull(t, dockerlab.ImageOpenSSH)

	key, pub := generateSSHKey(t)

	container := dockerlab.Start(t, dockerlab.ImageOpenSSH, []int{2222}, map[string]string{
		"PUID":            "1000",
		"PGID":            "1000",
		"USER_NAME":       "backups",
		"PUBLIC_KEY":      pub,
		"SUDO_ACCESS":     "false",
		"PASSWORD_ACCESS": "false",
	})

	// Wait for the host key to exist, which is also what the client needs.
	container.WaitReady(t, 120*time.Second,
		"test", "-f", "/config/ssh_host_keys/ssh_host_ed25519_key.pub")

	addr := container.HostPort(t, 2222)

	// The server is up once it accepts a connection; the key file appearing
	// is necessary but not sufficient.
	waitForSSH(t, addr)

	// The pin is read repeatedly until a real handshake accepts it.
	//
	// This image's init writes the host keys and then starts sshd, and for a
	// short window the key on disk is not yet the key sshd is serving. A pin
	// read in that window produces a host-key mismatch -- which is exactly
	// the failure these tests are meant to detect deliberately, so a flaky
	// one here would be indistinguishable from a real regression in the very
	// check it would be hiding.
	server := &sshServer{Container: container, Addr: addr, User: "backups", PrivateKey: key}

	deadline := time.Now().Add(60 * time.Second)
	for {
		server.KnownHosts = readHostKey(t, container, addr)

		probe := sftp.New()
		_, err := probe.List(context.Background(), server.Ref(t, "probe"))
		_ = probe.Close()
		if err == nil {
			return server
		}
		if time.Now().After(deadline) {
			t.Fatalf("the pinned host key never matched what sshd served: %v", err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// readHostKey reads the server's own host key out of the container, rather than
// scanning it over the network.
//
// A keyscan would prove only that two network reads agree, which is precisely
// what an attacker on the path can arrange. Reading it from inside means a test
// that passes proves the pin matched the server.
func readHostKey(t *testing.T, container *dockerlab.Container, addr string) string {
	t.Helper()

	out, err := container.Exec(t, "cat", "/config/ssh_host_keys/ssh_host_ed25519_key.pub")
	require.NoError(t, err)

	fields := strings.Fields(strings.TrimSpace(out))
	require.GreaterOrEqual(t, len(fields), 2, "the host key file was not in the expected shape")

	// known_hosts wants `[host]:port keytype key`, because the target is on
	// a non-standard port.
	host, port, found := strings.Cut(addr, ":")
	require.True(t, found)

	return fmt.Sprintf("[%s]:%s %s %s", host, port, fields[0], fields[1])
}

// Credentials is what a target's secret would hold for this server.
func (s *sshServer) Credentials() ports.TargetCredentials {
	return ports.TargetCredentials{PrivateKey: s.PrivateKey, KnownHosts: s.KnownHosts}
}

// Ref builds a target URL under the server's writable directory.
func (s *sshServer) Ref(t *testing.T, dir string) ports.TargetRef {
	t.Helper()

	// /config is the writable volume in this image. `docker exec` runs as
	// root, so the directory has to be handed to the login user or every
	// push fails on permissions -- which would be a test fixture problem
	// masquerading as an adapter one.
	_, err := s.Container.Exec(t, "mkdir", "-p", "/config/"+dir)
	require.NoError(t, err)
	_, err = s.Container.Exec(t, "chown", "1000:1000", "/config/"+dir)
	require.NoError(t, err)

	ref, err := ports.TargetURL(fmt.Sprintf("ssh://%s@%s/config/%s", s.User, s.Addr, dir))
	require.NoError(t, err)
	return ref.WithCredentials(s.Credentials())
}

func TestBackupTargetContract_SSH(t *testing.T) {
	server := startSSH(t)

	var n int
	contract.RunBackupTargetSuite(t, func(t *testing.T) contract.BackupTargetHarness {
		n++
		dir := fmt.Sprintf("contract-%d", n)
		ref := server.Ref(t, dir)

		adapter := sftp.New()
		t.Cleanup(func() { _ = adapter.Close() })

		return contract.BackupTargetHarness{
			Target: adapter,
			Ref:    ref,
			Keys:   func() []string { return server.keys(t, dir) },
		}
	})
}

// keys lists the files under a target directory, read from inside the container
// so the suite sees what the transport actually left there.
func (s *sshServer) keys(t *testing.T, dir string) []string {
	t.Helper()

	out, err := s.Container.Exec(t, "find", "/config/"+dir, "-type", "f")
	if err != nil {
		return nil
	}

	var keys []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			keys = append(keys, strings.TrimPrefix(line, "/config/"+dir+"/"))
		}
	}
	return keys
}

// TestSSHRefusesAHostKeyThatIsNotThePinnedOne is the test this whole suite
// exists for, and it cannot be written against a fake.
//
// The harm a wrong host key does is not that an impostor reads the backup -- it
// is encrypted to this deployment's own age recipients and the impostor is not
// one of them. The harm is that an impostor can accept every push and answer
// every listing, and the operator would believe they had off-site backups while
// having none at all.
func TestSSHRefusesAHostKeyThatIsNotThePinnedOne(t *testing.T) {
	server := startSSH(t)
	ref := server.Ref(t, "wrong-host-key")

	// A syntactically valid pin for a different key: the shape of a
	// rebuilt host, and the shape of somebody in the middle.
	_, otherPub := generateSSHKey(t)
	creds := server.Credentials()
	host, port, _ := strings.Cut(server.Addr, ":")
	fields := strings.Fields(otherPub)
	creds.KnownHosts = fmt.Sprintf("[%s]:%s %s %s", host, port, fields[0], fields[1])

	adapter := sftp.New()
	t.Cleanup(func() { _ = adapter.Close() })

	_, err := adapter.List(context.Background(), ref.WithCredentials(creds))
	require.Error(t, err, "the adapter connected to a host whose key was not the pinned one")

	// The remedy has to distinguish the two cases, because they call for
	// opposite actions.
	hint := domain.AsError(err).Hint
	assert.Contains(t, hint, "rebuilt")
	assert.Contains(t, hint, "nothing should be")
}

// TestSSHRefusesATargetWithNoHostKeyPinned. There is no flag that reaches
// InsecureIgnoreHostKey, and the refusal names how to get the pin.
func TestSSHRefusesATargetWithNoHostKeyPinned(t *testing.T) {
	server := startSSH(t)
	ref := server.Ref(t, "no-pin")

	creds := server.Credentials()
	creds.KnownHosts = ""

	adapter := sftp.New()
	t.Cleanup(func() { _ = adapter.Close() })

	_, err := adapter.List(context.Background(), ref.WithCredentials(creds))
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Hint, "ssh-keyscan",
		"the refusal must say how to obtain the pin")
	assert.Contains(t, domain.AsError(err).Hint, "only as trustworthy as the network",
		"and must say why a keyscan alone is not enough")
}

// TestSSHRefusesAKeyTheServerDoesNotAccept, by name and with the remedy.
func TestSSHRefusesAKeyTheServerDoesNotAccept(t *testing.T) {
	server := startSSH(t)
	ref := server.Ref(t, "wrong-key")

	otherKey, _ := generateSSHKey(t)
	creds := server.Credentials()
	creds.PrivateKey = otherKey

	adapter := sftp.New()
	t.Cleanup(func() { _ = adapter.Close() })

	_, err := adapter.List(context.Background(), ref.WithCredentials(creds))
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Hint, "authorized_keys")
}

// TestSSHRefusesAPrivateKeyItCannotRead, without echoing the key material.
func TestSSHRefusesAPrivateKeyItCannotRead(t *testing.T) {
	server := startSSH(t)
	ref := server.Ref(t, "bad-key")

	const material = "-----BEGIN OPENSSH PRIVATE KEY-----\nTHISISNOTAKEYATALL\n"
	creds := server.Credentials()
	creds.PrivateKey = material

	adapter := sftp.New()
	t.Cleanup(func() { _ = adapter.Close() })

	_, err := adapter.List(context.Background(), ref.WithCredentials(creds))
	require.Error(t, err)
	assert.NotContains(t, err.Error()+domain.AsError(err).Hint, "THISISNOTAKEYATALL",
		"the refusal echoed the key material it failed to parse")
}

// TestSSHSurvivesATargetThatGoesAwayMidPush. The container is stopped while a
// push is in flight; what matters is that the push fails rather than reporting
// a backup that is not there.
func TestSSHSurvivesATargetThatGoesAwayMidPush(t *testing.T) {
	server := startSSH(t)
	ref := server.Ref(t, "goes-away")

	adapter := sftp.New()
	t.Cleanup(func() { _ = adapter.Close() })

	local := writeTestBackup(t, "20260101T000000Z", map[string]string{
		"database.sql.age": strings.Repeat("ciphertext", 1000),
	})

	// Connect first, so the failure is the transfer rather than the dial.
	_, err := adapter.List(context.Background(), ref)
	require.NoError(t, err)

	stop := exec.CommandContext(context.Background(), "docker", "stop", server.Container.Name)
	stopOut, err := stop.CombinedOutput()
	require.NoError(t, err, "cannot stop the target: %s", stopOut)

	_, err = adapter.Push(context.Background(), ref, local, "20260101T000000Z")
	require.Error(t, err,
		"a push to a host that disappeared reported success; the operator would "+
			"believe the backup was off the machine")
}

// generateSSHKey returns an ed25519 keypair in the formats sshd and the adapter
// each want: an OpenSSH private key, and one authorized_keys line.
//
// Generated in this process rather than by shelling out to `ssh-keygen`. These
// suites are meant to need a docker daemon and nothing else, and a host tool
// nobody declared is one a minimal runner does not have -- where the whole
// ssh:// suite would fail to start rather than report anything about the
// adapter. The formats are OpenSSH's either way: x/crypto writes them, and the
// sshd in the container is the thing that decides whether they are right.
func generateSSHKey(t *testing.T) (private, public string) {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	block, err := ssh.MarshalPrivateKey(priv, "morzer-test")
	require.NoError(t, err)

	signer, err := ssh.NewSignerFromKey(priv)
	require.NoError(t, err)

	return string(pem.EncodeToMemory(block)),
		strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))) + " morzer-test"
}

// waitForSSH blocks until the server accepts connections.
func waitForSSH(t *testing.T, addr string) {
	t.Helper()

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("sshd at %s never accepted a connection", addr)
}
