package ociserve

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	godigest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// The server is handed a directory of a customer's private images and a URL
// path an outside process controls. Everything below is about that pair: what
// it will serve, what it will not, and what it says when the bundle is not
// what its own index claims.

// layout writes a minimal OCI image layout and returns its directory.
func layout(t *testing.T) (dir, manifestDigest, blobDigest string) {
	t.Helper()
	dir = t.TempDir()

	blob := []byte("a layer, for the purposes of argument")
	blobDigest = digestOf(blob)

	manifest, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     ocispec.MediaTypeImageManifest,
		"layers": []map[string]any{{
			"mediaType": ocispec.MediaTypeImageLayerGzip,
			"digest":    blobDigest,
			"size":      len(blob),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest = digestOf(manifest)

	writeBlob(t, dir, blobDigest, blob)
	writeBlob(t, dir, manifestDigest, manifest)

	index, err := json.Marshal(ocispec.Index{
		Manifests: []ocispec.Descriptor{{
			MediaType: ocispec.MediaTypeImageManifest,
			Digest:    godigest.Digest(manifestDigest),
			Size:      int64(len(manifest)),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, ocispec.ImageIndexFile), index)
	write(t, filepath.Join(dir, ocispec.ImageLayoutFile), []byte(`{"imageLayoutVersion":"1.0.0"}`))
	return dir, manifestDigest, blobDigest
}

func writeBlob(t *testing.T, dir, dgst string, data []byte) {
	t.Helper()
	algorithm, encoded, _ := strings.Cut(dgst, ":")
	path := filepath.Join(dir, ocispec.ImageBlobsDir, algorithm, encoded)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, path, data)
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func serve(t *testing.T, dir string) *Server {
	t.Helper()
	s, err := Start(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// reply is a response already read and closed.
//
// The response object does not escape this helper: a test that held one would
// have to close it, and a body left open in a table-driven loop leaks a
// connection per case.
type reply struct {
	status int
	header http.Header
	body   []byte
}

func get(t *testing.T, method, url string) reply {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return reply{status: res.StatusCode, header: res.Header, body: body}
}

// TestItServesTheThreeRequestsAPullMakes.
//
// A version probe, a manifest by digest, and a blob -- that is the whole of
// the distribution API a pull exercises, and the reason this is worth pinning
// is that the client is the Docker daemon rather than anything in this
// repository. A missing Docker-Content-Digest header or a wrong media type
// fails in the daemon's vocabulary, a long way from here.
func TestItServesTheThreeRequestsAPullMakes(t *testing.T) {
	dir, manifestDigest, blobDigest := layout(t)
	s := serve(t, dir)
	base := "http://" + s.Addr()

	if res := get(t, http.MethodGet, base+"/v2/"); res.status != http.StatusOK {
		t.Errorf("the version probe answered %d", res.status)
	}

	res := get(t, http.MethodGet, base+"/v2/demo/app/manifests/"+manifestDigest)
	if res.status != http.StatusOK {
		t.Fatalf("the manifest answered %d: %s", res.status, res.body)
	}
	if got := res.header.Get("Docker-Content-Digest"); got != manifestDigest {
		t.Errorf("Docker-Content-Digest = %q, want %q", got, manifestDigest)
	}
	if got := res.header.Get("Content-Type"); got != ocispec.MediaTypeImageManifest {
		t.Errorf("Content-Type = %q, want the descriptor's media type", got)
	}
	if digestOf(res.body) != manifestDigest {
		t.Error("the manifest served is not the manifest the index names")
	}

	res = get(t, http.MethodGet, base+"/v2/demo/app/blobs/"+blobDigest)
	if res.status != http.StatusOK {
		t.Fatalf("the blob answered %d: %s", res.status, res.body)
	}
	if digestOf(res.body) != blobDigest {
		t.Error("the blob served does not hash to the digest it was asked for")
	}

	// HEAD is how a client asks whether it needs the bytes at all.
	res = get(t, http.MethodHead, base+"/v2/demo/app/blobs/"+blobDigest)
	if res.status != http.StatusOK {
		t.Errorf("HEAD answered %d", res.status)
	}
	if len(res.body) != 0 {
		t.Errorf("HEAD returned %d bytes of res.body", len(res.body))
	}
	if res.header.Get("Content-Length") == "" {
		t.Error("HEAD did not report a length, which is the only thing it is for")
	}

	if s.Mismatch() != nil {
		t.Errorf("an intact layout reported a mismatch: %v", s.Mismatch())
	}
}

// TestItIsReadOnly.
//
// The registry serves a bundle; it does not accept one. An upload endpoint
// would make this a place other things on the machine could put images, which
// is a different program with a different threat model.
func TestItIsReadOnly(t *testing.T) {
	dir, _, blobDigest := layout(t)
	s := serve(t, dir)
	base := "http://" + s.Addr()

	for _, method := range []string{
		http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
	} {
		res := get(t, method, base+"/v2/demo/app/blobs/uploads/")
		if res.status != http.StatusMethodNotAllowed {
			t.Errorf("%s answered %d, want 405", method, res.status)
		}
	}

	// And the refusal is about the method, not about the path: a PUT of a
	// blob that exists is refused exactly as one that does not.
	res := get(t, http.MethodPut, base+"/v2/demo/app/blobs/"+blobDigest)
	if res.status != http.StatusMethodNotAllowed {
		t.Errorf("PUT of an existing blob answered %d, want 405", res.status)
	}
}

// TestAPathIsNotAWayIntoTheFilesystem.
//
// The digest arrives as a URL segment, so it is the one input here an outside
// process chooses. Two guards stand behind each other -- the grammar refuses
// anything that is not a sha256 digest, and os.Root refuses an escape even if
// the grammar ever stops -- and this asserts the outcome rather than which one
// fired.
func TestAPathIsNotAWayIntoTheFilesystem(t *testing.T) {
	dir, _, _ := layout(t)

	// A file worth stealing, one level above the layout.
	secret := filepath.Join(filepath.Dir(dir), "age.key")
	write(t, secret, []byte("AGE-SECRET-KEY-1"))

	s := serve(t, dir)
	base := "http://" + s.Addr()

	// The status matters as much as the refusal. A malformed digest is
	// refused by the grammar -- 400 -- while a well-formed one the layout
	// does not carry is simply absent -- 404. Asserting only "not 200"
	// cannot tell those apart, and every one of these would still be
	// refused with the grammar deleted, because the path it built would
	// name no file. That is what makes the weaker assertion useless: it
	// passes just as happily with no check at all.
	for _, tc := range []struct {
		attempt string
		want    int
	}{
		{"sha256:" + strings.Repeat("g", 64), http.StatusBadRequest},     // right length, not hex
		{"sha256:ABCD" + strings.Repeat("a", 60), http.StatusBadRequest}, // hex is lowercase
		{"sha256:abc", http.StatusBadRequest},                            // right alphabet, wrong length
		{"md5:" + strings.Repeat("a", 32), http.StatusBadRequest},
		{"md5:" + strings.Repeat("a", 64), http.StatusBadRequest}, // a length the layout would accept
		{"not-a-digest", http.StatusBadRequest},
		{"sha256:../../age.key", http.StatusBadRequest},
		// Well formed, and simply not here.
		{"sha256:" + strings.Repeat("b", 64), http.StatusNotFound},
	} {
		res := get(t, http.MethodGet, base+"/v2/demo/app/blobs/"+tc.attempt)
		if res.status != tc.want {
			t.Errorf("%q answered %d, want %d: %s", tc.attempt, res.status, tc.want, res.body)
		}
		if strings.Contains(string(res.body), "AGE-SECRET-KEY") {
			t.Fatalf("%q read a file outside the layout", tc.attempt)
		}
	}

	// A path that climbs out never reaches the blob handler at all: it is
	// cleaned away first, so it names something outside the routes this
	// server has. Asserted separately because the mechanism is different,
	// and a reader who saw it in the table above would conclude the digest
	// grammar caught it.
	for _, attempt := range []string{"../age.key", "..%2F..%2Fage.key"} {
		res := get(t, http.MethodGet, base+"/v2/demo/app/blobs/"+attempt)
		if res.status != http.StatusNotFound {
			t.Errorf("%q answered %d, want 404", attempt, res.status)
		}
		if strings.Contains(string(res.body), "AGE-SECRET-KEY") {
			t.Fatalf("%q read a file outside the layout", attempt)
		}
	}
}

// TestOnlyWhatTheIndexNamesIsAManifest.
//
// A layer blob is served as a blob and refused as a manifest. The distinction
// is what keeps the served surface to the images the bundle declares: every
// blob the daemon asks for afterwards is one it learned about from a manifest
// this map admitted.
func TestOnlyWhatTheIndexNamesIsAManifest(t *testing.T) {
	dir, manifestDigest, blobDigest := layout(t)
	s := serve(t, dir)
	base := "http://" + s.Addr()

	res := get(t, http.MethodGet, base+"/v2/demo/app/manifests/"+blobDigest)
	if res.status != http.StatusNotFound {
		t.Errorf("a layer blob was served as a manifest with %d", res.status)
	}

	// And a tag is not a way in either: the manager pulls by digest and
	// nothing else, so there is no tag resolution to get wrong.
	res = get(t, http.MethodGet, base+"/v2/demo/app/manifests/latest")
	if res.status != http.StatusNotFound {
		t.Errorf("the tag `latest` answered %d", res.status)
	}

	// The manifest the index does name is still served, so the refusals
	// above are about the index rather than about manifests in general.
	res = get(t, http.MethodGet, base+"/v2/demo/app/manifests/"+manifestDigest)
	if res.status != http.StatusOK {
		t.Errorf("the indexed manifest answered %d", res.status)
	}
}

// TestABlobThatIsNotItsDigestIsReported.
//
// The refusal belongs to the daemon: it pulls by digest and rejects bytes that
// do not match, which is the guarantee. What this adds is the diagnosis --
// "filesystem layer verification failed" names no file, and an operator
// holding a bundle needs to know which blob in it is wrong.
func TestABlobThatIsNotItsDigestIsReported(t *testing.T) {
	dir, _, blobDigest := layout(t)

	// The digest keeps its name; the bytes stop matching it. This is what
	// a bundle assembled by hand, or truncated in transit past the point
	// SHA256SUMS covers, looks like from in here.
	writeBlob(t, dir, blobDigest, []byte("different bytes entirely"))

	s := serve(t, dir)
	if s.Mismatch() != nil {
		t.Fatal("a mismatch was reported before anything was served")
	}

	res := get(t, http.MethodGet, "http://"+s.Addr()+"/v2/demo/app/blobs/"+blobDigest)
	if res.status != http.StatusOK {
		t.Fatalf("the blob answered %d; it is served and then reported, not withheld",
			res.status)
	}

	err := s.Mismatch()
	if err == nil {
		t.Fatal("a blob that is not its digest was served without a word")
	}
	if !strings.Contains(err.Error(), blobDigest) {
		t.Errorf("the report does not name the blob: %v", err)
	}
}

// TestADirectoryIsNotABlob.
//
// os.Stat succeeds for a directory, so a layout carrying an empty directory
// named after a digest passes every check that only asks whether the path
// exists -- `release verify` refuses one, and this is the same rule at the
// point of use for a layout that arrived another way.
func TestADirectoryIsNotABlob(t *testing.T) {
	dir, _, _ := layout(t)
	const encoded = "1111111111111111111111111111111111111111111111111111111111111111"
	if err := os.MkdirAll(filepath.Join(dir, ocispec.ImageBlobsDir, "sha256", encoded), 0o755); err != nil {
		t.Fatal(err)
	}

	s := serve(t, dir)
	res := get(t, http.MethodGet, "http://"+s.Addr()+"/v2/demo/app/blobs/sha256:"+encoded)
	if res.status != http.StatusNotFound {
		t.Errorf("a directory named after a digest answered %d, want 404", res.status)
	}
}

// TestItBindsLoopbackOnly.
//
// The layout is a customer's private images and nothing here authenticates
// anybody. A bind that reached the network would publish them for as long as
// an install takes, which is the one failure of this package that would not
// look like a failure.
func TestItBindsLoopbackOnly(t *testing.T) {
	dir, _, _ := layout(t)
	s := serve(t, dir)

	if host, _, _ := strings.Cut(s.Addr(), ":"); host != "127.0.0.1" {
		t.Errorf("listening on %q, want 127.0.0.1", s.Addr())
	}
}

// TestASegmentedReferenceIsNotABlobName.
//
// A digest is one path segment. Something with a slash in it is a request for
// a path the layout does not have a name for, and is refused before the digest
// grammar is even consulted.
func TestASegmentedReferenceIsNotABlobName(t *testing.T) {
	dir, _, _ := layout(t)
	s := serve(t, dir)

	for _, path := range []string{
		"/v2/demo/app/blobs/sha256/abc",
		"/v2/demo/app/manifests/sha256/abc",
		"/v2/demo/app/blobs/",
		"/not-v2/demo/app/blobs/sha256:abc",
		"/",
	} {
		if res := get(t, http.MethodGet, "http://"+s.Addr()+path); res.status != http.StatusNotFound {
			t.Errorf("%q answered %d, want 404", path, res.status)
		}
	}
}

// TestAnIndexThatIsNotJSONIsRefusedAtTheDoor.
//
// Before a port is bound. A layout whose index cannot be read serves nothing,
// so the caller gets one error rather than a listener plus a pull that fails
// for a reason it cannot explain.
func TestAnIndexThatIsNotJSONIsRefusedAtTheDoor(t *testing.T) {
	dir, _, _ := layout(t)
	write(t, filepath.Join(dir, ocispec.ImageIndexFile), []byte("this is not an index"))

	if _, err := Start(dir); err == nil {
		t.Fatal("a layout whose index is not JSON was served")
	}
}

// TestALayoutWithNoIndexIsRefusedAtTheDoor.
//
// Before a port is bound, so a bundle that cannot be served does not leave a
// listener behind while the caller works out what went wrong.
func TestALayoutWithNoIndexIsRefusedAtTheDoor(t *testing.T) {
	if _, err := Start(t.TempDir()); err == nil {
		t.Fatal("a directory with no index.json was served")
	}
	if _, err := Start(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a directory that does not exist was served")
	}
}
