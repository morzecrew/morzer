package quadlet

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
)

// parametersFile is the adapter's copy of the values the units read, beside
// them. Only Up writes it once it exists, so a Start after a Stop reads the
// values the containers were created with (RFC 0023 row 29). Quadlet reads
// only its own kinds of file, so the copy sits in its directory unread.
const parametersFile = "parameters.env"

// dropIn is the drop-in each installed file gets, named so a vendor's own
// drop-ins sort before it.
const dropIn = "90-morzer.conf"

// unsetRemote keeps remoteEnv out of the Podman a unit runs, should the user's
// manager hold it, so the units and the adapter's own commands drive the same
// Podman (localEnv).
var unsetRemote = "UnsetEnvironment=" + strings.Join(remoteEnv, " ") + "\n"

// definitionLabel carries a container's definition hash, as Compose's
// config-hash label does: Up re-creates a container whose label differs from
// its unit's, and DefinitionDrift reads it.
const definitionLabel = "dev.morzer.definition"

// variable finds the variables a unit names, in either of systemd's forms.
// `$$` is a literal dollar and is skipped by the caller.
var variable = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// variables lists the names text refers to, sorted and once each.
func variables(text []byte) []string {
	seen := map[string]bool{}
	clean := strings.ReplaceAll(string(text), "$$", "")
	for _, m := range variable.FindAllStringSubmatch(clean, -1) {
		name := m[1]
		if name == "" {
			name = m[2]
		}
		seen[name] = true
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// definition hashes what a unit's container is created from: its text and the
// value of every variable it names, so a changed parameter or image re-creates
// it as a changed file does.
func definition(u unitFile, env map[string]string) string {
	h := sha256.New()
	h.Write(u.text)
	for _, name := range variables(u.text) {
		fmt.Fprintf(h, "\x00%s=%s", name, env[name])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// environmentFile renders values in systemd's EnvironmentFile syntax: one
// double-quoted assignment a line, with a backslash, a quote and a dollar
// escaped. A value holding a line break cannot be written and is refused.
func environmentFile(env map[string]string) ([]byte, error) {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	escape := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`)
	for _, name := range names {
		value := env[name]
		if strings.ContainsAny(value, "\n\r") {
			return nil, domain.ValidationError(nil,
				"%s holds a line break, which a systemd environment file cannot carry", name)
		}
		b.WriteString(name + `="` + escape.Replace(value) + "\"\n")
	}
	return []byte(b.String()), nil
}

// dropInFor is the drop-in an installed file gets: the parameter copy for every
// unit, and for a container its definition hash.
func dropInFor(u unitFile, parameters, hash string) []byte {
	var b strings.Builder
	b.WriteString("# Written by morzer. Edits are replaced at the next apply.\n")
	if u.kind == kindContainer {
		b.WriteString("[Container]\nLabel=" + definitionLabel + "=" + hash + "\n\n")
	}
	// Without the `-`: a unit started before the manager rendered the file
	// fails rather than running on empty values (RFC 0023 row 21).
	b.WriteString("[Service]\nEnvironmentFile=" + parameters + "\n")
	b.WriteString(unsetRemote)
	if u.kind == kindContainer {
		// A stop sends SIGTERM, and Podman returns the container's 143 for
		// it. Counted as a failure, every Stop, the cold capture's
		// included, would leave the unit failed and the user's manager
		// degraded (measured on systemd 261 and Podman 6.1.1).
		b.WriteString("SuccessExitStatus=143\n")
	}
	return []byte(b.String())
}

// writeFile writes a file whole, through a temporary name, so a unit or a
// parameter file is never seen half written.
func writeFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// installOptions say what an install writes beyond the files themselves.
type installOptions struct {
	// parameters writes the parameter copy. Up always does; a one-shot
	// does only when there is none yet.
	parameters bool

	// containers writes the `.container` files. A one-shot before the
	// first Up writes them; one after leaves the running definitions alone.
	containers bool
}

// install writes the bundle's files into dir with their drop-ins.
func install(dir string, b bundle, env map[string]string, opts installOptions) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return domain.RuntimeError(err, "cannot create the unit directory %s", dir)
	}
	parameters := filepath.Join(dir, parametersFile)
	if opts.parameters {
		data, err := environmentFile(env)
		if err != nil {
			return err
		}
		if err := writeFile(parameters, data); err != nil {
			return domain.RuntimeError(err, "cannot write %s", parameters)
		}
	}
	for _, u := range b.units {
		if u.kind == kindContainer && !opts.containers {
			continue
		}
		if err := writeFile(filepath.Join(dir, u.name), u.text); err != nil {
			return domain.RuntimeError(err, "cannot install %s", u.name)
		}
		drop := filepath.Join(dir, u.name+".d")
		if err := os.MkdirAll(drop, 0o700); err != nil {
			return domain.RuntimeError(err, "cannot install %s", u.name)
		}
		if err := writeFile(filepath.Join(drop, dropIn), dropInFor(u, parameters, definition(u, env))); err != nil {
			return domain.RuntimeError(err, "cannot install %s", u.name)
		}
	}
	return nil
}

// installed reads back the files installed in dir, as a bundle. A directory
// that does not exist is an installation with nothing installed.
func installed(dir, product string) (bundle, error) {
	b := bundle{product: product}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return b, domain.RuntimeError(err, "cannot read the unit directory %s", dir)
	}
	for _, e := range entries {
		name := e.Name()
		kind := strings.TrimPrefix(filepath.Ext(name), ".")
		if e.IsDir() || strings.HasPrefix(name, ".") ||
			(kind != kindContainer && kind != kindVolume && kind != kindNetwork) {
			continue
		}
		text, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return b, domain.RuntimeError(err, "cannot read the installed unit %s", name)
		}
		entries, err := parseUnit(name, text)
		if err != nil {
			return b, domain.RuntimeError(err, "cannot read the installed unit %s", name)
		}
		service := strings.TrimSuffix(strings.TrimPrefix(name, product+"-"), "."+kind)
		b.units = append(b.units, unitFile{name: name, kind: kind, service: service, text: text, entries: entries})
	}
	return b, nil
}

// remove deletes an installed file and its drop-in.
func remove(dir string, u unitFile) error {
	if err := os.RemoveAll(filepath.Join(dir, u.name+".d")); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, u.name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func asExit(err error, target **exec.ExitError) bool { return errors.As(err, target) }

// firstLine is the first line of a tool's output, for an error message.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "no output"
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// wrapExit types a failed run as a runtime error, keeping an interruption or a
// preflight refusal as it was.
func wrapExit(err error, message, hint string) error {
	if err == nil {
		return nil
	}
	if de := domain.AsError(err); de.Code == domain.CodeInterrupted || de.Code == domain.CodePreflight {
		return err
	}
	e := domain.RuntimeError(err, "%s", message)
	if hint != "" {
		e = e.WithHint("%s", hint)
	}
	return e
}
