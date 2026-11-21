package compose

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/ports"
)

// inspectImageMounts prints, per container, its service, whether `compose run`
// made it, whether it runs, and the type of each of its mounts.
const inspectImageMounts = `{{index .Config.Labels "com.docker.compose.service"}}	` +
	`{{index .Config.Labels "com.docker.compose.oneoff"}}	{{.State.Running}}	` +
	`{{range .Mounts}}{{.Type}} {{end}}`

// recreateLostImageMounts re-creates, without starting them, the services
// that have a stopped container with an image mount and none that runs. It
// looks at the named services and what they depend on, or at every declared
// one when none is named.
//
// The daemon mounts an image when it creates the container, and records where.
// A reboot takes the mount away and `start` does not make it again, so the
// `up` that starts the container fails with "cannot access path"
// until it is re-created, and every reboot leaves such a service down. A
// service with a container that runs is left alone: the re-create would
// replace that one too. Re-created rather than removed: Compose carries a
// container's anonymous volumes over to the one that replaces it. Measured
// with Docker 29.7.2 and Compose 5.5.1.
//
// Best effort up to the re-create: a daemon that cannot list or inspect
// containers fails the `up` that follows, which says so.
func (r *Runtime) recreateLostImageMounts(ctx context.Context, cfg ports.RuntimeConfig, services []string) error {
	ids := strings.Fields(r.quiet(ctx, cfg, r.args(cfg, "ps", "--all", "--quiet")))
	if len(ids) == 0 {
		return nil
	}
	lost := lostImageMounts(r.quiet(ctx, cfg, append([]string{r.docker, "inspect", "--format", inspectImageMounts}, ids...)))
	if len(lost) == 0 {
		return nil
	}
	// `config` of named services is them and what they depend on, under the
	// profiles `up` enables for them. It also leaves out an orphan, whose
	// container `ps` lists and whose name `up` refuses.
	scope, err := serviceNames(r.quiet(ctx, cfg, append(r.args(cfg, "config", "--format", "json"), services...)))
	if err != nil {
		return nil
	}
	lost = slices.DeleteFunc(lost, func(s string) bool { return !slices.Contains(scope, s) })
	if len(lost) == 0 {
		return nil
	}

	argv := append(r.args(cfg, "up", "--no-start", "--force-recreate", "--no-deps"), lost...)
	if _, err := r.runner.Run(ctx, r.command(cfg, 10*time.Minute, argv...)); err != nil {
		return wrapExit(err, "cannot re-create "+strings.Join(lost, ", ")+", whose image mount a restart removed",
			"the daemon makes an image mount when it creates a container, and a reboot takes it away; "+
				"`docker rm` of the container lets the next run create it again")
	}
	return nil
}

// quiet runs a command whose output is read, not shown, and returns its
// standard output, or nothing when it failed.
func (r *Runtime) quiet(ctx context.Context, cfg ports.RuntimeConfig, argv []string) string {
	cmd := r.command(cfg, 60*time.Second, argv...)
	cmd.OnLine = nil
	res, err := r.runner.Run(ctx, cmd)
	if err != nil {
		return ""
	}
	return res.Stdout
}

// lostImageMounts reads inspectImageMounts' lines and returns, sorted, the
// services with a container that does not run and has an image mount, and no
// container that runs. A `compose run` container counts for neither: `up`
// never starts it.
func lostImageMounts(raw string) []string {
	var stopped, running []string
	for line := range strings.SplitSeq(raw, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 4 || fields[0] == "" || fields[1] == "True" {
			continue
		}
		switch {
		case fields[2] == "true":
			running = append(running, fields[0])
		case fields[2] == "false" && slices.Contains(strings.Fields(fields[3]), "image"):
			stopped = append(stopped, fields[0])
		}
	}
	stopped = slices.DeleteFunc(stopped, func(s string) bool { return slices.Contains(running, s) })
	slices.Sort(stopped)
	return slices.Compact(stopped)
}
