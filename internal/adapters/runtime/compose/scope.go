package compose

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

var _ ports.EngineScopeProber = (*Runtime)(nil)

// ProbeEngineScope refuses a daemon that is not the user's own (RFC 0023 row
// 32). A rootless daemon lists `name=rootless` among the security options
// `docker info` reports, and the system's daemon, reached through the docker
// group, does not.
//
// The hint names a Docker context rather than DOCKER_HOST, because the context
// is in the user's Docker configuration, which the user's units read too, and
// a variable set in a shell is not.
func (r *Runtime) ProbeEngineScope(ctx context.Context) error {
	res, err := r.runner.Run(ctx, exec.Command{
		Argv:          []string{r.docker, "info", "--format", "{{json .SecurityOptions}}"},
		Timeout:       30 * time.Second,
		Redact:        r.redact,
		CaptureOutput: true,
	})
	if err != nil {
		var exitErr *exec.ExitError
		if asExit(err, &exitErr) {
			return domain.RuntimeError(err, "cannot ask the Docker daemon whose it is: %s", firstLine(exitErr.Stderr))
		}
		return domain.RuntimeError(err, "cannot ask the Docker daemon whose it is")
	}
	var options []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &options); err != nil {
		return domain.RuntimeError(err, "cannot read the Docker daemon's security options")
	}
	for _, option := range options {
		if slices.Contains(strings.Split(option, ","), "name=rootless") {
			return nil
		}
	}
	return domain.Preflight(nil, "the Docker daemon is the system's, not this user's own, "+
		"so this installation would share its containers and volumes with the system's").
		WithHint("run rootless Docker for this user (`dockerd-rootless-setuptool.sh install`) and select it " +
			"with `docker context use rootless`, or install as root in the system layout")
}
