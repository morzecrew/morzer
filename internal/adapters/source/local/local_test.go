package local_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/adapters/source/local"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

// This adapter is the reference implementation of ReleaseSource: resolve
// without side effects, fetch into a destination it does not choose, never let
// an entry escape it. The shared contract suite drives the happy paths from
// test/suite; what is here is every way an operator can point it at the wrong
// thing, because "no release bundle at /home/ops/relase" is a message someone
// reads at two in the morning.

func bundleDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd, "..", "..", "..", "..", "testdata", "bundle")
}

func ref(location string) ports.Ref {
	return ports.Ref{Scheme: local.Scheme, Location: location}
}

func TestResolveReadsABundleWithoutTouchingIt(t *testing.T) {
	dir := bundleDir(t)

	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	got, err := local.New().Resolve(context.Background(), ref(dir))
	if err != nil {
		t.Fatalf("the example bundle did not resolve: %v", err)
	}
	if got.Version.String() != "1.2.0" {
		t.Errorf("version = %s", got.Version)
	}
	if !strings.HasPrefix(got.Digest, "sha256:") {
		t.Errorf("digest = %q, want a sha256 reference", got.Digest)
	}
	if got.Size <= 0 {
		t.Error("size is zero, and preflight checks disk against it")
	}

	after, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Error("Resolve left something behind in the source directory")
	}
}

// TestADigestOnTheReferenceIsCheckedNotTrusted. Catching a mismatch here means
// it is caught before anything is copied into the release store.
func TestADigestOnTheReferenceIsCheckedNotTrusted(t *testing.T) {
	r := ref(bundleDir(t))
	r.Digest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

	_, err := local.New().Resolve(context.Background(), r)
	if err == nil {
		t.Fatal("a bundle that does not match its recorded digest was accepted")
	}
	de := domain.AsError(err)
	if !strings.Contains(de.Message, "expected") {
		t.Errorf("message %q does not say a digest was expected", de.Message)
	}
	if !strings.Contains(de.Hint, "modified") {
		t.Errorf("hint %q does not say what a mismatch means", de.Hint)
	}
}

func TestADigestThatMatchesIsAccepted(t *testing.T) {
	dir := bundleDir(t)
	resolved, err := local.New().Resolve(context.Background(), ref(dir))
	if err != nil {
		t.Fatal(err)
	}

	r := ref(dir)
	r.Digest = resolved.Digest
	if _, err := local.New().Resolve(context.Background(), r); err != nil {
		t.Errorf("a bundle rejected its own digest: %v", err)
	}
}

// The refusals. Each one is somebody's typo, and each has to say which typo.

func TestEveryWayAReferenceCanPointAtNothing(t *testing.T) {
	dir := t.TempDir()

	notABundle := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notABundle, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	emptyDir := filepath.Join(dir, "empty")
	if err := os.Mkdir(emptyDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct {
		location string
		wants    []string
	}{
		"no path at all": {
			"", []string{"no release path"},
		},
		"a path that does not exist": {
			filepath.Join(dir, "typo"), []string{"no release bundle at"},
		},
		"a file that is not an archive": {
			notABundle, []string{"not a release bundle"},
		},
		"a directory with no manifest": {
			emptyDir, []string{"contains no", "manifest.yaml"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := local.New().Resolve(context.Background(), ref(tc.location))
			if err == nil {
				t.Fatalf("%s was accepted as a release", name)
			}
			msg := err.Error()
			for _, want := range tc.wants {
				if !strings.Contains(msg, want) {
					t.Errorf("the refusal drops %q: %s", want, msg)
				}
			}
		})
	}
}

// TestTheRefusalForAParentDirectorySaysWhichDirectoryToUse. Pointing at the
// parent of a bundle is the commonest mistake, and "contains no manifest.yaml"
// alone leaves an operator guessing.
func TestTheRefusalForAParentDirectorySaysWhichDirectoryToUse(t *testing.T) {
	parent := filepath.Dir(bundleDir(t))

	_, err := local.New().Resolve(context.Background(), ref(parent))
	if err == nil {
		t.Fatal("the parent of a bundle was accepted as a bundle")
	}
	if !strings.Contains(domain.AsError(err).Hint, "not its parent") {
		t.Errorf("hint %q does not name the mistake", domain.AsError(err).Hint)
	}
}

// TestAVersionQualifiedReferenceFindsTheRightRelease is how the release store
// under /opt is addressed: one directory holding many versions.
func TestAVersionQualifiedReferenceFindsTheRightRelease(t *testing.T) {
	store := t.TempDir()
	if err := copyTree(bundleDir(t), filepath.Join(store, "1.2.0")); err != nil {
		t.Fatal(err)
	}

	r := ref(store)
	r.Version = domain.MustParseVersion("1.2.0")

	got, err := local.New().Resolve(context.Background(), r)
	if err != nil {
		t.Fatalf("a version-qualified reference into a release store failed: %v", err)
	}
	if got.Version.String() != "1.2.0" {
		t.Errorf("version = %s", got.Version)
	}
}

// TestAVersionThatIsNotInTheStoreFallsThroughToTheDirectoryItself, and then
// fails on the missing manifest -- which is the honest answer, because the
// directory really does not hold a bundle.
func TestAVersionThatIsNotInTheStoreIsRefused(t *testing.T) {
	store := t.TempDir()
	if err := copyTree(bundleDir(t), filepath.Join(store, "1.2.0")); err != nil {
		t.Fatal(err)
	}

	r := ref(store)
	r.Version = domain.MustParseVersion("9.9.9")

	if _, err := local.New().Resolve(context.Background(), r); err == nil {
		t.Fatal("a version that is not in the store resolved to something")
	}
}

func TestFetchCopiesRatherThanLinking(t *testing.T) {
	src := bundleDir(t)
	dest := filepath.Join(t.TempDir(), "release")

	path, err := local.New().Fetch(context.Background(), ref(src), dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(path) != dest {
		t.Errorf("Fetch returned %q, want the destination it was given", path)
	}

	// A release under /opt must be immutable; a symlink into an operator's
	// working directory is a release whose contents can change after it was
	// verified.
	info, err := os.Lstat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("Fetch produced a symlink to the source")
	}
	if _, err := os.Stat(filepath.Join(dest, "manifest.yaml")); err != nil {
		t.Errorf("the manifest did not arrive: %v", err)
	}
}

// TestFetchIntoTheSourceItselfIsANoOp. The walk would otherwise recurse into
// its own output.
func TestFetchIntoTheSourceItselfIsANoOp(t *testing.T) {
	dir := t.TempDir()
	if err := copyTree(bundleDir(t), dir); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	path, err := local.New().Fetch(context.Background(), ref(dir), dir)
	if err != nil {
		t.Fatalf("fetching a bundle into its own directory failed: %v", err)
	}
	if string(path) == "" {
		t.Error("no path was returned")
	}

	after, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("the copy recursed into its own output: %d entries became %d",
			len(before), len(after))
	}
}

func TestFetchOfSomethingThatIsNotThere(t *testing.T) {
	_, err := local.New().Fetch(context.Background(),
		ref(filepath.Join(t.TempDir(), "gone")), t.TempDir())
	if err == nil {
		t.Fatal("fetching a bundle that does not exist succeeded")
	}
}

// TestListDistinguishesCannotAnswerFromNothingToShow. "No versions here" and
// "this source cannot answer that" are different answers, and only one means
// the operator should look somewhere else.
func TestListDistinguishesCannotAnswerFromNothingToShow(t *testing.T) {
	t.Run("a single bundle directory", func(t *testing.T) {
		_, err := local.New().List(context.Background(), ref(bundleDir(t)))
		if err == nil {
			t.Fatal("a single bundle was listed as a version index")
		}
		if !strings.Contains(err.Error(), "single bundle") {
			t.Errorf("the refusal does not say why: %v", err)
		}
	})

	t.Run("an archive", func(t *testing.T) {
		_, err := local.New().List(context.Background(), ref("/tmp/bundle.tar.zst"))
		if err == nil {
			t.Fatal("an archive was listed as a version index")
		}
		if !strings.Contains(err.Error(), "single bundle") {
			t.Errorf("the refusal does not say why: %v", err)
		}
	})

	t.Run("a directory that does not exist", func(t *testing.T) {
		_, err := local.New().List(context.Background(), ref(filepath.Join(t.TempDir(), "nope")))
		if err == nil {
			t.Fatal("a directory that is not there was listed")
		}
	})

	t.Run("an empty release store", func(t *testing.T) {
		got, err := local.New().List(context.Background(), ref(t.TempDir()))
		if err != nil {
			t.Fatalf("an empty release store is not an error: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("versions appeared from an empty directory: %v", got)
		}
	})
}

// TestListSkipsWhatIsNotABundle: release stores accumulate stray directories,
// and one of them must not make `release list` fail.
func TestListSkipsWhatIsNotABundle(t *testing.T) {
	store := t.TempDir()
	if err := copyTree(bundleDir(t), filepath.Join(store, "1.2.0")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(store, "tmp-leftovers"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "README"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := local.New().List(context.Background(), ref(store))
	if err != nil {
		t.Fatalf("a stray directory made the whole store unlistable: %v", err)
	}
	if len(got) != 1 || got[0].String() != "1.2.0" {
		t.Errorf("got %v, want just 1.2.0", got)
	}
}

func TestSchemes(t *testing.T) {
	got := local.New().Schemes()
	if len(got) != 1 || got[0] != "file" {
		t.Errorf("Schemes() = %v; the registry dispatches on this", got)
	}
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
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
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

// Fetch extracts before it validates, so a bundle it refuses is a bundle it
// has already written. Leaving it there puts an unusable release in the store,
// one `update --to` away from being installed by somebody who never saw the
// error -- and neither caller cleans it up: `ops.fetchRelease` returns straight
// out on a Fetch error, and `stepStageRelease`'s compensation keys off the
// release in engine state, which a failed Fetch never put there.
func TestFetchRemovesAnArchiveItRefuses(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, release.ManifestFileName),
		[]byte("schema_version: 1\nmetadata:\n  name: demo\n  version: not-a-version\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "demo-1.2.0.tar.zst")
	if err := atomicfs.WriteTarZst(archive, src,
		[]string{release.ManifestFileName}, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "releases", "1.2.0")
	if _, err := local.New().Fetch(context.Background(), ref(archive), dest); err == nil {
		t.Fatal("expected the invalid bundle to be refused")
	}

	entries, err := os.ReadDir(dest)
	if err == nil && len(entries) > 0 {
		t.Fatalf("Fetch left %d entries in the destination it refused", len(entries))
	}
}
