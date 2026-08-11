package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Completions (RFC 0022 §5.5), which this script does not implement. It runs
// the binary it just installed, because where a shell reads completions from is
// knowledge that belongs in one place — and a second copy written in sh would
// drift silently, since a completion in the wrong directory produces no error
// at all, just a Tab key that does nothing.

func TestCompletionsAreDelegatedToTheBinary(t *testing.T) {
	// The stub goes into the archive, at the path the script invokes, rather
	// than onto PATH where it would never be reached — a test whose double
	// is never called passes by not running.
	rel := newRelease(t, fixtureOptions{})
	into := filepath.Join(t.TempDir(), "bin")

	run(t, script(t, rel),
		env{home: newHome(t), shell: "/bin/zsh", release: rel},
		"--version", fixtureVersion, "--dir", into,
		"--no-verify-signature", "--completions").requireOK(t)

	argv := rel.argvLog(t)
	if !strings.Contains(argv, "completion install zsh") {
		t.Errorf("the binary was not asked to install the zsh completion.\n"+
			"What it was asked:\n%s", argv)
	}
}

func TestTheScriptWritesNoCompletionItself(t *testing.T) {
	// The assertion that a file-based one would get wrong: checking for a
	// completion *file* would pass on a script that wrote one itself, which
	// is the thing this design forbids. So the stub writes nothing, and the
	// home directory must stay empty of completion paths.
	rel := newRelease(t, fixtureOptions{})
	home := newHome(t)

	run(t, script(t, rel),
		env{home: home, shell: "/bin/bash", release: rel},
		"--version", fixtureVersion, "--dir", filepath.Join(t.TempDir(), "bin"),
		"--no-verify-signature", "--completions").requireOK(t)

	for _, path := range []string{
		filepath.Join(home, ".local", "share", "bash-completion", "completions", "morzer"),
		filepath.Join(home, ".local", "share", "zsh", "site-functions", "_morzer"),
		filepath.Join(home, ".config", "fish", "completions", "morzer.fish"),
	} {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("the script wrote a completion itself: %s", path)
		}
	}
}

func TestAFailingCompletionDoesNotFailTheInstall(t *testing.T) {
	// The binary is on the machine and works, which is what was asked for.
	// A completion that could not be written is a warning naming the command
	// to retry -- and failing here would also fail the optional completion
	// step of somebody's provisioning script.
	rel := newRelease(t, fixtureOptions{completionExit: 3})
	into := filepath.Join(t.TempDir(), "bin")

	out := run(t, script(t, rel),
		env{home: newHome(t), shell: "/bin/bash", release: rel},
		"--version", fixtureVersion, "--dir", into,
		"--no-verify-signature", "--completions").requireOK(t)

	assertInstalled(t, into)
	out.requireSays(t, "could not install the bash completion",
		"completion install bash")
}

func TestCompletionsAreOffWhenNobodyIsWatching(t *testing.T) {
	// A Dockerfile, a CI job, an Ansible task: stdout is not a terminal and
	// writing into a home directory that belongs to a build is noise. The
	// test process is never a terminal, which is exactly the case under test.
	rel := newRelease(t, fixtureOptions{})

	out := run(t, script(t, rel),
		env{home: newHome(t), shell: "/bin/bash", release: rel},
		"--version", fixtureVersion, "--dir", filepath.Join(t.TempDir(), "bin"),
		"--no-verify-signature").requireOK(t)

	if argv := rel.argvLog(t); strings.Contains(argv, "completion") {
		t.Errorf("a non-interactive install installed completions anyway:\n%s", argv)
	}
	out.requireSays(t, "completions skipped")
}

func TestAnUnrecognisedShellIsToldHowToDoItByHand(t *testing.T) {
	// `completion install` knows bash, zsh and fish. Calling it with
	// something else would produce a completion script on stdout, which is
	// useful at a terminal and noise here.
	rel := newRelease(t, fixtureOptions{})

	out := run(t, script(t, rel),
		env{home: newHome(t), shell: "/usr/bin/nu", release: rel},
		"--version", fixtureVersion, "--dir", filepath.Join(t.TempDir(), "bin"),
		"--no-verify-signature", "--completions").requireOK(t)

	if argv := rel.argvLog(t); strings.Contains(argv, "completion") {
		t.Errorf("it asked the binary to place a completion for a shell it "+
			"cannot name:\n%s", argv)
	}
	out.requireSays(t, "completion install")
}
