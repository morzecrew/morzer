---
title: Exit codes
icon: lucide/circle-alert
summary: The stable exit-code table, the machine-readable error codes, and what each one means
---

# Exit codes

Stable, and depended on by systemd units, CI pipelines and operator scripts.
Codes are never renamed or repurposed once published.

The mapping happens in exactly one place — `domain.ExitCode` — so a new error
type cannot quietly acquire a new exit code by being handled somewhere else.

| Code | Error code | Meaning |
| ---: | --- | --- |
| 0 | — | Success. |
| 1 | `internal` | Internal or unexpected error. The manager itself is at fault. |
| 2 | `usage` | Usage or input validation. A mistyped flag, an unknown subcommand, an invalid manifest. |
| 3 | `preflight` | A precondition failed: a missing tool, a version below what the release requires, not enough disk. |
| 4 | `locked` | The deployment lock is held by another operation. Retry, or pass `--wait`. |
| 5 | `installation` | The installation is missing or corrupted. |
| 6 | `secrets` | A secrets error: the state could not be decrypted, a required secret is absent. |
| 7 | `runtime` | The container runtime failed — `docker` or `docker compose`. |
| 8 | `health` | A health check or the release's smoke test failed. |
| 9 | `incompatible` | The release cannot be installed over, or rolled back to, what is running — or this manager cannot read what it was handed: state written by a newer manager, or an export from an installation whose runtime this build does not drive. |
| 10 | `backup` | A backup or restore failed. |
| 11 | `compensated` | The operation failed and compensation succeeded. The system is back where it started. |
| 12 | `manual-intervention` | The operation mutated something it could not undo. A human has to look. |
| 130 | `interrupted` | Interrupted by a signal. |

## 12 is the one that matters

Exit 12 means an operation changed something no automatic action can put back —
a migration that ran, a restore that got halfway. It is not a louder version of
11: 11 says the system is where you left it, and 12 says it is not.

The systemd unit sets `RestartPreventExitStatus=12` so a unit that hits it does
not spin. The failed operation keeps surfacing in `status` and `doctor` until an
operator clears it with `morzer status --clear-intervention`.

Steps declare this explicitly rather than having it inferred from the absence of
a compensator. Most steps without one are simply read-only, and inferring would
flag every failed health check for human acknowledgement — which trains people
to clear the flag without looking, destroying the value of the one signal meant
to stop them.

## Error codes in output

Every failure carries a machine-readable `code` (the middle column above)
alongside a human `message` and a `hint`. Under `--json` they are fields of the
single object on stdout:

```json
{
  "ok": false,
  "exit_code": 3,
  "error": {
    "code": "preflight",
    "category": "user",
    "message": "docker 23.0.1 is older than the 24 this release requires",
    "hint": "upgrade docker, or install a release with a lower requirement"
  }
}
```

Match on `code`, not on `message`. Messages are written for people and will be
reworded; codes are a contract.

## Categories

`category` groups a failure by who can act on it. It carries no control-flow
meaning — it exists so a dashboard can separate "the operator mistyped
something" from "the manager has a bug".

| Category | Meaning |
| --- | --- |
| `user` | The operator can fix this directly. |
| `system` | The machine or an external tool is at fault. |
| `bug` | The manager itself is at fault. |
| `conflict` | Another actor holds the resource. |

## Every error carries a hint

`message` says what happened; `hint` says what to do about it. Both are printed.
An error without its remedy is a support ticket.

Neither may contain a secret value — callers redact before constructing, and the
`domain.Secret` type cannot be formatted into one by accident.
