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
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Registry is a writable OCI distribution registry held in memory: enough of
// the API for a push and the pull that reads it back.
//
// Tags are held per repository, so a tag written to an image's repository is
// not one in the bundle's. Manifests and blobs are content-addressed and shared
// by every repository, which no test here tells apart from a registry that
// keeps them per repository.
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
	tags      map[string]string // repository:tag -> digest

	// FailTag makes a manifest write under this tag fail, so a test can break
	// one channel move and watch what the push does about the rest.
	FailTag string

	// Retag points a tag at Decoy after every write to it: another
	// pipeline's tag write landing just after this one's.
	Retag string
	Decoy string

	// DenyRead answers a manifest read under this tag with 403, as a
	// registry does for credentials that may not pull it.
	DenyRead string

	// ReadDelay holds every manifest read this long before it is served:
	// a slow registry, so a test can tell what was sampled before a read
	// from what was sampled after it.
	ReadDelay time.Duration
	readAt    map[string]time.Time

	requests  atomic.Int64
	writes    atomic.Int64
	uploads   atomic.Int64
	blobReads atomic.Int64
}

type stored struct {
	mediaType string
	body      []byte
}

// BundleRepository is the repository the push tests publish to, which Tagged,
// Manifest and the tag list read.
const BundleRepository = "demo/bundle"

// NewRegistry returns an empty registry. Serve it with httptest.NewServer; a
// loopback address is reached over plain HTTP by the OCI adapter's own rule.
func NewRegistry() *Registry {
	return &Registry{
		blobs:     map[string][]byte{},
		manifests: map[string]stored{},
		tags:      map[string]string{},
		readAt:    map[string]time.Time{},
	}
}

// ReadAt is when a manifest was last served under ref, a tag or a digest:
// after ReadDelay, as the client received it.
func (r *Registry) ReadAt(ref string) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.readAt[ref]
}

// Requests counts every request, including the version probe.
func (r *Registry) Requests() int64 { return r.requests.Load() }

// Writes counts the requests that changed something: upload sessions, blob
// and manifest PUTs.
func (r *Registry) Writes() int64 { return r.writes.Load() }

// BlobReads counts blob downloads, which is how a test tells "recognised the
// same bundle" from "downloaded it to find out".
func (r *Registry) BlobReads() int64 { return r.blobReads.Load() }

// Tagged reports what a tag in BundleRepository points at.
func (r *Registry) Tagged(tag string) (string, bool) { return r.TaggedIn(BundleRepository, tag) }

// TaggedIn reports what a tag in a repository points at.
func (r *Registry) TaggedIn(repository, tag string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.tags[repository+":"+tag]
	return d, ok
}

// Put stores a manifest as though it had been pushed to repository, under tag
// unless tag is empty, and returns its digest. It is not counted as a request
// or a write: it is how a test seeds what a release pins.
func (r *Registry) Put(repository, tag, mediaType string, body []byte) string {
	dgst := digestOf(body)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.manifests[dgst] = stored{mediaType: mediaType, body: body}
	if tag != "" {
		r.tags[repository+":"+tag] = dgst
	}
	return dgst
}

// Delete removes a manifest and every tag naming it, as a registry's retention
// does to an image nothing tags.
func (r *Registry) Delete(dgst string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.manifests, dgst)
	for key, d := range r.tags {
		if d == dgst {
			delete(r.tags, key)
		}
	}
}

// Manifest returns the manifest a tag in BundleRepository or a digest
// addresses, decoded; the zero manifest when there is none.
func (r *Registry) Manifest(ref string) ocispec.Manifest {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out ocispec.Manifest
	if m, ok := r.manifests[r.resolve(BundleRepository, ref)]; ok {
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
		r.listTags(w, repositoryOf(path, "/tags/list"))
	case strings.Contains(path, "/blobs/uploads/"):
		r.upload(w, req)
	case strings.Contains(path, "/blobs/"):
		r.blob(w, req, path[strings.Index(path, "/blobs/")+len("/blobs/"):])
	case strings.Contains(path, "/manifests/"):
		r.manifest(w, req, repositoryOf(path, "/manifests/"),
			path[strings.Index(path, "/manifests/")+len("/manifests/"):])
	default:
		http.NotFound(w, req)
	}
}

// repositoryOf is the repository in a request path: /v2/<repository><marker>….
func repositoryOf(path, marker string) string {
	return strings.TrimPrefix(path[:strings.Index(path, marker)], "/v2/")
}

// resolve maps a tag in a repository to its digest; a digest maps to itself.
// Callers hold mu.
func (r *Registry) resolve(repository, ref string) string {
	if strings.HasPrefix(ref, "sha256:") {
		return ref
	}
	return r.tags[repository+":"+ref]
}

func (r *Registry) listTags(w http.ResponseWriter, repository string) {
	r.mu.Lock()
	tags := make([]string, 0, len(r.tags))
	for key := range r.tags {
		if tag, ok := strings.CutPrefix(key, repository+":"); ok {
			tags = append(tags, tag)
		}
	}
	r.mu.Unlock()
	sort.Strings(tags)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"name": repository, "tags": tags})
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

func (r *Registry) manifest(w http.ResponseWriter, req *http.Request, repository, ref string) {
	if req.Method == http.MethodPut {
		r.putManifest(w, req, repository, ref)
		return
	}

	r.mu.Lock()
	dgst := r.resolve(repository, ref)
	m, ok := r.manifests[dgst]
	denied := r.DenyRead != "" && ref == r.DenyRead
	r.mu.Unlock()
	if denied {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":[{"code":"DENIED","message":"requested access to the resource is denied"}]}`))
		return
	}
	if !ok {
		http.NotFound(w, req)
		return
	}
	if req.Method != http.MethodHead {
		time.Sleep(r.ReadDelay)
		r.mu.Lock()
		r.readAt[ref] = time.Now()
		r.mu.Unlock()
	}
	w.Header().Set("Content-Type", m.mediaType)
	w.Header().Set("Docker-Content-Digest", dgst)
	w.Header().Set("Content-Length", fmt.Sprint(len(m.body)))
	if req.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(m.body)
}

func (r *Registry) putManifest(w http.ResponseWriter, req *http.Request, repository, ref string) {
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
		r.tags[repository+":"+ref] = dgst
		if ref == r.Retag {
			r.tags[repository+":"+ref] = r.Decoy
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
