package suite

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
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

// TestADeadSFTPSessionUnderALiveTransportIsNotReused.
//
// The narrower half of the same defect, and the one waiting on the transport
// cannot see: the remote sftp-server is OOM-killed, or the server drops the
// subsystem channel after an idle timeout, and the ssh connection carries on
// perfectly well underneath. `Wait` never returns, so the cached sftp client
// stays in the cache as a corpse and every later operation in the process
// reaches for it -- a push whose components each fail against a session that
// died between the first two.
//
// Only the session channel is closed here; the transport is left up, which is
// what makes this test about the operation's own error rather than about the
// transport eviction.
func TestADeadSFTPSessionUnderALiveTransportIsNotReused(t *testing.T) {
	client := newSSHKey(t)
	host := newSSHKey(t)

	root := t.TempDir()
	lab := startInProcessSSHLab(t, host, client.public, root)

	adapter := sftp.New()
	t.Cleanup(func() { _ = adapter.Close() })

	ref := ports.TargetRef{
		Scheme: "ssh", Host: lab.addr, Path: root, User: "ops",
		URL: "ssh://ops@" + lab.addr + root,
		Credentials: ports.TargetCredentials{
			PrivateKey: client.private,
			KnownHosts: sshKnownHostsLine(t, lab.addr, host.public),
		},
	}

	_, err := adapter.List(context.Background(), ref)
	require.NoError(t, err, "the first listing, over a session that is alive")

	lab.killSFTPSessions()

	// Eventually rather than once, because the first call after the kill is
	// the one that discovers the session is gone. Without eviction no amount
	// of waiting helps: every call reaches for the same dead client, and
	// pkg/sftp answers connection-lost for the life of the process.
	require.Eventually(t, func() bool {
		_, listErr := adapter.List(context.Background(), ref)
		return listErr == nil
	}, 5*time.Second, 50*time.Millisecond,
		"the adapter kept reusing an sftp session that had been closed")
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

	return startInProcessSSHLab(t, host, authorized, root).addr
}

// sshLab is the in-process server with a hand on its session channels.
//
// The sftp subsystem can be killed without touching the transport it runs on,
// which is what a remote sftp-server being OOM-killed looks like from here: the
// ssh connection is fine, and the sftp session on it is dead.
type sshLab struct {
	addr string

	mu       sync.Mutex
	channels []ssh.Channel
}

func (l *sshLab) record(ch ssh.Channel) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.channels = append(l.channels, ch)
}

// killSFTPSessions closes every sftp channel the server has accepted and leaves
// the ssh transport up.
//
// The channels are taken out of the lab first and closed after the lock is
// released. Closing writes to the transport, which can block, and `record` --
// the server's accept path -- wants the same mutex: the next session would then
// be waiting on a close that is waiting on the wire. In this test that is not
// merely untidy, because the next session is the reconnection the caller is
// about to require, and a stalled accept would fail it with the message that
// accuses the adapter of reusing a dead session. A flake indistinguishable from
// the regression it is watching for is the worst shape a test can take.
func (l *sshLab) killSFTPSessions() {
	l.mu.Lock()
	doomed := l.channels
	l.channels = nil
	l.mu.Unlock()

	for _, ch := range doomed {
		_ = ch.Close()
	}
}

func startInProcessSSHLab(t *testing.T, host sshKey, authorized ssh.PublicKey, root string) *sshLab {
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

	lab := &sshLab{addr: listener.Addr().String()}

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go serveSSH(conn, cfg, lab)
		}
	}()

	return lab
}

func serveSSH(conn net.Conn, cfg *ssh.ServerConfig, lab *sshLab) {
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
		lab.record(channel)

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

// TestAConnectionDialledAcrossCloseIsNotLeaked.
//
// Dialling happens outside the target's lock, because it is the slow part and
// holding the mutex across it would serialise every target a backup touches.
// The consequence is that a connection can arrive *after* Close has emptied the
// cache: it would be filed in a map nothing will empty again, holding an ssh
// session and its watching goroutine open with no reference left to reach it.
//
// The race is made deterministic here by a dialler the test holds shut until
// Close has run, which is the interleaving that would otherwise happen rarely
// and never reproducibly.
func TestAConnectionDialledAcrossCloseIsNotLeaked(t *testing.T) {
	client := newSSHKey(t)
	host := newSSHKey(t)

	root := t.TempDir()
	addr := startInProcessSSH(t, host, client.public, root)

	dialling := make(chan struct{})
	release := make(chan struct{})
	var closed atomic.Bool

	adapter := sftp.New().WithDialer(func(ctx context.Context, network, address string) (net.Conn, error) {
		close(dialling)
		<-release

		conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &watchedConn{Conn: conn, closed: &closed}, nil
	})

	ref := ports.TargetRef{
		Scheme: "ssh", Host: addr, Path: root, User: "ops",
		URL: "ssh://ops@" + addr + root,
		Credentials: ports.TargetCredentials{
			PrivateKey: client.private,
			KnownHosts: sshKnownHostsLine(t, addr, host.public),
		},
	}

	done := make(chan error, 1)
	go func() {
		_, err := adapter.List(context.Background(), ref)
		done <- err
	}()

	<-dialling
	require.NoError(t, adapter.Close(), "close found nothing to close, and must still succeed")
	close(release)

	require.Error(t, <-done,
		"a target that had been closed handed out a connection anyway")
	require.True(t, closed.Load(),
		"the connection dialled across Close was cached instead, so it stays open "+
			"with nothing left holding a reference to it")
}

// watchedConn records whether the connection under it was ever closed.
type watchedConn struct {
	net.Conn
	closed *atomic.Bool
}

func (c *watchedConn) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

// TestSSHPushSweepsAStalePartialFile: a push whose cancellation tore the
// transport down cannot remove its own staging file -- the connection died
// first -- so the next push of the same component must, or cancelled pushes
// accumulate garbage on the target until it fills.
func TestSSHPushSweepsAStalePartialFile(t *testing.T) {
	client := newSSHKey(t)
	host := newSSHKey(t)
	root := t.TempDir()
	addr := startInProcessSSH(t, host, client.public, root)

	dir := filepath.Join(root, "offsite")
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

	// A minimal real backup to push.
	const id = "20260807T120000Z"
	local := filepath.Join(t.TempDir(), id)
	require.NoError(t, os.MkdirAll(local, 0o700))
	content := []byte("ciphertext")
	require.NoError(t, os.WriteFile(filepath.Join(local, "db.dump.age"), content, 0o600))
	manifest := ports.BackupManifest{
		SchemaVersion:  2,
		ID:             id,
		InstallationID: "inst_sshlab",
		Product:        "demo",
		ReleaseVersion: domain.MustParseVersion("1.0.0"),
		CreatedAt:      domain.NewTime(time.Now()),
		ManagerVersion: "0.0.0",
		Reason:         "sweep-test",
		Components: []ports.ComponentRecord{{
			Component:  ports.ComponentDatabase,
			Path:       "db.dump.age",
			Size:       int64(len(content)),
			Encryption: ports.EncryptionAge,
		}},
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(local, ports.BackupManifestFileName),
		append(data, '\n'), 0o600))

	// The garbage a torn-down push leaves, planted on the server's own
	// filesystem beside the component's final name.
	remote := filepath.Join(dir, id)
	require.NoError(t, os.MkdirAll(remote, 0o700))
	stale := filepath.Join(remote, "db.dump.age.partial-999-9")
	require.NoError(t, os.WriteFile(stale, []byte("half a component"), 0o600))

	_, err = adapter.Push(context.Background(), ref, local, id)
	require.NoError(t, err)

	if _, statErr := os.Stat(stale); !os.IsNotExist(statErr) {
		t.Error("the stale partial from a torn-down push was not swept")
	}
	require.NoError(t, adapter.Verify(context.Background(),
		ports.RemoteRef{Target: ref, ID: id}),
		"the pushed backup must verify end to end")
}
