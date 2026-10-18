# Notify target commands · notification targets are set by a command

From the ERP bundle's requests after 0.4.0 (item M4), on branch
`notify-target-commands` from `f927b4c` (v0.4.0). The notifications page
told operators to configure targets in `installation.yaml`, which is a report
the manager rewrites, and nothing wrote `notify.targets` except `init
--repair` keeping existing ones and export/import. The request asked for
`config set notify.targets+=<url>` or `notify add|remove`; RFC 0015 decision
2 says notification configuration mirrors backup targets, and those change
through `morzer backup target add|remove|list`, so this adds `morzer notify
target add|remove|list`.

RFC 0015 carries no grades, so its rows are treated as `LOCKED`. Nothing
here contradicts one.

## Plan

| File | Change | Governing row |
|---|---|---|
| `internal/lifecycle/ops/notify_targets.go` | new: `NotifyTargetAdd`, `NotifyTargetRemove`, `NotifyTargetList`, and `ResolveNotifyTarget`, which reads a target's secrets and refuses what a notifier would drop | 2, 3, 10, 11 |
| `internal/cli/notify.go` | the wiring builds each notifier from `ResolveNotifyTarget`, so the command and the wiring refuse the same targets | 2, 12 |
| `internal/cli/notify_target.go` | new: `morzer notify target add [url] --name --url-secret --credentials --min-level`, `remove <url\|name>`, `list` | 2, 10, 11 |
| `internal/cli/root.go` | register `notify` | — |
| `internal/lifecycle/ops/status.go`, `internal/ui/views/status.go` | `status` names the targets, never a URL read from a secret | 10 |
| `internal/lifecycle/ops/doctor.go` | a check that every target resolves, when any is configured | 10, 12 |
| `internal/ui/views/lists.go` | the list view | — |
| `pages/docs/operating/notifications.md`, reference pages | configure with the commands | — |
| `CHANGELOG.md` | Added | — |

No installation schema bump: `notify.targets` already exists in the state
(schema 4, decision 9) and the commands write that field and no new one, so an
older manager reads what they write.

Decisions the RFC does not settle, each decided below: whether `add` probes
the endpoint, how `remove` names a target, what counts as a duplicate, and
what `doctor` says about a target that would be dropped. None is load-bearing
on another RFC.

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
at: 2026-10-17T20:53:05Z
attempt: 1
claim: RFC 0015 configured targets in installation.yaml and named no command, so once the file became a report nothing could set them; decision 2's mirror of backup targets is a subcommand, morzer notify target add|remove|list
evidence: internal/cli/backup_target.go:20
action: decided
proposal: ASSUMED — RFC 0015 decision 13: notification targets change through `morzer notify target add|remove|list`, written under the deployment lock without an engine operation, as backup targets and trusted keys are.
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-17T20:53:05Z
attempt: 1
claim: `backup target add` lists the target before recording it; a webhook has no read-only request, and a probe would be a delivery the receiver acts on, of a kind outside decision 4's allowlist. `add` instead resolves the target as the wiring will, refusing http (decision 3), a credential in a plain url, a named secret that is not set, and a credential document without a header and value
evidence: internal/cli/notify.go:46
action: decided
proposal: ASSUMED — `notify target add` sends nothing; it refuses any target the wiring would drop, and `doctor` reports one that stops resolving later.
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-17T20:53:05Z
attempt: 1
claim: a target using url_secret has no URL that may be printed (decision 10), so `remove` takes either the url or the name; `add` refuses a second target with the same url, the same name, or the same url_secret, since two targets reading one endpoint deliver every event twice
evidence: internal/domain/installation.go:558
action: decided
proposal: ASSUMED — a notify target is named by its name, or by its url when it has none; both are unique within an installation.
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-17T20:53:05Z
attempt: 1
claim: a target that does not resolve is dropped at wiring with a log line (decision 12 keeps the notifier nil), so a deleted secret silences notifications with nothing to show for it; `doctor` gains a warning naming each such target. A warning rather than a failure: notification never changes an operation's outcome, and a failing check would turn a monitor watching doctor's exit code red over the alerting rather than the deployment
evidence: internal/cli/notify.go:58
action: decided
proposal: ASSUMED — `doctor` warns about a notify target that would be dropped, naming it by its label and never by a URL read from a secret.
```

## Review: three ways a target still went wrong

Raised by CodeRabbit and CodeAnt on #112, each reproduced by a failing test
before the fix:

- **A password in a hand-written URL reached output through `Label()`.** `list`,
  `status` and `doctor` wrapped the label in `ports.RedactRefCredentials`, but
  `remove`'s summaries, `add`'s refusals and `Validate`'s `min_level` refusal
  did not. The redaction now happens inside `Label()`, once, and the wraps are
  gone. `remove`'s "not a notify target" refusal echoes what was typed, which
  may be such a URL, so it is redacted too. The redactor moved to
  `domain.RedactURLCredentials` so the domain type can use it.
  `ports.RedactRefCredentials` calls it.
- **A credential document whose header net/http refuses was accepted.** Every
  delivery then failed and was dropped, which is the silent channel the empty
  check exists to stop. `ResolveNotifyTarget` now refuses a header name or value
  that `httpguts` rejects, which is net/http's own test. `golang.org/x/net` was
  already a dependency and is now a direct one, at the same version.
