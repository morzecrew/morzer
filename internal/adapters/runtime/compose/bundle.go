package compose

import (
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

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
// line naming a variable is not a reference to it. An `include` is followed as
// Compose follows it, relative to the file that includes it, while it stays
// inside the bundle; one that does not, such as a remote one, leaves the
// services it adds unknown, and says so.
func (r *Runtime) ReadBundle(root string, files []string) (ports.BundleFiles, error) {
	dir, err := os.OpenRoot(root)
	if err != nil {
		return ports.BundleFiles{}, domain.ValidationError(err, "cannot read the release at %s", root)
	}
	defer func() { _ = dir.Close() }()

	var out ports.BundleFiles
	services := map[string]bool{}
	variables := map[string]bool{}
	seen := map[string]bool{}
	queue := slices.Clone(files)
	for len(queue) > 0 {
		name := path.Clean(filepath.ToSlash(queue[0]))
		queue = queue[1:]
		if seen[name] {
			continue
		}
		seen[name] = true

		raw, err := dir.ReadFile(name)
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
		included, complete := includes(doc["include"])
		if !complete {
			out.ServicesPartial = true
		}
		for _, inc := range included {
			next := path.Join(path.Dir(name), inc)
			if next == ".." || strings.HasPrefix(next, "../") {
				out.ServicesPartial = true
				continue
			}
			queue = append(queue, next)
		}
		collectVariables(doc, variables)
	}
	out.Services = slices.Sorted(maps.Keys(services))
	out.Variables = slices.Sorted(maps.Keys(variables))
	return out, nil
}

// includes returns the bundle-relative paths an `include` names, in its short
// and long spellings, and whether that is all of them: a remote or absolute
// one, one Compose would interpolate first, or a shape this does not know, is
// not followed.
func includes(v any) ([]string, bool) {
	if v == nil {
		return nil, true
	}
	entries, ok := v.([]any)
	if !ok {
		return nil, false
	}
	var out []string
	complete := true
	add := func(p any) {
		s, ok := p.(string)
		if !ok || path.IsAbs(s) || strings.Contains(s, "://") || strings.HasPrefix(s, "git@") ||
			strings.Contains(s, "$") {
			complete = false
			return
		}
		out = append(out, s)
	}
	for _, e := range entries {
		switch t := e.(type) {
		case string:
			add(t)
		case map[string]any:
			if paths, ok := t["path"].([]any); ok {
				for _, p := range paths {
					add(p)
				}
			} else {
				add(t["path"])
			}
		default:
			complete = false
		}
	}
	return out, complete
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
