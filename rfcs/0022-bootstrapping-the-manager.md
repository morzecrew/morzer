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
curl -fsSL https://morze.dev/install.sh | sh -s -- --version 1.4.0
```

and keeps the long form documented underneath, because the long form is what the
script is doing and an operator who wants to check its work needs to be able to.

The script verifies before it installs: the checksum always, the minisign
signature whenever `minisign` is present, and a `--digest` the caller pins when
they have one. It refuses to write outside a prefix it names, it never runs
`sudo` on its own initiative, and it is the same script the release artefacts
carry — so a machine that can reach the release can bootstrap without reaching
anything else.

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

## 5. Design

### 5.1 The interface

```text
install.sh [--version X.Y.Z] [--dir PATH] [--digest sha256:…]
           [--require-signature] [--no-verify-signature] [--print-only]

  --version              Release to install. Default: the latest published
                         release, resolved once and then printed.
  --dir                  Install prefix. Default: /usr/local/bin when writable,
                         otherwise $HOME/.local/bin.
  --digest               Expected sha256 of the archive. When given, it is
                         checked *before* SHA256SUMS and a mismatch is fatal.
  --require-signature    Refuse to install when minisign is absent or the
                         signature does not verify.
  --no-verify-signature  Skip the signature check. Prints what it is skipping.
  --print-only           Print the resolved version, URL and target path; do not
                         download.
```

Environment equivalents (`MORZER_VERSION`, `MORZER_INSTALL_DIR`) for the
`curl | sh` form, where passing flags means `sh -s --` and half of the readers
will get it wrong.

### 5.2 What it verifies, and in what order

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

### 5.3 Where it writes, and what it never does

- Writes exactly one file: `<dir>/morzer`. The schemas and licence in the archive
  are not installed anywhere; they exist for a vendor who wants them.
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

### 5.4 Publication and the URL that is stable

The script is published in two places, and both matter:

- **`https://morze.dev/install.sh`** — served from the documentation site, which
  is already built and deployed from this repository. This is the URL that goes
  in the README and stays constant.
- **As a release asset** — so a machine that has the release tarball can
  bootstrap without reaching the site, and so the script that installed a version
  is archived beside it.

`goreleaser`'s `extra_files` carries it, which also means the script is covered
by `SHA256SUMS` — the script that verifies the archive is itself verifiable
against the same file, for anyone who wants to close that loop.

### 5.5 The corrections that ship with it

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

## 12. Phasing

- **P1 — The corrections, alone.** The asset name, the `--ignore-missing`
  removal, the versioned URL, and the `docs-check` assertion that a documented
  release URL matches the goreleaser template. This is a bug fix and needs
  nothing else in this RFC.
- **P2 — The script.** `install.sh`, `shellcheck` in CI, the container-lane test
  with its failure cases, publication as a release asset.
- **P3 — The site URL and the docs rewrite.** Gated on P2 and on the
  documentation site serving a non-page file, which is a build question rather
  than a design one.
- **P4 — The nightly drift job.** Independent; last because it is the only piece
  that depends on a release existing.
