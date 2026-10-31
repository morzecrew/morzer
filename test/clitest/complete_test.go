package clitest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/test/clitest"
)

// Shell completion as the shell asks for it: cobra's hidden `__complete`
// command, one candidate per line, then `:<directive>`. 4 is
// ShellCompDirectiveNoFileComp -- a machine with nothing to offer must not fall
// back to the file names in the current directory (#210).

func TestProductCompletesTheInstallationsOnTheMachine(t *testing.T) {
	r := withTwoInstallations(t)

	r.Run("__complete", "--product", "").ExitCode(0).
		OutputContains("demo\t", "sandbox\t", ":4")
}

func TestProductCompletesNothingOnABareMachine(t *testing.T) {
	r := clitest.New(t)

	out := r.Run("__complete", "--product", "").ExitCode(0)
	if got := strings.TrimSpace(out.Stdout); !strings.HasPrefix(got, ":4") {
		t.Errorf("a bare machine offered candidates or allowed file names:\n%s", got)
	}
}

// Under sudo the completion runs as the operator, who cannot open an
// installation's 0750 directory. Only a unit the manager wrote for this root
// names it then, and the host has none for a test's root: the matching itself
// is TestTheFallbackRecognisesTheUnitsTheManagerWrites.
func TestProductOffersNoDirectoryItCannotOpenWithoutAUnit(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens every directory, so nothing is unreadable to fall back from")
	}
	r := withTwoInstallations(t)

	sandbox := filepath.Join(r.Root, "etc", "sandbox")
	if err := os.Chmod(sandbox, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sandbox, 0o750) })

	r.Run("__complete", "--product", "").ExitCode(0).
		OutputContains("demo\t", ":4").
		NoOutputContains("sandbox")
}

// What lives inside an installation completes from it: its releases, the
// names `config` takes, its tasks and its backups, as `release list`, `config
// list`, `config settings`, `task list` and `backup list` read them. 0 lets the
// shell fall back to file names, which `release show` takes too; 6 adds
// NoSpace, so `config set` leaves the cursor after the `=`.
func TestAnInstallationsOwnValuesComplete(t *testing.T) {
	r := clitest.New(t)
	r.Bundle = r.BundleWithATask()
	r.Run("init",
		"--release", r.Bundle,
		"--profile", "embedded",
		"--no-recovery-recipient",
		"--install-units=false",
	).ExitCode(0)
	r.FetchReleases("1.1.0", "1.3.0")
	r.Run("backup").ExitCode(0)
	backup, _ := r.Run("--json", "backup", "list").Field("data.0.id").(string)
	if backup == "" {
		t.Fatal("`backup list --json` names no backup")
	}

	r.Run("__complete", "release", "show", "").ExitCode(0).
		OutputContains("1.1.0\n", "1.2.0\tinstalled", "1.3.0\n", ":0")
	r.Run("__complete", "rollback", "--to", "").ExitCode(0).
		OutputContains("1.1.0\n", ":4").NoOutputContains("1.2.0", "1.3.0")
	r.Run("__complete", "update", "--to", "").ExitCode(0).
		OutputContains("1.3.0\n", ":4").NoOutputContains("1.1.0", "1.2.0")

	r.Run("__complete", "config", "get", "").ExitCode(0).
		OutputContains("http_port\tPort the application", "update.check\t", ":4")
	r.Run("__complete", "config", "set", "http_port=8080", "").ExitCode(0).
		OutputContains("log_level=\t", "update.check=\t", ":6").NoOutputContains("http_port")
	r.Run("__complete", "config", "set", "log_level=").ExitCode(0).
		OutputContains(":4").NoOutputContains("log_level")
	r.Run("__complete", "config", "unset", "log_level", "").ExitCode(0).
		OutputContains("http_port\t").NoOutputContains("log_level")

	r.Run("__complete", "task", "run", "").ExitCode(0).
		OutputContains("seed\tFill an empty database", ":4")
	r.Run("__complete", "task", "run", "seed", "").ExitCode(0).
		NoOutputContains("seed\t")

	r.Run("__complete", "restore", "--backup", "").ExitCode(0).OutputContains(backup, ":4")
	r.Run("__complete", "backup", "verify", "").ExitCode(0).OutputContains(backup, ":4")
}

// fakeDocker puts a `docker` on PATH that runs ps for `compose ps` and does
// nothing for anything else, standing in for the daemon.
func fakeDocker(t *testing.T, ps string) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \" $* \" in *\" ps \"*)\n" + ps + "\n;;\nesac\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// Services complete from what the runtime lists, as `ps` shows them. A scaled
// service is listed once per instance and offered once.
func TestServicesCompleteFromWhatTheProjectRuns(t *testing.T) {
	r := clitest.NewInstalled(t)
	fakeDocker(t, `echo '{"Name":"demo-app-1","Service":"app","State":"running"}'
echo '{"Name":"demo-app-2","Service":"app","State":"running"}'
echo '{"Name":"demo-db-1","Service":"db","State":"exited"}'`)

	out := r.Run("__complete", "logs", "").ExitCode(0).
		OutputContains("app\trunning", "db\texited", ":4")
	if n := strings.Count(out.Stdout, "app\t"); n != 1 {
		t.Errorf("app offered %d times:\n%s", n, out.Stdout)
	}
	r.Run("__complete", "logs", "app", "").ExitCode(0).
		OutputContains("db\t").NoOutputContains("app\t")
	r.Run("__complete", "exec", "").ExitCode(0).OutputContains("app\t", "db\t")
	r.Run("__complete", "exec", "app", "").ExitCode(0).NoOutputContains("app\t", "db\t")
}

// A daemon that does not answer gives no candidates within seconds, rather
// than holding the prompt for as long as `compose ps` is allowed.
func TestServiceCompletionGivesUpOnAStalledDaemon(t *testing.T) {
	r := clitest.NewInstalled(t)
	fakeDocker(t, "exec sleep 120")

	start := time.Now()
	r.Run("__complete", "logs", "").ExitCode(0).OutputContains(":4")
	if took := time.Since(start); took > 15*time.Second {
		t.Errorf("completion waited %s for a stalled daemon", took.Round(time.Second))
	}
}

// The values come from the installation the line names, which setup could not
// know: it ran before cobra parsed the line. With two installations and none
// named there is nothing to offer, even when one of them has the placeholder's
// name, as status refuses it then.
func TestValuesCompleteFromTheInstallationTheLineNames(t *testing.T) {
	r := clitest.NewInstalled(t)
	r.Run("init",
		"--product", "morzer",
		"--release", r.Bundle,
		"--profile", "embedded",
		"--no-recovery-recipient",
		"--install-units=false",
	).ExitCode(0)
	r.Run("--product", "morzer", "release", "fetch", r.BundleAt("1.1.0")).ExitCode(0)
	r.Run("--product", "demo", "backup").ExitCode(0)
	backup, _ := r.Run("--json", "--product", "demo", "backup", "list").Field("data.0.id").(string)
	if backup == "" {
		t.Fatal("`backup list --json` names no backup")
	}

	r.Run("__complete", "restore", "--product", "demo", "--backup", "").ExitCode(0).
		OutputContains(backup)
	r.Run("__complete", "restore", "--product", "morzer", "--backup", "").ExitCode(0).
		NoOutputContains(backup)
	r.Run("__complete", "release", "show", "--product", "morzer", "").ExitCode(0).
		OutputContains("1.1.0\n")
	r.Run("__complete", "release", "show", "").ExitCode(0).
		NoOutputContains("1.1.0", "1.2.0")
	r.Run("__complete", "restore", "--backup", "").ExitCode(0).
		NoOutputContains(backup)

	t.Setenv("MORZER_PRODUCT", "demo")
	r.Run("__complete", "restore", "--backup", "").ExitCode(0).OutputContains(backup)
}

func TestShellCompletesTheInstallationsOnTheMachine(t *testing.T) {
	r := withTwoInstallations(t)

	r.Run("__complete", "shell", "").ExitCode(0).OutputContains("demo\t", "sandbox\t", ":4")
	r.Run("__complete", "shell", "demo", "").ExitCode(0).NoOutputContains("sandbox")
}
