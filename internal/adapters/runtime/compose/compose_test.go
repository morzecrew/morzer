package compose_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/morzecrew/morzer/internal/adapters/runtime/compose"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/fakes"
)

// The acceptance run drives this adapter against real Docker, which proves the
// happy paths and nothing else: a healthy daemon does not report a container
// that exited 137, a pull that cannot reach a registry, or a `ps` whose output
// shape changed between Compose versions.
//
// A scripted runner supplies those answers. What is under test is the adapter's
// reading of them -- which is all this adapter is.

func newRuntime() (*compose.Runtime, *fakes.Scripted) {
	runner := fakes.NewScripted()
	return compose.New(runner, compose.WithDockerBinary("/usr/bin/docker")), runner
}

func cfg() ports.RuntimeConfig {
	return ports.RuntimeConfig{
		Product:    "demo",
		Files:      []string{"/rel/compose.yaml"},
		WorkingDir: "/rel",
		Env:        map[string]string{"DEMO_PARAM_HTTP_PORT": "18080"},
	}
}

func TestValidateReturnsTheMergedConfigAndItsServices(t *testing.T) {
	r, runner := newRuntime()
	runner.OnOutput("config", `{"services":{"web":{},"api":{},"db":{}}}`)

	rendered, err := r.Validate(context.Background(), cfg())
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	// Sorted, so a plan and a diff are stable between runs.
	if got := strings.Join(rendered.Services, ","); got != "api,db,web" {
		t.Errorf("services = %q, want them sorted", got)
	}
	if len(rendered.Config) == 0 {
		t.Error("the merged configuration was not returned")
	}

	// The project and every file must reach the command line, or Compose
	// merges a different set than the release declares.
	if !runner.Ran("--project-name demo") || !runner.Ran("--file /rel/compose.yaml") {
		t.Errorf("the project or its files did not reach compose:\n%s", runner.CommandLines())
	}
}

func TestValidateReportsAnInvalidConfiguration(t *testing.T) {
	r, runner := newRuntime()
	runner.OnError("config", &exec.ExitError{
		ExitCode: 15,
		Stderr:   "services.web.ports: invalid port specification",
	})

	_, err := r.Validate(context.Background(), cfg())
	if err == nil {
		t.Fatal("an invalid compose file was accepted")
	}
	// The remedy names the command an operator can run to see the whole
	// diagnostic, rather than reprinting a truncated one.
	if !strings.Contains(err.Error(), "compose configuration is invalid") {
		t.Errorf("the error does not say what failed: %v", err)
	}
}

func TestValidateReportsOutputItCannotParse(t *testing.T) {
	r, runner := newRuntime()
	runner.OnOutput("config", "this is not json")

	if _, err := r.Validate(context.Background(), cfg()); err == nil {
		t.Fatal("unparseable compose output was accepted")
	}
}

// TestPullFetchesTheManifestsImages pins the defect the acceptance run found
// once already: `docker compose pull` ignores the image list it is given, so
// the release would pull whatever the Compose file happened to name.
func TestPullFetchesTheManifestsImages(t *testing.T) {
	r, runner := newRuntime()
	// Nothing is present locally, so everything must be fetched.
	runner.OnError("image inspect", &exec.ExitError{ExitCode: 1, Stderr: "No such image"})

	images := []string{
		"registry.example/demo/app@sha256:aaa",
		"registry.example/demo/db@sha256:bbb",
	}
	if err := r.Pull(context.Background(), cfg(), images); err != nil {
		t.Fatalf("Pull: %v", err)
	}

	for _, ref := range images {
		if !runner.Ran("pull " + ref) {
			t.Errorf("%s was never pulled:\n%s", ref, runner.CommandLines())
		}
	}
}

// TestPullSkipsWhatIsAlreadyLocal is what lets a machine boot without network.
func TestPullSkipsWhatIsAlreadyLocal(t *testing.T) {
	r, runner := newRuntime()
	// `image inspect` succeeding means the image is here already.
	runner.OnOutput("image inspect", "sha256:aaa")

	if err := r.Pull(context.Background(), cfg(),
		[]string{"registry.example/demo/app@sha256:aaa"}); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if runner.Ran("pull ") {
		t.Errorf("a digest-pinned image already present was pulled again:\n%s",
			runner.CommandLines())
	}
}

func TestPullReportsAnUnreachableRegistry(t *testing.T) {
	r, runner := newRuntime()
	runner.OnError("image inspect", &exec.ExitError{ExitCode: 1})
	runner.OnError("pull", &exec.ExitError{
		ExitCode: 1, Stderr: "failed to resolve reference: dial tcp: i/o timeout",
	})

	err := r.Pull(context.Background(), cfg(),
		[]string{"registry.example/demo/app@sha256:aaaaaaaabbbbbbbbcccccccc"})
	if err == nil {
		t.Fatal("an unreachable registry was ignored")
	}
	// The digest is 71 characters of noise in a message that already names
	// the failure, so the short form is what appears.
	if strings.Contains(err.Error(), "sha256:aaaaaaaabbbbbbbbcccccccc") {
		t.Errorf("the full digest was printed in the error: %v", err)
	}
	if !strings.Contains(err.Error(), "registry.example/demo/app") {
		t.Errorf("the error does not name the image: %v", err)
	}
}

func TestPullWithNoImagesDoesNothing(t *testing.T) {
	r, runner := newRuntime()
	if err := r.Pull(context.Background(), cfg(), nil); err != nil {
		t.Fatalf("Pull with no images: %v", err)
	}
	if len(runner.Calls()) != 0 {
		t.Errorf("an empty image list still ran something:\n%s", runner.CommandLines())
	}
}

func TestUpPassesItsOptionsThrough(t *testing.T) {
	r, runner := newRuntime()

	err := r.Up(context.Background(), cfg(), ports.UpOptions{
		Services: []string{"web"}, Wait: true, WaitTimeout: 90_000_000_000, RemoveOrphans: true,
	})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}

	line := strings.Join(runner.Calls()[0].Argv, " ")
	for _, want := range []string{"up", "--detach", "--wait", "--remove-orphans", "web"} {
		if !strings.Contains(line, want) {
			t.Errorf("the command line is missing %q: %s", want, line)
		}
	}
}

func TestUpReportsAServiceThatWillNotStart(t *testing.T) {
	r, runner := newRuntime()
	runner.OnError("up", &exec.ExitError{
		ExitCode: 1, Stderr: "dependency failed to start: container demo-db-1 is unhealthy",
	})

	err := r.Up(context.Background(), cfg(), ports.UpOptions{Wait: true})
	if err == nil {
		t.Fatal("a failed start was reported as success")
	}
	if !strings.Contains(err.Error(), "unhealthy") {
		t.Errorf("the daemon's reason did not survive: %v", err)
	}
}

func TestStatusParsesBothOutputShapes(t *testing.T) {
	// Compose has emitted a JSON array and newline-delimited objects at
	// different versions, and both are still in the wild.
	shapes := map[string]string{
		"a JSON array":              `[{"Service":"web","State":"running","Health":"healthy","Image":"app@sha256:a"}]`,
		"newline-delimited objects": `{"Service":"web","State":"running","Health":"healthy","Image":"app@sha256:a"}`,
	}

	for name, out := range shapes {
		t.Run(name, func(t *testing.T) {
			r, runner := newRuntime()
			runner.OnOutput("ps", out)

			states, err := r.Status(context.Background(), cfg())
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if len(states) != 1 || states[0].Name != "web" {
				t.Fatalf("states = %+v", states)
			}
			if states[0].Health != ports.HealthHealthy {
				t.Errorf("health = %q, want healthy", states[0].Health)
			}
			if !states[0].Running() {
				t.Error("a running service was not reported running")
			}
		})
	}
}

func TestStatusReportsAContainerThatDied(t *testing.T) {
	r, runner := newRuntime()
	runner.OnOutput("ps",
		`[{"Service":"db","State":"exited","ExitCode":137,"Status":"Exited (137) 2 minutes ago"}]`)

	states, err := r.Status(context.Background(), cfg())
	if err != nil {
		t.Fatal(err)
	}
	if states[0].Running() {
		t.Error("an exited container was reported running")
	}
	// 137 is a kill, usually the out-of-memory killer, and it is the whole
	// diagnosis. It must survive to the status report.
	if states[0].ExitCode != 137 {
		t.Errorf("exit code = %d, want 137", states[0].ExitCode)
	}
}

func TestStatusOnAProjectThatWasNeverStarted(t *testing.T) {
	r, runner := newRuntime()
	runner.OnOutput("ps", "")

	states, err := r.Status(context.Background(), cfg())
	if err != nil {
		t.Fatalf("an empty project must not be an error: %v", err)
	}
	if len(states) != 0 {
		t.Errorf("states = %+v, want none", states)
	}
}

func TestStatusReportsOutputItCannotParse(t *testing.T) {
	r, runner := newRuntime()
	runner.OnOutput("ps", "{not json")

	if _, err := r.Status(context.Background(), cfg()); err == nil {
		t.Fatal("unparseable ps output was accepted")
	}
}

// TestHealthVocabularyDistinguishesAbsenceFromIllness pins the distinction the
// status view depends on: a service with no healthcheck declared is not sick.
func TestHealthVocabularyDistinguishesAbsenceFromIllness(t *testing.T) {
	cases := map[string]ports.ServiceHealth{
		"healthy":   ports.HealthHealthy,
		"unhealthy": ports.HealthUnhealthy,
		"starting":  ports.HealthStarting,
		"":          ports.HealthNone,
		"something": ports.HealthUnknown,
	}

	for input, want := range cases {
		r, runner := newRuntime()
		runner.OnOutput("ps",
			`[{"Service":"web","State":"running","Health":"`+input+`"}]`)

		states, err := r.Status(context.Background(), cfg())
		if err != nil {
			t.Fatal(err)
		}
		if states[0].Health != want {
			t.Errorf("Health=%q read as %q, want %q", input, states[0].Health, want)
		}
	}
}

func TestRunOneShotReturnsANonZeroExitAsData(t *testing.T) {
	r, runner := newRuntime()
	runner.OnError("run", &exec.ExitError{
		ExitCode: 2, Stderr: "nothing to migrate",
	})

	res, err := r.RunOneShot(context.Background(), cfg(), "migrate", ports.RunOptions{
		Argv: []string{"/hooks/migrate"}, Remove: true,
	})
	// A migration exiting 2 means "nothing to do" under the hook ABI, so
	// the caller decides what it means -- not this adapter.
	if err != nil {
		t.Fatalf("a non-zero exit must be data, not an error: %v", err)
	}
	if res.ExitCode != 2 {
		t.Errorf("exit code = %d, want 2", res.ExitCode)
	}
}

func TestDownRemovesVolumesOnlyWhenAsked(t *testing.T) {
	r, runner := newRuntime()
	if err := r.Down(context.Background(), cfg(), ports.DownOptions{}); err != nil {
		t.Fatal(err)
	}
	if runner.Ran("--volumes") {
		t.Errorf("volumes were removed without being asked for:\n%s", runner.CommandLines())
	}

	r2, runner2 := newRuntime()
	if err := r2.Down(context.Background(), cfg(), ports.DownOptions{Volumes: true}); err != nil {
		t.Fatal(err)
	}
	if !runner2.Ran("--volumes") {
		t.Errorf("an explicit volume removal did not reach compose:\n%s", runner2.CommandLines())
	}
}

func TestHasImageDistinguishesPresentFromAbsent(t *testing.T) {
	present, runner := newRuntime()
	runner.OnOutput("image inspect", "sha256:aaa")
	if ok, err := present.HasImage(context.Background(), "app@sha256:aaa"); err != nil || !ok {
		t.Errorf("HasImage(present) = %v, %v", ok, err)
	}

	absent, runner2 := newRuntime()
	runner2.OnError("image inspect", &exec.ExitError{ExitCode: 1, Stderr: "No such image"})
	if ok, err := absent.HasImage(context.Background(), "app@sha256:bbb"); err != nil || ok {
		t.Errorf("HasImage(absent) = %v, %v; absence is not an error", ok, err)
	}
}

// The tools this adapter needs are its own answer to a question `doctor` asks
// before an installation exists. Asserted here because everything above this
// package sees the answer through a fake, and a fake with its own list is a
// list nobody has checked against the adapter that ships.
//
// Both names, in order. The daemon and the CLI plugin are separately
// installable and separately versioned -- a host with `docker` and no
// `compose` plugin is a machine that passes preflight and fails at the first
// operation with an error about an unknown subcommand.
func TestTheRuntimeNeedsTheDaemonAndTheCLIPlugin(t *testing.T) {
	r, _ := newRuntime()
	got := r.RequiredTools()
	if len(got) != 2 || got[0] != "docker" || got[1] != "compose" {
		t.Errorf("RequiredTools() = %v, want the daemon and the plugin", got)
	}
}

// The project is this adapter's to resolve, and the option is how a vendor
// says so. Asserted through argv rather than through the resolver, because
// argv is what actually decides which volumes a deployment gets.
func TestTheProjectComesFromTheOptionAndFallsBackToTheProduct(t *testing.T) {
	r, runner := newRuntime()
	runner.OnOutput("config", `{"services":{"web":{}}}`)

	cfg := ports.RuntimeConfig{Product: "demo", Files: []string{"/rel/compose.yaml"}}
	if _, err := r.Validate(context.Background(), cfg); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := strings.Join(runner.Calls()[0].Argv, " "); !strings.Contains(got, "--project-name demo") {
		t.Errorf("argv = %q, want the product name as the project", got)
	}

	r, runner = newRuntime()
	runner.OnOutput("config", `{"services":{"web":{}}}`)
	cfg.Options = map[string]string{"project": "myapp"}
	if _, err := r.Validate(context.Background(), cfg); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := strings.Join(runner.Calls()[0].Argv, " "); !strings.Contains(got, "--project-name myapp") {
		t.Errorf("argv = %q, want the declared project", got)
	}
}

// An option this runtime has never heard of is refused rather than ignored.
// The manager cannot make this refusal -- it holds no list of what any runtime
// understands -- so if it does not happen here it does not happen, and a
// mistyped `project` silently deploys under the product name instead.
func TestAnUnknownOptionIsRefused(t *testing.T) {
	r, runner := newRuntime()
	runner.OnOutput("config", `{"services":{"web":{}}}`)

	_, err := r.Validate(context.Background(), ports.RuntimeConfig{
		Product: "demo",
		Files:   []string{"/rel/compose.yaml"},
		Options: map[string]string{"porject": "myapp"},
	})
	if err == nil {
		t.Fatal("an unknown runtime option must be refused")
	}
	if !strings.Contains(err.Error(), "porject") {
		t.Errorf("error = %q, want it to name the option the vendor wrote", err)
	}
	if !strings.Contains(domain.AsError(err).Hint, "project") {
		t.Errorf("hint = %q, want it to name what this runtime does understand",
			domain.AsError(err).Hint)
	}
}

// The hook variable vendors have always read, under the name they read it by.
// What changed is who supplies it: the core ABI stopped promising a value a
// runtime without projects cannot mean.
func TestTheProjectIsSuppliedToHooksUnderItsPublishedName(t *testing.T) {
	r, _ := newRuntime()

	vars := r.HookVars(ports.RuntimeConfig{Product: "demo", Options: map[string]string{"project": "myapp"}})
	if vars["COMPOSE_PROJECT"] != "myapp" {
		t.Errorf("HookVars = %v, want COMPOSE_PROJECT=myapp", vars)
	}
	// Unprefixed: the product namespace is the manager's to apply, and an
	// adapter that built `DEMO_COMPOSE_PROJECT` would be a second
	// implementation of HookEnv.Var.
	if _, prefixed := vars["DEMO_COMPOSE_PROJECT"]; prefixed {
		t.Error("HookVars must return suffixes, not namespaced names")
	}
}

// The adapter's half of ports.OptionResolver, asserted here rather than only in
// the contract battery: the battery's real-adapter leg is behind the `docker`
// build tag, and a tagged tree is invisible to every lane that does not set it.
//
// What the manager does with this is refuse a release or apply it, so the rule
// is worth stating twice.
func TestResolveOptionsFillsInTheProjectTheRuntimeWouldUse(t *testing.T) {
	r, _ := newRuntime()

	t.Run("an absent project resolves to the product", func(t *testing.T) {
		resolved := r.ResolveOptions(ports.RuntimeConfig{Product: "demo"})
		assert.Equal(t, "demo", resolved[compose.OptionProject],
			"this is the value a deployment created without a project is already running under")
	})

	t.Run("a declared project is left alone", func(t *testing.T) {
		resolved := r.ResolveOptions(ports.RuntimeConfig{
			Product: "demo",
			Options: map[string]string{compose.OptionProject: "elsewhere"},
		})
		assert.Equal(t, "elsewhere", resolved[compose.OptionProject])
	})

	t.Run("writing out the default resolves the same as omitting it", func(t *testing.T) {
		omitted := r.ResolveOptions(ports.RuntimeConfig{Product: "demo"})
		explicit := r.ResolveOptions(ports.RuntimeConfig{
			Product: "demo",
			Options: map[string]string{compose.OptionProject: "demo"},
		})
		assert.Equal(t, omitted, explicit,
			"the manager compares these two, and refusing the second is the defect this closes")
	})

	t.Run("a key the runtime does not know survives", func(t *testing.T) {
		resolved := r.ResolveOptions(ports.RuntimeConfig{
			Product: "demo",
			Options: map[string]string{"unit_prefix": "a"},
		})
		assert.Equal(t, "a", resolved["unit_prefix"],
			"dropping it would hide a change the manager treats as durable")
	})
}

// The declared map belongs to the caller, and under `apply` it is the
// installation's own recorded baseline: `resolveRuntimeOptions` hands it in
// directly and `persistRuntimeBaseline` writes it back. An adapter that filled
// the project in place would therefore convert a deployment that declared no
// project into one that declares its own default, on disk, silently.
//
// Found by a sabotage that survived: the contract battery asserts this, but its
// real-adapter leg is behind the `docker` build tag, so no untagged lane ran it.
func TestResolveOptionsDoesNotEditTheCallersMap(t *testing.T) {
	r, _ := newRuntime()

	declared := map[string]string{"unit_prefix": "a"}
	resolved := r.ResolveOptions(ports.RuntimeConfig{Product: "demo", Options: declared})

	assert.Equal(t, map[string]string{"unit_prefix": "a"}, declared,
		"the caller's map must come back untouched")
	assert.Equal(t, "demo", resolved[compose.OptionProject],
		"and the copy must carry the resolved value")

	// A nil map is the other shape a caller can hand over, and it must not
	// become a map the caller now shares with the adapter.
	assert.NotPanics(t, func() {
		r.ResolveOptions(ports.RuntimeConfig{Product: "demo"})
	})
}
