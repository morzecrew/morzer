# manager-self-update · RFC 0046 P2, `morzer self-update`

On branch `manager-self-update` from `78bc64f`, building
[RFC 0046](../rfcs/0046-updating-the-manager-itself.md) P2 as ruled by the
author on 2026-11-02: decision 1 reopened RFC 0022 decision 9 for
`self-update`, and decisions 6–15 describe how it trusts, locks, swaps and
records. P1, the check and the upgrade page, is #257, and this reuses its
release-feed adapter.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## Plan

- `internal/ports/manager.go`: the feed gains `Download`, and a new
  `ManagerUnpacker` port verifies, unpacks and asks a binary its version.
- `internal/adapters/managerfeed/github`: `Download` fetches this platform's
  archive, `SHA256SUMS` and its signature from the release (decision 4).
- `internal/adapters/managerfeed/signed`, new: the embedded copy of
  `morzer.pub`, the only key, signature required; the archive against its
  own line; extraction; `version --json` (decisions 6 and 8).
- `internal/ports/supervisor.go` and `systemd`: the binary a product's
  installed units run, for decision 13's warning.
- `internal/lifecycle/ops/self_update.go`, new: resolve, fetch, verify,
  unpack and run, take every installation's two locks without waiting,
  re-check, rename, journal, release (§5.3, decisions 5, 7, 9, 11, 12, 14).
- `internal/domain/operation.go`: `manager-update`.
- `Deps.LockerFor`, beside `StateFor`, so one command can lock another
  installation.
- CLI `self-update` with `--to`, `--from` and `--check`, machine-scoped; a
  view; the command reference, the upgrade page, SECURITY.md, CHANGELOG.

Found while planning, and decided before any code.

§5.3 refuses the same version, and decision 5 an older one. Neither says
whether finding nothing newer is a failure, and a `self-update` run on a
machine that is already current is the ordinary case.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-03T06:33:00Z
attempt: 1
claim: the RFC refuses the same version and an older one without saying what the command exits with, and an operator running it on a current machine is not making a mistake
evidence: rfcs/0046-updating-the-manager-itself.md:194-199
action: decided
proposal: ASSUMED — a target that is not newer is never swapped. Without `--to` or `--from`, nothing newer published is reported as such and exits 0, as is a target equal to the running version. A target older than the running version that the operator named, with `--to` or by `--from`, is refused at exit 2 (decision 5).
```

The global `--dry-run` flag reaches every command, and a `self-update` that
ignored it would replace the binary of an operator who asked it not to.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-03T06:33:00Z
attempt: 1
claim: §5.3 does not mention `--dry-run`, which every command accepts
evidence: internal/cli/root.go:552
action: decided
proposal: ASSUMED — with `--dry-run`, `self-update` resolves, fetches, verifies, unpacks and runs the new binary, then stops before taking any lock, reports what it would have replaced, and removes the staged binary. Nothing at the path changes.
```

Decision 7 locks "every installation". Discovery also finds directories under
`/etc` it cannot look inside, and `ls` reports them without counting them as
installations: `/etc/credstore` on any host is one.

```divergence
decision: 7
grade: ASSUMED
class: spec-gap
at: 2026-11-03T06:33:00Z
attempt: 1
claim: a directory discovery cannot read is neither an installation nor evidence of none, and refusing on one would refuse every unprivileged run on a real host
evidence: internal/lifecycle/ops/list.go:113-127
action: departed
proposal: ASSUMED — `self-update` locks every installation discovery finds and none of the directories it cannot read, and names those in a warning, as `ls` lists them without counting them. An unprivileged process that cannot read an installation is also one that cannot write the root-owned directory its units run the binary from, which decision 9 refuses first.
```

Found while building.

Decision 12 lets a dev build update only when `--to` names a version, so that
it never guesses the newest. `--from` names one too: the files carry it
(decision 14).

```divergence
decision: 12
grade: ASSUMED
class: spec-gap
at: 2026-11-03T06:37:00Z
attempt: 1
claim: decision 12 names only `--to`, and `--from` settles the version just as explicitly
evidence: internal/lifecycle/ops/self_update.go:79-83
action: departed
proposal: ASSUMED — a dev build refuses `self-update` unless `--to` or `--from` names the release.
```

§5.3 refuses an unwritable directory "before anything is fetched". Refusing
before asking the feed would tell an unprivileged operator on a current
machine to re-run with `sudo` for nothing.

```divergence
decision: 9
grade: ASSUMED
class: discovery
at: 2026-11-03T06:37:00Z
attempt: 1
claim: checked first, the directory refusal answers "re-run with sudo" on a machine that has nothing to update
evidence: internal/lifecycle/ops/self_update.go:85-108
action: departed
proposal: ASSUMED — the directory is checked after the target is resolved and found newer, and before the release is downloaded, so nothing is downloaded into a command that cannot finish and nothing newer is never a refusal.
```

Decision 6 is `LOCKED`, and this branch builds the code it governs.

```divergence
decision: 6
grade: LOCKED
kind: resolved
at: 2026-11-03T06:51:00Z
attempt: 1
claim: touched the governed area; the self-update verifies SHA256SUMS.minisig with a compiled-in copy of morzer.pub as the only key, signature required, then the archive against its own line, and no flag, setting or environment variable reaches either
evidence: internal/adapters/managerfeed/signed/signed.go:35-168
action: decided
```

## Self-audit, 2026-11-03

Whole branch against `78bc64f`: the ports, both adapters, `self_update.go`,
the CLI and view, tests and docs. Each finding below is fixed on the branch.

- **The bytes verified were not the bytes unpacked.** `Unpack` read
  `SHA256SUMS` once to check the signature and again to find the line, hashed
  the archive, then opened it again to extract. A `--from` directory another
  user can write could swap a file in between, and the swapped binary would be
  run, as root, and installed. Now the sums are read once, and the archive is
  copied into a private directory and hashed there before it is unpacked. No
  test drives the race; the fix removes the second read.
- **A manager that could not find itself would replace `./morzer`.**
  `systemd.ManagerPath` falls back to the bare name when `os.Executable`
  fails, and the directory of that is the working directory. `self-update` now
  refuses a path that is not absolute, and a refusal case pins it.
- **`--to` naming the running release said it was the newest.** The view now
  says there is nothing to install, which is true either way.
- **The downgrade hint said any newer manager's state is refused by an older
  one.** Decision 22 narrowed that to a schema change, and the hint now says
  so.

Rule: when a check and a use read the same file, read it once, or the check
covers a file nobody used.

**Drift count: 0.**

## From #259's review, 2026-11-03

The class of the decision 9 entry above was wrong. Ordering the refusal
before the feed's answer has a consequence anybody reading §5.3 could have
seen, so the spec was silent, not the build surprised. The entry stays as
written; this one supersedes its class.

```divergence
decision: 9
grade: ASSUMED
class: spec-gap
at: 2026-11-03T07:22:00Z
attempt: 2
claim: the unwritable-directory refusal ordered before the feed's answer tells an operator on a current machine to use sudo for nothing, which §5.3 could have settled
evidence: internal/lifecycle/ops/self_update.go:130-145
action: departed
proposal: ASSUMED — as attempt 1 proposed: the directory is checked once the target is resolved and newer, before anything is downloaded.
```

Decision 7's locks are every installation's. With none on the machine there
is no lock at all, and two `self-update` runs could both pass the re-check
before either renamed, the older one last.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-03T07:22:00Z
attempt: 2
claim: the installations' locks are the only exclusion between two self-updates, and a machine with no installation has none
evidence: internal/lifecycle/ops/self_update.go:205-214
action: decided
proposal: ASSUMED — `self-update` also takes a non-waiting flock on the binary's directory before the installations' locks and holds it through the rename and the journal entries. The directory's inode survives the rename, which a lock on the binary would not, and it leaves no file behind.
```

Decision 11 records "the old and new versions". Another `self-update` can
install a release between this one's start and its locks.

```divergence
decision: 11
grade: ASSUMED
class: spec-gap
at: 2026-11-03T07:22:00Z
attempt: 2
claim: the old version could be read at the start or under the locks, and only the second is what the swap replaced
evidence: internal/lifecycle/ops/self_update.go:240-249
action: departed
proposal: ASSUMED — the version the journal and the report name as replaced is the one read under the locks.
```

P1's check printed the feed's own `html_url`. That is text the feed chose,
written to the operator's terminal.

```divergence
decision: 17
grade: ASSUMED
class: spec-gap
at: 2026-11-03T07:22:00Z
attempt: 2
claim: a release page taken from the feed is free text on the terminal, where a tag that parsed as a version is not
evidence: internal/adapters/managerfeed/github/github.go:183-187
action: departed
proposal: ASSUMED — the release page is built from the tag that parsed as a version, as `https://github.com/morzecrew/morzer/releases/tag/<tag>`, never taken from the feed.
```

**Drift count: 0.**

