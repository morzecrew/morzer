//go:build docker

// Package dockerlab starts real containers for the suites that need one.
//
// The adapters this project ships talk to Docker, Compose and a database. A
// fake can prove that the code calls them in the right order; only a real
// service can prove that what comes back is what the adapter thinks it is.
// RFC 0008 §5.4 is the argument in full: `docker compose ps` has two output
// shapes, a healthcheck's `starting` is not its `unhealthy`, and a Postgres
// dump is either restorable or it is not.
//
// Everything here is behind the `docker` build tag. A contributor without
// Docker still gets a fast `just test`; `just test-docker` opts in, and once
// opted in a missing daemon is a failure rather than a skip — a suite that
// quietly skipped is the failure mode this whole programme exists to catch.
//
// Images are pinned by digest, which is the same rule the manager enforces on
// every release it installs. A test fixture that floated on a tag would be a
// fixture that changes underneath a failure.
package dockerlab

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/ports"
)

// The images the suites run against, pinned by digest.
//
// Chosen for what each one proves rather than for variety: busybox for the
// trivial cases and one-shot exits, redis for a listener that answers
// immediately, caddy for real HTTP status codes, postgres for a backup that
// means something.
const (
	ImageBusybox  = "busybox@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0"
	ImageRedis    = "redis@sha256:978f0e01593e65eed801f2402944efcd936d43b5027e4908a7897baf88ed6241"
	ImageCaddy    = "caddy@sha256:5f5c8640aae01df9654968d946d8f1a56c497f1dd5c5cda4cf95ab7c14d58648"
	ImagePostgres = "postgres@sha256:9a8afca54e7861fd90fab5fdf4c42477a6b1cb7d293595148e674e0a3181de15"
	ImageRegistry = "registry@sha256:1be55279f18a2fe1a74edf2664cac61c1bea305b7b4642dab412e7affdcb3e33"

	// ImageMinIO backs the s3:// target suite. MinIO speaks the same API as
	// S3, R2, B2 and GCS interoperability mode, which is the whole reason
	// one adapter answers for all of them -- so proving the adapter against
	// MinIO is proving it against the API rather than against one vendor.
	ImageMinIO = "minio/minio@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e"

	// ImageOpenSSH backs the ssh:// target suite: a real sshd with a real
	// host key, which is the only way to test that a *changed* host key is
	// refused.
	ImageOpenSSH = "linuxserver/openssh-server@sha256:96b9a4d3b5106746d08d43a6911650d4d21f7d5c7f2ac9660e792bdb5e63157c"
)

// counter names projects and containers apart within a run. Not a random
// suffix: workflow scripts forbid randomness for replay, and a counter makes a
// failure reproducible by name.
var counter atomic.Int64

// Require fails the test when Docker cannot be used.
//
// Deliberately a failure and not a skip. The build tag is the opt-in; having
// opted in, a run with no daemon has exercised nothing and must say so.
func Require(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("the docker build tag is set but docker is not on PATH: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if out, err := run(ctx, "docker", "info", "--format", "{{.ServerVersion}}"); err != nil {
		t.Fatalf("the docker build tag is set but the daemon is unreachable: %v\n%s", err, out)
	}
}

// Pull makes sure an image is local before a test's clock starts.
//
// Pulling inside a timed wait is how a first run on a cold cache turns into a
// flaky timeout rather than a slow pass.
func Pull(t *testing.T, images ...string) {
	t.Helper()
	for _, image := range images {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		if out, err := run(ctx, "docker", "image", "inspect", image); err == nil {
			cancel()
			continue
		} else {
			_ = out
		}
		out, err := run(ctx, "docker", "pull", image)
		cancel()
		if err != nil {
			t.Fatalf("cannot pull %s: %v\n%s", image, err, out)
		}
	}
}

// Name returns a unique, readable identifier derived from the test.
func Name(t *testing.T) string {
	t.Helper()
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 32
		default:
			return '-'
		}
	}, t.Name())
	safe = strings.Trim(safe, "-")
	if len(safe) > 40 {
		safe = safe[:40]
	}
	return fmt.Sprintf("morzer-test-%s-%d", safe, counter.Add(1))
}

// Project writes a Compose file and returns the config the Runtime adapter
// takes, with teardown registered.
//
// Teardown removes volumes: these are test fixtures, and a run that left named
// volumes behind would fill a developer's disk one `just test-docker` at a
// time. That is the opposite of the production default, which never removes a
// volume without an explicit confirmation.
func Project(t *testing.T, composeYAML string) ports.RuntimeConfig {
	t.Helper()

	dir := t.TempDir()
	file := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(file, []byte(composeYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	// Named through the option rather than a field, because that is how a
	// manifest names one now: the manager carries `project` and the adapter
	// reads it. A lab that set it any other way would be exercising a path
	// no release takes.
	project := Name(t)
	cfg := ports.RuntimeConfig{
		Product:    project,
		Options:    map[string]string{"project": project},
		Files:      []string{file},
		WorkingDir: dir,
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		out, err := run(ctx, "docker", "compose", "--project-name", project,
			"--project-directory", dir, "--file", file,
			"down", "--volumes", "--remove-orphans", "--timeout", "5")
		if err != nil {
			t.Logf("tearing down %s: %v\n%s", project, err, out)
		}
	})

	return cfg
}

// Container is a running container the test owns.
type Container struct {
	ID   string
	Name string
}

// Start runs a container detached and registers its removal.
//
// ports are container ports to publish on an ephemeral loopback port; read the
// host side back with HostPort. Binding to 127.0.0.1 rather than 0.0.0.0 keeps
// a test fixture off the network of whatever machine is running it.
func Start(t *testing.T, image string, publish []int, env map[string]string, argv ...string) *Container {
	t.Helper()
	Pull(t, image)

	name := Name(t)
	args := []string{"run", "--detach", "--name", name, "--rm"}
	for _, p := range publish {
		args = append(args, "--publish", fmt.Sprintf("127.0.0.1::%d", p))
	}
	for k, v := range env {
		args = append(args, "--env", k+"="+v)
	}
	args = append(args, image)
	args = append(args, argv...)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	out, err := run(ctx, "docker", args...)
	if err != nil {
		t.Fatalf("cannot start %s: %v\n%s", image, err, out)
	}

	c := &Container{ID: strings.TrimSpace(out), Name: name}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Minute)
		defer stopCancel()
		// --rm removes it once stopped; a failure here means it was
		// already gone, which is the outcome wanted anyway.
		_, _ = run(stopCtx, "docker", "rm", "--force", "--volumes", name)
	})
	return c
}

// HostPort returns the loopback address a published container port landed on.
func (c *Container) HostPort(t *testing.T, containerPort int) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out, err := run(ctx, "docker", "port", c.Name, fmt.Sprintf("%d/tcp", containerPort))
	if err != nil {
		t.Fatalf("cannot read the published port for %s: %v\n%s", c.Name, err, out)
	}
	addr := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
	if addr == "" {
		t.Fatalf("container %s published nothing for %d/tcp", c.Name, containerPort)
	}
	return addr
}

// Exec runs a command inside the container and returns its combined output.
func (c *Container) Exec(t *testing.T, argv ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return run(ctx, "docker", append([]string{"exec", c.Name}, argv...)...)
}

// WaitReady polls a command inside the container until it succeeds.
//
// Polling a readiness command rather than sleeping: `postgres` prints "ready to
// accept connections" once during initialisation and again for real, and a
// fixed sleep tuned to one machine is the classic source of a suite that only
// fails in CI.
func (c *Container) WaitReady(t *testing.T, within time.Duration, argv ...string) {
	t.Helper()
	deadline := time.Now().Add(within)
	var last string
	for time.Now().Before(deadline) {
		out, err := c.Exec(t, argv...)
		if err == nil {
			return
		}
		last = out
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("%s never became ready within %s; last attempt said:\n%s", c.Name, within, last)
}

// run executes a command and returns its combined output.
func run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}
