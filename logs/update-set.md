# update-set · #346, values for the release an update installs

On branch `update-set` from `main` at `e2727d1a` (v0.16.0), fixing
[#346](https://github.com/morzecrew/morzer/issues/346): an update has no way
to give a value to a parameter the new release adds without a default.
[RFC 0007](../rfcs/0007-operator-parameters.md) left `--set` on `update` open
(§12 question 1). The author ruled 2026-11-11, in the session that took the
issue: option 1, `update --set` and `--set-file`, with an update that refuses
before any downtime when a value is missing, and a `--resume` that asks for
`--set` again rather than keeping values anywhere.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## Plan

### Operations

- `internal/lifecycle/ops/update.go`:
  - `UpdateOptions.Parameters`, the `--set` and `--set-file` values, by name.
  - Before any step, once the new release's manifest is loaded:
    - a name the new release does not declare is refused (RFC 0007
      decision 1);
    - each value is parsed with its type, a refusal naming only the
      parameter (RFC 0054 row 16);
    - the values are merged over the recorded ones in memory, beside the
      retired names already dropped there;
    - `MissingValues` over the merged map refuses the update, with the hint
      `--set <name>=<value>`. This is before the backup, the restore point
      and the stop, so the running release keeps serving and nothing is
      undone.
  - A `record-parameters` step, last with `retire-parameters` and for the
    same reason: by then the deployment is converged, so a stop before it
    leaves nothing to undo. It writes the values into the installation as
    recorded when it runs. Abort on failure, as `retire-parameters` does, and
    a plan detail naming the parameters.
  - The journal's flags name the parameters (`set: a,b`), never a value.
  - `--resume` with an interrupted run whose flags name parameters that are
    not recorded and are not given again is refused, naming them.
- `internal/lifecycle/ops/channel.go`: `assessUnattended` adds the missing
  values as a reason, so the tick leaves the release staged and names what
  `update --set` must give.

### CLI

- `internal/cli/commands.go`: `update --set` and `--set-file`, parsed as
  `init` parses them (`ParseAssignments`, `readParameterFiles`,
  `mergeParameterFiles`). Refused with `--check` and `--stage`, which install
  nothing a value could be recorded with.

### Docs and changelog

- `reference/commands.md`: the two flags on `update`, and the refusal.
- `reference/manifest.md`, `### required`: the example becomes
  `morzer update ./demo-1.4.0.tar.zst --set admin_email=ops@example`.
- `operating/unattended-updates.md`: the reason a tick gives.
- `CHANGELOG.md`, Unreleased: Added for the flags; Fixed for the refusal
  before downtime and the unattended reason.

### Tests

`test/suite`:
- A release adding a parameter with no default, and no value: refused before
  the backup, no backup taken, the old release current, nothing stopped.
- The same with `--set`: installed, the value recorded, the journal naming
  it and not quoting it.
- `--set` of a name the new release does not declare: refused.
- An update with `--set` that fails its health check: rolled back, and the
  value not recorded.
- Interrupted after the converge and before `record-parameters`: `--resume`
  without `--set` is refused naming it; with it, the value is recorded.
- The unattended tick: staged, not installed, the reason naming the
  parameter.
- A dry run names the parameters in the plan.

`test/clitest`: the flags parse, and `--set` with `--check` or `--stage` is
refused.

### Decisions the plan needs that nobody has settled

None load-bearing. The author's rulings cover the shape. The entries below
record them for RFC 0007, and one gap the build will meet.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-11T19:42:10Z
attempt: 1
claim: RFC 0007 §12 asked whether `--set` belongs on `update` and leaned no, since a per-run override runs a deployment its recorded state does not describe. A value given to an update is recorded by that update, so the state still describes the deployment, and without it a release that adds a parameter with no default cannot be installed at all (#346). Ruled by the author 2026-11-11: `update --set` and `--set-file`, validated against the release being installed, recorded by the update's last step, named in the journal and never quoted, and asked for again by a `--resume` whose run had not recorded them.
evidence: rfcs/0007-operator-parameters.md:491
action: decided
proposal: UNLISTED — `update` takes `--set` and `--set-file` for the release it installs, recorded only by an update that succeeds; a resumed run that had not recorded them needs them given again
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-11T19:42:10Z
attempt: 1
claim: RFC 0007 §12 question 3 says a release that adds a parameter "is fine — it defaults", and one with no default is not: the update converged, failed the render or the health check, and rolled back after the downtime. `MissingValues` was checked by `init` and `config set` only. Ruled by the author 2026-11-11: the update refuses before the backup and the stop, and the unattended tick stages the release and names the parameter.
evidence: internal/lifecycle/ops/config.go:294
action: decided
proposal: UNLISTED — an update whose release declares no default for a parameter with no value is refused before anything stops, and an unattended tick leaves it staged, naming the parameter
```
