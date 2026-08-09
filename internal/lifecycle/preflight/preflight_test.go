package preflight_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/infra/tools"
	"github.com/morzecrew/morzer/internal/lifecycle/preflight"
)

// Preflight is where an operator finds out that a deployment will not work,
// and the whole value of it is in finding out *before* anything has changed.
// A check that silently passes on a machine it should have failed is worse
// than no check: it converts a message into an incident at step nine of an
// apply, with images pulled and migrations run.
//
// Every check below is driven to each of its outcomes.

func statuses(report preflight.Report) map[string]events.CheckStatus {
	out := make(map[string]events.CheckStatus, len(report.Results))
	for _, r := range report.Results {
		out[r.ID] = r.Status
	}
	return out
}

func run(t *testing.T, checks ...preflight.Check) preflight.Report {
	t.Helper()
	return preflight.NewRunner(nil).Run(context.Background(), checks)
}

func always(status events.CheckStatus, message string) func(context.Context) events.CheckResult {
	return func(context.Context) events.CheckResult {
		switch status {
		case events.CheckOK:
			return preflight.OK("%s", message)
		case events.CheckWarn:
			return preflight.Warn("a remedy", "%s", message)
		default:
			return preflight.Fail("a remedy", "%s", message)
		}
	}
}

// TestEveryCheckRunsEvenAfterOneFails is the difference between fixing one
// problem and fixing them one round trip at a time.
func TestEveryCheckRunsEvenAfterOneFails(t *testing.T) {
	report := run(t,
		preflight.Check{ID: "first", Fatal: true, Run: always(events.CheckFail, "no")},
		preflight.Check{ID: "second", Fatal: true, Run: always(events.CheckFail, "also no")},
		preflight.Check{ID: "third", Run: always(events.CheckOK, "fine")},
	)

	if len(report.Results) != 3 {
		t.Fatalf("%d of 3 checks ran; an operator gets a partial list", len(report.Results))
	}
	if !report.Failed() {
		t.Error("a fatal failure did not fail the report")
	}
	if report.Worst != events.CheckFail {
		t.Errorf("Worst = %v, want fail", report.Worst)
	}
}

// TestANonFatalFailureIsAWarning: the release said it wanted something, the
// machine does not have it, and the operator gets to decide.
func TestANonFatalFailureIsAWarning(t *testing.T) {
	report := run(t,
		preflight.Check{ID: "advisory", Fatal: false, Run: always(events.CheckFail, "would like more RAM")},
	)

	if got := statuses(report)["advisory"]; got != events.CheckWarn {
		t.Errorf("status = %v, want warn: a non-fatal check must not be able to "+
			"stop an operation", got)
	}
	if report.Failed() {
		t.Error("a downgraded warning still failed the report")
	}
	if err := report.Err(); err != nil {
		t.Errorf("a report with only warnings produced an error: %v", err)
	}
}

func TestACheckInheritsTheDescriptionItDidNotSet(t *testing.T) {
	report := run(t, preflight.Check{
		ID: "x", Category: "system", Description: "the declared description",
		Run: always(events.CheckOK, "fine"),
	})

	if got := report.Results[0].Description; got != "the declared description" {
		t.Errorf("description = %q; the report would name nothing", got)
	}
	if report.Results[0].Category != "system" {
		t.Error("the category was lost, so the doctor view cannot group the result")
	}
	if report.Results[0].Duration <= 0 {
		t.Error("no duration was recorded, and a slow check is a finding of its own")
	}
}

// TestAnInterruptedRunStopsRatherThanContinuing. Ctrl-C during preflight has
// to stop preflight.
func TestAnInterruptedRunStopsRatherThanContinuing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	report := preflight.NewRunner(nil).Run(ctx, []preflight.Check{
		{ID: "one", Run: always(events.CheckOK, "fine")},
	})

	if len(report.Results) != 0 {
		t.Errorf("%d checks ran after cancellation", len(report.Results))
	}
}

// TestTheReportNamesEveryBlockerAndItsRemedy is the error an operator reads.
func TestTheReportNamesEveryBlockerAndItsRemedy(t *testing.T) {
	report := run(t,
		preflight.Check{ID: "a", Description: "docker is installed", Fatal: true,
			Run: func(context.Context) events.CheckResult {
				return preflight.Fail("install docker", "not found on PATH")
			}},
		preflight.Check{ID: "b", Description: "port is free", Fatal: true,
			Run: func(context.Context) events.CheckResult {
				return preflight.Fail("", "port 443 already in use")
			}},
		preflight.Check{ID: "c", Description: "memory", Run: always(events.CheckOK, "8 GiB")},
	)

	err := report.Err()
	if err == nil {
		t.Fatal("two fatal failures produced no error")
	}
	de := domain.AsError(err)
	if de.Code != domain.CodePreflight {
		t.Errorf("code = %v, want the preflight code: the exit status is how a "+
			"script tells a refusal from a crash", de.Code)
	}
	for _, want := range []string{"docker is installed", "not found on PATH",
		"install docker", "port is free", "port 443 already in use"} {
		if !strings.Contains(de.Message, want) {
			t.Errorf("the message drops %q, so the operator fixes one problem "+
				"and discovers the next on the retry:\n%s", want, de.Message)
		}
	}
	if strings.Contains(de.Message, "8 GiB") {
		t.Error("a passing check appears in the list of blockers")
	}
	if de.Hint == "" {
		t.Error("the refusal points nowhere")
	}
}

func TestTheBusSeesEveryResult(t *testing.T) {
	bus := events.NewBus()
	var seen []string
	unsubscribe := bus.SubscribeFunc(func(e events.Event) {
		if e.Check != nil {
			seen = append(seen, e.Check.ID)
		}
	})
	defer unsubscribe()

	preflight.NewRunner(bus).Run(context.Background(), []preflight.Check{
		{ID: "one", Run: always(events.CheckOK, "fine")},
		{ID: "two", Run: always(events.CheckFail, "no")},
	})

	if len(seen) != 2 {
		t.Errorf("the bus saw %d of 2 results, so the live view shows a partial "+
			"run: %v", len(seen), seen)
	}
}

func TestArchitecture(t *testing.T) {
	cases := map[string]struct {
		req  domain.Requirements
		want events.CheckStatus
	}{
		"no requirement stated": {
			domain.Requirements{}, events.CheckOK,
		},
		"this machine's architecture is listed": {
			domain.Requirements{Architectures: []string{"riscv64", runtime.GOARCH}}, events.CheckOK,
		},
		"a bundle for something else": {
			domain.Requirements{Architectures: []string{"s390x"}}, events.CheckFail,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			check := preflight.Architecture(tc.req)
			if !check.Fatal {
				t.Error("an architecture mismatch must be fatal: nothing in the " +
					"bundle would run")
			}
			res := check.Run(context.Background())
			if res.Status != tc.want {
				t.Errorf("status = %v, want %v (%s)", res.Status, tc.want, res.Message)
			}
			if tc.want == events.CheckFail {
				if !strings.Contains(res.Message, runtime.GOARCH) {
					t.Errorf("message %q does not say what this machine is", res.Message)
				}
				if res.Remedy == "" {
					t.Error("the operator is told no and nothing else")
				}
			}
		})
	}
}

// TestOperatingSystemIsNeverFatal. The OS list is what the vendor tested, not
// a technical bound, so refusing on it would be the manager overreaching.
func TestOperatingSystemIsNeverFatal(t *testing.T) {
	check := preflight.OperatingSystem(domain.Requirements{
		OS: []domain.OSRequirement{{ID: "plan9"}},
	})
	if check.Fatal {
		t.Fatal("an untested-but-compatible distribution must not be refused")
	}

	res := check.Run(context.Background())
	if res.Status == events.CheckOK {
		t.Skip("this machine really is plan9, which would be a surprise")
	}
	if res.Status != events.CheckWarn {
		t.Errorf("status = %v, want warn", res.Status)
	}
	if !strings.Contains(res.Message, "tested OS list") {
		t.Errorf("message %q does not explain why it is a warning", res.Message)
	}
}

func TestOperatingSystemWithNoRequirementPasses(t *testing.T) {
	res := preflight.OperatingSystem(domain.Requirements{}).Run(context.Background())
	if res.Status != events.CheckOK {
		t.Errorf("status = %v for a release that states no OS requirement", res.Status)
	}
}

// TestOperatingSystemAcceptsThisMachine reads the real /etc/os-release, which
// is the code path every install actually takes.
func TestOperatingSystemAcceptsThisMachine(t *testing.T) {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		t.Skipf("no /etc/os-release on this machine: %v", err)
	}
	var id string
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok && k == "ID" {
			id = strings.Trim(v, `"'`)
		}
	}
	if id == "" {
		t.Skip("this machine's /etc/os-release declares no ID")
	}

	res := preflight.OperatingSystem(domain.Requirements{
		OS: []domain.OSRequirement{{ID: id}},
	}).Run(context.Background())

	if res.Status != events.CheckOK {
		t.Errorf("the host OS %q was not recognised as itself: %s (%v)",
			id, res.Message, res.Status)
	}
}

// TestOperatingSystemVersionRangeCoversBothSides drives the version
// comparison, including the "not comparable" branch a rolling release hits.
func TestOperatingSystemVersionRangeCoversBothSides(t *testing.T) {
	_, version := hostOSForTest(t)
	if version == "" {
		t.Skip("this machine states no VERSION_ID, so there is no range to compare")
	}
	id, _ := hostOSForTest(t)

	// A range nothing could satisfy: the check must warn, not pass.
	impossible, err := domain.ParseConstraint(">=9999.0.0")
	if err != nil {
		t.Fatal(err)
	}
	res := preflight.OperatingSystem(domain.Requirements{
		OS: []domain.OSRequirement{{ID: id, Version: impossible}},
	}).Run(context.Background())

	if res.Status == events.CheckOK {
		t.Errorf("a version range this machine cannot satisfy passed: %s", res.Message)
	}
	if res.Status == events.CheckFail {
		t.Error("an OS version mismatch must be a warning, not a blocker")
	}
}

func hostOSForTest(t *testing.T) (id, version string) {
	t.Helper()
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		t.Skipf("no /etc/os-release: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "ID":
			id = v
		case "VERSION_ID":
			version = v
		}
	}
	return id, version
}

func TestToolReportsWhatIsMissing(t *testing.T) {
	registry := tools.NewRegistry(exec.New())

	check := preflight.Tool(registry, "definitely-not-installed-xyzzy", domain.Constraint{})
	if !check.Fatal {
		t.Error("a missing required tool must be fatal")
	}
	res := check.Run(context.Background())
	if res.Status != events.CheckFail {
		t.Fatalf("a tool that is not installed passed: %s", res.Message)
	}
	if res.Message == "" {
		t.Error("the failure says nothing")
	}
	if !strings.Contains(check.Description, "definitely-not-installed-xyzzy") {
		t.Errorf("the description %q does not name the tool", check.Description)
	}
}

func TestToolsAreBuiltInAStableOrder(t *testing.T) {
	registry := tools.NewRegistry(exec.New())
	req := domain.Requirements{Tools: map[string]domain.Constraint{
		"zsh": {}, "docker": {}, "awk": {},
	}}

	checks := preflight.Tools(registry, req)
	if len(checks) != 3 {
		t.Fatalf("got %d checks for 3 tools", len(checks))
	}
	// Sorted, so a doctor report does not reshuffle between runs and make a
	// diff of two reports unreadable.
	want := []string{"tools.awk", "tools.docker", "tools.zsh"}
	for i, id := range want {
		if checks[i].ID != id {
			t.Errorf("check %d is %s, want %s", i, checks[i].ID, id)
		}
	}
}

func TestToolsOnAReleaseThatRequiresNone(t *testing.T) {
	if got := preflight.Tools(tools.NewRegistry(exec.New()), domain.Requirements{}); len(got) != 0 {
		t.Errorf("got %d checks for no required tools", len(got))
	}
}

func TestDisk(t *testing.T) {
	dir := t.TempDir()

	t.Run("no requirement stated", func(t *testing.T) {
		res := preflight.Disk(dir, 0).Run(context.Background())
		if res.Status != events.CheckOK {
			t.Errorf("status = %v with no disk requirement", res.Status)
		}
	})

	t.Run("a requirement this machine meets", func(t *testing.T) {
		res := preflight.Disk(dir, domain.ByteSize(1024)).Run(context.Background())
		if res.Status != events.CheckOK {
			t.Errorf("1 KiB was reported unavailable: %s", res.Message)
		}
		if !strings.Contains(res.Message, "free") {
			t.Errorf("message %q does not say how much is free", res.Message)
		}
	})

	t.Run("a requirement no machine meets", func(t *testing.T) {
		// An exabyte. Fatal, because running out of disk halfway through
		// an extraction is the failure this exists to prevent.
		check := preflight.Disk(dir, domain.ByteSize(1)<<60)
		if !check.Fatal {
			t.Error("insufficient disk must be fatal")
		}
		res := check.Run(context.Background())
		if res.Status != events.CheckFail {
			t.Fatalf("this machine claims an exabyte free: %s", res.Message)
		}
		if res.Remedy == "" {
			t.Error("the operator is told no and nothing else")
		}
	})
}

// TestFreeSpaceWalksUpToAnExistingAncestor is what makes the check work on a
// fresh install, where the data directory does not exist yet.
func TestFreeSpaceWalksUpToAnExistingAncestor(t *testing.T) {
	deep := filepath.Join(t.TempDir(), "not", "created", "yet", "data")

	free, err := preflight.FreeSpace(deep)
	if err != nil {
		t.Fatalf("a path that does not exist yet could not be measured: %v", err)
	}
	if free <= 0 {
		t.Errorf("free space is %d on a filesystem that plainly has some", free)
	}
}

func TestPorts(t *testing.T) {
	t.Run("a release that needs no ports", func(t *testing.T) {
		res := preflight.Ports(nil).Run(context.Background())
		if res.Status != events.CheckOK {
			t.Errorf("status = %v with no required ports", res.Status)
		}
	})

	t.Run("a port nothing is holding", func(t *testing.T) {
		ln, err := net.Listen("tcp", ":0")
		if err != nil {
			t.Fatal(err)
		}
		addr, ok := ln.Addr().(*net.TCPAddr)
		if !ok {
			t.Fatalf("listener address is %T, not TCP", ln.Addr())
		}
		port := addr.Port
		_ = ln.Close()

		res := preflight.Ports([]int{port}).Run(context.Background())
		if res.Status != events.CheckOK {
			t.Errorf("a free port was reported occupied: %s", res.Message)
		}
	})

	t.Run("a port something is holding", func(t *testing.T) {
		ln, err := net.Listen("tcp", ":0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = ln.Close() }()
		addr, ok := ln.Addr().(*net.TCPAddr)
		if !ok {
			t.Fatalf("listener address is %T, not TCP", ln.Addr())
		}
		port := addr.Port

		check := preflight.Ports([]int{port})
		if !check.Fatal {
			t.Error("an occupied port must be fatal: the container will not start")
		}
		res := check.Run(context.Background())
		if res.Status != events.CheckFail {
			t.Fatalf("a port held by this very process was reported free: %s", res.Message)
		}
		if !strings.Contains(res.Message, strconv.Itoa(port)) {
			t.Errorf("message %q does not name the port, so the operator cannot "+
				"go and find what is holding it", res.Message)
		}
		if !strings.Contains(res.Remedy, "ss -tlnp") {
			t.Errorf("remedy %q does not say how to find the listener", res.Remedy)
		}
	})
}

func TestDirectories(t *testing.T) {
	root := t.TempDir()
	paths := domain.PathsUnder(root, "demo")

	t.Run("nothing created yet", func(t *testing.T) {
		check := preflight.Directories(paths)
		if check.Fatal {
			t.Error("a wrong layout is reported by doctor and repaired by init; " +
				"it must not be a blocker on its own")
		}
		res := check.Run(context.Background())
		if res.Status != events.CheckWarn {
			t.Fatalf("status = %v on a machine with nothing created: %s",
				res.Status, res.Message)
		}
		if !strings.Contains(res.Message, "missing") {
			t.Errorf("message %q does not say the directories are missing", res.Message)
		}
		if !strings.Contains(res.Remedy, "--repair") {
			t.Errorf("remedy %q does not point at the repair path", res.Remedy)
		}
	})

	t.Run("everything created correctly", func(t *testing.T) {
		for _, dir := range paths.ManagedDirs() {
			if err := os.MkdirAll(dir.Path, os.FileMode(dir.Mode)); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir.Path, os.FileMode(dir.Mode)); err != nil {
				t.Fatal(err)
			}
		}
		res := preflight.Directories(paths).Run(context.Background())
		if res.Status != events.CheckOK {
			t.Fatalf("a correct layout was reported wrong: %s", res.Message)
		}
	})

	t.Run("a directory with the wrong mode", func(t *testing.T) {
		dirs := paths.ManagedDirs()
		if err := os.Chmod(dirs[0].Path, 0o777); err != nil {
			t.Fatal(err)
		}
		res := preflight.Directories(paths).Run(context.Background())
		if res.Status != events.CheckWarn {
			t.Fatalf("a world-writable managed directory was reported correct")
		}
		if !strings.Contains(res.Message, "mode") {
			t.Errorf("message %q does not name the mode", res.Message)
		}
	})

	t.Run("a file where a directory belongs", func(t *testing.T) {
		other := domain.PathsUnder(t.TempDir(), "demo")
		dirs := other.ManagedDirs()
		if err := os.MkdirAll(filepath.Dir(dirs[0].Path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dirs[0].Path, []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		res := preflight.Directories(other).Run(context.Background())
		if !strings.Contains(res.Message, "not a directory") {
			t.Errorf("message %q does not distinguish a file from a missing "+
				"directory, and the remedies differ", res.Message)
		}
	})
}

func TestSecretsPresent(t *testing.T) {
	schema := domain.SecretSchema{Secrets: []domain.SecretDeclaration{
		{Name: "db_password", Required: true},
		{Name: "session_key", Required: true},
		{Name: "smtp_password", Required: false},
	}}

	t.Run("everything required is set", func(t *testing.T) {
		set := domain.NewSecretSet(map[string]domain.Secret{
			"db_password": domain.NewSecret("a"),
			"session_key": domain.NewSecret("b"),
		})
		res := preflight.SecretsPresent(schema, set).Run(context.Background())
		if res.Status != events.CheckOK {
			t.Errorf("a complete secret set was reported incomplete: %s", res.Message)
		}
	})

	t.Run("a required secret is missing", func(t *testing.T) {
		set := domain.NewSecretSet(map[string]domain.Secret{
			"db_password": domain.NewSecret("a"),
		})
		check := preflight.SecretsPresent(schema, set)
		if !check.Fatal {
			t.Error("a missing required secret must be fatal: the product cannot start")
		}
		res := check.Run(context.Background())
		if res.Status != events.CheckFail {
			t.Fatalf("a missing required secret passed: %s", res.Message)
		}
		if !strings.Contains(res.Message, "session_key") {
			t.Errorf("message %q does not name what is missing", res.Message)
		}
		if strings.Contains(res.Message, "smtp_password") {
			t.Errorf("message %q demands an optional secret", res.Message)
		}
		// The values themselves must never appear -- this message goes
		// to the log and to `--json`.
		if strings.Contains(res.Message, "a") && strings.Contains(res.Message, "=") {
			t.Errorf("message %q looks like it carries a value", res.Message)
		}
	})
}

func TestMemory(t *testing.T) {
	t.Run("no requirement stated", func(t *testing.T) {
		res := preflight.Memory(0).Run(context.Background())
		if res.Status != events.CheckOK {
			t.Errorf("status = %v with no memory requirement", res.Status)
		}
	})

	t.Run("a requirement this machine meets", func(t *testing.T) {
		res := preflight.Memory(domain.ByteSize(1) << 20).Run(context.Background())
		if res.Status != events.CheckOK {
			t.Errorf("1 MiB was reported insufficient: %s", res.Message)
		}
	})

	t.Run("more memory than any machine has", func(t *testing.T) {
		check := preflight.Memory(domain.ByteSize(1) << 60)
		if check.Fatal {
			t.Error("total RAM says nothing about swap or about what the " +
				"product needs at rest; refusing on it would be guessing")
		}
		res := check.Run(context.Background())
		if res.Status != events.CheckWarn {
			t.Fatalf("this machine claims an exabyte of RAM: %s", res.Message)
		}
		if res.Remedy == "" {
			t.Error("the warning says nothing about what might go wrong")
		}
	})
}

func TestCPUs(t *testing.T) {
	t.Run("no requirement stated", func(t *testing.T) {
		res := preflight.CPUs(0).Run(context.Background())
		if res.Status != events.CheckOK {
			t.Errorf("status = %v with no CPU requirement", res.Status)
		}
	})

	t.Run("a requirement every machine meets", func(t *testing.T) {
		res := preflight.CPUs(1).Run(context.Background())
		if res.Status != events.CheckOK {
			t.Errorf("one CPU was reported insufficient: %s", res.Message)
		}
	})

	t.Run("more CPUs than any machine has", func(t *testing.T) {
		check := preflight.CPUs(1 << 20)
		if check.Fatal {
			t.Error("a machine with fewer cores than recommended is slow, not " +
				"broken; refusing would overrule an operator who may know " +
				"their workload better than the vendor's guess")
		}
		res := check.Run(context.Background())
		if res.Status != events.CheckWarn {
			t.Fatalf("this machine claims a million CPUs: %s", res.Message)
		}
	})
}

// TestRequiredParameters is what makes `required` more than decorative.
//
// Fatal, and checked on every apply rather than only where a value can be
// supplied: a release that introduces a required parameter must fail to deploy
// rather than deploy with an empty value the product then misreads. The
// current release keeps running, which is the safe side of that trade.
func TestRequiredParameters(t *testing.T) {
	declared := map[string]domain.ParameterSpec{
		"admin_email": {Type: domain.ParamString, Required: true},
		"http_port":   {Type: domain.ParamPort, Default: "8080"},
	}

	t.Run("unset", func(t *testing.T) {
		check := preflight.RequiredParameters(declared, map[string]string{})
		if !check.Fatal {
			t.Error("deploying with a required parameter unset must not be a warning")
		}
		res := check.Run(context.Background())
		if res.Status != events.CheckFail {
			t.Fatalf("status = %v with a required parameter unset", res.Status)
		}
		if !strings.Contains(res.Message, "admin_email") {
			t.Errorf("the refusal does not name the parameter: %s", res.Message)
		}
		// The one with a default must not be dragged in: it is not
		// required, and naming it would send the operator to set a
		// value the vendor already chose.
		if strings.Contains(res.Message, "http_port") {
			t.Errorf("a parameter with a default was reported missing: %s", res.Message)
		}
	})

	t.Run("set", func(t *testing.T) {
		res := preflight.RequiredParameters(declared,
			map[string]string{"admin_email": "ops@example"}).Run(context.Background())
		if res.Status != events.CheckOK {
			t.Errorf("status = %v with every required parameter set: %s", res.Status, res.Message)
		}
	})

	t.Run("set to whitespace", func(t *testing.T) {
		// An empty value dressed up. The product receives "" either
		// way, so accepting this would make the check performative.
		res := preflight.RequiredParameters(declared,
			map[string]string{"admin_email": "   "}).Run(context.Background())
		if res.Status != events.CheckFail {
			t.Errorf("status = %v for a required parameter set to whitespace", res.Status)
		}
	})
}

func TestNoUnfinishedOperation(t *testing.T) {
	t.Run("a clean history", func(t *testing.T) {
		res := preflight.NoUnfinishedOperation(nil).Run(context.Background())
		if res.Status != events.CheckOK {
			t.Errorf("status = %v with no unfinished operations", res.Status)
		}
	})

	t.Run("one that did not finish", func(t *testing.T) {
		check := preflight.NoUnfinishedOperation([]domain.OperationRecord{
			{ID: "op-123", Type: domain.OpTypeUpdate, Status: domain.StatusRunning},
		})
		if !check.Fatal {
			t.Error("layering changes on an unconfirmed state is how a " +
				"recoverable failure becomes an unrecoverable one")
		}
		res := check.Run(context.Background())
		if res.Status != events.CheckFail {
			t.Fatalf("an unfinished operation passed: %s", res.Message)
		}
		if !strings.Contains(res.Message, "op-123") {
			t.Errorf("message %q does not name the operation", res.Message)
		}
		if !strings.Contains(res.Remedy, "--resume") {
			t.Errorf("remedy %q does not offer to resume it", res.Remedy)
		}
	})

	t.Run("one that needs a human", func(t *testing.T) {
		res := preflight.NoUnfinishedOperation([]domain.OperationRecord{
			{ID: "op-456", Type: domain.OpTypeUpdate, Status: domain.StatusManualIntervention},
		}).Run(context.Background())

		if res.Status != events.CheckFail {
			t.Fatalf("an operation needing intervention passed: %s", res.Message)
		}
		if !strings.Contains(res.Message, "manual intervention") {
			t.Errorf("message %q does not say a human is needed", res.Message)
		}
		if strings.Contains(res.Remedy, "--resume") {
			t.Errorf("remedy %q offers to resume an operation that needs "+
				"repairing first, which is how a bad state gets compounded",
				res.Remedy)
		}
		if !strings.Contains(res.Remedy, "--clear-intervention") {
			t.Errorf("remedy %q does not say how to clear the flag once "+
				"the system is repaired", res.Remedy)
		}
	})
}

// TestFilesystemTypeIdentifiesThisMachine reads the real /proc/mounts, which is
// what `doctor` uses to tell an operator that rendered secrets are landing on
// disk rather than in memory.
func TestFilesystemTypeIdentifiesThisMachine(t *testing.T) {
	if _, err := os.Stat("/proc/mounts"); err != nil {
		t.Skip("no /proc/mounts on this machine")
	}

	if got := preflight.FilesystemType("/"); got == "" {
		t.Error("the root filesystem could not be identified, so the tmpfs check " +
			"silently reports nothing on every machine")
	}

	// The longest matching mount point wins, which is how the kernel
	// resolves it: a path under /proc must not be attributed to /.
	if got := preflight.FilesystemType("/proc/self"); got != "proc" {
		t.Errorf("FilesystemType(/proc/self) = %q, want proc; the longest "+
			"matching mount point must win", got)
	}
}

func TestFilesystemTypeOnSomethingItCannotAnswer(t *testing.T) {
	// A path that resolves nowhere still has to produce "cannot tell"
	// rather than a wrong answer. Empty means unknown, never "not tmpfs".
	if got := preflight.FilesystemType(""); got != "" && got == "tmpfs" {
		t.Errorf("an unanswerable path was reported as tmpfs")
	}
}

func TestIsEphemeralFilesystem(t *testing.T) {
	for _, fs := range []string{"tmpfs", "ramfs"} {
		if !preflight.IsEphemeralFilesystem(fs) {
			t.Errorf("%s is memory-backed and was not recognised as such", fs)
		}
	}
	for _, fs := range []string{"ext4", "xfs", "btrfs", "overlay", "nfs", ""} {
		if preflight.IsEphemeralFilesystem(fs) {
			t.Errorf("%s was reported memory-backed, so rendered secrets would "+
				"be left on disk with nobody warned", fs)
		}
	}
}

// TestACheckThatPanicsIsNotSilentlySwallowed documents the boundary: the
// runner does not recover, so a panicking check crashes the operation rather
// than being reported as a pass.
func TestACheckThatPanicsIsNotSilentlySwallowed(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a panicking check was swallowed; a bug in a check must not " +
				"be able to look like a passing preflight")
		}
	}()

	run(t, preflight.Check{ID: "boom", Run: func(context.Context) events.CheckResult {
		panic(errors.New("a check with a bug in it"))
	}})
}

var _ = time.Second
