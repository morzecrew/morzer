package quadlet

import (
	"context"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// volumeName is the Podman volume a `.volume` file creates: Quadlet's
// `systemd-%N`, or the name it sets.
func volumeName(u unitFile) string {
	if name, ok := u.last("Volume", "VolumeName"); ok && name != "" {
		return name
	}
	return "systemd-" + strings.TrimSuffix(u.name, ".volume")
}

// networkName is the Podman network a `.network` file creates.
func networkName(u unitFile) string {
	if name, ok := u.last("Network", "NetworkName"); ok && name != "" {
		return name
	}
	return "systemd-" + strings.TrimSuffix(u.name, ".network")
}

// networks are the Podman networks the bundle's `.network` files create.
func (b bundle) networks() []string {
	var out []string
	for _, u := range b.units {
		if u.kind == kindNetwork {
			out = append(out, networkName(u))
		}
	}
	return out
}

// expand replaces the variables a value names with their values, as systemd
// does when it starts the unit.
func expand(value string, env map[string]string) string {
	value = strings.ReplaceAll(value, "$$", "\x00")
	value = variable.ReplaceAllStringFunc(value, func(m string) string {
		name := strings.Trim(m, "${}")
		return env[name]
	})
	return strings.ReplaceAll(value, "\x00", "$")
}

// Volumes reports what the bundle mounts, read from its files.
func (r *Runtime) Volumes(_ context.Context, cfg ports.RuntimeConfig) (ports.ProjectStorage, error) {
	b, err := checkBundle(cfg)
	if err != nil {
		return ports.ProjectStorage{}, err
	}
	return b.storage(cfg.Env), nil
}

func (b bundle) storage(env map[string]string) ports.ProjectStorage {
	named := map[string]*ports.NamedVolume{}
	binds := map[string]*ports.BindMount{}
	for _, u := range b.units {
		if u.kind == kindVolume {
			named[u.name] = &ports.NamedVolume{Name: u.service, Actual: volumeName(u)}
		}
	}
	for _, c := range b.containers() {
		for _, v := range c.values("Container", "Volume") {
			source, _, _ := strings.Cut(v, ":")
			switch {
			case strings.HasSuffix(source, ".volume"):
				if nv := named[source]; nv != nil {
					nv.Services = appendOnce(nv.Services, c.service)
				}
			case strings.HasPrefix(source, "/"), strings.HasPrefix(source, "$"), strings.HasPrefix(source, "%"):
				path := expand(source, env)
				if binds[path] == nil {
					binds[path] = &ports.BindMount{Source: path}
				}
				binds[path].Services = appendOnce(binds[path].Services, c.service)
			default:
				if named[source] == nil {
					named[source] = &ports.NamedVolume{Name: source, Actual: source}
				}
				named[source].Services = appendOnce(named[source].Services, c.service)
			}
		}
	}

	var out ports.ProjectStorage
	for _, v := range named {
		sort.Strings(v.Services)
		out.Volumes = append(out.Volumes, *v)
	}
	sort.Slice(out.Volumes, func(i, j int) bool { return out.Volumes[i].Name < out.Volumes[j].Name })
	for _, m := range binds {
		sort.Strings(m.Services)
		out.Binds = append(out.Binds, *m)
	}
	sort.Slice(out.Binds, func(i, j int) bool { return out.Binds[i].Source < out.Binds[j].Source })
	return out
}

func appendOnce(list []string, s string) []string {
	for _, have := range list {
		if have == s {
			return list
		}
	}
	return append(list, s)
}

// removeVolumes removes the volumes the installation declares. One already
// gone is not an error: the data is gone either way.
//
// Each `.volume` file is asked for its own name, so an installed file and the
// declared one of the same name that set different names both go.
func (r *Runtime) removeVolumes(ctx context.Context, b bundle) error {
	var actual []string
	for _, u := range b.units {
		if u.kind == kindVolume {
			actual = append(actual, volumeName(u))
		}
	}
	for _, v := range b.storage(nil).Volumes {
		actual = append(actual, v.Actual)
	}
	seen := map[string]bool{}
	for _, name := range actual {
		if seen[name] {
			continue
		}
		seen[name] = true
		res, err := r.podmanRun(ctx, 5*time.Minute, "volume", "rm", name)
		if err == nil {
			continue
		}
		var exitErr *exec.ExitError
		if asExit(err, &exitErr) && strings.Contains(res.Stderr+exitErr.Stderr, "no such volume") {
			continue
		}
		return wrapExit(err, "cannot remove volume "+name, "")
	}
	return nil
}

// removeNetwork removes a network the installation created. Best effort: a
// network left behind holds no data, and one in use by a container outside
// the installation is not this installation's to break.
func (r *Runtime) removeNetwork(ctx context.Context, name string) {
	_, _ = r.podmanRun(ctx, time.Minute, "network", "rm", name)
}

func removeAll(dir string) error { return os.RemoveAll(dir) }

// CaptureVolume writes a volume's contents to destPath as an uncompressed tar,
// through the helper container Compose uses, run with Podman. It is the only
// way to read a rootless volume whatever uid wrote into it (RFC 0023 §12
// item 5).
func (r *Runtime) CaptureVolume(ctx context.Context, cfg ports.RuntimeConfig, volume, destPath string) error {
	return r.volumes().CaptureVolume(ctx, cfg, volume, destPath)
}

// RestoreVolume replaces a volume's contents with a tar.
func (r *Runtime) RestoreVolume(ctx context.Context, cfg ports.RuntimeConfig, volume, srcPath string) error {
	return r.volumes().RestoreVolume(ctx, cfg, volume, srcPath)
}

// VolumeSize reports an upper bound on the bytes a capture writes.
func (r *Runtime) VolumeSize(ctx context.Context, cfg ports.RuntimeConfig, volume string) (int64, error) {
	return r.volumes().VolumeSize(ctx, cfg, volume)
}

// HelperImage reports the image volumes are read through.
func (r *Runtime) HelperImage() string { return r.volumes().HelperImage() }

// HelperImagePinned reports whether the helper image is pinned by digest,
// which every volume operation requires. `doctor` asks it.
func (r *Runtime) HelperImagePinned() bool { return r.volumes().HelperImagePinned() }

// LocalHelperImage resolves the helper image as the volume operations do.
func (r *Runtime) LocalHelperImage(ctx context.Context) (image string, local bool, err error) {
	return r.volumes().LocalHelperImage(ctx)
}

// HasImage reports whether an image is in the user's Podman store. `podman
// image exists` answers with its exit status alone: 0 present, 1 absent.
func (r *Runtime) HasImage(ctx context.Context, ref string) (bool, error) {
	cmd := r.command(30*time.Second, r.podman, "image", "exists", ref)
	cmd.OnLine = nil
	res, err := r.runner.Run(ctx, cmd)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if asExit(err, &exitErr) && exitErr.ExitCode == 1 {
		return false, nil
	}
	return false, domain.RuntimeError(err, "cannot inspect %s: %s", ref, firstLine(res.Stderr))
}
