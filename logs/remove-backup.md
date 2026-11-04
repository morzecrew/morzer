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
