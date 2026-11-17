package ops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
)

// A Mac's boot job is started again after every failed exit, and launchd has
// no counterpart of systemd's RestartPreventExitStatus=12, so an exit 12 is
// recorded here and ends the retries: while the record stands, a start at boot
// exits 0 without converging, and an operator's `apply` or `start` that
// succeeds removes it (RFC 0055 §5.6, logs/macos-host.md). Linux keeps
// systemd's rule, and converges at every boot.

// startupHoldFile is the record, root's, beside the manager's locks.
func (d *Deps) startupHoldFile() string { return filepath.Join(d.Paths.ManagerDir(), "startup-held") }

// startupHeld is the record's reason, and whether it stands. One that cannot be
// read still stands: the record exists to stop a loop.
func (d *Deps) startupHeld() (string, bool) {
	data, err := os.ReadFile(d.startupHoldFile())
	if os.IsNotExist(err) {
		return "", false
	}
	return strings.TrimSpace(string(data)), true
}

// heldSummary is what a start at boot says while the record stands.
func (d *Deps) heldSummary(reason string) string {
	return fmt.Sprintf("%s's start at boot is held since it ended needing a human (%s); "+
		"`sudo morzer apply` releases it once that is put right", d.Paths.Product, reason)
}

// recordStartup writes the record when a start at boot ends in exit 12, and
// removes it when an operator's run succeeds.
func (d *Deps) recordStartup(opts Options, err error) {
	if opts.DryRun || d.platform() != "darwin" {
		return
	}
	switch {
	case opts.Startup && domain.ExitCode(err) == domain.ExitManualIntervention:
		reason := fmt.Sprintf("exit 12 at %s: %s",
			time.Now().UTC().Format(time.RFC3339), domain.AsError(err).Message)
		if werr := atomicfs.WriteFile(d.startupHoldFile(), []byte(reason+"\n"), 0o600); werr != nil {
			d.warnf("cannot record the exit 12, so launchd starts this again in 30 seconds: %s",
				domain.AsError(werr).Message)
		}
	case !opts.Startup && err == nil:
		if rerr := os.Remove(d.startupHoldFile()); rerr != nil && !os.IsNotExist(rerr) {
			d.warnf("cannot release the start at boot, which stays held: %s", rerr)
		}
	}
}

// engineAnswers asks the engine for the project's containers. A Mac's Docker
// Desktop starts at login, and the boot job runs before one: an attempt that
// cannot reach it exits 7 before the lock, so an operator's command never waits
// on a login and nothing is journalled, and launchd tries again in 30 seconds.
func (d *Deps) engineAnswers(ctx context.Context, rel domain.Release, inst domain.Installation, profiles []string) error {
	cfg, err := d.runtimeConfig(rel, inst, profiles)
	if err != nil {
		return err
	}
	probe, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := d.Runtime.Status(probe, cfg); err != nil {
		return domain.RuntimeError(err, "the engine does not answer yet").
			WithHint("Docker Desktop starts at login; launchd starts this again in 30 seconds")
	}
	return nil
}
