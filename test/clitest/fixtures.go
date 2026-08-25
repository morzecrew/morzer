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

// Corrupt replaces a file under the root with bytes that will not parse.
//
// For the tests that assert what the manager does with state it cannot read.
// That is a case worth driving through the real reader rather than a fake that
// returns an error, because the reader's own answer -- which file, and why -- is
// what the operator is given.
func (r *Runner) Corrupt(parts ...string) *Runner {
	r.t.Helper()

	path := r.Path(parts...)
	if _, err := os.Stat(path); err != nil {
		// A path that does not exist would make the test pass for the
		// wrong reason: nothing was corrupted, and the assertion below
		// it would be about a missing file instead.
		r.t.Fatalf("cannot corrupt %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte("{ this is not the state you left\n"), 0o600); err != nil {
		r.t.Fatalf("cannot corrupt %s: %v", path, err)
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

// LegacyBundle is a copy of the example bundle written the way a released
// bundle really was: the deprecated `runtime:` block instead of `runtimes:`.
//
// It exists because the shared fixture no longer is one. `runtime:` stopped
// being read in 0.3.0 (RFC 0023 decision 23), so every fixture in the tree
// moved to the current spelling -- and the refusal that removal put in place
// then had nothing to refuse. A test for a rejected input needs the rejected
// input, and after a removal the only place left to get it is a fixture that
// reconstructs it.
func (r *Runner) LegacyBundle() string {
	r.t.Helper()

	dir := filepath.Join(r.t.TempDir(), "legacy-bundle")
	copyDir(r.t, r.Bundle, dir)

	manifest := filepath.Join(dir, "manifest.yaml")
	data, err := os.ReadFile(manifest)
	if err != nil {
		r.t.Fatal(err)
	}
	const current = `runtimes:
  compose:
    options:
      project: demo
    files:
      - compose/compose.yaml
    profiles:
      embedded: [compose/compose.embedded.yaml]
      external-db: [compose/compose.external-db.yaml]`
	const legacy = `runtime:
  project: demo
  files:
    - compose/compose.yaml
  profiles:
    embedded: [compose/compose.embedded.yaml]
    external-db: [compose/compose.external-db.yaml]`
	rewritten := strings.Replace(string(data), current, legacy, 1)
	if rewritten == string(data) {
		r.t.Fatalf("the example bundle no longer carries the block this fixture "+
			"rewrites; update %s", manifest)
	}
	if err := os.WriteFile(manifest, []byte(rewritten), 0o644); err != nil {
		r.t.Fatal(err)
	}
	return dir
}

// BundleWithARequiredParameter is the example bundle with one declaration that
// has no default.
//
// A manifest saying "you must choose this" is what `MissingValues` exists for,
// and the example bundle gives every parameter a default -- so nothing in the
// suite reached that check until a plan started making it.
func (r *Runner) BundleWithARequiredParameter() string {
	r.t.Helper()

	dir := filepath.Join(r.t.TempDir(), "required-parameter-bundle")
	copyDir(r.t, r.Bundle, dir)

	manifest := filepath.Join(dir, "manifest.yaml")
	data, err := os.ReadFile(manifest)
	if err != nil {
		r.t.Fatal(err)
	}

	const anchor = "parameters:\n"
	const added = `parameters:
  admin_email:
    type: string
    description: Where the application sends operational mail
    services: [app]
`
	rewritten := strings.Replace(string(data), anchor, added, 1)
	if rewritten == string(data) {
		r.t.Fatalf("the example bundle no longer declares parameters; update %s", manifest)
	}
	if err := os.WriteFile(manifest, []byte(rewritten), 0o644); err != nil {
		r.t.Fatal(err)
	}
	return dir
}
