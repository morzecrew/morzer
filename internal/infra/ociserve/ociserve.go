// Package ociserve serves an OCI image layout to a container runtime over
// loopback.
//
// It exists because of a measurement. A bundled image has to end up resolvable
// by the local daemon, and the only mechanism that gets it there with its
// registry digest intact is a real pull -- `docker load` discards the digest,
// and `docker tag` refuses to create a digest reference at all. A pull needs
// something speaking the distribution API, so this is that something: an
// HTTP server inside the manager's own process, bound to 127.0.0.1 on a port
// the kernel picks, serving one layout, read-only, for the length of one
// ingest.
//
// In-process rather than a registry container, which is what RFC 0011
// originally specified. A container needs its image, an image comes from a
// registry, and the machine this feature exists for is the one that cannot
// reach a registry -- so the container form was unavailable in precisely the
// case it was designed for. Nothing here needs anything that is not already in
// the binary.
//
// Read-only in the strict sense: there is no upload endpoint, and every method
// other than GET and HEAD is refused. A registry that can be written to is a
// registry that can be made to serve something other than the bundle.
package ociserve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/morzecrew/morzer/internal/domain"
)

// Server is a running layout server. Close it when the ingest is done.
type Server struct {
	// root confines every read to the layout directory. Digests arrive as
	// URL path segments, so the path a request produces is attacker-shaped
	// even though the bytes it reaches are the vendor's -- and os.Root
	// refuses an escape rather than relying on the digest grammar having
	// caught it first.
	root *os.Root

	// manifests is what index.json names, by digest. A blob is servable as
	// a manifest only if the index says it is one: everything the daemon
	// asks for afterwards it learned from a manifest this map admitted.
	manifests map[string]ocispec.Descriptor

	ln  net.Listener
	srv *http.Server
	wg  sync.WaitGroup

	mu sync.Mutex
	// mismatch records the first blob whose bytes did not hash to the
	// digest they were requested under. See Mismatch.
	mismatch error
}

// Start reads the layout at dir and serves it on a loopback port.
//
// dir is the layout itself -- the directory holding `oci-layout`,
// `index.json` and `blobs/` -- not the bundle that contains it.
func Start(dir string) (*Server, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, domain.RuntimeError(err, "cannot open the image layout at %s", dir)
	}

	index, err := readIndex(root)
	if err != nil {
		_ = root.Close()
		return nil, err
	}

	// Loopback, never 0.0.0.0. The layout is a customer's private images
	// and this process is not authenticating anybody: a bind that reached
	// the network would publish them for as long as an install takes.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = root.Close()
		return nil, domain.RuntimeError(err,
			"cannot listen on loopback to serve the bundle's images")
	}

	s := &Server{root: root, manifests: index, ln: ln}
	s.srv = &http.Server{
		Handler: http.HandlerFunc(s.serve),
		// The daemon is on this machine and the bytes come off local
		// disk, but a read timeout of zero means a stuck connection
		// holds a goroutine until the process exits. Generous, because
		// the write side is streaming gigabytes to a daemon that may be
		// unpacking them as they arrive.
		ReadHeaderTimeout: 30 * time.Second,
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		// http.ErrServerClosed is what Close looks like from in here.
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.recordMismatch(domain.RuntimeError(err, "the image layout server stopped"))
		}
	}()
	return s, nil
}

// Addr is the host:port the layout is served on.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Mismatch reports the first blob whose bytes did not hash to the digest they
// were served under, if any.
//
// The refusal itself is the daemon's: it pulls by digest and verifies every
// blob against what the manifest names, so bytes that do not match are
// rejected there and the image is never committed. What this adds is the
// diagnosis. Without it the operator gets the daemon's "filesystem layer
// verification failed", which names no file and suggests no cause; with it the
// ingest can say which blob in which bundle is not what its index claims.
//
// Hashed on the way past rather than before sending, which is the reason this
// is a report rather than a guard: verifying first would mean reading every
// layer twice, and the guard it would duplicate already exists one process
// away.
func (s *Server) Mismatch() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mismatch
}

// Close stops serving and releases the layout.
//
// Deliberately not governed by the ingest's context. A cancelled install still
// has to give back the port and the open layout, and a shutdown that inherited
// an already-cancelled context would return before either was released.
//
//nolint:contextcheck // teardown must outlive the operation it is tearing down
func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := s.srv.Shutdown(ctx)
	s.wg.Wait()
	if rootErr := s.root.Close(); err == nil {
		err = rootErr
	}
	return err
}

func (s *Server) recordMismatch(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mismatch == nil {
		s.mismatch = err
	}
}

// readIndex reads index.json and maps each manifest descriptor by digest.
func readIndex(root *os.Root) (map[string]ocispec.Descriptor, error) {
	f, err := root.Open(ocispec.ImageIndexFile)
	if err != nil {
		return nil, domain.RuntimeError(err, "cannot read the layout's %s",
			ocispec.ImageIndexFile)
	}
	defer func() { _ = f.Close() }()

	var index ocispec.Index
	if err := json.NewDecoder(f).Decode(&index); err != nil {
		return nil, domain.RuntimeError(err, "cannot parse the layout's %s",
			ocispec.ImageIndexFile)
	}

	out := make(map[string]ocispec.Descriptor, len(index.Manifests))
	for _, m := range index.Manifests {
		out[m.Digest.String()] = m
	}
	return out, nil
}

// serve routes the three requests a pull makes, and refuses everything else.
func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		// Including the upload endpoints, which is the point: this
		// serves a bundle, it does not accept one.
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, "UNSUPPORTED",
			"this registry is read-only")
		return
	}

	switch kind, name, ref := route(r.URL.Path); kind {
	case routeVersion:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
		_, _ = w.Write([]byte(`{}`))

	case routeManifest:
		desc, ok := s.manifests[ref]
		if !ok {
			// Including every tag: the manager pulls by digest and
			// nothing else, so a request for a name is a request
			// this server was not built to answer.
			writeError(w, http.StatusNotFound, "MANIFEST_UNKNOWN",
				"the layout carries no manifest %s", ref)
			return
		}
		s.blob(w, r, ref, desc.MediaType)

	case routeBlob:
		s.blob(w, r, ref, "application/octet-stream")

	default:
		writeError(w, http.StatusNotFound, "UNSUPPORTED",
			"%s names nothing this registry serves", name)
	}
}

type routeKind int

const (
	routeNone routeKind = iota
	routeVersion
	routeManifest
	routeBlob
)

// route splits a distribution path into its repository and reference.
//
// The repository name is parsed and then ignored, which is deliberate: a
// bundle's layout is addressed by digest, and the daemon has to be given some
// repository to pull from only because the reference grammar demands one. Two
// images in one layout differ by digest, not by name.
func route(urlPath string) (kind routeKind, name, ref string) {
	rest, ok := strings.CutPrefix(path.Clean(urlPath), "/v2")
	if !ok {
		return routeNone, urlPath, ""
	}
	if rest == "" || rest == "/" {
		return routeVersion, "", ""
	}

	// The repository may contain slashes, so the separator is found from
	// the right: everything before it is the name, everything after is one
	// segment of reference.
	for _, sep := range []struct {
		token string
		kind  routeKind
	}{
		{"/manifests/", routeManifest},
		{"/blobs/", routeBlob},
	} {
		if i := strings.LastIndex(rest, sep.token); i > 0 {
			ref = rest[i+len(sep.token):]
			if ref == "" || strings.Contains(ref, "/") {
				return routeNone, rest, ""
			}
			return sep.kind, strings.TrimPrefix(rest[:i], "/"), ref
		}
	}
	return routeNone, rest, ""
}

// blob writes the bytes stored under a digest, hashing them on the way past.
func (s *Server) blob(w http.ResponseWriter, r *http.Request, ref, mediaType string) {
	rel, digester, err := blobPath(ref)
	if err != nil {
		writeError(w, http.StatusBadRequest, "DIGEST_INVALID", "%s", err.Error())
		return
	}

	f, err := s.root.Open(rel)
	if err != nil {
		writeError(w, http.StatusNotFound, "BLOB_UNKNOWN",
			"the layout carries no blob %s", ref)
		return
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		// A directory named after a digest reads as present to every
		// check that only asks whether the path exists. `release
		// verify` refuses one; this is the same rule at the point of
		// use, for a layout that reached here another way.
		writeError(w, http.StatusNotFound, "BLOB_UNKNOWN",
			"the layout carries no blob %s", ref)
		return
	}

	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Docker-Content-Digest", ref)
	w.Header().Set("Content-Length", fmt.Sprint(info.Size()))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	if _, err := io.Copy(io.MultiWriter(w, digester), f); err != nil {
		// The client hung up, or the disk did. Either way the response
		// is already committed, so there is nothing to say in it.
		return
	}
	if got := "sha256:" + hex.EncodeToString(digester.Sum(nil)); got != ref {
		s.recordMismatch(domain.ValidationError(domain.ErrDigestMismatch,
			"the bundle's blob %s hashes to %s", ref, got).
			WithHint("the layout's index names a digest the blob does not have; " +
				"re-run `morzer release pack` on the vendor's machine"))
	}
}

// blobPath turns a digest into a path inside the layout, and refuses anything
// that is not one.
//
// The refusal is what keeps a URL segment from reaching the filesystem: the
// algorithm and the encoded form are both checked against their grammar before
// either is joined to a path. os.Root would refuse an escape anyway; this
// refuses the attempt with an error that says why.
func blobPath(ref string) (string, hash.Hash, error) {
	algorithm, encoded, ok := strings.Cut(ref, ":")
	if !ok {
		return "", nil, fmt.Errorf("%q is not a digest", ref)
	}
	if algorithm != "sha256" {
		// The only algorithm a manifest may pin, so the only one a
		// layout this manager wrote can contain.
		return "", nil, fmt.Errorf("%q is not a supported digest algorithm", algorithm)
	}
	if len(encoded) != sha256.Size*2 {
		return "", nil, fmt.Errorf("%q is not a sha256 digest", ref)
	}
	for _, c := range encoded {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", nil, fmt.Errorf("%q is not a sha256 digest", ref)
		}
	}
	return path.Join(ocispec.ImageBlobsDir, algorithm, encoded), sha256.New(), nil
}

// writeError answers in the shape a distribution client expects.
func writeError(w http.ResponseWriter, status int, code, format string, args ...any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"errors": []map[string]string{{
			"code":    code,
			"message": fmt.Sprintf(format, args...),
		}},
	})
}
