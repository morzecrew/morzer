---
title: Rolling back
icon: lucide/arrow-down-circle
summary: Returning to the previous release, and the three questions asked before it is allowed
---

# Rolling back

```sh
morzer rollback
morzer rollback --to 1.2.0
```

Rollback is **not update in reverse**. It asks three questions first, reports
them separately, and refuses when the answers do not permit a safe return.

## The three questions

| Question | Answered by | If no |
| --- | --- | --- |
| Are the containers reversible? | `compatibility.rollback_safe` on the *installed* release | Restore required |
| Can the previous release read this schema? | the running schema against the previous release's `database_schema_max` | Restore required |
| Is a restore required? | either of the above | The rollback is refused |

They are reported separately because they fail independently and have different
remedies. Collapsing them into one boolean would hide the difference between
"your migrations are irreversible" and "your schema has moved on".

```text
rollback 1.3.0 → 1.2.0: containers reversible: yes, schema compatible: no, restore required: yes
```

## Why a rollback is usually refused after a real update

This is the case that surprises people, so it is worth being plain about.

A release that migrates your database moves it to a schema the *previous*
release was never written to read. Swapping the containers back would put an old
binary in front of a newer schema — which does not crash. It misreads data,
quietly, for as long as it takes someone to notice.

So after an update that ran a migration, `rollback` says:

```text
error: cannot roll back 1.3.0 to 1.2.0: the database schema is at 14,
       past what 1.2.0 can read
hint:  restore from a backup instead, most recently 20260803T184218Z:
       `morzer restore --backup 20260803T184218Z --force --confirm <installation-id>`
```

That is the tool working. The pre-update backup is the way back, and the refusal
names it.

!!! danger "`--force` does not override this"

    Force authorises destructive actions, not incorrect ones. The failure mode
    here is silent data corruption rather than a visible break, and a warning
    that can be scrolled past is not a safety mechanism when nothing will look
    wrong until it is far too late.

## Reaching further back

Each rollback promotes the release it displaced to *previous*. So a second
`rollback` returns to where the first one started:

```text
1.3.0 → rollback → 1.2.0 → rollback → 1.3.0 → …
```

That is not a bug, it is what "previous" means. To reach a release two steps
back, name it:

```sh
morzer release list
morzer rollback --to 1.1.0
```

`--to` refuses a version newer than what is installed — that is an update, and
they differ in what they check — and refuses the version already running, which
would be a stop and start for nothing.

## When rollback is not the answer

If the rollback is refused, the sequence is a restore:

```sh
morzer backup list
morzer restore --backup <the pre-update one> --force --confirm <installation-id>
```

See [Backups and restore](backups.md). A restore returns the data and re-applies
the *currently installed* release over it, so if you also want the older
software, install it first and restore afterwards.
