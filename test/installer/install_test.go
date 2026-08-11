package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What the script promises: a verified binary on the disk, or a refusal that
// names the reason. The failure cases are the point — every one of them is a
// way the six commands this replaces failed quietly.

func TestTheEmbeddedKeyIsTheOneTheRepositoryPublishes(t *testing.T) {
	// The script carries the key rather than fetching it, which is what
	// makes the signature worth checking (RFC 0022 decision 5). The cost of
	// embedding is drift: a key that no longer matches the one the pipeline
	// signs with rejects every release, and it does so at install time on
	// someone else's machine.
	root := repoRoot(t)

	published, err := os.ReadFile(filepath.Join(root, "morzer.pub"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, "install.sh"))
	if err != nil {
		t.Fatal(err)
	}

	for _, line := range strings.Split(strings.TrimSpace(string(published)), "\n") {
		if !strings.Contains(string(body), strings.TrimSpace(line)) {
			t.Errorf("install.sh does not carry this line of morzer.pub:\n  %s", line)
		}
	}
}

func TestAnInstallLandsTheBinaryAndSaysWhere(t *testing.T) {
	rel := newRelease(t, fixtureOptions{})
	home := newHome(t)
	dir := filepath.Join(t.TempDir(), "bin")

	out := run(t, script(t, rel),
		env{home: home, shell: "/bin/bash", release: rel},
		"--version", fixtureVersion, "--dir", dir, "--no-verify-signature",
	).requireOK(t)

	binary := filepath.Join(dir, "morzer")
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatalf("nothing was installed: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("the installed binary is not executable: %v", info.Mode())
	}

	// The summary is what goes into a runbook or a build log, so it is on
	// stdout and it carries the three facts that identify what was installed.
	for _, want := range []string{fixtureVersion, rel.digest, binary} {
		if !strings.Contains(out.stdout, want) {
			t.Errorf("the summary does not carry %q:\n%s", want, out.stdout)
		}
	}
}

func TestNothingIsLeftInTheTemporaryDirectory(t *testing.T) {
	// A partial archive left in /tmp is the file somebody finds later and
	// extracts. The script removes its working directory on every exit path,
	// and the failing path is the one that would otherwise leak.
	rel := newRelease(t, fixtureOptions{})
	tmp := t.TempDir()
	home := newHome(t)

	before := entries(t, tmp)

	run(t, script(t, rel),
		env{home: home, shell: "/bin/bash", release: rel, extra: []string{"TMPDIR=" + tmp}},
		"--version", fixtureVersion, "--dir", filepath.Join(t.TempDir(), "bin"),
		"--no-verify-signature",
	).requireOK(t)

	if got := entries(t, tmp); len(got) != len(before) {
		t.Errorf("a successful run left %v behind in TMPDIR", got)
	}

	// And the failing one, which is the case that matters.
	bad := newRelease(t, fixtureOptions{corruptArchive: true})
	run(t, script(t, bad),
		env{home: home, shell: "/bin/bash", release: bad, extra: []string{"TMPDIR=" + tmp}},
		"--version", fixtureVersion, "--dir", filepath.Join(t.TempDir(), "bin"),
		"--no-verify-signature",
	).requireFailed(t, "the archive is corrupt")

	if got := entries(t, tmp); len(got) != len(before) {
		t.Errorf("a failed run left %v behind in TMPDIR", got)
	}
}

func TestVerificationRefusesRatherThanInstalls(t *testing.T) {
	dir := func(t *testing.T) string { return filepath.Join(t.TempDir(), "bin") }

	t.Run("a corrupted archive", func(t *testing.T) {
		rel := newRelease(t, fixtureOptions{corruptArchive: true})
		into := dir(t)

		run(t, script(t, rel),
			env{home: newHome(t), shell: "/bin/bash", release: rel},
			"--version", fixtureVersion, "--dir", into, "--no-verify-signature",
		).requireFailed(t, "the checksum does not match").
			requireSays(t, "checksum mismatch")

		assertNothingInstalled(t, into)
	})

	t.Run("SHA256SUMS with no line for this archive", func(t *testing.T) {
		// The `--ignore-missing` trap, which is what the documented
		// command did: with no line to check, it reported OK.
		rel := newRelease(t, fixtureOptions{omitSumsLine: true})
		into := dir(t)

		run(t, script(t, rel),
			env{home: newHome(t), shell: "/bin/bash", release: rel},
			"--version", fixtureVersion, "--dir", into, "--no-verify-signature",
		).requireFailed(t, "the sums file does not cover this archive").
			requireSays(t, "SHA256SUMS has no line for", rel.archive)

		assertNothingInstalled(t, into)
	})

	t.Run("a digest the caller pinned and did not get", func(t *testing.T) {
		rel := newRelease(t, fixtureOptions{})
		into := dir(t)

		run(t, script(t, rel),
			env{home: newHome(t), shell: "/bin/bash", release: rel},
			"--version", fixtureVersion, "--dir", into, "--no-verify-signature",
			"--digest", "sha256:"+strings.Repeat("a", 64),
		).requireFailed(t, "the pinned digest does not match").
			requireSays(t, "--digest does not match", rel.digest)

		assertNothingInstalled(t, into)
	})

	t.Run("a binary that reports another version", func(t *testing.T) {
		// A correct download of the wrong thing: the checksum cannot
		// catch it, because the checksum came from the same place.
		rel := newRelease(t, fixtureOptions{reportsVersion: "9.9.9"})
		into := dir(t)

		run(t, script(t, rel),
			env{home: newHome(t), shell: "/bin/bash", release: rel},
			"--version", fixtureVersion, "--dir", into, "--no-verify-signature",
		).requireFailed(t, "the binary reports the wrong version").
			requireSays(t, "9.9.9", fixtureVersion)

		assertNothingInstalled(t, into)
	})

	t.Run("an unwritable prefix", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root writes anywhere, so this cannot be staged")
		}
		rel := newRelease(t, fixtureOptions{})
		into := filepath.Join(t.TempDir(), "bin")
		if err := os.MkdirAll(into, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(into, 0o700) })

		out := run(t, script(t, rel),
			env{home: newHome(t), shell: "/bin/bash", release: rel},
			"--version", fixtureVersion, "--dir", into, "--no-verify-signature",
		).requireFailed(t, "the prefix cannot be written to")

		// It never runs sudo; it prints the command to re-run.
		out.requireSays(t, "not writable", "sudo")
		assertNothingInstalled(t, into)
	})
}

func TestContradictoryFlagsAreRefusedBeforeAnythingIsDownloaded(t *testing.T) {
	// Letting argument order decide whether verification happens is how a
	// runbook that inherited both flags silently stops verifying. The
	// assertion is not only that it refuses but that it refused *first*:
	// the server saw nothing.
	rel := newRelease(t, fixtureOptions{})

	run(t, script(t, rel),
		env{home: newHome(t), shell: "/bin/bash", release: rel},
		"--version", fixtureVersion, "--dir", filepath.Join(t.TempDir(), "bin"),
		"--require-signature", "--no-verify-signature",
	).requireFailed(t, "the two flags contradict each other").
		requireSays(t, "--require-signature", "--no-verify-signature")

	if len(rel.hits.paths) != 0 {
		t.Errorf("it downloaded %v before refusing", rel.hits.paths)
	}
}

func TestTheSignatureDecidesWhatItCan(t *testing.T) {
	t.Run("it verifies", func(t *testing.T) {
		rel := newRelease(t, fixtureOptions{})
		into := filepath.Join(t.TempDir(), "bin")

		run(t, script(t, rel),
			env{home: newHome(t), shell: "/bin/bash", release: rel,
				stubs: minisignThat(t, true)},
			"--version", fixtureVersion, "--dir", into,
		).requireOK(t).requireSays(t, "signature verifies")

		assertInstalled(t, into)
	})

	t.Run("it does not verify", func(t *testing.T) {
		// The event the documentation already tells operators to stop
		// for: a release signed by a different key than the one this
		// script was published with.
		rel := newRelease(t, fixtureOptions{})
		into := filepath.Join(t.TempDir(), "bin")

		run(t, script(t, rel),
			env{home: newHome(t), shell: "/bin/bash", release: rel,
				stubs: minisignThat(t, false)},
			"--version", fixtureVersion, "--dir", into,
		).requireFailed(t, "the signature does not verify").
			requireSays(t, "signed by a different key")

		assertNothingInstalled(t, into)
	})

	t.Run("minisign is absent", func(t *testing.T) {
		// Warns and continues. A default that failed on a machine with
		// only curl and tar would push operators to skip verification
		// altogether, which is worse than this.
		rel := newRelease(t, fixtureOptions{})
		into := filepath.Join(t.TempDir(), "bin")

		run(t, script(t, rel),
			env{home: newHome(t), shell: "/bin/bash", release: rel,
				pathOnly: noMinisign(t)},
			"--version", fixtureVersion, "--dir", into,
		).requireOK(t).requireSays(t, "minisign is not installed", "--require-signature")

		assertInstalled(t, into)
	})

	t.Run("minisign is absent and required", func(t *testing.T) {
		rel := newRelease(t, fixtureOptions{})
		into := filepath.Join(t.TempDir(), "bin")

		run(t, script(t, rel),
			env{home: newHome(t), shell: "/bin/bash", release: rel,
				pathOnly: noMinisign(t)},
			"--version", fixtureVersion, "--dir", into, "--require-signature",
		).requireFailed(t, "minisign is required and absent").
			requireSays(t, "--require-signature")

		assertNothingInstalled(t, into)
		if len(rel.hits.paths) != 0 {
			t.Errorf("it downloaded %v before refusing", rel.hits.paths)
		}
	})
}

func TestTheInstallTargetIsRefusedWhenItIsNotARegularFile(t *testing.T) {
	t.Run("a symlink", func(t *testing.T) {
		// The ordinary case is a symlink into a package manager's tree.
		// Writing through it would replace a file the package manager
		// believes it owns, somewhere else entirely.
		rel := newRelease(t, fixtureOptions{})
		into := t.TempDir()
		elsewhere := filepath.Join(t.TempDir(), "packaged-morzer")
		if err := os.WriteFile(elsewhere, []byte("not ours\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, filepath.Join(into, "morzer")); err != nil {
			t.Fatal(err)
		}

		run(t, script(t, rel),
			env{home: newHome(t), shell: "/bin/bash", release: rel},
			"--version", fixtureVersion, "--dir", into, "--no-verify-signature",
		).requireFailed(t, "the target is a symlink").
			requireSays(t, "symlink", elsewhere)

		body, err := os.ReadFile(elsewhere)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != "not ours\n" {
			t.Error("it wrote through the symlink")
		}
	})

	t.Run("a directory", func(t *testing.T) {
		rel := newRelease(t, fixtureOptions{})
		into := t.TempDir()
		if err := os.MkdirAll(filepath.Join(into, "morzer"), 0o700); err != nil {
			t.Fatal(err)
		}

		run(t, script(t, rel),
			env{home: newHome(t), shell: "/bin/bash", release: rel},
			"--version", fixtureVersion, "--dir", into, "--no-verify-signature",
		).requireFailed(t, "the target is a directory").
			requireSays(t, "not a regular file")
	})
}

func TestTheVersionIsResolvedWhenNobodyNamedOne(t *testing.T) {
	// `latest` is resolved once and then printed, because a runbook has to
	// be able to see what it installed. The endpoint used never returns a
	// prerelease: admissible when named, never when inferred.
	rel := newRelease(t, fixtureOptions{})
	into := filepath.Join(t.TempDir(), "bin")

	run(t, script(t, rel),
		env{home: newHome(t), shell: "/bin/bash", release: rel},
		"--dir", into, "--no-verify-signature",
	).requireOK(t).requireSays(t, fixtureVersion)

	assertInstalled(t, into)
}

func TestPrintOnlyChangesNothingAndExplainsEverything(t *testing.T) {
	rel := newRelease(t, fixtureOptions{})
	into := filepath.Join(t.TempDir(), "bin")
	home := newHome(t)

	out := run(t, script(t, rel),
		env{home: home, shell: "/bin/bash", release: rel},
		"--version", fixtureVersion, "--dir", into, "--print-only",
	).requireOK(t)

	// Everything §5.2 detects, so the detection is testable without a
	// download — which is what the nightly job runs against the real API.
	out.requireSays(t,
		"os", "arch", "kernel", "shell", "version", "archive", "url",
		"install to", "already on PATH", "signature", "completions")

	if !strings.Contains(out.stdout, rel.archive) {
		t.Errorf("the report does not name the archive it would fetch:\n%s", out.stdout)
	}
	assertNothingInstalled(t, into)
	if len(rel.hits.paths) != 0 {
		t.Errorf("--print-only fetched %v", rel.hits.paths)
	}
}

func TestDetectionRefusesWhatItCannotServe(t *testing.T) {
	// uname stubbed on PATH: the whole of §5.2 without a download. The
	// refusals name what was found rather than guessing — a guessed
	// architecture downloads an archive whose binary the kernel will not
	// exec, and that fails several steps later with a much worse message.
	rel := newRelease(t, fixtureOptions{})

	for _, tc := range []struct {
		name, os, machine string
		wantRefusal       bool
		wantSays          []string
	}{
		{name: "macOS", os: "Darwin", machine: "arm64", wantRefusal: true,
			wantSays: []string{"Linux builds only", "no macOS build"}},
		{name: "32-bit arm", os: "Linux", machine: "armv7l", wantRefusal: true,
			wantSays: []string{"armv7l", "amd64 and arm64"}},
		{name: "riscv", os: "Linux", machine: "riscv64", wantRefusal: true,
			wantSays: []string{"riscv64"}},
		{name: "i686", os: "Linux", machine: "i686", wantRefusal: true,
			wantSays: []string{"i686"}},
		{name: "aarch64 resolves arm64", os: "Linux", machine: "aarch64",
			wantSays: []string{"morzer_" + fixtureVersion + "_linux_arm64.tar.zst"}},
		{name: "x86_64 resolves amd64", os: "Linux", machine: "x86_64",
			wantSays: []string{"morzer_" + fixtureVersion + "_linux_amd64.tar.zst"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubs := stubDir(t, map[string]string{
				"uname": "#!/bin/sh\ncase \"$1\" in\n" +
					"-s) printf '" + tc.os + "\\n' ;;\n" +
					"-m) printf '" + tc.machine + "\\n' ;;\n" +
					"-r) printf '6.6.0\\n' ;;\nesac\n",
			})

			out := run(t, script(t, rel),
				env{home: newHome(t), shell: "/bin/bash", release: rel, stubs: stubs},
				"--version", fixtureVersion, "--print-only")

			if tc.wantRefusal {
				out.requireFailed(t, "the platform is not published")
			} else {
				out.requireOK(t)
			}
			out.requireSays(t, tc.wantSays...)
		})
	}
}

func TestAShadowingBinaryIsNamedRatherThanIgnored(t *testing.T) {
	// Installing into ~/.local/bin on a machine that already has
	// /usr/local/bin/morzer: the old one goes on answering to the name, and
	// the operator's next command runs it.
	rel := newRelease(t, fixtureOptions{})
	into := filepath.Join(t.TempDir(), "bin")
	shadow := stubDir(t, map[string]string{
		"morzer": "#!/bin/sh\nprintf 'morzer 0.0.1\\n'\n",
	})

	run(t, script(t, rel),
		env{home: newHome(t), shell: "/bin/bash", release: rel, stubs: shadow},
		"--version", fixtureVersion, "--dir", into, "--no-verify-signature",
	).requireOK(t).requireSays(t, filepath.Join(shadow, "morzer"), "still runs that one")
}

// noMinisign is a machine that does not have minisign.
//
// Not a stub that exits non-zero — that is a machine where minisign is present
// and the signature is bad, which is a different branch and the one this was
// first written as. Absence means a PATH the binary is not on, which is what
// minimalPATH builds.
func noMinisign(t *testing.T) string {
	t.Helper()
	return minimalPATH(t)
}

func assertInstalled(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "morzer")); err != nil {
		t.Errorf("nothing was installed into %s: %v", dir, err)
	}
}

func assertNothingInstalled(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "morzer")); err == nil {
		t.Errorf("it installed a binary into %s despite refusing", dir)
	}
}

func entries(t *testing.T, dir string) []string {
	t.Helper()

	found, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(found))
	for _, e := range found {
		names = append(names, e.Name())
	}
	return names
}
