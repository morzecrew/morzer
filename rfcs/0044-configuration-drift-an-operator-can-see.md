# RFC 0044 — Configuration drift an operator can see

- **Status:** 📝 Draft — proposed, and ruled by the author 2026-10-31: §11's
  `OPEN` rows as recommended. P1 is ready to build
- **Scope:** Where the files on disk differ from what the installed release
  renders from the recorded parameters, shown on the machine: a command that
  prints the differences, and a `doctor` check that warns about them. P2 adds
  the services whose running definition differs from the declared one. The RFC
  reuses the comparison the support bundle and the fleet row already make; it
  adds no second one. It does not correct drift, which `apply` already does.
  It does not deliver RFC 0027 P3, which compares a desired-state file with the
  installation and stays gated with 0027 P2. No schema bump.
- **Related:** [`internal/lifecycle/ops/support.go`](../internal/lifecycle/ops/support.go)
  (`configComparison`, `collectConfigDiff`);
  [`internal/lifecycle/ops/fleet_row.go`](../internal/lifecycle/ops/fleet_row.go)
  (`fleetDrift`);
  [`internal/lifecycle/ops/apply.go`](../internal/lifecycle/ops/apply.go)
  (`stepRenderConfiguration`);
  [`internal/lifecycle/ops/doctor.go`](../internal/lifecycle/ops/doctor.go);
  [`internal/adapters/runtime/compose/compose.go`](../internal/adapters/runtime/compose/compose.go)
  (`Start`);
  [0016](0016-update-checking-and-unattended-updates.md) (the unattended gate),
  [0040](0040-keeping-an-installation-down.md) decision 9,
  [0024](0024-the-support-bundle.md) (`config-diff.txt`),
  [0026](0026-fleet-as-a-read-model.md) (the drift count),
  [0027](0027-desired-state-in-a-repository.md) §4.5 and P3.
- **Origin:** Drafted 2026-10-31 from the gap report's "no drift view" finding.

---

## 1. Summary

The manager already knows when a configuration file on disk differs from what
the release renders. The support bundle writes the diff to `config-diff.txt`,
and the fleet row publishes how many files differ. Neither is something an
operator on the machine reads before an `apply` replaces the file.

This RFC adds `morzer config diff`, which prints those differences, and a
`doctor` check, `config.drift`, which warns while any exist. Both read the same
`configComparison` the support bundle and the fleet row read. P2 adds the
services an `apply` would re-create because their definition changed since
they started.

## 2. Motivation

**A hand edit is lost without a word.** An operator who edits
`/etc/demo/application.yaml` to try something keeps the change until the next
`apply`, `update` or `config set`. Each renders the configuration, and the
render step replaces every file that differs from the rendered bytes
(`apply.go:460-620`). It keeps the old content only to undo a failed step, so
after a success the edit is gone. An unattended update does the same at night,
with nobody watching.

**The facts exist and are in the wrong places.** To see drift today, an
operator runs `apply --dry-run` and finds the diff among some twenty plan
steps, or builds a support bundle and opens an archive. The fleet row shows a
count, but only where fleet publishing is set up, and it shows no file names.

**Nothing raises it unprompted.** `doctor` checks the installation file
against state (`config.installation-file`), which is a different drift, and
has no check for the rendered files. The scheduled `doctor` RFC 0043 proposes
can only notify what `doctor` checks.

## 3. Current state

- **`configComparison`** (`support.go:900-933`) renders every configuration
  target from the installed release and the recorded parameters, read-only,
  and compares each with the file on disk. It returns two lists: one unified
  diff per target that differs, and one line per target that cannot be read.
  An absent file counts as a difference; an unreadable one does not.
- **The support bundle** prints both lists as `config-diff.txt`
  (`collectConfigDiff`, `support.go:860`). It is safe by an invariant:
  `templateData` puts secret references, never values, in the render context.
- **The fleet row** counts the differing targets and names a problem when it
  cannot compare (`fleetDrift`, `fleet_row.go:163-200`). `morzer fleet ls`
  shows the count in its `DRIFT` column.
- **`apply --dry-run`** shows the same differences in the render step's plan
  detail (`apply.go`, `PlanDetail` of `stepRenderConfiguration`), computed by a
  separate loop over the same render.
- **The render step** writes every target whose bytes differ, from `apply`,
  `update`, `rollback` and `config set` (`config.go:342-345`).
- **The unattended tick** (`channel.go:480-525`) leaves a staged release
  uninstalled, with a reason, when `update.auto_apply` is off or the product
  is stopped (RFC 0040 decision 9). Otherwise `AssessUnattended`
  (`version.go:498`) decides, from compatibility and policy. None of these
  reads the files on disk.
- **Running services.** `compose.Start` uses `compose start`, not `up`, because
  `up` re-creates a container whose definition has drifted
  (`compose.go:443-448`). Nothing reports which services that would be.

## 4. Goals / Non-goals

**Goals**

- An operator on the machine can see which configuration files differ, and
  how, with one read-only command.
- `doctor` warns while any differ, so the scheduled `doctor` and monitoring
  see it.
- One comparison behind every view: the command, the check, the support
  bundle and the fleet row cannot disagree.

**Non-goals**

- **Correcting drift.** `apply` is the correction, as RFC 0027 §4.5 decided for
  reconciliation in general.
- **Keeping a hand edit.** A change meant to last belongs in a parameter or in
  the release. This RFC makes the edit visible before it is replaced; it does
  not preserve it.
- **RFC 0027 P3.** That compares a desired-state file with the installation.
  It stays gated with 0027 P2.

## 5. Design

### 5.1 `morzer config diff`

```console
$ sudo morzer config diff
--- /etc/demo/application.yaml
@@ -3,1 +3,1 @@
- log_level: debug
+ log_level: info

1 of 4 configuration files differ from what 1.2.0 renders.
`morzer apply` replaces them; a change meant to last belongs in a parameter.
```

- Reads `configComparison`, takes no lock and writes nothing.
- Prints the diffs on stdout and the summary on stderr. With no drift it prints
  `no drift: the 4 configuration files match what 1.2.0 renders`.
- Lists unreadable targets after the diffs, each with its error, and counts
  them apart: "2 could not be read, so they were not compared".
- `--json` carries `release`, `targets`, `drift` (a list of
  `{target, diff}`) and `unreadable` (a list of `{target, error}`).
- Exits 0 whether or not files differ (decision 6).
- With no release installed it fails as `config list` does.
- **Both sides of each diff are scrubbed before they are printed.** The render
  side cannot hold a secret value: the render context holds references, not
  values. The on-disk side can, once an operator pastes one into a file. So
  every diff passes through the redactor, armed with the values this
  installation holds (`armRedaction`, `inspect.go:189`), as the support bundle
  scrubs `config-diff.txt` (`scrub`, `support.go:1126-1145`). When it cannot
  be armed, because the secret state does not decrypt, no lines are printed:
  each differing target is named, and the summary says redaction was not
  possible. `--json` then carries `redacted: false` and no diffs (decision 10).

### 5.2 `doctor` check `config.drift`

- `ok`: "every configuration file matches what the release renders".
- `warn`: "2 configuration files differ from what 1.2.0 renders:
  /etc/demo/application.yaml, /etc/demo/nginx.conf". The remedy names
  `morzer config diff` to see them, and `morzer apply` to replace them.
- A target that cannot be read is named in the message and not counted, as in
  the fleet row.
- Skipped, with the reason, when no release is installed or the release cannot
  be read.

Warn or fail is decision 3.

### 5.3 The unattended gate

Decision 4 asks whether an unattended update waits while files differ. If
yes: the tick checks `configComparison` beside the stopped check, and leaves
the release staged with the reason "2 configuration files differ from what
the installed release renders; `morzer config diff` shows them", reported as
the stopped case is. `AssessUnattended` stays a function of compatibility and
policy. An interactive `update` is unchanged: its plan already shows the
diff.

### 5.4 P2: services whose definition drifted

Compose labels each container with `com.docker.compose.config-hash`, and
`docker compose config --hash '*'` prints the hash each service would get now.
A service whose two hashes differ is one `up`, and so `apply`, would
re-create. P2 adds those services to `config diff` under their own heading, to
`doctor` as `runtime.definition`, and to `--json` as `services`.

It needs one more compose call, `config --hash`, which older Compose versions
lack. A runtime that cannot answer is reported as not compared, and is not
counted as drift.

### 5.5 One comparison

`config diff`, `config.drift`, the unattended tick, the support bundle and
the fleet row all call `configComparison`. `apply --dry-run`'s plan detail
moves onto it too, so no second loop remains to disagree with the others. It
gains the profile to render with, empty for the installed one, because a plan
for `update --profile` renders the profile it was asked for.

### Rejected alternatives

- **`status --drift`.** `status` answers "is it up", fast, without rendering
  templates. Drift belongs with `config`, the command group that owns what is
  rendered. Decision 2 keeps it open.
- **A drift exit code.** Every non-zero exit in `exit-codes.md` is a failure.
  A difference is a finding, and `doctor` is where findings meet exit codes.
- **Keeping a copy of each replaced file.** It answers "where did my edit go"
  after the fact, adds files to `/etc` that nothing cleans up, and keeps
  hand edits alive as a second source of configuration.

## 6. Tests

- A hand-edited target: `config diff` prints its diff and the count, `--json`
  carries it, and `doctor` warns naming it. After `apply`, both report no
  drift.
- An absent target counts as drift. An unreadable one is listed apart and not
  counted, in the command, the check and the fleet row.
- A target with a stored secret's value pasted in prints the redactor's mark
  in its place, in text and in `--json`. With the secret state undecryptable,
  only the target names print.
- With no release installed, `config diff` fails as `config list` does, and the
  check is skipped.
- `apply --dry-run`'s plan detail matches `config diff` on the same machine.
- If decision 4 is yes: an unattended tick with drift leaves the release
  staged and says why, and an interactive `update` proceeds.
- P2: a service whose definition hash differs is listed; a runtime that cannot
  answer is reported and not counted.

## 7. Docs

- `reference/commands.md`: `config diff`, and `config.drift` in the `doctor`
  table.
- `operating/changing-configuration.md`: a section on hand edits — what
  replaces them, how to see them first, and where a lasting change belongs.
- `operating/unattended-updates.md`: the wait, if decision 4 is yes.

## 8. Out of scope

- **Secret files.** Rendered secrets are values. They are never compared or
  shown, here or in the support bundle.
- **Files the release does not render.** Only declared configuration targets
  are compared.
- **RFC 0027 P3.** As in §4.

## 9. Risks

- **Rendering on every `doctor` run.** It is the render `apply --dry-run` and
  the fleet row already do, read-only, and has the same cost.
- **A warning nobody acts on.** A file edited on purpose and kept that way
  warns on every `doctor` run. That is intended: the next `apply` will replace
  it, and the warning is the notice of that.
- **A secret this installation does not hold.** A third-party key pasted into
  a file by hand is not one the redactor was told about, and prints. The
  support bundle has the same limit today.
- **Root-only files.** Run without `sudo`, targets readable only by root are
  listed as not compared rather than as drift.

## 10. Unresolved questions

None. The author ruled §11's `OPEN` rows 2026-10-31.

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `ASSUMED` | Drift is a configuration target whose bytes on disk differ from what the installed release renders from the recorded parameters, and it is computed only by `configComparison`. An absent target is drift; an unreadable one is reported apart and never counted. |
| 2 | `ASSUMED` | **Where the view lives.** Recommended: `morzer config diff`, beside the commands that change what is rendered. Alternative: `status --drift`, which puts it next to the question "is it up" but makes `status` render templates when asked. Was `OPEN`; ruled by the author 2026-10-31: as recommended. |
| 3 | `ASSUMED` | **How `doctor` reports drift.** Recommended: `config.drift` warns, as `secrets.rotation` does, because a hand edit can be deliberate and a failing exit code over one trains people to ignore it. Alternative: it fails, exit 3, so monitoring that watches the exit code pages on it. Was `OPEN`; ruled by the author 2026-10-31: as recommended. |
| 4 | `LOCKED` | **Whether drift holds an unattended update.** Recommended: yes, as a stopped product does (RFC 0040 decision 9): the release stays staged with the reason. A hand edit replaced with nobody watching cannot be recovered, and a staged release can be installed later. Alternative: no; the scheduled `doctor` warns, and the update replaces the file as now. Was `OPEN`; ruled by the author 2026-10-31: as recommended. |
| 5 | `ASSUMED` | **Whether P2 belongs here.** Recommended: yes, as P2: services whose definition hash differs are the same question one layer down, and an operator reading `config diff` before `apply` wants both. Alternative: out of scope until someone asks. Was `OPEN`; ruled by the author 2026-10-31: as recommended. |
| 6 | `ASSUMED` | `config diff` exits 0 whether or not files differ. Exit codes in this project mean failure; `doctor` is where a finding becomes one. |
| 7 | `ASSUMED` | Read-only and lock-free, like `apply --dry-run` and `config list`. |
| 8 | `ASSUMED` | `apply --dry-run`'s plan detail is computed by `configComparison` too, so no second comparison remains. It takes the profile to render with, empty for the installed one. |
| 9 | `ASSUMED` | Secret values are never compared or shown. The invariant is the support bundle's: the render context holds secret references, not values. *(Superseded by decision 10.)* |
| 10 | `ASSUMED` | *(2026-10-31, from the review.)* Every diff is scrubbed by the redactor before it is shown, on both sides: the rendered side holds no secret value by the support bundle's invariant, but the on-disk side holds whatever was pasted into it. When redaction cannot be armed, no diff lines are shown, only the target names. |

## 12. Phasing

- **P1**: §5.1, §5.2, §5.3 and §5.5. One PR.
- **P2**: §5.4. One PR.
- Nothing is demand-gated.
