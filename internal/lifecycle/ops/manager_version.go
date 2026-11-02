package ops

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/preflight"
)

// ManagerCheck compares the running manager with the newest published one
// (RFC 0046 §5.1). `version --check --json` carries it, so the shape is stable.
type ManagerCheck struct {
	Running string `json:"running"`
	Newest  string `json:"newest"`

	// URL is the newest release's page, where its notes are.
	URL string `json:"url"`

	// DevBuild is a manager with no release version, which has nothing to
	// compare and is never outdated (decision 12).
	DevBuild bool `json:"dev_build"`

	// Outdated is a newer release published; any newer one counts
	// (decision 3).
	Outdated bool `json:"outdated"`

	// Supported is the running manager on the newest release's minor line,
	// at or past its first release, which is what SECURITY.md supports. A
	// build ahead of that line is not a supported release.
	Supported bool `json:"supported"`

	// Upgrade is the command that moves this binary to the newest release,
	// present when it is outdated.
	Upgrade string `json:"upgrade,omitempty"`
}

// CheckManager asks the release feed for the newest manager and compares it
// with this one. It needs no installation and no consent: callers that run
// unprompted check `manager.check` first.
func CheckManager(ctx context.Context, d *Deps) (ManagerCheck, error) {
	if d.ManagerFeed == nil {
		return ManagerCheck{}, domain.Internal(nil, "this build has no release feed to ask")
	}
	newest, err := d.ManagerFeed.Newest(ctx)
	if err != nil {
		return ManagerCheck{}, err
	}

	running := d.ManagerVersion
	out := ManagerCheck{
		Running: running.String(),
		Newest:  newest.Version.String(),
		URL:     newest.URL,
		// A dev or snapshot build reaches this layer as 0.0.0, which no
		// release is.
		DevBuild: !running.GreaterThan(domain.MustParseVersion("0.0.0")),
	}
	if out.DevBuild {
		return out, nil
	}
	out.Outdated = newest.Version.GreaterThan(running)
	line := newest.Version.MinorLine()
	out.Supported = running.MinorLine().Equal(line) && !running.LessThan(line)
	if out.Outdated {
		out.Upgrade = "install.sh --version " + out.Newest + " --require-signature"
		if d.ManagerPath != "" {
			out.Upgrade += " --dir " + domain.ShellQuote(filepath.Dir(d.ManagerPath))
		}
	}
	return out, nil
}

// Summary is the one line that says where this manager stands.
func (c ManagerCheck) Summary() string {
	switch {
	case c.DevBuild:
		return fmt.Sprintf("a dev build, with no version to compare; %s is the newest release", c.Newest)
	case c.Running == c.Newest:
		return fmt.Sprintf("%s is the newest release", c.Running)
	case !c.Outdated:
		return fmt.Sprintf("%s is ahead of the newest release, %s", c.Running, c.Newest)
	case c.Supported:
		return fmt.Sprintf("%s is running and still supported; %s is the newest release", c.Running, c.Newest)
	default:
		return fmt.Sprintf("%s is running and no longer supported; %s is the newest release", c.Running, c.Newest)
	}
}

// checkManagerVersion is `manager.version`: whether a newer manager is
// published, asked only when the installation consented with `manager.check`,
// because the scheduled `doctor` would otherwise contact GitHub unprompted.
func (d *Deps) checkManagerVersion(inst domain.Installation) preflight.Check {
	return preflight.Check{
		ID:          "manager.version",
		Category:    preflight.CategoryManager,
		Description: "a newer manager is published",
		Fatal:       false,
		Run: func(ctx context.Context) events.CheckResult {
			if !inst.Update.ManagerCheck {
				return preflight.OK("not checked: manager.check is off")
			}
			if d.ManagerFeed == nil {
				return preflight.OK("this build has no release feed to ask")
			}

			probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			res, err := CheckManager(probeCtx, d)
			if err != nil {
				// Never "up to date": an operator would act on an
				// answer nobody gave.
				return preflight.Warn("run `morzer version --check` for the detail",
					"cannot check for a newer manager: %s", domain.AsError(err).Message)
			}
			if res.Outdated {
				return preflight.Warn("upgrade with "+res.Upgrade, "%s", res.Summary())
			}
			return preflight.OK("%s", res.Summary())
		},
	}
}
