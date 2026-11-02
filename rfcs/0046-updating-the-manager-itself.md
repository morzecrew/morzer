# RFC 0046 — Updating the manager itself

- **Status:** 📝 Draft — proposed, and ruled by the author 2026-11-02: §11's
  `OPEN` rows as recommended, so RFC 0022 decision 9 is reopened for
  `self-update`. **P1 is ready to build**; P2 follows it
- **Scope:** How an operator learns that the `morzer` binary is out of date,
  and how it moves to a newer one. P1 is a check that compares the running
  manager with the newest published release, on request
  (`morzer version --check`) and in `doctor` under consent, and an upgrade
  how-to built on `install.sh`. P2, if the author reopens RFC 0022 decision 9,
  is `morzer self-update`. It fetches a published release, verifies its signed
  checksums against a key embedded in the binary, and replaces the binary in
  place while it holds every installation's deployment lock. Not covered:
  updating the manager without a human, distribution packages and mirrors
  (still refused by 0022 decision 9), downgrading the manager (still a re-run
  of `install.sh`), and anything about a product's releases. One new
  per-installation setting, `manager.check`, stored beside `update.check`;
  absent reads as false, and an older manager reads past it, so there is no
  installation schema change.
- **Related:** [0022](0022-bootstrapping-the-manager.md) (§4's non-goal and
  decision 9, which refuse `self-update`);
  [`install.sh`](../install.sh), [`.goreleaser.yaml`](../.goreleaser.yaml),
  [`morzer.pub`](../morzer.pub);
  [`internal/adapters/verify/minisign/minisign.go`](../internal/adapters/verify/minisign/minisign.go);
  [`internal/adapters/supervisor/systemd/systemd.go`](../internal/adapters/supervisor/systemd/systemd.go)
  (`ManagerPath`); [`internal/domain/installation.go`](../internal/domain/installation.go)
  (`UpdateConfig.Check`, the newer-state refusal);
  [`internal/lifecycle/ops/list.go`](../internal/lifecycle/ops/list.go)
  (`ListInstallations`); [SECURITY.md](../SECURITY.md) "Supported versions";
  [0016](0016-update-checking-and-unattended-updates.md) (consent for update
  checks), [0020](0020-several-installations-on-one-machine.md) (one binary,
  several installations).

---

## 1. Summary

Nothing tells an operator that their manager is old, and nothing upgrades it
but re-running the installer. This RFC proposes two phases. P1 is a check that
asks the release feed for the newest published manager: always when the
operator asks with `morzer version --check`, and in `doctor` when the
installation has consented with a new `manager.check` setting. It comes with a
page that says how to upgrade. P2 is `morzer self-update`, which RFC 0022
refused by name. Whether to build it is the first decision for the author.
If built, it trusts nothing new: the same signed `SHA256SUMS` and the same key
`install.sh` already embeds, verified in-process and always required. It
answers 0022's running-unit objection by swapping the binary only while it
holds the deployment lock of every installation on the machine.

## 2. Motivation

The gap report lists it under manager lifecycle: no self-update, nothing says
the manager is outdated, and no upgrade how-to.

- **SECURITY.md supports one release.** "The latest 0.x release, and nothing
  else": a fix ships in the next release, and an operator on an older one must
  upgrade to get it. Nothing on the machine says a newer one exists, so the
  operator learns from the release page or not at all.
- **The first sign is usually a refusal.** A bundle that needs a newer manager
  declares `min_manager_version`, and `update` refuses it as too new. On a
  machine following a channel, that refusal arrives on an unattended tick at
  night: the release is staged and cannot be installed.
- **Fixes to the manager itself reach nobody who does not reinstall.** 0.12.0
  fixed hooks that could hold an operation past their timeout, a hook that is a
  directory passing `release verify`, and a description bound that counted
  bytes. A machine still on 0.11.0 has all three, and nothing says so.

## 3. Current state

Verified against `main` at 4766a54 and later.

- **Installing is `install.sh`** ([RFC 0022](0022-bootstrapping-the-manager.md)).
  It takes a version or resolves the newest through the GitHub releases API,
  downloads `morzer_<version>_linux_<arch>.tar.zst`, and matches it against
  its own line in `SHA256SUMS`. It checks `SHA256SUMS.minisig` against the key
  it embeds, `6244CB37DB91DD52`, and `test/installer/install_test.go` checks
  that the script carries each line of `morzer.pub`. Without the `minisign`
  tool it only warns, unless `--require-signature` is given (0022 decision 6).
  It never runs `sudo` (decision 7).
- **The release pipeline signs the checksums, not each archive**
  ([`.goreleaser.yaml`](../.goreleaser.yaml) `signs`): one `SHA256SUMS.minisig`
  covers every archive.
- **The binary carries no copy of the key.** The manager verifies minisign
  signatures in-process for release bundles, through a Go library
  ([`verify/minisign`](../internal/adapters/verify/minisign/minisign.go)), so
  checking the manager's own signature needs no external tool.
- **Units name the binary by path.** Every unit runs
  `ExecStart=<ManagerPath> …`, where `ManagerPath` is the running binary's
  resolved path (`systemd.ManagerPath`, `os.Executable` then `EvalSymlinks`).
  Replacing the file at that path changes what every timer runs next, and
  leaves a process already running on the old file.
- **State migrates forward only.** An installation written by a newer manager is
  refused at exit 9: "installation was written by a newer manager … state
  migrations only run forward" ([`installation.go:1022`](../internal/domain/installation.go)).
  So moving the manager back after it has touched state fails.
- **One binary serves every installation on a machine**
  ([0020](0020-several-installations-on-one-machine.md)).
  `ListInstallations` finds them, and each has its own deployment lock.
- **Consent for update checks is per installation.** `update.check` enables the
  unprompted check of the *vendor's* registry, off by default, because it
  reveals an IP, a time and by inference a version
  ([`UpdateConfig.Check`](../internal/domain/installation.go)). An explicit
  command needs no setting (`CheckAllowed`).
- **`morzer version`** prints the build and the manifest API versions it reads,
  and contacts nothing.

## 4. Goals / Non-goals

**Goals**

- An operator can ask whether a newer manager exists, and a machine that
  consented hears it from `doctor`, including the scheduled one.
- Upgrading is a documented path with no surprises about units, state or
  several installations.
- If `self-update` is built, it adds no trust root and no running-unit hazard:
  the key is the one `install.sh` embeds, the signature is always required, and
  the swap happens with no operation running anywhere on the machine.

**Non-goals**

- **Updating the manager without a human.** A product's unattended updates are
  gated by what the release declares about its own failure. Nothing comparable
  describes a manager release, and a broken one would land on every machine at
  once.
- **Packages and mirrors.** RFC 0022 decision 9 refuses both, and nothing here
  reopens them.
- **Moving the manager back.** State migrates forward only, so a downgrade is a
  deliberate act with consequences. It stays a re-run of `install.sh`, and the
  how-to says what it costs.

## 5. Design

### 5.1 P1: knowing the manager is out of date

`morzer version --check` asks the release feed for the newest published
release: not a draft, and not a prerelease. It prints the running version
beside it, and whether the running version is still supported under
SECURITY.md's rule. It is explicit, so it needs no setting, as `update --check`
needs none. A machine that cannot reach the feed gets an error saying so, and a
dev build reports that it has no version to compare.

`doctor` gains `manager.version`, run when the installation's `manager.check`
is true (decision 2). It warns when a newer release exists, naming both
versions and whether the running one is still supported (decision 3), with the
upgrade command as its remedy. When the setting is off, it reports "not
checked" as information. The scheduled `doctor` reports the warning like any
other check, and a configured target that takes warnings is notified of it.

`manager.check` is stored in the installation's settings beside
`update.check`, and absent reads as false. Installation state is decoded
leniently, so an older manager reads past the field. A downgrade alone keeps
it, but the first time an older manager rewrites the installation the field is
dropped, and the consent has to be given again. That is why no schema bump is
needed.

```text
manager
  ! a newer manager is published
    0.11.0 is running and no longer supported; 0.12.0 is the newest release
    upgrade with install.sh --version 0.12.0 --require-signature
```

### 5.2 P1: the upgrade how-to

A page under `operating/`, "Upgrading the manager". It says:

- run `install.sh --version <new> --require-signature --dir <dir>`, where
  `<dir>` is the directory of the binary the units name (`systemctl cat
  <product>.service` shows it). Left to itself, the script picks
  `/usr/local/bin` or `~/.local/bin`, and a manager installed anywhere else
  would get a second copy while the units go on running the old one;
- the units need nothing, since they name the path, which is unchanged, and the
  next `apply`, `start` or `update` reconciles them anyway;
- an operation already running finishes on the old binary;
- every installation on the machine moves at once;
- state is migrated forward by the new manager on its next write, after which
  an older manager refuses it, so going back means reinstalling the older
  version knowing that.

### 5.3 P2: `morzer self-update`

```sh
morzer self-update            # the newest published release
morzer self-update --to 0.12.1
morzer self-update --check    # what version --check says, and nothing else
morzer self-update --from ./downloads   # archive, SHA256SUMS and .minisig, offline;
                                        # the version is the archive's in SHA256SUMS
```

In order:

1. **Resolve the target**: the newest release, or `--to`. With `--from`, the
   target is the version in the name of this platform's archive in the
   directory's `SHA256SUMS`, and the feed is not asked. A sums file names one
   release, and `--to` must agree with it when both are given. A target older
   than the running manager is refused, and so is the same version
   (decision 5).
2. **Fetch** the archive for this platform, `SHA256SUMS` and
   `SHA256SUMS.minisig` from the release, or read the three from `--from`.
3. **Verify** the signature over `SHA256SUMS`, then the archive against its own
   line. The key is a copy of `morzer.pub` embedded in the binary at build
   time, and a test asserts it is byte for byte the file at the repository's
   root. That is stricter than the test for `install.sh`'s copy, which checks
   that the script carries each line of the file. It is passed to the
   in-process minisign verifier as the only public key, with the signature
   required. No flag, setting or environment variable changes the key or the
   requirement (decision 6).
4. **Unpack** the binary to a temporary file in the running binary's
   directory, so the final rename is atomic. Run it with `version --json`, and
   require it to report the version it was fetched as (decision 8).
5. **Take every installation's deployment lock and notification-outbox lock**
   on the machine without waiting, and refuse if any is held, naming the
   installation and what holds it (decision 7). Under the locks, ask the
   binary now at the path for its version again, and refuse unless the target
   is newer, since another `self-update` may have finished since the command
   started. A binary with no release version, a dev build, passes only if it is
   still the file it was when the command started (decision 12).
6. **Rename** the new binary over the running one. A process already running
   keeps the old file, and the next one any unit starts is the new manager.
7. **Record** a `manager-update` entry in each installation's journal while
   still holding the locks (decision 11), then **release** them, and print the
   old and new versions with a link to the release notes. A crash between the
   rename and the last entry leaves the new binary in place with the record
   missing from some journals; `morzer version` still says what is installed.

It refuses, before anything is fetched:

- a running binary that is a dev build with no version, unless `--to` names
  one (decision 12);
- a directory this user cannot write to. It prints the command to re-run with
  `sudo` and never escalates itself, as `install.sh` does (decision 9).

When the units on this machine name a different path from the running binary,
it warns and names that path, since replacing this copy leaves them on the
other one (decision 13).

### 5.4 The running-unit case 0022 asked for

RFC 0022 refused `self-update` because "a manager that replaces its own binary
while a systemd timer may be running it is a class of bug this project does not
need", and named the condition for reopening it: a story for the running-unit
case. This is that story:

- **No operation is in flight during the swap.** It holds every installation's
  deployment lock, and every command that changes an installation takes one,
  whether an operator or a unit runs it. The locks are taken without waiting,
  so an operation that started first holds its lock and the swap is refused. A
  boot `apply`, an update tick or a scheduled backup that fires during the swap
  meets a held lock. A tick exits 0, as it already does when an operator's
  operation holds it.
- **The scheduled `doctor` writes only the notification outbox.** The outbox
  lock, which the swap holds too, keeps two senders from sending one entry. It
  does not cover a failed delivery, which writes its entry without it. That
  entry is a file of its own holding one event, decoded leniently, so one
  written by the old binary during or after the swap is read by the new one as
  across any upgrade. A `status` or `doctor` running during the swap otherwise
  reads and finishes on the old file, since the rename does not touch an open
  executable.
- **A command that started before the swap can still run on the old binary
  after it.** One that was loading or fetching before it took its lock, or was
  waiting with `--wait`, takes the lock once the swap releases it. What it
  meets is decided by the forward-only state rule. If the new manager has not
  written that installation yet, the old one reads and writes it as before, and
  the new one migrates it on its next write. If it has, the old one refuses at
  exit 9, "written by a newer manager". Nothing is misread, and the operator
  runs the command again.
- **An installation being created during the swap is not locked by it.**
  `ListInstallations` finds an installation once its `installation.yaml`
  exists. An `init` that has not written it yet finishes on the old binary, and
  its state is migrated by the new manager on its next write, as after any
  upgrade.
- **The swap is one rename** in the same directory. A crash before it leaves
  the old binary and a temporary file, and a crash after it leaves the new one.
  There is no state between them a unit could observe.
- **State moves forward only.** The first command the new binary runs that
  writes an installation's state migrates it, as after any upgrade with
  `install.sh`, and from then on the old binary refuses it rather than
  misreading it.

### 5.5 Alternatives considered

- **P1 alone, keeping 0022 decision 9.** The check and the how-to remove most
  of the gap, and add no code that writes over the manager. This is the
  alternative to decision 1: it costs operators a curl-and-pipe per upgrade,
  and costs this project nothing it does not already maintain.
- **A configurable release feed or mirror.** It adds a setting whose wrong
  value changes what a machine trusts to replace its manager. `--from` covers a
  machine without the internet with three files an operator carries in, which
  is how `install.sh` is already used there.
- **Keeping the previous binary as `morzer.previous` for a rollback.** It looks
  free and is not. The new manager may already have migrated state that the
  previous one refuses, so a rollback command would promise what the state
  schema cannot keep.
- **Running `self-update` from a unit.** That is an unattended manager update,
  a non-goal (§4).

## 6. Tests

- **P1:** `version --check` against a fake release feed: a newer release, the
  same release, a prerelease that is ignored, a running release no longer
  supported, an unreachable feed, a dev build. `manager.version` in `doctor`:
  "not checked" without consent, a warning with it, and the scheduled run
  notifying it.
- **P2:** a fake feed serving an archive signed with a test key that the test
  binary embeds in place of the release key:
  - refused: a bad signature, a different key, a checksum mismatch, an archive
    with no line, a downgrade, the same version, a dev build without `--to`, an
    unwritable directory, a held lock;
  - the new binary run before the swap, refused when it reports another
    version;
  - the swap leaving a process started before it on the old file;
  - a command that took its lock after the swap on the old binary: it reads an
    installation the new manager has not written, and refuses at exit 9 on one
    it has;
  - an outbox lock held by the scheduled `doctor` refusing the swap, as a held
    deployment lock does;
  - the journal entry in every installation;
  - `--from` reading the three files offline.
- The embedded key equals `morzer.pub` byte for byte, a stricter check than
  the per-line one for `install.sh`'s copy.

## 7. Docs

- `operating/upgrading-the-manager.md`, new (§5.2), linked from the install
  page.
- The command reference: `version --check`, and `self-update` with its
  refusals, if built.
- The settings reference: `manager.check`.
- SECURITY.md: how a self-update is verified, and that it needs no `minisign`
  tool, unlike `install.sh`.
- RFC 0022 is not amended. This RFC records the reopening, and if decision 1
  is ruled yes, 0022 gains a row naming this one.

## 8. Out of scope

- **Unattended manager updates** (§4). They would reopen with a manager
  release format that declares something comparable to a product's
  `rollback_safe`.
- **Downgrade and rollback of the manager** (§5.5).
- **Signing-key rotation** beyond embedding a list of keys. A rotation ships
  the next key in a release signed by the current one, and is its own
  procedure when it happens.
- **Platforms other than Linux** on `amd64` and `arm64`: the pipeline builds
  nothing else.

## 9. Risks

- **A broken manager release reaches every machine that self-updates.** This is
  the reason there is no unattended path. `--to` pins a version, and the how-to
  says to read the release notes first.
- **The release feed's rate limit.** Unauthenticated GitHub API requests are
  limited per IP, 60 an hour. A scheduled `doctor` makes one request per
  consenting installation per run, so a machine with dozens of installations
  stays inside it without a shared cache. A machine behind a shared NAT that
  runs `version --check` in a loop may hit the limit, and gets an error, not a
  wrong answer.
- **`install.sh` and `self-update` at the same moment.** The script takes none
  of these locks, and running both at once is not supported. Each replaces the
  binary with one atomic rename, so the result is one whole binary, possibly
  the older of the two; `morzer version` names it, and either command run again
  settles it.
- **A compromised release key.** The same key already decides what
  `install.sh` installs. `self-update` adds no new trust root, and the
  mitigation is the same as for `install.sh`: SECURITY.md says what to do when
  the key changes.
- **An upgrade across a state schema bump.** After the new manager writes, an
  older one refuses the state. That is the existing rule, and the how-to and
  the command's summary both say so.

## 10. Unresolved questions

The `OPEN` rows in §11 are the author's to rule. Nothing else blocks the design.

*(2026-11-02: ruled. Every `OPEN` row went as recommended, and §11 records each
ruling, so P2 is no longer conditional on decision 1.)*

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `ASSUMED` | **Reopen RFC 0022 decision 9 and build `self-update` as P2?** Recommended yes, with §5.4's answer to the running-unit case: the swap waits for no operation, because it holds every installation's lock, and it trusts only the key `install.sh` already embeds, so it adds no trust root. Alternative: keep 0022's refusal and ship P1 alone, the check and the how-to. Was `OPEN`; ruled by the author 2026-11-02: as recommended, `self-update` is built as P2, and RFC 0022 gains a row naming this one. |
| 2 | `ASSUMED` | **Consent for the unprompted check.** Recommended a new per-installation setting, `manager.check`, default off: the check contacts GitHub, a party other than the vendor whose registry `update.check` consents to. `version --check` and `self-update` are explicit and need neither. Alternative: `update.check` covers both, one consent for learning about updates. Was `OPEN`; ruled by the author 2026-11-02: as recommended, a separate `manager.check`, default off. |
| 3 | `ASSUMED` | **What `doctor` warns about.** Recommended: any newer published release, naming whether the running one is still supported, since SECURITY.md supports only the newest. Alternative: warn only when the running release is no longer supported, and report a newer patch as information. Was `OPEN`; ruled by the author 2026-11-02: as recommended, any newer published release warns. |
| 4 | `ASSUMED` | **Where releases come from.** Recommended: GitHub releases, as `install.sh` uses, plus `--from` with the three files for a machine without the internet; no configurable feed (0022 decision 9's "no mirrors"). Alternative: a configurable base URL. Was `OPEN`; ruled by the author 2026-11-02: as recommended, GitHub releases plus `--from`, and no configurable feed. |
| 5 | `ASSUMED` | **Going back.** Recommended: `self-update` refuses an older target, and moving back is a re-run of `install.sh` with the how-to's warning about forward-only state. Alternative: `--allow-older`, journalled, as RFC 0037 gives `update`. Was `OPEN`; ruled by the author 2026-11-02: as recommended, `self-update` refuses an older target. |
| 6 | `LOCKED` | A self-update verifies `SHA256SUMS.minisig` with a copy of `morzer.pub` embedded in the binary, asserted equal to the repository's, as the only key and with the signature required, and the archive against its own line, every time; no flag, setting or environment variable changes the key or skips either check. Allowing an unverified binary to replace the manager would make this the weakest path onto a machine, and reversing it later could not recall what it let through. |
| 7 | `ASSUMED` | The swap happens while this command holds every installation's deployment lock and notification-outbox lock, taken without waiting; a held lock refuses it and names what holds it. Under the locks, the version of the binary at the path is read again, and the swap is refused unless the target is newer. |
| 8 | `ASSUMED` | The new binary is unpacked beside the running one and run with `version --json` before the swap, and must report the version it was fetched as. |
| 9 | `ASSUMED` | It never escalates privileges: a directory it cannot write is a refusal that prints the `sudo` command, as `install.sh` does (0022 decision 7). |
| 10 | `ASSUMED` | No unit and no timer runs `self-update`; it is an operator's command only. |
| 11 | `ASSUMED` | Each installation's journal records a `manager-update` entry with the old and new versions, written while the locks are still held, so an incident review finds when the manager changed. A crash after the rename can leave some journals without it. |
| 12 | `ASSUMED` | A dev build, whose version is not a release, never reports itself outdated, and refuses `self-update` unless `--to` names a version. Its re-check under the locks requires the binary at the path to be the file it was when the command started, since it has no version to compare. |
| 13 | `ASSUMED` | When the units on the machine name another binary path than the running one, `self-update` warns and names it, since replacing this copy leaves the units on the other. The how-to says to pass `install.sh` the directory the units name, for the same reason. |
| 14 | `ASSUMED` | With `--from`, the target is the version in the name of this platform's archive in that directory's `SHA256SUMS`, and the release feed is not asked; `--to`, if given, must agree with it. |
| 15 | `ASSUMED` | A command that started before the swap and runs on the old binary after it is not stopped: it reads state the new manager has not yet written, or refuses at exit 9 on state it has. The forward-only state rule makes the overlap a refusal, never a misreading. An installation being created during the swap is in the same position. |

## 12. Phasing

- **P1** (§5.1, §5.2): `version --check`, the `manager.version` check and
  `manager.check`, and the upgrade page. One PR. It needs decisions 2 and 3.
- **P2** (§5.3): `self-update`, if decision 1 is ruled yes. One PR, after P1,
  whose release-feed client it reuses.
