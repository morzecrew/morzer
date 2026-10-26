package clitest_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	gominisign "github.com/jedisct1/go-minisign"

	"github.com/morzecrew/morzer/internal/adapters/source/oci"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/clitest"
)

// TestAnUnattendedTickTakesThePreUpdateBackup.
//
// `update --unattended` is what the update timer runs, and it returned before
// the line that wires the backup engine. Every suite test of the tick hands
// the operation a fake engine, so the tick passed there and failed on every
// real machine with `backup.before_update` on: "no backup engine is
// configured", at step 3, every night.
//
// Asserted by the backup it leaves rather than by the exit code. Without
// Docker the update fails later, at the pull, and that is not what this is
// about; the pre-update backup is taken before anything needs a runtime.
func TestAnUnattendedTickTakesThePreUpdateBackup(t *testing.T) {
	r := clitest.New(t)
	sk, publicKey := minisignKeypair(t)

	// The gate refuses any machine that does not require a signature, so
	// the tick reaches the update only on one that does. A sandbox, so the
	// gate asks nothing about schemas the example bundle does not declare.
	signBundle(t, sk, r.Bundle)
	r.Run("init",
		"--release", r.Bundle,
		"--profile", "embedded",
		"--domain", "demo.example",
		"--no-recovery-recipient",
		"--install-units=false",
		"--mode", "dev",
		"--require-signature",
		"--signing-key", publicKey,
	).ExitCode(0)

	next := r.BundleAt("1.3.0")
	signBundle(t, sk, next)
	archive := filepath.Join(t.TempDir(), "bundle.tar.zst")
	r.Run("release", "archive", next, "-o", archive).ExitCode(0)
	channel := serveChannel(t, archive)

	r.Run("config", "set",
		"update.check=true",
		"update.channel="+channel,
		"update.auto_apply=true",
	).ExitCode(0)

	r.Run("update", "--unattended").NoOutputContains("no backup engine is configured")

	// Nothing before the tick takes a backup, so the one listed is its.
	r.Run("backup", "list", "--json").ExitCode(0).FieldLen("data", 1)
}

// serveChannel publishes one release archive on a loopback registry, which the
// OCI source reads over plain HTTP, and returns the channel reference.
//
// Only what a fetch reads: the tag's manifest, its empty config and the one
// layer. A registry that answered more would be testing itself.
func serveChannel(t *testing.T, archivePath string) string {
	t.Helper()

	archive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	config := []byte(`{}`)
	blobs := map[string][]byte{digestOf(archive): archive, digestOf(config): config}

	manifest, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"artifactType":  oci.MediaType,
		"config": map[string]any{
			"mediaType": "application/vnd.oci.empty.v1+json",
			"digest":    digestOf(config),
			"size":      len(config),
		},
		"layers": []map[string]any{{
			"mediaType": oci.MediaType,
			"digest":    digestOf(archive),
			"size":      len(archive),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		path := req.URL.Path
		switch {
		case path == "/v2/":
		case strings.Contains(path, "/manifests/"):
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			w.Header().Set("Docker-Content-Digest", digestOf(manifest))
			w.Header().Set("Content-Length", strconv.Itoa(len(manifest)))
			if req.Method != http.MethodHead {
				_, _ = w.Write(manifest)
			}
		case strings.Contains(path, "/blobs/"):
			blob, ok := blobs[path[strings.LastIndex(path, "/")+1:]]
			if !ok {
				http.NotFound(w, req)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(blob)))
			_, _ = w.Write(blob)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(srv.Close)

	return "oci://" + strings.TrimPrefix(srv.URL, "http://") + "/demo/bundle:latest"
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// signBundle does what a vendor's release pipeline does: list every file in
// SHA256SUMS and sign that list.
func signBundle(t *testing.T, sk gominisign.PrivateKey, dir string) {
	t.Helper()

	var sums []byte
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == ports.SumsFileName || rel == ports.SignatureFileName {
			return nil
		}
		digest, err := atomicfs.DigestFile(path)
		if err != nil {
			return err
		}
		sums = append(sums, []byte(digest[len("sha256:"):]+"  "+filepath.ToSlash(rel)+"\n")...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sumsPath := filepath.Join(dir, ports.SumsFileName)
	if err := os.WriteFile(sumsPath, sums, 0o644); err != nil {
		t.Fatal(err)
	}

	sig, err := sk.SignFile(sumsPath, gominisign.SignOptions{})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := sig.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ports.SignatureFileName), encoded, 0o644); err != nil {
		t.Fatal(err)
	}
}

// minisignKeypair builds an unencrypted minisign key and its public half in
// the form `minisign -G` prints. The library only reads keys, so the test
// assembles one from an Ed25519 key.
func minisignKeypair(t *testing.T) (gominisign.PrivateKey, string) {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sk := gominisign.PrivateKey{
		SignatureAlgorithm: [2]byte{'E', 'd'},
		ChecksumAlgorithm:  [2]byte{'B', '2'},
	}
	if _, err := rand.Read(sk.KeyId[:]); err != nil {
		t.Fatal(err)
	}
	copy(sk.SecretKey[:], priv)

	pk := sk.PublicKey()
	blob := append(append(pk.SignatureAlgorithm[:], pk.KeyId[:]...), pk.PublicKey[:]...)
	return sk, base64.StdEncoding.EncodeToString(blob)
}
