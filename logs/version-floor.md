# version-floor · RFC 0037 P1, the installed floor and channel marks

On branch `version-floor` from `66a5481`, building
[RFC 0037](../rfcs/0037-freshness-of-a-signed-release.md) P1 as ruled by the
author on 2026-11-01: `update` refuses a release older than the one installed,
the channel follower refuses a release older than the newest it fetched and
verified from that channel, `update --allow-older` overrides both and is
journalled, and an export carries the channel marks. P2, the signed index,
stays gated on a vendor asking.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

Two things the RFC does not settle, found while planning and decided before any
code.

§9 names `--allow-older` and `rollback` as the answers when a vendor withdraws a
release. Neither touches a channel mark, so a machine that fetched the withdrawn
1.4.0 would go back and then refuse every 1.3.x patch the channel serves, until
the vendor publishes something above 1.4.0.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-02T16:56:00Z
attempt: 1
claim: after a deliberate move below a channel mark, the follower would refuse every later release below that mark, so the answers §9 names leave the channel stuck
evidence: rfcs/0037-freshness-of-a-signed-release.md:322-325
action: decided
proposal: ASSUMED — `update --allow-older` and `rollback`, when they install a release below the followed channel's mark, lower that mark to the release they installed. Both are an operator's deliberate act, which nothing on the channel can trigger, and the follower resumes from there. Other channels' marks are left alone.
```

The RFC puts the marks "in the manager's state beside the update candidate" and
names the file, but not whether the state port carries them.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-02T16:56:00Z
attempt: 1
claim: the RFC names channel-marks.json beside the update candidate and leaves open whether the state port reads and writes it
evidence: rfcs/0037-freshness-of-a-signed-release.md:153-159
action: decided
proposal: ASSUMED — the channel marks are a file under the manager directory that the lifecycle layer reads and writes atomically, as the notification outbox is, rather than three more methods on the state port, whose only other implementation is a test fake.
```

Found while building.

The RFC says `update` refuses a release older than the one installed, and not
how the refusal exits.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-02T17:05:00Z
attempt: 1
claim: the installed floor's refusal has no exit code in the RFC, and the channel's existing refusal of an older release is a validation error, exit 2
evidence: internal/lifecycle/ops/update.go:407
action: decided
proposal: ASSUMED — `update` refuses a release older than the one installed at exit 9, `incompatible`, which the exit-code reference defines as a release that cannot be installed over what is running, as `upgrade_from` refuses. The channel's refusals keep exit 2, since they are a poll's outcome rather than an install.
```

Decision 4 makes the override per run, and `update` has three modes that never
install.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-02T17:05:00Z
attempt: 1
claim: `--allow-older` beside `--check`, `--stage` or `--unattended` would be silently ignored, and the tick has nobody to make the decision decision 4 asks for
evidence: internal/cli/commands.go:309
action: decided
proposal: ASSUMED — `--allow-older` beside `--check`, `--stage` or `--unattended` is a usage error, and staging never installs below a channel's mark. A resumed update keeps the flag from its journal, as it keeps `--generate-secrets=false`.
```

An export is taken when a machine may be failing, and the marks are a file that
can be unreadable.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-02T17:05:00Z
attempt: 1
claim: decision 5 puts the marks in the export, and an unreadable marks file would refuse the export at the moment a recovery depends on it
evidence: internal/lifecycle/ops/recovery.go:244-252
action: decided
proposal: ASSUMED — an export whose marks cannot be read is written without them, with a warning, rather than refused. An import writes the marks the export carries, and removes them again if it is compensated.
```
