package compose

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/test/fakes"
)

const (
	relayFor = "/var/lib/demo/manager"
	dbRef    = "registry.example/demo/db@sha256:0000000000000000000000000000000000000000000000000000000000000002"
)

// hooked answers the commands answer claims, and every other one as the
// scripted runner does, which records them all.
type hooked struct {
	*fakes.Scripted
	answer func(argv []string) (exec.Result, error, bool)
}

func (h hooked) Run(ctx context.Context, cmd exec.Command) (exec.Result, error) {
	res, err := h.Scripted.Run(ctx, cmd)
	if r, e, ok := h.answer(cmd.Argv); ok {
		return r, e
	}
	return res, err
}

func exitWith(code int, stderr string) (exec.Result, error, bool) {
	return exec.Result{ExitCode: code, Stderr: stderr}, &exec.ExitError{ExitCode: code, Stderr: stderr}, true
}

// relayRuntime is a runtime with the relay on, its images absent and its
// helper local, whose relay commands answer answer says.
func relayRuntime(answer func(argv []string) (exec.Result, error, bool)) (*Runtime, *fakes.Scripted) {
	scripted := fakes.NewScripted()
	scripted.OnExit("image inspect registry.example", 1, "Error: No such image")
	if answer == nil {
		answer = func([]string) (exec.Result, error, bool) { return exec.Result{}, nil, false }
	}
	r := New(hooked{Scripted: scripted, answer: answer}, WithRelay(relayFor))
	r.relayWait = 0
	return r, scripted
}

// relayName is the container a `docker run -d` names, or "".
func relayName(argv []string) string {
	if len(argv) < 3 || argv[1] != "run" || argv[2] != "-d" {
		return ""
	}
	return argv[slices.Index(argv, "--name")+1]
}

// verbs is what each docker command did, in order.
func verbs(s *fakes.Scripted) []string {
	var out []string
	for _, cmd := range s.Calls() {
		out = append(out, cmd.Argv[1])
	}
	return out
}

// The relay is the whole of what a Mac adds: started before the first pull,
// checked through, every pull and tag through it, and removed after the last
// (RFC 0060 §5.1, decisions 2, 9, 15 and 16).
func TestARelayCarriesTheIngestOnAMac(t *testing.T) {
	r, s := relayRuntime(nil)
	require.NoError(t, r.IngestImages(context.Background(), emptyLayout(t), []string{appRef}))

	var run []string
	for _, cmd := range s.Calls() {
		if relayName(cmd.Argv) != "" {
			run = cmd.Argv
		}
	}
	require.NotEmpty(t, run, s.CommandLines())
	name := relayName(run)
	port, err := strconv.Atoi(run[slices.Index(run, "-p")+1])
	require.NoError(t, err)
	assert.GreaterOrEqual(t, port, 49152)
	assert.LessOrEqual(t, port, 65535)
	assert.True(t, strings.HasPrefix(name, "morzer-ingest-"), name)
	line := strings.Join(run, " ")
	assert.Contains(t, line, "docker run -d --pull=never --name "+name+" --network host "+
		"--label dev.morzer.ingest-relay="+relayFor+" "+DefaultHelperImage+
		" timeout 3000 nc -lk -s 127.0.0.1 -p "+strconv.Itoa(port)+" -e nc host.docker.internal ")
	assert.NotContains(t, run, "--rm")

	relayAddr := "127.0.0.1:" + strconv.Itoa(port)
	assert.True(t, s.Ran("exec "+name+" wget -q -T 5 -O /dev/null http://"+relayAddr+
		"/v2/demo/app/manifests/"+appDigest), s.CommandLines())
	assert.True(t, s.Ran("pull "+relayAddr+"/demo/app@"+appDigest), s.CommandLines())
	assert.True(t, s.Ran("tag "+relayAddr+"/demo/app@"+appDigest+" "+appAlias), s.CommandLines())
	assert.True(t, s.Ran("rmi "+relayAddr+"/demo/app@"+appDigest), s.CommandLines())

	got := verbs(s)
	assert.Equal(t, []string{"ps", "run", "exec", "pull", "tag", "rmi", "rm"}, got[slices.Index(got, "ps"):])
	last := s.Calls()[len(s.Calls())-1].Argv
	assert.Equal(t, []string{"docker", "rm", "--force", name}, last)
}

// Without the option the ingest is Linux's, byte for byte: no relay, and the
// pull straight from the registry on this machine's loopback.
func TestWithoutTheRelayTheIngestIsLinuxs(t *testing.T) {
	runner := fakes.NewScripted()
	runner.OnExit("image inspect", 1, "Error: No such image")
	require.NoError(t, New(runner).IngestImages(context.Background(), emptyLayout(t), []string{appRef}))
	assert.Equal(t, []string{"image", "pull", "tag", "rmi"}, verbs(runner))
}

// A failed pull still removes the relay.
func TestARelayIsRemovedWhenAPullFails(t *testing.T) {
	r, s := relayRuntime(nil)
	s.OnExit(" pull ", 1, "filesystem layer verification failed")
	require.Error(t, r.IngestImages(context.Background(), emptyLayout(t), []string{appRef}))
	last := s.Calls()[len(s.Calls())-1].Argv
	assert.Equal(t, "rm", last[1], s.CommandLines())
	assert.True(t, strings.HasPrefix(last[3], "morzer-ingest-"), s.CommandLines())
}

// The helper is found before anything is served or started, never pulled, and
// an unpinned one is refused (decisions 4 and 18). A directory that is not a
// layout shows the server was not reached.
func TestARelayNeedsALocalPinnedHelper(t *testing.T) {
	r, s := relayRuntime(nil)
	s.OnExit("image inspect "+DefaultHelperImage, 1, "Error: No such image")
	s.OnExit("image inspect sha256:", 1, "Error: No such image")
	err := r.IngestImages(context.Background(), t.TempDir(), []string{appRef})
	require.ErrorIs(t, err, domain.ErrToolMissing)
	assert.Contains(t, domain.AsError(err).Hint, "docker pull "+DefaultHelperImage)
	assert.Contains(t, domain.AsError(err).Hint, "`docker load` an archive of it")
	assert.False(t, s.Ran(" run "), s.CommandLines())
	assert.False(t, s.Ran(" pull "), s.CommandLines())

	r, s = relayRuntime(nil)
	WithHelperImage("busybox:latest")(r)
	err = r.IngestImages(context.Background(), t.TempDir(), []string{appRef})
	require.ErrorIs(t, err, domain.ErrValidation)
	assert.False(t, s.Ran(" run "), s.CommandLines())
}

// An ingest after one whose manager was killed removes that installation's
// relays before it starts its own (decision 10).
func TestAnIngestRemovesTheRelaysAKilledOneLeft(t *testing.T) {
	r, s := relayRuntime(nil)
	s.OnOutput("ps --all --quiet --filter label=dev.morzer.ingest-relay="+relayFor, "abc\ndef\n")
	require.NoError(t, r.IngestImages(context.Background(), emptyLayout(t), []string{appRef}))
	lines := strings.Split(s.CommandLines(), "\n")
	at := func(want string) int {
		return slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, want) })
	}
	run := at("run -d")
	require.Positive(t, run, s.CommandLines())
	assert.Positive(t, at("rm --force abc"))
	assert.Less(t, at("rm --force abc"), run)
	assert.Less(t, at("rm --force def"), run)
}

// The bound covers the whole ingest: the time left on its context, or with no
// deadline the check's limit and each image's pull, tag and untag (decision
// 15).
func TestARelaysBoundCoversTheWholeIngest(t *testing.T) {
	bound := func(ctx context.Context, refs ...string) int {
		t.Helper()
		r, s := relayRuntime(nil)
		require.NoError(t, r.IngestImages(ctx, emptyLayout(t), refs))
		for _, cmd := range s.Calls() {
			if relayName(cmd.Argv) != "" {
				n, err := strconv.Atoi(cmd.Argv[slices.Index(cmd.Argv, "timeout")+1])
				require.NoError(t, err)
				return n
			}
		}
		t.Fatalf("no relay ran:\n%s", s.CommandLines())
		return 0
	}
	assert.Equal(t, 60+2*49*60, bound(context.Background(), appRef, dbRef))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	got := bound(ctx, appRef, dbRef)
	assert.LessOrEqual(t, got, 600)
	assert.Greater(t, got, 590)
}

// A relay that exits at start because its port is held is read, removed and
// replaced on another port, five times at most (decisions 19 and 20).
func TestARelayOnAHeldPortIsReplaced(t *testing.T) {
	var first string
	held := func(name string) bool { return first == "" || name == first }
	answer := func(argv []string) (exec.Result, error, bool) {
		if n := relayName(argv); n != "" && first == "" {
			first = n
		}
		switch {
		case argv[1] == "exec" && held(argv[2]):
			return exitWith(1, "Error response from daemon: container is not running")
		case argv[1] == "inspect" && held(argv[len(argv)-1]):
			return exec.Result{Stdout: "false\n"}, nil, true
		case argv[1] == "logs" && held(argv[2]):
			return exec.Result{Stderr: "nc: bind: Address already in use\n"}, nil, true
		}
		return exec.Result{}, nil, false
	}
	r, s := relayRuntime(answer)
	require.NoError(t, r.IngestImages(context.Background(), emptyLayout(t), []string{appRef}))
	var names []string
	for _, cmd := range s.Calls() {
		if n := relayName(cmd.Argv); n != "" {
			names = append(names, n)
		}
	}
	require.Len(t, names, 2, s.CommandLines())
	lines := s.CommandLines()
	assert.Less(t, strings.Index(lines, "logs "+first), strings.Index(lines, "rm --force "+first))
	assert.Less(t, strings.Index(lines, "rm --force "+first), strings.Index(lines, "--name "+names[1]))

	// Held every time: refused naming the ports, each relay removed.
	r, s = relayRuntime(func(argv []string) (exec.Result, error, bool) {
		first = ""
		return answer(argv)
	})
	err := r.IngestImages(context.Background(), emptyLayout(t), []string{appRef})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "held on the engine's loopback")
	assert.Equal(t, 5, strings.Count(s.CommandLines(), "run -d"))
	assert.Equal(t, 5, strings.Count(s.CommandLines(), "rm --force morzer-ingest-"))
	assert.False(t, s.Ran(" pull "))
}

// What a relay that cannot work printed says which refusal it is: the helper
// lacks what the relay runs (decision 11), the engine would not create it
// (decision 13), or what answered is not this manager (decisions 14 and 16).
// Each is refused before the first pull, and its relay removed.
func TestARelayThatCannotWorkIsRefusedBeforeThePull(t *testing.T) {
	cases := []struct {
		name   string
		answer func(argv []string) (exec.Result, error, bool)
		is     error
		says   string
		execs  int
	}{
		{"nc refuses a flag", func(argv []string) (exec.Result, error, bool) {
			switch argv[1] {
			case "exec":
				return exitWith(1, "Error response from daemon: container is not running")
			case "inspect":
				return exec.Result{Stdout: "false\n"}, nil, true
			case "logs":
				return exec.Result{Stderr: "nc: invalid option -- 's'\n"}, nil, true
			}
			return exec.Result{}, nil, false
		}, domain.ErrToolMissing, "nc: invalid option -- 's'", 1},
		{"no wget", func(argv []string) (exec.Result, error, bool) {
			if argv[1] == "exec" {
				return exitWith(127, `OCI runtime exec failed: exec failed: unable to start container process: exec: "wget": executable file not found in $PATH`)
			}
			if argv[1] == "inspect" {
				return exec.Result{Stdout: "true\n"}, nil, true
			}
			return exec.Result{}, nil, false
		}, domain.ErrToolMissing, `exec: \"wget\": executable file not found`, 1},
		{"no timeout", func(argv []string) (exec.Result, error, bool) {
			if relayName(argv) != "" {
				return exitWith(127, `docker: Error response from daemon: failed to create task for container: exec: "timeout": executable file not found in $PATH`)
			}
			return exec.Result{}, nil, false
		}, domain.ErrToolMissing, `exec: \"timeout\": executable file not found`, 0},
		{"not created", func(argv []string) (exec.Result, error, bool) {
			if relayName(argv) != "" {
				return exitWith(125, "docker: Error response from daemon: host networking is not allowed")
			}
			return exec.Result{}, nil, false
		}, nil, "Enhanced Container Isolation", 0},
		{"another registry answered", func(argv []string) (exec.Result, error, bool) {
			if argv[1] == "exec" {
				return exitWith(1, "wget: server returned error: HTTP/1.1 404 Not Found")
			}
			if argv[1] == "inspect" {
				return exec.Result{Stdout: "true\n"}, nil, true
			}
			return exec.Result{}, nil, false
		}, domain.ErrUnsupported, "DOCKER_CONTEXT", 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, s := relayRuntime(c.answer)
			err := r.IngestImages(context.Background(), emptyLayout(t), []string{appRef})
			require.Error(t, err)
			if c.is != nil {
				require.ErrorIs(t, err, c.is)
			}
			assert.Contains(t, domain.AsError(err).Hint, c.says)
			assert.Equal(t, c.execs, strings.Count(s.CommandLines(), " exec "))
			assert.False(t, s.Ran(" pull "), s.CommandLines())
			last := s.Calls()[len(s.Calls())-1].Argv
			assert.Equal(t, "rm", last[1], s.CommandLines())
		})
	}
}
