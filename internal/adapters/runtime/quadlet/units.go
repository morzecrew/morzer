package quadlet

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// The kinds of file a bundle may carry. A `.pod` or `.kube` file is refused:
// a pod groups containers the way a Compose project does, which this adapter
// does with a name prefix, and a `.kube` file is Kubernetes YAML (RFC 0023
// row 1). `.image` and `.build` would move image handling from the manifest's
// pinned digests into a unit.
const (
	kindContainer = "container"
	kindVolume    = "volume"
	kindNetwork   = "network"
)

// unitFile is one Quadlet file of a bundle, read as systemd reads a unit:
// sections of key=value lines, a key that may repeat, `#` and `;` comments, and
// a line ending in a backslash continued on the next.
type unitFile struct {
	// name is the file's base name, `demo-web.container`.
	name string

	// kind is the extension without its dot.
	kind string

	// service is the name without the product's prefix and the kind:
	// `web`. The manifest names services this way under every runtime
	// (logs/quadlet-adapter.md, Q1).
	service string

	text    []byte
	entries []entry
}

type entry struct {
	section, key, value string
	line                int
}

// values returns every value of key in section, in file order.
func (u unitFile) values(section, key string) []string {
	var out []string
	for _, e := range u.entries {
		if e.section == section && e.key == key {
			out = append(out, e.value)
		}
	}
	return out
}

// last returns the value that wins for a key set once per unit: the last.
func (u unitFile) last(section, key string) (string, bool) {
	vs := u.values(section, key)
	if len(vs) == 0 {
		return "", false
	}
	return vs[len(vs)-1], true
}

// hasSection reports a `[section]` line, even when no key follows it.
func (u unitFile) hasSection(section string) bool {
	sc := bufio.NewScanner(bytes.NewReader(u.text))
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "["+section+"]" {
			return true
		}
	}
	return false
}

// oneshot reports a container that runs to completion, which Up never starts
// (logs/quadlet-adapter.md, Q2).
func (u unitFile) oneshot() bool {
	t, _ := u.last("Service", "Type")
	return u.kind == kindContainer && strings.TrimSpace(t) == "oneshot"
}

// unitName is the systemd unit Quadlet generates for the file.
func (u unitFile) unitName() string {
	base := strings.TrimSuffix(u.name, "."+u.kind)
	if u.kind == kindContainer {
		return base + ".service"
	}
	return base + "-" + u.kind + ".service"
}

// parseUnit reads a unit file's sections and keys.
func parseUnit(name string, text []byte) ([]entry, error) {
	var (
		out     []entry
		section string
		pending strings.Builder
		start   int
	)
	lines := strings.Split(string(text), "\n")
	for i, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		if pending.Len() == 0 {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || trimmed[0] == '#' || trimmed[0] == ';' {
				continue
			}
			if strings.HasPrefix(trimmed, "[") {
				if !strings.HasSuffix(trimmed, "]") {
					return nil, fmt.Errorf("%s:%d: %q is not a section header", name, i+1, trimmed)
				}
				section = trimmed[1 : len(trimmed)-1]
				continue
			}
			start = i + 1
		}
		if strings.HasSuffix(line, `\`) {
			pending.WriteString(strings.TrimSuffix(line, `\`))
			pending.WriteString(" ")
			continue
		}
		pending.WriteString(line)
		full := strings.TrimSpace(pending.String())
		pending.Reset()

		key, value, ok := strings.Cut(full, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: %q is not a key=value line", name, start, full)
		}
		if section == "" {
			return nil, fmt.Errorf("%s:%d: %q comes before any section", name, start, full)
		}
		out = append(out, entry{section: section, key: strings.TrimSpace(key), value: strings.TrimSpace(value), line: start})
	}
	if pending.Len() > 0 {
		return nil, fmt.Errorf("%s: the last line continues past the end of the file", name)
	}
	return out, nil
}

// serviceName matches what follows the product's prefix: the same characters
// a Compose service may use, so a bundle declaring both runtimes can name its
// services alike.
var serviceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// bundle is a release's Quadlet files, as the manifest lists them.
type bundle struct {
	product string
	units   []unitFile
}

// loadBundle reads the files cfg names and checks their names. Every name is
// `<product>-<service>.<kind>`, so two installations' units, containers,
// volumes and networks cannot meet in the user's manager or the user's Podman
// (logs/quadlet-adapter.md, Q1).
func loadBundle(cfg ports.RuntimeConfig) (bundle, error) {
	b := bundle{product: cfg.Product}
	if cfg.Product == "" {
		return b, domain.Internal(nil, "a Quadlet configuration names no product")
	}
	seen := map[string]bool{}
	for _, path := range cfg.Files {
		name := filepath.Base(path)
		kind := strings.TrimPrefix(filepath.Ext(name), ".")
		switch kind {
		case kindContainer, kindVolume, kindNetwork:
		default:
			return b, refusal(name, "is a .%s file, and a Quadlet bundle may carry only "+
				".container, .volume and .network files", kind)
		}
		service := strings.TrimSuffix(strings.TrimPrefix(name, cfg.Product+"-"), "."+kind)
		if !strings.HasPrefix(name, cfg.Product+"-") || !serviceName.MatchString(service) {
			return b, refusal(name, "does not start with %q followed by a name: every Quadlet "+
				"file is named <product>-<name>.<kind>, so its unit, container, volume or network "+
				"cannot be another installation's", cfg.Product+"-")
		}
		if seen[name] {
			return b, refusal(name, "is listed twice")
		}
		seen[name] = true

		text, err := os.ReadFile(path)
		if err != nil {
			return b, domain.ValidationError(err, "cannot read Quadlet file %s", name)
		}
		entries, err := parseUnit(name, text)
		if err != nil {
			return b, domain.ValidationError(err, "cannot read Quadlet file %s", name)
		}
		b.units = append(b.units, unitFile{name: name, kind: kind, service: service, text: text, entries: entries})
	}
	return b, nil
}

func refusal(name, format string, args ...any) error {
	return domain.ValidationError(nil, "Quadlet file %s %s", name, fmt.Sprintf(format, args...))
}

// containers are the bundle's `.container` files.
func (b bundle) containers() []unitFile {
	var out []unitFile
	for _, u := range b.units {
		if u.kind == kindContainer {
			out = append(out, u)
		}
	}
	return out
}

// service finds a service's `.container` file.
func (b bundle) service(name string) (unitFile, bool) {
	for _, u := range b.containers() {
		if u.service == name {
			return u, true
		}
	}
	return unitFile{}, false
}

// started are the services Up starts: every container that is not a one-shot,
// sorted.
func (b bundle) started() []string {
	var out []string
	for _, u := range b.containers() {
		if !u.oneshot() {
			out = append(out, u.service)
		}
	}
	sort.Strings(out)
	return out
}

// file finds a bundle file by name.
func (b bundle) file(name string) (unitFile, bool) {
	for _, u := range b.units {
		if u.name == name {
			return u, true
		}
	}
	return unitFile{}, false
}

// containerName is the container a service's unit runs: Quadlet's
// `systemd-%N`, or the name the file sets.
func (b bundle) containerName(u unitFile) string {
	if name, ok := u.last("Container", "ContainerName"); ok && name != "" {
		return name
	}
	return "systemd-" + strings.TrimSuffix(u.name, ".container")
}
