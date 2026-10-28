# RFC 0034 · removing an installation

On branch `installation-remove` from `e1ed187`, building
[RFC 0034](../rfcs/0034-removing-an-installation.md), which was written in the
same change: `morzer installation remove`, its default and `--purge`, its
confirmation, its steps and the record it leaves. Closes #158.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

Every decision in the RFC is `ASSUMED` and was written before any code, so
there is no ruling to depart from. What the build turns up is recorded below.

One question was settled by a spike before the RFC was written, and is recorded
here because decision 10 rests on it: Compose 5.5 takes a project down by name
from an empty project directory, volumes included.

```console
$ docker compose --project-name mzrmtest --project-directory "$EMPTY" down --volumes --remove-orphans
 Volume mzrmtest_data Removing
 Network mzrmtest_default Removing
 Volume mzrmtest_data Removed
 Network mzrmtest_default Removed
```

An installation record that exists and will not load is refused, not read as
"no record". §5.5 covers a missing record, and is silent on an unreadable one.
Reading it as missing would skip the production confirmation on exactly the
machine whose record says it is production.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-28T19:58:22Z
attempt: 1
claim: §5.5 covers a machine with no installation record and says nothing of one whose record exists and cannot be read
evidence: internal/lifecycle/ops/remove.go:76-87
action: decided
proposal: ASSUMED — an unreadable installation record refuses the removal, with a hint to move the file aside; the removal then treats the machine as one with no record, and asks only for --force.
```

A product with nothing of it on the machine is refused. Without the refusal, a
typo in `--product` journaled a removal of nothing, and the lock and the
journal created `/var/lib/<typo>` to do it.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-28T19:58:22Z
attempt: 1
claim: the RFC does not say what a removal of a product with no directory and no unit on the machine does, and running it creates the directories it journals into
evidence: internal/lifecycle/ops/remove.go:88-95
action: decided
proposal: ASSUMED — when none of the product's four directories and none of its units exist, the removal is refused before the lock, naming the product and pointing at `morzer ls`.
```

A typed `--confirm` that is not this installation's id is refused on a dev
installation too. Decision 3 says a dev installation needs `--force` alone, and
it still does. An id the operator typed and that names something else means
the command is pointed at the wrong machine, whatever the mode.

```divergence
decision: 3
grade: ASSUMED
class: spec-gap
at: 2026-10-28T19:58:22Z
attempt: 1
claim: decision 3 says what confirmation each kind of machine needs, and not what a confirmation that names a different installation does on a dev one
evidence: internal/lifecycle/ops/remove.go:104-111
action: departed
proposal: ASSUMED — a --confirm that does not match is refused on every installation; on a dev installation --confirm stays optional.
```

A plan refuses what the operation would refuse, so `--dry-run` needs `--force`
and, on production, `--confirm`. §5.2 does not mention plans. The global
`--dry-run` contract says a plan refuses what the operation would, and
`restore` already behaves that way.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-28T19:58:22Z
attempt: 1
claim: §5.2 does not say whether a plan of a removal is confirmed like the removal
evidence: pages/docs/reference/commands.md:28
action: decided
proposal: ASSUMED — `installation remove --dry-run` is confirmed exactly as the removal is, per the global --dry-run contract.
```

§5.4 says the purged journal holds only the removal's record "because
`remove-data` took the rest". That is not the mechanism. `remove-data` leaves
`manager/` alone, because the lock is in it. Under `--purge`,
`forget-installation` removes the live journal and the journals of earlier
removals, and the engine's next append starts the journal again with this
removal's record. The outcome is the one §5.4 describes. The sentence naming
the step is wrong.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-28T19:58:22Z
attempt: 1
claim: §5.4 attributes the purged journal's contents to remove-data, which cannot touch manager/ while the lock lives there
evidence: internal/lifecycle/ops/remove.go:515-535
action: decided
proposal: ASSUMED — under --purge, forget-installation removes the journal and earlier removals' journals, keeping only locks/; the engine's next append restarts the journal with the removal's own record.
```

## Self-audit, 2026-10-28

The whole branch: 19 files, about 1,700 lines. That is the RFC, the operation,
the split of `emitAttestation` and `pushStatement`, `removePlannedImages`
factored out of `PruneReleases`, the command, the fake's `DownConfigs`, the
tests and the docs.

**Fixed:**

- **A resumed removal renamed the journal after an id no record carries.**
  - `finishRemoval` took the id minted for this run.
  - On `--resume` the engine continues the earlier record under that record's
    id, so the renamed journal and the removal's statement disagreed.
  - It now takes `result.Record.ID`. The resume test asserts the name, and
    fails on the old code.
- **The docs implied every removal is attested.** One with no installation
  record is not. Said so in the reference.
- **`managerKept`'s comment was ambiguous** about whether earlier removals'
  journals survive a purge. They do not.
- **A machine where only units are left was untested.** The case is real when
  the directories were deleted by hand. Added the test, and broke
  `anythingLeft`'s unit branch to watch it fail.

**Checked, nothing found:**

- **Product names.** A traversal through `--product ../x` is refused when paths
  are resolved, before any `RemoveAll`. Confirmed by running the binary.
- **Sabotage.** Keeping `age/` under `--purge` and dropping it by default each
  fail the two tests that pin them.
- **Credential ordering.** Resolving attestation targets after the steps fails
  the credential-ordering test.
- **Coverage of `remove.go`** under the suite and clitest is 70–100% per
  function. What is uncovered is filesystem error branches and the warnings in
  `finishRemoval`.

**Residue:**

- **The project-name fallback** is proved by a fake and by the spike above,
  not by a test against Compose.
- **An installation from before runtime options were recorded,** whose release
  also cannot be read, is taken down under the product name. If its release had
  set another project, that `down` finds nothing and leaves its containers.
