package cli

import (
	"io"
	"maps"
	"os"
	"slices"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// readParameterFiles reads `--set-file name=path` assignments and adds their
// values to set, the `name=value` assignments, and returns the paths they came
// from for the wizard's equivalent command. A name given both ways is refused,
// as ParseAssignments refuses one given twice.
//
// `-` is standard input, which can be read once. Each value is refused for a
// NUL byte or invalid UTF-8 whatever the parameter's type (RFC 0054 decision
// 9); the type's own Parse runs later, where every value's does. A dotted name
// is refused: installation settings have no file form.
func (a *App) readParameterFiles(set map[string]string, assignments []string) (values, paths map[string]string, err error) {
	paths, err = domain.ParseAssignments(assignments)
	if err != nil {
		return nil, nil, err
	}
	// Before anything is read: refused after the first read, a terminal
	// user would be waiting for an EOF they did not know was needed.
	for _, name := range slices.Sorted(maps.Keys(paths)) {
		if _, dup := set[name]; dup {
			return nil, nil, domain.Usage("parameter %q is set twice", name).
				WithHint("give it as name=value or with --set-file, not both")
		}
	}
	var stdin []string
	for _, name := range slices.Sorted(maps.Keys(paths)) {
		if paths[name] == "-" {
			stdin = append(stdin, name)
		}
	}
	if len(stdin) > 1 {
		return nil, nil, domain.Usage(
			"--set-file reads standard input for both %s and %s, and it can be read once", stdin[0], stdin[1])
	}

	values = make(map[string]string, len(set)+len(paths))
	maps.Copy(values, set)
	for _, name := range slices.Sorted(maps.Keys(paths)) {
		if ops.IsSettingName(name) {
			return nil, nil, domain.Usage("%s is an installation setting, and settings have no file form", name).
				WithHint("set it with `morzer config set %s=<value>`", name)
		}

		path := paths[name]
		var data []byte
		if path == "-" {
			data, err = io.ReadAll(a.Stream.In)
		} else {
			data, err = os.ReadFile(path) //nolint:gosec // the operator names the file
		}
		if err != nil {
			return nil, nil, domain.ValidationError(err, "cannot read the value of parameter %q from %s: %v",
				name, path, err)
		}

		if values[name], err = domain.ParameterFileValue(name, data); err != nil {
			return nil, nil, err
		}
	}
	return values, paths, nil
}
