package suite

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	pkgsftp "github.com/pkg/sftp"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/morzecrew/morzer/internal/adapters/target/sftp"
	"github.com/morzecrew/morzer/internal/domain"
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

// TestADeadConnectionIsNotReused.
//
// Connections are cached so one backup does not open a session per file, and a
// cache with no eviction hands out corpses: a NAT timing out during a long push
// left every later operation of the same command talking to a socket that was
// closed minutes ago, including the ones that would have worked.
//
// Driven through the adapter's own dialler, so the connection dies the way a
// real one does -- underneath, with nothing told about it.
func TestADeadConnectionIsNotReused(t *testing.T) {
	client := newSSHKey(t)
	host := newSSHKey(t)

	root := t.TempDir()
	addr := startInProcessSSH(t, host, client.public, root)

	var mu sync.Mutex
	var dialled []net.Conn

	adapter := sftp.New().WithDialer(func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err == nil {
			mu.Lock()
			dialled = append(dialled, conn)
			mu.Unlock()
		}
		return conn, err
	})
	t.Cleanup(func() { _ = adapter.Close() })

	ref := ports.TargetRef{
		Scheme: "ssh", Host: addr, Path: root, User: "ops",
		URL: "ssh://ops@" + addr + root,
		Credentials: ports.TargetCredentials{
			PrivateKey: client.private,
			KnownHosts: sshKnownHostsLine(t, addr, host.public),
		},
	}

	_, err := adapter.List(context.Background(), ref)
	require.NoError(t, err)

	mu.Lock()
	first := dialled[0]
	mu.Unlock()
	require.NoError(t, first.Close())

	// Eventually rather than once, because the cached entry is dropped when
	// the connection is seen to die, which is a moment later than the socket
	// closing. Without eviction no amount of waiting helps: every call
	// reaches for the same dead client.
	require.Eventually(t, func() bool {
		_, listErr := adapter.List(context.Background(), ref)
		return listErr == nil
	}, 5*time.Second, 50*time.Millisecond,
		"the adapter kept reusing a connection that had been closed")

	mu.Lock()
	defer mu.Unlock()
	require.Greater(t, len(dialled), 1, "no second connection was ever dialled")
}

// TestATargetPathThatIsAFileSaysSo.
//
// The ordinary typo -- a path that names the backup rather than the directory
// backups live in. Nothing on the target can be created under it, and the
// diagnosis has to say which directory and what to check, because the same
// syscall failure is what an unmounted medium and a full disk produce.
func TestATargetPathThatIsAFileSaysSo(t *testing.T) {
	client := newSSHKey(t)
	host := newSSHKey(t)

	root := t.TempDir()
	addr := startInProcessSSH(t, host, client.public, root)

	blocked := filepath.Join(root, "backups")
	require.NoError(t, os.WriteFile(blocked, []byte("a file where a directory was meant"), 0o600))

	local := writeTestBackup(t, "20260101T000000Z", map[string]string{
		"database.sql.age": "ciphertext",
	})

	adapter := sftp.New()
	t.Cleanup(func() { _ = adapter.Close() })

	ref := ports.TargetRef{
		Scheme: "ssh", Host: addr, Path: blocked, User: "ops",
		URL: "ssh://ops@" + addr + blocked,
		Credentials: ports.TargetCredentials{
			PrivateKey: client.private,
			KnownHosts: sshKnownHostsLine(t, addr, host.public),
		},
	}

	_, err := adapter.Push(context.Background(), ref, local, "20260101T000000Z")
	require.Error(t, err, "a backup was pushed into a path that is a file")
	require.Contains(t, domain.AsError(err).Hint, blocked,
		"the refusal must name the directory the target account could not write to")
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
