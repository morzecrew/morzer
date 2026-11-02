# RFC 0045 — Preflight checks a vendor ships

- **Status:** 📝 Draft — proposed, and ruled by the author 2026-11-02: §11's
  `OPEN` rows as recommended, except that `start` warns as boot does
  (decision 4). P1 is ready to build
- **Scope:** Checks a bundle declares and ships as executables, run by the
  manager beside its own preflight checks: before `apply`, `start`, `update`
  and `rollback` change anything, and in `doctor`. A failing check the vendor
  marked fatal refuses `apply` and `update`, as a failing built-in check does;
  any other failure is a warning. This makes the hook ABI's reserved `preflight`
  phase live. It adds one manifest field and one `OPERATION_TYPE` value
  (`doctor`), and changes no installation schema. It does not make
  `pre-update` or `post-update` live, which stay reserved. It adds no
  declarative check language (kernel parameters, URLs, file contents) for the
  manager to evaluate, and runs no check in a container. It gives a licence
  check no special standing: one is a vendor check like any other.
- **Related:** [`internal/lifecycle/preflight/preflight.go`](../internal/lifecycle/preflight/preflight.go)
  (`Check`, `Runner`, `Report`);
  [`internal/lifecycle/ops/apply.go`](../internal/lifecycle/ops/apply.go)
  (`stepPreflight`, `applySteps`);
  [`internal/lifecycle/ops/doctor.go`](../internal/lifecycle/ops/doctor.go)
  (`doctorChecks`, `checkHealth`);
  [`internal/ports/hooks.go`](../internal/ports/hooks.go) (the ABI);
  [`internal/domain/manifest.go`](../internal/domain/manifest.go);
  [`internal/release/load.go`](../internal/release/load.go);
  [`pages/docs/reference/hooks.md`](../pages/docs/reference/hooks.md);
  [0016](0016-update-checking-and-unattended-updates.md) decision 28,
  [0018](0018-the-pre-1-0-manifest-surface.md) decisions 1 and 9,
  [0024](0024-the-support-bundle.md) §5,
  [0035](0035-tasks-an-operator-runs-on-demand.md), [0040](0040-keeping-an-installation-down.md).

---

## 1. Summary

A manifest gains `preflight:`, a map of named checks. Each names an executable
in the bundle, a description, whether a failure is fatal, a timeout and an
optional remedy. The manager runs them in the preflight step every
release-converging operation already has, after its own checks and in the same
report, so an operator sees every blocker at once. `doctor` runs the installed
release's checks too. Each check runs under the hook ABI with `PHASE`
`preflight`. Exit 0 passes, exit 2 means the check does not apply here, and
anything else fails. A failure is a warning unless the vendor declared the
check fatal. A fatal one refuses `apply` and `update`, and is only reported at
boot, in `start` and in `rollback`, so a product that already ran is never kept
down by one.

## 2. Motivation

A vendor knows things about their product that the manager cannot.
Elasticsearch will not start with `vm.max_map_count` below 262144. A product
with an external database needs its host to resolve. A GPU build needs a driver.
Today the manager checks only what `requirements` can state: architecture, OS,
tools, disk, memory, CPUs, ports and parameters. Anything else is found by the
product at its own start, which in `apply` is after images are pulled and
migrations have run, as a failed health wait. On an unattended update it is
found at night, after the pre-update backup.

The ABI has promised the phase since the first release. `preflight` is one of
the three phases `PHASE` may name and that no manager runs
([`hooks.md:103`](../pages/docs/reference/hooks.md),
[`ports/hooks.go:30`](../internal/ports/hooks.go)). A hook may branch on it
today and nothing reaches the branch.

[RFC 0024 §5](0024-the-support-bundle.md) named this shape when it ruled
licence gating out of the manifest: if a vendor shows a check must happen before
`apply`, "the honest shape is a *manifest-declared preflight the vendor
writes*, not a licence subsystem". Licensing is planned as its own RFC. This
one is the general mechanism, and decision 4 is what keeps it from
becoming the failure mode 0024 described.

Replicated's preflight checks are the market's version of this. Their
collectors and analyzers are a declarative language the vendor writes and the
installer evaluates. §5.6 says why this RFC runs the vendor's executable
instead.

## 3. Current state

Verified against `main` at 4d26d8d.

- **The preflight step.** `stepPreflight` ([`apply.go:266`](../internal/lifecycle/ops/apply.go))
  is the first of `applySteps` and runs before secrets are loaded. It builds a
  `[]preflight.Check` from the manifest's `requirements` and runs them through
  `preflight.NewRunner(d.Bus)`, with a three-minute step timeout (line 272).
  Every check runs even after one fails, and `Report.Err`
  ([`preflight.go:54`](../internal/lifecycle/preflight/preflight.go)) lists
  every failure in one error, exit 3. A check whose `Fatal` is false has its
  failure turned into a warning (line 103).
- **Who runs that step.** `apply`, and so a first install, boot
  (`apply --startup`) and `start` ([`stop.go:183`](../internal/lifecycle/ops/stop.go)).
  `update` runs it inside the reused `applySteps`
  ([`update.go:467`](../internal/lifecycle/ops/update.go)), after the
  pre-update backup (line 431), staging and image ingest. `rollback` runs it
  before the release pointer moves
  ([`rollback.go:393`](../internal/lifecycle/ops/rollback.go)). `restore` does
  not run it; it re-applies through its own step
  ([`backup.go:769`](../internal/lifecycle/ops/backup.go)). Nor does
  `config set`, whose steps record and render
  ([`config.go:343`](../internal/lifecycle/ops/config.go)). A plan
  (`--dry-run`) runs no steps, so no check runs in one.
- **`doctor`** builds its own list (`doctorChecks`,
  [`doctor.go:153`](../internal/lifecycle/ops/doctor.go)), lock-free, and
  exits 3 on any failure. It already runs vendor executables: a `command`
  health check runs from `runtime.health`, with `OPERATION_TYPE` `apply`
  (line 1350), although no apply is running. Once a notification target is
  configured, the scheduled `doctor` runs every check each morning and notifies
  what warns or fails (RFC 0043).
- **The manifest** refuses an `operations` key outside `migrate`, `smoke_test`,
  `backup` and `restore` ([`manifest.go:637`](../internal/domain/manifest.go)).
  It is decoded strictly, so a new top-level field is fatal to an older
  manager. `tasks` met that with `TasksMinManagerVersion` (line 741): a bundle
  declaring tasks must declare that manager version, so an older manager says
  "upgrade" instead of "unknown field". The support bundle's recipients went
  under `extensions."morzer.dev/support"` instead, which an older manager
  tolerates and ignores (RFC 0018 decision 9).
- **Hooks are validated at load.** `release/load.go` checks that every `hook`
  operation and `command` health check names a file in the bundle with the
  executable bit (lines 525–543), so a broken one is refused before the lock.
  The same function would cover a new field.
- **The example bundle** ships `hooks/check-db`, a `command` health check, as
  the nearest thing to a vendor check: it runs after the services start, not
  before.

## 4. Goals / Non-goals

**Goals**

- A vendor can stop `apply` and `update` on a machine their product
  cannot run on, before anything changes, with their own message and remedy.
- An operator sees the vendor's checks in `doctor`, and so in the support
  bundle and the scheduled `doctor`'s notification, grouped apart from the
  manager's.
- A check is visible before it runs: `release show` lists it and `release diff`
  compares it, fatal flag included.
- The ABI changes by additions only.

**Non-goals**

- **A check language.** The manager evaluating `sysctl ≥ N` or `url reachable`
  is a second product; the vendor already has a shell. See §5.6.
- **Checks in a container.** The step runs before images are pulled and on a
  first install before the project exists. A question that needs the product's
  network is a smoke test's.
- **`pre-update` and `post-update`.** They stay reserved. Each needs its own
  answer about what a failure compensates, which a check that changes nothing
  does not.
- **Licensing.** A licence check written as a vendor check gets exactly what
  decision 4 gives any check, and no more.

## 5. Design

### 5.1 The declaration

```yaml
preflight:
  max-map-count:
    description: the kernel allows enough memory maps for the search index
    command: ["hooks/preflight", "max-map-count"]
    fatal: true
    timeout: 30s
    remedy: run `sysctl -w vm.max_map_count=262144` and persist it in /etc/sysctl.d
  db-host:
    description: the external database host resolves
    command: ["hooks/preflight", "db-host"]
```

- The name follows `TaskNamePattern`. The check's ID is `vendor.<name>` and its
  category `vendor`, so `doctor` groups the vendor's checks apart from its own.
- `description` is required: it is the line `doctor` prints.
- `command` names an executable in the bundle, resolved as every hook is.
  `release verify` and load refuse a missing file or one without the
  executable bit, as for operations.
- `fatal` defaults to false. A check the vendor did not mark fatal can only
  warn (decision 9).
- `timeout` defaults to 30s.
- `remedy` is optional and static, and is shown with a warning or failure.
- Checks run in name order, after the built-in checks.

A bundle declaring `preflight:` must declare
`compatibility.min_manager_version` at least the release that ships P1, which
validation enforces as it does for tasks (decision 13).

### 5.2 Running a check

A check runs through `ports.HookRunner`, so the process group, the timeout and
the 1 MiB bound on fd 3 are the ones every hook has.

| Outcome | Result |
| --- | --- |
| Exit 0 | Passes. The message is fd 3's `message`, or `passed`. |
| Exit 2 | Passes as "not applicable here", with fd 3's `message` if any. The ABI's "nothing to do", read for a check. |
| Any other exit | Fails. The message is what a failing hook is quoted with: fd 3's `message`, else the last lines of stderr, then stdout. |
| Timeout | Fails: "did not finish within 30s". |
| fd 3 unreadable | Fails, as for any hook. |

A failure becomes a warning when the check is not fatal, or when the operation
does not refuse on vendor checks (decision 4). The preflight step's timeout
grows by the sum of the declared timeouts, so a vendor's checks never share the
manager's three minutes.

### 5.3 What a check is told

The hook ABI's environment, with these differences:

- `PHASE` is `preflight`.
- `OPERATION_TYPE` is the operation running the step (`apply`, `start`,
  `update`, `rollback`), or `doctor`. That last value is new to the ABI. A check
  may want to skip a slow probe on the scheduled run.
- `RELEASE_VERSION` and `RELEASE_DIR` are the release being checked: the new
  one during `update`, the previous one during `rollback`, the installed one in
  `doctor`. `PREVIOUS_VERSION` is set as for any hook.
- Parameters are those resolved for the release being checked.
- `SECRETS_DIR`, `CONFIG_FILE` and `BACKUP_DIR` are absent. In an operation the
  step runs before anything is rendered, so those paths would name the previous
  release's files or none. Secrets are decision 6.
- `DRY_RUN` is `0`: no check runs in a plan.

**A check must not change the machine** (decision 16). `doctor` runs it
without the deployment lock, possibly while an operation is running, and
possibly every morning on a machine nobody is watching.

### 5.4 Where it refuses

| Operation | A fatal check that fails |
| --- | --- |
| `apply`, a first install | Refuses, exit 3, listed with any built-in blockers. Nothing has changed. |
| `update`, interactive or unattended | Refuses, exit 3. The current release keeps running. As with a built-in check, this is after the pre-update backup. Unattended, it is a failed install, so the candidate is marked (RFC 0016 decision 28). |
| `apply --startup` (boot) | Warns. The release already ran on this machine. |
| `start` | Warns, as boot does. `update` and `rollback` are refused while the product is stopped (RFC 0040 decision 3), so a refused `start` would leave no way back. |
| `rollback` | Warns. Refusing would withhold the way back. |
| `doctor` | Fails, exit 3, as a built-in failure does. |

This is decision 4. The asymmetry is the point. Moving forward onto a
machine the vendor says is wrong is refused. Restoring what already ran is not,
because a check the vendor wrote, failing closed, must not be what keeps a
paying customer's product down (RFC 0024 §5).

### 5.5 Seen before it runs

- `release show` lists each check with its description and fatal flag.
- `release diff` gains a `preflight` section, compared by name like `tasks`.
  A check that became fatal is a change.
- The schema generator picks the field up. `schemas/selfhost-v1alpha1-manifest.json`
  gains `preflight` additively.

### 5.6 Alternatives considered

- **One hook reporting a list on fd 3.** `operations.preflight` runs once and
  writes `{"checks": [{"id", "status", "message"}]}`. That is one process and
  one file for the vendor, and closer to Replicated's single spec. It lost
  because the severity is decided at runtime, inside the script. `release show`
  and `release diff` could not say which checks refuse, the result schema grows
  a list the ABI then has to version, and one timeout covers every check. It is
  decision 1.
- **A declarative language** (sysctl, file, URL, command-output analyzers).
  The manager would own every question a vendor might ask, and each new one
  would be a manager release. Whatever it could not express would come back as
  a script anyway. It reopens if vendors keep writing the same three scripts.
- **`extensions."morzer.dev/preflight"`.** An older manager ignores it and
  installs without running the checks. That is decision 2. It is the
  right shape only if a check is advice. A fatal check skipped by an older
  manager is an install onto the machine the vendor said to refuse.
- **Vendor checks ahead of the pre-update backup.** A refused update would cost
  no backup. But the blocker list would split in two, the vendor's checks would
  run before the images they might inspect are ingested, and the same argument
  applies to the built-in checks. Reordering update's preflight is its own
  change, for both.
- **A `runtime-service` kind.** On a first install there is no image and no
  network yet, and on an update the new release's images arrive after staging.
  A check in a container would run against the old release's.

## 6. Tests

- **Manifest:** the name pattern, a missing description, a missing or
  non-executable command and the `min_manager_version` floor each refuse; one
  row each.
- **The runner:** exit 0, 2, 1, a timeout and an unreadable fd 3 each map to the
  row in §5.2. Fatal and non-fatal, and the demotion at boot and in rollback,
  are a table over operation × fatal.
- **Environment:** a check script records its environment. `PHASE` is
  `preflight`, `OPERATION_TYPE` is `doctor` in doctor, and `SECRETS_DIR`,
  `CONFIG_FILE` and `BACKUP_DIR` are absent.
- **CLI (`test/clitest`):** `apply` refused by a fatal failing check exits 3
  and lists it beside a built-in blocker. A non-fatal one warns and the apply
  succeeds. `doctor` shows the `vendor` group. `update` refused by the new
  release's check leaves the current release running. Boot and `start` with a
  failing fatal check start the product and warn.
- **Views:** `release show` and `release diff` fixtures with a check, goldens at
  every width.
- **Acceptance:** the example bundle declares one non-fatal check, so the real
  Docker run exercises the step.

## 7. Docs

- `reference/hooks.md`: `preflight` leaves the reserved list, and the Phases
  section says what a check is told and what its exits mean. `OPERATION_TYPE`
  gains `doctor`.
- `reference/manifest.md`: a `preflight` section beside `tasks` and `health`,
  stating the read-only contract and the `min_manager_version` floor.
- `operating/` (doctor): the `vendor` group, and that its remedies are the
  vendor's words.
- `reference/release-commands.md`: `show` and `diff` list the checks.
- Changelog: Added.

## 8. Out of scope

- **`pre-update` and `post-update`.** They stay reserved. They reopen with a
  vendor who needs to run something around an update that a migration step
  cannot do.
- **Running checks in a plan.** `apply --dry-run` runs no step, built-in
  checks included. A plan that reports what preflight would say is a change to
  every plan, not to vendor checks.
- **Moving update's preflight before the backup** (§5.6).
- **A check language** (§5.6).

## 9. Risks

- **A buggy fatal check blocks the vendor's own customers.** It refuses every
  `apply` of that release. Update is not blocked: the new
  release's checks run, so the vendor's fix installs. Boot, `start` and
  rollback never refuse. Whether an operator also gets an override is decision 7.
- **A slow check slows every operation and every scheduled `doctor`.** It is
  bounded by its timeout, 30s by default. The step's timeout grows by the
  declared total and is never shared.
- **A check that changes the machine.** The contract forbids it and nothing can
  enforce it. A command health check carries the same risk today, from `doctor`
  as well.
- **Read as a licence mechanism.** A vendor will write one. Decision 4 bounds
  what one can do: refuse a forward move, never keep a running product down.
  The Phase 4 licensing RFC decides anything beyond that, and decision 17 says
  this RFC grants nothing more.

## 10. Unresolved questions

None. The author ruled §11's `OPEN` rows 2026-11-02.

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `LOCKED` | **The shape.** Recommended: a `preflight:` map of named checks, each its own command, with `fatal`, `timeout`, `description` and `remedy` declared in the manifest. Severity is then visible in `release show` and `release diff`, each check has its own timeout, and the ABI's result schema is unchanged. Alternative: one `operations.preflight` hook reporting a list of results on fd 3, one process for the vendor, severity decided at runtime. Was `OPEN`; ruled by the author 2026-11-02: as recommended. Locks the manifest shape: changing it later breaks every bundle that declares checks. |
| 2 | `LOCKED` | **Where it lives.** Recommended: top level, with `compatibility.min_manager_version` required at the release that ships it, as tasks did. An older manager then refuses the bundle and says to upgrade. Alternative: `extensions."morzer.dev/preflight"`, which an older manager ignores, so the release installs there without its checks. A vendor's fatal check would silently not run. Was `OPEN`; ruled by the author 2026-11-02: as recommended. Locks the placement for the same reason. |
| 3 | `ASSUMED` | A check is a host executable from the bundle, run under the hook ABI. There is no `runtime-service` kind: the step runs before images are pulled and, on a first install, before the project exists. |
| 4 | `LOCKED` | **Where a fatal check refuses.** Recommended: `apply`, a first install, `start` and `update` (unattended included) refuse. Boot and `rollback` warn, because they restore what already ran, and `doctor` fails as for a built-in check. Alternative: every operation refuses, boot and rollback included, which keeps one rule and can keep a running product down over a vendor's check. Was `OPEN`; ruled by the author 2026-11-02: as recommended, except that `start` warns as boot does. The review found that a refused `start` has no way around it: `update` and `rollback` are refused while stopped (RFC 0040 decision 3), boot leaves the product down, and decision 7 gives no override. |
| 5 | `ASSUMED` | **`doctor`.** Recommended: `doctor` runs the installed release's checks, the scheduled run included, so a check failing after a kernel or network change is reported before the next operation needs it, and lands in the support bundle. Alternative: checks run only in operations, which never runs vendor code on a schedule. Was `OPEN`; ruled by the author 2026-11-02: as recommended. |
| 6 | `ASSUMED` | **Secrets.** Recommended: none in P1. The step runs before secrets load, and `doctor` would have to render them to tmpfs on every scheduled run. A check that needs a credential is a smoke test's. Alternative: a check may declare `secrets: true` and runs after `load-secrets` with `SECRETS_DIR` set, while `doctor` skips it. Was `OPEN`; ruled by the author 2026-11-02: as recommended. |
| 7 | `ASSUMED` | **An override.** Recommended: none in P1. The built-in checks have none, the new release's checks govern an update, and boot and rollback never refuse. Alternative: `--skip-check vendor.NAME`, repeatable, journalled with the names, and refused with `--unattended`. Was `OPEN`; ruled by the author 2026-11-02: as recommended. |
| 8 | `ASSUMED` | Exit 0 passes; exit 2 passes as not applicable; anything else, a timeout or an unreadable fd 3 fails. The message is fd 3's `message`, or the quoted output a failing hook gets. The manifest's `remedy` accompanies a warning or failure. |
| 9 | `ASSUMED` | `fatal` defaults to false, so a failure warns unless the vendor opted into refusal. This is `preflight.Check.Fatal` as the built-in checks already use it. |
| 10 | `ASSUMED` | The environment is the hook ABI's with `PHASE` `preflight` and `OPERATION_TYPE` the running operation, or `doctor`, a new value. `SECRETS_DIR`, `CONFIG_FILE` and `BACKUP_DIR` are absent. `DRY_RUN` is `0`, and the release and parameters are those being checked. `doctor`'s command health check keeps saying `apply`; changing that is not this RFC's. |
| 11 | `ASSUMED` | Checks run in the existing preflight step, after the built-in checks and in name order, so one report lists every blocker. The step's timeout grows by their declared total. In `update` that step is after the pre-update backup, as it is for built-in checks today. |
| 12 | `ASSUMED` | The ID is `vendor.<name>`, in a new `vendor` category. The name follows `TaskNamePattern`, `description` is required, `remedy` is optional and static, and `timeout` defaults to 30s. |
| 13 | `ASSUMED` | Load and `release verify` refuse a check whose command is missing or not executable, and a bundle declaring checks below the manager version that runs them. |
| 14 | `ASSUMED` | `release show` lists the checks and `release diff` compares them by name; a change to `fatal`, `command` or `description` is a change. |
| 15 | `ASSUMED` | `pre-update` and `post-update` stay reserved; only `preflight` becomes live. |
| 16 | `LOCKED` | A preflight check must not change the machine. It is a public ABI promise, and `doctor` runs checks lock-free, possibly during an operation and on a schedule. Allowing a check to mutate later would mean taking the lock in `doctor` and running checks inside every operation's compensation story. |
| 17 | `ASSUMED` | No check has special standing. A licence check is a vendor check, and gets what decision 4 gives any check. RFC 0024 §5's ruling against licence gating in the manager stands, and anything beyond this is the Phase 4 licensing RFC's. |

## 12. Phasing

**P1** is the whole design, one PR: the field, validation, the step, `doctor`,
`show` and `diff`, the docs and the example bundle's check. It is ready to
build. Nothing is demand-gated.
