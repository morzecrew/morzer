package clitest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures the command tests act on.
//
// Built by running the commands rather than by writing state files: a fixture
// assembled by hand is a fixture that keeps passing after the code that
// produces that shape has changed. `release fetch` populates the store,
// `secret set` populates the secret state, and if either breaks these break
// with it.

// BundleAt copies the example bundle and rewrites its version, so a release
// store can hold several.
//
// The version lives in the manifest, and the store is keyed by it, so changing
// one line is the whole of what makes a second release.
func (r *Runner) BundleAt(version string) string {
	r.t.Helper()

	dir := filepath.Join(r.t.TempDir(), "bundle-"+version)
	copyDir(r.t, r.Bundle, dir)

	manifest := filepath.Join(dir, "manifest.yaml")
	data, err := os.ReadFile(manifest)
	if err != nil {
		r.t.Fatal(err)
	}
	rewritten := strings.Replace(string(data), "version: 1.2.0", "version: "+version, 1)
	if rewritten == string(data) {
		r.t.Fatalf("the example bundle no longer declares version 1.2.0, so this "+
			"fixture is rewriting nothing; manifest:\n%s", data)
	}
	if err := os.WriteFile(manifest, []byte(rewritten), 0o644); err != nil {
		r.t.Fatal(err)
	}

	// The bundle also carries a VERSION file, and the loader refuses a
	// bundle where the two disagree -- which this fixture discovered by
	// being refused. Rewriting only the manifest would have produced a
	// fixture that tests the disagreement check rather than the store.
	versionFile := filepath.Join(dir, "VERSION")
	if _, err := os.Stat(versionFile); err == nil {
		if err := os.WriteFile(versionFile, []byte(version+"\n"), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	return dir
}

// FetchReleases populates the release store with each version given.
func (r *Runner) FetchReleases(versions ...string) *Runner {
	r.t.Helper()
	for _, v := range versions {
		r.Run("release", "fetch", r.BundleAt(v)).ExitCode(0)
	}
	return r
}

// SetSecret stores a value the way an operator does: piped, never in argv.
func (r *Runner) SetSecret(name, value string) *Runner {
	r.t.Helper()
	r.RunWithInput(value+"\n", "secret", "set", name).ExitCode(0)
	return r
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()

	if err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// The executable bit matters: a hook that arrives without it is
		// a broken bundle and the loader says so.
		return os.WriteFile(target, data, info.Mode().Perm())
	}); err != nil {
		t.Fatal(err)
	}
}
