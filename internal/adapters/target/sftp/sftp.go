// Package sftp implements ports.BackupTarget over SSH.
//
// This is the commonest self-hosted answer to "somewhere else": a second VM the
// operator already has, reached over a protocol every Linux host already
// speaks. No object store account, no third party, no egress bill.
//
// Host keys are verified, always, and there is no flag to disable it. The
// reasoning is worth stating because "just this once" is how it usually gets
// turned off: a target that accepts any host key is a target that can be
// replaced by whoever is on the path. The backup itself is encrypted to this
// deployment's own age recipients, so an impostor learns nothing by receiving
// it -- but they can accept every push and answer every listing, and the
// operator would believe they had off-site backups while having none. Host-key
// verification is what makes "the backup reached the target" mean "the backup
// reached *that* machine".
package sftp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/morzecrew/morzer/internal/adapters/target/blob"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// Scheme is the URL scheme this target handles.
const Scheme = "ssh"

// DefaultPort is used when the URL names no port.
const DefaultPort = "22"

// posixRenameExtension is the OpenSSH extension that makes replacing a file
// atomic. The constant is spelled out because pkg/sftp keeps its own copy
// behind an internal package.
const posixRenameExtension = "posix-rename@openssh.com"

// fsyncExtension is the OpenSSH extension that flushes a written file to the
// target's disk before it is renamed into place.
//
// Without it the rename is atomic with respect to *this* transfer and says
// nothing about the target's own power supply: the entry can be durable while
// the bytes it names are still in the server's page cache. A backup that
// survives the push and not the night is the failure this exists to prevent, so
// it is used wherever it is advertised and its absence is a property of the
// server rather than something to fail over.
const fsyncExtension = "fsync@openssh.com"

// errClosedTarget refuses work on a target that has been shut down. Internal
// rather than a backup error: nothing about the target is wrong, and no
// operator action can help -- something asked this adapter for a connection
// after the command that owned it had finished with it.
var errClosedTarget = domain.Internal(nil, "this backup target has been closed")

// Target is the SFTP backup target.
//
// Connections are cached per host so one backup does not open a session per
// file. Close releases them; the CLI calls it when the command ends.
type Target struct {
	mu    sync.Mutex
	conns map[string]*connection

	// closed is set by Close, and is what stops a connection being cached
	// after it has run. Dialling happens outside the lock -- it is the slow
	// part, and holding the mutex across it would serialise every target --
	// so a connection that was in flight when Close ran arrives afterwards,
	// with nothing left to close it.
	closed bool

	// seq names staging files apart, so two writes to one path cannot
	// truncate each other.
	seq atomic.Uint64

	// dial is the transport, injectable so a test can drive a server in
	// this process without a container.
	dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

func New() *Target {
	return &Target{conns: map[string]*connection{}}
}

// WithDialer overrides how the TCP connection is made.
func (t *Target) WithDialer(dial func(ctx context.Context, network, addr string) (net.Conn, error)) *Target {
	t.dial = dial
	return t
}

var (
	_ ports.BackupTarget = (*Target)(nil)
	_ io.Closer          = (*Target)(nil)
)

func (t *Target) Schemes() []string { return []string{Scheme} }

func (t *Target) Push(ctx context.Context, ref ports.TargetRef, localDir, id string) (ports.RemoteRef, error) {
	store, err := t.store(ctx, ref)
	if err != nil {
		return ports.RemoteRef{}, err
	}
	return blob.Push(ctx, store, ref, localDir, id)
}

func (t *Target) List(ctx context.Context, ref ports.TargetRef) ([]ports.BackupManifest, error) {
	store, err := t.store(ctx, ref)
	if err != nil {
		return nil, err
	}
	return blob.List(ctx, store)
}

func (t *Target) Fetch(ctx context.Context, ref ports.RemoteRef, destDir string) error {
	store, err := t.store(ctx, ref.Target)
	if err != nil {
		return err
	}
	return blob.Fetch(ctx, store, ref, destDir)
}

func (t *Target) FetchFile(ctx context.Context, ref ports.RemoteRef, name, destDir string) error {
	store, err := t.store(ctx, ref.Target)
	if err != nil {
		return err
	}
	return blob.FetchFile(ctx, store, ref, name, destDir)
}

func (t *Target) Verify(ctx context.Context, ref ports.RemoteRef) error {
	store, err := t.store(ctx, ref.Target)
	if err != nil {
		return err
	}
	return blob.Verify(ctx, store, ref)
}

func (t *Target) Remove(ctx context.Context, ref ports.RemoteRef) error {
	store, err := t.store(ctx, ref.Target)
	if err != nil {
		return err
	}
	if err := blob.Remove(ctx, store, ref); err != nil {
		return err
	}
	// An empty directory left behind is not a backup, but it is clutter an
	// operator has to reason about while looking for one.
	_ = store.client.RemoveDirectory(path.Join(store.root, ref.ID))
	return nil
}

var _ ports.ObjectStore = (*Target)(nil)

func (t *Target) PutObject(ctx context.Context, ref ports.TargetRef, key string, data []byte) error {
	store, err := t.store(ctx, ref)
	if err != nil {
		return err
	}
	return blob.PutObject(ctx, store, key, data)
}

func (t *Target) ObjectKeys(ctx context.Context, ref ports.TargetRef, prefix string) ([]string, error) {
	store, err := t.store(ctx, ref)
	if err != nil {
		return nil, err
	}
	return blob.ObjectKeys(ctx, store, prefix)
}

// Close releases every cached connection.
//
// The cache is emptied under the lock and the connections are closed after it
// is released, the same way `drop` does. Closing writes a disconnect to the
// wire, and a host that has stopped answering without closing its socket -- an
// unplugged NAS, a firewall that black-holes rather than refuses -- can hold
// that write for a long time. Holding the mutex across it means an operation
// still finishing, or the goroutine watching a connection die, waits on a
// server that is never going to reply, at the moment the command is trying to
// exit.
func (t *Target) Close() error {
	t.mu.Lock()
	t.closed = true
	doomed := make([]*connection, 0, len(t.conns))
	for key, conn := range t.conns {
		doomed = append(doomed, conn)
		delete(t.conns, key)
	}
	t.mu.Unlock()

	var errs []error
	for _, conn := range doomed {
		if err := conn.close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type connection struct {
	client *sftp.Client
	ssh    *ssh.Client
}

func (c *connection) close() error {
	var errs []error
	if c.client != nil {
		errs = append(errs, c.client.Close())
	}
	if c.ssh != nil {
		errs = append(errs, c.ssh.Close())
	}
	return errors.Join(errs...)
}

// kill severs the transport out from under in-flight requests.
//
// sftp.Client.Close is not safe to call concurrently with requests still in
// flight; closing the ssh transport instead makes every pending request fail
// and the client's own goroutines exit on their error path -- which is
// exactly the teardown both callers of drop want, for a corpse or for a
// cancellation. close() stays for the orderly end-of-command path, where
// nothing is in flight by construction.
func (c *connection) kill() {
	if c.ssh != nil {
		_ = c.ssh.Close()
	}
}

// store connects, or reuses a connection, and returns the blob.Store over it.
func (t *Target) store(ctx context.Context, ref ports.TargetRef) (*sftpStore, error) {
	conn, key, err := t.connect(ctx, ref)
	if err != nil {
		return nil, err
	}
	return &sftpStore{
		client:   conn.client,
		root:     ref.Path,
		ref:      ref,
		seq:      &t.seq,
		dropDead: func() { t.drop(key, conn) },
	}, nil
}

// drop evicts a connection from the cache and closes it, unless somebody else
// already took it out.
func (t *Target) drop(key string, conn *connection) {
	t.mu.Lock()
	cached := t.conns[key] == conn
	if cached {
		delete(t.conns, key)
	}
	t.mu.Unlock()

	// Severed outside the lock, and only by whoever removed it, so a
	// connection is never torn down twice and Close is never held up by a
	// server that has stopped answering. kill rather than close: drop's
	// callers reach it with requests possibly still in flight.
	if cached {
		conn.kill()
	}
}

func (t *Target) connect(ctx context.Context, ref ports.TargetRef) (*connection, string, error) {
	// The configuration is built before the cache is consulted, so the cache
	// key can be derived from the parsed key's *public* half rather than from
	// the private material. Parsing on a cache hit costs microseconds and
	// happens a handful of times per backup.
	cfg, signer, err := t.clientConfig(ref)
	if err != nil {
		return nil, "", err
	}
	key := connectionKey(ref, signer)

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, "", errClosedTarget
	}
	if conn, ok := t.conns[key]; ok {
		t.mu.Unlock()
		return conn, key, nil
	}
	t.mu.Unlock()

	addr := ref.Host
	if _, _, splitErr := net.SplitHostPort(addr); splitErr != nil {
		addr = net.JoinHostPort(addr, DefaultPort)
	}

	raw, err := t.dialTCP(ctx, addr)
	if err != nil {
		return nil, "", domain.BackupError(err, "cannot reach the backup target %s", ref).
			WithHint("check that the host is up and reachable on its ssh port")
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(raw, addr, cfg)
	if err != nil {
		_ = raw.Close()
		return nil, "", t.handshakeError(ref, err)
	}
	client := ssh.NewClient(sshConn, chans, reqs)

	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		_ = client.Close()
		return nil, "", domain.BackupError(err,
			"connected to %s but could not start an sftp session", ref).
			WithHint("the target host must run an sftp subsystem; on OpenSSH that is " +
				"the `Subsystem sftp` line in sshd_config")
	}

	conn := &connection{client: sftpClient, ssh: client}

	t.mu.Lock()
	// Close ran while this one was dialling. The cache it emptied will never
	// be emptied again, so caching this connection now would leave it open
	// for the life of the process with nothing holding a reference to it.
	if t.closed {
		t.mu.Unlock()
		_ = conn.close()
		return nil, "", errClosedTarget
	}
	defer t.mu.Unlock()
	// Another goroutine may have connected while this one was dialling.
	if existing, ok := t.conns[key]; ok {
		_ = conn.close()
		return existing, key, nil
	}
	t.conns[key] = conn

	// A cached connection is dropped from the cache when it dies, so the
	// next operation dials a fresh one instead of reusing a corpse. Without
	// this, one dropped TCP connection -- a NAT timing out during a long
	// push -- failed every later step of the same command, including the
	// ones that would otherwise have recovered.
	//
	// Waiting on the connection costs nothing and asks the server nothing;
	// a liveness probe before each use would be a round trip per operation
	// that is still racing the answer it gets.
	go func() {
		_ = client.Wait()
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.conns[key] == conn {
			delete(t.conns, key)
		}
	}()

	return conn, key, nil
}

// connectionKey identifies a connection by everything that authenticated it,
// not merely by where it went.
//
// Keying on `user@host` alone was a defect, and a security-relevant one. Two
// targets on the same host share a connection under that key, so the second
// one's credentials are never used and -- worse -- **its host key is never
// checked**: the pin is verified during the handshake, and the second target
// does not get a handshake. An operator could configure one target with a
// correct pin and a second with a wrong one, and nothing would object.
//
// Every input here is public. The authenticating key contributes its own
// fingerprint -- the public half uniquely identifies the private one -- and the
// pin contributes a digest of known_hosts, which holds nothing but public host
// keys. No secret material reaches this string, which matters twice over: it is
// a map key that lives as long as the process, and a cache key derived from a
// passphrase is indistinguishable, to a reader and to a scanner, from storing
// one badly.
//
// The passphrase is deliberately absent. A wrong one does not produce a
// different connection; it produces no connection at all, because the key fails
// to parse before this is ever called.
func connectionKey(ref ports.TargetRef, signer ssh.Signer) string {
	pin := sha256.Sum256([]byte(ref.Credentials.KnownHosts))
	return ref.User + "@" + ref.Host +
		"|" + ssh.FingerprintSHA256(signer.PublicKey()) +
		"|" + hex.EncodeToString(pin[:16])
}

func (t *Target) dialTCP(ctx context.Context, addr string) (net.Conn, error) {
	if t.dial != nil {
		return t.dial(ctx, "tcp", addr)
	}
	dialer := net.Dialer{Timeout: 30 * time.Second}
	return dialer.DialContext(ctx, "tcp", addr)
}

// clientConfig builds the SSH configuration, refusing anything that would
// weaken authentication in either direction.
func (t *Target) clientConfig(ref ports.TargetRef) (*ssh.ClientConfig, ssh.Signer, error) {
	creds := ref.Credentials

	if strings.TrimSpace(creds.PrivateKey) == "" {
		return nil, nil, domain.BackupError(nil,
			"the ssh backup target %s has no private key", ref).
			WithHint("put one in a secret as `private_key`, alongside `known_hosts`, " +
				"and name it with --credentials")
	}

	var signer ssh.Signer
	var err error
	if creds.Passphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(
			[]byte(creds.PrivateKey), []byte(creds.Passphrase))
	} else {
		signer, err = ssh.ParsePrivateKey([]byte(creds.PrivateKey))
	}
	if err != nil {
		// Never %v on the parse error: it can echo the key material it
		// failed on.
		hint := "the value of `private_key` must be an OpenSSH private key, " +
			"beginning -----BEGIN OPENSSH PRIVATE KEY-----"
		if _, isPassphrase := err.(*ssh.PassphraseMissingError); isPassphrase {
			hint = "this key is encrypted; add `passphrase` to the same secret"
		}
		return nil, nil, domain.BackupError(nil,
			"the private key for the backup target %s cannot be read", ref).
			WithHint("%s", hint)
	}

	callback, algorithms, err := t.hostKeyCallback(ref)
	if err != nil {
		return nil, nil, err
	}

	user := ref.User
	if user == "" {
		user = "root"
	}

	return &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		// Restricted to the algorithms actually pinned.
		//
		// Without this the pin is a coin flip: a host with both an
		// ed25519 and an RSA key offers whichever it prefers, and an
		// operator who pinned one of them gets "the host key is not the
		// pinned one" against a server that is perfectly honest. That
		// refusal is indistinguishable from the attack it exists to
		// catch, which makes it worse than no message at all -- an
		// operator who sees it twice for no reason learns to ignore it.
		HostKeyAlgorithms: algorithms,
		HostKeyCallback:   callback,
		Timeout:           30 * time.Second,
	}, signer, nil
}

// hostKeyCallback builds the verifier, and refuses to build one that accepts
// anything.
//
// There is no InsecureIgnoreHostKey path here and no flag that reaches one. See
// the package comment: the harm is not that an impostor reads the backup -- it
// cannot -- but that it can accept every push, answer every listing, and leave
// an operator believing they have off-site backups they do not have.
func (t *Target) hostKeyCallback(ref ports.TargetRef) (ssh.HostKeyCallback, []string, error) {
	raw := strings.TrimSpace(ref.Credentials.KnownHosts)
	if raw == "" {
		return nil, nil, domain.BackupError(nil,
			"the ssh backup target %s pins no host key", ref).
			WithHint("add `known_hosts` to the target's credentials. Get the line " +
				"with `ssh-keyscan -p <port> <host>`, and check it against the " +
				"host's own `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` -- " +
				"a keyscan taken over the network is only as trustworthy as the " +
				"network. On a port other than 22 the entry names the host as " +
				"[host]:port, which is what ssh-keyscan -p prints")
	}

	algorithms, err := pinnedAlgorithms(raw)
	if err != nil {
		return nil, nil, domain.BackupError(nil,
			"the `known_hosts` value for the backup target %s is not valid", ref).
			WithHint("it is a line in the format `ssh-keyscan <host>` prints: " +
				"hostname, key type, then the key")
	}

	// knownhosts reads files, not strings, so the pin is staged. 0600 and
	// removed straight after: it is a public key and not a secret, but a
	// world-readable file in /tmp naming this deployment's backup host is
	// still more than anyone needs.
	file, err := os.CreateTemp("", "morzer-known-hosts-")
	if err != nil {
		return nil, nil, domain.Internal(err, "cannot stage the known_hosts file")
	}
	defer func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}()

	if _, err := file.WriteString(raw + "\n"); err != nil {
		return nil, nil, domain.Internal(err, "cannot stage the known_hosts file")
	}
	if err := file.Close(); err != nil {
		return nil, nil, domain.Internal(err, "cannot stage the known_hosts file")
	}

	callback, err := knownhosts.New(file.Name())
	if err != nil {
		return nil, nil, domain.BackupError(nil,
			"the `known_hosts` value for the backup target %s is not valid", ref).
			WithHint("it is a line in the format `ssh-keyscan <host>` prints: " +
				"hostname, key type, then the key")
	}
	return callback, algorithms, nil
}

// pinnedAlgorithms lists the host-key algorithms the pin actually covers.
//
// This is what makes pinning one key type work against a host that has several.
// sshd offers whichever key it prefers; a client that accepts any algorithm but
// pins only ed25519 will be offered RSA and report a mismatch -- a refusal
// identical to the one a real attack produces, for a server doing nothing wrong.
func pinnedAlgorithms(raw string) ([]string, error) {
	seen := map[string]bool{}
	var out []string

	rest := []byte(raw)
	for len(rest) > 0 {
		var key ssh.PublicKey
		var err error

		_, _, key, _, rest, err = ssh.ParseKnownHosts(rest)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}

		for _, algo := range algorithmsFor(key.Type()) {
			if !seen[algo] {
				seen[algo] = true
				out = append(out, algo)
			}
		}
	}

	if len(out) == 0 {
		return nil, errors.New("the known_hosts value pins no key")
	}
	return out, nil
}

// algorithmsFor expands a key type into the signature algorithms it can be used
// with.
//
// Only RSA needs expanding, and it needs it badly: a key recorded as `ssh-rsa`
// is verified today with rsa-sha2-256 or rsa-sha2-512, because SHA-1 signatures
// are refused by every current OpenSSH. Pinning the recorded name alone would
// reject a modern server for being modern.
func algorithmsFor(keyType string) []string {
	if keyType == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	return []string{keyType}
}

// handshakeError turns a failed handshake into the diagnosis it deserves.
//
// A changed host key is the one worth calling out by name: it is either the
// target being rebuilt -- ordinary, and the operator updates the pin -- or
// somebody between here and there, which is the case this check exists for.
func (t *Target) handshakeError(ref ports.TargetRef, cause error) error {
	var keyErr *knownhosts.KeyError
	if errors.As(cause, &keyErr) {
		return domain.BackupError(domain.ErrDigestMismatch,
			"the backup target %s presented a host key that is not the pinned one", ref).
			WithHint("if the host was rebuilt, update `known_hosts` in its credentials. " +
				"If it was not, something is answering for it, and nothing should be " +
				"pushed there until you know what")
	}
	if strings.Contains(cause.Error(), "unable to authenticate") {
		return domain.BackupError(nil,
			"the backup target %s refused the key", ref).
			WithHint("check that the public half of `private_key` is in the target " +
				"account's ~/.ssh/authorized_keys, and that the URL names the " +
				"right user")
	}
	return domain.BackupError(cause, "cannot connect to the backup target %s", ref)
}

// sftpStore is blob.Store over an SFTP session.
type sftpStore struct {
	client *sftp.Client
	root   string
	ref    ports.TargetRef

	// dropDead evicts this connection from the target's cache.
	//
	// The sftp subsystem can die while the ssh transport it runs on stays up
	// -- the remote sftp-server being OOM-killed, or a server closing the
	// subsystem channel on idle. Nothing ends the transport then, so the
	// goroutine waiting on it never fires, and the cached client is a corpse
	// every later operation in the process reaches for.
	dropDead func()

	// seq names each staging file apart within this process.
	seq *atomic.Uint64
}

var _ blob.Store = (*sftpStore)(nil)

// ctxReader stops a copy that is already running when the operation is
// abandoned -- localdir's pattern, repeated here because the two stores share
// a contract, not code. Between reads, not during one: a read blocked on a
// dead connection stays blocked, and the connection teardown handles that.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

func (s *sftpStore) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := s.resolve(key)
	if err != nil {
		return err
	}

	// The watcher covers every remote call in this function, not only the
	// copy: a stalled server can hang MkdirAll or Create just as well.
	// Cancellation tears the transport down -- dropDead closes and evicts
	// the cached connection, the same lever the corpse-detection path
	// pulls -- and the forced error is what unblocks whichever call was
	// stuck. The next operation redials.
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-ctx.Done():
			if s.dropDead != nil {
				s.dropDead()
			}
		case <-watchDone:
		}
	}()
	// After the teardown, every remote call reports connection errors; the
	// operator's cancellation is the true cause and the one reported --
	// classified here, so the direct command paths that never pass through
	// the engine's classifier still exit as interrupted.
	fail := func(cause error, format string, args ...any) error {
		if ctx.Err() != nil {
			return domain.Interrupted("the push to the backup target was cancelled")
		}
		return s.unreachable(cause, format, args...)
	}

	if err := s.client.MkdirAll(path.Dir(target)); err != nil {
		return fail(err, "cannot create %s on the target", path.Dir(target))
	}

	// Stale .partial-* files of this component, left by pushes whose
	// cancellation tore the connection down before their own cleanup could
	// run, are swept before writing a new one. Only this target's: the
	// deployment lock serialises pushes, so anything matching is a leftover.
	if entries, err := s.client.ReadDir(path.Dir(target)); err == nil {
		stalePrefix := path.Base(target) + ".partial-"
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), stalePrefix) {
				_ = s.client.Remove(path.Join(path.Dir(target), e.Name()))
			}
		}
	}

	// Written beside and renamed, so an interrupted transfer never leaves a
	// truncated component under its final name. The staging name is unique
	// per write: a shared one lets two pushes of the same backup truncate
	// each other, and the survivor renames bytes it did not write.
	tmp := fmt.Sprintf("%s.partial-%d-%d", target, os.Getpid(), s.seq.Add(1))
	f, err := s.client.Create(tmp)
	if err != nil {
		return fail(err, "cannot create %s on the target", tmp)
	}
	// On the handle, not the path. Create returned an open file, and a
	// name-based chmod would follow whatever that name refers to *now* --
	// which after a replacement is a different inode, left at the server's
	// umask while this call narrows something else.
	//
	// Before a single byte is written, too, rather than after the rename: a
	// component is the ciphertext of the deployment's database, and one
	// created at the umask and narrowed afterwards is readable to everyone
	// on the target for the length of the whole transfer.
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = s.client.Remove(tmp)
		return fail(err, "cannot set the mode of %s on the target", key)
	}
	// The reader checks the context between chunks, as localdir's push
	// does; a write already *blocked* past that check is what the watcher
	// above unblocks.
	if _, copyErr := io.Copy(f, ctxReader{ctx: ctx, r: r}); copyErr != nil {
		_ = f.Close()
		_ = s.client.Remove(tmp)
		return fail(copyErr, "cannot write %s to the target", key)
	}
	if _, ok := s.client.HasExtension(fsyncExtension); ok {
		if err := f.Sync(); err != nil {
			_ = f.Close()
			_ = s.client.Remove(tmp)
			return fail(err, "cannot flush %s to the target's disk", key)
		}
	}
	if err := f.Close(); err != nil {
		_ = s.client.Remove(tmp)
		return fail(err, "cannot finish writing %s to the target", key)
	}

	// Replaced atomically where the server supports it. Removing the old
	// file first and renaming after was a window in which a failed rename
	// left the component gone and the manifest still naming it -- a remote
	// backup that lists as whole and is not.
	//
	// posix-rename@openssh.com is an OpenSSH extension, so the remove-then-
	// rename path stays for servers without it. That window is narrower than
	// it was, not closed: a server that lacks the extension cannot offer an
	// atomic replace at all.
	//
	// Which path is taken is decided by what the server *advertised*, not by
	// whether PosixRename failed. Falling back on any failure meant a
	// permission error or a full disk deleted the existing component and
	// then failed the rename too -- destroying a good copy in the name of
	// avoiding exactly that.
	if _, ok := s.client.HasExtension(posixRenameExtension); ok {
		if err := s.client.PosixRename(tmp, target); err != nil {
			_ = s.client.Remove(tmp)
			return fail(err, "cannot replace %s on the target", key)
		}
	} else {
		if removeErr := s.client.Remove(target); removeErr != nil &&
			!errors.Is(removeErr, fs.ErrNotExist) {
			_ = s.client.Remove(tmp)
			return fail(removeErr, "cannot replace %s on the target", key)
		}
		if err := s.client.Rename(tmp, target); err != nil {
			_ = s.client.Remove(tmp)
			return fail(err, "cannot place %s on the target", key)
		}
	}

	syncRemoteDir(s.client, path.Dir(target))

	// Rechecked, so every exit from this function agrees about what a
	// cancelled push is. The component itself is on the target by now --
	// this is the difference between reporting a push that raced a ctrl-C
	// as complete and reporting it as interrupted, and the caller is about
	// to fail the operation either way.
	if ctx.Err() != nil {
		return domain.Interrupted("the push to the backup target was cancelled")
	}
	return nil
}

// syncRemoteDir asks the server to flush a directory entry, and shrugs when it
// cannot.
//
// The local writer fsyncs the directory after a rename, because the rename is
// only as durable as the entry recording it. Over SFTP there is no equivalent
// operation: fsync@openssh.com takes a file handle, and whether a directory can
// be opened as one at all is the server's business -- OpenSSH's own sftp-server
// refuses. So this is best effort, and its failure is not the push's: what it
// buys on a server that allows it is the same guarantee localdir gives, and
// what it costs on one that does not is a round trip and an ignored error.
func syncRemoteDir(client *sftp.Client, dir string) {
	if _, ok := client.HasExtension(fsyncExtension); !ok {
		return
	}
	handle, err := client.Open(dir)
	if err != nil {
		return
	}
	defer func() { _ = handle.Close() }()
	_ = handle.Sync()
}

func (s *sftpStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, err := s.resolve(key)
	if err != nil {
		return nil, err
	}
	f, err := s.client.Open(target)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Unwrapped, so blob can tell "no such backup" from
			// "the host is gone".
			return nil, err
		}
		return nil, s.unreachable(err, "cannot read %s from the target", key)
	}
	return f, nil
}

func (s *sftpStore) Keys(ctx context.Context, prefix string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []string

	// A root that is not there yet holds no backups -- the state before the
	// first push, which `backup list --remote` has to be able to answer.
	// Anything else about the root is a reachability problem and must be
	// reported: a target whose directory cannot be read was being reported as
	// reachable with zero backups, which is the one answer that looks healthy
	// and is not.
	if _, err := s.client.Stat(s.root); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, s.unreachable(err, "cannot read %s on the target", s.root)
	}

	walker := s.client.Walk(s.root)
	for walker.Step() {
		if err := walker.Err(); err != nil {
			// A directory that vanished between listing and walking is
			// a concurrent prune. Anything else -- a subdirectory that
			// cannot be read -- would silently shorten the listing, and
			// a short listing is how retention decides to delete the
			// wrong thing.
			if errors.Is(walker.Err(), fs.ErrNotExist) {
				continue
			}
			return nil, s.unreachable(err, "cannot read %s on the target", walker.Path())
		}
		info := walker.Stat()
		if info == nil || info.IsDir() || !info.Mode().IsRegular() {
			continue
		}

		// Staging files are listed rather than filtered out. The filter
		// that used to be here matched `.partial`, which no staging file
		// has been called since the names became unique per write -- so it
		// was dead, and reviving it would be worse than deleting it.
		// Everything that reads Keys is manifest-driven: List looks only
		// at manifests, Fetch and Verify at what a manifest names. The one
		// caller that sees these keys is Remove, which must delete them,
		// or an interrupted push leaves a directory nothing ever cleans.
		key := strings.TrimPrefix(strings.TrimPrefix(walker.Path(), s.root), "/")
		if key == "" {
			continue
		}
		if prefix != "" && !strings.HasPrefix(key, prefix) {
			continue
		}
		out = append(out, key)
	}

	sort.Strings(out)
	return out, nil
}

func (s *sftpStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := s.resolve(key)
	if err != nil {
		return err
	}
	if err := s.client.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return s.unreachable(err, "cannot remove %s from the target", key)
	}
	return nil
}

// resolve joins a key onto the root and refuses one that escapes it.
//
// A manifest on a target is a file this manager may not have written, so a
// component path of "../../.ssh/authorized_keys" must not be a way for whoever
// controls the target to decide what a fetch reads or a removal deletes.
func (s *sftpStore) resolve(key string) (string, error) {
	// Cleaned and then checked, rather than a substring test for "..".
	// `notes..age` is a legal filename that the other two transports accept,
	// and rejecting it here would make a backup restorable or not depending
	// on which transport it happened to be pushed with.
	clean := path.Clean("/" + key)
	if clean == "/" {
		return "", domain.BackupError(nil, "the backup names an empty component path")
	}
	if key == "" || blob.HasParentComponent(key) {
		return "", domain.BackupError(nil,
			"the backup names a component outside the target: %q", key).
			WithHint("this backup was not written by this manager; do not restore from it")
	}
	return path.Join(s.root, clean), nil
}

// unreachable diagnoses a failed operation, and evicts the connection if the
// failure means there is no longer one.
//
// Every operation's error passes through here, which is the only place a dead
// session becomes visible when the transport under it is still up.
func (s *sftpStore) unreachable(cause error, format string, args ...any) error {
	if s.dropDead != nil && connectionIsGone(cause) {
		s.dropDead()
	}
	return domain.BackupError(cause, format, args...).
		WithHint("check that the target account can write to %s and that the "+
			"filesystem is not full", s.root)
}

// connectionIsGone reports whether a failure means the session is over, rather
// than that the target refused the work.
//
// The distinction is the whole point: a permission error or a full disk is
// about the target and reconnecting changes nothing, while these mean every
// later operation on this client will fail the same way for as long as the
// process lives. pkg/sftp answers with ErrSSHFxConnectionLost once its reader
// has seen the channel end -- that is what a killed remote sftp-server looks
// like from here -- and with the read error itself for whichever request was in
// flight when it happened.
//
// Evicting a connection that was in fact healthy costs one reconnection. Not
// evicting a dead one costs every remaining step of the command.
func connectionIsGone(err error) bool {
	return errors.Is(err, sftp.ErrSSHFxConnectionLost) ||
		errors.Is(err, sftp.ErrSSHFxNoConnection) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, net.ErrClosed)
}
