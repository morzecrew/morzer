# prometheus-metrics · RFC 0047 P1, exporting status and doctor to Prometheus

On branch `prometheus-metrics` from `abbb246`, building
[RFC 0047](../rfcs/0047-exporting-status-and-doctor-to-prometheus.md) P1 as
ruled by the author on 2026-11-03, with decision 19 added on 2026-11-04.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## Plan

- `internal/domain`: `Policy.MetricsTextfile`, schema 16 (decision 17), and
  the setting's validation. `internal/infra/state`: the 15 → 16 migration.
- `internal/lifecycle/ops/metrics.go`, new: collect what `status` collects,
  keeping "could not ask" apart (decision 9), render the text format
  (§5.4, decisions 7, 12), and write a file atomically (decision 8).
- `status.go`: the runtime read split out so `status` and the metrics run
  share it. `list.go` and the fleet row count a completed on-demand job the
  way the metrics do (decision 19).
- `settings.go`: `metrics.textfile`; the metrics timer and the doctor timer's
  wider condition (decisions 2, 3); retiring the files when the setting
  changes or the doctor timer goes (decisions 16, 18).
- `doctor.go`: the scheduled run writes the doctor file (decisions 3, 11);
  the `metrics.textfile` check (decision 10). `remove.go`: the files go with
  the installation (decision 16).
- `systemd` and the fake supervisor: the metrics pair, its unit carrying the
  directory (decision 15).
- CLI `metrics [--textfile <dir>]`; the monitoring page, the command and
  settings references, the changelog.

Found while planning, and decided before any code.

The RFC names the setting and the schema bump, not the field.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-04T18:20:00Z
attempt: 1
claim: the RFC names `metrics.textfile` and moves the schema for it, without saying where in the installation it is recorded
evidence: internal/domain/installation.go:799-809
action: decided
proposal: ASSUMED — `metrics.textfile` is recorded as `policy.metrics_textfile`, beside the doctor's schedule and switch, which are the other settings that decide a timer.
```

The directory is written into the unit's `ExecStart=` line unquoted, as
every path there is.

```divergence
decision: 10
grade: ASSUMED
class: spec-gap
at: 2026-11-04T18:20:00Z
attempt: 1
claim: an absolute existing directory can still hold a space, which splits ExecStart's arguments, or a `%`, which systemd reads as a specifier
evidence: internal/adapters/supervisor/systemd/systemd.go:699
action: departed
proposal: ASSUMED — as decision 10, and the path may hold no whitespace, quote, backslash or `%`, so that the unit runs the command with the directory it was given.
```

§5.4 says `morzer_service_running` counts running containers, without saying
whether an unhealthy one counts.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-04T18:20:00Z
attempt: 1
claim: "running" has two readings, the runtime's state word and the manager's own `Running()`, which also requires a container not to be unhealthy
evidence: internal/ports/runtime.go:367-369
action: decided
proposal: ASSUMED — `morzer_service_running` counts containers that `Running()` accepts, running and not unhealthy, as `ls --status` and the fleet row count them, so an unhealthy service alerts as not running.
```

§5.4's last-operation families do not say which records count.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-04T18:20:00Z
attempt: 1
claim: the journal holds records that are still running and records of every status, and the RFC says "the last one of each type finished" without saying what success is
evidence: internal/domain/operation.go:79-87
action: decided
proposal: ASSUMED — for each operation type in the journal, the newest record whose status is terminal; its finish time is the timestamp, and success is 1 only for `succeeded`, so a compensated or interrupted operation reads 0.
```

`morzer metrics` without `--textfile` prints to stdout, and the RFC says what
a state that does not load writes, not how the command exits.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-04T18:20:00Z
attempt: 1
claim: a run whose state does not load has produced the answer it exists to produce, `morzer_up 0`, and a failed unit would say the same thing a second time in another place
evidence: rfcs/0047-exporting-status-and-doctor-to-prometheus.md:224-226
action: decided
proposal: ASSUMED — a state that does not load is printed or written as `morzer_up 0`, its reason goes to the log, and the command exits 0. A file that cannot be written is an error, and the unit fails.
```

Found while building.

Decision 6 labels a sandbox `mode="sandbox"`. The installation records a
sandbox as mode `dev`, and `ls`, `status --json` and the fleet row all say
`dev`.

```divergence
decision: 6
grade: ASSUMED
class: discovery
at: 2026-11-04T18:29:00Z
attempt: 1
claim: there is no `sandbox` mode to export; a sandbox is `dev` everywhere else a mode is read
evidence: internal/domain/installation.go:198
action: departed
proposal: ASSUMED — `morzer_release_info`'s `mode` is the installation's own word, `dev` on a sandbox, and `production` where the installation records none, so a rule matches the value every other surface shows.
```

Decision 10 warns about a stale file "while the timer is installed". `doctor`
cannot tell an installed timer from one a host without systemd never had, and
a file nothing writes is the same fault either way.

```divergence
decision: 10
grade: ASSUMED
class: discovery
at: 2026-11-04T18:29:00Z
attempt: 1
claim: whether the timer is installed does not change what an operator must do about a file nothing writes, and asking the supervisor adds a way for the check itself to fail
evidence: internal/lifecycle/ops/metrics.go:236-270
action: departed
proposal: ASSUMED — with `metrics.textfile` set, `doctor` warns when the status file is missing or more than ten minutes old, whether or not a timer is installed, and names the timer to check.
```

From #265's review.

Retiring the old files in `config set` raced two writers: a metrics run
already under way with the old directory, which renames its file back after
the removal, and a unit refresh that fails after the state is saved, which
leaves the old unit writing the old directory and a retry with nothing to
retire.

```divergence
decision: 16
grade: ASSUMED
class: discovery
at: 2026-11-04T18:54:00Z
attempt: 2
claim: a retirement done once, by `config set`, cannot see a writer that started before it or a unit that outlived it
evidence: internal/lifecycle/ops/metrics.go:61-75
action: departed
proposal: ASSUMED — as decision 16, and a metrics run writes its file only while the installation's setting still names its directory, read again just before the write. A run whose directory is no longer the setting's removes its file instead, so an old unit or an old run retires what it wrote.
```

Decision 10 checks the directory is writable, and `$` passes the path
validation although systemd expands it on an `ExecStart=` line.

```divergence
decision: 10
grade: ASSUMED
class: discovery
at: 2026-11-04T18:54:00Z
attempt: 2
claim: access(2) answers for the user running `doctor`, not for the root the units run as, and a `$` in the directory reaches the command as something else
evidence: internal/lifecycle/ops/metrics.go:253
action: departed
proposal: ASSUMED — the writability warning is given only when `doctor` runs as root, as the units do; otherwise the staleness warning covers a directory the timer cannot write. The path may hold no `$` either.
```
