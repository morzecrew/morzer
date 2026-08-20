package domain

import "strings"

// Excluded bundle paths: what a source tree carries that a release does not.
//
// RFC 0014 gave a vendor two commands between a source tree and something
// publishable and never said the two are different things. They are: a bundle
// is authored in a directory, that directory is usually a working copy, and
// `release build` writes in place (RFC 0014 decision 10) precisely so a
// multi-gigabyte `images/` layout is not copied. So whatever else is sitting in
// that directory was being enumerated, checksummed, signed over, and packed.
//
// Measured 2026-08-19 on a bundle built inside a git repository: `SHA256SUMS`
// carried 55 entries of which 42 were `.git/`, including `.git/config` and the
// whole of `.git/objects`, and `release archive` packed all of them into the
// published archive. `.git/config` routinely holds a remote URL with an
// embedded token in CI. The signature chain is signature -> SHA256SUMS -> every
// file, so "every file" had come to mean the vendor's repository.
//
// Not exotic: `--version-from-git` requires the bundle to be a git repository,
// so this is the workflow that flag exists for.
var (
	// excludedBundleDirs are matched against every path component, because
	// a working copy can sit at the bundle root or above a subdirectory of
	// it, and neither belongs in a release.
	excludedBundleDirs = map[string]bool{
		".git": true, ".hg": true, ".svn": true, ".bzr": true,
	}

	// excludedBundleFiles are matched against the base name.
	excludedBundleFiles = map[string]bool{
		".DS_Store": true, "Thumbs.db": true, ".directory": true,
	}
)

// IsExcludedFromBundle reports whether a bundle-relative slash path is part of
// the source tree rather than part of the release.
//
// Exact names only, deliberately -- no `*~` or `*.swp` globbing. A pattern rule
// decides the fate of names nobody has looked at, and the direction it fails in
// is the expensive one: a file wrongly excluded is absent from a release that
// otherwise looks complete. Exact names can only be wrong about files that are
// named exactly those things.
//
// The safety net for the remaining risk is not this function. A file the
// manifest declares and this excludes fails `checkReferencedFiles` at load, on
// the vendor's machine, before anything is published -- which is why the list
// can afford to be conservative rather than clever.
func IsExcludedFromBundle(rel string) bool {
	if rel == "" {
		return false
	}
	parts := strings.Split(rel, "/")
	for _, part := range parts[:len(parts)-1] {
		if excludedBundleDirs[part] {
			return true
		}
	}
	last := parts[len(parts)-1]
	return excludedBundleDirs[last] || excludedBundleFiles[last]
}
