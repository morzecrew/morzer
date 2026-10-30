package cli

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/adapters/supervisor/systemd"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// completeProducts offers the installations `morzer ls` lists as values for
// --product.
//
// The listing itself rather than a second walk of /etc: no Docker call, no
// lock, no network, and an installation whose state will not load is offered
// anyway, because the operator may be about to run `doctor` or `installation
// remove` on it.
//
// Under sudo the completion runs as the operator, not as root, so the 0750
// installation directories cannot be opened and the listing comes back with
// every one of them unreadable. The generated units are 0644, so a
// `<product>.service` the manager wrote names an installation without opening
// it. An installation made with `--install-units=false` has none and is not
// offered then; the command still checks whatever name it is given.
func (a *App) completeProducts(cmd *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if a.Deps == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	// The completion request parses the line after setup ran, so the root
	// setup saw is not the one the line names: re-root a copy.
	d := *a.Deps
	d.Paths = domain.PathsUnder(a.Flags.root, "")

	entries, err := ops.ListInstallations(cmd.Context(), &d, ops.ListOptions{})
	unreadable := err != nil
	offered := map[string]bool{}
	var out []cobra.Completion
	for _, e := range entries {
		if e.Skipped {
			unreadable = true
			continue
		}
		offered[e.Product] = true
		out = append(out, cobra.CompletionWithDesc(e.Product, describeForCompletion(e)))
	}
	if unreadable {
		for _, product := range unitProducts(filepath.Join(a.Flags.root, systemd.UnitDir)) {
			if !offered[product] {
				offered[product] = true
				out = append(out, cobra.CompletionWithDesc(product, "not readable as this user"))
			}
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// describeForCompletion is the part of an `ls` row that tells two
// installations apart at a glance.
func describeForCompletion(e ops.InstallationEntry) string {
	switch {
	case e.Problem != "":
		return "state does not load"
	case e.Release.IsZero():
		return "no release yet"
	case e.Mode == domain.ModeDev:
		return e.Release.String() + ", dev"
	default:
		return e.Release.String()
	}
}

// unitProducts names the products whose service unit the manager wrote in dir.
//
// Recognised by the ExecStart the manager writes, not by the file name alone:
// `/etc/systemd/system` holds every service on the host. The whole argument
// shape of serviceTemplate is matched rather than a substring of it, so another
// tool that happens to take `apply --startup` is not mistaken for this one.
func unitProducts(dir string) []string {
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var products []string
	for _, f := range files {
		product, ok := strings.CutSuffix(f.Name(), ".service")
		if !ok || f.IsDir() || domain.ValidateProductName(product) != nil {
			continue
		}
		if startsTheManager(filepath.Join(dir, f.Name())) {
			products = append(products, product)
		}
	}
	return products
}

func startsTheManager(path string) bool {
	f, err := os.Open(path) //nolint:gosec // a unit file in the directory named above
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	lines := bufio.NewScanner(f)
	for lines.Scan() {
		if exec, ok := strings.CutPrefix(lines.Text(), "ExecStart="); ok {
			args := strings.Fields(exec)
			return len(args) >= 6 && slices.Equal(args[1:6],
				[]string{"apply", "--startup", "--plain", "--log-format", "json"})
		}
	}
	return false
}
