# RFC 0022 — Bootstrapping the manager

- **Status:** 📝 Draft
- **Scope:** Getting the `morzer` binary onto a machine: an sh-compatible
  `install.sh` that takes a version and verifies what it downloaded, published
  from this repository and from every release, plus the corrections the current
  instructions need (they name an asset the pipeline does not produce). Covers
  the script, its verification rules, where it puts things, and how it is tested.
  Adds no Go code and no command — the manager does not install itself, and
  `morzer self-update` is refused by name below. Deliberately not a package: apt,
  rpm, brew and nix are distribution channels with their own maintainers and
  their own trust stories, and none of them is what an operator who has just
  been handed a Linux box will use.
- **Related:** [`.goreleaser.yaml`](../.goreleaser.yaml),
  [`.github/workflows/release.yaml`](../.github/workflows/release.yaml),
  [`pages/docs/get-started/installation.md`](../pages/docs/get-started/installation.md),
  [`morzer.pub`](../morzer.pub),
  [0004](0004-distribution-and-verification.md) (why signing lives in the
  pipeline and there is no `morzer sign`),
  [0014](0014-building-a-release-bundle.md) (the reproducibility claim the script
  inherits)

---

## 1. Summary

The documented installation is four `curl` commands, a `minisign` invocation, a
`sha256sum`, a `tar` and an `install` — and it downloads an asset the release
pipeline does not produce. This RFC replaces it with:

```sh
curl -fsSL https://morzecrew.github.io/morzer/install.sh | sh -s -- --version 1.4.0
```

and keeps the long form documented underneath, because the long form is what the
script is doing and an operator who wants to check its work needs to be able to.

The script verifies before it installs: the checksum always, the minisign
signature whenever `minisign` is present, and a `--digest` the caller pins when
they have one. It refuses to write outside a prefix it names, it never runs
`sudo` on its own initiative, and it is the same script the release artefacts
carry — so a machine that can reach the release can bootstrap without reaching
anything else.

It also does the two things that separate "a binary is on the disk" from "the
tool works": it puts the install prefix on `PATH` when it is not already there,
in one marked block in one startup file that it prints before writing; and it
installs shell completions by running `morzer completion install`, because where
a shell reads completions from is knowledge that belongs in the binary rather
than in a copy of it written in `sh`.

## 2. Motivation

### The published instructions cannot work

`installation.md` says:

```sh
curl -fsSLO https://github.com/morzecrew/morzer/releases/latest/download/morzer_linux_amd64.tar.zst
```

`.goreleaser.yaml` says:

```yaml
name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"
```

which produces `morzer_1.4.0_linux_amd64.tar.zst`. The documented URL 404s, and
nothing catches it: `docs-check` verifies links *between pages* and *into the
repository*, and this is an external URL. No release has been cut yet — there are
no tags and no GitHub releases — so the instructions have never been executed by
anyone, which is exactly how a snippet like this survives.

That is the whole motivation in miniature. The steps are correct in spirit and
wrong in detail, and the only way to keep them right is to make them executable
and run them.

### Six steps, each of which an operator can get subtly wrong

The current sequence downloads an archive, a checksum file, a signature and a
public key; verifies two things; extracts; and installs. Every step is
defensible — the RFC 0004 decision that signing happens in the pipeline is what
makes the signature worth checking — but the failure modes are quiet:

- `sha256sum -c SHA256SUMS --ignore-missing` **passes when the archive is
  absent**, because `--ignore-missing` is what makes the sums file (which covers
  both architectures) usable at all. An operator whose download failed gets
  `OK` from the thing that was supposed to catch it.
- Fetching `morzer.pub` from `main` at install time proves the repository is
  self-consistent and nothing more. The docs say so in a tip; a script can *do*
  something about it (pin the key, and say what changed when it differs).
- Nothing pins the version. `latest/download` is a moving target, so a runbook
  written today installs something else tomorrow — which is the property this
  project refuses everywhere else. `update.channel` exists precisely because
  following a moving tag has to be a decision.

### The comparison the request named

`uv`, `rustup` and `deno` all ship a script that takes a version, verifies a
checksum, and installs to a user-writable prefix. The reason it is the expected
shape is not fashion: it collapses six commands whose failure modes are silent
into one whose failure modes are the script's problem, and it gives a runbook a
single line to pin.

## 3. Current state

| Thing | State |
| --- | --- |
| Archives | `morzer_<version>_linux_<amd64\|arm64>.tar.zst`, containing the binary, `LICENSE`, `README.md`, `CHANGELOG.md` and `schemas/*` |
| Checksums | `SHA256SUMS` over the archives |
| Signature | `SHA256SUMS.minisig`, minisign, signed in the release job from a repository secret; skipped when the secret is absent |
| Public key | [`morzer.pub`](../morzer.pub), committed at the repository root |
| Releases | `draft: true`, `prerelease: auto`; a human publishes after reading the notes |
| Release footer | Already documents the verify sequence, and already tells the reader to reuse a key they have verified before |
| Tags / releases published | None yet |
| Install script | None |
| Docs | `get-started/installation.md`, with the broken asset name |
| Reproducibility | `just build-all` matches the pipeline's flags; a binary built from a tag is byte-identical to the published one |

## 4. Goals / Non-goals

**Goals**

- One line that installs a *named* version and fails loudly if what arrived is
  not what was published.
- POSIX sh, no bashisms, no dependency beyond `curl` (or `wget`), `tar`, `sha256sum`
  (or `shasum`), and `zstd` where `tar` cannot do it.
- Works unprivileged, into a user prefix, and says what it did.
- A working tool afterwards, not just a file: on `PATH`, with completions, or an
  exact instruction when the script declines to do it for you.
- Verifiable by hand: every step the script takes is a step the docs still show.

**Non-goals**

- **`morzer self-update`.** A manager that replaces its own binary while a
  systemd timer may be running it is a class of bug this project does not need,
  and the machine that most needs a pinned manager is the one where an unattended
  update path exists for the *product*. Upgrading the manager is re-running the
  script with a new version. *What would change this:* nothing short of a
  demonstrated operational need with a story for the running-unit case.
- **Distribution packages.** apt/rpm/brew/nix each need a maintainer and a trust
  story. They are welcome later and are not the bootstrap path.
- **Windows or macOS.** The manager targets Linux hosts running Docker Compose;
  goreleaser builds `linux/{amd64,arm64}` and nothing else.
- **Installing Docker, sops or minisign.** The script checks for what it needs to
  verify itself and points at `morzer doctor` for the rest. A bootstrap script
  that installed a container runtime would be making decisions about the
  machine's package manager that nobody asked it to make.
- **Auditing the machine.** `doctor` exists, it is thorough, and it runs after the
  binary is installed. The script's environment detection stops at what decides
  *which archive to fetch and whether the result will be usable* — os, arch,
  shell, `PATH` — and hands the rest over by name.
- **Managing dotfiles.** One marked block in one file, or a printed block. A
  script that reordered a `PATH`, rewrote a profile, or "fixed" a shell's
  configuration would be doing something nobody asked for on the file an operator
  is most attached to.

## 5. Design

### 5.1 The interface

```text
install.sh [--version X.Y.Z] [--dir PATH] [--digest sha256:…]
           [--require-signature] [--no-verify-signature]
           [--no-modify-path] [--completions|--no-completions]
           [--shell bash|zsh|fish] [--print-only]

  --version              Release to install. Default: the latest published
                         release, resolved once and then printed.
  --dir                  Install prefix. Default: /usr/local/bin when writable,
                         otherwise $HOME/.local/bin.
  --digest               Expected sha256 of the archive. When given, it is
                         checked *before* SHA256SUMS and a mismatch is fatal.
  --require-signature    Refuse to install when minisign is absent or the
                         signature does not verify.
  --no-verify-signature  Skip the signature check. Prints what it is skipping.
                         Refused together with --require-signature, before
                         anything is downloaded: letting argument order decide
                         whether verification happens is how a runbook that
                         inherited both silently stops verifying.
  --no-modify-path       Never edit a shell startup file; print what to add.
  --completions          Install shell completions even when not interactive.
  --no-completions       Do not install completions.
  --shell                Override the detected shell for PATH and completions.
  --print-only           Print everything it detected and would do; change
                         nothing.
```

Environment equivalents (`MORZER_VERSION`, `MORZER_INSTALL_DIR`) for the
`curl | sh` form, where passing flags means `sh -s --` and half of the readers
will get it wrong.

### 5.2 What it detects, and what it refuses

The script's whole job is to land a working binary, so it checks what bears on
*that* and hands everything else to `doctor`. The division is worth stating
because a bootstrap script that grows into a system audit is a second `doctor`
that nobody maintains.

| Detected | How | What it does with it |
| --- | --- | --- |
| Operating system | `uname -s` | `Linux` proceeds. Anything else is a refusal naming what was found — `Darwin` gets its own sentence, because the goreleaser matrix is `goos: [linux]` and no macOS build exists to point at. WSL reports `Linux` and is a supported target. |
| Architecture | `uname -m` | `x86_64`/`amd64` → `amd64`; `aarch64`/`arm64` → `arm64`. Anything else — `armv7l`, `riscv64`, `i686` — is refused by name rather than guessed. Guessing here downloads an archive that extracts to a binary the kernel will not exec, which fails several steps later with a worse message. |
| Kernel | `uname -r` | Recorded in the summary; warned below the floor a Go 1.25 static binary needs. Not otherwise acted on: Docker's kernel requirements are `doctor`'s business, not the installer's. |
| libc | not checked | `CGO_ENABLED=0`, so there is no dynamic dependency to check. Stated because its absence otherwise looks like an oversight. |
| Shell | `basename "$SHELL"`, overridable with `--shell` | `$SHELL` holds a path (`/bin/bash`, `/usr/bin/fish`), and every consumer of this wants the name: the startup file for PATH (§5.4) and the `--shell` argument for completions (§5.5), which accepts `bash\|zsh\|fish` and nothing else. Normalised once, here. An unrecognised shell is not fatal: the binary still installs, and the script prints what to add by hand. |
| `$PATH` | string match against the resolved prefix | Decides whether §5.4 has anything to do at all. |
| An existing `morzer` | `command -v morzer` | Warns when the binary that will answer to `morzer` is **not** the one just installed — the ordinary trap of installing to `~/.local/bin` while `/usr/local/bin/morzer` exists and wins. Names both paths and their versions. |
| Interactivity | stdout is a terminal | Decides the completions default (§5.5) and nothing else. |

Everything it detected is printed by `--print-only`, which makes the detection
itself testable without installing anything.

### 5.3 What it verifies, and in what order

1. **The digest, when the caller pinned one.** First, because a caller who names
   a digest is asserting something about specific bytes and must not be told
   about a checksum file first.
2. **`SHA256SUMS`, matched against the archive by name** — never
   `--ignore-missing`, which is what makes the current instructions pass on a
   failed download. The script greps its own archive's line out of the file and
   compares that one.
3. **`SHA256SUMS.minisig` with `morzer.pub`**, when `minisign` is available. The
   key is **embedded in the script**, not fetched: a script that downloads the
   key it verifies against has verified that the server was self-consistent.
   Embedding also gives the script something to say when the release is signed by
   a different key — which is the event the docs already tell operators to stop
   for.
4. **The extracted binary runs and reports the version asked for.**
   `morzer version` on a mismatch is the check that catches a correct download of
   the wrong thing.

Absent `minisign`, the script warns and continues — unless `--require-signature`,
which is what a runbook for a production machine sets. Making it fatal by default
would mean the recommended path fails on a machine that has curl and tar and
nothing else, and the operator's workaround would be to skip verification
entirely.

### 5.4 PATH, when the prefix is not on it

Only the unprivileged case needs this. `/usr/local/bin` is on every distribution's
default `PATH`; `$HOME/.local/bin` is on some and not others, and an installer
that leaves a binary somewhere the shell cannot find it has not installed
anything.

The rule is: **the smallest edit that survives a new shell, in a marked block, in
one file, printed before it is made.**

The block is generated from the **resolved prefix**, not from a constant, and in
the syntax of the shell whose file it is going into. POSIX shells get:

```sh
# >>> morzer >>>
# Added by morzer's install.sh. Remove this block to undo it.
case ":$PATH:" in *":<prefix>:"*) ;; *) PATH="<prefix>:$PATH" ;; esac
export PATH
# <<< morzer <<<
```

and fish gets fish, because a `case … esac` in a `.fish` file is a syntax error
at every subsequent shell start:

```fish
# >>> morzer >>>
# Added by morzer's install.sh. Remove this file to undo it.
fish_add_path <prefix>
# <<< morzer <<<
```

Where it goes, by shell:

| Shell | File | Why that one |
| --- | --- | --- |
| `fish` | `~/.config/fish/conf.d/morzer.fish` | A drop-in directory: no existing file is edited, and removing the file is a complete uninstall. Where a shell offers this it is strictly better than appending, and `fish_add_path` is idempotent on its own. |
| `bash` | `~/.profile`, and `~/.bashrc` when it exists | Both, and this is the one place the script writes twice. `~/.profile` is read by a **login** shell — the ssh session, the `su -`, the desktop session that exports the environment — and `~/.bashrc` by an interactive non-login one. Neither covers the other: bash reads `~/.bash_profile` *or* `~/.bash_login` *or* `~/.profile` and stops at the first, and Debian's stock `~/.profile` is what sources `~/.bashrc`. A single file therefore leaves one of the two cases without the prefix on `PATH`, which is the failure that looks like "the installer didn't work". Where `~/.bash_profile` or `~/.bash_login` already exists, it takes `~/.profile`'s place, because bash will not read `~/.profile` at all. |
| `zsh` | `~/.zshrc`, and `~/.zprofile` when it exists | Same asymmetry: `~/.zprofile` is the login file and `~/.zshrc` the interactive one. `~/.zshenv` is read by *every* zsh including non-interactive ones and is deliberately not used — a `PATH` prepend there affects scripts that never asked for it. |
| anything else | nothing | Prints the block and the sentence "add this to your shell's startup file". |

The guarantee, stated at the width it actually holds: **a new login shell and a
new interactive shell both find the binary.** A non-interactive non-login shell —
`ssh host morzer status`, a cron job — reads none of these files on any shell, so
it needs an absolute path or a prefix that is already on the system `PATH`. That
is a property of shells, not of this script, and saying so is better than a
guarantee that quietly excludes the automation case.

And the rules around it:

- **Idempotent by marker.** Re-running the script finds the block and leaves it
  alone. Without the markers, an operator who runs the installer three times gets
  three copies, which is the failure everyone has seen.
- **`--no-modify-path` prints the block instead of writing it**, for an operator
  whose dotfiles are managed elsewhere. So does an unwritable or symlinked
  startup file — a symlink into a dotfiles repository is a signal that the file
  is generated, and appending to it silently loses the edit at the next sync.
- **Never a system-wide file, and there is no flag that makes it one.**
  `/etc/profile.d` is untouched: it is on every user's `PATH` already for the
  system prefix, so the only reason to write there would be to put a *user's*
  prefix on everyone's `PATH`, which is not a thing an installer gets to decide.
  An earlier draft referenced a `--system` escape hatch that the interface never
  defined; leaving an undefined, security-sensitive flag in a document is how it
  gets implemented by guess.
- **It says what it did and what to do now**: the file it touched and
  `exec $SHELL` (or "open a new terminal"), because a `PATH` edit that does not
  affect the shell you are standing in is the other thing everyone has seen.

### 5.5 Completions, which this script does not implement

The script installs shell completions by **running the binary it just
installed**:

```sh
"$dir/morzer" completion install --shell "$shell"
```

That is [0019](0019-the-command-surface.md) §5.8, and the split is the point:
*where a shell reads completions from* is knowledge that belongs in one place,
and that place is the Go code, where it is versioned with the binary, tested
against a fake `HOME`, and updated when cobra grows a shell. A shell script that
learned the same paths would be a second implementation that drifts — and it
would drift silently, because a completion that is installed to the wrong
directory produces no error at all, just a Tab key that does nothing.

Defaults, which are about consent rather than capability:

- **On when the install is interactive and the shell is recognised.** Somebody at
  a terminal running an installer wants the tool to work properly.
- **Off when it is not** — in a Dockerfile, a CI job, an Ansible task — because
  writing into a home directory that belongs to a build is noise at best.
  `--completions` forces it on; `--no-completions` forces it off.
- **A failure here never fails the install.** The binary is on the machine and
  works; a completion script that could not be written is a warning naming the
  path and the command to retry.

This creates the one ordering dependency between these RFCs: 0022's completion
step needs 0019 P5. Until then the script prints the `completion install`
invocation instead of running it, which is also exactly what it does today for an
unrecognised shell.

### 5.6 Where it writes, and what it never does

- **Installs exactly one file: `<dir>/morzer`.** The schemas and licence in the
  archive are not installed anywhere; they exist for a vendor who wants them.
  Three other writes are possible and each is opt-out and announced: the shell
  startup file or fish drop-in (§5.4), the completion script (§5.5, written by
  the binary rather than by the script), and a temporary directory that is
  removed on every exit path including failure. Nothing else on the filesystem
  is touched, which is the claim the tests assert.
- **Never invokes `sudo`.** If the prefix is not writable, it prints the one
  command to re-run with elevation and exits non-zero. A script fetched over the
  network that escalates on its own is the thing operators are right to distrust.
- Downloads into a temporary directory it creates and removes, including on
  failure. A partial archive left in `/tmp` is the file somebody finds later and
  extracts.
- Refuses to overwrite a `morzer` that is not a regular file (a symlink into a
  package manager's tree, a directory), naming what it found.
- Prints, on success, the version, the digest it verified, and the path — so the
  output is what goes into a runbook or a build log.

### 5.7 Publication and the URL that is stable

The site is `https://morzecrew.github.io/morzer/`, and it is **versioned**: `mike`
deploys each minor into its own subdirectory on the `gh-pages` branch and owns
the root, where it writes an `index.html` redirect and `versions.json`
([`justfile`](../justfile), `deploy-docs` / `default-docs`). A URL under a
version directory is the wrong home for an installer — `…/morzer/1.4/install.sh`
pins the script to a docs version that has nothing to do with the release being
installed, and `…/latest/install.sh` moves when the docs move.

So the script is published in three places, with one of them canonical:

- **`https://morzecrew.github.io/morzer/install.sh`** — canonical, and placed at
  the `gh-pages` root *beside* the version directories rather than inside one, by
  a step in the docs release workflow. `mike` touches only the version directory,
  `index.html` and `versions.json`, so a sibling file at the root survives a
  deploy — but that is a property of `mike`'s behaviour rather than a promise it
  makes, so the workflow re-publishes the file on every docs release and a test
  fetches it after deploying. This is the URL the README carries.
- **`https://raw.githubusercontent.com/morzecrew/morzer/main/install.sh`** — the
  same file, always current, no site machinery in the path. Already the shape
  this project uses for `morzer.pub`, and it is what the docs show for an
  operator who wants to read the script before running it. Swapping `main` for a
  tag pins the script itself, which a cautious runbook will want.
- **As a release asset**, carried by `goreleaser`'s `extra_files` — so a machine
  that has the release tarball can bootstrap without reaching either of the
  above, and so the script that installed a version is archived beside it.
  `extra_files` also puts it under `SHA256SUMS`: the script that verifies the
  archive becomes verifiable against the same file, for anyone who wants to close
  that loop.

All three are the same bytes, which is a test (§6), not an assumption.

### 5.8 The corrections that ship with it

- `installation.md`'s asset name becomes the real one, and the manual sequence
  drops `--ignore-missing` in favour of matching the archive's own line.
- The `latest/download` URL is replaced by a versioned one in the primary
  example, with `latest` shown separately and labelled as what it is: a moving
  target.
- A `docs-check` addition: every URL in the docs pointing at
  `github.com/morzecrew/morzer/releases/...` must use an asset name the
  goreleaser template can produce. This is a string check against the template,
  not a network call — CI must not depend on GitHub being up, and a network check
  would fail for every contributor working offline.

## 6. Tests

The script is shell, so it is tested the way this repository tests shell: by
running it.

- **`shellcheck` in CI**, with `sh` as the dialect so a bashism fails the build.
  The lint workflow already exists.
- **A container-lane test** that serves a fixture release over a local HTTP
  server — archive, `SHA256SUMS`, `SHA256SUMS.minisig` — and runs the script
  against it in a minimal image. Assertions: the binary lands, it is executable,
  `morzer version` reports the fixture version, and the temp directory is gone.
- **Verified-red failure cases**, each asserting a non-zero exit and a message
  naming the reason: a corrupted archive, a `SHA256SUMS` with the archive's line
  removed (the `--ignore-missing` trap), a signature made by a different key with
  `--require-signature`, a `--digest` that does not match, and an unwritable
  `--dir`.
- **Detection, through `--print-only`.** `uname` stubbed on `PATH` to report
  `Darwin`, `armv7l`, `riscv64` and `aarch64` in turn: the first three refuse by
  name, the fourth resolves the arm64 archive. This is the whole of §5.2 tested
  without a download, which is why `--print-only` prints what it detected rather
  than only what it would fetch.
- **PATH, four ways.** A prefix already on `PATH` writes nothing; a prefix that is
  not gets exactly one marked block *per file the shell needs* — two for bash and
  zsh, one drop-in for fish; running the script again adds nothing; and the
  generated block names the resolved prefix rather than a hardcoded
  `~/.local/bin`, asserted with a `--dir` somewhere else entirely.
- **The fish block is fish.** Sourcing the generated file with `fish -c` must
  succeed and must leave the prefix on `PATH`. A POSIX `case` in a `.fish` file
  is a syntax error on every subsequent shell start, and nothing but running it
  catches that.
  The third is the assertion that matters: an installer that appends on every
  run is the classic defect of this shape, and it is invisible until the second
  run.
- **A symlinked startup file is not written to**, and the block is printed
  instead. Reachable, and the failure is silent otherwise: a dotfiles repository
  reclaims the file at the next sync and the operator never learns why `morzer`
  stopped being found.
- **Completions are delegated, not reimplemented.** The stub goes at
  `<dir>/morzer` — the path the script actually invokes — not on `PATH`, where it
  would never be reached and the test would pass by never running. It records its
  argv, and the assertion is that `completion install --shell <detected>` was
  called. A test that checked for a completion *file* would pass on a script that
  wrote one itself, which is the thing this design forbids.
- **The install target is refused when it is not a regular file.** A symlink at
  `<dir>/morzer` (into a package manager's tree) and a directory at
  `<dir>/morzer`, each asserting a refusal that names what it found and that the
  symlink's target is unchanged. The startup-file symlink case above is a
  different file and does not cover this one.
- **A failing completion step does not fail the install.** The stub exits
  non-zero; the binary is still installed and the exit code is still 0.
- **The three published copies are byte-identical.** A checksum comparison
  between the repository file, the release asset and the deployed site file, run
  in the nightly job. `mike` owning the `gh-pages` root is the reason this is a
  test rather than an assumption.
- **`--print-only` against the real GitHub API**, in a nightly job rather than
  per-PR: it is the only test that can catch the asset name drifting, and it is
  the exact class of failure this RFC exists to fix. Nightly, because a
  network-dependent check on every PR is a flake generator.

## 7. Docs

- `pages/docs/get-started/installation.md` — the script first, the manual
  sequence second, and the "why both" paragraph between them.
- `README.md` — the one-liner.
- The goreleaser release footer — the script URL alongside the verify commands it
  already carries.

## 8. Out of scope

- **A `morzer upgrade` subcommand.** See non-goals; the script with a new
  `--version` is the upgrade path.
- **Checksums for the script itself beyond `SHA256SUMS`.** Verifying a script you
  are about to pipe into a shell requires downloading it first, which is the
  documented alternative to piping.
- **Mirrors.** One publication point plus the release assets. A mirror is a
  second trust root.
- **Uninstall.** `rm <dir>/morzer`. A machine with an installation is a different
  question, and the first-deployment guide already refuses to make *that* a
  one-liner.

## 9. Risks

- **`curl | sh` is a pattern with real critics**, and the criticism is correct in
  general: the reader executes what a server chose to send. The mitigations here
  are the ones that can be made — the manual path stays documented and stays
  equivalent, the script is versioned and checksummed as a release asset, it
  never escalates privileges, and it verifies before it installs. What cannot be
  mitigated is the first fetch, which is why `--print-only` exists and why the
  docs show the download-then-read form beside the pipe.
- **An embedded key ages.** A key rotation means every published copy of the
  script rejects the new signature. That is the correct failure — loud, at the
  verification step, naming the key — and rotation therefore ships a new script
  in the same release. Recorded so it is a plan rather than a surprise.
- **The nightly test depends on GitHub.** It will occasionally fail for reasons
  that are not this project's. It is nightly and it names what it checked, so a
  failure is triaged rather than blocking a PR.

## 10. Unresolved questions

- **Does `--version` accept a prerelease?** goreleaser marks them
  `prerelease: auto`, and "latest" must not resolve to one — but an operator
  naming `1.5.0-rc.1` explicitly is asking for it deliberately. The resolution
  rule needs writing down in the script's help, and it should match how
  [0016](0016-update-checking-and-unattended-updates.md) treats prereleases for
  *releases*: admissible when named, never when inferred.
- **Should the script offer `--check` (verify an already-installed binary)?**
  Cheap to add, and it is the question "is the thing on this machine what was
  published" — which is `doctor`'s kind of question and might belong there
  instead.

## 11. Decisions

| # | Decision | Why |
| --- | --- | --- |
| 1 | An sh script, not a Go subcommand | The machine does not have the binary yet. That is the whole problem, and it is also why `self-update` is a different question with a worse answer. |
| 2 | POSIX sh with no bashisms, `shellcheck`-clean in CI | The target is a freshly provisioned Linux box, where `/bin/sh` may be dash. A bashism fails on exactly the machine the script exists for. |
| 3 | The version is a parameter; `latest` is resolved and then printed | A runbook must pin. Everywhere else this project refuses to follow a moving reference without the operator saying so; the installer is not the place to make an exception. |
| 4 | `SHA256SUMS` is matched against the archive's own line, never `--ignore-missing` | The documented command passes when the download failed. That is the failure mode the script exists to remove, and it is currently in the docs. |
| 5 | The public key is embedded in the script, not fetched at install time | Fetching the key you verify against proves only that the server was self-consistent. Embedding also lets the script say something useful when the signing key changes. |
| 6 | Signature verification warns without `minisign`, and `--require-signature` makes it fatal | A default that failed on a machine with only curl and tar would push operators to skip verification entirely. A production runbook sets the flag. |
| 7 | The script never runs `sudo`; it prints the command to re-run | A network-fetched script that escalates on its own initiative deserves the distrust it gets. |
| 8 | Published at a stable site URL *and* as a release asset covered by `SHA256SUMS` | The site URL is what a README can carry for years; the asset is what an air-gapped-ish machine already has, and it closes the "who verifies the verifier" loop. |
| 9 | No `self-update`, no packages, no mirrors | Each adds a trust root or a running-unit hazard, and none of them is the path a new operator takes. |
| 10 | A nightly `--print-only` job against the real release API | The broken asset name in today's docs is exactly the drift no offline check can catch. Nightly rather than per-PR, so a GitHub outage is not a red build. |
| 11 | The canonical URL is `https://morzecrew.github.io/morzer/install.sh`, at the `gh-pages` root beside the version directories | The site is versioned by `mike`, so any URL inside a version directory pins the installer to a docs release it has nothing to do with. The root is the only place with the right lifetime, and `raw.githubusercontent.com` is the documented equivalent — already how `morzer.pub` is fetched. |
| 12 | Detection covers os, arch, kernel, shell and `PATH`, and stops there | Those decide which archive to fetch and whether the result is usable. Everything else about the machine is `doctor`'s, which runs a minute later and is thorough. A second auditor is one nobody maintains. |
| 13 | An unrecognised architecture is refused by name, never guessed | Guessing downloads an archive whose binary the kernel will not exec, and the failure surfaces several steps later with a much worse message. |
| 14 | `PATH` is edited in one marked block, in one file per shell, printed before it is written, and `--no-modify-path` opts out | The marker makes re-running idempotent, which is the defect every installer of this shape eventually has. One file avoids rustup's four. Printing first is what makes a piped-into-`sh` script auditable after the fact. |
| 15 | A symlinked or unwritable startup file is printed to, not written to | A symlink into a dotfiles repository means the file is generated; appending to it loses the edit at the next sync, silently. |
| 15a | Both the login file and the interactive file are written for bash and zsh; `~/.zshenv` never is | Neither covers the other — bash reads the first of `~/.bash_profile`/`~/.bash_login`/`~/.profile` and stops, and `~/.bashrc` is the interactive one — so a single file leaves half the sessions without the prefix. `~/.zshenv` would put a prepend in front of every non-interactive script that never asked for it. |
| 15b | The guarantee is "a new login shell and a new interactive shell find it"; non-interactive non-login is out | `ssh host morzer status` reads no startup file on any shell. That is a property of shells, and a guarantee written wider than it holds is worse than a narrow one. |
| 15c | The block is generated from the resolved prefix, in the target shell's syntax | It is written for whatever `--dir` resolved to, and a POSIX `case` in a `.fish` file is a syntax error at every subsequent shell start. |
| 15d | No `--system`, and `/etc/profile.d` is never touched | The system prefix is already on everyone's `PATH`; the only use for writing there is to put one user's prefix on everyone's, which is not an installer's decision. An undefined security-sensitive flag left in a document gets implemented by guess. |
| 15e | `--require-signature` and `--no-verify-signature` together are refused before anything is downloaded | Letting argument order decide whether verification happens is how a runbook that inherited both silently stops verifying. |
| 16 | Completions are installed by running `morzer completion install`, never by the script itself | Where a shell reads completions from belongs in one implementation, versioned with the binary and tested. A completion written to the wrong directory produces no error — just a Tab key that does nothing — so a drifting second copy would never announce itself. |
| 17 | Completions default on for an interactive install, off otherwise; failure warns and never fails the install | Somebody at a terminal wants the tool to work; a Dockerfile does not want writes into a build's home directory. And the binary being installed is the thing that was asked for. |

## 12. Phasing

- **P1 — The corrections, alone.** The asset name, the `--ignore-missing`
  removal, the versioned URL, and the `docs-check` assertion that a documented
  release URL matches the goreleaser template. This is a bug fix and needs
  nothing else in this RFC.
- **P2 — The script: fetch, verify, install.** `install.sh` with detection
  (§5.2), verification (§5.3) and the write (§5.6); `shellcheck` in CI; the
  container-lane test with its failure cases; publication as a release asset.
  Stops at "the binary is on the disk and `--print-only` explains everything".
- **P3 — PATH.** The marked block, the per-shell file, `--no-modify-path`, the
  symlink refusal, and the second-run test. Separable from P2 and the piece most
  likely to want a round of opinions before it edits anyone's dotfiles.
- **P4 — Completions.** One call to `morzer completion install`, its defaults and
  its non-fatal failure. **Gated on [0019](0019-the-command-surface.md) P5**;
  until that ships the script prints the invocation, which is already its
  behaviour for an unrecognised shell.
- **P5 — The site URL and the docs rewrite.** Publishing the script at the
  `gh-pages` root beside `mike`'s version directories, re-published on every docs
  release, plus the identical-copies check.
- **P6 — The nightly drift job.** Independent; last because it is the only piece
  that depends on a release existing.
