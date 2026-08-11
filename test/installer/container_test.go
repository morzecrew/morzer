//go:build docker

package installer

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/morzecrew/morzer/test/dockerlab"
)

// The script on a machine that is not the developer's.
//
// What this lane answers and the rest of the package cannot: /bin/sh here is
// busybox ash rather than bash, busybox tar does not take --zstd so the zstd
// fallback is the path that runs, and fish is a real fish reading a file this
// script generated. Decision 2 of RFC 0022 is "POSIX sh with no bashisms", and
// a bashism fails on exactly the machine the script exists for -- a freshly
// provisioned box, which is this.

const alpine = "alpine:3.21"

// inAlpine runs a shell command in a container that can reach this process's
// test server, with the script and the certificate mounted.
//
// --network host because the fixture is served on the host's loopback: the
// alternative is publishing a port from a container that does not exist, since
// the server is in the test binary rather than in a container.
func inAlpine(t *testing.T, rel *release, scriptPath, command string) (string, error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// /root is the container's own and vanishes with it. Mounting a host
	// directory there would leave root-owned files inside a t.TempDir() --
	// fish creates ~/.local at mode 700 -- which the test user then cannot
	// remove, failing the test after every assertion in it had passed.
	args := []string{
		"run", "--rm", "--network", "host",
		"--volume", scriptPath + ":/install.sh:ro",
		"--volume", rel.ca + ":/ca.pem:ro",
		// The fixture directory at the same path it has on the host, so
		// the stub binary's argv log -- whose path was baked in when the
		// archive was built -- is a file this process can read afterwards.
		"--volume", rel.dir + ":" + rel.dir,
		"--env", "CURL_CA_BUNDLE=/ca.pem",
		"--env", "HOME=/root",
		alpine, "sh", "-c", command,
	}
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	return string(out), err
}

func TestTheScriptRunsWhereThereIsNoBash(t *testing.T) {
	dockerlab.Require(t)
	dockerlab.Pull(t, alpine)

	rel := newRelease(t, fixtureOptions{})
	scriptPath := script(t, rel)

	// busybox tar has no --zstd, so this is also the run that exercises the
	// `zstd -dc | tar -xf -` fallback -- the path no test on a GNU machine
	// takes.
	out, err := inAlpine(t, rel, scriptPath,
		"apk add --no-cache curl zstd >/dev/null 2>&1 && "+
			"sh /install.sh --version "+fixtureVersion+
			" --dir /usr/local/bin --no-verify-signature --no-completions && "+
			"/usr/local/bin/morzer version")
	if err != nil {
		t.Fatalf("the script failed under busybox ash: %v\n%s", err, out)
	}

	for _, want := range []string{"morzer " + fixtureVersion, rel.digest} {
		if !strings.Contains(out, want) {
			t.Errorf("the output does not carry %q:\n%s", want, out)
		}
	}
}

func TestTheScriptRefusesUnderBusyboxToo(t *testing.T) {
	// A refusal that only works under bash is a refusal that does not work.
	// The checksum path is the one to check: it is grep, a parameter
	// expansion and a string comparison, and busybox's grep is not GNU's.
	dockerlab.Require(t)
	dockerlab.Pull(t, alpine)

	rel := newRelease(t, fixtureOptions{omitSumsLine: true})

	out, err := inAlpine(t, rel, script(t, rel),
		"apk add --no-cache curl zstd >/dev/null 2>&1 && "+
			"sh /install.sh --version "+fixtureVersion+
			" --dir /usr/local/bin --no-verify-signature --no-completions")
	if err == nil {
		t.Fatalf("a SHA256SUMS with no line for the archive was accepted:\n%s", out)
	}
	if !strings.Contains(out, "SHA256SUMS has no line for") {
		t.Errorf("the refusal does not name the reason:\n%s", out)
	}
	if strings.Contains(out, "installed") {
		t.Errorf("it reported an install after refusing:\n%s", out)
	}
}

func TestFishReadsTheBlockThisScriptWrote(t *testing.T) {
	// The assertion that nothing but a real fish can make. A POSIX
	// `case ... esac` in a .fish file is a syntax error at every subsequent
	// shell start, and what breaks is the operator's shell rather than
	// morzer -- so they have no reason to suspect this file.
	dockerlab.Require(t)
	dockerlab.Pull(t, alpine)

	rel := newRelease(t, fixtureOptions{})

	out, err := inAlpine(t, rel, script(t, rel),
		"apk add --no-cache curl zstd fish >/dev/null 2>&1 && "+
			"SHELL=/usr/bin/fish sh /install.sh --version "+fixtureVersion+
			" --dir /opt/morzer/bin --no-verify-signature --no-completions && "+
			"fish -c 'source /root/.config/fish/conf.d/morzer.fish; "+
			"if contains /opt/morzer/bin $PATH; echo PREFIX-ON-PATH; end'")
	if err != nil {
		t.Fatalf("fish could not read the block: %v\n%s", err, out)
	}
	if !strings.Contains(out, "PREFIX-ON-PATH") {
		t.Errorf("sourcing the generated file did not put the prefix on fish's PATH:\n%s", out)
	}
}

func TestTheCompletionCallReachesTheBinaryInAContainer(t *testing.T) {
	// The delegation, end to end on a machine with a shell fish knows: the
	// stub records its argv into the mounted home, so the assertion reads a
	// file the container wrote.
	dockerlab.Require(t)
	dockerlab.Pull(t, alpine)

	rel := newRelease(t, fixtureOptions{})

	// --completions forces it on: nothing here is a terminal, which is the
	// default this flag exists to override.
	out, err := inAlpine(t, rel, script(t, rel),
		"apk add --no-cache curl zstd >/dev/null 2>&1 && "+
			"SHELL=/bin/bash sh /install.sh --version "+fixtureVersion+
			" --dir /usr/local/bin --no-verify-signature --completions")
	if err != nil {
		t.Fatalf("the install failed: %v\n%s", err, out)
	}

	if argv := rel.argvLog(t); !strings.Contains(argv, "completion install bash") {
		t.Errorf("the binary was not asked to install a completion.\nargv log:\n%s\n"+
			"output:\n%s", argv, out)
	}
}
