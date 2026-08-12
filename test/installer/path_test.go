package installer

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The PATH edit (RFC 0022 §5.4). An installer that leaves a binary somewhere
// the shell cannot find it has not installed anything, and the two classic
// defects of this shape are both invisible on the first run: appending a second
// copy of the block, and writing a file the shell in question never reads.

const beginMark = "# >>> morzer >>>"

// installWithShell runs a complete install for one shell and returns the home
// it wrote into.
func installWithShell(t *testing.T, shell string, extra ...string) (string, result) {
	t.Helper()

	rel := newRelease(t, fixtureOptions{})
	home := newHome(t)
	into := filepath.Join(t.TempDir(), "bin")

	args := append([]string{
		"--version", fixtureVersion, "--dir", into,
		"--no-verify-signature", "--no-completions",
	}, extra...)

	out := run(t, script(t, rel),
		env{home: home, shell: "/bin/" + shell, release: rel}, args...).requireOK(t)
	return home, out
}

func TestEachShellGetsTheFilesItActuallyReads(t *testing.T) {
	// Both the login file and the interactive one for bash and zsh. Neither
	// covers the other -- bash reads the first of ~/.bash_profile,
	// ~/.bash_login and ~/.profile and stops, and ~/.bashrc is the
	// interactive one -- so a single file leaves half the sessions without
	// the prefix, which is the failure that looks like "the installer didn't
	// work".
	t.Run("bash with a bashrc", func(t *testing.T) {
		rel := newRelease(t, fixtureOptions{})
		home := newHome(t)
		touch(t, filepath.Join(home, ".bashrc"))

		run(t, script(t, rel), env{home: home, shell: "/bin/bash", release: rel},
			"--version", fixtureVersion, "--dir", filepath.Join(t.TempDir(), "bin"),
			"--no-verify-signature", "--no-completions").requireOK(t)

		assertBlocks(t, filepath.Join(home, ".profile"), 1)
		assertBlocks(t, filepath.Join(home, ".bashrc"), 1)
	})

	t.Run("bash with a bash_profile", func(t *testing.T) {
		// bash reads ~/.bash_profile and then stops, so ~/.profile is
		// never read and writing it would be a no-op the operator
		// cannot see.
		rel := newRelease(t, fixtureOptions{})
		home := newHome(t)
		touch(t, filepath.Join(home, ".bash_profile"))

		run(t, script(t, rel), env{home: home, shell: "/bin/bash", release: rel},
			"--version", fixtureVersion, "--dir", filepath.Join(t.TempDir(), "bin"),
			"--no-verify-signature", "--no-completions").requireOK(t)

		assertBlocks(t, filepath.Join(home, ".bash_profile"), 1)
		if _, err := os.Stat(filepath.Join(home, ".profile")); err == nil {
			t.Error("it wrote ~/.profile, which this bash never reads")
		}
	})

	t.Run("zsh", func(t *testing.T) {
		home, _ := installWithShell(t, "zsh")

		assertBlocks(t, filepath.Join(home, ".zshrc"), 1)
		// ~/.zshenv is read by every zsh including non-interactive
		// ones, so a prepend there reaches scripts that never asked.
		if _, err := os.Stat(filepath.Join(home, ".zshenv")); err == nil {
			t.Error("it wrote ~/.zshenv, which every non-interactive zsh reads")
		}
	})

	t.Run("zsh with a zprofile", func(t *testing.T) {
		rel := newRelease(t, fixtureOptions{})
		home := newHome(t)
		touch(t, filepath.Join(home, ".zprofile"))

		run(t, script(t, rel), env{home: home, shell: "/bin/zsh", release: rel},
			"--version", fixtureVersion, "--dir", filepath.Join(t.TempDir(), "bin"),
			"--no-verify-signature", "--no-completions").requireOK(t)

		assertBlocks(t, filepath.Join(home, ".zshrc"), 1)
		assertBlocks(t, filepath.Join(home, ".zprofile"), 1)
	})

	t.Run("fish", func(t *testing.T) {
		// A drop-in: no existing file is edited, and removing the file
		// is a complete uninstall.
		home, _ := installWithShell(t, "fish")

		assertBlocks(t, filepath.Join(home, ".config", "fish", "conf.d", "morzer.fish"), 1)
	})
}

func TestAPrefixAlreadyOnPathIsLeftAlone(t *testing.T) {
	// Nothing to do, so nothing is written. An installer that edits a
	// startup file it did not need to has still edited a startup file.
	rel := newRelease(t, fixtureOptions{})
	home := newHome(t)
	into := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(into, 0o700); err != nil {
		t.Fatal(err)
	}

	// stubs prepends the prefix to PATH, which is the condition under test:
	// the script reads $PATH and finds it there.
	out := run(t, script(t, rel),
		env{home: home, shell: "/bin/bash", release: rel, stubs: into},
		"--version", fixtureVersion, "--dir", into,
		"--no-verify-signature", "--no-completions").requireOK(t)

	out.requireSays(t, "already on PATH")
	for _, name := range []string{".profile", ".bashrc", ".bash_profile"} {
		if _, err := os.Stat(filepath.Join(home, name)); err == nil {
			t.Errorf("it wrote ~/%s for a prefix that was already on PATH", name)
		}
	}
}

func TestASecondRunAddsNothing(t *testing.T) {
	// The defect every installer of this shape eventually has, and it is
	// invisible until the second run: three runs, three copies of the block.
	rel := newRelease(t, fixtureOptions{})
	home := newHome(t)
	into := filepath.Join(t.TempDir(), "bin")
	scriptPath := script(t, rel)

	for range 3 {
		run(t, scriptPath, env{home: home, shell: "/bin/bash", release: rel},
			"--version", fixtureVersion, "--dir", into,
			"--no-verify-signature", "--no-completions").requireOK(t)
	}

	assertBlocks(t, filepath.Join(home, ".profile"), 1)
}

func TestTheBlockNamesThePrefixThatWasResolved(t *testing.T) {
	// Generated from the resolved prefix rather than from a constant. A
	// block that hardcoded ~/.local/bin would be wrong for every --dir and
	// would look right in the one case anybody tests by hand.
	elsewhere := filepath.Join(t.TempDir(), "somewhere", "else", "bin")
	rel := newRelease(t, fixtureOptions{})
	home := newHome(t)

	run(t, script(t, rel), env{home: home, shell: "/bin/bash", release: rel},
		"--version", fixtureVersion, "--dir", elsewhere,
		"--no-verify-signature", "--no-completions").requireOK(t)

	body := read(t, filepath.Join(home, ".profile"))
	if !strings.Contains(body, elsewhere) {
		t.Errorf("the block does not name %s:\n%s", elsewhere, body)
	}
	if strings.Contains(body, ".local/bin") {
		t.Errorf("the block names a prefix nobody asked for:\n%s", body)
	}
}

func TestTheFishBlockIsFish(t *testing.T) {
	// A POSIX `case ... esac` in a .fish file is a syntax error at every
	// subsequent shell start -- and it is the *shell* that breaks, not
	// morzer, so the operator has no reason to suspect this file.
	home, _ := installWithShell(t, "fish")
	path := filepath.Join(home, ".config", "fish", "conf.d", "morzer.fish")
	body := read(t, path)

	if strings.Contains(body, "case ") || strings.Contains(body, "esac") {
		t.Errorf("POSIX syntax in a fish file:\n%s", body)
	}
	if !strings.Contains(body, "fish_add_path") {
		t.Errorf("the fish block does not use fish_add_path:\n%s", body)
	}

	// And if there is a fish here, the only assertion that really settles
	// it: the shell reads the file without complaint and the prefix is on
	// PATH afterwards.
	fish, err := exec.LookPath("fish")
	if err != nil {
		t.Log("no fish on this machine; the syntax assertions above are what ran. " +
			"The container lane runs this against a real fish.")
		return
	}
	out, err := exec.Command(fish, "-c", "source "+path+"; echo $PATH").CombinedOutput()
	if err != nil {
		t.Fatalf("fish cannot read the block it was given: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "bin") {
		t.Errorf("sourcing the block left nothing on PATH:\n%s", out)
	}
}

func TestNoModifyPathPrintsTheBlockInstead(t *testing.T) {
	// For an operator whose dotfiles are managed elsewhere. Printing is not
	// a consolation prize: it is the whole answer, so it goes to stdout
	// where it can be piped into the file they actually maintain.
	rel := newRelease(t, fixtureOptions{})
	home := newHome(t)

	out := run(t, script(t, rel), env{home: home, shell: "/bin/bash", release: rel},
		"--version", fixtureVersion, "--dir", filepath.Join(t.TempDir(), "bin"),
		"--no-verify-signature", "--no-completions", "--no-modify-path").requireOK(t)

	if !strings.Contains(out.stdout, beginMark) {
		t.Errorf("the block was not printed:\n%s", out.stdout)
	}
	if _, err := os.Stat(filepath.Join(home, ".profile")); err == nil {
		t.Error("--no-modify-path wrote a startup file")
	}
}

func TestASymlinkedStartupFileIsPrintedNotAppendedTo(t *testing.T) {
	// A symlink into a dotfiles repository means the file is generated.
	// Appending to it loses the edit at the next sync, silently -- the
	// operator learns only that morzer stopped being found.
	rel := newRelease(t, fixtureOptions{})
	home := newHome(t)

	real := filepath.Join(t.TempDir(), "dotfiles-profile")
	if err := os.WriteFile(real, []byte("# managed elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(home, ".profile")); err != nil {
		t.Fatal(err)
	}

	out := run(t, script(t, rel), env{home: home, shell: "/bin/bash", release: rel},
		"--version", fixtureVersion, "--dir", filepath.Join(t.TempDir(), "bin"),
		"--no-verify-signature", "--no-completions").requireOK(t)

	if body := read(t, real); body != "# managed elsewhere\n" {
		t.Errorf("it wrote through the symlink:\n%s", body)
	}
	out.requireSays(t, "symlink")
	if !strings.Contains(out.stdout, beginMark) {
		t.Errorf("it did not print the block it declined to write:\n%s", out.stdout)
	}
}

func TestAnUnrecognisedShellGetsTheBlockAndTheBinary(t *testing.T) {
	// Not fatal: the binary is what was asked for, and a shell this script
	// has no file for is a shell whose operator can be told what to add.
	rel := newRelease(t, fixtureOptions{})
	home := newHome(t)
	into := filepath.Join(t.TempDir(), "bin")

	out := run(t, script(t, rel), env{home: home, shell: "/usr/bin/nu", release: rel},
		"--version", fixtureVersion, "--dir", into,
		"--no-verify-signature", "--no-completions").requireOK(t)

	assertInstalled(t, into)
	if !strings.Contains(out.stdout, beginMark) {
		t.Errorf("no block was printed for an unrecognised shell:\n%s", out.stdout)
	}
}

// assertBlocks counts the marked blocks in a file. One is correct; zero means
// the file the shell reads was not edited, and two is the classic defect.
func assertBlocks(t *testing.T, path string, want int) {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s was not written: %v", path, err)
	}
	if got := strings.Count(string(body), beginMark); got != want {
		t.Errorf("%s holds %d morzer blocks, want %d:\n%s", path, got, want, body)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func touch(t *testing.T, path string) {
	t.Helper()

	if err := os.WriteFile(path, []byte("# existing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAPrefixWithASpaceStillWorksInBothShells(t *testing.T) {
	// The one awkward path that actually occurs. In the POSIX block it is
	// inside quotes and works; in fish it was an unquoted argument, so
	// `fish_add_path /opt/my morzer/bin` added neither of the two paths it
	// was split into -- an installer that reported success and left the
	// binary unfindable.
	for _, shell := range []string{"bash", "fish"} {
		t.Run(shell, func(t *testing.T) {
			spaced := filepath.Join(t.TempDir(), "my morzer", "bin")
			rel := newRelease(t, fixtureOptions{})
			home := newHome(t)

			run(t, script(t, rel),
				env{home: home, shell: "/bin/" + shell, release: rel},
				"--version", fixtureVersion, "--dir", spaced,
				"--no-verify-signature", "--no-completions").requireOK(t)

			file := filepath.Join(home, ".profile")
			if shell == "fish" {
				file = filepath.Join(home, ".config", "fish", "conf.d", "morzer.fish")
			}
			body := read(t, file)
			if !strings.Contains(body, spaced) {
				t.Fatalf("the block does not name the prefix:\n%s", body)
			}
			// The path has to survive as one word. In fish that means
			// quotes around it; in POSIX sh the assignment already has
			// them.
			if shell == "fish" && !strings.Contains(body, "'"+spaced+"'") {
				t.Errorf("the fish block leaves the path unquoted, so it is two "+
					"arguments:\n%s", body)
			}
		})
	}
}

func TestAPrefixThatWouldBecomeCodeIsRefused(t *testing.T) {
	// The block is written once and then run by that shell at every start,
	// so a prefix carrying shell syntax is not a formatting problem: it is a
	// line of code in the file an operator is least likely to suspect.
	// Refused rather than escaped -- quoting correctly for two shells is a
	// lot of care spent on a prefix nobody has.
	for _, bad := range []string{
		`/tmp/a"; touch /tmp/pwned; #`,
		"/tmp/a$(id)",
		"/tmp/a`id`",
		`/tmp/a'b`,
		"/tmp/a\nb",
	} {
		rel := newRelease(t, fixtureOptions{})
		out := run(t, script(t, rel),
			env{home: newHome(t), shell: "/bin/bash", release: rel},
			"--version", fixtureVersion, "--dir", bad,
			"--no-verify-signature", "--no-completions")

		out.requireFailed(t, "the prefix carries shell syntax").
			requireSays(t, "--no-modify-path")
		if len(rel.hits.paths) != 0 {
			t.Errorf("--dir %q downloaded %v before refusing", bad, rel.hits.paths)
		}
	}
}
