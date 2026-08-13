package suite

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// The support bundle (RFC 0024 P2).
//
// The feature's failure mode is silent and permanent: an archive that carried
// something it should not have is already in a ticket system by the time
// anybody looks. So these tests are mostly about what is *not* in it, and they
// assert that by reading the archive rather than by reading the code that
// writes it.

// Everything the inventory classifies for inclusion, and that this phase
// collects, is in the archive.
func TestASupportBundleCarriesEveryCollectedComponent(t *testing.T) {
	h := newHarness(t)
	h.install()

	report, err := ops.SupportBundle(context.Background(), h.Deps, ops.SupportOptions{Dir: t.TempDir()})
	require.NoError(t, err)

	entries := archiveEntries(t, report.Path)

	// Named individually rather than counted against the inventory, so
	// that classifying a new component does not make this test pass by
	// definition -- it has to be collected as well as classified.
	for _, name := range []string{
		"manifest.yaml", "installation.yaml", "parameters.json", "config-diff.txt",
		"journal.jsonl", "doctor.json", "releases.json", "services.json",
		"manager.json", "meta.json",
	} {
		require.Containsf(t, entries, name, "%s is missing from the archive", name)
	}

	// The report and the archive agree. A report that listed a component
	// the archive lacks is the preview lying about the artifact, which is
	// the one thing --preview exists to prevent.
	reported := make([]string, 0, len(report.Entries))
	for _, e := range report.Entries {
		reported = append(reported, e.Name)
	}
	require.ElementsMatch(t, reported, keys(entries))
}

// Nothing the inventory refuses reaches the archive -- asserted against the
// bytes, not against the collector list.
//
// This is the test the feature exists to pass. Every refused location is
// seeded with a marker that appears nowhere else, the archive is built, and
// every entry is searched for every marker. A guard that only checked which
// collectors are registered would be an intent-guard: it would pass on the day
// a collector reads a whole directory and sweeps one of these up by accident.
func TestNothingRefusedReachesTheArchive(t *testing.T) {
	h := newHarness(t)
	h.install()

	markers := map[string]string{}
	for i, path := range domain.SupportRefusedPaths(h.Paths) {
		marker := "REFUSED-MARKER-" + string(rune('A'+i)) + "-must-not-travel"
		markers[path] = marker
		seedRefusedPath(t, path, marker)
	}
	require.NotEmpty(t, markers, "no refused paths were seeded, so this test proves nothing")

	report, err := ops.SupportBundle(context.Background(), h.Deps, ops.SupportOptions{Dir: t.TempDir()})
	require.NoError(t, err)

	for name, body := range archiveEntries(t, report.Path) {
		for path, marker := range markers {
			require.NotContainsf(t, body, marker,
				"%s carries content from %s, which the inventory refuses", name, path)
		}
	}
}

// A preview writes nothing at all, and reports the same components a real run
// would.
//
// "Writes nothing" is checked against the directory rather than against the
// returned path: a preview that produced an archive and forgot to mention it
// would still have an empty Path.
func TestAPreviewWritesNothing(t *testing.T) {
	h := newHarness(t)
	h.install()

	dir := t.TempDir()
	preview, err := ops.SupportBundle(context.Background(), h.Deps, ops.SupportOptions{
		Preview: true,
		Dir:     dir,
	})
	require.NoError(t, err)
	require.True(t, preview.Preview)
	require.Empty(t, preview.Path)

	left, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, left, "--preview wrote something")

	real, err := ops.SupportBundle(context.Background(), h.Deps, ops.SupportOptions{Dir: t.TempDir()})
	require.NoError(t, err)

	require.Equal(t, componentNames(preview), componentNames(real),
		"the preview and the archive disagree about what is collected")
}

// A component that cannot be collected is recorded as omitted, never dropped.
//
// The difference matters to whoever reads the archive. A missing file with no
// explanation is three different situations -- the component does not exist on
// this machine, it failed to collect, it was never part of the format -- and
// they have different next steps. Unlike `installation describe`, whose
// document is committed as a record and must refuse rather than record an
// absence nobody verified, this one states the gap.
func TestAComponentThatCannotBeCollectedIsRecordedNotDropped(t *testing.T) {
	h := newHarness(t)
	h.install()

	// The runtime is what `services.json` needs, and a runtime that will
	// not answer is the ordinary case for this command: it is run when
	// something is broken.
	h.Runtime.Fail = map[string]error{"Status": errors.New("the runtime is not answering")}

	report, err := ops.SupportBundle(context.Background(), h.Deps, ops.SupportOptions{Dir: t.TempDir()})
	require.NoError(t, err, "one broken component must not cost the whole archive")

	var omitted []string
	for _, o := range report.Omitted {
		omitted = append(omitted, o.Name)
		require.NotEmptyf(t, o.Reason, "%s was omitted without a reason", o.Name)
	}
	require.Contains(t, omitted, "services.json")

	entries := archiveEntries(t, report.Path)
	require.NotContains(t, entries, "services.json")

	// And the archive says so itself, so a reader who has only the file
	// knows the gap is a failure rather than a format they do not know.
	require.Contains(t, entries["meta.json"], "services.json")
}

// The archive's name survives being sent somewhere, which is the only journey
// it is built to make.
//
// A colon is legal in a filename on every platform this runs on and is a trap
// in `scp support-...T17:04:05Z.tar.zst host:` -- scp reads everything before
// the first colon as a hostname. RFC 0024 §3.1 says RFC 3339; this is the basic
// form of it, recorded as amendment A1.
func TestTheArchiveNameSurvivesBeingSent(t *testing.T) {
	h := newHarness(t)
	inst := h.install()

	report, err := ops.SupportBundle(context.Background(), h.Deps, ops.SupportOptions{Dir: t.TempDir()})
	require.NoError(t, err)

	// Absolute, because `--json` puts this on stdout for something else to
	// act on, and that something is not standing where the archive was
	// written. Found by the acceptance lane reading `.data.path` from a
	// directory other than the one it had cd'd into.
	require.True(t, filepath.IsAbs(report.Path), "the reported path is relative: %s", report.Path)

	name := filepath.Base(report.Path)
	require.NotContains(t, name, ":", "a colon in the name makes scp read it as a host")
	require.Contains(t, name, inst.Product)
	require.Contains(t, name, inst.ID)
	require.True(t, strings.HasSuffix(name, ".tar.zst"))
}

// The archive is written by a build that injects no clock.
//
// Found by the acceptance lane, not by this suite, and the reason is the
// harness: every test here sets `Deps.Now` so that journal output is stable, so
// every test here was exercising a field production leaves nil. `Deps` has a
// `now()` accessor for exactly this and the archive writer was calling `Now()`
// directly -- which meant the one code path no unit test could reach was the
// one an operator reaches first.
//
// `--preview` never touched it, so the crash waited behind the flag an operator
// is told to run second.
func TestTheArchiveIsWrittenWithNoInjectedClock(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.Deps.Now = nil

	report, err := ops.SupportBundle(context.Background(), h.Deps, ops.SupportOptions{Dir: t.TempDir()})
	require.NoError(t, err)
	require.FileExists(t, report.Path)
}

// An installation with no release still produces an archive.
//
// A failed first `apply` is one of the times somebody most needs to send
// evidence, and it is exactly the state in which the release-dependent
// components cannot be collected. The archive that results is smaller and says
// why, rather than being a refusal.
func TestABundleIsProducedBeforeAnyReleaseIsInstalled(t *testing.T) {
	h := newHarness(t)
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), domain.Installation{
		SchemaVersion: domain.InstallationSchemaVersion,
		ID:            "inst_01NORELEASEYET",
		Product:       "demo",
		CreatedAt:     domain.NewTime(h.Deps.Now()),
		Policy:        domain.DefaultPolicy(),
	}))

	report, err := ops.SupportBundle(context.Background(), h.Deps, ops.SupportOptions{Dir: t.TempDir()})
	require.NoError(t, err)

	entries := archiveEntries(t, report.Path)
	require.Contains(t, entries, "installation.yaml")
	require.Contains(t, entries, "journal.jsonl")

	var omitted []string
	for _, o := range report.Omitted {
		omitted = append(omitted, o.Name)
	}
	require.Contains(t, omitted, "manifest.yaml")
	require.Contains(t, omitted, "config-diff.txt")
}

// ----------------------------------------------------------------------------

// seedRefusedPath writes a marker at a refused location, whether the inventory
// named a file or a directory.
//
// The distinction is not known here and must not be: the inventory names real
// paths, and a test that hard-coded which of them are directories would go
// stale the first time one changed shape.
func seedRefusedPath(t *testing.T, path, marker string) {
	t.Helper()

	if ext := filepath.Ext(path); ext != "" {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(marker), 0o600))
		return
	}
	require.NoError(t, os.MkdirAll(path, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(path, "seeded"), []byte(marker), 0o600))
}

// archiveEntries reads the archive the way its recipient will: `tar` and
// `zstd`, not the manager's own bundle extractor.
//
// Deliberately not `atomicfs.ExtractTarZst`. That reader enforces a release
// bundle's contract -- a required first entry, a declared size budget -- and
// asserting through it would prove the support bundle satisfies a format it is
// not in. A stranger will run `tar --zstd -xf`.
func archiveEntries(t *testing.T, path string) map[string]string {
	t.Helper()

	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	zr, err := zstd.NewReader(f)
	require.NoError(t, err)
	defer zr.Close()

	out := map[string]string{}
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)

		body, err := io.ReadAll(tr)
		require.NoError(t, err)
		out[hdr.Name] = string(body)

		// The normalisation atomicfs applies is worth asserting here
		// too: an archive that carried the operator's account name says
		// something about them nobody asked it to say.
		require.Zerof(t, hdr.Uid, "%s carries a uid", hdr.Name)
		require.Emptyf(t, hdr.Uname, "%s carries an owner name", hdr.Name)
	}
	return out
}

func componentNames(r ops.SupportReport) []string {
	out := make([]string, 0, len(r.Entries))
	for _, e := range r.Entries {
		out = append(out, e.Name)
	}
	return out
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A release that cannot be read is reported as broken, not as absent.
//
// This is the distinction `describe.go` names and this file's own omission
// policy rests on: absent is absent, broken is broken, and the state layer
// already tells them apart. Discarding the resolution error collapsed them, so
// an operator whose release directory had been modified after installation --
// a digest mismatch, which the resolver raises deliberately -- got a bundle
// saying no release was installed. The one machine most in need of a support
// bundle produced the one that hid why.
func TestABrokenReleaseIsReportedAsBrokenNotAbsent(t *testing.T) {
	h := newHarness(t)
	h.install()

	// Modified after installation: the release is there, and it is no
	// longer what was verified.
	require.NoError(t, os.WriteFile(
		filepath.Join(h.Release.Root, "morzer.yaml"), []byte("name: tampered\n"), 0o600))

	report, err := ops.SupportBundle(context.Background(), h.Deps,
		ops.SupportOptions{Dir: t.TempDir()})
	require.NoError(t, err, "a broken release must still produce an archive")

	reasons := map[string]string{}
	for _, o := range report.Omitted {
		reasons[o.Name] = o.Reason
	}
	require.Contains(t, reasons, "manifest.yaml")
	require.NotContainsf(t, reasons["manifest.yaml"], "no release is installed",
		"a release that failed to load was reported as one that was never installed: %q",
		reasons["manifest.yaml"])
	require.Contains(t, reasons, "config-diff.txt")
}

// A release record that will not parse is also reported, not fatal.
//
// The narrower half of the same fix. Keeping the resolution error made an
// unreadable *record* travel the way a broken release directory does, where it
// used to fail the whole command — and an installation whose state will not
// answer is one somebody urgently needs a bundle from.
func TestAnUnreadableReleaseRecordStillProducesAnArchive(t *testing.T) {
	h := newHarness(t)
	h.install()

	require.NoError(t, os.WriteFile(h.Paths.CurrentReleaseFile(),
		[]byte("{not json at all"), 0o600))

	report, err := ops.SupportBundle(context.Background(), h.Deps,
		ops.SupportOptions{Dir: t.TempDir()})
	require.NoError(t, err, "an unreadable release record cost the whole archive")

	entries := archiveEntries(t, report.Path)
	require.Contains(t, entries, "installation.yaml")
	require.Contains(t, entries, "journal.jsonl")

	var reason string
	for _, o := range report.Omitted {
		if o.Name == "manifest.yaml" {
			reason = o.Reason
		}
	}
	require.NotEmpty(t, reason)
	require.NotContains(t, reason, "no release is installed",
		"a record that would not parse was reported as no release at all")
}
