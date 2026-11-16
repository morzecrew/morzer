package ops

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/lifecycle/preflight"
	"github.com/morzecrew/morzer/internal/ports"
)

// checkFileSharing fails while a container cannot reach a managed directory as
// services need it: list EtcDir, VarDir, OptDir and RunDir, and write DataDir
// (RFC 0055 §5.3 and decision 16). It runs only where the engine reads the
// host as one user, as on a Mac. A probe that cannot run warns, since `up`
// then refuses the same bind in the engine's words (logs/macos-host.md).
//
// give hands DataDir to the engine's user first, as every start does, so
// apply's preflight probes it as services will find it. doctor gives
// nothing, and writes DataDir only once that user owns it.
func (d *Deps) checkFileSharing(prober ports.FileSharingProber, give bool) preflight.Check {
	return preflight.Check{
		ID:          "runtime.file-sharing",
		Category:    preflight.CategoryRuntime,
		Description: "a container can list the managed directories and write the data directory",
		Fatal:       true,
		Run: func(ctx context.Context) events.CheckResult {
			// Not knowing who runs the engine, or not managing to give
			// it DataDir, warns: the render and the start refuse with it.
			uid, gid, err := d.EngineUser()
			if err == nil && give {
				err = atomicfs.GiveTree(ctx, d.Paths.DataDir(), uid, gid)
			}
			if err != nil {
				e := domain.AsError(err)
				return preflight.Warn(e.Hint, "%s; nothing was probed", e.Message)
			}

			var read, write []string
			for _, dir := range []string{d.Paths.EtcDir, d.Paths.VarDir, d.Paths.OptDir, d.Paths.RunDir, d.Paths.DataDir()} {
				info, err := os.Stat(dir)
				if err != nil || !info.IsDir() {
					continue // storage.directories reports it
				}
				if owner, _ := atomicfs.Owner(info); dir == d.Paths.DataDir() && owner == uid {
					write = append(write, dir)
				} else {
					read = append(read, dir)
				}
			}

			err = prober.ProbeFileSharing(ctx, read, write)
			e := domain.AsError(err)
			switch {
			case errors.Is(err, domain.ErrPreflight):
				return preflight.Fail(e.Hint, "%s", e.Message)
			case err != nil:
				return preflight.Warn(e.Hint, "%s; nothing was probed", e.Message)
			case len(write) > 0:
				return preflight.OK("a container lists %s and writes %s", strings.Join(read, ", "), strings.Join(write, ", "))
			}
			return preflight.OK("a container lists %s", strings.Join(read, ", "))
		},
	}
}
