# No way back · a failure after an unreadable migration is not compensated

Finding 1 from the review of the ERP bundle's requests, on branch
`a-migrated-database-is-not-rolled-back` from `bd74239`. Reproduced before any
change, in a scratch worktree: 1.2.0 → 1.3.0 with health checks failing exits 11
with "earlier changes were rolled back", while the database marker is at 14,
1.2.0 is recorded at schema 12 and reads at most 12, no operation needs
attention, and the rendered secrets directory is gone.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

RFC 0001 carries no grades, so its rows are treated as `LOCKED`.

```divergence
decision: RFC 0001 D-2
grade: LOCKED
class: spec-gap
at: 2026-10-15T08:30:00Z
attempt: 1
claim: D-2 says compensation reverts the release pointer and symlink only, and never asks what a completed migration did; compensation after one reverted configuration and secrets toward a release that cannot read the migrated database and reported exit 11, the rollback D-5 refuses, taken without its assessment
evidence: internal/lifecycle/engine/engine.go:384
action: halted
proposal: LOCKED — refines D-2: compensation never crosses a migration that left a database the fallback release cannot read, judged by AssessRollback; a later failure ends at requires-manual-intervention with the new release left in place.
```

**Ruled 2026-10-15 by the author: "fix finding 1 first, then do amendment".**
The proposal above is taken as written. The amendment (item 4, restoring the
previous release's containers when compensation is allowed) follows on its own
branch. Entries from here on record the build.

```divergence
decision: RFC 0001 D-2
grade: LOCKED
kind: resolved
at: 2026-10-15T05:56:34Z
attempt: 1
claim: built as ruled -- the migrate step records a point of no return when AssessRollback says returning would need a restore, and the engine then undoes only what came after it; recorded as decision 18
evidence: internal/lifecycle/ops/apply.go:686
action: decided
```

```divergence
decision: RFC 0001 D-8
grade: LOCKED
kind: resolved
at: 2026-10-15T05:56:34Z
attempt: 1
claim: no new port, event kind, exit code or state-schema field; the point of no return is a value on the engine's operation state, and the outcome is the existing requires-manual-intervention
evidence: internal/lifecycle/engine/engine.go:384
action: decided
```

```divergence
decision: unlisted
grade: UNLISTED
class: discovery
at: 2026-10-15T05:56:34Z
attempt: 1
claim: stage-release never moves the release pointer -- record-state does, last -- so its compensator was already a no-op on this path; the harm was the configuration and secrets put back, the exit-11 claim and the lost schema, and past the point of no return the record still names the old release while everything else is the new one's, so the error names `update --to <new>` as the way forward
evidence: internal/lifecycle/ops/update.go:825
action: decided
proposal: ASSUMED — part of decision 18: past the point of no return the release record is left naming the old release, and the error says how to finish rather than moving the pointer early.
```

```divergence
decision: unlisted
grade: UNLISTED
class: discovery
at: 2026-10-15T05:56:34Z
attempt: 1
claim: a resumed update re-runs migrate, which reports "already current", so judging only migrations that ran would let the attempt before the crash move the schema and compensation cross it anyway; a skipped migration is judged by the schema it reports too
evidence: internal/lifecycle/ops/apply.go:683
action: decided
proposal: ASSUMED — part of decision 18: the question is what the database is at now, not whether this run moved it.
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-15T05:56:34Z
attempt: 1
claim: the running release's manifest can be unreadable while its runtime options are on record, and refusing the update for that would be a new refusal unrelated to this fix; it is recorded as unknown and treated as unable to read anything, which closes the way back after any migration
evidence: internal/lifecycle/ops/update.go:89
action: decided
proposal: ASSUMED — an unreadable fallback is never fallen back to after a migration; the update itself still runs.
```

Sabotage, each mutant compiled and restored from a copy, with the change
committed first: twelve on the engine filter, the status, the first-reason rule,
the journal message, both migrate kinds, the declared-schema fallback, the
unknown fallback, and the two halves of how `Update` resolves it. Ten were killed
by the first tests; the last two survived until `TestAnUnreadableFallbackIsNotFallenBackTo`
was added. Twelve of twelve killed.

**Drift count: 0.**

## Review, round 1, 2026-10-15

- **Fixed — an abort past the point of no return ended `failed`.** Only the
  compensating branch consulted the point, and `record-state` and
  `retire-parameters` abort. An update whose release record could not be
  written after migrating ended `failed`, which blocks nothing, and the next
  boot's `apply` converged the old release over the migrated database.
  Reproduced red first (`TestAnAbortPastThePointOfNoReturnNeedsAPerson`,
  "failed", exit 7); any non-interrupted failure past the point now needs a
  person. Decision 18 already said "a later failure", so the row stands.
- **Kept, out of scope — `from` can be stale under `--wait`.** Real, and older
  than this change: `Update` reads the current release once, before the lock,
  and the compatibility check and the staging compensator use it as well as the
  fallback. Re-reading it under the lock is its own change.

**Drift count: 0.**
