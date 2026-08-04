package hooks_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/adapters/hooks"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// Hooks are the only way to add product logic without changing the manager, so
// this ABI is a public contract: a documented environment, a structured result
// channel that is not stdout, three exit-code meanings, and a refusal for
// anything a release got wrong.
//
// These run real scripts. A hook runner tested against a mocked process tests
// the mock, and the interesting parts here -- fd 3, the process group, the
// executable bit -- have no meaning without a process.

func release(t *testing.T) domain.Release {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	return domain.Release{
		Root: root,
		Manifest: domain.Manifest{
			Metadata: domain.Metadata{Name: "demo", Version: domain.MustParseVersion("1.2.0")},
		},
	}
}

func hook(t *testing.T, rel domain.Release, name, body string) []string {
	t.Helper()
	path := filepath.Join(rel.Root, "hooks", name)
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return []string{"hooks/" + name}
}

// The environment is built by the caller, not derived from the release: the
// runner is handed both, and the lifecycle layer is what fills this in.
func env() ports.HookEnv {
	return ports.HookEnv{
		Product:        "demo",
		InstallationID: "inst-1",
		Phase:          ports.PhaseMigrate,
		ReleaseVersion: domain.MustParseVersion("1.2.0"),
	}
}

func run(t *testing.T, rel domain.Release, command []string) (ports.HookOutcome, error) {
	t.Helper()
	return hooks.NewRunner(exec.New()).
		Run(context.Background(), rel, command, env(), 30*time.Second)
}

func TestAHookRunsFromTheReleaseRootAndSeesItsEnvironment(t *testing.T) {
	rel := release(t)
	cmd := hook(t, rel, "migrate", `#!/bin/sh
echo "cwd=$(pwd)"
echo "phase=$DEMO_PHASE version=$DEMO_RELEASE_VERSION id=$DEMO_INSTALLATION_ID"
echo "dry=$DEMO_DRY_RUN fd=$DEMO_RESULT_FD"
`)

	out, err := run(t, rel, cmd)
	if err != nil {
		t.Fatalf("a hook that exits zero reported a failure: %v", err)
	}
	if out.ExitCode != 0 {
		t.Errorf("exit code %d", out.ExitCode)
	}

	// The working directory is the release root, so a hook can use paths
	// relative to its own files.
	if !strings.Contains(out.Stdout, "cwd="+rel.Root) {
		t.Errorf("the hook did not run from the release root:\n%s", out.Stdout)
	}
	for _, want := range []string{"phase=migrate", "version=1.2.0", "id=inst-1", "dry=0", "fd=3"} {
		if !strings.Contains(out.Stdout, want) {
			t.Errorf("the ABI variable %q did not reach the hook:\n%s", want, out.Stdout)
		}
	}
}

// TestDryRunIsAlwaysPresent. A hook testing for the variable's existence rather
// than its value would otherwise mutate during a plan.
func TestDryRunIsAlwaysPresent(t *testing.T) {
	rel := release(t)
	cmd := hook(t, rel, "migrate", `#!/bin/sh
if [ -z "${DEMO_DRY_RUN+set}" ]; then echo "UNSET"; else echo "VALUE=$DEMO_DRY_RUN"; fi
`)

	e := env()
	e.DryRun = true
	out, err := hooks.NewRunner(exec.New()).
		Run(context.Background(), rel, cmd, e, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Stdout, "VALUE=1") {
		t.Errorf("a dry run did not tell the hook so:\n%s", out.Stdout)
	}
}

// TestAHookReportsThroughFileDescriptorThree, not stdout: stdout goes to the
// log and the live view, and a hook whose logging was constrained by the
// manager's parsing would be one nobody could debug.
func TestAHookReportsThroughFileDescriptorThree(t *testing.T) {
	rel := release(t)
	cmd := hook(t, rel, "migrate", `#!/bin/sh
echo "this is human output, and is not JSON"
printf '{"message":"applied 3 migrations","schema_version":12,"artifacts":[{"name":"dump","path":"db.sql","size":42}]}' >&3
`)

	out, err := run(t, rel, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if out.Result.Message != "applied 3 migrations" {
		t.Errorf("Result.Message = %q", out.Result.Message)
	}
	if out.Result.SchemaVersion != 12 {
		t.Errorf("SchemaVersion = %d; rollback needs the schema the hook left behind",
			out.Result.SchemaVersion)
	}
	if len(out.Result.Artifacts) != 1 || out.Result.Artifacts[0].Path != "db.sql" {
		t.Errorf("the artifacts were lost: %+v", out.Result.Artifacts)
	}
	if !strings.Contains(out.Stdout, "human output") {
		t.Error("the hook's own output did not reach the log")
	}
}

// TestAHookThatSaysNothingIsNotInError is the common case: most hooks just do
// work and exit.
func TestAHookThatSaysNothingIsNotInError(t *testing.T) {
	rel := release(t)
	out, err := run(t, rel, hook(t, rel, "migrate", "#!/bin/sh\nexit 0\n"))
	if err != nil {
		t.Fatalf("a silent hook was treated as broken: %v", err)
	}
	if out.Result.Message != "" || out.Skipped {
		t.Errorf("a silent hook produced a result out of nothing: %+v", out.Result)
	}
}

// TestGarbageOnFileDescriptorThreeIsIgnored. A hook that writes something
// unparseable has still done its work; refusing the operation over it would
// make the result channel a liability rather than a convenience.
func TestGarbageOnFileDescriptorThreeIsIgnored(t *testing.T) {
	rel := release(t)
	cmd := hook(t, rel, "migrate", `#!/bin/sh
printf 'this is definitely not json' >&3
exit 0
`)

	out, err := run(t, rel, cmd)
	if err != nil {
		t.Fatalf("unparseable result output failed the hook: %v", err)
	}
	if out.Result.Message != "" {
		t.Errorf("garbage was decoded into a result: %+v", out.Result)
	}
}

// TestExitTwoMeansNothingToDo is the one non-zero exit the ABI gives a meaning
// to, so `apply` can report "migrations: nothing to run" rather than implying
// work happened.
func TestExitTwoMeansNothingToDo(t *testing.T) {
	rel := release(t)
	cmd := hook(t, rel, "migrate", `#!/bin/sh
echo "schema is already current"
exit 2
`)

	out, err := run(t, rel, cmd)
	if err != nil {
		t.Fatalf("exit 2 was treated as a failure: %v", err)
	}
	if !out.Skipped {
		t.Error("exit 2 did not set Skipped, so the operator is told work happened")
	}
	if out.ExitCode != 2 {
		t.Errorf("exit code %d", out.ExitCode)
	}
}

// TestAHookCanReportSkippedWhileExitingZero: the other half of the same
// contract, for a hook that would rather not use an exit code for it.
func TestAHookCanReportSkippedWhileExitingZero(t *testing.T) {
	rel := release(t)
	cmd := hook(t, rel, "migrate", `#!/bin/sh
printf '{"skipped":true,"message":"nothing to do"}' >&3
exit 0
`)

	out, err := run(t, rel, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Skipped {
		t.Error("a hook that reported skipped was recorded as having done work")
	}
}

// TestAFailingHookQuotesWhatItSaid. The last thing a failing script prints is
// almost always the reason it failed, and an operator should not have to open
// the log for it.
func TestAFailingHookQuotesWhatItSaid(t *testing.T) {
	rel := release(t)
	cmd := hook(t, rel, "migrate", `#!/bin/sh
echo "connecting" >&2
echo "applying 0003_add_index" >&2
echo "ERROR: relation already exists" >&2
exit 1
`)

	out, err := run(t, rel, cmd)
	if err == nil {
		t.Fatal("a hook that exited 1 was reported successful")
	}
	if out.ExitCode != 1 {
		t.Errorf("exit code %d", out.ExitCode)
	}

	de := domain.AsError(err)
	if !strings.Contains(de.Message, "hooks/migrate") {
		t.Errorf("the failure does not name the hook: %q", de.Message)
	}
	if !strings.Contains(de.Message, "exit code 1") {
		t.Errorf("the failure does not give the exit code: %q", de.Message)
	}
	if !strings.Contains(de.Message, "relation already exists") {
		t.Errorf("the hook's own diagnostic was dropped: %q", de.Message)
	}
	if de.Hint == "" {
		t.Error("the operator is not told where the full output went")
	}
}

// TestAFailingHookPrefersItsStructuredMessage over scraping stderr, because a
// hook that took the trouble to say what went wrong should be believed.
func TestAFailingHookPrefersItsStructuredMessage(t *testing.T) {
	rel := release(t)
	cmd := hook(t, rel, "migrate", `#!/bin/sh
echo "a wall of noise nobody wants in an error message" >&2
printf '{"message":"the database refused the connection"}' >&3
exit 1
`)

	_, err := run(t, rel, cmd)
	if err == nil {
		t.Fatal("a failing hook was reported successful")
	}
	msg := domain.AsError(err).Message
	if !strings.Contains(msg, "refused the connection") {
		t.Errorf("the hook's own summary was ignored: %q", msg)
	}
	if strings.Contains(msg, "wall of noise") {
		t.Errorf("stderr was scraped over an explicit message: %q", msg)
	}
}

// TestAFailingSilentHookFallsBackToStdout, because a script that only prints
// to stdout is common and "failed with exit code 1" alone is useless.
func TestAFailingSilentHookFallsBackToStdout(t *testing.T) {
	rel := release(t)
	cmd := hook(t, rel, "migrate", `#!/bin/sh
echo "could not reach the database"
exit 1
`)

	_, err := run(t, rel, cmd)
	if err == nil {
		t.Fatal("a failing hook was reported successful")
	}
	if !strings.Contains(domain.AsError(err).Message, "could not reach the database") {
		t.Errorf("stdout was not used when stderr was empty: %q", domain.AsError(err).Message)
	}
}

func TestAFailingHookThatSaysNothingAtAllStillReportsTheCode(t *testing.T) {
	rel := release(t)
	_, err := run(t, rel, hook(t, rel, "migrate", "#!/bin/sh\nexit 9\n"))
	if err == nil {
		t.Fatal("a failing hook was reported successful")
	}
	if !strings.Contains(domain.AsError(err).Message, "exit code 9") {
		t.Errorf("the message says nothing usable: %q", domain.AsError(err).Message)
	}
}

// TestOnlyTheLastLinesOfAChattyFailureAreQuoted keeps one verbose migration
// from filling the terminal with its own log.
func TestOnlyTheLastLinesOfAChattyFailureAreQuoted(t *testing.T) {
	rel := release(t)
	cmd := hook(t, rel, "migrate", `#!/bin/sh
i=0
while [ $i -lt 200 ]; do echo "step $i" >&2; i=$((i+1)); done
echo "FINAL: out of disk" >&2
exit 1
`)

	_, err := run(t, rel, cmd)
	if err == nil {
		t.Fatal("a failing hook was reported successful")
	}
	msg := domain.AsError(err).Message
	if !strings.Contains(msg, "out of disk") {
		t.Errorf("the last thing the hook said was dropped: %q", msg)
	}
	if strings.Contains(msg, "step 5") {
		t.Errorf("the whole log was quoted into the error: %q", msg)
	}
}

// The refusals. Each is a broken bundle, and each has to say which kind.

func TestAHookThatDoesNotExistIsABrokenBundle(t *testing.T) {
	rel := release(t)

	_, err := run(t, rel, []string{"hooks/never-shipped"})
	if err == nil {
		t.Fatal("a declared-but-missing hook ran")
	}
	de := domain.AsError(err)
	if de.Code != domain.CodeUsage {
		t.Errorf("code = %v, want the usage code: the bundle is wrong, not the "+
			"machine, and the exit status has to say so", de.Code)
	}
	if !strings.Contains(de.Message, "never-shipped") {
		t.Errorf("the refusal does not name the hook: %q", de.Message)
	}
	if !strings.Contains(de.Hint, "broken bundle") {
		t.Errorf("hint %q does not say whose fault it is", de.Hint)
	}
}

func TestAHookWithoutTheExecutableBitIsRefused(t *testing.T) {
	rel := release(t)
	path := filepath.Join(rel.Root, "hooks", "migrate")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := run(t, rel, []string{"hooks/migrate"})
	if err == nil {
		t.Fatal("a non-executable hook ran")
	}
	de := domain.AsError(err)
	if !strings.Contains(de.Message, "not executable") {
		t.Errorf("the refusal does not say what is wrong: %q", de.Message)
	}
	if !strings.Contains(de.Message, "0644") {
		t.Errorf("the refusal does not give the mode, which is the whole "+
			"diagnostic: %q", de.Message)
	}
	if !strings.Contains(de.Hint, "chmod +x") {
		t.Errorf("hint %q does not say how to fix it", de.Hint)
	}
}

// TestAHookPathCannotEscapeTheRelease. The manifest is release-supplied input;
// a path escaping the root would let a bundle execute arbitrary host files.
func TestAHookPathCannotEscapeTheRelease(t *testing.T) {
	rel := release(t)

	for _, path := range []string{"../../../bin/sh", "/bin/sh", "hooks/../../../../bin/sh"} {
		t.Run(path, func(t *testing.T) {
			if _, err := run(t, rel, []string{path}); err == nil {
				t.Errorf("a hook path escaped the release root: %q", path)
			}
		})
	}
}

func TestAHookInvokedWithNoCommandIsAnInternalError(t *testing.T) {
	rel := release(t)

	_, err := run(t, rel, nil)
	if err == nil {
		t.Fatal("a hook with no command ran")
	}
	// Internal, not validation: the manifest loader rejects an empty
	// command, so reaching here means the manager itself is wrong.
	if domain.AsError(err).Code != domain.CodeInternal {
		t.Errorf("code = %v, want internal", domain.AsError(err).Code)
	}
}

// TestAHookThatHangsIsKilled. Without this an `apply` waits forever on a
// migration that is never going to finish.
func TestAHookThatHangsIsKilled(t *testing.T) {
	rel := release(t)
	cmd := hook(t, rel, "migrate", `#!/bin/sh
sleep 300
`)

	start := time.Now()
	_, err := hooks.NewRunner(exec.New()).
		Run(context.Background(), rel, cmd, env(), 500*time.Millisecond)

	if err == nil {
		t.Fatal("a hook that never finished was reported successful")
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("the timeout took %s to fire", elapsed)
	}
}

// TestATimeoutReachesTheWholeProcessGroup: a hook that backgrounds work and
// exits would otherwise leave the child running past the operation.
func TestATimeoutReachesTheWholeProcessGroup(t *testing.T) {
	rel := release(t)
	marker := filepath.Join(t.TempDir(), "still-alive")
	cmd := hook(t, rel, "migrate", `#!/bin/sh
sh -c 'sleep 2; echo yes > `+marker+`' &
sleep 300
`)

	_, err := hooks.NewRunner(exec.New()).
		Run(context.Background(), rel, cmd, env(), 300*time.Millisecond)
	if err == nil {
		t.Fatal("a hanging hook was reported successful")
	}

	// If the group was killed, the backgrounded child never got to write.
	time.Sleep(3 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Error("a process the hook backgrounded survived the timeout, so a " +
			"killed operation leaves work running behind it")
	}
}

// TestAHookThatFloodsTheResultChannelIsBounded. A broken hook must not take
// the manager's memory with it.
func TestAHookThatFloodsTheResultChannelIsBounded(t *testing.T) {
	rel := release(t)
	cmd := hook(t, rel, "migrate", `#!/bin/sh
head -c 4000000 /dev/zero | tr '\0' 'x' >&3 2>/dev/null || true
exit 0
`)

	out, err := hooks.NewRunner(exec.New()).
		Run(context.Background(), rel, cmd, env(), 60*time.Second)
	if err != nil {
		t.Fatalf("a hook that wrote too much to fd 3 failed the operation: %v", err)
	}
	// Unparseable, so no result -- the point is that it returned at all
	// rather than reading four megabytes into an error message.
	if out.Result.Message != "" {
		t.Errorf("four megabytes of x was decoded into a result: %+v", out.Result)
	}
}

// TestCancellingAnOperationStopsItsHook is what ctrl-c has to do.
func TestCancellingAnOperationStopsItsHook(t *testing.T) {
	rel := release(t)
	cmd := hook(t, rel, "migrate", "#!/bin/sh\nsleep 300\n")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := hooks.NewRunner(exec.New()).Run(ctx, rel, cmd, env(), time.Hour)
	if err == nil {
		t.Fatal("a cancelled hook reported success")
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("cancellation took %s", elapsed)
	}
}

// TestParametersReachAHookUnderTheDocumentedNames, the same names the Compose
// files interpolate, so a hook and a topology file refer to a port the same
// way.
func TestParametersReachAHookUnderTheDocumentedNames(t *testing.T) {
	rel := release(t)
	cmd := hook(t, rel, "migrate", `#!/bin/sh
echo "port=$DEMO_PARAM_HTTP_PORT level=$DEMO_PARAM_LOG_LEVEL"
`)

	e := env()
	e.Parameters = map[string]string{"http_port": "8443", "log_level": "debug"}
	out, err := hooks.NewRunner(exec.New()).
		Run(context.Background(), rel, cmd, e, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Stdout, "port=8443 level=debug") {
		t.Errorf("the parameters did not reach the hook under the documented "+
			"names:\n%s", out.Stdout)
	}
}

// TestSecretsAreScrubbedFromHookOutput. A hook that echoes a connection string
// must not put it in the log.
func TestSecretsAreScrubbedFromHookOutput(t *testing.T) {
	rel := release(t)
	const secret = "s3cr3t-database-password"
	cmd := hook(t, rel, "migrate", `#!/bin/sh
echo "connecting with `+secret+`"
echo "failed with `+secret+`" >&2
exit 1
`)

	out, err := hooks.NewRunner(exec.New(), hooks.WithRedaction([]string{secret})).
		Run(context.Background(), rel, cmd, env(), 30*time.Second)
	if err == nil {
		t.Fatal("the fixture was supposed to fail")
	}
	if strings.Contains(out.Stdout, secret) || strings.Contains(out.Stderr, secret) {
		t.Errorf("a secret survived in the captured output:\n%s\n%s", out.Stdout, out.Stderr)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("a secret survived into the error message: %v", err)
	}
}

// TestOutputIsForwardedLineByLine is what the live view subscribes to.
func TestOutputIsForwardedLineByLine(t *testing.T) {
	rel := release(t)
	cmd := hook(t, rel, "migrate", `#!/bin/sh
echo "first"
echo "second" >&2
echo "third"
`)

	var lines []string
	_, err := hooks.NewRunner(exec.New(), hooks.WithOutputSink(func(l exec.Line) {
		lines = append(lines, l.Text)
	})).Run(context.Background(), rel, cmd, env(), 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(lines, "|")
	for _, want := range []string{"first", "second", "third"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the live view never saw %q: %v", want, lines)
		}
	}
}
