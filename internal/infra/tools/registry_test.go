package tools_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/infra/tools"
)

// Preflight's whole job is to tell an operator which of two things is wrong:
// the tool is missing, or the tool is too old. This registry is the single
// place those become distinguishable, and the parsing behind it has to cope
// with tools that print `sops 3.13.2`, `systemd 255 (255.4-1)` and a JSON
// document, all in answer to "what version are you".

func registry(t *testing.T) (*tools.Registry, *exec.Scripted) {
	t.Helper()
	runner := exec.NewScripted()
	return tools.NewRegistry(runner), runner
}

func TestTheDaemonVersionIsPreferredOverTheClients(t *testing.T) {
	reg, runner := registry(t)
	// The client can be newer or older than the daemon, and it is the
	// daemon that has to support what the release asks for.
	runner.OnOutput("version --format",
		`{"Client":{"Version":"29.6.2"},"Server":{"Version":"24.0.7"}}`)

	info, err := reg.Lookup(context.Background(), tools.Docker)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Version.String(); got != "24.0.7" {
		t.Errorf("version = %s, want the server's 24.0.7", got)
	}
	if info.Path == "" {
		t.Error("the resolved path was not recorded, and doctor prints it")
	}
}

// TestAClientThatCannotReachTheDaemonIsItsOwnFailure. "docker is missing" and
// "the daemon is down" have different remedies.
func TestAClientThatCannotReachTheDaemonIsItsOwnFailure(t *testing.T) {
	reg, runner := registry(t)
	runner.OnOutput("version --format", `{"Client":{"Version":"29.6.2"}}`)

	_, err := reg.Lookup(context.Background(), tools.Docker)
	if err == nil {
		t.Fatal("a docker client with no daemon behind it was reported healthy")
	}
	if !strings.Contains(err.Error(), "cannot reach the daemon") {
		t.Errorf("the failure does not say the daemon is unreachable: %v", err)
	}
}

// TestDockerVersionFallsBackWhenItIsNotJSON covers the older client that
// ignores --format.
func TestDockerVersionFallsBackWhenItIsNotJSON(t *testing.T) {
	reg, runner := registry(t)
	runner.OnOutput("version --format", "Docker version 24.0.7, build afdd53b")

	info, err := reg.Lookup(context.Background(), tools.Docker)
	if err != nil {
		t.Fatalf("plain-text version output was refused: %v", err)
	}
	if got := info.Version.String(); got != "24.0.7" {
		t.Errorf("version = %s, want 24.0.7", got)
	}
}

func TestVersionsAreParsedOutOfWhateverEachToolPrints(t *testing.T) {
	cases := map[string]struct {
		tool, output, want string
	}{
		"sops":                   {tools.SOPS, "sops 3.13.2 (latest)", "3.13.2"},
		"age":                    {tools.Age, "v1.3.1", "1.3.1"},
		"restic with a banner":   {tools.Restic, "restic 0.16.4 compiled with go1.21.6 on linux/amd64", "0.16.4"},
		"systemd, no patch":      {tools.Systemd, "systemd 255 (255.4-1)\n+PAM +AUDIT", "255.4.0"},
		"compose, short form":    {tools.Compose, "v2.30.3", "2.30.3"},
		"a version with no dots": {tools.Age, "age version 1.2", "1.2.0"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			reg, runner := registry(t)
			runner.Fallback = exec.Result{Stdout: tc.output}

			info, err := reg.Lookup(context.Background(), tc.tool)
			if err != nil {
				t.Fatalf("%q was not understood: %v", tc.output, err)
			}
			if got := info.Version.String(); got != tc.want {
				t.Errorf("version = %s, want %s (from %q)", got, tc.want, tc.output)
			}
			if info.Raw == "" {
				t.Error("the raw string was dropped, and doctor prints what the " +
					"tool actually said")
			}
		})
	}
}

// TestAVersionOnStderrIsStillFound. Several tools print it there, and a
// per-tool stream flag in the probe table would be one more thing to get wrong.
func TestAVersionOnStderrIsStillFound(t *testing.T) {
	reg, runner := registry(t)
	runner.Fallback = exec.Result{Stderr: "sops 3.13.2"}

	info, err := reg.Lookup(context.Background(), tools.SOPS)
	if err != nil {
		t.Fatalf("a version printed to stderr was missed: %v", err)
	}
	if got := info.Version.String(); got != "3.13.2" {
		t.Errorf("version = %s", got)
	}
}

func TestAToolThatIsNotInstalled(t *testing.T) {
	reg, runner := registry(t)
	runner.LookErr = errors.New("exec: \"sops\": executable file not found in $PATH")

	info, err := reg.Lookup(context.Background(), tools.SOPS)
	if err == nil {
		t.Fatal("a tool that is not on PATH was resolved")
	}
	if info.Name != tools.SOPS {
		t.Errorf("the failure does not carry the tool name: %+v", info)
	}
	if tools.NewRegistry(runner).Available(context.Background(), tools.SOPS) {
		t.Error("an absent tool was reported available; optional tools would " +
			"then be used and fail later")
	}
}

// TestAToolThatIsInstalledButWillNotAnswer is the hung-daemon case.
func TestAToolThatIsInstalledButWillNotAnswer(t *testing.T) {
	reg, runner := registry(t)
	runner.OnError("version", errors.New("signal: killed"))

	_, err := reg.Lookup(context.Background(), tools.Docker)
	if err == nil {
		t.Fatal("a tool that never answered was reported working")
	}
	de := domain.AsError(err)
	if !strings.Contains(de.Message, "did not report a version") {
		t.Errorf("message %q does not distinguish this from being missing", de.Message)
	}
	if !strings.Contains(de.Hint, "docker") {
		t.Errorf("hint %q does not give the command to run by hand", de.Hint)
	}
}

func TestOutputThatCarriesNoVersionAtAll(t *testing.T) {
	for name, output := range map[string]string{
		"empty":                    "",
		"a message, not a version": "Cannot connect to the Docker daemon at unix:///var/run/docker.sock.",
	} {
		t.Run(name, func(t *testing.T) {
			reg, runner := registry(t)
			runner.Fallback = exec.Result{Stdout: output}

			_, err := reg.Lookup(context.Background(), tools.SOPS)
			if err == nil {
				t.Fatalf("%q was parsed as a version", output)
			}
			if !strings.Contains(domain.AsError(err).Message, "cannot understand") {
				t.Errorf("message does not say the output was unparseable: %v", err)
			}
		})
	}
}

// TestUnparseableOutputIsQuotedButTruncated: an operator needs to see what the
// tool said, not a screenful of it.
func TestUnparseableOutputIsQuotedButTruncated(t *testing.T) {
	reg, runner := registry(t)
	runner.Fallback = exec.Result{Stdout: strings.Repeat("no version here ", 200)}

	_, err := reg.Lookup(context.Background(), tools.SOPS)
	if err == nil {
		t.Fatal("a wall of text was parsed as a version")
	}
	hint := domain.AsError(err).Hint
	if !strings.Contains(hint, "no version here") {
		t.Errorf("hint %q does not quote what the tool said", hint)
	}
	if len(hint) > 200 {
		t.Errorf("the hint is %d characters of the tool's own noise", len(hint))
	}
}

func TestAToolWithNoProbeDefinedIsAnInternalError(t *testing.T) {
	reg, _ := registry(t)

	_, err := reg.Lookup(context.Background(), "kubectl")
	if err == nil {
		t.Fatal("a tool nobody defined a probe for was resolved")
	}
	// Internal: the catalogue is the manager's own, so asking for something
	// not in it is the manager being wrong, not the machine.
	if domain.AsError(err).Code != domain.CodeInternal {
		t.Errorf("code = %v, want internal", domain.AsError(err).Code)
	}
}

func TestRequireDistinguishesMissingFromTooOld(t *testing.T) {
	t.Run("too old", func(t *testing.T) {
		reg, runner := registry(t)
		runner.OnOutput("version --format",
			`{"Client":{"Version":"20.0.0"},"Server":{"Version":"20.0.0"}}`)

		want, err := domain.ParseConstraint(">=24")
		if err != nil {
			t.Fatal(err)
		}
		_, err = reg.Require(context.Background(), tools.Docker, want)
		if err == nil {
			t.Fatal("docker 20 satisfied >=24")
		}
		de := domain.AsError(err)
		if !strings.Contains(de.Message, "20.0.0") || !strings.Contains(de.Message, ">=24") {
			t.Errorf("the refusal does not say what is installed and what is "+
				"wanted: %q", de.Message)
		}
		if !strings.Contains(de.Hint, "upgrade") {
			t.Errorf("hint %q does not say what to do", de.Hint)
		}
		if !errors.Is(err, domain.ErrToolIncompatible) {
			t.Error("a too-old tool is not distinguishable from a missing one")
		}
	})

	t.Run("new enough", func(t *testing.T) {
		reg, runner := registry(t)
		runner.OnOutput("version --format",
			`{"Client":{"Version":"29.6.2"},"Server":{"Version":"29.6.2"}}`)

		want, err := domain.ParseConstraint(">=24")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reg.Require(context.Background(), tools.Docker, want); err != nil {
			t.Errorf("docker 29 did not satisfy >=24: %v", err)
		}
	})

	t.Run("no constraint at all", func(t *testing.T) {
		reg, runner := registry(t)
		runner.Fallback = exec.Result{Stdout: "sops 3.13.2"}

		if _, err := reg.Require(context.Background(), tools.SOPS, domain.Constraint{}); err != nil {
			t.Errorf("a tool with no version requirement was refused: %v", err)
		}
	})

	t.Run("missing entirely", func(t *testing.T) {
		reg, runner := registry(t)
		runner.LookErr = errors.New("not found in $PATH")

		if _, err := reg.Require(context.Background(), tools.SOPS, domain.Constraint{}); err == nil {
			t.Error("a missing tool satisfied a requirement")
		}
	})
}

// TestBothOutcomesAreCachedOnce. `doctor` asks about docker, preflight asks
// again, an adapter asks a third time -- and a daemon that restarts mid
// operation must not produce three different answers.
func TestBothOutcomesAreCachedOnce(t *testing.T) {
	t.Run("a success", func(t *testing.T) {
		reg, runner := registry(t)
		runner.Fallback = exec.Result{Stdout: "sops 3.13.2"}

		for range 5 {
			if _, err := reg.Lookup(context.Background(), tools.SOPS); err != nil {
				t.Fatal(err)
			}
		}
		if n := len(runner.Calls()); n != 1 {
			t.Errorf("the tool was probed %d times for 5 questions:\n%s",
				n, runner.CommandLines())
		}
	})

	t.Run("a failure", func(t *testing.T) {
		reg, runner := registry(t)
		runner.LookErr = errors.New("not found in $PATH")

		for range 5 {
			if _, err := reg.Lookup(context.Background(), tools.SOPS); err == nil {
				t.Fatal("a missing tool was resolved")
			}
		}
		// Failures are cached too: if sops is missing, asking again will
		// not find it, and each attempt costs a PATH walk.
		if n := len(runner.Calls()); n != 0 {
			t.Errorf("a missing tool was re-probed %d times", n)
		}
	})
}
