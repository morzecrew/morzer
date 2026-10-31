package cli

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/adapters/backup/hookbackup"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// valueLister names the values one kind of argument takes in an installation.
type valueLister func(ctx context.Context, d *ops.Deps) ([]cobra.Completion, error)

// completeValues offers what list finds in the installation the line being
// completed names: --product, --config, MORZER_PRODUCT, or the only one there
// is.
//
// Resolved again here because setup ran before cobra parsed the line, against
// the defaults. Nothing is offered when that installation cannot be read --
// two on the machine and none named, or under sudo, which runs the completion
// as the operator -- since an error printed into a command line helps nobody.
//
// The ambiguity is refused here as well as by the lookups, because the release
// store and the backups are listed without loading the installation, and an
// installation named like resolvePaths' placeholder would answer for them.
func (a *App) completeValues(list valueLister) cobra.CompletionFunc {
	return func(cmd *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
		ctx := cmd.Context()
		paths, err := a.resolvePaths(ctx)
		if err == nil {
			err = a.wireAt(ctx, paths, a.bus, a.redactor, a.log)
		}
		if err == nil {
			err = a.Deps.RequireInstallationChosen()
		}
		var out []cobra.Completion
		if err == nil {
			out, _ = list(ctx, a.Deps)
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

// onlyFirst completes a command's first argument and nothing after it.
func onlyFirst(f cobra.CompletionFunc) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return f(cmd, args, toComplete)
	}
}

// unnamed leaves out what the line already names, for a command that takes
// several. `name=value` names name.
func unnamed(f cobra.CompletionFunc) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		out, directive := f(cmd, args, toComplete)
		given := map[string]bool{}
		for _, arg := range args {
			name, _, _ := strings.Cut(arg, "=")
			given[name] = true
		}
		return slices.DeleteFunc(out, func(c cobra.Completion) bool {
			value, _, _ := strings.Cut(c, "\t")
			return given[strings.TrimSuffix(value, "=")]
		}), directive
	}
}

// serviceCompletionTimeout bounds the one completion that asks the Docker
// daemon, so a stalled daemon costs the prompt seconds rather than the minute
// `compose ps` is allowed.
const serviceCompletionTimeout = 3 * time.Second

// services offers what the project runs, as `ps` lists it. A scaled service is
// listed once per instance and offered once.
func services(ctx context.Context, d *ops.Deps) ([]cobra.Completion, error) {
	ctx, cancel := context.WithTimeout(ctx, serviceCompletionTimeout)
	defer cancel()
	states, err := ops.ListServices(ctx, d)
	if err != nil {
		return nil, err
	}
	var out []cobra.Completion
	seen := map[string]bool{}
	for _, s := range states {
		if !seen[s.Name] {
			seen[s.Name] = true
			out = append(out, cobra.CompletionWithDesc(s.Name, s.State))
		}
	}
	return out, nil
}

func tasks(ctx context.Context, d *ops.Deps) ([]cobra.Completion, error) {
	entries, err := ops.ListTasks(ctx, d)
	if err != nil {
		return nil, err
	}
	out := make([]cobra.Completion, 0, len(entries))
	for _, e := range entries {
		out = append(out, cobra.CompletionWithDesc(e.Name, e.Description))
	}
	return out, nil
}

// releasesWhere offers the versions in the release store that keep accepts,
// given the installed one, which is zero before the first release.
func releasesWhere(keep func(v, installed domain.Version) bool) valueLister {
	return func(ctx context.Context, d *ops.Deps) ([]cobra.Completion, error) {
		entries, err := ops.InstalledReleases(ctx, d)
		if err != nil {
			return nil, err
		}
		var installed domain.Version
		for _, e := range entries {
			if e.Current {
				installed = e.Version
			}
		}
		var out []cobra.Completion
		for _, e := range entries {
			if !keep(e.Version, installed) {
				continue
			}
			var role string
			switch {
			case e.Current:
				role = "installed"
			case e.Previous:
				role = "previous"
			case e.Staged:
				role = "staged"
			}
			out = append(out, cobra.CompletionWithDesc(e.Version.String(), role))
		}
		return out, nil
	}
}

func anyRelease(domain.Version, domain.Version) bool { return true }

// configNames offers the two kinds of name `config get`, `set` and `unset`
// take: the release's parameters and the installation settings, each followed
// by suffix.
func configNames(suffix string) valueLister {
	return func(ctx context.Context, d *ops.Deps) ([]cobra.Completion, error) {
		var out []cobra.Completion
		// Without the other: the settings exist before the first release.
		if report, err := ops.ConfigList(ctx, d); err == nil {
			for _, p := range report.Parameters {
				out = append(out, cobra.CompletionWithDesc(p.Name+suffix, p.Description))
			}
		}
		report, err := ops.ListSettings(ctx, d)
		if err != nil {
			return out, err
		}
		for _, s := range report.Settings {
			out = append(out, cobra.CompletionWithDesc(s.Name+suffix, s.Description))
		}
		return out, nil
	}
}

// unsettable adds to configNames the recorded values the current release no
// longer declares, which `config list` reports as stale and `config unset` is
// how an operator clears.
func unsettable(ctx context.Context, d *ops.Deps) ([]cobra.Completion, error) {
	out, err := configNames("")(ctx, d)
	if report, err := ops.ConfigList(ctx, d); err == nil {
		for _, name := range report.Stale {
			out = append(out, cobra.CompletionWithDesc(name, "no longer declared by the release"))
		}
	}
	return out, err
}

// completeConfigAssignments offers `name=` for `config set`, and nothing once
// the value is being typed.
func (a *App) completeConfigAssignments(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if strings.Contains(toComplete, "=") {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	out, directive := a.completeValues(configNames("="))(cmd, args, toComplete)
	return out, directive | cobra.ShellCompDirectiveNoSpace
}

// operations offers the journal's operations, newest first, each described by
// what it was and how it ended.
func operations(ctx context.Context, d *ops.Deps) ([]cobra.Completion, error) {
	history, err := ops.ListHistory(ctx, d, 0)
	if err != nil {
		return nil, err
	}
	out := make([]cobra.Completion, 0, len(history.Operations))
	for _, rec := range history.Operations {
		out = append(out, cobra.CompletionWithDesc(rec.ID, string(rec.Type)+" "+string(rec.Status)))
	}
	return out, nil
}

// backups offers this machine's backups, newest first. The listing reads only
// the backups directory, so no release has to load for it.
func backups(ctx context.Context, d *ops.Deps) ([]cobra.Completion, error) {
	refs, err := hookbackup.New(hookbackup.Config{Paths: d.Paths}).List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]cobra.Completion, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.ID)
	}
	return out, nil
}
