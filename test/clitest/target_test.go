package clitest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
)

// The target commands as an operator types them. What the suites underneath
// cannot cover: flag parsing, the refusals a mistyped URL produces, and the exit
// codes a script reads.

func TestBackupTargetAddAndList(t *testing.T) {
	r := NewInstalled(t)
	offsite := filepath.Join(t.TempDir(), "offsite")

	r.Run("backup", "target", "list").
		ExitCode(0).
		StdoutContains("no backup targets")

	r.Run("backup", "target", "add", "file://"+offsite).
		ExitCode(0).
		StderrContains("added")

	r.Run("backup", "target", "list").
		ExitCode(0).
		StdoutContains(offsite, "0 backup")

	// The URL is recorded in the installation, where an operator can read
	// it and `doctor` can print it.
	r.Run("backup", "target", "list", "--json").
		ExitCode(0).
		FieldLen("data", 1)
}

func TestBackupTargetAddRefusesAMistypedURL(t *testing.T) {
	r := NewInstalled(t)

	for name, url := range map[string]string{
		"no scheme at all":  "/mnt/usb/backups",
		"a release scheme":  "https://backups.example/demo",
		"a host in file://": "file://nas/backups",
		"ssh with no path":  "ssh://backups.example",
	} {
		t.Run(name, func(t *testing.T) {
			r.Run("backup", "target", "add", url).
				ExitCode(domain.ExitUsage).
				StderrContains("hint:")
		})
	}
}

// TestBackupTargetAddRefusesACredentialInTheURL. The URL is written to
// installation.yaml, printed by `doctor` and quoted in support tickets.
func TestBackupTargetAddRefusesACredentialInTheURL(t *testing.T) {
	r := NewInstalled(t)

	result := r.Run("backup", "target", "add", "ssh://ops:hunter2@nas.internal/srv/backups").
		ExitCode(domain.ExitUsage)

	result.NoOutputContains("hunter2")
	result.StderrContains("--credentials")
}

// TestBackupTargetAddRefusesOneThatDoesNotAnswer, before recording it. A typo
// that only fails at push time fails during the nightly backup.
func TestBackupTargetAddRefusesOneThatDoesNotAnswer(t *testing.T) {
	r := NewInstalled(t)

	blocked := filepath.Join(r.Root, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	r.Run("backup", "target", "add", "file://"+filepath.Join(blocked, "backups")).
		Failed().
		StderrContains("not added")

	r.Run("backup", "target", "list").
		ExitCode(0).
		StdoutContains("no backup targets")
}

func TestBackupTargetAddRefusesADuplicate(t *testing.T) {
	r := NewInstalled(t)
	offsite := filepath.Join(t.TempDir(), "offsite")

	r.Run("backup", "target", "add", "file://"+offsite).ExitCode(0)
	r.Run("backup", "target", "add", "file://"+offsite).
		Failed().
		StderrContains("already")
}

// TestBackupTargetRemoveLeavesWhatIsThere, and says so, because "remove" that
// silently erased an off-site archive would be the worst possible reading.
func TestBackupTargetRemoveLeavesWhatIsThere(t *testing.T) {
	r := NewInstalled(t)
	offsite := filepath.Join(t.TempDir(), "offsite")

	r.Run("backup", "target", "add", "file://"+offsite).ExitCode(0)
	r.Run("backup", "target", "remove", "file://"+offsite).
		ExitCode(0).
		StderrContains("left alone", "on this machine")

	r.Run("backup", "target", "remove", "file://"+offsite).
		Failed().
		StderrContains("not a backup target")
}

// TestBackupListRemoteAgainstAnEmptyTarget. Answering "nothing there" is a
// state the command has to handle: it is where every installation starts.
func TestBackupListRemoteAgainstAnEmptyTarget(t *testing.T) {
	r := NewInstalled(t)
	offsite := filepath.Join(t.TempDir(), "offsite")

	r.Run("backup", "target", "add", "file://"+offsite).ExitCode(0)
	r.Run("backup", "list", "--remote").
		ExitCode(0).
		StdoutContains("no backups on the target")
}

// TestBackupListRemoteWithNoTargetConfigured names the remedy rather than
// failing blankly.
func TestBackupListRemoteWithNoTargetConfigured(t *testing.T) {
	NewInstalled(t).
		Run("backup", "list", "--remote").
		ExitCode(domain.ExitUsage).
		StderrContains("--target", "backup target add")
}

// TestBackupListAgainstATargetGivenByURL is the recovery shape: an operator
// with the medium and nothing configured.
func TestBackupListAgainstATargetGivenByURL(t *testing.T) {
	r := NewInstalled(t)
	offsite := filepath.Join(t.TempDir(), "offsite")
	if err := os.MkdirAll(offsite, 0o700); err != nil {
		t.Fatal(err)
	}

	r.Run("backup", "list", "--target", "file://"+offsite).
		ExitCode(0).
		StdoutContains("no backups on the target")
}

// TestACredentialsFileIsReadFromDiskRatherThanArgv. A flag carrying a secret is
// visible in `ps` to every user on the machine.
func TestACredentialsFileIsReadFromDiskRatherThanArgv(t *testing.T) {
	r := NewInstalled(t)
	offsite := filepath.Join(t.TempDir(), "offsite")
	if err := os.MkdirAll(offsite, 0o700); err != nil {
		t.Fatal(err)
	}

	creds := filepath.Join(t.TempDir(), "creds.yaml")
	if err := os.WriteFile(creds,
		[]byte("access_key_id: AKIAEXAMPLE\nsecret_access_key: s3kr3t\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// file:// ignores credentials, which is the point: supplying them must
	// not be an error, so one command line works across transports.
	r.Run("backup", "list", "--target", "file://"+offsite, "--credentials-file", creds).
		ExitCode(0)

	r.Run("backup", "list", "--target", "file://"+offsite,
		"--credentials-file", filepath.Join(t.TempDir(), "absent.yaml")).
		ExitCode(domain.ExitUsage).
		StderrContains("cannot read")
}

// TestAMalformedCredentialsFileIsRefusedWithoutQuotingIt.
func TestAMalformedCredentialsFileIsRefusedWithoutQuotingIt(t *testing.T) {
	r := NewInstalled(t)
	const password = "correct-horse-battery-staple"

	creds := filepath.Join(t.TempDir(), "creds.yaml")
	if err := os.WriteFile(creds,
		[]byte("access_key_id: [unclosed\nsecret_access_key: "+password), 0o600); err != nil {
		t.Fatal(err)
	}

	result := r.Run("backup", "list", "--target", "file://"+filepath.Join(t.TempDir(), "elsewhere"),
		"--credentials-file", creds).
		ExitCode(domain.ExitUsage)

	result.NoOutputContains(password)
	result.StderrContains("access_key_id")
}

// TestBackupPushWithNoTargetsNamesTheRemedy.
func TestBackupPushWithNoTargetsNamesTheRemedy(t *testing.T) {
	NewInstalled(t).
		Run("backup", "push").
		ExitCode(domain.ExitUsage).
		StderrContains("backup target add")
}

// TestBackupFetchFromAnEmptyTargetIsNotFound rather than a crash or a silent
// success.
func TestBackupFetchFromAnEmptyTargetIsNotFound(t *testing.T) {
	r := NewInstalled(t)
	offsite := filepath.Join(t.TempDir(), "offsite")

	r.Run("backup", "target", "add", "file://"+offsite).ExitCode(0)
	r.Run("backup", "fetch").
		Failed().
		StderrContains("no backups")
}

// TestABackupGoesToATargetAndComesBack drives the whole lifecycle as an
// operator types it: configure a target, take a backup, lose the local copy,
// list what is on the target, bring it back.
//
// The suites underneath cover the operation; this covers the commands, which is
// where an operator actually meets it -- and where a flag that reaches the wrong
// field is invisible to everything else.
func TestABackupGoesToATargetAndComesBack(t *testing.T) {
	r := NewInstalled(t)
	offsite := filepath.Join(t.TempDir(), "offsite")

	r.Run("backup", "target", "add", "file://"+offsite).ExitCode(0)
	r.Run("backup", "--reason", "manual").ExitCode(0)

	remote := r.Run("backup", "list", "--remote", "--json").ExitCode(0)
	remote.FieldLen("data", 1)

	local := r.Run("backup", "list", "--json").ExitCode(0)
	local.FieldLen("data", 1)

	// The target reports the backup by id, and `target list` counts it.
	r.Run("backup", "target", "list").
		ExitCode(0).
		StdoutContains("1 backup")

	// Lose the local copy, the way a failed disk does.
	backupsDir := domain.PathsUnder(r.Root, "demo").BackupsDir()
	backups, err := os.ReadDir(backupsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("expected one local backup, found %d", len(backups))
	}
	id := backups[0].Name()
	if err := os.RemoveAll(filepath.Join(backupsDir, id)); err != nil {
		t.Fatal(err)
	}

	r.Run("backup", "list").ExitCode(0).StdoutContains("no backups")

	r.Run("backup", "fetch", id).
		ExitCode(0).
		StderrContains("fetched from")

	r.Run("backup", "list").ExitCode(0).StdoutContains(id)
	r.Run("backup", "verify", id).ExitCode(0)
}

// TestBackupPushRetriesACopyThatIsAlreadyLocal, which is the documented remedy
// for a push that failed, and must not need another backup.
func TestBackupPushRetriesACopyThatIsAlreadyLocal(t *testing.T) {
	r := NewInstalled(t)
	offsite := filepath.Join(t.TempDir(), "offsite")

	// Taken before the target exists, so nothing was pushed.
	r.Run("backup", "--reason", "manual").ExitCode(0)
	r.Run("backup", "target", "add", "file://"+offsite).ExitCode(0)
	r.Run("backup", "list", "--remote").ExitCode(0).StdoutContains("no backups on the target")

	r.Run("backup", "push").
		ExitCode(0).
		StderrContains("copied to")

	r.Run("backup", "list", "--remote").ExitCode(0).StdoutContains(offsite)
}

// TestNoPushKeepsABackupLocal, for the operator who knows the medium is
// disconnected and wants a backup anyway.
func TestNoPushKeepsABackupLocal(t *testing.T) {
	r := NewInstalled(t)
	offsite := filepath.Join(t.TempDir(), "offsite")

	r.Run("backup", "target", "add", "file://"+offsite).ExitCode(0)
	r.Run("backup", "--no-push").
		ExitCode(0).
		NoOutputContains("copied to")

	r.Run("backup", "list", "--remote").ExitCode(0).StdoutContains("no backups on the target")
}

// TestDoctorReportsABackupThatNeverLeftTheMachineAtTheCLI, with the exit code a
// monitoring system reads.
func TestDoctorReportsABackupThatNeverLeftTheMachineAtTheCLI(t *testing.T) {
	r := NewInstalled(t)
	offsite := filepath.Join(t.TempDir(), "offsite")

	r.Run("backup", "target", "add", "file://"+offsite).ExitCode(0)
	r.Run("backup", "--no-push").ExitCode(0)

	// Exit 3 rather than 0: this is the failure that hides, and a warning
	// would let it keep hiding.
	r.Run("doctor").
		ExitCode(domain.ExitPreflight).
		OutputContains("backup push")
}

// TestVerifyRemoteChecksTheCopyOnTheTarget, and reports the count rather than
// silently succeeding against nothing.
func TestVerifyRemoteChecksTheCopyOnTheTarget(t *testing.T) {
	r := NewInstalled(t)
	offsite := filepath.Join(t.TempDir(), "offsite")

	r.Run("backup", "target", "add", "file://"+offsite).ExitCode(0)
	r.Run("backup").ExitCode(0)

	r.Run("backup", "verify", "--remote").
		ExitCode(0).
		StderrContains("verified")
}

// TestVerifyRemoteAgainstNothingIsNotFound rather than a success that means
// "there was nothing to check", which is the reading that turns a scheduled
// verification into a green light for an empty bucket.
func TestVerifyRemoteAgainstNothingIsNotFound(t *testing.T) {
	r := NewInstalled(t)
	offsite := filepath.Join(t.TempDir(), "offsite")

	r.Run("backup", "target", "add", "file://"+offsite).ExitCode(0)
	r.Run("backup", "verify", "--remote").
		Failed().
		StderrContains("no backups")
}

// TestDryRunDoesNotChangeTheTargets.
//
// `--dry-run` is documented as "plan only, make no changes", and it was adding
// and removing targets for real. A global flag that lies about one command is a
// global flag nobody can trust on any of them.
func TestDryRunDoesNotChangeTheTargets(t *testing.T) {
	r := NewInstalled(t)
	offsite := filepath.Join(t.TempDir(), "offsite")

	r.Run("--dry-run", "backup", "target", "add", "file://"+offsite).
		ExitCode(0).
		StderrContains("would add")

	r.Run("backup", "target", "list").
		ExitCode(0).
		StdoutContains("no backup targets")

	// And the same going the other way.
	r.Run("backup", "target", "add", "file://"+offsite).ExitCode(0)
	r.Run("--dry-run", "backup", "target", "remove", "file://"+offsite).
		ExitCode(0).
		StderrContains("would remove")

	r.Run("backup", "target", "list").
		ExitCode(0).
		StdoutContains(offsite)
}

// TestADryRunStillReportsWhetherTheTargetAnswers, because that is the only
// question worth asking before adding one, and it reads nothing and writes
// nothing.
func TestADryRunStillReportsWhetherTheTargetAnswers(t *testing.T) {
	r := NewInstalled(t)

	blocked := filepath.Join(r.Root, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	r.Run("--dry-run", "backup", "target", "add", "file://"+filepath.Join(blocked, "backups")).
		Failed().
		StderrContains("not added")
}

// TestDryRunDoesNotPush. `backup push` is a write like any other, and the flag
// that says it will not write has to hold for every command that has one.
func TestDryRunDoesNotPush(t *testing.T) {
	r := NewInstalled(t)
	offsite := filepath.Join(t.TempDir(), "offsite")

	r.Run("backup", "target", "add", "file://"+offsite).ExitCode(0)
	r.Run("backup", "--no-push").ExitCode(0)

	r.Run("--dry-run", "backup", "push").
		ExitCode(0).
		StderrContains("would copy")

	r.Run("backup", "list", "--remote").
		ExitCode(0).
		StdoutContains("no backups on the target")
}

// TestACredentialsFileWithNothingToApplyItToIsRefused, rather than silently
// ignored. An operator who passed credentials and got a local listing would
// reasonably conclude the target holds what this machine holds.
func TestACredentialsFileWithNothingToApplyItToIsRefused(t *testing.T) {
	r := NewInstalled(t)

	creds := filepath.Join(t.TempDir(), "creds.yaml")
	if err := os.WriteFile(creds, []byte("access_key_id: AKIA\nsecret_access_key: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, cmd := range [][]string{
		{"backup", "list", "--credentials-file", creds},
		{"backup", "verify", "--credentials-file", creds},
	} {
		r.Run(cmd...).
			ExitCode(domain.ExitUsage).
			StderrContains("--remote", "--target")
	}
}
