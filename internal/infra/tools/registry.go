// Package tools resolves external binaries and their versions once per
// operation.
//
// Every check is cached for the operation's duration: `doctor` asks about
// docker's version, preflight asks again, and an adapter asks a third time.
// Running `docker version` three times is wasteful and, worse, can produce
// three different answers if the daemon restarts mid-operation.
package tools

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// Known tool names, matching the keys used in requirements.tools.
const (
	Docker  = "docker"
	Compose = "compose"
	SOPS    = "sops"
	Age     = "age"
	Restic  = "restic"
	Systemd = "systemctl"
)

// probe describes how to discover one tool's version.
type probe struct {
	// binary is the executable to resolve in PATH.
	binary string
	// argv is appended to the binary to ask for a version.
	argv []string
	// parse extracts a version from the output.
	parse func(string) (domain.Version, string, error)
}

// probes is the tool catalogue. Adding a tool here is all it takes for
// preflight and doctor to check it.
var probes = map[string]probe{
	Docker:  {binary: "docker", argv: []string{"version", "--format", "{{json .}}"}, parse: parseDockerVersion},
	Compose: {binary: "docker", argv: []string{"compose", "version", "--short"}, parse: parseSemverish},
	SOPS:    {binary: "sops", argv: []string{"--version", "--disable-version-check"}, parse: parseSemverish},
	Age:     {binary: "age", argv: []string{"--version"}, parse: parseSemverish},
	Restic:  {binary: "restic", argv: []string{"version"}, parse: parseSemverish},
	Systemd: {binary: "systemctl", argv: []string{"--version"}, parse: parseSemverish},
}

// Registry resolves tools and caches the results.
type Registry struct {
	runner exec.Runner

	mu      sync.Mutex
	results map[string]result
}

type result struct {
	info ports.ToolInfo
	err  error
}

func NewRegistry(runner exec.Runner) *Registry {
	return &Registry{runner: runner, results: make(map[string]result)}
}

// probeTimeout bounds a version query. A tool that cannot answer in this long
// is hung -- `docker version` against a dead daemon is the usual case, and
// blocking an operation on it helps nobody.
const probeTimeout = 15 * time.Second

// Lookup resolves a tool and its version, caching both outcomes.
//
// Failures are cached too: if docker is missing, asking five more times will
// not find it, and each attempt costs a PATH walk.
func (r *Registry) Lookup(ctx context.Context, name string) (ports.ToolInfo, error) {
	r.mu.Lock()
	if cached, ok := r.results[name]; ok {
		r.mu.Unlock()
		return cached.info, cached.err
	}
	r.mu.Unlock()

	info, err := r.probe(ctx, name)

	r.mu.Lock()
	r.results[name] = result{info: info, err: err}
	r.mu.Unlock()

	return info, err
}

func (r *Registry) probe(ctx context.Context, name string) (ports.ToolInfo, error) {
	p, ok := probes[name]
	if !ok {
		return ports.ToolInfo{}, domain.Internal(nil, "no version probe is defined for tool %q", name)
	}

	path, err := r.runner.Look(p.binary)
	if err != nil {
		return ports.ToolInfo{Name: name}, err
	}

	res, runErr := r.runner.Run(ctx, exec.Command{
		Argv:          append([]string{path}, p.argv...),
		Timeout:       probeTimeout,
		CaptureOutput: true,
	})
	if runErr != nil {
		return ports.ToolInfo{Name: name, Path: path},
			domain.Preflight(runErr, "%s is installed but did not report a version", name).
				WithHint("run `%s %s` by hand to see what it says",
					p.binary, strings.Join(p.argv, " "))
	}

	// Some tools print their version to stderr. Preferring stdout and
	// falling back keeps the probe table free of a per-tool stream flag.
	out := res.Stdout
	if strings.TrimSpace(out) == "" {
		out = res.Stderr
	}

	version, raw, parseErr := p.parse(out)
	if parseErr != nil {
		return ports.ToolInfo{Name: name, Path: path, Raw: strings.TrimSpace(out)},
			domain.Preflight(parseErr, "cannot understand the version %s reported", name).
				WithHint("output was: %q", truncate(strings.TrimSpace(out), 120))
	}

	return ports.ToolInfo{Name: name, Path: path, Version: version, Raw: raw}, nil
}

// Require resolves a tool and checks it against a constraint. It is the single
// place "missing" and "too old" become distinguishable errors, so preflight
// can tell an operator which of the two they are facing.
func (r *Registry) Require(ctx context.Context, name string, constraint domain.Constraint) (ports.ToolInfo, error) {
	info, err := r.Lookup(ctx, name)
	if err != nil {
		return info, err
	}
	if constraint.IsZero() {
		return info, nil
	}
	if !constraint.Allows(info.Version) {
		return info, domain.Preflight(domain.ErrToolIncompatible,
			"%s %s does not satisfy the release requirement %q", name, info.Version, constraint).
			WithHint("upgrade %s to a version matching %q", name, constraint)
	}
	return info, nil
}

// dockerVersionDoc is the subset of `docker version --format {{json .}}` the
// registry reads.
type dockerVersionDoc struct {
	Client struct {
		Version string `json:"Version"`
	} `json:"Client"`
	Server *struct {
		Version string `json:"Version"`
	} `json:"Server"`
}

// parseDockerVersion prefers the server version.
//
// The client can be newer or older than the daemon, and it is the daemon that
// actually has to support what the release asks for. A missing Server block
// means the client could not reach the daemon, which is a distinct failure
// worth its own message.
func parseDockerVersion(out string) (domain.Version, string, error) {
	var doc dockerVersionDoc
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
		return parseSemverish(out)
	}
	if doc.Server == nil || doc.Server.Version == "" {
		return domain.Version{}, doc.Client.Version, domain.Preflight(nil,
			"the Docker client is installed but cannot reach the daemon")
	}
	v, err := parseVersionLoose(doc.Server.Version)
	return v, doc.Server.Version, err
}

// versionPattern finds the first version-looking token in arbitrary output.
var versionPattern = regexp.MustCompile(`v?(\d+)\.(\d+)(?:\.(\d+))?`)

// parseSemverish pulls a version out of whatever a tool prints.
//
// Tools are inconsistent: `sops 3.13.2`, `restic 0.16.4 compiled with go1.21`,
// `systemd 255 (255.4-1)`. Matching the first version-shaped token handles all
// of them without a parser per tool.
func parseSemverish(out string) (domain.Version, string, error) {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return domain.Version{}, "", domain.ValidationError(nil, "empty version output")
	}
	// Only the first line: several tools follow the version with a banner.
	firstLine := trimmed
	if i := strings.IndexByte(firstLine, '\n'); i >= 0 {
		firstLine = firstLine[:i]
	}

	m := versionPattern.FindStringSubmatch(firstLine)
	if m == nil {
		return domain.Version{}, firstLine, domain.ValidationError(nil,
			"no version found in %q", truncate(firstLine, 80))
	}

	patch := m[3]
	if patch == "" {
		patch = "0"
	}
	v, err := domain.ParseVersion(m[1] + "." + m[2] + "." + patch)
	return v, firstLine, err
}

// parseVersionLoose accepts a version with missing components, which Docker
// and Compose both emit ("28.0" rather than "28.0.0").
func parseVersionLoose(s string) (domain.Version, error) {
	v, _, err := parseSemverish(s)
	return v, err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
