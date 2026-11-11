package suite

import (
	"bufio"
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gominisign "github.com/jedisct1/go-minisign"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/managerfeed/signed"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	infraexec "github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/infra/lock"
	"github.com/morzecrew/morzer/internal/infra/state"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// managerScript stands in for a manager binary: it answers `version --json`
// as the release it claims to be. Run with `linger`, it says so and waits
// first, which is a unit's process still running when the swap happens.
func managerScript(reports string) []byte {
	return fmt.Appendf(nil, "#!/bin/sh\nif [ \"$1\" = linger ]; then echo started; sleep 1; fi\n"+
		"echo '{\"data\":{\"version\":\"%s\"}}'\n", reports)
}

// publishManager writes what the release pipeline publishes for version: this
// platform's archive holding a binary that reports `reports`, SHA256SUMS with
// its line and one for an architecture nothing runs, and sk's signature over that.
func publishManager(t *testing.T, sk gominisign.PrivateKey, version, reports string) string {
	t.Helper()
	dir, bin := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "morzer"), managerScript(reports), 0o755)) //nolint:gosec // an executable stand-in
	archive := ports.ManagerArchive(domain.MustParseVersion(version))
	require.NoError(t, atomicfs.WriteTarZst(filepath.Join(dir, archive), bin, []string{"morzer"}, time.Unix(0, 0)))
	digest, err := atomicfs.DigestFile(filepath.Join(dir, archive))
	require.NoError(t, err)
	writeManagerSums(t, sk, dir, strings.TrimPrefix(digest, "sha256:")+"  "+archive+"\n"+
		strings.Repeat("0", 64)+"  morzer_"+version+"_linux_nonesuch.tar.zst\n")
	return dir
}

func writeManagerSums(t *testing.T, sk gominisign.PrivateKey, dir, sums string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ports.SumsFileName), []byte(sums), 0o600))
	sig, err := sk.Sign([]byte(sums), gominisign.SignOptions{})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ports.SignatureFileName), sig.Encode(), 0o600))
}

// selfUpdateHost is a machine with two installations, a manager binary of
// its own, and a feed whose newest release, 0.13.0, is signed by the key the
// unpacker trusts in place of the release key.
type selfUpdateHost struct {
	*host
	sk     gominisign.PrivateKey
	feed   *fakeManagerFeed
	binDir string
}

func newSelfUpdateHost(t *testing.T, running string) *selfUpdateHost {
	t.Helper()
	m := newHost(t, "demo", "other")
	sk, key := minisignKeypair(t)

	bin := filepath.Join(t.TempDir(), "bin")
	require.NoError(t, os.MkdirAll(bin, 0o755))
	path := filepath.Join(bin, "morzer")
	require.NoError(t, os.WriteFile(path, managerScript(running), 0o755)) //nolint:gosec // an executable stand-in
	resolved, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)

	feed := &fakeManagerFeed{newest: "0.13.0", dir: publishManager(t, sk, "0.13.0", "0.13.0")}
	m.Deps.ManagerFeed = feed
	m.Deps.ManagerUnpacker = signed.ForKey(key, infraexec.New())
	m.Deps.ManagerPath = resolved
	m.Deps.ManagerVersion = domain.MustParseVersion(running)
	m.Deps.LockerFor = func(p domain.Paths) ports.Locker { return lock.New(p.LockDir()) }
	return &selfUpdateHost{host: m, sk: sk, feed: feed, binDir: filepath.Dir(resolved)}
}

// replacedWhileLocking puts another manager at the path as the locks are
// taken, which is another self-update finishing first.
func (s *selfUpdateHost) replacedWhileLocking(t *testing.T, reports string) {
	t.Helper()
	next := s.Deps.LockerFor
	s.Deps.LockerFor = func(p domain.Paths) ports.Locker {
		require.NoError(t, os.WriteFile(s.Deps.ManagerPath, managerScript(reports), 0o755)) //nolint:gosec // the stand-in binary
		return next(p)
	}
}

// reports is what the binary at the manager's path says it is.
func (s *selfUpdateHost) reports(t *testing.T) string {
	t.Helper()
	v, err := s.Deps.ManagerUnpacker.Version(context.Background(), s.Deps.ManagerPath)
	require.NoError(t, err)
	return v.String()
}

func (s *selfUpdateHost) managerUpdates(t *testing.T, product string) []domain.OperationRecord {
	t.Helper()
	recs, err := state.New(domain.PathsUnder(s.Root, product)).Operations(context.Background(),
		ports.Filter{Type: domain.OpTypeManagerUpdate})
	require.NoError(t, err)
	return recs
}

// leftovers is anything self-update staged beside the binary and left there.
func (s *selfUpdateHost) leftovers(t *testing.T) []string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join(s.binDir, ".morzer.self-update.*"))
	require.NoError(t, err)
	return found
}

// The binary at the path becomes the newest release, verified and run first,
// with every installation's journal saying so; a process started before the
// swap finishes on the file it started from (RFC 0046 §5.3, §5.4).
func TestSelfUpdateReplacesTheManagerAndJournalsEveryInstallation(t *testing.T) {
	s := newSelfUpdateHost(t, "0.12.0")
	ctx := context.Background()

	lingering := osexec.Command(s.Deps.ManagerPath, "linger") //nolint:gosec // the stand-in binary
	stdout, err := lingering.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, lingering.Start())
	lines := bufio.NewScanner(stdout)
	require.True(t, lines.Scan())
	require.Equal(t, "started", lines.Text(), "the lingering process never opened the old binary")

	res, err := ops.SelfUpdate(ctx, s.Deps, ops.SelfUpdateOptions{})
	require.NoError(t, err)
	assert.True(t, res.Updated)
	assert.Equal(t, "0.12.0", res.From)
	assert.Equal(t, "0.13.0", res.To)
	assert.Equal(t, []string{"demo", "other"}, res.Installations)
	assert.Equal(t, "https://github.com/morzecrew/morzer/releases/tag/v0.13.0", res.URL)
	assert.Empty(t, res.Warnings)
	assert.Equal(t, "0.13.0", s.reports(t))
	assert.Empty(t, s.leftovers(t))

	require.True(t, lines.Scan())
	assert.Contains(t, lines.Text(), `"0.12.0"`, "a process started before the swap did not finish on the old file")
	require.NoError(t, lingering.Wait())

	for _, product := range []string{"demo", "other"} {
		recs := s.managerUpdates(t, product)
		require.Len(t, recs, 1, product)
		assert.Equal(t, domain.StatusSucceeded, recs[0].Status)
		assert.Equal(t, "0.12.0", recs[0].From.String())
		assert.Equal(t, "0.13.0", recs[0].To.String())
		assert.Equal(t, "inst_"+product, recs[0].InstallationID)
		assert.Equal(t, s.Deps.ManagerPath, recs[0].Flags["path"])

		// Released: the next operation takes them.
		locker := lock.New(domain.PathsUnder(s.Root, product).LockDir())
		for _, name := range []string{"deployment", "notify-outbox"} {
			_, held, err := locker.Owner(ctx, name)
			require.NoError(t, err)
			assert.False(t, held, "%s's %s lock was left held", product, name)
		}
	}
}

// Every refusal leaves the binary, the journals and the binary's directory as
// they were.
func TestSelfUpdateRefusesWhatItCannotTrust(t *testing.T) {
	holdLock := func(t *testing.T, s *selfUpdateHost, name string) {
		release, err := lock.New(domain.PathsUnder(s.Root, "other").LockDir()).Acquire(context.Background(), name,
			ports.LockOptions{Owner: ports.LockOwner{PID: os.Getpid(), Type: "doctor", OperationID: "op_held"}})
		require.NoError(t, err)
		t.Cleanup(func() { _ = release() })
	}

	cases := map[string]struct {
		running string
		setup   func(t *testing.T, s *selfUpdateHost, opts *ops.SelfUpdateOptions)
		want    string
		after   string
	}{
		"a signature by another key": {setup: func(t *testing.T, s *selfUpdateHost, _ *ops.SelfUpdateOptions) {
			other, _ := minisignKeypair(t)
			s.feed.dir = publishManager(t, other, "0.13.0", "0.13.0")
		}, want: "not signed by the key this manager carries"},
		"no signature": {setup: func(t *testing.T, s *selfUpdateHost, opts *ops.SelfUpdateOptions) {
			require.NoError(t, os.Remove(filepath.Join(s.feed.dir, ports.SignatureFileName)))
			opts.From = s.feed.dir
		}, want: "cannot read the signature"},
		"an archive that does not match its line": {setup: func(t *testing.T, s *selfUpdateHost, _ *ops.SelfUpdateOptions) {
			f, err := os.OpenFile(filepath.Join(s.feed.dir, ports.ManagerArchive(domain.MustParseVersion("0.13.0"))), os.O_APPEND|os.O_WRONLY, 0)
			require.NoError(t, err)
			_, err = f.WriteString("tampered")
			require.NoError(t, err)
			require.NoError(t, f.Close())
		}, want: "does not match its line"},
		"an archive with no line": {setup: func(t *testing.T, s *selfUpdateHost, _ *ops.SelfUpdateOptions) {
			writeManagerSums(t, s.sk, s.feed.dir, strings.Repeat("0", 64)+"  morzer_0.13.0_linux_nonesuch.tar.zst\n")
		}, want: "has no line for"},
		"a binary that is not the release it was fetched as": {setup: func(t *testing.T, s *selfUpdateHost, _ *ops.SelfUpdateOptions) {
			s.feed.dir = publishManager(t, s.sk, "0.13.0", "0.13.1")
		}, want: "reports 0.13.1, not 0.13.0"},
		"an older release": {setup: func(_ *testing.T, _ *selfUpdateHost, opts *ops.SelfUpdateOptions) {
			opts.To = "0.11.0"
		}, want: "older than this manager"},
		"a dev build that names no release": {running: "0.0.0", want: "dev build"},
		"a manager that cannot find itself": {setup: func(_ *testing.T, s *selfUpdateHost, _ *ops.SelfUpdateOptions) {
			s.Deps.ManagerPath = "morzer"
		}, want: "cannot tell which file this manager runs from"},
		"a held deployment lock": {setup: func(t *testing.T, s *selfUpdateHost, _ *ops.SelfUpdateOptions) {
			holdLock(t, s, "deployment")
		}, want: "while other is busy"},
		"another self-update in the same directory": {setup: func(t *testing.T, s *selfUpdateHost, _ *ops.SelfUpdateOptions) {
			release, err := lock.Directory(s.binDir)
			require.NoError(t, err)
			t.Cleanup(func() { _ = release() })
		}, want: "another self-update is replacing"},
		"an archive whose version carries build metadata": {setup: func(t *testing.T, s *selfUpdateHost, opts *ops.SelfUpdateOptions) {
			opts.From = publishManager(t, s.sk, "0.13.0+rebuild", "0.13.0")
		}, want: "names no morzer archive"},
		"a SHA256SUMS larger than any release's": {setup: func(t *testing.T, s *selfUpdateHost, opts *ops.SelfUpdateOptions) {
			sums, err := os.ReadFile(filepath.Join(s.feed.dir, ports.SumsFileName))
			require.NoError(t, err)
			writeManagerSums(t, s.sk, s.feed.dir, string(sums)+strings.Repeat("#\n", 1<<19+1))
			opts.From = s.feed.dir
		}, want: "larger than"},
		"a signature file larger than any release's": {setup: func(t *testing.T, s *selfUpdateHost, opts *ops.SelfUpdateOptions) {
			sig := filepath.Join(s.feed.dir, ports.SignatureFileName)
			f, err := os.OpenFile(sig, os.O_APPEND|os.O_WRONLY, 0)
			require.NoError(t, err)
			_, err = f.WriteString(strings.Repeat("x", 1<<20))
			require.NoError(t, err)
			require.NoError(t, f.Close())
			opts.From = s.feed.dir
		}, want: "larger than"},
		"a build with no feed": {setup: func(_ *testing.T, s *selfUpdateHost, opts *ops.SelfUpdateOptions) {
			s.Deps.ManagerFeed = nil
			opts.To = "0.13.0"
		}, want: "no release feed"},
		"a held outbox lock": {setup: func(t *testing.T, s *selfUpdateHost, _ *ops.SelfUpdateOptions) {
			holdLock(t, s, "notify-outbox")
		}, want: "while other is busy"},
		"locks this user cannot take": {setup: func(t *testing.T, s *selfUpdateHost, _ *ops.SelfUpdateOptions) {
			if os.Geteuid() == 0 {
				t.Skip("root takes any lock")
			}
			// An installation's locks are root's, and this is a user
			// running a copy of their own.
			locks := domain.PathsUnder(s.Deps.Paths.Root(), "other").LockDir()
			require.NoError(t, os.MkdirAll(locks, 0o750))
			require.NoError(t, os.Chmod(locks, 0o500))
			t.Cleanup(func() { _ = os.Chmod(locks, 0o750) })
		}, want: "do not run this copy with sudo"},
		"a newer manager installed meanwhile": {setup: func(t *testing.T, s *selfUpdateHost, _ *ops.SelfUpdateOptions) {
			s.replacedWhileLocking(t, "0.13.0")
		}, want: "another self-update installed", after: "0.13.0"},
		"a dev build replaced meanwhile": {running: "0.0.0", setup: func(t *testing.T, s *selfUpdateHost, opts *ops.SelfUpdateOptions) {
			opts.To = "0.13.0"
			s.replacedWhileLocking(t, "0.12.5")
		}, want: "changed since this command started", after: "0.12.5"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			running := tc.running
			if running == "" {
				running = "0.12.0"
			}
			s := newSelfUpdateHost(t, running)
			path := s.Deps.ManagerPath
			before, err := atomicfs.DigestFile(path)
			require.NoError(t, err)
			var opts ops.SelfUpdateOptions
			if tc.setup != nil {
				tc.setup(t, s, &opts)
			}

			_, err = ops.SelfUpdate(context.Background(), s.Deps, opts)
			require.Error(t, err)
			e := domain.AsError(err)
			assert.Contains(t, e.Message+" "+e.Hint, tc.want)

			if tc.after == "" {
				after, err := atomicfs.DigestFile(path)
				require.NoError(t, err)
				assert.Equal(t, before, after, "a refused self-update changed the binary")
			}
			assert.Empty(t, s.leftovers(t))
			assert.Empty(t, s.managerUpdates(t, "demo"))
		})
	}
}

// A release installed by another self-update while this one waited is what
// the swap replaced, and the journal says so rather than naming the version
// this command started as.
func TestSelfUpdateRecordsTheVersionItActuallyReplaced(t *testing.T) {
	s := newSelfUpdateHost(t, "0.12.0")
	s.replacedWhileLocking(t, "0.12.5")

	res, err := ops.SelfUpdate(context.Background(), s.Deps, ops.SelfUpdateOptions{})
	require.NoError(t, err)
	assert.Equal(t, "0.12.5", res.From)
	assert.Equal(t, "0.13.0", s.reports(t))
	recs := s.managerUpdates(t, "demo")
	require.Len(t, recs, 1)
	assert.Equal(t, "0.12.5", recs[0].From.String())
}

// replacingUnpacker puts another manager at the path while the release is
// being resolved, which is another self-update finishing before this one has
// looked at anything.
type replacingUnpacker struct {
	ports.ManagerUnpacker
	path, reports string
}

func (u replacingUnpacker) Target(dir string) (domain.Version, error) {
	if err := os.WriteFile(u.path, managerScript(u.reports), 0o755); err != nil { //nolint:gosec // the stand-in binary
		return domain.Version{}, err
	}
	return u.ManagerUnpacker.Target(dir)
}

// A dev build is held to the file it was before any of the command's work,
// so a binary replaced while the release was resolved is not blessed as the
// one this command started from (decision 12).
func TestADevBuildIsTheFileItWasBeforeAnything(t *testing.T) {
	s := newSelfUpdateHost(t, "0.0.0")
	s.Deps.ManagerUnpacker = replacingUnpacker{s.Deps.ManagerUnpacker, s.Deps.ManagerPath, "0.12.5"}

	_, err := ops.SelfUpdate(context.Background(), s.Deps, ops.SelfUpdateOptions{From: s.feed.dir})
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Message, "changed since this command started")
	assert.Equal(t, "0.12.5", s.reports(t))
}

// A directory this user cannot write is refused with the command to run
// instead, and nothing is escalated (decision 9).
func TestSelfUpdateNeverEscalates(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes a 0555 directory, so this arrangement proves nothing")
	}
	s := newSelfUpdateHost(t, "0.12.0")
	dir := filepath.Dir(s.Deps.ManagerPath)
	require.NoError(t, os.Chmod(dir, 0o555)) //nolint:gosec // the point is a directory nobody may write
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	_, err := ops.SelfUpdate(context.Background(), s.Deps, ops.SelfUpdateOptions{To: "0.13.0"})
	require.Error(t, err)
	e := domain.AsError(err)
	assert.Contains(t, e.Message, "is not writable by this user")
	// This copy is one its user can replace, so sudo on it would run as
	// root whatever they put there: the hint installs one for root instead.
	assert.NotContains(t, e.Hint, "sudo "+s.Deps.ManagerPath)
	assert.Contains(t, e.Hint, "| sudo sh -s -- --version 0.13.0")
	assert.Equal(t, "0.12.0", s.reports(t))

	// A copy only root can replace is the one to re-run with sudo, keeping
	// the root this ran under, so it locks the same installations.
	s.Deps.ReplaceableByNonRoot = func(string) (string, error) { return "", nil }
	_, err = ops.SelfUpdate(context.Background(), s.Deps, ops.SelfUpdateOptions{To: "0.13.0"})
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Hint, "sudo "+s.Deps.ManagerPath+" self-update --root "+s.Deps.Paths.Root()+" --to 0.13.0")

	// Following the hint for a dry run must not perform the swap, and it
	// keeps the deadline it was given.
	_, err = ops.SelfUpdate(context.Background(), s.Deps, ops.SelfUpdateOptions{DryRun: true, Timeout: 90 * time.Second})
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Hint, "self-update --root "+s.Deps.Paths.Root()+" --dry-run --timeout 1m30s")
}

// Nothing newer is not a failure, and nothing is replaced.
func TestSelfUpdateWithNothingNewerReplacesNothing(t *testing.T) {
	cases := map[string]struct {
		running, to string
	}{
		"the newest":           {running: "0.13.0"},
		"ahead of the newest":  {running: "0.14.0-rc.1"},
		"the release it names": {running: "0.13.0", to: "0.13.0"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := newSelfUpdateHost(t, tc.running)
			res, err := ops.SelfUpdate(context.Background(), s.Deps, ops.SelfUpdateOptions{To: tc.to})
			require.NoError(t, err)
			assert.False(t, res.Updated)
			assert.Equal(t, tc.running, s.reports(t))
			assert.Empty(t, s.managerUpdates(t, "demo"))
		})
	}
}

// --from reads the three files and asks nobody (decision 14), and a dev build
// updates once a release is named (decision 12).
func TestSelfUpdateFromADirectoryAsksNobody(t *testing.T) {
	s := newSelfUpdateHost(t, "0.0.0")
	s.feed.err = domain.ValidationError(nil, "cannot reach the release feed")

	_, err := ops.SelfUpdate(context.Background(), s.Deps, ops.SelfUpdateOptions{From: s.feed.dir, To: "0.13.1"})
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Message, "--to 0.13.1 does not match 0.13.0")

	res, err := ops.SelfUpdate(context.Background(), s.Deps, ops.SelfUpdateOptions{From: s.feed.dir})
	require.NoError(t, err)
	assert.True(t, res.Updated)
	assert.True(t, res.DevBuild)
	assert.Equal(t, "0.13.0", s.reports(t))
	assert.Zero(t, s.feed.asked, "--from asked the feed")
}

// A dry run verifies and runs the new binary and stops before the locks: the
// binary and the journals stay as they were.
func TestSelfUpdateDryRunReplacesNothing(t *testing.T) {
	s := newSelfUpdateHost(t, "0.12.0")
	holdLock := lock.New(domain.PathsUnder(s.Root, "other").LockDir())
	release, err := holdLock.Acquire(context.Background(), "deployment", ports.LockOptions{})
	require.NoError(t, err)
	defer func() { _ = release() }()

	res, err := ops.SelfUpdate(context.Background(), s.Deps, ops.SelfUpdateOptions{DryRun: true})
	require.NoError(t, err, "a dry run took a lock")
	assert.False(t, res.Updated)
	assert.Equal(t, "0.13.0", res.To)
	assert.Equal(t, []string{"demo", "other"}, res.Installations)
	assert.Equal(t, "0.12.0", s.reports(t))
	assert.Empty(t, s.leftovers(t))
	assert.Empty(t, s.managerUpdates(t, "other"))

	other, _ := minisignKeypair(t)
	s.feed.dir = publishManager(t, other, "0.13.0", "0.13.0")
	_, err = ops.SelfUpdate(context.Background(), s.Deps, ops.SelfUpdateOptions{DryRun: true})
	require.Error(t, err, "a dry run did not verify the signature")
}

// Units that run another copy of the manager stay on it, and the operator is
// told which (decision 13).
func TestSelfUpdateNamesUnitsThatRunAnotherCopy(t *testing.T) {
	s := newSelfUpdateHost(t, "0.12.0")
	s.Supervisor.ManagerPaths["demo"] = []string{s.Deps.ManagerPath}
	s.Supervisor.ManagerPaths["other"] = []string{s.Deps.ManagerPath, "/opt/elsewhere/morzer"}

	res, err := ops.SelfUpdate(context.Background(), s.Deps, ops.SelfUpdateOptions{})
	require.NoError(t, err)
	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0], "other's units run /opt/elsewhere/morzer")
}

// The key a self-update trusts is the repository's, byte for byte: a copy that
// drifted would refuse every release, and one replaced would trust the wrong
// one (decision 6).
func TestTheEmbeddedReleaseKeyIsTheRepositorys(t *testing.T) {
	want, err := os.ReadFile("../../morzer.pub")
	require.NoError(t, err)
	assert.Equal(t, string(want), signed.ReleaseKey())
}
