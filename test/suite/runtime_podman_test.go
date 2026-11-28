//go:build podman

package suite

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/adapters/runtime/quadlet"
	infraexec "github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/contract"
)

// The Quadlet adapter against the user's own rootless Podman and systemd
// manager, which is RFC 0023's tested configuration (row 2). The suites are the
// ones Compose passes, unchanged (§8); only the fixtures differ, written as the
// Quadlet files a vendor would ship.
//
// It runs on a developer's machine, not in CI: no lane has been watched go red
// (RFC 0023 §12). Each test's units live in the user's runtime directory under
// a product name of its own, and are torn down with their volumes after it.

// The images the suite runs, pinned as dockerlab pins them, and fully
// qualified, since a short name may not resolve for a rootless user.
// dockerlab itself builds only under the docker tag.
const (
	quadletBusybox = "docker.io/library/busybox@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0"
	quadletRedis   = "docker.io/library/redis@sha256:978f0e01593e65eed801f2402944efcd936d43b5027e4908a7897baf88ed6241"
)

// requirePodman fails rather than skips: the build tag asked for these tests.
func requirePodman(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"podman", "systemctl"} {
		if _, err := osexec.LookPath(tool); err != nil {
			t.Fatalf("the podman build tag is set but %s is not on PATH: %v", tool, err)
		}
	}
	if os.Geteuid() == 0 {
		t.Fatal("the podman build tag runs rootless Podman, and this runs as root")
	}
	if out, err := osexec.Command("systemctl", "--user", "is-system-running").CombinedOutput(); err != nil &&
		!strings.Contains(string(out), "degraded") {
		t.Fatalf("the user's systemd manager does not answer: %v\n%s", err, out)
	}
}

// pullPodman makes the images local to the user's Podman.
func pullPodman(t *testing.T, refs ...string) {
	t.Helper()
	for _, ref := range refs {
		if osexec.Command("podman", "image", "exists", ref).Run() == nil {
			continue
		}
		if out, err := osexec.Command("podman", "pull", "--quiet", ref).CombinedOutput(); err != nil {
			t.Fatalf("podman pull %s: %v\n%s", ref, err, out)
		}
	}
}

// quadletProject writes Quadlet files under a product of the test's own, and
// tears the installation down after the test.
func quadletProject(t *testing.T, files map[string]string) (*quadlet.Runtime, ports.RuntimeConfig) {
	t.Helper()
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	product := "mzq" + hex.EncodeToString(b)

	dir := t.TempDir()
	cfg := ports.RuntimeConfig{Product: product, WorkingDir: dir}
	for name, text := range files {
		path := filepath.Join(dir, product+"-"+name)
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg.Files = append(cfg.Files, path)
	}
	rt := quadlet.New(infraexec.New())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := rt.Down(ctx, cfg, ports.DownOptions{Volumes: true}); err != nil {
			t.Logf("tearing down %s: %v", product, err)
		}
	})
	return rt, cfg
}

// mainProject is the fixture Compose's suite runs against, in Quadlet's
// words: one service that stays up with a named volume, one real listener,
// and a one-shot that exits non-zero on purpose.
func mainProject(t *testing.T) (*quadlet.Runtime, ports.RuntimeConfig) {
	t.Helper()
	busybox, redis := quadletBusybox, quadletRedis
	pullPodman(t, busybox, redis)
	rt, cfg := quadletProject(t, map[string]string{
		"web.container": fmt.Sprintf(`[Container]
Image=%s
RunInit=true
Exec=sh -c "echo web is up; while true; do sleep 1; done"
Volume=%%s-data.volume:/data
`, busybox),
		"cache.container": fmt.Sprintf(`[Container]
Image=%s
Exec=redis-server --save "" --appendonly no
`, redis),
		"migrate.container": fmt.Sprintf(`[Container]
Image=%s
Exec=sh -c "echo nothing to migrate; exit 2"

[Service]
Type=oneshot
`, busybox),
		"data.volume": "[Volume]\n",
	})
	return rt, fixVolumeRefs(t, cfg)
}

// fixVolumeRefs fills in the product a fixture's `%s-` placeholders name,
// which is not known until the project is.
func fixVolumeRefs(t *testing.T, cfg ports.RuntimeConfig) ports.RuntimeConfig {
	t.Helper()
	for _, path := range cfg.Files {
		text, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		fixed := strings.ReplaceAll(string(text), "%s-", cfg.Product+"-")
		if err := os.WriteFile(path, []byte(fixed), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return cfg
}

func TestRuntimeContract_Quadlet(t *testing.T) {
	requirePodman(t)
	pullPodman(t, "docker.io/library/"+quadletHelperImage())
	contract.RunRuntimeSuite(t, func(t *testing.T) (ports.Runtime, ports.RuntimeConfig) {
		return mainProject(t)
	})
}

// quadletHelperImage is the pinned helper image the volume operations run.
func quadletHelperImage() string { return quadlet.New(nil).HelperImage() }

func TestRecreateContract_Quadlet(t *testing.T) {
	requirePodman(t)
	busybox := quadletBusybox
	pullPodman(t, busybox)
	contract.RunRecreateSuite(t, func(t *testing.T) (ports.Runtime, ports.RuntimeConfig) {
		rt, cfg := quadletProject(t, map[string]string{
			"dep.container": fmt.Sprintf(`[Container]
Image=%s
RunInit=true
Exec=sh -c "while true; do sleep 1; done"
HealthCmd=false
HealthInterval=1s
HealthTimeout=2s
HealthRetries=1
`, busybox),
			"app.container": fmt.Sprintf(`[Unit]
Wants=%%s-dep.service
After=%%s-dep.service

[Container]
Image=%s
RunInit=true
Exec=sh -c "while true; do sleep 1; done"
`, busybox),
		})
		return rt, fixVolumeRefs(t, cfg)
	})
}

func TestStartKeepsValuesContract_Quadlet(t *testing.T) {
	requirePodman(t)
	busybox := quadletBusybox
	pullPodman(t, busybox)
	contract.RunStartKeepsValuesSuite(t, func(t *testing.T) (ports.Runtime, ports.RuntimeConfig) {
		return quadletProject(t, map[string]string{
			"app.container": fmt.Sprintf(`[Container]
Image=%s
RunInit=true
Exec=sh -c "while true; do sleep 1; done"
Environment=VALUE=${%s}
`, busybox, contract.ValueVar),
		})
	})
}

// TestQuadletDownKeepsTheVolumeAndDownWithVolumesDoesNot is the claim the
// compensation design rests on, as Compose's twin of it asserts. The second
// Down runs after the first removed the units, as a teardown after a failed
// update does, so the volume is named by the configuration alone.
func TestQuadletDownKeepsTheVolumeAndDownWithVolumesDoesNot(t *testing.T) {
	requirePodman(t)
	ctx := context.Background()
	rt, cfg := mainProject(t)
	volume := "systemd-" + cfg.Product + "-data"

	if err := rt.Up(ctx, cfg, ports.UpOptions{Wait: true}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Down(ctx, cfg, ports.DownOptions{}); err != nil {
		t.Fatal(err)
	}
	if osexec.Command("podman", "volume", "exists", volume).Run() != nil {
		t.Fatalf("Down without the volume flag removed %s", volume)
	}
	if err := rt.Down(ctx, cfg, ports.DownOptions{Volumes: true}); err != nil {
		t.Fatal(err)
	}
	if osexec.Command("podman", "volume", "exists", volume).Run() == nil {
		t.Errorf("Down with the volume flag left %s behind", volume)
	}
}

// TestQuadletDrivesThePodmanItsUnitsRunOn. systemd runs the units on this
// user's Podman, so a CONTAINER_HOST or CONTAINER_CONNECTION in the manager's
// environment must not send the adapter's own commands elsewhere (RFC 0023 row
// 61). Both name a service that does not exist, so a command that kept either
// fails.
func TestQuadletDrivesThePodmanItsUnitsRunOn(t *testing.T) {
	requirePodman(t)
	ctx := context.Background()
	rt, cfg := mainProject(t)
	t.Setenv("CONTAINER_HOST", "tcp://127.0.0.1:1")
	t.Setenv("CONTAINER_CONNECTION", "morzer-test-nowhere")

	if err := rt.Up(ctx, cfg, ports.UpOptions{Wait: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Status(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	res, err := rt.Exec(ctx, cfg, "web", []string{"echo", "here"})
	if err != nil || res.ExitCode != 0 || strings.TrimSpace(res.Stdout) != "here" {
		t.Fatalf("Exec: %+v, %v", res, err)
	}
	var out strings.Builder
	code, err := rt.ExecInteractive(ctx, cfg, "web", []string{"echo", "there"},
		ports.ExecIO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out})
	if err != nil || code != 0 || strings.TrimSpace(out.String()) != "there" {
		t.Fatalf("ExecInteractive: exit %d, %v\n%s", code, err, out.String())
	}
	if err := rt.Down(ctx, cfg, ports.DownOptions{Volumes: true}); err != nil {
		t.Fatal(err)
	}
}

// TestQuadletStopLeavesNoFailedUnit. A stop sends SIGTERM, and a container
// that ends on it exits 143; a unit counting that as a failure would leave the
// user's manager degraded after every backup's cold capture.
func TestQuadletStopLeavesNoFailedUnit(t *testing.T) {
	requirePodman(t)
	ctx := context.Background()
	rt, cfg := mainProject(t)
	if err := rt.Up(ctx, cfg, ports.UpOptions{Wait: true}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Stop(ctx, cfg, nil, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	out, _ := osexec.Command("systemctl", "--user", "list-units", "--failed", "--no-legend", cfg.Product+"-*").Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("a stop left units failed:\n%s", out)
	}
}

// TestQuadletUpRecreatesAServiceWhoseValuesChanged. A start of a running unit
// does nothing, so without the definition label a changed parameter would
// reach the container only at its next restart, where Compose re-creates it
// at the next `up`.
func TestQuadletUpRecreatesAServiceWhoseValuesChanged(t *testing.T) {
	requirePodman(t)
	ctx := context.Background()
	pullPodman(t, quadletBusybox)
	rt, cfg := quadletProject(t, map[string]string{
		"app.container": fmt.Sprintf(`[Container]
Image=%s
RunInit=true
Exec=sh -c "while true; do sleep 1; done"
Environment=VALUE=${%s}
`, quadletBusybox, contract.ValueVar),
	})
	value := func() string {
		res, err := rt.Exec(ctx, cfg, "app", []string{"sh", "-c", `printf %s "$VALUE"`})
		if err != nil {
			t.Fatal(err)
		}
		return res.Stdout
	}
	cfg.Env = map[string]string{contract.ValueVar: "before"}
	if err := rt.Up(ctx, cfg, ports.UpOptions{Wait: true}); err != nil {
		t.Fatal(err)
	}
	if got := value(); got != "before" {
		t.Fatalf("the service read %q, not the value it was created with", got)
	}
	cfg.Env = map[string]string{contract.ValueVar: "after"}
	if err := rt.Up(ctx, cfg, ports.UpOptions{Wait: true}); err != nil {
		t.Fatal(err)
	}
	if got := value(); got != "after" {
		t.Errorf("after an Up with a changed value the service reads %q", got)
	}
}

// TestQuadletRunOneShotKeepsItsStreamsApart. Facts are read from a one-shot's
// stdout alone, so stderr must not reach it; and the argv, the environment and
// the mounts a task passes must reach the container as given, `$` and `%`
// included, which systemd would otherwise expand.
func TestQuadletRunOneShotKeepsItsStreamsApart(t *testing.T) {
	requirePodman(t)
	ctx := context.Background()
	rt, cfg := mainProject(t)
	input := filepath.Join(t.TempDir(), "in.txt")
	if err := os.WriteFile(input, []byte("from the host"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := rt.RunOneShot(ctx, cfg, "migrate", ports.RunOptions{
		Argv:   []string{"sh", "-c", `echo out; echo err >&2; printf '%s|%s\n' "$X" 100%; cat /in/in.txt; exit 3`},
		Env:    map[string]string{"X": "a $dollar"},
		Mounts: []ports.Mount{{Source: filepath.Dir(input), Target: "/in", ReadOnly: true}},
		Remove: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 {
		t.Errorf("the one-shot exited %d, want 3", res.ExitCode)
	}
	if want := "out\na $dollar|100%\nfrom the host"; res.Stdout != want {
		t.Errorf("stdout is %q, want %q", res.Stdout, want)
	}
	if strings.TrimSpace(res.Stderr) != "err" {
		t.Errorf("stderr is %q, want only what the command wrote there", res.Stderr)
	}
	out, _ := osexec.Command("systemctl", "--user", "list-units", "--all", "--no-legend", cfg.Product+"-migrate-run-*").Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("the one-shot's transient unit is still loaded:\n%s", out)
	}
}

// loop is a service that stays up until it is stopped.
const loop = `Exec=sh -c "while true; do sleep 1; done"`

// TestQuadletUpEndsTheWaitWhenAServiceStops. A unit that ended after its start
// will not bring its container up, so the wait says so at once rather than at
// its deadline.
func TestQuadletUpEndsTheWaitWhenAServiceStops(t *testing.T) {
	requirePodman(t)
	ctx := context.Background()
	pullPodman(t, quadletBusybox)
	rt, cfg := quadletProject(t, map[string]string{
		// Starting for its health check's start period, then gone, so
		// only the unit can say it will not come up.
		"app.container": fmt.Sprintf("[Container]\nImage=%s\nExec=sh -c \"sleep 2; exit 1\"\n"+
			"HealthCmd=/bin/false\nHealthStartPeriod=5m\nHealthInterval=1s\n", quadletBusybox),
	})
	began := time.Now()
	err := rt.Up(ctx, cfg, ports.UpOptions{Wait: true, WaitTimeout: 3 * time.Minute})
	if err == nil || !strings.Contains(err.Error(), "stopped while it was starting") {
		t.Fatalf("Up returned %v, want the unit named as stopped", err)
	}
	if waited := time.Since(began); waited > time.Minute {
		t.Errorf("the wait ran %s before it noticed", waited)
	}
}

// TestQuadletUpReplacesARenamedContainer. Podman's stop removes a container
// by name, so a unit whose ContainerName= changed is stopped under its old
// definition before the reload; otherwise the old container would go on.
func TestQuadletUpReplacesARenamedContainer(t *testing.T) {
	requirePodman(t)
	ctx := context.Background()
	pullPodman(t, quadletBusybox)
	rt, cfg := quadletProject(t, map[string]string{
		"app.container": fmt.Sprintf("[Container]\nImage=%s\nRunInit=true\n%s\nContainerName=%%s-app-old\n", quadletBusybox, loop),
	})
	cfg = fixVolumeRefs(t, cfg)
	if err := rt.Up(ctx, cfg, ports.UpOptions{Wait: true, WaitTimeout: 2 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(cfg.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.Files[0], []byte(strings.Replace(string(text), "-app-old", "-app-new", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rt.Up(ctx, cfg, ports.UpOptions{Wait: true, WaitTimeout: 2 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	out, err := osexec.Command("podman", "ps", "--format", "{{.Names}}").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), cfg.Product+"-app-old") {
		t.Errorf("the container under its old name still runs:\n%s", out)
	}
	if !strings.Contains(string(out), cfg.Product+"-app-new") {
		t.Errorf("the container under its new name does not run:\n%s", out)
	}
}

// TestQuadletTargetedUpRecreatesWhatTheValuesChange. Up writes every
// service's files and the shared parameter copy, so a service outside the
// named ones whose definition changes is recreated too; otherwise its next
// Start would run what no Up started.
func TestQuadletTargetedUpRecreatesWhatTheValuesChange(t *testing.T) {
	requirePodman(t)
	ctx := context.Background()
	pullPodman(t, quadletBusybox)
	service := fmt.Sprintf("[Container]\nImage=%s\nRunInit=true\n%s\nEnvironment=VALUE=${%s}\n",
		quadletBusybox, loop, contract.ValueVar)
	rt, cfg := quadletProject(t, map[string]string{"web.container": service, "db.container": service})
	value := func(svc string) string {
		res, err := rt.Exec(ctx, cfg, svc, []string{"sh", "-c", `printf %s "$VALUE"`})
		if err != nil {
			t.Fatal(err)
		}
		return res.Stdout
	}
	cfg.Env = map[string]string{contract.ValueVar: "before"}
	if err := rt.Up(ctx, cfg, ports.UpOptions{Wait: true}); err != nil {
		t.Fatal(err)
	}
	cfg.Env = map[string]string{contract.ValueVar: "after"}
	if err := rt.Up(ctx, cfg, ports.UpOptions{Services: []string{"web"}, Wait: true}); err != nil {
		t.Fatal(err)
	}
	if got := value("web"); got != "after" {
		t.Errorf("the named service reads %q", got)
	}
	if got := value("db"); got != "after" {
		t.Errorf("the service outside the named ones reads %q, while its unit now names another value", got)
	}
}

// TestQuadletOneShotOfANamedContainerLeavesTheServiceRunning. The copy a
// one-shot runs names a container of its own: Quadlet runs with --replace, so
// under the service's name it would take the running service's place. Its
// output is scrubbed as the runner scrubs, and a status systemd counts as a
// success still reaches the caller.
func TestQuadletOneShotOfANamedContainerLeavesTheServiceRunning(t *testing.T) {
	requirePodman(t)
	ctx := context.Background()
	pullPodman(t, quadletBusybox)
	rt, cfg := quadletProject(t, map[string]string{
		"app.container": fmt.Sprintf("[Container]\nImage=%s\nRunInit=true\n%s\nContainerName=%%s-app-1\n\n[Service]\nSuccessExitStatus=4\n",
			quadletBusybox, loop),
	})
	cfg = fixVolumeRefs(t, cfg)
	if err := rt.Up(ctx, cfg, ports.UpOptions{Wait: true, WaitTimeout: 2 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	scrubbed := quadlet.New(infraexec.New(), quadlet.WithRedaction([]string{"s3cr3t-value"}))
	res, err := scrubbed.RunOneShot(ctx, cfg, "app", ports.RunOptions{
		Argv: []string{"sh", "-c", "echo the secret is s3cr3t-value; exit 4"}, Remove: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Stdout+res.Output, "s3cr3t-value") {
		t.Errorf("the one-shot's output carries a redacted value: %q", res.Stdout)
	}
	if res.ExitCode != 4 {
		t.Errorf("the one-shot exited %d, want 4, which the unit counts as a success", res.ExitCode)
	}
	out, err := osexec.Command("podman", "ps", "--format", "{{.Names}}").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), cfg.Product+"-app-1") {
		t.Errorf("the service's container no longer runs after the one-shot:\n%s", out)
	}
}
