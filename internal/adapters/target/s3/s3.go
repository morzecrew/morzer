// Package s3 implements ports.BackupTarget for S3 and everything that speaks
// its API: MinIO, Cloudflare R2, Backblaze B2, and Google Cloud Storage's
// interoperability mode.
//
// One adapter for all of them, which is the whole reason to prefer the S3 API
// over native SDKs. A native GCS adapter would be a second large dependency for
// a second API, and interoperability mode already covers the operator who wants
// a bucket there. It waits for somebody to need a feature that mode lacks.
//
// TLS is verified, always, and there is no flag that disables it -- the same
// invariant release sources hold. A plaintext endpoint is allowed only when it
// is explicitly written as http://, which is what makes a MinIO container in a
// test suite usable without making an accident possible in production.
package s3

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/morzecrew/morzer/internal/adapters/target/blob"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// Scheme is the URL scheme this target handles.
const Scheme = "s3"

// DefaultEndpoint is AWS S3, used when the credentials name no other.
const DefaultEndpoint = "s3.amazonaws.com"

// DefaultRegion is what an S3-compatible store that does not care is told.
const DefaultRegion = "us-east-1"

// Target is the object-store backup target.
type Target struct {
	mu      sync.Mutex
	clients map[string]*minio.Client
}

func New() *Target { return &Target{clients: map[string]*minio.Client{}} }

var _ ports.BackupTarget = (*Target)(nil)

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
	return blob.Remove(ctx, store, ref)
}

// store builds the client and resolves the bucket and prefix out of the URL.
func (t *Target) store(ctx context.Context, ref ports.TargetRef) (*bucketStore, error) {
	bucket, prefix := ref.Bucket()
	if bucket == "" {
		return nil, domain.Usage("the s3:// backup target names no bucket").
			WithHint("write s3://bucket/prefix")
	}

	client, err := t.client(ref)
	if err != nil {
		return nil, err
	}

	// The bucket is checked here rather than at the first PUT, because
	// "NoSuchBucket" arriving in the middle of a push reads as a transfer
	// failure when it is a configuration mistake.
	exists, err := client.BucketExists(ctx, bucket)
	if err != nil {
		return nil, t.unreachable(ref, err)
	}
	if !exists {
		return nil, domain.BackupError(domain.ErrNotFound,
			"the bucket %q does not exist, or these credentials cannot see it", bucket).
			WithHint("create it first; the manager does not create buckets, because " +
				"a typo would silently make a new one and the backups would go " +
				"somewhere nobody is watching")
	}

	return &bucketStore{client: client, bucket: bucket, prefix: strings.Trim(prefix, "/"), ref: ref, target: t}, nil
}

func (t *Target) client(ref ports.TargetRef) (*minio.Client, error) {
	creds := ref.Credentials

	endpoint, secure, err := resolveEndpoint(creds.Endpoint)
	if err != nil {
		return nil, err
	}

	// Keyed on every value that authenticates the client, not just the access
	// key id: a rotated secret under the same id would otherwise be ignored
	// for the life of the process. Hashed, because a map key holding a secret
	// access key is a secret access key in a heap dump.
	sum := sha256.Sum256([]byte(
		creds.AccessKeyID + "\x00" + creds.SecretAccessKey + "\x00" + creds.SessionToken))
	key := endpoint + "|" + creds.Region + "|" + hex.EncodeToString(sum[:8])

	t.mu.Lock()
	if client, ok := t.clients[key]; ok {
		t.mu.Unlock()
		return client, nil
	}
	t.mu.Unlock()

	region := creds.Region
	if region == "" {
		region = DefaultRegion
	}

	// No credentials at all is a legitimate configuration -- an EC2
	// instance role, or a public MinIO in a test -- so it is passed through
	// rather than refused. What is refused is half a credential, which is
	// always a mistake and always confusing at the moment it fails.
	if (creds.AccessKeyID == "") != (creds.SecretAccessKey == "") {
		return nil, domain.BackupError(nil,
			"the credentials for this backup target have an access key id or a "+
				"secret access key, but not both").
			WithHint("both fields are needed; leave both empty to use the " +
				"instance's own role")
	}

	client, err := minio.New(endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(creds.AccessKeyID, creds.SecretAccessKey, creds.SessionToken),
		// Always verified. There is no flag that reaches this, for the
		// same reason no flag skips a release's signature: a target
		// whose certificate is not checked is a target somebody else
		// can be.
		Secure:    secure,
		Region:    region,
		Transport: transport(),
	})
	if err != nil {
		return nil, domain.BackupError(err, "cannot build a client for %s", endpoint)
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if existing, ok := t.clients[key]; ok {
		return existing, nil
	}
	t.clients[key] = client
	return client, nil
}

func transport() http.RoundTripper {
	return &http.Transport{
		ResponseHeaderTimeout: 60 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	}
}

// resolveEndpoint reads the endpoint override.
//
// A bare host means TLS. http:// is the one way to ask for plaintext, and it
// has to be written out: an operator who types it has said so, and nobody
// reaches it by default or by a flag.
func resolveEndpoint(raw string) (endpoint string, secure bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultEndpoint, true, nil
	}
	if !strings.Contains(raw, "://") {
		return raw, true, nil
	}

	u, parseErr := url.Parse(raw)
	if parseErr != nil || u.Host == "" {
		return "", false, domain.Usage("the backup target endpoint %q is not a URL", raw).
			WithHint("write a host like minio.example:9000, or https://minio.example")
	}
	switch u.Scheme {
	case "https":
		return u.Host, true, nil
	case "http":
		return u.Host, false, nil
	default:
		return "", false, domain.Usage(
			"the backup target endpoint %q uses the %q scheme", raw, u.Scheme).
			WithHint("an endpoint is https:// or, when you mean it, http://")
	}
}

func (t *Target) unreachable(ref ports.TargetRef, cause error) error {
	var resp minio.ErrorResponse
	if errors.As(cause, &resp) {
		switch resp.Code {
		case "InvalidAccessKeyId", "SignatureDoesNotMatch", "AccessDenied":
			return domain.BackupError(nil,
				"the backup target %s refused these credentials (%s)", ref, resp.Code).
				WithHint("check the secret named by --credentials; on a rebuilt " +
					"machine, pass them with --credentials-file instead")
		}
	}
	return domain.BackupError(cause, "cannot reach the backup target %s", ref).
		WithHint("check network access to the endpoint and that the bucket exists")
}

// bucketStore is blob.Store over a bucket and prefix.
type bucketStore struct {
	client *minio.Client
	bucket string
	prefix string
	ref    ports.TargetRef
	target *Target
}

var _ blob.Store = (*bucketStore)(nil)

func (s *bucketStore) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	object, err := s.resolve(key)
	if err != nil {
		return err
	}

	// No `.partial` dance here, unlike the filesystem and SFTP stores: an
	// S3 PUT is atomic by definition, so a half-written object is not a
	// state this store can be in. An interrupted push leaves whole objects
	// and no manifest, which is what blob.Push relies on.
	if _, err := s.client.PutObject(ctx, s.bucket, object, r, size,
		minio.PutObjectOptions{ContentType: "application/octet-stream"}); err != nil {
		return s.target.unreachable(s.ref, err)
	}
	return nil
}

func (s *bucketStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	object, err := s.resolve(key)
	if err != nil {
		return nil, err
	}

	reader, err := s.client.GetObject(ctx, s.bucket, object, minio.GetObjectOptions{})
	if err != nil {
		return nil, s.target.unreachable(s.ref, err)
	}

	// GetObject is lazy: it reports nothing until the first read, so a
	// missing object would otherwise surface as a read error deep inside a
	// copy rather than as "there is no such backup".
	if _, err := reader.Stat(); err != nil {
		_ = reader.Close()
		if notFound(err) {
			return nil, fs.ErrNotExist
		}
		return nil, s.target.unreachable(s.ref, err)
	}
	return reader, nil
}

func (s *bucketStore) Keys(ctx context.Context, prefix string) ([]string, error) {
	search := s.prefix
	if prefix != "" {
		search = joinKey(s.prefix, prefix)
	}

	var out []string
	for object := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:    search,
		Recursive: true,
	}) {
		if object.Err != nil {
			return nil, s.target.unreachable(s.ref, object.Err)
		}
		key := strings.TrimPrefix(strings.TrimPrefix(object.Key, s.prefix), "/")
		if key == "" {
			continue
		}
		// Filtered again after trimming, because a listing prefix is a
		// string match rather than a path one: asking for `20260101/`
		// and being handed `20260101-old/db.age` is a removal deleting a
		// neighbour's component. The other two stores match on the
		// trailing slash, and the contract says all three behave alike.
		if prefix != "" && !strings.HasPrefix(key, prefix) {
			continue
		}
		out = append(out, key)
	}

	sort.Strings(out)
	return out, nil
}

func (s *bucketStore) Delete(ctx context.Context, key string) error {
	object, err := s.resolve(key)
	if err != nil {
		return err
	}
	if err := s.client.RemoveObject(ctx, s.bucket, object,
		minio.RemoveObjectOptions{}); err != nil {
		if notFound(err) {
			return nil
		}
		return s.target.unreachable(s.ref, err)
	}
	return nil
}

// resolve joins a key onto the prefix and refuses one that escapes it.
//
// Object keys are not paths and `..` is a legal character sequence in one --
// which is exactly why it has to be refused here rather than assumed harmless.
// A manifest on a target is a file this manager may not have written, and its
// component paths decide what a fetch reads and what a removal deletes.
func (s *bucketStore) resolve(key string) (string, error) {
	if key == "" {
		return "", domain.BackupError(nil, "the backup names an empty component path")
	}
	if strings.Contains(key, "..") || strings.HasPrefix(key, "/") {
		return "", domain.BackupError(nil,
			"the backup names a component outside the target: %q", key).
			WithHint("this backup was not written by this manager; do not restore from it")
	}
	return joinKey(s.prefix, key), nil
}

func joinKey(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.Trim(p, "/"); p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "/")
}

func notFound(err error) bool {
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		return resp.Code == "NoSuchKey" || resp.StatusCode == http.StatusNotFound
	}
	return false
}
