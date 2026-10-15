# Unattended retry · a failed unattended install is tried once

From the ERP bundle's requests (item 3), on branch
`unattended-does-not-retry-a-failed-install` from `bd74239`. The report was
that a failed unattended update is staged and applied again on every tick.
Verified, with one correction: the loop is real for a compensated (exit 11) or
failed update, because neither blocks the next operation; an update that ended
needing intervention (exit 12) was already stopped by `update`'s own gate, and
each tick after it failed with exit 3 instead. RFC 0001 §8 already rules
automatic retry out ("would obscure the failure that a human should look at"),
and RFC 0016 §4 keeps everything inside an update as 0001 shipped it; RFC 0016
never considered that the candidate outlives a failed install, so the loop is a
gap in 0016 rather than a departure from it.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-15T05:26:23Z
attempt: 1
claim: RFC 0016 never says what the next tick does with a candidate whose unattended install failed, and the code re-installed it every tick; the failure is now recorded on the candidate and the tick skips it until the channel moves or an operator runs update --to
evidence: internal/lifecycle/ops/channel.go:521
action: decided
proposal: ASSUMED — RFC 0016 decision 28: an unattended tick installs a staged release at most once. A failure is recorded on the update candidate, and the timer leaves that candidate alone until the channel points somewhere new or an operator installs it with `update --to`. No `--retry` flag: the explicit update is the retry.
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-15T05:26:23Z
attempt: 1
claim: with the skip above, a tick whose earlier install ended needing intervention would exit 0 on a quiet-night summary; the tick now refuses first, before fetching, whenever an operation needs a person
evidence: internal/lifecycle/ops/channel.go:464
action: decided
proposal: ASSUMED — RFC 0016 decision 29: while an operation is flagged for manual intervention, a tick fetches nothing and installs nothing, and fails with the refusal `update` gives.
```

```divergence
decision: unlisted
grade: UNLISTED
class: discovery
at: 2026-10-15T05:26:23Z
attempt: 1
claim: an update refused before its operation started -- the lock, a preflight -- returns an error with no operation record, and marking the candidate then would leave a good release uninstalled until the vendor published again
evidence: internal/lifecycle/ops/channel.go:520
action: decided
proposal: ASSUMED — part of decision 28: only an install that ran and failed is recorded.
```

```divergence
decision: unlisted
grade: UNLISTED
class: discovery
at: 2026-10-15T05:26:23Z
attempt: 1
claim: gateUnfinished also refuses over a record that is merely running, so using it at the top of the tick would fail the unit whenever a tick overlapped an operator's backup; the tick's gate is narrowed to records needing attention, and contention stays a lock question
evidence: internal/lifecycle/ops/channel.go:540
action: decided
proposal: ASSUMED — part of decision 29: "needs a person" means NeedsAttention, not "unfinished".
```

```divergence
decision: RFC 0016 D-20
grade: LOCKED
class: spec-gap
at: 2026-10-15T05:26:23Z
attempt: 1
claim: decision 20's rationale says a health check that never passes reaches requires-manual-intervention; the health-checks step compensates, so the update ends compensated (exit 11) on the old release, and RunUnattended's comment repeated the claim; the comment is corrected, the row's promise ("no database restore will be required") is untouched
evidence: internal/lifecycle/ops/apply.go:714-717
action: halted
proposal: LOCKED — decision 20 unchanged; its rationale to read "through a failed migration hook or a converge the engine cannot compensate; a health check that never passes ends compensated".
```

Sabotage, each mutant compiled and restored from a copy, with the change
committed first: seven on the skip, the record, the two gates and the status
line. Five were killed by the first four tests; the digest re-check and the
narrowed gate survived, and each now has a test of its own. Seven of seven
killed.

Not changed, and found on the way: an update whose `migrate` completed and
whose later step failed ends compensated, with the database moved forward and
the release pointer moved back, and the error says "earlier changes were rolled
back". That is outside this task and is reported to the author, not fixed here.

**Drift count: 0.**
