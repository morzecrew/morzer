package health_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/adapters/health"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// "Healthy" is the claim `apply` waits for before calling an update successful,
// so what these probes report is what decides whether a deployment is rolled
// back. Every case below is one an operator will meet: a service that is slow,
// one that is broken, one that is not listening at all.
//
// Real listeners rather than mocks. A prober's whole job is to talk to the
// network, and a mocked network tests the mock.

func spec(check domain.HealthCheck) ports.CheckSpec {
	return ports.CheckSpec{Check: check}
}

func TestHTTPProbeReportsWhatTheServerDid(t *testing.T) {
	handlers := map[string]struct {
		handler http.HandlerFunc
		ok      bool
		message string
	}{
		"a ready service": {
			func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}, true, "",
		},
		"a service that is up but not ready": {
			func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
			}, false, "503",
		},
		"a service that has broken": {
			func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			}, false, "500",
		},
		"a redirect, which a health endpoint must not do": {
			func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/elsewhere", http.StatusFound)
			}, false, "302",
		},
	}

	for name, tc := range handlers {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()

			res, err := health.NewHTTP().Check(context.Background(),
				spec(domain.HealthCheck{
					Name: "api", Type: domain.HealthHTTP, URL: srv.URL + "/health",
				}))
			if err != nil {
				t.Fatalf("the prober errored rather than reporting: %v", err)
			}
			if res.OK != tc.ok {
				t.Errorf("OK = %v, want %v (%s)", res.OK, tc.ok, res.Message)
			}
			if tc.message != "" && !strings.Contains(res.Message, tc.message) {
				t.Errorf("message %q does not name the status", res.Message)
			}
			if res.Name != "api" {
				t.Errorf("the result is not attributed to the check: %q", res.Name)
			}
		})
	}
}

// TestHTTPProbeOnNothingListening is the state a deployment is in for the first
// few seconds of every apply, so its message is one operators read often.
func TestHTTPProbeOnNothingListening(t *testing.T) {
	// A port that was listening and is not any more: bound, read, closed.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	res, err := health.NewHTTP().Check(context.Background(),
		spec(domain.HealthCheck{Name: "api", Type: domain.HealthHTTP,
			URL: "http://" + addr + "/health"}))
	if err != nil {
		t.Fatalf("a refused connection must be a result, not an error: %v", err)
	}
	if res.OK {
		t.Error("a refused connection was reported healthy")
	}
	// Summarised, not a paragraph of wrapped syscall errors.
	if !strings.Contains(res.Message, "refused") {
		t.Errorf("message %q does not say the connection was refused", res.Message)
	}
	if len(res.Message) > 80 {
		t.Errorf("the message is a paragraph where a phrase will do: %q", res.Message)
	}
}

func TestHTTPProbeTimesOutRatherThanHanging(t *testing.T) {
	// The handler waits for the client to give up rather than sleeping a
	// fixed two seconds: httptest.Server.Close blocks on outstanding
	// requests, so a sleep is paid in full on every run even though the
	// probe times out in a tenth of it.
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	res, err := health.NewHTTP().Check(ctx, spec(domain.HealthCheck{
		Name: "slow", Type: domain.HealthHTTP, URL: srv.URL,
	}))
	if err != nil {
		t.Fatalf("a timeout must be a result, not an error: %v", err)
	}
	if res.OK {
		t.Error("a service that never answered was reported healthy")
	}
	if !strings.Contains(res.Message, "timed out") {
		t.Errorf("message %q does not say it timed out", res.Message)
	}
}

func TestHTTPProbeRefusesAURLItCannotUse(t *testing.T) {
	res, err := health.NewHTTP().Check(context.Background(), spec(domain.HealthCheck{
		Name: "bad", Type: domain.HealthHTTP, URL: "://not a url",
	}))
	if err == nil && res.OK {
		t.Error("an unusable URL was reported healthy")
	}
}

// TestTCPProbeAgainstARealListener covers the prober that had no test at all.
func TestTCPProbeAgainstARealListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	// Accept and drop, which is all a TCP health check needs to see.
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	res, err := health.NewTCP().Check(context.Background(), spec(domain.HealthCheck{
		Name: "db", Type: domain.HealthTCP, Address: ln.Addr().String(),
	}))
	if err != nil {
		t.Fatalf("probing a live listener: %v", err)
	}
	if !res.OK {
		t.Errorf("a listening port was reported unhealthy: %s", res.Message)
	}
	if res.Duration <= 0 {
		t.Error("the result carries no duration, which status reports")
	}
}

func TestTCPProbeOnAClosedPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	res, err := health.NewTCP().Check(context.Background(), spec(domain.HealthCheck{
		Name: "db", Type: domain.HealthTCP, Address: addr,
	}))
	if err != nil {
		t.Fatalf("a closed port must be a result, not an error: %v", err)
	}
	if res.OK {
		t.Error("a closed port was reported healthy")
	}
}

func TestCommandProbeReportsTheExitStatus(t *testing.T) {
	prober := health.NewCommand(exec.New(), nil)
	dir := t.TempDir()

	cases := map[string]struct {
		script  string
		ok      bool
		message string
	}{
		"a passing check": {
			"#!/bin/sh\necho 'replica caught up'\nexit 0\n", true, "replica caught up",
		},
		"a failing check prefers stderr": {
			"#!/bin/sh\necho 'to stdout'\necho 'replica is behind' >&2\nexit 1\n",
			false, "replica is behind",
		},
		"a silent failure still says something": {
			"#!/bin/sh\nexit 3\n", false, "code 3",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			script := writeScript(t, dir, strings.ReplaceAll(name, " ", "_"), tc.script)

			res, err := prober.Check(context.Background(), ports.CheckSpec{
				Check: domain.HealthCheck{
					Name: "db", Type: domain.HealthCommand, Command: []string{script},
				},
				WorkingDir: dir,
			})
			if err != nil {
				t.Fatalf("a non-zero exit must be data, not an error: %v", err)
			}
			if res.OK != tc.ok {
				t.Errorf("OK = %v, want %v (%s)", res.OK, tc.ok, res.Message)
			}
			if !strings.Contains(res.Message, tc.message) {
				t.Errorf("message %q does not contain %q", res.Message, tc.message)
			}
		})
	}
}

// TestCommandProbeTruncatesAWallOfOutput keeps one chatty check from filling a
// status report.
func TestCommandProbeTruncatesAWallOfOutput(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "chatty",
		"#!/bin/sh\nhead -c 5000 /dev/zero | tr '\\0' 'x'\nexit 0\n")

	res, err := health.NewCommand(exec.New(), nil).Check(context.Background(),
		ports.CheckSpec{
			Check: domain.HealthCheck{
				Name: "chatty", Type: domain.HealthCommand, Command: []string{script},
			},
			WorkingDir: dir,
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Message) > 250 {
		t.Errorf("a %d-character message reached the status report", len(res.Message))
	}
}

func TestCommandProbeReportsAMissingExecutable(t *testing.T) {
	res, err := health.NewCommand(exec.New(), nil).Check(context.Background(),
		ports.CheckSpec{
			Check: domain.HealthCheck{
				Name: "gone", Type: domain.HealthCommand,
				Command: []string{"/nonexistent/hook"},
			},
			WorkingDir: t.TempDir(),
		})
	if err == nil && res.OK {
		t.Error("a missing health command was reported healthy")
	}
}

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := dir + "/" + name
	if err := writeExecutable(path, body); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeExecutable(path, body string) error {
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}
