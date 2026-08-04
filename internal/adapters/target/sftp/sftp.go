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
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
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

// Target is the SFTP backup target.
//
// Connections are cached per host so one backup does not open a session per
// file. Close releases them; the CLI calls it when the command ends.
type Target struct {
	mu    sync.Mutex
	conns map[string]*connection

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

// Close releases every cached connection.
func (t *Target) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	var errs []error
	for key, conn := range t.conns {
		if err := conn.close(); err != nil {
			errs = append(errs, err)
		}
		delete(t.conns, key)
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

// store connects, or reuses a connection, and returns the blob.Store over it.
func (t *Target) store(ctx context.Context, ref ports.TargetRef) (*sftpStore, error) {
	conn, err := t.connect(ctx, ref)
	if err != nil {
		return nil, err
	}
	return &sftpStore{client: conn.client, root: ref.Path, ref: ref}, nil
}

func (t *Target) connect(ctx context.Context, ref ports.TargetRef) (*connection, error) {
	// The configuration is built before the cache is consulted, so the cache
	// key can be derived from the parsed key's *public* half rather than from
	// the private material. Parsing on a cache hit costs microseconds and
	// happens a handful of times per backup.
	cfg, signer, err := t.clientConfig(ref)
	if err != nil {
		return nil, err
	}
	key := connectionKey(ref, signer)

	t.mu.Lock()
	if conn, ok := t.conns[key]; ok {
		t.mu.Unlock()
		return conn, nil
	}
	t.mu.Unlock()

	addr := ref.Host
	if _, _, splitErr := net.SplitHostPort(addr); splitErr != nil {
		addr = net.JoinHostPort(addr, DefaultPort)
	}

	raw, err := t.dialTCP(ctx, addr)
	if err != nil {
		return nil, domain.BackupError(err, "cannot reach the backup target %s", ref).
			WithHint("check that the host is up and reachable on its ssh port")
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(raw, addr, cfg)
	if err != nil {
		_ = raw.Close()
		return nil, t.handshakeError(ref, err)
	}
	client := ssh.NewClient(sshConn, chans, reqs)

	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		_ = client.Close()
		return nil, domain.BackupError(err,
			"connected to %s but could not start an sftp session", ref).
			WithHint("the target host must run an sftp subsystem; on OpenSSH that is " +
				"the `Subsystem sftp` line in sshd_config")
	}

	conn := &connection{client: sftpClient, ssh: client}

	t.mu.Lock()
	defer t.mu.Unlock()
	// Another goroutine may have connected while this one was dialling.
	if existing, ok := t.conns[key]; ok {
		_ = conn.close()
		return existing, nil
	}
	t.conns[key] = conn
	return conn, nil
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
				"with `ssh-keyscan <host>`, and check it against the host's own " +
				"`ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` -- a keyscan " +
				"taken over the network is only as trustworthy as the network")
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
}

var _ blob.Store = (*sftpStore)(nil)

func (s *sftpStore) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	target, err := s.resolve(key)
	if err != nil {
		return err
	}
	if err := s.client.MkdirAll(path.Dir(target)); err != nil {
		return s.unreachable(err, "cannot create %s on the target", path.Dir(target))
	}

	// Written beside and renamed, so an interrupted transfer never leaves a
	// truncated component under its final name.
	tmp := target + ".partial"
	f, err := s.client.Create(tmp)
	if err != nil {
		return s.unreachable(err, "cannot create %s on the target", tmp)
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		_ = s.client.Remove(tmp)
		return s.unreachable(err, "cannot write %s to the target", key)
	}
	if err := f.Close(); err != nil {
		_ = s.client.Remove(tmp)
		return s.unreachable(err, "cannot finish writing %s to the target", key)
	}

	// Rename over an existing file: a re-push must overwrite rather than
	// fail, because a re-push is the documented remedy for a partial one.
	_ = s.client.Remove(target)
	if err := s.client.Rename(tmp, target); err != nil {
		_ = s.client.Remove(tmp)
		return s.unreachable(err, "cannot place %s on the target", key)
	}
	if err := s.client.Chmod(target, 0o600); err != nil {
		return s.unreachable(err, "cannot set the mode of %s on the target", key)
	}
	return nil
}

func (s *sftpStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
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
	var out []string

	walker := s.client.Walk(s.root)
	for walker.Step() {
		if err := walker.Err(); err != nil {
			// A directory that vanished between listing and
			// walking is a concurrent prune, not a failure.
			continue
		}
		info := walker.Stat()
		if info == nil || info.IsDir() || !info.Mode().IsRegular() {
			continue
		}

		key := strings.TrimPrefix(strings.TrimPrefix(walker.Path(), s.root), "/")
		if key == "" || strings.HasSuffix(key, ".partial") {
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
	clean := path.Clean("/" + key)
	if clean == "/" {
		return "", domain.BackupError(nil, "the backup names an empty component path")
	}
	if strings.Contains(key, "..") {
		return "", domain.BackupError(nil,
			"the backup names a component outside the target: %q", key).
			WithHint("this backup was not written by this manager; do not restore from it")
	}
	return path.Join(s.root, clean), nil
}

func (s *sftpStore) unreachable(cause error, format string, args ...any) error {
	return domain.BackupError(cause, format, args...).
		WithHint("check that the target account can write to %s and that the "+
			"filesystem is not full", s.root)
}
