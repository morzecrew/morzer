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

## Build

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-11T20:02:59Z
attempt: 1
claim: #194 left a test asserting "a parameter with no default is the operator's to set after the update" (TestAnUpdateThatSwapsAParameterLeavesConfigUsable), the stance the early refusal reverses: the value is now given with the update. Five more tests reached a state with a declared parameter that has no value by updating to a release that adds one, which the refusal now stops. The swap test now gives the value with the update and changes it with `config set` after. The others give a value to the update; the two text tests expect the change from one line and the value kept, and the stale-value test still writes the state #190 describes directly, since only a failure reaches it now.
evidence: test/suite/update_test.go:385
action: decided
proposal: UNLISTED — a parameter a new release adds with no default is given its value with the update, not after it; the state with no value is reached only by a failure, as #190 describes
```

## Self-audit, 2026-11-11T20:11:28Z

Scope: the whole branch, 6 commits counting this one: the plan, the feature,
the fix, a refactor, two test commits and a resume fix.

- **Sabotage.** Sixteen breaks, each run against the test it targets. Fifteen
  were caught. Four survived the first sweep:
  - the type check, the converge delivering the value, and the resume check
    each needed a sharper test, and got one;
  - the explicit undeclared-name check was redundant, since
    ResolveParameters refuses the same name with the same message, so it was
    removed.

  A resume of a run that recorded its values and stopped later was found
  refused: it planned one step fewer than the run journalled. Fixed, and two
  more breaks now catch it.
- **Behaviour a test had pinned.** One test asserted the stance the early
  refusal reverses (entry above). Five others reached their starting state
  through an update that is now refused, and were moved, each keeping what it
  tests.
- **Not exercised.** The acceptance rig and the docker suites update to no
  release with a parameter that lacks a default, so the change does not reach
  them. A resume given `--set` for a run that had none is refused as planning
  a different operation, which this branch does not change.

**Drift count: 0.**

## Review findings, 2026-11-12T02:42:45Z

Defects the self-audit missed, each reproduced red before its fix:

- **A failed update could not roll back** (d28976da). The restore point was
  given the merged installation, so the previous release resolved a name only
  the new one declares and refused: requires-manual-intervention instead of
  compensated. The rule: a restore point gets the state as recorded, never
  what the operation merged in memory.
- **Given values were recorded raw** (610a54ed): a text value was rendered
  normalised and recorded as typed. `init` records its `--set` the same way,
  which this branch leaves alone.
- **The unattended reason named an update that refuses** for want of a
  release, and a recorded value the new release refuses passed the
  assessment, so the tick's update was refused on every tick (d8f94031).
- **A resumed record lost the names** given to the run it continued; carrying
  them needed a skipped `record-parameters` to read as done (3a5a5b00).
- **"Recorded only if the update succeeds"** overstated it: an abort in
  retire-parameters leaves them recorded on a converged deployment (5d772ecc).

Sabotage of these fixes: 8 breaks, 7 caught. The survivor drops the
"given no values" condition on carrying the names, which differs only for a
resume that gives different names after its run recorded its own.

## Follow-up, 2026-11-12

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-12T03:19:30Z
attempt: 2
claim: The first ruling had a resume whose run had not recorded its values ask for `--set` again, and #347 checked only that the same names came back. Review asked whether a different value should pass; the author ruled 2026-11-12 that it should not: a resume continues one operation, as `--allow-older` and `--generate-secrets=false` carry into it, and a changed value is a `config set` after it. The journal keeps one SHA-256 over the given values as parsed, `set_digest`, beside their names, and a resume with other names or values is refused naming the parameters. It fingerprints rather than quotes: support bundles already carry the values, and the digest is there to compare, not to hide.
evidence: internal/lifecycle/ops/update.go:441
action: decided
proposal: UNLISTED — a resumed update whose run had not recorded its values is given the same values again, checked against a digest the journal keeps; a different value is refused, and changed with `config set` after the update
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-12T03:22:10Z
attempt: 3
claim: Refines the entry above, on the author's word the same morning: one digest per parameter rather than one over the set, so a refused resume names the values that differ and the ones the interrupted run was not given. `set_digest` holds `name=<16 hex digits>` per parameter, a SHA-256 over the name and the parsed value, so one value given to two parameters does not read as the same. A low-entropy value can be guessed from its digest; support bundles already carry every value in parameters.json, and notifications carry no flags.
evidence: internal/lifecycle/ops/update.go:447
action: decided
proposal: UNLISTED — a resumed update is given the values its interrupted run was, each checked against a digest the journal keeps per parameter; a resume naming a value that differs, or one the run was not given, is refused naming them
```

## Follow-up self-audit, 2026-11-12T03:37:44Z

Scope: branch update-set-followups, four commits: the resume held to its
values, `--set-file` duplicates before standard input, the lock re-check of
parameters and domains, and a test commit.

- **Sabotage.** Twelve breaks, eleven caught. The survivor ignores whether a
  parameter is recorded at all, comparing values only, under the lock. It
  misses an absent value replaced by an explicit empty one, which renders
  differently for a string with a default; the fixture declares none, and an
  empty port or enum is refused by the converge anyway.
- **Behaviour a test had pinned.** Two tests (rollback_test.go,
  `...RetiresAValueSetBeforeItsLock`) had a `config set` of a name the target
  release does not declare land before the lock and expect the operation to
  go ahead. That is stepRetireParameters' stance, so the re-check compares
  only the names the converged release declares.
- **Not exercised.** A resume journalled by a build with `set` and no
  `set_digest` is refused, since no released manager wrote one: #347 is
  unreleased.

**Drift count: 0.**
