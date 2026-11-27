package quadlet

import (
	"os"
	"path"
	"path/filepath"
	"sort"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

var _ ports.BundleReader = (*Runtime)(nil)

// ReadBundle reads the Quadlet files themselves, inside the release, because
// `release verify` runs where no Podman is installed.
//
// A service is a `.container` file's name without the product's prefix, a
// one-shot included, since a task names it. A variable is one a value names,
// in either of systemd's forms: a comment interpolates nothing.
func (r *Runtime) ReadBundle(product, root string, files []string) (ports.BundleFiles, error) {
	dir, err := os.OpenRoot(root)
	if err != nil {
		return ports.BundleFiles{}, domain.ValidationError(err, "cannot read the release at %s", root)
	}
	defer func() { _ = dir.Close() }()

	b, err := readFiles(product, files, func(name string) ([]byte, error) {
		return dir.ReadFile(path.Clean(filepath.ToSlash(name)))
	})
	if err != nil {
		return ports.BundleFiles{}, err
	}

	var out ports.BundleFiles
	for _, u := range b.containers() {
		out.Services = append(out.Services, u.service)
	}
	sort.Strings(out.Services)
	seen := map[string]bool{}
	for _, u := range b.units {
		for _, e := range u.entries {
			for _, name := range variables([]byte(e.value)) {
				if !seen[name] {
					seen[name] = true
					out.Variables = append(out.Variables, name)
				}
			}
		}
	}
	sort.Strings(out.Variables)
	return out, nil
}
