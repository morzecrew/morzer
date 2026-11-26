package quadlet

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// generators are where Podman installs the Quadlet generator: the user
// generator link systemd itself runs, then the binary it points at on the
// distributions that put it elsewhere.
var generators = []string{
	"/usr/lib/systemd/user-generators/podman-user-generator",
	"/lib/systemd/user-generators/podman-user-generator",
	"/usr/libexec/podman/quadlet",
	"/usr/lib/podman/quadlet",
}

// networkModes are the values of `Network=` that name no network, so no
// installation's name is at stake in them.
var networkModes = map[string]bool{
	"none": true, "host": true, "private": true, "slirp4netns": true, "pasta": true,
}

// Validate checks a bundle as this adapter will run it, then asks the
// Quadlet generator whether it would generate every unit. It writes nothing
// the units read: the generator runs over a copy in a temporary directory.
func (r *Runtime) Validate(ctx context.Context, cfg ports.RuntimeConfig) (ports.Rendered, error) {
	b, err := checkBundle(cfg)
	if err != nil {
		return ports.Rendered{}, err
	}
	if err := r.generate(ctx, b); err != nil {
		return ports.Rendered{}, err
	}
	return ports.Rendered{Config: b.canonical(), Services: b.started()}, nil
}

// checkBundle reads a bundle and refuses what this adapter cannot run as the
// vendor wrote it.
func checkBundle(cfg ports.RuntimeConfig) (bundle, error) {
	if len(cfg.Options) > 0 {
		keys := make([]string, 0, len(cfg.Options))
		for k := range cfg.Options {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return bundle{}, domain.ValidationError(nil,
			"runtimes.quadlet.options sets %s, and the Quadlet runtime takes no options",
			strings.Join(keys, ", "))
	}
	if len(cfg.Profiles) > 0 {
		return bundle{}, domain.ValidationError(nil,
			"the Quadlet runtime has no service profiles, and %s was asked for", strings.Join(cfg.Profiles, ", "))
	}
	b, err := loadBundle(cfg)
	if err != nil {
		return b, err
	}
	b.env = cfg.Env
	if len(b.containers()) == 0 {
		return b, domain.ValidationError(nil, "the Quadlet files declare no .container file, so nothing would run")
	}
	for _, u := range b.units {
		if err := b.checkUnit(u); err != nil {
			return b, err
		}
	}
	return b, nil
}

// checkUnit refuses a file this adapter cannot run as written.
func (b bundle) checkUnit(u unitFile) error {
	// Row 28: only the manager starts a unit, after it has rendered the
	// parameters. An [Install] section would have systemd start it at boot,
	// before anything is rendered. Refused rather than removed, because
	// removing it would change what the vendor wrote.
	if u.hasSection("Install") {
		return refusal(u.name, "has an [Install] section; the manager starts every unit itself, "+
			"after it has rendered what the unit reads, so a unit must not be enabled for a target")
	}
	if _, ok := u.last("Container", "Pod"); ok {
		return refusal(u.name, "names a pod, and a Quadlet bundle has no .pod files; "+
			"its containers are grouped by their names")
	}

	own := b.product + "-"
	for _, key := range []struct{ section, key string }{
		{"Container", "ContainerName"}, {"Volume", "VolumeName"}, {"Network", "NetworkName"},
	} {
		name, ok := u.last(key.section, key.key)
		if !ok {
			continue
		}
		if !strings.HasPrefix(name, own) {
			return refusal(u.name, "sets %s=%s, which does not start with %q, so it could name "+
				"another installation's", key.key, name, own)
		}
		// systemd would expand a specifier or a variable in it, and the
		// adapter would then address a container, volume or network that
		// is not there.
		if strings.ContainsAny(name, "%$") {
			return refusal(u.name, "sets %s=%s, which systemd expands; write the name out", key.key, name)
		}
	}
	for _, m := range u.values("Container", "Mount") {
		if !strings.Contains(","+m+",", ",type=tmpfs,") {
			return refusal(u.name, "mounts Mount=%s; declare storage with Volume=, which the manager "+
				"reads for backups and removes with the installation", m)
		}
	}

	for _, v := range u.values("Container", "Volume") {
		if err := b.checkVolume(u, v); err != nil {
			return err
		}
	}
	for _, n := range u.values("Container", "Network") {
		if err := b.checkNetwork(u, n); err != nil {
			return err
		}
	}
	for _, f := range u.values("Container", "EnvironmentFile") {
		// Quadlet joins anything that is not an absolute path or a
		// specifier onto the unit directory, `${...}` included, before
		// systemd would expand it (measured on Podman 6.1.1).
		if relative(f) || strings.HasPrefix(f, "$") {
			return refusal(u.name, "reads EnvironmentFile=%s, which Quadlet resolves against the directory "+
				"the units are installed in, not the release; write an absolute path", f)
		}
	}
	return nil
}

// checkVolume refuses a mount this adapter cannot keep.
//
// An anonymous volume is deleted with its data at every stop, because
// Quadlet's stop is `podman rm -v` (RFC 0023 row 29, measured), so a backup's
// cold capture and Stop's contract would both lose it. A relative host path
// would resolve against the unit directory rather than the release.
func (b bundle) checkVolume(u unitFile, value string) error {
	source, _, hasTarget := strings.Cut(value, ":")
	if !hasTarget {
		return refusal(u.name, "mounts an anonymous volume at %s, which a stop deletes with its data; "+
			"name it, with a .volume file of the bundle", value)
	}
	switch {
	case source == "":
		return refusal(u.name, "mounts Volume=%s, which names no source", value)
	case strings.HasPrefix(source, "$"):
		// systemd expands it when the unit starts. A value that is not a
		// path makes it a named volume the files do not name, which no
		// backup would capture and no removal would find.
		if !strings.HasPrefix(expand(source, b.env), "/") {
			return refusal(u.name, "mounts Volume=%s, whose source is not a path once its values are set; "+
				"write a named volume out, or a .volume file of the bundle", value)
		}
		return nil
	case strings.HasPrefix(source, "/"), strings.HasPrefix(source, "%"):
		return nil
	case strings.HasSuffix(source, ".volume"):
		if _, ok := b.file(source); !ok {
			return refusal(u.name, "mounts %s, which is not a file of the bundle", source)
		}
		return nil
	case strings.Contains(source, "/"):
		return refusal(u.name, "mounts the relative path %s; it would resolve against the directory "+
			"the units are installed in, so name it from the release with ${%s_RELEASE_DIR}",
			source, envPrefix(b.product))
	case !strings.HasPrefix(source, b.product+"-"):
		return refusal(u.name, "mounts the volume %s, which does not start with %q, so it could be "+
			"another installation's", source, b.product+"-")
	}
	return nil
}

// checkNetwork refuses a network that could be another installation's.
func (b bundle) checkNetwork(u unitFile, value string) error {
	name, _, _ := strings.Cut(value, ":")
	switch {
	case networkModes[name], strings.HasPrefix(value, "ns:"):
		return nil
	case strings.HasSuffix(name, ".network"):
		if _, ok := b.file(name); !ok {
			return refusal(u.name, "joins %s, which is not a file of the bundle", name)
		}
		return nil
	case !strings.HasPrefix(name, b.product+"-"):
		return refusal(u.name, "joins the network %s, which does not start with %q, so it could be "+
			"another installation's", name, b.product+"-")
	}
	return nil
}

func relative(path string) bool {
	return path != "" && !strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "$") &&
		!strings.HasPrefix(path, "%") && !strings.HasPrefix(path, "-/")
}

// envPrefix is a product's variable prefix, as the manager spells it.
func envPrefix(product string) string {
	return strings.ToUpper(strings.ReplaceAll(product, "-", "_"))
}

// canonical is the bundle as the plan view diffs it: each file under its name.
func (b bundle) canonical() []byte {
	var out bytes.Buffer
	for _, u := range b.units {
		out.WriteString("# " + u.name + "\n")
		out.Write(u.text)
		if !bytes.HasSuffix(u.text, []byte("\n")) {
			out.WriteString("\n")
		}
	}
	return out.Bytes()
}

// generate runs the Quadlet generator in dry run over a copy of the bundle,
// and refuses what it refuses.
func (r *Runtime) generate(ctx context.Context, b bundle) error {
	generator, err := r.findGenerator()
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "morzer-quadlet-")
	if err != nil {
		return domain.RuntimeError(err, "cannot stage the Quadlet files")
	}
	defer func() { _ = os.RemoveAll(dir) }()
	for _, u := range b.units {
		if err := os.WriteFile(filepath.Join(dir, u.name), u.text, 0o600); err != nil {
			return domain.RuntimeError(err, "cannot stage %s", u.name)
		}
	}

	cmd := r.command(time.Minute, generator, "-dryrun", "-user")
	cmd.OnLine = nil
	cmd.Env = exec.BaseEnv(map[string]string{"QUADLET_UNIT_DIRS": dir})
	res, err := r.runner.Run(ctx, cmd)
	if err != nil {
		var exitErr *exec.ExitError
		if asExit(err, &exitErr) {
			return domain.ValidationError(err, "the Quadlet generator refused the bundle: %s",
				firstLine(res.Stderr+exitErr.Stderr))
		}
		return domain.RuntimeError(err, "cannot run the Quadlet generator")
	}
	// The generator skips a file it cannot convert and says so, and still
	// exits 0 for the others. A unit that would not exist is a refusal.
	for _, u := range b.units {
		if !bytes.Contains([]byte(res.Stdout), []byte("---"+u.unitName()+"---")) {
			return domain.ValidationError(nil, "the Quadlet generator would not generate %s: %s",
				u.name, firstLine(res.Stderr))
		}
	}
	return nil
}

func (r *Runtime) findGenerator() (string, error) {
	if r.generator != "" {
		return r.generator, nil
	}
	for _, path := range generators {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", domain.RuntimeError(domain.ErrToolMissing, "the Quadlet generator is not installed").
		WithHint("install Podman 4.4 or later, which ships it as systemd's podman-user-generator")
}
