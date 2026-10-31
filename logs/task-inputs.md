# task-inputs · RFC 0041 P1, `task run --input`

On branch `task-inputs` from `b946bf1`, building
[RFC 0041](../rfcs/0041-files-an-operator-passes-to-a-task.md) P1 as ruled by
the author on 2026-10-30. That covers:

- the manifest's `tasks.<name>.inputs` and its `release verify` checks;
- `task run --input <name>=<path>` and its refusals;
- staging under `<var>/manager/inputs/<operation-id>/`, with the copy bounded and hashed;
- the read-only mount at `/morzer/inputs/<name>`, with both `{inputs.<name>}` and `MORZER_INPUT_<NAME>`;
- the journal flag and attestation predicate v3;
- `--dry-run`, the sweep and `doctor` report of leftovers;
- the tests of §6 and the docs of §7.

Closes #215.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

Five things the RFC leaves to the build, decided while planning and before any
code.

Decision 12 makes the predicate v3 but does not say which statements carry it.
v2 set the precedent: every statement written after it carried the new type,
the old schema file stayed published, and `attest verify` read both.

```divergence
decision: 12
grade: LOCKED
kind: resolved
at: 2026-10-30T21:21:00Z
attempt: 1
claim: v3 follows v2's precedent, so every new statement is v3, `inputs` is present only when a task had some, v2's schema file stays published, and `attest verify` reads v1, v2 and v3
evidence: internal/domain/attestation.go:43
action: decided
```

§5.3 asks for "the sum of the sizes and a margin" and names no margin.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-10-30T21:22:00Z
attempt: 1
claim: the free-space check for staging needs a margin the RFC does not size
evidence: rfcs/0041-files-an-operator-passes-to-a-task.md:156
action: decided
proposal: ASSUMED — staging needs the inputs' total size plus 64 MiB free under `<var>/manager/inputs`; the copy is bounded by that budget, less the margin, as well as by each `max_size`.
```

§5.4 says `{inputs.<name>}` "is replaced", but not whether it must be a whole
argv element. A vendor command such as `--file={inputs.snapshot}` needs it
inside one.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-10-30T21:23:00Z
attempt: 1
claim: the placeholder's position inside an argv element is not settled
evidence: rfcs/0041-files-an-operator-passes-to-a-task.md:186
action: decided
proposal: ASSUMED — `{inputs.<name>}` is replaced wherever it appears inside an element of the declared `command`; `release verify` refuses any `{inputs.` that does not name a declared input.
```

§5.3 says the next `task run` removes every staging directory "whose operation
is not running", but not how it tells.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-10-30T21:24:00Z
attempt: 1
claim: the sweep of leftover staging directories needs a way to tell a running operation's directory from a dead one's
evidence: rfcs/0041-files-an-operator-passes-to-a-task.md:166
action: decided
proposal: ASSUMED — the sweep runs inside `stage-inputs`, under the deployment lock, where no other operation can be running, so every directory there except this operation's is a leftover; `doctor` warns about any it finds, as `tasks.staged-inputs`.
```

Decision 3 asks for `min_manager_version` "at least the release that ships
this", which is a number the code has to name.

```divergence
decision: 3
grade: ASSUMED
class: spec-gap
at: 2026-10-30T21:25:00Z
attempt: 1
claim: the minimum manager version for `inputs` has to be a concrete release number, and the next release is 0.11.0
evidence: internal/domain/manifest.go:686
action: departed
proposal: ASSUMED — a manifest whose tasks declare `inputs` declares `min_manager_version` at least 0.11.0, the release that ships them, through a constant beside `TasksMinManagerVersion`.
```

## Found while building

Three more, decided when the code reached them.

The journal flag carries the digest of the bytes copied (decision 13), which is
known only once `stage-inputs` has run. An operation's flags were fixed before
the engine started, and the only way a step could have added one was by writing
into the map the record shares with the caller.

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-10-30T21:36:00Z
attempt: 1
claim: a journal flag a step learns while it runs had no way into the record except by mutating the operation's own map
evidence: internal/lifecycle/engine/engine.go:176
action: decided
proposal: ASSUMED — a step adds a journal flag with `State.SetFlag`; the engine merges it into a copy of the record's flags before it journals that step, so it is kept even when a later step fails.
```

Opening a named pipe to check its type blocks until something writes to it, so
"refused" would have been a hang.

```divergence
decision: 11
grade: ASSUMED
class: discovery
at: 2026-10-30T21:37:00Z
attempt: 1
claim: opening the operator's path to check its type blocks on a named pipe
evidence: internal/lifecycle/ops/task_inputs.go:98
action: departed
proposal: ASSUMED — `--input` paths are opened non-blocking, so a named pipe is refused rather than waited on; the type check and the copy still use that one descriptor.
```

§5.2 makes every refusal of the operator's request exit 2. Running out of space
is a refusal of the machine, not of the request.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-10-30T21:38:00Z
attempt: 1
claim: the RFC gives no exit code for inputs that do not fit in the free space
evidence: internal/lifecycle/ops/task_inputs.go:159
action: decided
proposal: ASSUMED — inputs that do not fit are refused before the lock with the preflight exit code, as other checks of the machine are; a file that grows past the free space while it is copied is refused the same way.
```

The `LOCKED` rows this branch touches, honored:

```divergence
decision: 1
grade: LOCKED
kind: resolved
at: 2026-10-30T21:39:00Z
attempt: 1
claim: named file inputs are built as P1
evidence: internal/lifecycle/ops/task.go:330
action: decided
```

```divergence
decision: 2
grade: LOCKED
kind: resolved
at: 2026-10-30T21:39:00Z
attempt: 1
claim: the task finds an input through both `{inputs.<name>}` in its declared command and MORZER_INPUT_<NAME>, and an input no placeholder names is not refused
evidence: internal/lifecycle/ops/task.go:370
action: decided
```

**Drift count: 0.**

## Found in review

Three, each from a reviewer's finding on #225.

The sweep proposed above ran inside `stage-inputs`, so only a task that takes
inputs swept. A killed task's copy then waited for the next task that happened
to declare one, while `doctor` told the operator the next `task run` would
remove it. This supersedes the earlier entry's "the sweep runs inside
`stage-inputs`".

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-10-30T21:54:00Z
attempt: 2
claim: a sweep inside stage-inputs runs only for a task that takes inputs, so leftovers outlived every other task
evidence: internal/lifecycle/ops/task.go:226
action: decided
proposal: ASSUMED — every `task run` sweeps leftover staging directories under the deployment lock before its steps, and a sweep that fails warns rather than failing the task.
```

`doctor` takes no lock, so the operation holding it may be staging its inputs
while `doctor` reads the directory.

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-10-30T21:55:00Z
attempt: 2
claim: doctor named the running task's staging directory as a leftover and told the operator to remove it
evidence: internal/lifecycle/ops/task_inputs.go:335
action: decided
proposal: ASSUMED — `tasks.staged-inputs` leaves out the directory of the operation holding the deployment lock.
```

`docker compose run` has only the short `--volume` syntax, which splits on `:`.

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-10-30T21:56:00Z
attempt: 2
claim: a host path containing ':' cannot be expressed to `compose run --volume`
evidence: internal/adapters/runtime/compose/compose.go:488
action: decided
proposal: ASSUMED — the Compose runtime refuses a mount whose path contains ':', by name, before running anything; the default layout has none.
```

**Drift count: 0.**
