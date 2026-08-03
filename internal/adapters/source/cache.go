package source

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
)

// TempCache holds what a network transport downloaded, for the life of one
// command.
//
// Every caller resolves a reference and then fetches it: `release fetch` does,
// and so does `update`, because reading a bundle's manifest and copying it into
// the store are separate steps by design. Downloading the same artifact twice
// per command is a waste an operator on a slow link would notice, and the fix
// is small enough that both transports should share it rather than each grow
// their own.
//
// It is deliberately not a persistent cache. A cache that survives the process
// is a cache with invalidation, and "the bundle I fetched an hour ago" is
// exactly the thing a content digest exists to stop anyone assuming.
type TempCache struct {
	mu    sync.Mutex
	paths map[string]string
	dir   string
	name  string
}

// NewTempCache returns a cache whose temporary directory is named after the
// transport using it, so a stray one is attributable.
func NewTempCache(name string) *TempCache {
	return &TempCache{paths: map[string]string{}, name: name}
}

// Lookup returns a previously downloaded path for key.
func (c *TempCache) Lookup(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	path, ok := c.paths[key]
	return path, ok
}

// Reserve returns the path a download for key should be written to.
//
// Always `.tar.zst`: what a transport carries is an archive, and the extension
// is what tells the local source how to read it. A server-supplied filename
// would be attacker-chosen input deciding how bytes get parsed.
func (c *TempCache) Reserve() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.dir == "" {
		dir, err := os.MkdirTemp("", "morzer-"+c.name+"-")
		if err != nil {
			return "", domain.Internal(err, "cannot create a download directory")
		}
		c.dir = dir
	}
	return filepath.Join(c.dir, "bundle-"+itoa(len(c.paths))+".tar.zst"), nil
}

// Store records a completed download.
func (c *TempCache) Store(key, path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paths[key] = path
}

// Close removes everything downloaded. Safe to call more than once.
func (c *TempCache) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.dir == "" {
		return nil
	}
	err := atomicfs.RemoveAll(c.dir)
	c.dir, c.paths = "", map[string]string{}
	return err
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
