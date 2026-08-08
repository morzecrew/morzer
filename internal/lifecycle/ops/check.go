package ops

import (
	"context"
	"errors"
	"fmt"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// UpdateCheckOptions are the inputs to an update check.
type UpdateCheckOptions struct {
	Options

	// Ref is where to look. Empty uses the ref recorded with the current
	// release, which is why `update` records one.
	Ref string

	// Explicit marks a check an operator asked for by name, which is its
	// own authorisation. Unprompted callers -- `doctor`, `status` -- leave
	// it false and are gated by `update.check`.
	Explicit bool
}

// UpdateCheckResult is what a check found.
type UpdateCheckResult struct {
	// Ref is where it looked.
	Ref string `json:"ref,omitempty"`

	// Installed is what is running now.
	Installed domain.Version `json:"installed"`

	// Latest is the newest version the source offers that this
	// installation could actually move to. Zero when there is none.
	Latest domain.Version `json:"latest,omitempty"`

	// Available is true when Latest is newer than Installed.
	Available bool `json:"available"`

	// Considered is how many versions the source offered, before
	// compatibility narrowed them.
	Considered int `json:"considered"`
}

// CheckForUpdate asks a release source what versions exist.
//
// It exists because ReleaseSource.List has been implemented against the OCI tag
// list since RFC 0004, carries a comment saying it is on the port for exactly
// this, and has never been called by anything.
//
// Every refusal here is a refusal to *guess*. A transport that cannot
// enumerate, an installation with no recorded ref, a check that is not
// permitted: each says so, because reporting "up to date" for a question that
// was never asked is the worst outcome this feature has.
func CheckForUpdate(ctx context.Context, d *Deps, opts UpdateCheckOptions) (UpdateCheckResult, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return UpdateCheckResult{}, err
	}
	if !inst.Update.CheckAllowed(opts.Explicit) {
		return UpdateCheckResult{}, domain.ValidationError(domain.ErrUnsupported,
			"update checking is not enabled on this installation").
			WithHint("a check contacts the vendor's registry; enable it with " +
				"`morzer config`, or ask for one now with `morzer update --check`")
	}

	current, err := d.State.CurrentRelease(ctx)
	if err != nil {
		return UpdateCheckResult{}, err
	}
	if current.IsZero() {
		return UpdateCheckResult{}, domain.ValidationError(domain.ErrReleaseNotFound,
			"no release is installed, so there is nothing to compare against")
	}

	ref := opts.Ref
	if ref == "" {
		ref = current.SourceRef
	}
	if ref == "" {
		return UpdateCheckResult{}, domain.ValidationError(domain.ErrNotFound,
			"this installation has no recorded release source").
			WithHint("pass one -- `morzer update --check <ref>` -- or install the " +
				"next release from a ref, which records it")
	}

	parsed, err := ports.ParseRef(ref)
	if err != nil {
		return UpdateCheckResult{}, err
	}
	if d.Source == nil {
		return UpdateCheckResult{}, domain.Internal(nil, "no release source is configured")
	}

	versions, err := d.Source.List(ctx, parsed)
	if err != nil {
		// Reported rather than swallowed. "This transport cannot
		// enumerate versions" is a true answer; "up to date" would be a
		// false one, and it is the false one an operator would act on.
		if errors.Is(err, domain.ErrUnsupported) {
			return UpdateCheckResult{}, domain.ValidationError(domain.ErrUnsupported,
				"%s cannot list available versions", parsed.Scheme).
				WithHint("only an OCI registry keeps a tag list; check by hand, " +
					"or publish releases to one")
		}
		return UpdateCheckResult{}, err
	}

	result := UpdateCheckResult{
		Ref:        ref,
		Installed:  current.Version,
		Considered: len(versions),
	}

	// Narrowed by what this installation could actually install, not by
	// what exists: telling an operator about a release their current one
	// cannot be upgraded from is telling them about work they cannot do.
	for _, v := range versions {
		if !v.GreaterThan(current.Version) {
			continue
		}
		rep := domain.CheckUpgrade(current.Version, v, releaseCompatibility(ctx, d, v),
			d.ManagerVersion, current.SchemaAtInstall)
		if !rep.OK {
			continue
		}
		if result.Latest.IsZero() || v.GreaterThan(result.Latest) {
			result.Latest = v
		}
	}
	result.Available = !result.Latest.IsZero()
	return result, nil
}

// releaseCompatibility returns what a candidate version declares, when it
// happens to be in the release store already.
//
// A version the source merely *lists* has not been fetched, so its manifest is
// not on this machine and its compatibility is unknown. The zero value admits
// it — which is the right direction for a report: `--check` tells an operator
// something exists, and `update` is what refuses. Narrowing here on absent
// information would hide a release rather than describe it.
func releaseCompatibility(ctx context.Context, d *Deps, v domain.Version) domain.Compatibility {
	if rel, err := d.resolveInstalled(v.String()); err == nil {
		return rel.Manifest.Compatibility
	}
	_ = ctx
	return domain.Compatibility{}
}

// Summary renders the result for a human.
func (r UpdateCheckResult) Summary() string {
	if r.Available {
		return fmt.Sprintf("%s is available (installed %s)", r.Latest, r.Installed)
	}
	return fmt.Sprintf("%s is the newest available", r.Installed)
}
