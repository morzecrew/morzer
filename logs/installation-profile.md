# installation-profile · `config set installation.profile` (#286)

On branch `installation-profile` from `main` at `a34a2c35`, building
[#286](https://github.com/morzecrew/morzer/issues/286) against
[RFC 0042](../rfcs/0042-settings-an-operator-changes-after-init.md), whose §1
left the profile out on the premise that `update --profile` already overrides
it. That premise is false: the override lasts one run, and `inst.Profile` is
written only by `init` (`internal/lifecycle/ops/init.go:543`, `:626`). The
author reopened it in #286 and ruled two of its questions on 2026-11-07:

- On a running installation the change converges now: record it, keep the old
  topology to bring back on failure, then run apply's steps under the new
  profile.
- An update or rollback whose target does not declare the stored profile is
  refused before any step, whatever `--profile` says.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## Plan

- `internal/lifecycle/ops/settings.go`:
  - an `installation.profile` setting, validated against the installed
    release's profiles for this installation's runtime; empty or `unset` is
    the base files;
  - `journalled` includes a profile change;
  - `planSettings`: on a running installation a profile change is
    `record-settings`, a restore point holding the old installation, then
    `applySteps` under the new one. Under a stop, or with nothing running, it
    is record and render, as a domain change is;
  - `settingsSummary` says which it did.
- `internal/lifecycle/ops/ops.go`: `Options.ForceRecreate`, which
  `stepStartServices` (`apply.go`) passes to `Up`. Only the profile converge
  sets it (entry below).
- `internal/lifecycle/ops/update.go`: `stepRestorePoint` and
  `restorePreviousRuntime` take whether to force the re-create. `update`
  passes false, so its behaviour does not change.
- `internal/lifecycle/ops/update.go`, `rollback.go`: one helper, beside
  `refuseRuntimeOptionChange`, that refuses before any step when the target
  does not declare the stored profile, or the `--profile` given.
- `internal/lifecycle/ops/apply.go`, `update.go`: a `--profile` that differs
  from the stored one warns that it lasts one run, and names
  `config set installation.profile=<name>`. Nothing is persisted.
- `internal/cli/init_wizard.go`: "none, the base files" is the first option
  and the default.
- `internal/cli/config.go`: the help names the second setting that reaches
  the deployment.
- Tests, in `test/suite/settings_after_init_test.go` and the update and
  rollback suites:
  - after the set, a plain `apply`, `apply --startup` and an unattended update
    render `.Profile` and include its files;
  - the converge forces the re-create and removes orphans;
  - `unset` returns to the base files;
  - an unknown name is refused with nothing changed;
  - a failed converge brings the old profile and topology back;
  - under a stop it only records and renders;
  - `apply --profile` warns and leaves the stored profile alone;
  - an update or rollback to a release without the profile is refused, with
    or without `--profile`;
  - the wizard offers no profile first.
- `rfcs/0042`: rows 15-19, ruled by the author 2026-11-07 (#286), and a dated
  note on §1's exclusion, as RFC 0034 decision 11 was recorded (#267).
- Docs:
  - `pages/docs/reference/parameters.md`: the setting's row, and "the one
    exception" becomes two;
  - `pages/docs/operating/changing-configuration.md`: the profile row;
  - `pages/docs/reference/commands.md`: `--profile` on `apply` and `update`,
    and the wizard;
  - `pages/docs/reference/manifest.md`: the refusal beside "an unknown profile
    is an error".
- The changelog.

Decisions the plan needs that #286 and the rulings do not settle: two, neither
load-bearing, below.

`stepStartServices` runs a plain `up` (`apply.go`), which re-creates only the
containers whose definition changed. A service that reads `.Profile` through a
rendered file bind-mounted into it, as dotdash's inbound does, would keep
running the old file: the trap `stepRecreateServices` forces its way past
(#239).

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-07T00:45:03Z
attempt: 1
claim: apply's steps under the new profile run a plain `up`, which leaves a container reading `.Profile` through a bind-mounted rendered file on the old contents
evidence: internal/lifecycle/ops/config.go:391-405
action: decided
proposal: ASSUMED — a profile change's converge re-creates every container (`Options.ForceRecreate`), and so does its restore point when a failure brings the old topology back. A plain `apply` and `update` are unchanged.
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-07T00:45:03Z
attempt: 1
claim: #286 validates the name against the current release's profiles, and an installation with no release installed has none to check against
evidence: internal/lifecycle/ops/settings.go:787-797
action: decided
proposal: ASSUMED — with no release installed, a named profile is refused and `unset` is accepted.
```

## Build, 2026-11-07

The author's rulings are RFC 0042 decisions 15–19, appended in this branch
with a dated note on §1's exclusion. The two entries above are this build's
proposals.

## Self-audit, 2026-11-07

Scope: the whole branch against `a34a2c35`, two commits, 16 files, 549 lines
added and 32 removed: code, tests, docs, the RFC rows and the changelog.

- **Fixed before the push:** nothing tested `update --profile`'s warning.
  `TestAOneRunProfileLeavesTheStoredOneAndSaysSo` now runs an update with an
  override and counts the warning.
- **Sabotage:** twelve mutations, each run against the test written for it,
  and each went red:
  - the forced re-create, on the converge and on the restore point;
  - the journal entry;
  - the refusal, whole, for an override only, and in `rollback`;
  - both warnings;
  - the name check;
  - the converge under a stop;
  - the restore point left out;
  - the wizard's first option.
- **Behaviour change outside the setting:** `restorePreviousRuntime` gives the
  health checks the running operation's type rather than `update`. An update
  is unchanged; the profile change's restore runs them as `config`.
- **Residue:**
  - An unattended tick refused by the profile check writes no record and is
    refused again at the next tick, as one refused for its runtime options is.
  - The converge has only run against the fake runtime. Acceptance proves that
    Compose honours `--force-recreate` and `--remove-orphans` (`config set`
    moving a port), not a profile switch on real Docker.
  - Whether `update`'s own restore point should force the re-create is not
    examined here.
