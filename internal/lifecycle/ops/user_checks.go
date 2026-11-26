package ops

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/preflight"
)

// The checks only a user installation runs (RFC 0023 row 26). Variables so a
// test can stand in for the host.
var (
	// lingerDir holds one empty file per user whose manager outlives their
	// sessions, which `loginctl enable-linger` writes.
	lingerDir = "/var/lib/systemd/linger"

	// portStartFile is net.ipv4.ip_unprivileged_port_start: a port below it
	// needs a privilege to bind that a rootless engine does not have.
	portStartFile = "/proc/sys/net/ipv4/ip_unprivileged_port_start"
)

// checkLinger warns a user installation on an account that does not linger:
// its units stop when the user logs out and do not start at boot.
func (d *Deps) checkLinger() preflight.Check {
	return preflight.Check{
		ID:          "system.linger",
		Category:    preflight.CategorySystem,
		Description: "a user installation's units run without a login session",
		Run: func(context.Context) events.CheckResult {
			u, err := user.Current()
			if err != nil {
				return preflight.Warn("", "cannot tell who runs this: %s", err)
			}
			_, err = os.Stat(filepath.Join(lingerDir, u.Username))
			switch {
			case err == nil:
				return preflight.OK("%s lingers, so the units start at boot and outlive a logout", u.Username)
			case errors.Is(err, fs.ErrNotExist):
				return preflight.Warn(fmt.Sprintf("run `sudo loginctl enable-linger %s`", u.Username),
					"%s does not linger, so this installation stops at logout and does not start at boot",
					u.Username)
			}
			return preflight.Warn("", "cannot tell whether %s lingers: %s", u.Username, err)
		},
	}
}

// checkUnprivilegedPorts warns a user installation whose release publishes a
// port below net.ipv4.ip_unprivileged_port_start. A warning rather than a
// failure: an engine given CAP_NET_BIND_SERVICE binds it anyway, which this
// cannot see.
func (d *Deps) checkUnprivilegedPorts(inst domain.Installation, rel domain.Release) preflight.Check {
	return preflight.Check{
		ID:          "network.unprivileged-ports",
		Category:    preflight.CategoryNetwork,
		Description: "a user installation publishes no port that needs privilege to bind",
		Run: func(context.Context) events.CheckResult {
			params, err := d.parameters(rel, inst)
			if err != nil {
				return preflight.Warn("", "cannot resolve the release's ports: %s", domain.AsError(err).Message)
			}
			profiles, err := d.selection(rel, inst, nil)
			if err != nil {
				return preflight.Warn("", "cannot resolve the release's ports: %s", domain.AsError(err).Message)
			}
			required, err := rel.Manifest.ResolvePorts(params, profiles)
			if err != nil {
				return preflight.Warn("", "cannot resolve the release's ports: %s", domain.AsError(err).Message)
			}
			data, err := os.ReadFile(portStartFile)
			if err != nil {
				return preflight.Warn("", "cannot read %s: %s", portStartFile, err)
			}
			start, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				return preflight.Warn("", "cannot read %s: %s", portStartFile, err)
			}
			var low []string
			lowest := start
			for _, p := range required {
				if int(p.Port) < start {
					low = append(low, p.String())
					lowest = min(lowest, int(p.Port))
				}
			}
			if len(low) > 0 {
				return preflight.Warn(fmt.Sprintf("lower it with `sudo sysctl -w net.ipv4.ip_unprivileged_port_start=%d` "+
					"and keep that in /etc/sysctl.d, or give the engine CAP_NET_BIND_SERVICE", lowest),
					"the release publishes %s, below net.ipv4.ip_unprivileged_port_start (%d), "+
						"which an engine without privilege cannot bind", strings.Join(low, ", "), start)
			}
			return preflight.OK("every port the release publishes is %d or above", start)
		},
	}
}
