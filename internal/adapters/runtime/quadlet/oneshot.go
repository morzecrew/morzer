package quadlet

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// podmanFailed is the exit status Podman uses for its own failure, such as an
// image it cannot find: the command never ran, so it is not the command's.
const podmanFailed = 125

// systemdWord quotes s as one word of a unit's command line. Quadlet hands
// `Exec=`, `Environment=` and `Volume=` to `ExecStart=`, where systemd expands
// `$` and `%` before Podman sees them, so both are doubled.
func systemdWord(s string) string {
	escape := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, `$`, `$$`, `%`, `%%`)
	return `"` + escape.Replace(s) + `"`
}

// RunOneShot runs a service to completion: a migration, a task, a facts
// collector. It runs a transient copy of the service's unit, so the container
// is what the vendor declared, with the dependencies it requires started by
// systemd, as `compose run` starts them.
//
// The copy reads the values cfg carries now, from a file of its own, and its
// drop-in sets the command, the extra environment and the mounts. The
// container logs to a file of the run's own in Podman's k8s-file format, which
// names each line's stream and keeps the order lines arrived in: facts are read
// from stdout alone, and a failure is quoted from what came last. Measured on
// Podman 6.1.1: under a Quadlet unit, a passthrough container's output reached
// neither the unit's stdout nor the journal, where `podman run` under
// `systemd-run` wrote it to both.
//
// Before the first Up nothing is installed, so the bundle is installed first,
// parameters included. After it, the running services' definitions and values
// are left as Up left them, and only the bundle's volumes and networks are
// written, so a file the copy names exists.
//
// Quadlet always runs a container with `--rm`, so Remove is always true here.
func (r *Runtime) RunOneShot(ctx context.Context, cfg ports.RuntimeConfig, service string, opts ports.RunOptions) (ports.ExitResult, error) {
	started := time.Now()
	b, err := checkBundle(cfg)
	if err != nil {
		return ports.ExitResult{}, err
	}
	u, ok := b.service(service)
	if !ok {
		return ports.ExitResult{}, domain.ValidationError(nil, "no Quadlet file defines the service %q", service)
	}
	dir, err := r.unitDir(cfg.Product)
	if err != nil {
		return ports.ExitResult{}, err
	}
	_, statErr := os.Stat(filepath.Join(dir, parametersFile))
	first := errors.Is(statErr, os.ErrNotExist)
	if err := install(dir, b, cfg.Env, installOptions{parameters: first, containers: first}); err != nil {
		return ports.ExitResult{}, err
	}

	base := strings.TrimSuffix(u.name, ".container") + "-run-" + r.runID()
	unit := base + ".service"
	paths := runPaths{
		unit:   filepath.Join(dir, base+".container"),
		dropIn: filepath.Join(dir, base+".container.d"),
		env:    filepath.Join(dir, base+".env"),
		log:    filepath.Join(dir, base+".log"),
	}
	defer r.cleanUp(ctx, unit, paths)

	if err := paths.write(u, cfg.Env, opts); err != nil {
		return ports.ExitResult{}, err
	}
	if err := r.reload(ctx); err != nil {
		return ports.ExitResult{}, err
	}

	timeout := unitTimeout
	if opts.Timeout > 0 {
		timeout = opts.Timeout + time.Minute
	}
	_, runErr := r.systemctlUser(ctx, timeout, "start", unit)
	if ctx.Err() != nil {
		return ports.ExitResult{}, domain.Interrupted("the one-shot %s was cancelled", service)
	}

	out := ports.ExitResult{Duration: time.Since(started)}
	out.Stdout, out.Stderr, out.Output, out.Truncated = readRunLog(paths.log)

	states, err := r.unitStates(ctx, []string{unit})
	if err != nil {
		return out, err
	}
	st := states[0]
	switch st.result {
	case "success":
		return out, nil
	case "exit-code":
		if st.exitStatus == podmanFailed {
			return out, domain.RuntimeError(runErr, "cannot run %s: %s", service, firstLine(out.Stderr)).
				WithHint("run `journalctl --user -u %s` for what Podman said", unit)
		}
		out.ExitCode = st.exitStatus
		return out, nil
	case "signal", "core-dump":
		out.ExitCode = 128 + st.exitStatus
		return out, nil
	default:
		// Not a process that ran and exited: `resources` is a unit that
		// could not start, whose ExecMainStatus is a 0 nothing returned
		// (RFC 0023 §12 item 4).
		return out, domain.RuntimeError(runErr, "cannot run %s: the unit ended with %q", service, st.result).
			WithHint("run `journalctl --user -u %s` to see why", unit)
	}
}

// runPaths are the files of one transient one-shot.
type runPaths struct {
	unit, dropIn, env, log string
}

// write lays down the copy of the unit, its drop-in, and its values.
func (p runPaths) write(u unitFile, env map[string]string, opts ports.RunOptions) error {
	values, err := environmentFile(env)
	if err != nil {
		return err
	}
	if err := writeFile(p.env, values); err != nil {
		return domain.RuntimeError(err, "cannot write %s", p.env)
	}
	if err := writeFile(p.unit, u.text); err != nil {
		return domain.RuntimeError(err, "cannot write %s", p.unit)
	}

	var c strings.Builder
	c.WriteString("[Container]\n")
	c.WriteString("LogDriver=k8s-file\n")
	c.WriteString("PodmanArgs=--log-opt " + systemdWord("path="+p.log) + "\n")
	if len(opts.Argv) > 0 {
		words := make([]string, len(opts.Argv))
		for i, a := range opts.Argv {
			words[i] = systemdWord(a)
		}
		c.WriteString("Exec=" + strings.Join(words, " ") + "\n")
	}
	names := make([]string, 0, len(opts.Env))
	for name := range opts.Env {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		c.WriteString("Environment=" + systemdWord(name+"="+opts.Env[name]) + "\n")
	}
	for _, m := range opts.Mounts {
		mount := m.Source + ":" + m.Target
		if m.ReadOnly {
			mount += ":ro"
		}
		c.WriteString("Volume=" + strings.NewReplacer("$", "$$", "%", "%%").Replace(mount) + "\n")
	}
	c.WriteString("\n[Service]\nType=oneshot\nRestart=no\nRemainAfterExit=no\n")
	c.WriteString("EnvironmentFile=" + p.env + "\n")
	timeout := "infinity"
	if opts.Timeout > 0 {
		timeout = strconv.Itoa(int(opts.Timeout.Seconds())) + "s"
	}
	c.WriteString("TimeoutStartSec=" + timeout + "\n")

	if err := os.MkdirAll(p.dropIn, 0o700); err != nil {
		return domain.RuntimeError(err, "cannot write %s", p.dropIn)
	}
	return writeFile(filepath.Join(p.dropIn, dropIn), []byte(c.String()))
}

// cleanUp stops the transient unit if it still runs and removes its files,
// with the run's cancellation taken away: a cancelled run must not leave its
// container running or its unit loaded.
func (r *Runtime) cleanUp(ctx context.Context, unit string, p runPaths) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	_, _ = r.systemctlUser(ctx, time.Minute, "stop", unit)
	for _, path := range []string{p.unit, p.env, p.log} {
		_ = os.Remove(path)
	}
	_ = os.RemoveAll(p.dropIn)
	_, _ = r.systemctlUser(ctx, time.Minute, "daemon-reload")
	_, _ = r.systemctlUser(ctx, time.Minute, "reset-failed", unit)
}

// readRunLog reads a one-shot's k8s-file log: `<instant> <stream> <F|P>
// <text>` a line, where P is a piece of a line that continues in the next
// entry of the same stream. It keeps the head of each stream, as the runner
// keeps the head of a pipe, and the last lines of both in the order they
// arrived.
func readRunLog(path string) (stdout, stderr, output string, truncated bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", "", false
	}
	defer func() { _ = f.Close() }()

	var out, errs strings.Builder
	var tail []string
	pending := map[string]*strings.Builder{"stdout": {}, "stderr": {}}
	keep := func(b *strings.Builder, s string) {
		if room := exec.DefaultMaxCapture - b.Len(); room < len(s) {
			truncated = true
			if room > 0 {
				b.WriteString(s[:room])
			}
			return
		}
		b.WriteString(s)
	}
	rd := bufio.NewReader(f)
	for {
		line, err := rd.ReadString('\n')
		line = strings.TrimSuffix(line, "\n")
		if line != "" {
			fields := strings.SplitN(line, " ", 4)
			if len(fields) >= 3 {
				stream, tag, text := fields[1], fields[2], ""
				if len(fields) == 4 {
					text = fields[3]
				}
				target := &out
				if stream == "stderr" {
					target = &errs
				}
				if p := pending[stream]; p != nil {
					p.WriteString(text)
					if tag != "P" {
						keep(target, p.String()+"\n")
						if strings.TrimSpace(p.String()) != "" {
							tail = append(tail, p.String())
							if len(tail) > outputTailLines {
								tail = tail[1:]
							}
						}
						p.Reset()
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	// A last piece with no newline after it is what the stream ended with.
	for stream, target := range map[string]*strings.Builder{"stdout": &out, "stderr": &errs} {
		if rest := pending[stream].String(); rest != "" {
			keep(target, rest)
			tail = append(tail, rest)
		}
	}
	if len(tail) > outputTailLines {
		tail = tail[len(tail)-outputTailLines:]
	}
	return strings.TrimSuffix(out.String(), "\n"), strings.TrimSuffix(errs.String(), "\n"),
		strings.Join(tail, "\n"), truncated
}
