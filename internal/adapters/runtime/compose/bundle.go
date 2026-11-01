package compose

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/goccy/go-yaml"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

var _ ports.BundleReader = (*Runtime)(nil)

// interpolation matches what Compose substitutes in a value: $NAME and
// ${NAME...}. `$$` is an escaped dollar, matched first so the name after it is
// not read as a variable.
var interpolation = regexp.MustCompile(`\$\$|\$\{?([A-Za-z_][A-Za-z0-9_]*)`)

// ReadBundle reads the Compose files themselves rather than `compose config`'s
// merge of them, because it runs where no Docker is installed.
//
// Values only: Compose interpolates no key and no comment, so a commented-out
// line naming a variable is not a reference to it. A file with `include` takes
// services from files this does not follow, and says so.
func (r *Runtime) ReadBundle(root string, files []string) (ports.BundleFiles, error) {
	dir, err := os.OpenRoot(root)
	if err != nil {
		return ports.BundleFiles{}, domain.ValidationError(err, "cannot read the release at %s", root)
	}
	defer func() { _ = dir.Close() }()

	var out ports.BundleFiles
	services := map[string]bool{}
	variables := map[string]bool{}
	for _, name := range files {
		raw, err := dir.ReadFile(filepath.ToSlash(filepath.Clean(name)))
		if err != nil {
			return ports.BundleFiles{}, domain.ValidationError(err, "cannot read %s", name)
		}
		var doc map[string]any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return ports.BundleFiles{}, domain.ValidationError(err, "%s is not a Compose file", name).
				WithHint("%s", yaml.FormatError(err, false, true))
		}
		if defined, ok := doc["services"].(map[string]any); ok {
			for service := range defined {
				services[service] = true
			}
		}
		if _, ok := doc["include"]; ok {
			out.ServicesPartial = true
		}
		collectVariables(doc, variables)
	}
	out.Services = slices.Sorted(maps.Keys(services))
	out.Variables = slices.Sorted(maps.Keys(variables))
	return out, nil
}

func collectVariables(v any, into map[string]bool) {
	switch t := v.(type) {
	case string:
		for _, m := range interpolation.FindAllStringSubmatch(t, -1) {
			if m[1] != "" {
				into[m[1]] = true
			}
		}
	case map[string]any:
		for _, e := range t {
			collectVariables(e, into)
		}
	case []any:
		for _, e := range t {
			collectVariables(e, into)
		}
	}
}
