package compose

import (
	"context"
	"crypto/rand"
	"errors"
	"math"
	mrand "math/rand/v2"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
)

// relayLabel names a relay's installation, so the next ingest of it can
// remove one a killed manager left (RFC 0060 decision 10).
const relayLabel = "dev.morzer.ingest-relay"

const (
	// The dynamic range, where nothing is registered (decision 20).
	relayPortMin, relayPortMax = 49152, 65535

	// relayAttempts is how many ports an ingest tries before it gives up.
	relayAttempts = 5

	// relayCheckLimit bounds decision 16's check, whose tries share it.
	relayCheckLimit = time.Minute

	// relayCheckTries is how often the check asks a running relay: one that
	// has started may not be listening yet (logs/mac-bundle-relay.md).
	relayCheckTries = 3
)

// relayNeedsHelper is what an ingest's refusal of an absent helper says it is
// for.
const relayNeedsHelper = "on a Mac a bundle's images reach the engine through a container of it, " +
	"so they cannot be loaded without it"

// errPortHeld is a relay that exited at start because its port was taken.
var errPortHeld = errors.New("the relay's port is held")

// WithRelay has an ingest reach the manager's registry through a relay
// container on the engine's host network, for an engine in a VM whose loopback
// is not this machine's (RFC 0060). installation names the installation its
// relays are labelled with: one the deployment lock covers, so another relay
// with the same label is a leftover.
func WithRelay(installation string) Option {
	return func(r *Runtime) {
		r.relayFor = installation
		r.relayWait = time.Second
	}
}

// relay is a running relay: its container, and the port it listens on, on the
// engine's loopback.
type relay struct {
	name string
	port int
}

func (rl relay) addr() string { return "127.0.0.1:" + strconv.Itoa(rl.port) }

// startRelay starts the relay to server, the registry's address on this
// machine's loopback, and checks it end to end with a request for the first of
// pending, which only this ingest's registry serves (decisions 2, 3, 13, 16 and
// 20). A port another container holds is replaced; anything else is refused.
func (r *Runtime) startRelay(ctx context.Context, helper, server string, pending []string) (relay, error) {
	_, serverPort, err := net.SplitHostPort(server)
	if err != nil {
		return relay{}, domain.RuntimeError(err, "cannot read the bundle's registry address %s", server)
	}
	// Best effort: a leftover listens on another port, and its timeout ends
	// it in any case.
	r.sweepRelays(ctx)

	digest, _ := domain.ImageSpec{Ref: pending[0]}.Digest()
	path := "/v2/" + repositoryPath(pending[0]) + "/manifests/" + digest

	var tried []string
	for range relayAttempts {
		rl := relay{
			name: "morzer-ingest-" + strings.ToLower(rand.Text()),
			port: relayPortMin + mrand.IntN(relayPortMax-relayPortMin+1), //nolint:gosec // a port to try, not a secret
		}
		tried = append(tried, strconv.Itoa(rl.port))
		// No --rm, so one that exits at start can be read (decision 19),
		// and --pull=never, since the helper was found local (decision 4).
		_, err := r.runner.Run(ctx, r.relayCommand(time.Minute,
			"run", "-d", "--pull=never", "--name", rl.name, "--network", "host",
			"--label", relayLabel+"="+r.relayFor, helper,
			"timeout", strconv.FormatInt(relaySeconds(ctx, len(pending)), 10),
			"nc", "-lk", "-s", "127.0.0.1", "-p", strconv.Itoa(rl.port),
			"-e", "nc", "host.docker.internal", serverPort))
		if err != nil {
			err = relayNotStarted(err, helper)
		} else {
			err = r.checkRelay(ctx, rl, helper, path)
		}
		if err == nil {
			return rl, nil
		}
		r.removeRelay(ctx, rl)
		if !errors.Is(err, errPortHeld) {
			return relay{}, err
		}
	}
	return relay{}, domain.RuntimeError(nil,
		"the relay a Mac loads a bundle's images through found port %s held on the engine's loopback",
		strings.Join(tried, ", ")).
		WithHint("another container on Docker Desktop's host network holds them; run the command again")
}

// relaySeconds is the relay's `timeout`, in the whole seconds busybox takes:
// the time left on the ingest's context, or with no deadline the sum of the
// limits of what runs once the relay has started (decision 15).
func relaySeconds(ctx context.Context, images int) int64 {
	bound := relayCheckLimit + time.Duration(images)*(ingestPullTimeout+ingestTagTimeout+ingestUntagTimeout)
	if deadline, ok := ctx.Deadline(); ok {
		bound = time.Until(deadline)
	}
	return max(1, int64(math.Ceil(bound.Seconds())))
}

// checkRelay asks the relay, from inside it, for the manifest at path, and
// tells why it was not served: a relay that exited because its port was held
// (errPortHeld), a helper without what the relay runs (decision 11), or a
// request nobody here answered, which is a daemon on another host (decisions
// 14 and 16).
func (r *Runtime) checkRelay(ctx context.Context, rl relay, helper, path string) error {
	checkCtx, cancel := context.WithTimeout(ctx, relayCheckLimit)
	defer cancel()

	for try := 1; ; try++ {
		res, err := r.runner.Run(checkCtx, r.relayCommand(15*time.Second,
			"exec", rl.name, "wget", "-q", "-T", "5", "-O", "/dev/null", "http://"+rl.addr()+path))
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return wrapExit(err, "the relay was interrupted", "")
		}

		if running, printed := r.relayState(ctx, rl); !running {
			if strings.Contains(printed, "Address already in use") {
				return errPortHeld
			}
			return lacksRelayTools(helper, printed)
		}
		printed := res.Stdout + res.Stderr
		var exitErr *exec.ExitError
		if asExit(err, &exitErr) {
			printed += exitErr.Stderr
			if exitErr.ExitCode == 126 || exitErr.ExitCode == 127 {
				return lacksRelayTools(helper, firstLine(printed))
			}
		}
		if try < relayCheckTries && checkCtx.Err() == nil {
			select {
			case <-checkCtx.Done():
			case <-time.After(r.relayWait):
			}
			continue
		}
		return domain.RuntimeError(domain.ErrUnsupported,
			"the relay a Mac loads a bundle's images through did not reach this manager").
			WithHint("asked through it for %s, the engine answered %q. The relay runs where the "+
				"daemon does, so DOCKER_HOST, DOCKER_CONTEXT or the current context in the docker "+
				"CLI's configuration (`docker context show`) names a daemon on another host: point "+
				"them at this Mac's Docker Desktop", path, firstLine(printed))
	}
}

// relayState reports whether the relay still runs, and when it does not, the
// first line it printed that says why.
func (r *Runtime) relayState(ctx context.Context, rl relay) (running bool, printed string) {
	res, err := r.runner.Run(ctx, r.relayCommand(30*time.Second,
		"inspect", "--format", "{{.State.Running}}", rl.name))
	if err == nil && strings.TrimSpace(res.Stdout) == "true" {
		return true, ""
	}
	res, _ = r.runner.Run(ctx, r.relayCommand(30*time.Second, "logs", rl.name))
	for _, line := range strings.Split(res.Stdout+"\n"+res.Stderr, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return false, line
		}
	}
	return false, "nothing"
}

// relayNotStarted is a `docker run` of the relay that failed: a helper without
// `timeout`, which exits 126 or 127 before the relay starts (decision 11), or
// a container the engine would not create (decision 13).
func relayNotStarted(err error, helper string) error {
	var exitErr *exec.ExitError
	if asExit(err, &exitErr) && (exitErr.ExitCode == 126 || exitErr.ExitCode == 127) {
		return lacksRelayTools(helper, firstLine(exitErr.Stderr))
	}
	return wrapExit(err, "cannot start the relay a Mac loads a bundle's images through",
		"it is a container on Docker Desktop's host network, which Enhanced Container Isolation "+
			"refuses; with that setting off, `docker run --network host` of the helper image "+
			"shows what the engine says")
}

// lacksRelayTools refuses a helper the relay cannot run in (decision 11).
func lacksRelayTools(helper, printed string) error {
	return domain.RuntimeError(domain.ErrToolMissing,
		"the helper image %s cannot run the relay a Mac loads a bundle's images through",
		domain.ShortImageRef(helper)).
		WithHint("it printed %q. The relay runs busybox's `timeout`, `nc` with -l, -k, -s, -p and -e, "+
			"and `wget` in that image: set %s to an image with them, or leave it unset", printed, helperImageEnv)
}

// sweepRelays removes the relays an ingest of this installation left when its
// manager was killed (decision 10).
func (r *Runtime) sweepRelays(ctx context.Context) {
	res, err := r.runner.Run(ctx, r.relayCommand(30*time.Second,
		"ps", "--all", "--quiet", "--filter", "label="+relayLabel+"="+r.relayFor))
	if err != nil {
		return
	}
	for _, id := range strings.Fields(res.Stdout) {
		_ = r.removeContainer(ctx, id)
	}
}

// removeRelay removes a relay, also once the ingest was interrupted.
func (r *Runtime) removeRelay(ctx context.Context, rl relay) {
	_ = r.removeContainer(context.WithoutCancel(ctx), rl.name)
}

// relayCommand is a docker command of the relay's, with its output kept from
// the live view: container IDs and a check's wget say nothing to an operator.
func (r *Runtime) relayCommand(timeout time.Duration, argv ...string) exec.Command {
	return exec.Command{
		Argv:          append([]string{r.docker}, argv...),
		Timeout:       timeout,
		Redact:        r.redact,
		CaptureOutput: true,
	}
}
