# Local backup leaves targets · `backup --no-push` prunes no target

On branch `local-backup-leaves-targets` from `620c3e9`. Left open in
`logs/retention-and-update-delay.md`: `backupSteps` added
`prune-remote-backups` whenever `--no-prune-remote` was absent, pushed or not.
A local-only backup therefore applied retention on every target, which received
nothing new. With `retention.backups: 1`, a target holding three backups kept
one after a `--no-push` run.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

RFC 0001 carries no grades, so its rows are treated as `LOCKED`. Decision 22
says retention runs only once a newer backup exists, and applies it to the
update. It says nothing of `backup`, so this is a gap rather than a
contradiction, and it halted for a ruling.

```divergence
decision: RFC 0001 D-22
grade: LOCKED
class: spec-gap
at: 2026-10-18T22:09:37Z
attempt: 1
claim: decision 22 applies "retention runs once a newer backup exists" to the update only; backup --no-push still pruned every target, which received no newer backup
evidence: internal/lifecycle/ops/backup.go:312
action: halted
proposal: LOCKED — refines D-22: backup applies retention on a target only when this run's backup reached it, so --no-push prunes no target
```

## Ruled 2026-10-19

The author delegated the ruling: "decide whatever is best for a mature
package". Decided as proposed. Retention removes old backups to make room for a
newer one, and a target a local-only backup never touched has none. Pruning it
anyway shrinks the off-machine history for nothing, which is the copy that
matters when the machine is gone. Recorded as RFC 0001 decision 23.

## As built

- `stepPruneRemoteBackups` prunes the targets in `pushed-backups`, which
  both push steps record. The `which` parameter and the update's
  `reachedTargets` helper are gone: the two callers now share one rule.
- `backupSteps` adds the step only when the run pushes, so `--dry-run` no
  longer lists a pass that would do nothing.
- `backup`'s push fails the whole backup when any target fails, so a backup
  that reaches the prune step reached every target. The update's push is best
  effort, and its prune already skipped the targets it missed.

`TestALocalBackupLeavesTheTargetsAlone` was red before the fix: the target
kept 1 of its 3 backups.
