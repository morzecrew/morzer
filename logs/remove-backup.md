# remove-backup · `installation remove --backup`

On branch `remove-backup` from `c5222d2`. The author ruled on 2026-11-04 to
reopen [RFC 0034](../rfcs/0034-removing-an-installation.md)'s non-goal
narrowly: `--backup` takes the backup `morzer backup` takes, to the configured
targets, as the first step under the removal's lock, and a failed backup stops
the removal before anything is removed. RFC 0034 gains that row in this
branch.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## Plan

- `internal/lifecycle/ops/remove.go`: `RemoveOptions.Backup`; the backup's
  create, verify and push steps, `morzer backup`'s own, ahead of the
  removal's; the refusals below, before the lock.
- `internal/cli/installation.go`: the `--backup` flag, attaching the backup
  engine as `morzer backup` does.
- RFC 0034's row and dated notes; the command reference; the changelog.

Found while planning, and decided before any code.

`--purge` removes the local backups. With no backup target, the backup
`--backup` takes would be removed by the same command.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-04T20:18:00Z
attempt: 1
claim: with --purge and no backup target, the only copy of the backup --backup takes is in the directory --purge removes
evidence: internal/lifecycle/ops/remove.go:254-258
action: decided
proposal: ASSUMED — `--backup --purge` on an installation with no backup target is refused before the lock, naming the two ways out: add a target, or drop `--purge` and keep the local backup.
```

A backup is of a release that is running. A removal can be of a machine with
no installation record, or whose current release does not resolve.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-04T20:18:00Z
attempt: 1
claim: `morzer backup` needs an installation record and a current release, and a removal runs without either
evidence: internal/lifecycle/ops/backup.go:60-79
action: decided
proposal: ASSUMED — `--backup` is refused before the lock when there is no installation record or the current release does not resolve, saying that nothing can be backed up and that the removal runs without the flag. The backup records `pre-remove` as its reason, verifies as `morzer backup` does, and prunes nothing.
```

From #267's review.

A removal resumed after its backup steps finished re-ran `verify-backup`,
which the engine repeats as idempotent, with no reference to the backup it
was to verify. After `remove-releases`, the `--backup` refusals and the
backup engine both needed a release that was gone.

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-04T20:36:00Z
attempt: 1
claim: the journal carries no backup reference across processes, and a resumed removal past its backup no longer has the release a backup needs
evidence: internal/lifecycle/engine/engine.go:153-161
action: decided
proposal: ASSUMED — when the record a removal resumes shows its backup steps finished, those steps report already satisfied rather than run, and neither the refusals that need a release nor the backup engine are required.
```

`--purge` removes more than the local backups: the machine key goes, and
with it the only way to read a backup encrypted to nothing else. A `file://`
target inside the installation's own directories goes too.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-04T20:36:00Z
attempt: 2
claim: "a backup target" is not enough for --purge to leave a usable copy: the target may be inside what --purge deletes, and the backup may be readable only with the machine key it deletes
evidence: internal/lifecycle/ops/remove.go:262-268
action: decided
proposal: ASSUMED — as attempt 1, and `--backup --purge` is also refused when a `file://` target resolves inside the installation's directories, or when the secret state names no recipient but the machine's own, since the backup is encrypted to exactly those keys.
```

The removal's steps are built from the installation read before the lock. A
target added by a `backup target add` the removal waited behind would not be
pushed to.

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-04T20:36:00Z
attempt: 1
claim: with --wait, the targets a removal pushes to can change between planning and the lock
evidence: internal/lifecycle/ops/remove.go:152-163
action: decided
proposal: ASSUMED — under the lock, a removal with `--backup` re-reads the installation and refuses when its backup targets changed, so it pushes to the targets it planned for or not at all.
```
