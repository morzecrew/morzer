# vendor-preflight · RFC 0045 P1, preflight checks a vendor ships

On branch `vendor-preflight` from `8c02b43`, building
[RFC 0045](../rfcs/0045-preflight-checks-a-vendor-ships.md) P1 as ruled by the
author on 2026-11-02. That covers the manifest's `preflight:` map and its
validation, running the checks in the preflight step and in `doctor`, the hook
environment they get, `release show` and `release diff`, the tests of §6 and
the docs of §7.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

Three things the RFC does not settle, found while planning and decided before
any code.

§6 has the example bundle declare one check, so the real Docker run exercises
the step. A bundle declaring checks must declare the manager version that runs
them (decision 2), and a manager without a stamped version reports 0.0.0, so
every test that updates to the example bundle would be refused as too new.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-02T12:38:00Z
attempt: 1
claim: the example bundle cannot declare a check while test builds report manager 0.0.0, since the floor would refuse every update to it
evidence: internal/cli/root.go:1448-1457
action: decided
proposal: ASSUMED — the example bundle declares no check until a released manager runs them. The CLI tests use a copy of it that declares checks, as `BundleWithATask` does for tasks, and the acceptance run does not exercise the step.
```

Decision 10 makes `doctor` an `OPERATION_TYPE` value. `domain.OperationType`
names a journalled, mutating command, and `doctor` is neither.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-02T12:38:00Z
attempt: 1
claim: decision 10's `doctor` value has no home among the operation types, which are the journal's vocabulary
evidence: internal/domain/operation.go:5-7
action: decided
proposal: ASSUMED — `doctor` is a hook-environment value only, `ports.HookOperationDoctor`, never a domain.OperationType: nothing journals it and no lock is taken under it.
```

The RFC requires a description and allows a remedy, and says nothing of their
shape. `doctor` prints both on the operator's terminal, from a bundle whose
signature is optional.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-02T12:38:00Z
attempt: 1
claim: a check's description and remedy reach the operator's terminal and nothing bounds them
evidence: internal/domain/manifest.go:1718-1722
action: decided
proposal: ASSUMED — a check's description and remedy are each one line of at most 200 characters with no control characters, validated as a task's description is.
```

Found while building.

A hook inherits the manager's own environment (`exec.BaseEnv` starts from
`os.Environ`), so decision 10's "absent" can only mean the manager does not set
those variables. A `DEMO_SECRETS_DIR` exported in the shell that runs `morzer`
would still reach a check, as it reaches every hook today.

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-02T12:51:00Z
attempt: 1
claim: SECRETS_DIR, CONFIG_FILE and BACKUP_DIR are absent only in what the manager sets; a variable the manager's own environment holds still reaches the check
evidence: internal/infra/exec/exec.go:538-540
action: decided
proposal: ASSUMED — a check's environment leaves out SECRETS_DIR, CONFIG_FILE and BACKUP_DIR of what the manager sets, and inherits the manager's own environment as every hook does. The test asserts on the variables the manager builds for a check.
```

Close-outs for the `LOCKED` rows the branch touched.

```divergence
decision: 1
grade: LOCKED
kind: resolved
at: 2026-11-02T12:52:00Z
attempt: 1
claim: `preflight:` is a map of named checks, each its own command, with `fatal`, `timeout`, `description` and `remedy` declared in the manifest
evidence: internal/domain/manifest.go:765-785
action: decided
```

```divergence
decision: 2
grade: LOCKED
kind: resolved
at: 2026-11-02T12:52:00Z
attempt: 1
claim: `preflight:` is a top-level field, and a bundle declaring checks below manager 0.12.0 is refused by validation
evidence: internal/domain/manifest.go:1441-1446
action: decided
```

```divergence
decision: 4
grade: LOCKED
kind: resolved
at: 2026-11-02T12:52:00Z
attempt: 1
claim: a fatal check refuses `apply`, a first install and `update`; boot, `start` and `rollback` only warn; `doctor` fails on it
evidence: internal/lifecycle/ops/apply.go:311-316
action: decided
```

```divergence
decision: 16
grade: LOCKED
kind: resolved
at: 2026-11-02T12:52:00Z
attempt: 1
claim: the read-only contract is stated in the manifest and hook references, and `doctor` runs checks without the lock
evidence: pages/docs/reference/manifest.md:602-604
action: decided
```

Found in review, 2026-11-02.

A check's output is printed on the operator's terminal by `doctor` and the
preflight step, and it is the vendor's process's output, not manifest text that
validation has seen.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-02T13:40:00Z
attempt: 1
claim: a check's fd 3 message and the stderr or stdout a failure quotes reach the terminal with whatever escape sequences the check wrote
evidence: internal/lifecycle/ops/vendor_preflight.go:86-92
action: decided
proposal: ASSUMED — what a check says is stripped of escape sequences and control characters, as release notes are, and joined onto one line. Its description and remedy are printable characters only, so a bidi override is refused at load.
```

An operator's Ctrl-C while a check runs reached the report as that check
failing, so a fatal check turned it into a refusal naming the vendor's check.

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-02T13:40:00Z
attempt: 1
claim: a check cut off by the operator's cancellation was reported as a failed check, exit 3, instead of an interruption
evidence: internal/lifecycle/ops/vendor_preflight.go:94-101
action: decided
proposal: ASSUMED — a preflight step or a `doctor` run the operator cancels ends as an interruption, exit 130, whatever its checks reported when they were cut off.
```

The example bundle can declare a check once a released manager runs them, and
the acceptance run exercises the step from then on.

The two review entries above say 13:40; they were written before their commit
at 13:24, so that time is late by a quarter of an hour.

Corrected in review: the cancellation entry above is not a discovery. The
codebase already reports an operator's cancellation as an interruption, and the
RFC said nothing of it, so it could have been known before code.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-02T13:40:30Z
attempt: 2
claim: a cancelled preflight step reporting a failed check was knowable from the codebase's convention, which reports cancellation as domain.Interrupted, and the RFC was silent on it
evidence: internal/adapters/source/https/https.go:219
action: decided
proposal: ASSUMED — a preflight step or a `doctor` run the operator cancels ends as an interruption, exit 130, whatever its checks reported when they were cut off.
```

Found in the review's second round: the stripping of a check's output removed
escape sequences and control characters, and left the bidi controls, which are
formatting characters rather than control characters.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-02T16:03:00Z
attempt: 1
claim: what a check says could still carry bidi controls, which let it reorder the line an operator reads, since the stripping release notes share removed only escape sequences and control characters
evidence: internal/release/notes.go:109
action: decided
proposal: ASSUMED — the stripping a check's output and release notes share removes the bidi controls too. Other formatting characters stay, since joiners are part of how some scripts are written.
```
