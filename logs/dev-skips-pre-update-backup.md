# Dev skips the pre-update backup · `backup.before_update`

On branch `dev-skips-pre-update-backup` from `620c3e9`. The other half of the
ERP bundle's M3: "an installation setting to skip them, allowed on `--mode
dev` only". `policy.skip_backup_before_update` is honoured by `update`
(`internal/lifecycle/ops/update.go:655`) and by the auto-apply gate, but no
command sets it, and `installation.yaml` is a report. A sandbox whose timer
follows a channel every ten minutes therefore takes a full backup, Postgres and
Valkey included, before every update. Retention (RFC 0001 decisions 21 and 22)
now bounds how many it keeps, not how often it takes one.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

RFC 0016 carries no grades, so its rows are treated as `LOCKED`. Decision 11
says dev mode relaxes backups, and §5 lists "`skip_backup_before_update`
becomes permissible without `--force`". Neither says how a sandbox sets the
policy, so this halted for a ruling.

```divergence
decision: RFC 0016 D-11
grade: LOCKED
class: spec-gap
at: 2026-10-18T22:12:17Z
attempt: 1
claim: decision 11 relaxes backups on a sandbox and §5 makes skip_backup_before_update permissible there, but nothing sets the policy, so a sandbox cannot stop its timer's pre-update backups
evidence: internal/lifecycle/ops/settings.go:193
action: halted
proposal: LOCKED — refines D-11: the setting backup.before_update turns the pre-update backup off for every update; only a dev-mode installation may set it to false, and production refuses, naming update --skip-backup --force for one update
```

## Ruled 2026-10-19

The author delegated the ruling: "decide whatever is best for a mature
package". Decided as proposed, dev-only as the request asked.

- **Why not production with `--force`.** §5's wording could be read that way.
  `config set` takes no `--force`, and a durable unsafe default is a
  different thing from a per-run one. `update --skip-backup --force` already
  covers the production case, one update at a time, recorded in the journal.
  Decision 7 refuses an unsafe durable setting at configuration time, and this
  follows it.
- **Why a setting rather than nothing.** Retention bounds the disk, not the time
  and I/O of a full backup every ten minutes, which is the cost the request
  named.
- **A production installation that already has the policy**, from a hand edit,
  keeps it. Refusing it at load would stop that machine's updates, and the
  setting's refusal covers every door the manager owns. `config set
  backup.before_update=true` and `config unset` both turn it back on, on any
  machine.

Recorded as RFC 0016 decision 32.

`TestOnlyASandboxTurnsOffThePreUpdateBackup` was red before the setting
existed. The policy's effect on `update` was already covered by
`TestTheInstallationCanTurnOffThePreUpdateBackup`.
