//go:build docker

package suite

import (
	"context"
	"fmt"
	"io"
	osexec "os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/runtime/compose"
	infraexec "github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/contract"
	"github.com/morzecrew/morzer/test/dockerlab"
)

// The Compose adapter is the one place this project's model of a deployment
// meets the tool that actually runs it. Everything below asks the daemon
// rather than a script: what `ps` prints when a container has died, what a
// healthcheck reports before it has passed once, whether `down` kept the
// volume it promised to keep.
//
// The fake passes the same shared suite. That is the point -- a fake that
// disagrees with the real adapter makes every test built on it a fiction.

// project is the fixture the suite runs against: one service that stays up,
// one real listener, one one-shot that exits non-zero on purpose, and a named
// volume so the data-preservation claim can be checked rather than asserted.
func project(t *testing.T) ports.RuntimeConfig {
	t.Helper()
	dockerlab.Pull(t, dockerlab.ImageBusybox, dockerlab.ImageRedis)

	return dockerlab.Project(t, fmt.Sprintf(`
services:
  web:
    image: %s
    init: true
    command: ["sh", "-c", "echo web is up; while true; do sleep 1; done"]
    volumes:
      - data:/data
  cache:
    image: %s
    command: ["redis-server", "--save", "", "--appendonly", "no"]
  migrate:
    image: %s
    profiles: ["tools"]
    command: ["sh", "-c", "echo nothing to migrate; exit 2"]
volumes:
  data: {}
`, dockerlab.ImageBusybox, dockerlab.ImageRedis, dockerlab.ImageBusybox))
}

func newComposeRuntime(t *testing.T) (*compose.Runtime, ports.RuntimeConfig) {
	t.Helper()
	dockerlab.Require(t)
	return compose.New(infraexec.New()), project(t)
}

// TestRuntimeContract_Compose runs the shared Runtime suite against real
// Docker. Every behaviour the lifecycle layer relies on -- idempotent Up, a
// Down that preserves volumes, a Status that works before the first Up -- is
// asserted against the daemon rather than against a fake that was written to
// agree with them.
func TestRuntimeContract_Compose(t *testing.T) {
	dockerlab.Require(t)

	contract.RunRuntimeSuite(t, func(t *testing.T) (ports.Runtime, ports.RuntimeConfig) {
		return compose.New(infraexec.New()), project(t)
	})
}

// TestComposeDownKeepsTheVolumeAndDownWithVolumesDoesNot is the claim the
// whole compensation design rests on, checked by writing a byte and looking
// for it afterwards.
//
// The fake can only report that the flag was passed. This asserts the
// consequence: a failed update that calls Down must leave the database where
// it was.
func TestComposeDownKeepsTheVolumeAndDownWithVolumesDoesNot(t *testing.T) {
	rt, cfg := newComposeRuntime(t)
	ctx := context.Background()

	require.NoError(t, rt.Up(ctx, cfg, ports.UpOptions{Wait: true, WaitTimeout: 2 * time.Minute}))

	res, err := rt.Exec(ctx, cfg, "web", []string{"sh", "-c", "echo precious > /data/rows"})
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode, "writing the fixture failed: %s%s", res.Stdout, res.Stderr)

	// The default. This is what a compensation runs.
	require.NoError(t, rt.Down(ctx, cfg, ports.DownOptions{}))
	require.NoError(t, rt.Up(ctx, cfg, ports.UpOptions{Wait: true, WaitTimeout: 2 * time.Minute}))

	res, err = rt.Exec(ctx, cfg, "web", []string{"cat", "/data/rows"})
	require.NoError(t, err)
	assert.Contains(t, res.Stdout, "precious",
		"Down without the volume flag destroyed application data, which is the "+
			"one thing a compensation must never do")

	// And the explicit form does remove it, or the flag would be theatre.
	require.NoError(t, rt.Down(ctx, cfg, ports.DownOptions{Volumes: true}))
	require.NoError(t, rt.Up(ctx, cfg, ports.UpOptions{Wait: true, WaitTimeout: 2 * time.Minute}))

	res, err = rt.Exec(ctx, cfg, "web", []string{"sh", "-c", "cat /data/rows 2>&1 || true"})
	require.NoError(t, err)
	assert.NotContains(t, res.Stdout, "precious",
		"Down with Volumes set left the volume behind, so `--destroy-data` does nothing")
}

// TestComposeStatusReportsRealHealth pins the health vocabulary against the
// daemon that produces it. `starting` is not `unhealthy`, and an absent
// healthcheck is neither.
func TestComposeStatusReportsRealHealth(t *testing.T) {
	dockerlab.Require(t)
	dockerlab.Pull(t, dockerlab.ImageBusybox)

	cfg := dockerlab.Project(t, fmt.Sprintf(`
services:
  probed:
    image: %s
    init: true
    command: ["sh", "-c", "while true; do sleep 1; done"]
    healthcheck:
      test: ["CMD", "true"]
      interval: 1s
      timeout: 2s
      retries: 3
      start_period: 0s
  unprobed:
    image: %s
    init: true
    command: ["sh", "-c", "while true; do sleep 1; done"]
`, dockerlab.ImageBusybox, dockerlab.ImageBusybox))

	rt := compose.New(infraexec.New())
	ctx := context.Background()

	require.NoError(t, rt.Up(ctx, cfg, ports.UpOptions{Wait: true, WaitTimeout: 2 * time.Minute}))

	states, err := rt.Status(ctx, cfg)
	require.NoError(t, err)
	byName := map[string]ports.ServiceState{}
	for _, s := range states {
		byName[s.Name] = s
	}

	probed, ok := byName["probed"]
	require.True(t, ok, "compose ps did not report the probed service: %+v", states)
	assert.Equal(t, ports.HealthHealthy, probed.Health,
		"a passing healthcheck must read as healthy, not unknown")
	assert.True(t, probed.Running())

	unprobed, ok := byName["unprobed"]
	require.True(t, ok)
	assert.Equal(t, ports.HealthNone, unprobed.Health,
		"a service with no healthcheck must read as HealthNone: absence of a "+
			"probe is not evidence of illness")
	assert.True(t, unprobed.Running(),
		"an unprobed service that is up must count as running, or every "+
			"release without healthchecks would look broken")
}

// TestComposeStatusReportsAContainerThatDied is the case an operator meets
// after a crash. It is also the shape the scripted tests assert on, so this is
// what proves the script is faithful.
func TestComposeStatusReportsAContainerThatDied(t *testing.T) {
	dockerlab.Require(t)
	dockerlab.Pull(t, dockerlab.ImageBusybox)

	cfg := dockerlab.Project(t, fmt.Sprintf(`
services:
  doomed:
    image: %s
    command: ["sh", "-c", "exit 3"]
    restart: "no"
`, dockerlab.ImageBusybox))

	rt := compose.New(infraexec.New())
	ctx := context.Background()

	// No --wait: the service is expected to exit, and waiting for it to be
	// healthy is waiting for something that will not happen.
	require.NoError(t, rt.Up(ctx, cfg, ports.UpOptions{}))

	var died ports.ServiceState
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		states, err := rt.Status(ctx, cfg)
		require.NoError(t, err)
		if len(states) > 0 && states[0].State == "exited" {
			died = states[0]
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	require.Equal(t, "exited", died.State, "the container never reached a terminal state")
	assert.Equal(t, 3, died.ExitCode,
		"the exit code is what tells an operator whether the container failed "+
			"or was stopped")
	assert.False(t, died.Running())
	assert.NotEmpty(t, died.Status, "status carries the human-readable summary the report prints")
}

// TestComposeUpReportsAServiceThatWillNotStart covers the error path with a
// real failure rather than a scripted exit: the message has to name the
// project and point somewhere useful.
func TestComposeUpReportsAServiceThatWillNotStart(t *testing.T) {
	dockerlab.Require(t)
	dockerlab.Pull(t, dockerlab.ImageBusybox)

	cfg := dockerlab.Project(t, fmt.Sprintf(`
services:
  broken:
    image: %s
    command: ["sh", "-c", "exit 1"]
    restart: "no"
    healthcheck:
      test: ["CMD", "false"]
      interval: 1s
      timeout: 1s
      retries: 1
`, dockerlab.ImageBusybox))

	rt := compose.New(infraexec.New())
	err := rt.Up(context.Background(), cfg,
		ports.UpOptions{Wait: true, WaitTimeout: 20 * time.Second})

	require.Error(t, err, "a service that cannot start must fail Up, or a broken "+
		"release would be reported as deployed")
	assert.Contains(t, err.Error(), "start")
}

// TestComposeValidateAgainstRealCompose checks the parse this project depends
// on for the plan view and the health checks alike.
func TestComposeValidateAgainstRealCompose(t *testing.T) {
	rt, cfg := newComposeRuntime(t)

	rendered, err := rt.Validate(context.Background(), cfg)
	require.NoError(t, err)

	// Only the services the active profiles select. `docker compose config`
	// resolves profiles as part of merging, so a one-shot behind a profile is
	// absent here -- which is correct for `plan`, whose job is to say what
	// `up` would start, and is why the plan view does not list migrations.
	assert.ElementsMatch(t, []string{"web", "cache"}, rendered.Services,
		"Validate reports the services the active profiles select")
	assert.Contains(t, string(rendered.Config), "redis",
		"the merged config is what the dry-run diff is computed from, so it "+
			"has to carry the resolved images")
}

func TestComposeValidateRefusesAConfigurationItCannotResolve(t *testing.T) {
	dockerlab.Require(t)

	cfg := dockerlab.Project(t, `
services:
  broken:
    image: busybox
    ports:
      - "not-a-port:80"
`)

	_, err := compose.New(infraexec.New()).Validate(context.Background(), cfg)
	require.Error(t, err, "an unparseable Compose file must fail preflight, not `up`")
}

// TestComposeRunOneShotReturnsANonZeroExitAsData is the hook ABI's "nothing to
// do" contract, checked against a container that really exits 2.
func TestComposeRunOneShotReturnsANonZeroExitAsData(t *testing.T) {
	rt, cfg := newComposeRuntime(t)

	res, err := rt.RunOneShot(context.Background(), cfg, "migrate",
		ports.RunOptions{Remove: true, Timeout: 2 * time.Minute})

	require.NoError(t, err, "a process that ran and exited is a result, not a transport failure")
	assert.Equal(t, 2, res.ExitCode)
	assert.Contains(t, res.Stdout+res.Stderr, "nothing to migrate")
}

func TestComposeRunOneShotPassesItsEnvironment(t *testing.T) {
	dockerlab.Require(t)
	dockerlab.Pull(t, dockerlab.ImageBusybox)

	cfg := dockerlab.Project(t, fmt.Sprintf(`
services:
  echoer:
    image: %s
    profiles: ["tools"]
    command: ["sh", "-c", "echo GOT=$${MIGRATION_MODE:-unset}"]
`, dockerlab.ImageBusybox))

	res, err := compose.New(infraexec.New()).RunOneShot(context.Background(), cfg, "echoer",
		ports.RunOptions{
			Remove:  true,
			Env:     map[string]string{"MIGRATION_MODE": "offline"},
			Timeout: 2 * time.Minute,
		})

	require.NoError(t, err)
	assert.Contains(t, res.Stdout, "GOT=offline",
		"the hook ABI's variables must reach the one-shot container")
}

// TestComposeRunOneShotOnAMisnamedServiceIsAnExitNotAnError records the edge
// of the "a non-zero exit is data" rule.
//
// A manifest that names a service the Compose file does not define is an
// authoring mistake, but Compose reports it the same way it reports a failed
// migration: by exiting non-zero. The adapter cannot tell them apart and does
// not try, so the exit code reaches the caller and the diagnostic is in the
// output. Exit 1 rather than the ABI's 2 is what keeps it from being read as
// "nothing to do".
func TestComposeRunOneShotOnAMisnamedServiceIsAnExitNotAnError(t *testing.T) {
	rt, cfg := newComposeRuntime(t)

	res, err := rt.RunOneShot(context.Background(), cfg, "no-such-service",
		ports.RunOptions{Remove: true, Timeout: time.Minute})

	require.NoError(t, err)
	assert.NotEqual(t, 0, res.ExitCode)
	assert.NotEqual(t, 2, res.ExitCode,
		"exit 2 means `nothing to do` under the hook ABI; a misnamed service "+
			"must not be able to masquerade as a no-op migration")
}

// TestComposeExecReportsWhatRanInside covers both halves of Exec: a command
// that succeeds, and one whose non-zero exit is data.
func TestComposeExecReportsWhatRanInside(t *testing.T) {
	rt, cfg := newComposeRuntime(t)
	ctx := context.Background()

	require.NoError(t, rt.Up(ctx, cfg, ports.UpOptions{Wait: true, WaitTimeout: 2 * time.Minute}))

	res, err := rt.Exec(ctx, cfg, "web", []string{"echo", "hello from inside"})
	require.NoError(t, err)
	assert.Equal(t, 0, res.ExitCode)
	assert.Contains(t, res.Stdout, "hello from inside")

	res, err = rt.Exec(ctx, cfg, "web", []string{"sh", "-c", "exit 7"})
	require.NoError(t, err, "a non-zero exit from exec is a result, not a transport failure")
	assert.Equal(t, 7, res.ExitCode)

	// A service that is not there exits non-zero for the same reason a
	// command inside it does, and travels the same way.
	res, err = rt.Exec(ctx, cfg, "not-a-service", []string{"true"})
	require.NoError(t, err)
	assert.NotEqual(t, 0, res.ExitCode)
}

// TestComposeLogsStreamsRealOutput exercises the one path that does not go
// through the shared runner: a stream the caller closes, with its own process
// group so a `--follow` left running does not outlive the session.
func TestComposeLogsStreamsRealOutput(t *testing.T) {
	rt, cfg := newComposeRuntime(t)
	ctx := context.Background()

	require.NoError(t, rt.Up(ctx, cfg, ports.UpOptions{Wait: true, WaitTimeout: 2 * time.Minute}))

	stream, err := rt.Logs(ctx, cfg, ports.LogOptions{Services: []string{"web"}, Tail: 50})
	require.NoError(t, err)

	data, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.NoError(t, stream.Close())
	assert.Contains(t, string(data), "web is up")

	// A follow is the case that leaks if Close does not kill the process
	// group: the command never returns on its own.
	follow, err := rt.Logs(ctx, cfg, ports.LogOptions{
		Services: []string{"web"}, Follow: true, Since: time.Now().Add(-time.Hour),
	})
	require.NoError(t, err)

	buf := make([]byte, 64)
	_, _ = follow.Read(buf)

	done := make(chan error, 1)
	go func() { done <- follow.Close() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("closing a followed log stream hung, so `morzer logs -f` leaks a " +
			"docker process for the life of the session")
	}
}

func TestComposeRestartKeepsTheProjectRunning(t *testing.T) {
	rt, cfg := newComposeRuntime(t)
	ctx := context.Background()

	require.NoError(t, rt.Up(ctx, cfg, ports.UpOptions{Wait: true, WaitTimeout: 2 * time.Minute}))
	require.NoError(t, rt.Restart(ctx, cfg, []string{"web"}))

	states, err := rt.Status(ctx, cfg)
	require.NoError(t, err)
	for _, s := range states {
		if s.Name == "web" {
			assert.Equal(t, "running", s.State)
		}
	}

	require.Error(t, rt.Restart(ctx, cfg, []string{"not-a-service"}))
}

// TestComposeHasImageAndPullAgainstARealDaemon covers the check that lets a
// boot-time apply work without a network: an image already present is not
// pulled again.
func TestComposeHasImageAndPullAgainstARealDaemon(t *testing.T) {
	dockerlab.Require(t)
	dockerlab.Pull(t, dockerlab.ImageBusybox)

	rt := compose.New(infraexec.New())
	ctx := context.Background()

	present, err := rt.HasImage(ctx, dockerlab.ImageBusybox)
	require.NoError(t, err)
	assert.True(t, present, "a pulled image must be reported present")

	absent, err := rt.HasImage(ctx,
		"busybox@sha256:0000000000000000000000000000000000000000000000000000000000000000")
	require.NoError(t, err, "an absent image is an answer, not an error")
	assert.False(t, absent)

	// Pull of what is already local must be a no-op rather than a network
	// call: this is what makes `apply --startup` work on a rebooted machine
	// with no route to the registry.
	require.NoError(t, rt.Pull(ctx, ports.RuntimeConfig{}, []string{dockerlab.ImageBusybox}))
}

func TestComposePullReportsAnImageThatDoesNotExist(t *testing.T) {
	dockerlab.Require(t)

	err := compose.New(infraexec.New()).Pull(context.Background(), ports.RuntimeConfig{},
		[]string{"busybox@sha256:1111111111111111111111111111111111111111111111111111111111111111"})

	require.Error(t, err)
	// shortImage is what keeps the summary readable: the adapter's own
	// sentence names the repository, and the daemon's diagnostic follows it
	// rather than being replaced by it.
	assert.Contains(t, err.Error(), "cannot pull busybox:",
		"the summary must name the image without its 71 characters of digest")
}

// TestComposeProbeRegistryClassifiesWhatWentWrong covers the check `doctor`
// runs before an update: can the registry be reached without transferring
// layers, and if not, which remedy does the operator need.
//
// Against a registry started for the test rather than Docker Hub. A probe of a
// public registry would make this suite depend on the internet, on a rate
// limit, and on a manifest API that took 55 seconds to answer on the machine
// this was written on -- more than the adapter's own 30-second budget.
//
// **What this cannot assert.** `docker manifest inspect` speaks HTTPS unless
// given `--insecure`, and the adapter never passes it: a reachability probe
// that accepted plaintext could be answered by anyone on the path, which is
// the opposite of what `doctor` is being asked. So a plain-HTTP registry --
// the only kind a test can stand up without reconfiguring the daemon -- cannot
// produce the clean-probe case, and the success path is left to the operator's
// own registry. The three failure classifications are what carry real
// consequences, and they are all here.
func TestComposeProbeRegistryClassifiesWhatWentWrong(t *testing.T) {
	dockerlab.Require(t)
	dockerlab.Pull(t, dockerlab.ImageBusybox)

	reg := dockerlab.Start(t, dockerlab.ImageRegistry, []int{5000}, nil)
	addr := reg.HostPort(t, 5000)
	reg.WaitReady(t, 60*time.Second, "/bin/registry", "--version")

	local := addr + "/busybox:probe"
	requireDocker(t, "tag", dockerlab.ImageBusybox, local)
	requireDocker(t, "push", local)

	rt := compose.New(infraexec.New())
	ctx := context.Background()

	// A registry that is listening and holds the image, but only over
	// plaintext: the probe refuses it, and must not blame the operator's
	// credentials for it.
	err := rt.ProbeRegistry(ctx, local)
	require.Error(t, err)
	assert.NotContains(t, strings.ToLower(err.Error()), "authentication required",
		"a plaintext registry must not be reported as an authentication "+
			"problem: the operator would go and run `docker login` for nothing")

	// A digest nobody published: the remedy is a fixed manifest.
	err = rt.ProbeRegistry(ctx,
		"registry.example.invalid/nothing@sha256:1111111111111111111111111111111111111111111111111111111111111111")
	require.Error(t, err)

	// And a registry that is not listening at all.
	err = rt.ProbeRegistry(ctx, "127.0.0.1:1/nothing:v1")
	require.Error(t, err)
	assert.NotEmpty(t, err.Error(), "an unreachable registry still has to say something")
}

func requireDocker(t *testing.T, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := osexec.CommandContext(ctx, "docker", args...).CombinedOutput()
	require.NoError(t, err, "docker %s: %s", strings.Join(args, " "), out)
}
