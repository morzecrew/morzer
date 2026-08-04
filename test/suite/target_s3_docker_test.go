//go:build docker

package suite

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/target/s3"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/contract"
	"github.com/morzecrew/morzer/test/dockerlab"
)

// MinIO is the stand-in for every S3-compatible store, which is the point: the
// adapter is written against the API rather than against a vendor, so proving it
// here proves it for R2, B2 and GCS interoperability mode too.
//
// What a fake cannot prove and this can: that a PUT of a stream with a known
// size actually round-trips, that a listing of a prefix returns keys in the
// shape the adapter expects, and that a GET of a missing key produces a NoSuchKey
// the adapter recognises rather than a generic failure the restore path would
// misread as "the network is down".

const (
	minioAccessKey = "morzertestaccess"
	minioSecretKey = "morzertestsecret"
)

// startMinIO runs a MinIO server and returns credentials pointing at a fresh
// bucket.
func startMinIO(t *testing.T) ports.TargetCredentials {
	t.Helper()
	dockerlab.Require(t)
	dockerlab.Pull(t, dockerlab.ImageMinIO)

	container := dockerlab.Start(t, dockerlab.ImageMinIO, []int{9000}, map[string]string{
		"MINIO_ROOT_USER":     minioAccessKey,
		"MINIO_ROOT_PASSWORD": minioSecretKey,
	}, "server", "/data")

	container.WaitReady(t, 90*time.Second, "mc", "ready", "local")

	return ports.TargetCredentials{
		AccessKeyID:     minioAccessKey,
		SecretAccessKey: minioSecretKey,
		// http:// deliberately spelled out. A bare host means TLS, and
		// there is no flag anywhere that turns verification off -- so a
		// test fixture has to say plainly that it is plaintext, which is
		// also what makes it impossible to reach this configuration by
		// accident in production.
		Endpoint: "http://" + container.HostPort(t, 9000),
		Region:   "us-east-1",
	}
}

// createBucket makes a bucket with the SDK rather than with the adapter under
// test. The adapter deliberately cannot: a typo would silently create a new
// bucket and the backups would go somewhere nobody is watching.
func createBucket(t *testing.T, creds ports.TargetCredentials, name string) {
	t.Helper()

	client := rawClient(t, creds)
	exists, err := client.BucketExists(context.Background(), name)
	require.NoError(t, err)
	if exists {
		return
	}
	require.NoError(t, client.MakeBucket(context.Background(), name,
		minio.MakeBucketOptions{Region: creds.Region}))
}

// listBucket enumerates the raw object keys, which is what the contract suite
// needs in order to assert about what a push actually put there rather than
// about what a fetch brings back.
func listBucket(t *testing.T, creds ports.TargetCredentials, ref ports.TargetRef) []string {
	t.Helper()

	bucket, prefix := ref.Bucket()
	client := rawClient(t, creds)

	var out []string
	for object := range client.ListObjects(context.Background(), bucket,
		minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		require.NoError(t, object.Err)
		out = append(out, object.Key)
	}
	return out
}

func rawClient(t *testing.T, creds ports.TargetCredentials) *minio.Client {
	t.Helper()

	endpoint := strings.TrimPrefix(creds.Endpoint, "http://")
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(creds.AccessKeyID, creds.SecretAccessKey, ""),
		Secure: false,
		Region: creds.Region,
	})
	require.NoError(t, err)
	return client
}

// addComponent records a file in a backup's manifest, the way a hook artifact
// arrives.
func addComponent(t *testing.T, backupDir, path string, size int) {
	t.Helper()

	manifestPath := filepath.Join(backupDir, ports.BackupManifestFileName)
	data, err := os.ReadFile(manifestPath)
	require.NoError(t, err)

	var manifest ports.BackupManifest
	require.NoError(t, json.Unmarshal(data, &manifest))

	// Digested like the real engine does. Without it `verify` skips the
	// component entirely, so a fixture that omitted it would let a corrupt
	// round trip pass.
	sum, err := atomicfs.DigestFile(filepath.Join(backupDir, filepath.FromSlash(path)))
	require.NoError(t, err)

	manifest.Components = append(manifest.Components, ports.ComponentRecord{
		Component: ports.ComponentFiles, Path: path,
		Size: int64(size), SHA256: sum, Encryption: ports.EncryptionAge,
	})

	out, err := json.MarshalIndent(manifest, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(manifestPath, out, 0o600))
}

func TestBackupTargetContract_S3(t *testing.T) {
	creds := startMinIO(t)

	var n int
	contract.RunBackupTargetSuite(t, func(t *testing.T) contract.BackupTargetHarness {
		n++
		bucket := fmt.Sprintf("morzer-contract-%d", n)
		createBucket(t, creds, bucket)

		ref, err := ports.TargetURL("s3://" + bucket + "/backups")
		require.NoError(t, err)
		ref = ref.WithCredentials(creds)

		return contract.BackupTargetHarness{
			Target: s3.New(),
			Ref:    ref,
			Keys:   func() []string { return listBucket(t, creds, ref) },
		}
	})
}

// TestS3RefusesABucketThatIsNotThere. "NoSuchBucket" arriving in the middle of a
// push reads as a transfer failure when it is a configuration mistake, so it is
// caught before any bytes move.
func TestS3RefusesABucketThatIsNotThere(t *testing.T) {
	creds := startMinIO(t)

	ref, err := ports.TargetURL("s3://morzer-no-such-bucket/backups")
	require.NoError(t, err)
	ref = ref.WithCredentials(creds)

	_, err = s3.New().List(context.Background(), ref)
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Hint, "create it first",
		"the refusal must say the manager does not create buckets, and why")
}

// TestS3RefusesWrongCredentialsByName. During a recovery an operator is typing
// keys from a password manager under stress; "access denied" without a remedy is
// where that stops.
func TestS3RefusesWrongCredentialsByName(t *testing.T) {
	creds := startMinIO(t)
	createBucket(t, creds, "morzer-badcreds")

	creds.SecretAccessKey = "wrong-secret-entirely"

	ref, err := ports.TargetURL("s3://morzer-badcreds/backups")
	require.NoError(t, err)
	ref = ref.WithCredentials(creds)

	_, err = s3.New().List(context.Background(), ref)
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Hint, "--credentials-file",
		"the remedy has to name the escape hatch, because this failure happens "+
			"most often on a rebuilt machine")
}

// TestS3PushSurvivesABackupWithNestedComponents. Object keys are not paths, and
// a hook artifact in a subdirectory is the case where that difference shows.
func TestS3PushSurvivesABackupWithNestedComponents(t *testing.T) {
	creds := startMinIO(t)
	createBucket(t, creds, "morzer-nested")

	ref, err := ports.TargetURL("s3://morzer-nested/backups")
	require.NoError(t, err)
	ref = ref.WithCredentials(creds)

	local := writeTestBackup(t, "20260101T000000Z", map[string]string{
		"database.sql.age": "ciphertext",
	})
	// A nested component, added to the manifest by hand the way a hook
	// artifact arrives.
	require.NoError(t, os.MkdirAll(filepath.Join(local, "files"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(local, "files", "uploads.tar.age"),
		[]byte("nested ciphertext"), 0o600))
	addComponent(t, local, "files/uploads.tar.age", len("nested ciphertext"))

	adapter := s3.New()
	remote, err := adapter.Push(context.Background(), ref, local, "20260101T000000Z")
	require.NoError(t, err)

	back := filepath.Join(t.TempDir(), "fetched")
	require.NoError(t, adapter.Fetch(context.Background(), remote, back))

	data, err := os.ReadFile(filepath.Join(back, "files", "uploads.tar.age"))
	require.NoError(t, err, "a nested component did not survive the object store")
	assert.Equal(t, "nested ciphertext", string(data))
}
