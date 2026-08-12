package installer

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The branches the sabotage sweep could not reach, because they are not guards
// on the happy path: the second downloader, the second hasher, the environment
// equivalents, and the argument errors. Each one is a promise the interface
// makes to somebody who is not the developer — a machine with wget and no curl,
// a runbook that sets variables rather than getting `sh -s --` right — and none
// of them was exercised by anything above.

func TestTheEnvironmentEquivalentsAreRead(t *testing.T) {
	// `curl … | sh -s -- --version X` is the incantation half the readers get
	// wrong, so MORZER_VERSION and MORZER_INSTALL_DIR exist for a runbook that
	// would rather set variables. They are documented, which makes them a
	// promise; nothing else here tested them.
	rel := newRelease(t, fixtureOptions{})
	into := filepath.Join(t.TempDir(), "bin")

	run(t, script(t, rel),
		env{home: newHome(t), shell: "/bin/bash", release: rel, extra: []string{
			"MORZER_VERSION=" + fixtureVersion,
			"MORZER_INSTALL_DIR=" + into,
		}},
		"--no-verify-signature", "--no-completions").requireOK(t)

	assertInstalled(t, into)
}

func TestAFlagBeatsItsEnvironmentEquivalent(t *testing.T) {
	// Otherwise a variable left in a shell profile would quietly overrule the
	// version a runbook names on the command line, which is the direction that
	// loses an audit.
	rel := newRelease(t, fixtureOptions{})
	wanted := filepath.Join(t.TempDir(), "wanted")
	ignored := filepath.Join(t.TempDir(), "ignored")

	run(t, script(t, rel),
		env{home: newHome(t), shell: "/bin/bash", release: rel, extra: []string{
			"MORZER_INSTALL_DIR=" + ignored,
		}},
		"--version", fixtureVersion, "--dir", wanted,
		"--no-verify-signature", "--no-completions").requireOK(t)

	assertInstalled(t, wanted)
	assertNothingInstalled(t, ignored)
}

func TestWgetInstallsWhenThereIsNoCurl(t *testing.T) {
	// "curl (or wget)" is the documented dependency, and a machine with only
	// wget is a real machine -- Debian's minimal image is one. Nothing else
	// here takes this branch, because the developer's machine has curl.
	rel := newRelease(t, fixtureOptions{})
	into := filepath.Join(t.TempDir(), "bin")

	run(t, script(t, rel),
		env{home: newHome(t), shell: "/bin/bash", release: rel,
			pathOnly: toolsExcept(t, []string{"curl"}, "wget")},
		"--version", fixtureVersion, "--dir", into,
		"--no-verify-signature", "--no-completions").requireOK(t)

	assertInstalled(t, into)
}

func TestShasumHashesWhenThereIsNoSha256sum(t *testing.T) {
	// The other half of the same claim. A wrong invocation here would not
	// fail: `shasum` without -a 256 computes SHA-1, which produces a digest
	// that never matches and an install that always refuses.
	rel := newRelease(t, fixtureOptions{})
	into := filepath.Join(t.TempDir(), "bin")

	out := run(t, script(t, rel),
		env{home: newHome(t), shell: "/bin/bash", release: rel,
			pathOnly: toolsExcept(t, []string{"sha256sum"}, "shasum")},
		"--version", fixtureVersion, "--dir", into,
		"--no-verify-signature", "--no-completions").requireOK(t)

	assertInstalled(t, into)
	// The same digest the fixture's SHA256SUMS carries, so this is SHA-256
	// and not whatever shasum defaults to.
	out.requireSays(t, rel.digest)
}

func TestAMachineWithNeitherDownloaderSaysSo(t *testing.T) {
	rel := newRelease(t, fixtureOptions{})

	run(t, script(t, rel),
		env{home: newHome(t), shell: "/bin/bash", release: rel,
			pathOnly: toolsExcept(t, []string{"curl", "wget"})},
		"--version", fixtureVersion, "--dir", filepath.Join(t.TempDir(), "bin"),
	).requireFailed(t, "there is nothing to download with").
		requireSays(t, "curl", "wget")
}

func TestArgumentsThatCannotMeanAnythingAreRefused(t *testing.T) {
	rel := newRelease(t, fixtureOptions{})
	scriptPath := script(t, rel)

	t.Run("a flag with no value", func(t *testing.T) {
		// The failure this prevents is the quiet one: `--version` at the
		// end of a line consuming nothing and installing "latest"
		// instead of what the runbook meant.
		run(t, scriptPath, env{home: newHome(t), shell: "/bin/bash", release: rel},
			"--version").
			requireFailed(t, "the flag has no value").
			requireSays(t, "--version needs a value")

		if len(rel.hits.paths) != 0 {
			t.Errorf("it resolved a version anyway: %v", rel.hits.paths)
		}
	})

	t.Run("an option nobody defined", func(t *testing.T) {
		// Never ignored: a typo'd --no-verify-signature that was skipped
		// would verify when the caller believed it would not, or the
		// reverse.
		run(t, scriptPath, env{home: newHome(t), shell: "/bin/bash", release: rel},
			"--no-verify-signatures").
			requireFailed(t, "the option does not exist").
			requireSays(t, "unknown option", "--help")
	})

	t.Run("a digest that is not one", func(t *testing.T) {
		run(t, scriptPath, env{home: newHome(t), shell: "/bin/bash", release: rel},
			"--version", fixtureVersion, "--digest", "deadbeef").
			requireFailed(t, "the digest has no algorithm").
			requireSays(t, "sha256:")
	})
}

func TestHelpIsPrintedAndNothingHappens(t *testing.T) {
	rel := newRelease(t, fixtureOptions{})

	out := run(t, script(t, rel),
		env{home: newHome(t), shell: "/bin/bash", release: rel}, "--help").requireOK(t)

	// Every flag the documentation promises. A help text that drifted from
	// the options is the first thing an operator reads and the last thing
	// anybody checks.
	out.requireSays(t,
		"--version", "--dir", "--digest", "--require-signature",
		"--no-verify-signature", "--no-modify-path", "--completions",
		"--shell", "--print-only", "MORZER_VERSION", "MORZER_INSTALL_DIR")
}

func TestTheShellCanBeNamedRatherThanDetected(t *testing.T) {
	// For an operator whose $SHELL is not the shell they use, and for the
	// install script of a system that has no $SHELL at all.
	rel := newRelease(t, fixtureOptions{})
	home := newHome(t)

	run(t, script(t, rel),
		env{home: home, shell: "/bin/bash", release: rel},
		"--version", fixtureVersion, "--dir", filepath.Join(t.TempDir(), "bin"),
		"--no-verify-signature", "--no-completions", "--shell", "fish").requireOK(t)

	// fish's file, not bash's, despite $SHELL saying bash.
	assertBlocks(t, filepath.Join(home, ".config", "fish", "conf.d", "morzer.fish"), 1)
	if _, err := os.Stat(filepath.Join(home, ".profile")); err == nil {
		t.Error("it wrote bash's file for a run that named fish")
	}
}

func TestNoSignaturePublishedIsWarnedAboutAndRequiredWhenAsked(t *testing.T) {
	// A release whose signing step did not run: the checksum file is there
	// and the .minisig is not. Warning is right by default -- the checksum
	// still verified -- and --require-signature is what a production runbook
	// sets to make it fatal.
	rel := newRelease(t, fixtureOptions{})
	if err := os.Remove(filepath.Join(rel.dir, "SHA256SUMS.minisig")); err != nil {
		t.Fatal(err)
	}
	into := filepath.Join(t.TempDir(), "bin")

	run(t, script(t, rel),
		env{home: newHome(t), shell: "/bin/bash", release: rel,
			stubs: minisignThat(t, true)},
		"--version", fixtureVersion, "--dir", into, "--no-completions",
	).requireOK(t).requireSays(t, "no signature published")
	assertInstalled(t, into)

	strict := filepath.Join(t.TempDir(), "bin")
	run(t, script(t, rel),
		env{home: newHome(t), shell: "/bin/bash", release: rel,
			stubs: minisignThat(t, true)},
		"--version", fixtureVersion, "--dir", strict, "--no-completions",
		"--require-signature",
	).requireFailed(t, "no signature and one was required").
		requireSays(t, "--require-signature")
	assertNothingInstalled(t, strict)
}

func TestAnUnreachableReleaseAPISaysWhichFlagFixesIt(t *testing.T) {
	// The machine behind a proxy that blocks api.github.com but not the
	// release download. Naming --version turns a dead end into a workaround.
	rel := newRelease(t, fixtureOptions{})
	rel.breakAPI()

	run(t, script(t, rel),
		env{home: newHome(t), shell: "/bin/bash", release: rel},
		"--dir", filepath.Join(t.TempDir(), "bin"), "--no-verify-signature",
	).requireFailed(t, "latest cannot be resolved").
		requireSays(t, "--version")
}

// toolsExcept is a PATH holding the documented dependency set, minus `drop` and
// plus `add` — a machine that has the alternative tool rather than the usual
// one. Dropping and adding are separate arguments on purpose: an earlier
// version inferred the alternative from the dropped name, so the test that
// meant "neither curl nor wget" got wget back and passed for the wrong reason.
func toolsExcept(t *testing.T, drop []string, add ...string) string {
	t.Helper()

	dropped := map[string]bool{}
	for _, d := range drop {
		dropped[d] = true
	}
	kept := []string{}
	for _, tool := range installScriptTools {
		if !dropped[tool] {
			kept = append(kept, tool)
		}
	}
	return toolsPATH(t, append(kept, add...))
}

func TestTheScriptNamesEveryToolItNeeds(t *testing.T) {
	// installScriptTools is a hand-written list, and a list that drifted from
	// the script would make every "this machine has only X" test above run on
	// a machine that also happens to have Y. The proof is that an ordinary
	// install works with exactly the list and nothing else.
	rel := newRelease(t, fixtureOptions{})
	into := filepath.Join(t.TempDir(), "bin")

	run(t, script(t, rel),
		env{home: newHome(t), shell: "/bin/bash", release: rel,
			pathOnly: minimalPATH(t)},
		"--version", fixtureVersion, "--dir", into,
		"--no-verify-signature", "--no-completions").requireOK(t)

	assertInstalled(t, into)
	if strings.Contains(minimalPATH(t), ":") {
		t.Error("the minimal PATH is more than one directory, so this proves less than it claims")
	}
}

func TestNoHomeAndNoPrefixIsARefusalRatherThanARelativePath(t *testing.T) {
	// `$HOME/.local/bin` with an empty $HOME is `/.local/bin`, and under a
	// user who can write / that is a successful install nobody can find.
	// The systemd unit and the container build are where $HOME goes missing.
	//
	// --print-only, and not as a detail: without it, a machine that *can*
	// write /usr/local/bin -- which is most CI runners -- takes the other
	// arm of the default and this test installs a fixture binary into the
	// real system prefix before failing its own assertion. A test that
	// writes outside its temporary directory to prove a refusal has already
	// done the thing the refusal exists to prevent.
	rel := newRelease(t, fixtureOptions{})

	out := run(t, script(t, rel),
		env{home: "", shell: "/bin/bash", release: rel},
		"--version", fixtureVersion, "--print-only")

	if systemPrefixIsWritable(t) {
		// There is a prefix to fall back to, so there is nothing to
		// refuse: the documented default resolves and says so.
		out.requireOK(t).requireSays(t, "/usr/local/bin/morzer")
		return
	}
	out.requireFailed(t, "there is no home directory and no --dir").
		requireSays(t, "--dir")
}

// systemPrefixIsWritable answers the question install.sh asks, by asking it the
// same way: `[ -d /usr/local/bin ] && [ -w /usr/local/bin ]`. Which arm of the
// default runs is a property of the machine, and both are asserted rather than
// one being assumed.
func systemPrefixIsWritable(t *testing.T) bool {
	t.Helper()
	return exec.Command("sh", "-c",
		"[ -d /usr/local/bin ] && [ -w /usr/local/bin ]").Run() == nil
}

func TestThePrefixDefaultsToWhatThisMachineAllows(t *testing.T) {
	// Documented as "/usr/local/bin when writable, otherwise
	// $HOME/.local/bin", and which arm runs is a property of the machine:
	// a CI runner can usually write /usr/local/bin and a developer cannot.
	// Both arms are asserted, so neither passes by never running.
	rel := newRelease(t, fixtureOptions{})
	home := newHome(t)

	out := run(t, script(t, rel),
		env{home: home, shell: "/bin/bash", release: rel},
		"--version", fixtureVersion, "--print-only").requireOK(t)

	systemWritable := systemPrefixIsWritable(t)
	want := filepath.Join(home, ".local", "bin", "morzer")
	if systemWritable {
		want = "/usr/local/bin/morzer"
	}
	if !strings.Contains(out.stdout, want) {
		t.Errorf("with /usr/local/bin writable=%v the prefix should be %s:\n%s",
			systemWritable, want, out.stdout)
	}
}
