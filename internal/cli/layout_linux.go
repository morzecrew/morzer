package cli

import (
	"os"
	"path/filepath"

	"github.com/morzecrew/morzer/internal/domain"
)

// The user layout exists on Linux only (logs/quadlet-adapter.md, U5): a Mac
// runs the manager as root (RFC 0055 decision 13) and sets no $XDG_RUNTIME_DIR.
// layout_darwin.go says so for the Mac.

// userTree is where this user's installations live, `$XDG_DATA_HOME/morzer`
// and by default `~/.local/share/morzer` (RFC 0023 row 30). "" for root, whose
// layout is the system's, and for a user with no home.
func userTree() string {
	if os.Geteuid() == 0 {
		return ""
	}
	if data := userDataHome(); data != "" {
		return domain.UserTree(data)
	}
	return ""
}

// userDataHome is the XDG data home, or "" with neither an absolute
// $XDG_DATA_HOME nor a home to default it from.
func userDataHome() string {
	home, _ := os.UserHomeDir()
	if data := dataHome(os.Getenv, home); filepath.IsAbs(data) {
		return data
	}
	return ""
}

// isUserTree reports whether root is this user's tree, by any path that
// resolves to it: a data home behind a symlink is still the user's, and read
// as another tree its secrets would render inside it.
func isUserTree(root string) bool {
	tree := userTree()
	return tree != "" && root != "" && resolved(root) == resolved(tree)
}

// resolved is p with its symlinks resolved: through its nearest existing
// ancestor while p itself does not exist yet, and through a link whose target
// does not exist yet either.
func resolved(p string) string { return resolve(filepath.Clean(p), 0) }

// maxLinkHops bounds the links resolve follows by hand, as the kernel bounds
// its own, so a loop ends.
const maxLinkHops = 40

func resolve(p string, hops int) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	if target, err := os.Readlink(p); err == nil && hops < maxLinkHops {
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(p), target)
		}
		return resolve(filepath.Clean(target), hops+1)
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p
	}
	return filepath.Join(resolve(parent, hops), filepath.Base(p))
}

// userUnitDir is where the user's systemd manager reads the units written for
// it, `$XDG_CONFIG_HOME/systemd/user`, or "" with neither an absolute
// $XDG_CONFIG_HOME nor a home to default it from.
func userUnitDir() string {
	home, _ := os.UserHomeDir()
	if config := configHome(os.Getenv, home); filepath.IsAbs(config) {
		return filepath.Join(config, "systemd", "user")
	}
	return ""
}

// userPaths is product's layout in this user's tree. Refused while
// $XDG_RUNTIME_DIR is unset: the render directory holds decrypted secrets, and
// that is the user's one tmpfs (logs/quadlet-adapter.md, U1).
func userPaths(product string) (domain.Paths, error) {
	run := os.Getenv("XDG_RUNTIME_DIR")
	if !filepath.IsAbs(run) {
		return domain.Paths{}, domain.Preflight(nil,
			"XDG_RUNTIME_DIR is not set, and a user installation renders its secrets there").
			WithHint("run this from your own login session, or set XDG_RUNTIME_DIR=/run/user/%d",
				os.Geteuid())
	}
	if userUnitDir() == "" {
		// Else its units would be written relative to wherever the
		// command ran.
		return domain.Paths{}, domain.Preflight(nil,
			"HOME is not set, and a user installation's units are written under it").
			WithHint("run this from your own login session, or set HOME or XDG_CONFIG_HOME")
	}
	return domain.UserPaths(userDataHome(), run, product), nil
}
