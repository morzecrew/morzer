# Validate calendar expressions · ask systemd before storing a schedule

Follow-up to `update-schedule` (#86), on branch `validate-calendar-expressions`
from `866f9bb`. Three reviewers on #86 found that a schedule systemd cannot
parse was stored and rendered, leaving a timer that does not load; it was
acknowledged there as out of scope, because the fix needs a supervisor method
and covers `backup.schedule` too. The author asked for the follow-up on
2026-10-14, which settles the approach described on #86: ask the supervisor,
via `systemd-analyze calendar`, at the doors an operator types into.

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
at: 2026-10-14T19:56:37Z
attempt: 1
claim: nothing said who judges calendar syntax beyond the domain's refusal to; the supervisor port gains ValidateSchedule, answered by systemd-analyze calendar on the machine that will load the unit
evidence: internal/domain/installation.go:687
action: decided
proposal: ASSUMED — RFC 0016 decision 27: the supervisor validates a schedule before it is stored. The domain keeps no calendar grammar, as its comment has always argued; the supervisor is the party it named.
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-14T19:56:37Z
attempt: 1
claim: a host without systemd-analyze cannot be asked; refusing there would block a schedule on the hosts least likely to render one, so the value is accepted unchecked, as every value was before
evidence: internal/adapters/supervisor/systemd/systemd.go:304
action: decided
proposal: ASSUMED — an absent parser accepts. Revisit if a distribution is found that ships systemctl without systemd-analyze.
```

```divergence
decision: unlisted
grade: UNLISTED
class: discovery
at: 2026-10-14T19:56:37Z
attempt: 1
claim: patch coverage found the branch where systemd-analyze exists but could not be run; reporting that as "invalid" would send an operator to rewrite a good expression, and as "valid" would be a verdict nobody gave
evidence: internal/adapters/supervisor/systemd/systemd.go:324
action: decided
proposal: ASSUMED — a parser that did not run is an internal error, neither a refusal nor an acceptance.
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-14T19:56:37Z
attempt: 1
claim: the doors are `config set` for both schedules and `init --backup-schedule`, the latter before anything is created; not the state loader, which would exec a parser on every load, and not `installation import`, whose value was accepted where it was set
evidence: internal/lifecycle/ops/init.go:138
action: decided
proposal: ASSUMED — validate where an operator types a schedule, and nowhere that reads one back.
```

Not covered, deliberately: an expression that parses and never elapses again,
such as `2001-01-01`. It loads, and its timer never fires. Whether to refuse
one is a different question from whether it parses, and nobody has asked it.

Sabotage, each mutant compiled and restored from a copy, with the feature
committed first: eight on the wiring and the adapter, then two on the branches
coverage found uncovered -- ten of ten killed. Patch coverage after: every
added statement line in `internal/` executed.
