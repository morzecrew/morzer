package fakes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Registry is a writable OCI distribution registry held in memory: one
// repository, enough of the API for a push and the pull that reads it back.
//
// Separate from the pull suite's fake, which serves one fixed artifact for
// every tag -- the shape ten pull tests are written against. A push needs the
// opposite: tags that start absent, point where they were last written, and can
// be read back. It counts requests and writes, because "refused before
// anything was sent" and "a dry run writes nothing" are claims about traffic,
// not about results.
type Registry struct {
	mu        sync.Mutex
	blobs     map[string][]byte
	manifests map[string]stored
	tags      map[string]string

	// FailTag makes a manifest write under this tag fail, so a test can break
	// one channel move and watch what the push does about the rest.
	FailTag string

	// Retag points a tag at Decoy after every write to it: another
	// pipeline's tag write landing just after this one's.
	Retag string
	Decoy string

	requests  atomic.Int64
	writes    atomic.Int64
	uploads   atomic.Int64
	blobReads atomic.Int64
}

type stored struct {
	mediaType string
	body      []byte
}

// NewRegistry returns an empty registry. Serve it with httptest.NewServer; a
// loopback address is reached over plain HTTP by the OCI adapter's own rule.
func NewRegistry() *Registry {
	return &Registry{
		blobs:     map[string][]byte{},
		manifests: map[string]stored{},
		tags:      map[string]string{},
	}
}

// Requests counts every request, including the version probe.
func (r *Registry) Requests() int64 { return r.requests.Load() }

// Writes counts the requests that changed something: upload sessions, blob
// and manifest PUTs.
func (r *Registry) Writes() int64 { return r.writes.Load() }

// BlobReads counts blob downloads, which is how a test tells "recognised the
// same bundle" from "downloaded it to find out".
func (r *Registry) BlobReads() int64 { return r.blobReads.Load() }

// Tagged reports what a tag points at.
func (r *Registry) Tagged(tag string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.tags[tag]
	return d, ok
}

// Manifest returns the manifest a tag or digest addresses, decoded; the zero
// manifest when there is none.
func (r *Registry) Manifest(ref string) ocispec.Manifest {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out ocispec.Manifest
	if m, ok := r.manifests[r.resolve(ref)]; ok {
		_ = json.Unmarshal(m.body, &out)
	}
	return out
}

func (r *Registry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.requests.Add(1)
	path := req.URL.Path

	switch {
	case path == "/v2/" || path == "/v2":
		w.WriteHeader(http.StatusOK)
	case strings.HasSuffix(path, "/tags/list"):
		r.listTags(w)
	case strings.Contains(path, "/blobs/uploads/"):
		r.upload(w, req)
	case strings.Contains(path, "/blobs/"):
		r.blob(w, req, path[strings.Index(path, "/blobs/")+len("/blobs/"):])
	case strings.Contains(path, "/manifests/"):
		r.manifest(w, req, path[strings.Index(path, "/manifests/")+len("/manifests/"):])
	default:
		http.NotFound(w, req)
	}
}

// resolve maps a tag to its digest; a digest maps to itself. Callers hold mu.
func (r *Registry) resolve(ref string) string {
	if strings.HasPrefix(ref, "sha256:") {
		return ref
	}
	return r.tags[ref]
}

func (r *Registry) listTags(w http.ResponseWriter) {
	r.mu.Lock()
	tags := make([]string, 0, len(r.tags))
	for t := range r.tags {
		tags = append(tags, t)
	}
	r.mu.Unlock()
	sort.Strings(tags)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"name": "demo/bundle", "tags": tags})
}

// upload is the two-step monolithic push: POST opens a session, PUT with
// ?digest= completes it.
func (r *Registry) upload(w http.ResponseWriter, req *http.Request) {
	r.writes.Add(1)
	switch req.Method {
	case http.MethodPost:
		w.Header().Set("Location", fmt.Sprintf("%ssession-%d", req.URL.Path, r.uploads.Add(1)))
		w.WriteHeader(http.StatusAccepted)
	case http.MethodPut:
		body, err := io.ReadAll(req.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		dgst := digestOf(body)
		if want := req.URL.Query().Get("digest"); want != dgst {
			http.Error(w, "digest mismatch", http.StatusBadRequest)
			return
		}
		r.mu.Lock()
		r.blobs[dgst] = body
		r.mu.Unlock()
		w.Header().Set("Docker-Content-Digest", dgst)
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (r *Registry) blob(w http.ResponseWriter, req *http.Request, dgst string) {
	r.mu.Lock()
	body, ok := r.blobs[dgst]
	r.mu.Unlock()
	if !ok {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Docker-Content-Digest", dgst)
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	if req.Method == http.MethodHead {
		return
	}
	r.blobReads.Add(1)
	_, _ = w.Write(body)
}

func (r *Registry) manifest(w http.ResponseWriter, req *http.Request, ref string) {
	if req.Method == http.MethodPut {
		r.putManifest(w, req, ref)
		return
	}

	r.mu.Lock()
	dgst := r.resolve(ref)
	m, ok := r.manifests[dgst]
	r.mu.Unlock()
	if !ok {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Content-Type", m.mediaType)
	w.Header().Set("Docker-Content-Digest", dgst)
	w.Header().Set("Content-Length", fmt.Sprint(len(m.body)))
	if req.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(m.body)
}

func (r *Registry) putManifest(w http.ResponseWriter, req *http.Request, ref string) {
	r.writes.Add(1)
	if ref == r.FailTag {
		// A 4xx: the client retries a 5xx, and a test waiting out its
		// backoff proves nothing more.
		http.Error(w, "refused by the test", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	dgst := digestOf(body)
	if strings.HasPrefix(ref, "sha256:") && ref != dgst {
		http.Error(w, "digest mismatch", http.StatusBadRequest)
		return
	}

	r.mu.Lock()
	r.manifests[dgst] = stored{mediaType: req.Header.Get("Content-Type"), body: body}
	if !strings.HasPrefix(ref, "sha256:") {
		r.tags[ref] = dgst
		if ref == r.Retag {
			r.tags[ref] = r.Decoy
		}
	}
	r.mu.Unlock()

	w.Header().Set("Docker-Content-Digest", dgst)
	w.WriteHeader(http.StatusCreated)
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
