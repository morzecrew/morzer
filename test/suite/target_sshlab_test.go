package suite

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	pkgsftp "github.com/pkg/sftp"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/morzecrew/morzer/internal/adapters/target/sftp"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/contract"
)

// TestBackupTargetContract_SSHInProcess runs the shared suite against an SSH
// server in this process.
//
// The container suite proves the adapter works with OpenSSH; this proves it
// works at all, and proves it in the default `go test` run rather than only
// behind a build tag. The server is the real x/crypto server side driving the
// real sftp subsystem, so a transfer that succeeds here succeeded for the same
// reasons it would against a real host -- what it cannot prove is
// interoperability, which is exactly what the container is for.
func TestBackupTargetContract_SSHInProcess(t *testing.T) {
	client := newSSHKey(t)
	host := newSSHKey(t)

	root := t.TempDir()
	addr := startInProcessSSH(t, host, client.public, root)

	contract.RunBackupTargetSuite(t, func(t *testing.T) contract.BackupTargetHarness {
		dir := filepath.Join(root, t.Name())
		require.NoError(t, os.MkdirAll(dir, 0o700))

		ref := ports.TargetRef{
			Scheme: "ssh", Host: addr, Path: dir, User: "ops",
			URL: "ssh://ops@" + addr + dir,
			Credentials: ports.TargetCredentials{
				PrivateKey: client.private,
				KnownHosts: sshKnownHostsLine(t, addr, host.public),
			},
		}

		adapter := sftp.New()
		t.Cleanup(func() { _ = adapter.Close() })

		return contract.BackupTargetHarness{
			Target: adapter,
			Ref:    ref,
			Keys:   func() []string { return walkKeys(t, dir) },
		}
	})
}

type sshKey struct {
	signer  ssh.Signer
	private string
	public  ssh.PublicKey
}

func newSSHKey(t *testing.T) sshKey {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	block, err := ssh.MarshalPrivateKey(priv, "")
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(priv)
	require.NoError(t, err)

	return sshKey{signer: signer, private: string(pem.EncodeToMemory(block)), public: signer.PublicKey()}
}

func sshKnownHostsLine(t *testing.T, addr string, key ssh.PublicKey) string {
	t.Helper()

	host, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	return "[" + host + "]:" + port + " " + string(ssh.MarshalAuthorizedKey(key))
}

// startInProcessSSH runs an SSH server with an sftp subsystem, serving the
// whole filesystem the way a real host does.
func startInProcessSSH(t *testing.T, host sshKey, authorized ssh.PublicKey, root string) string {
	t.Helper()

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) != string(authorized.Marshal()) {
				// A plain rejection, not ssh.ErrNoAuth: that sentinel
				// means "no auth was attempted", and a server that
				// returned it for a wrong key would let the adapter's
				// diagnosis of the two cases go untested.
				return nil, fmt.Errorf("unknown public key for %q", "ops")
			}
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(host.signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go serveSSH(conn, cfg)
		}
	}()

	return listener.Addr().String()
}

func serveSSH(conn net.Conn, cfg *ssh.ServerConfig) {
	defer func() { _ = conn.Close() }()

	sshConn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer func() { _ = sshConn.Close() }()
	go ssh.DiscardRequests(reqs)

	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		channel, requests, acceptErr := newChannel.Accept()
		if acceptErr != nil {
			return
		}

		go func(in <-chan *ssh.Request) {
			for req := range in {
				ok := req.Type == "subsystem" && len(req.Payload) >= 4 &&
					string(req.Payload[4:]) == "sftp"
				_ = req.Reply(ok, nil)
			}
		}(requests)

		go func(ch ssh.Channel) {
			server, serverErr := pkgsftp.NewServer(ch)
			if serverErr != nil {
				return
			}
			defer func() { _ = server.Close() }()
			_ = server.Serve()
		}(channel)
	}
}
