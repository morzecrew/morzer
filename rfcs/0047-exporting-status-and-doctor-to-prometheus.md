# RFC 0047 — Exporting status and doctor to Prometheus

- **Status:** ✅ Complete — proposed, and ruled by the author 2026-11-03: §11's
  `OPEN` rows as recommended. **P1 built 2026-11-04** in #265; the entries its
  build and review wrote in [logs/prometheus-metrics.md](../logs/prometheus-metrics.md)
  are decisions 20–27
- **Scope:** How a machine that already runs Prometheus's node exporter learns
  what `morzer status` and `morzer doctor` know, without anybody running them.
  It adds a command, `morzer metrics`, that prints an installation's state in
  the Prometheus text format. It adds one per-installation setting,
  `metrics.textfile`, that names the node exporter's textfile directory, and a
  timer that writes the status there every minute. And it has the scheduled
  `doctor` write its check results there too. Not covered: an HTTP endpoint
  (nothing here listens), pushing anywhere, per-container resource usage
  (cAdvisor and the node exporter already measure that), and logs. The new
  setting is stored in the installation at schema 16: an older manager
  rewriting the state would drop it and the timer with it, so the number moves
  for the write path, as it did for `policy.backup_schedule` and the doctor
  settings. The metric names and labels become a published contract.
- **Related:** [0043](0043-notifications-that-survive-a-failed-delivery-and-a-scheduled-doctor.md)
  (the scheduled `doctor` and its timer);
  [0026](0026-fleet-as-a-read-model.md) (the fleet row,
  the nearest thing to this: a periodic summary of an installation, with an
  allowlist of what leaves the machine);
  [`internal/lifecycle/ops/status.go`](../internal/lifecycle/ops/status.go)
  (`GetStatus`); [`internal/lifecycle/ops/doctor.go`](../internal/lifecycle/ops/doctor.go)
  (`DoctorUnattended`); [`internal/lifecycle/ops/fleet_row.go`](../internal/lifecycle/ops/fleet_row.go);
  [`internal/adapters/supervisor/systemd/systemd.go`](../internal/adapters/supervisor/systemd/systemd.go)
  (the unit set and its timers); the node exporter's
  [textfile collector](https://github.com/prometheus/node_exporter#textfile-collector).

---

## 1. Summary

An operator who runs Prometheus has no way to alert on a morzer installation
except by wrapping `doctor`'s exit code in a script. This RFC proposes the
textfile route that the node exporter already offers to batch jobs and cron
scripts. With `metrics.textfile` set, a timer runs `morzer metrics` every
minute and writes `morzer-status-<product>.prom` into that directory. The file
carries the release, the services and their health, the stop, the operations
that need a human, the last outcome of each kind of operation, the last
backup and a staged update. The scheduled `doctor` writes
`morzer-doctor-<product>.prom` with one series per check. Nothing listens, and
the export sends nothing off the machine. The doctor timer it installs
contacts what every `doctor` run contacts: the release's image registry
(`network.registry`, a manifest inspect), its health endpoints, and with
consent the vendor's channel and GitHub. Every label value is either the manager's own
vocabulary or a name the manifest validated.

## 2. Motivation

The gap report lists it under operating: no metrics, no Prometheus, no log
export. Monitoring means `doctor`'s exit code.

- **The machines this runs on are often already monitored.** A self-hosting
  customer's operations team runs Prometheus and the node exporter on every
  host. A morzer installation is invisible to it: a stopped service, a backup
  that last succeeded nine days ago, or an update waiting for approval shows up
  only when somebody runs `status`.
- **Notifications cover events, not states.** RFC 0043's targets hear about an
  operation that failed and about the scheduled `doctor`'s findings, once a
  day. Neither says "this has been wrong for six hours", which is what an alert
  rule with a `for:` clause is for.
- **Distr and WUD export metrics**, the two tools the gap report compares
  against. An operator choosing between them and morzer asks.

## 3. Current state

Verified against `main` at dc302be.

- **`status`** (`GetStatus`) reads the installation's state, asks the runtime
  for its services, and runs the release's health probes once each, inside a
  bounded context. Its JSON is the richest machine-readable view of an
  installation, and it is what a scraper would have to call today.
- **`doctor`** runs the preflight checks plus the machine-level ones and
  returns `events.CheckResult` values: an id, a category, a status of
  ok/warn/fail, a message and a remedy. Some checks contact the network:
  `update.check` asks the vendor's registry under consent, and
  `manager.version` asks GitHub under `manager.check`.
- **The scheduled `doctor`** (RFC 0043) runs `doctor --unattended` from
  `<product>-doctor.timer`, by default at 06:00 each day
  (`DefaultDoctorSchedule`). The timer exists only while the installation has
  a notify target and `doctor.scheduled` is not false (`DoctorTimer:
  inst.Notify.HasTargets() && !inst.Policy.SkipScheduledDoctor`).
- **The fleet row** (RFC 0026) is a periodic summary of one installation:
  version, manager version, service counts, attention count, a drift count and
  the last operation. It is published to object storage by
  `<product>-fleet.timer`, with an allowlist that keeps parameter values,
  hostnames, logs and configuration out of it.
- **Every timer is a pair in the unit set** that
  `systemd.UnitNames(product)` lists, gated by a field of `UnitParams` and
  reconciled by `apply`, `config set` and `init --repair`.
- **Nothing writes the Prometheus text format**, and nothing in the module
  depends on a Prometheus client library.

## 4. Goals / Non-goals

**Goals**

- An operator who runs the node exporter can alert on any state `status`
  shows and on any check `doctor` runs, with one setting.
- A script can read the same metrics without the node exporter, from
  `morzer metrics`.
- The file never carries free text, secrets or anything RFC 0026's allowlist
  keeps out of a fleet row.

**Non-goals**

- **A metrics endpoint.** The manager is a command, not a daemon, and an HTTP
  listener would be the first thing on these machines that accepts
  connections. The textfile collector exists for exactly this kind of program.
- **Container resource usage.** `morzer stats` shows it; cAdvisor and the node
  exporter already export it, labelled by container.
- **Log export.** A different problem, with its own tools.

## 5. Design

### 5.1 The command and the file

```sh
morzer metrics                                   # the status metrics, Prometheus text, on stdout
morzer metrics --textfile /var/lib/node-exporter # write them into that directory, atomically
morzer config set metrics.textfile=/var/lib/prometheus/node-exporter
```

`morzer metrics` builds what `status` builds: the state, the runtime's
services, and one round of health probes. It renders that as the Prometheus
text exposition format, version 0.0.4, with `# HELP` and `# TYPE` for each
family. It writes no state and takes no lock. It is an `installationScope`
command, so `--product` chooses among several installations.

`--textfile <dir>` writes the same text to `<dir>/morzer-status-<product>.prom`.
The role comes before the product so that no two products' files share a name:
with the product first, `foo`'s doctor file and the status file of a product
named `foo-doctor` would both be `morzer-foo-doctor.prom`.
It creates a temporary file in that directory under a name that does not end
in `.prom`, so the collector skips it, with mode 0644 so the node exporter's
user can read it, and renames it into place. A crash leaves the previous file,
never half of one.

`metrics.textfile` is an absolute path to an existing directory. The manager
does not create it, since the node exporter decides where it reads, and a
directory nobody reads would be a quiet failure. Unset, which is the default,
means no timer and no file. *(2026-11-04: nor may it hold whitespace, a quote,
a backslash, `%` or `$`; see decision 21.)*

### 5.2 The timer

With `metrics.textfile` set, the unit set gains `<product>-metrics.service`
and `<product>-metrics.timer`. The service runs
`morzer metrics --textfile <dir> --plain --log-format json`, with the directory
the setting named when the unit was written, and the timer fires every minute
(`OnCalendar=*:*:00`). The unit carries the directory so that a run whose
state will not load still knows where to say so. The pair is reconciled like every other
timer: installed by `config set metrics.textfile=…`, removed by
`config unset`, and repaired by `init --repair`. Changing or unsetting the
setting removes both files from the directory it named before, and
`installation remove` removes them too, so a scrape never reads a file nothing
writes any more. *(2026-11-04: and a run writes only while the setting still
names its directory, removing its file otherwise; see decision 27.)*

A run that cannot write the file fails, which the journal records, and leaves
the last file in place. The node exporter's own
`node_textfile_mtime_seconds` for the file then stops advancing, and that is
the staleness signal to alert on. Its `file` label is the file's name in older
node exporter releases and its full path in newer ones, so the example rule
matches it with `file=~"(.*/)?morzer-status-<product>.prom"`. `doctor` warns, as
`metrics.textfile`, when the directory is missing or not writable, or when the
file is older than ten minutes while the timer is installed. *(2026-11-04:
whether or not a timer is installed, and the writability only as root; see
decision 26.)*

### 5.3 The doctor file

While `metrics.textfile` is set, the scheduled `doctor` also writes
`morzer-doctor-<product>.prom`, the same way. The doctor timer is installed
when the installation has a notify target **or** `metrics.textfile` is set, and
`doctor.scheduled=false` still turns it off, and turning it off, or unsetting
`metrics.textfile`, removes the doctor file, so a frozen result never reads as
a current one. Its schedule stays
`doctor.schedule`, daily by default. An operator who wants checks more often
sets that. On a machine with no notify target, setting `metrics.textfile` is
what starts the daily `doctor`, and with it the contact the summary lists.

It takes the directory from the setting, since `doctor` reads the state
anyway. A state that does not load leaves the doctor file as it was, and the
status file's `morzer_up 0` says why.

Only the scheduled run writes the file. An operator's `doctor` at a terminal
may run as a user who cannot write the directory, or with flags that narrow
what it checks, and the file should say what the timer found.

### 5.4 The metrics

Every series carries `product`. Every other label value comes from a closed
vocabulary (operation types, check ids and categories, `mode`) or is a name the
manifest validated (services and health checks).

```text
# morzer-status-<product>.prom, every minute
morzer_up{product}                                   1 when the installation's state was read
morzer_release_info{product,version,mode}            1; version is the installed release
morzer_manager_info{product,version}                 1
morzer_runtime_up{product}                           1 when the runtime answered
morzer_service_running{product,service}              running containers of the service (decision 22)
morzer_service_containers{product,service}           containers of the service
morzer_health_check_ok{product,check}                1/0, the release's health probes
morzer_stopped{product}                              1 while stopped by `morzer stop`
morzer_operations_needing_attention{product}         count
morzer_last_operation_timestamp_seconds{product,type}  when the last one of each type finished
morzer_last_operation_success{product,type}          1/0 for that same operation
morzer_last_backup_timestamp_seconds{product}        the newest backup
morzer_update_staged{product}                        1 while a release waits for approval

# morzer-doctor-<product>.prom, each scheduled doctor run
morzer_doctor_check{product,check,category}          0 ok, 1 warn, 2 fail
morzer_doctor_last_run_timestamp_seconds{product}
```

An installation with no backup yet, or no finished operation of a type, has no
series for it, never a timestamp of zero that a rule would read as 1970. A
runtime that does not answer, or whose configuration for this release cannot
be built, is `morzer_runtime_up 0`, with no service and no health series, never
a set of zeros. When nothing is installed, or nothing has been applied yet,
`status` does not ask the runtime, and `morzer_runtime_up` is absent with the
rest of the runtime's series, since there is nothing for them to describe. *(2026-11-04: a completed job
behind an inactive profile exports no service series; see decision 19.)* RFC 0026 learned that lesson about the
fleet row: a count of zero and "could not look" have to be told apart. A state
that does not load writes `morzer_up 0` and nothing else, into the directory
the unit names, so the file still replaces the last good one rather than
leaving it to read as current.

The example rules on the reference page are the alerts these exist for: a
service not running for five minutes, a health check failing, a backup older
than a day and a half, an operation needing attention, a doctor check at fail,
and the file itself going stale.

### 5.5 Alternatives considered

- **An HTTP exporter, `morzer metrics serve`.** The usual shape for an
  exporter, and the wrong one for a program with no daemon. It would be the
  first listening socket morzer adds to a host, with its own unit, its own
  port and its own access control.
- **Run `doctor` every minute instead of a separate status run.** One file and
  one timer, but `manager.version` would ask GitHub sixty times an hour per
  consenting installation, which is the unauthenticated limit for the whole
  address (RFC 0046 §9). Every network check would run at a scrape's cadence.
- **Write the file at the end of each operation as well.** Fresher after a
  backup, but it puts a write to another tool's directory into every
  operation's failure surface. A minute is fresh enough for any rule with a
  `for:` clause.
- **A Prometheus client library.** The text format is a page of grammar,
  and the families here are gauges. A dependency for rendering them would cost
  more than the renderer.
- **The doctor check state as a label**, one series per state with value 1. It
  is the Prometheus enum convention, and it triples the series and turns "any
  check failing" into a selector rather than a comparison. Decision 4 is this
  choice.

## 6. Tests

- The renderer against golden files: every family, a runtime that did not
  answer, a state that did not load, a sandbox, label values that need
  escaping. The output is checked with the Prometheus text parser from
  `github.com/prometheus/common/expfmt` as a test-only dependency, so a
  malformed file fails a test and not a scrape.
- Two products, `foo` and `foo-doctor`, in one directory write four
  distinct files.
- A state that does not load writes `morzer_up 0` into the directory the flag
  names. Unsetting or changing the setting removes both files.
- `--textfile`: the write is atomic, no file ending `.prom` exists before the
  rename, the mode is 0644, an unwritable directory fails and leaves the
  previous file, and a symlink placed at the final name is replaced, not
  written through.
- The unit set: the metrics pair exists exactly while `metrics.textfile` is
  set, and the doctor pair exists with a notify target or the setting, and
  never with `doctor.scheduled=false`, which removes the doctor file.
- The scheduled `doctor` writes the doctor file, and an operator's `doctor`
  does not.
- `doctor`'s `metrics.textfile` check: unset, healthy, a missing directory and
  a stale file.

## 7. Docs

- `operating/monitoring-with-prometheus.md`, new: the setting, where common
  distributions put the textfile directory, the metric reference, example
  alert rules, and the staleness rule.
- The command reference: `metrics`. The settings reference:
  `metrics.textfile`. The unit list: the metrics pair.

## 8. Out of scope

- **Fleet-wide metrics.** Each machine exports its own installations; a
  Prometheus server already aggregates.
- **Histograms of operation duration.** Each operation is a rare event, and
  the journal holds its duration. They become worth having if anybody alerts
  on a slow update.
- **OpenMetrics.** The node exporter reads the text format, and nothing here
  needs exemplars or created timestamps.
- **A configurable file name or extra labels.** Relabelling in the scrape
  config does that, where the operator already manages labels.

## 9. Risks

- **Cardinality.** Series per installation scale with its services and health
  checks, a few dozen at most. No label carries a version except the two
  `_info` metrics, no label carries an id, and nothing carries a time.
- **Writing as root into another tool's directory.** The textfile directory
  is commonly root-owned, but if a less privileged user can write it, they can
  place a symlink at the final name. The write creates its temporary file
  exclusively and renames it over the final name, which replaces a symlink
  rather than following it.
- **The metric names become a contract.** Dashboards and alert rules depend
  on them. A rename is a new name, with the old one kept for a minor release
  and marked deprecated in its `# HELP`.
- **Health probes every minute.** `status` runs each probe once with a bound,
  and a vendor whose health endpoint is expensive now has it called once a
  minute. The probes are the release's own, and the reference page says so.

## 10. Unresolved questions

The `OPEN` rows in §11 are the author's to rule. Nothing else blocks the design.

*(2026-11-03: ruled. Every `OPEN` row went as recommended, and §11 records each
ruling.)*

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `ASSUMED` | **The surface.** Recommended: a command, `morzer metrics`, printing the Prometheus text format, and a per-installation setting, `metrics.textfile`, naming the node exporter's textfile directory, which a timer writes `morzer-status-<product>.prom` into. Nothing listens. Alternative: an HTTP exporter, `morzer metrics serve`, with its own unit and port (§5.5). Was `OPEN`; ruled by the author 2026-11-03: as recommended. |
| 2 | `ASSUMED` | **The cadence and cost of the status file.** Recommended: a new `<product>-metrics.timer` every minute, running what `status` runs: state, the runtime, one round of health probes, and no `doctor`. Alternative: write the file only from the timers and operations that already run, with no new timer, so it can be a day old. Was `OPEN`; ruled by the author 2026-11-03: as recommended. |
| 3 | `ASSUMED` | **Exporting `doctor`.** Recommended: the scheduled `doctor` writes `morzer-doctor-<product>.prom`, and its timer is installed while `metrics.textfile` is set as well as while a notify target is, still at `doctor.schedule`. Alternative: leave `doctor` out, and export status only. Was `OPEN`; ruled by the author 2026-11-03: as recommended. |
| 4 | `ASSUMED` | **How a check's state is encoded.** Recommended: one series per check, `morzer_doctor_check{product,check,category}`, valued 0 ok, 1 warn, 2 fail, so "anything failing" is `== 2`. Alternative: the Prometheus enum convention, one series per check and state, valued 1 for the current state. Was `OPEN`; ruled by the author 2026-11-03: as recommended. |
| 5 | `ASSUMED` | **The identifying labels.** Recommended: `product` on every series and no installation id, since a product is unique on its machine and the instance label says which machine. Alternative: `installation_id` as well, which survives a product being re-initialised but adds a label no rule needs. Was `OPEN`; ruled by the author 2026-11-03: as recommended. |
| 6 | `ASSUMED` | Off by default. It needs no consent, because the export sends nothing off the machine; whoever scrapes the node exporter already reads the host. The doctor timer it installs (decision 3) contacts what any `doctor` run contacts: the release's image registry and health endpoints, and with consent the vendor's channel and GitHub. A sandbox exports too, with `mode="sandbox"` on `morzer_release_info`. |
| 7 | `ASSUMED` | No free text is exported: no check messages or remedies, no hostnames, no parameter values, no URLs. Label values are the manager's own vocabulary, check ids and categories, or service and health-check names the manifest validated. Every label value is escaped per the text format all the same. |
| 8 | `ASSUMED` | The file is written to a temporary name not ending in `.prom`, created exclusively in the same directory with mode 0644, and renamed into place. A failed write leaves the previous file, and the node exporter's `node_textfile_mtime_seconds` is the staleness signal; morzer exports no timestamp of its own for the status file. |
| 9 | `ASSUMED` | A runtime that does not answer, or whose configuration cannot be built, is `morzer_runtime_up 0` with no service or health series; with nothing installed or nothing applied yet, the runtime is not asked and its series, `morzer_runtime_up` included, are absent. A state that does not load writes `morzer_up 0` alone, into the directory the unit carries, since the setting is in the state that did not load. An installation with no backup, or no finished operation of a type, has no series for it. None of these is a zero. |
| 10 | `ASSUMED` | `metrics.textfile` must be an absolute path to an existing directory; the manager does not create it. `doctor` checks it as `metrics.textfile`: missing or unwritable, or the file older than ten minutes while the timer is installed, warns. |
| 11 | `ASSUMED` | Only the scheduled `doctor` writes the doctor file; an operator's `doctor` does not. |
| 12 | `ASSUMED` | The renderer is written here, without a Prometheus client library; the text parser from `prometheus/common` checks its output in tests only. |
| 13 | `ASSUMED` | The metric names and labels in §5.4 are a contract. A rename adds the new name and keeps the old one for a minor release, marked deprecated in its `# HELP`. |
| 14 | `ASSUMED` | The files are `morzer-status-<product>.prom` and `morzer-doctor-<product>.prom`, role first, so no product's file can carry another product's name. |
| 15 | `ASSUMED` | The metrics unit runs `morzer metrics --textfile <dir>` with the directory written into it, and the command takes the directory from that flag rather than from the state. |
| 16 | `ASSUMED` | Changing or unsetting `metrics.textfile` removes both files from the directory it named before; `installation remove` removes them as well. |
| 17 | `ASSUMED` | `metrics.textfile` moves the installation schema to 16 for the write path, as 7, 8 and 15 did: an older manager rewriting the state would drop the field and remove its timer, and the bump makes it refuse the state instead. |
| 18 | `ASSUMED` | Turning the doctor timer off with `doctor.scheduled=false` removes the doctor file, as unsetting `metrics.textfile` does. |
| 19 | `ASSUMED` | *(2026-11-04, ruled by the author)* A container of a service the configuration does not start, one behind an inactive profile, that exited 0 is a completed job, such as a dependency a task or a migration started. The status file exports no `morzer_service_running` or `morzer_service_containers` series for it, and `ls --status` and the fleet row leave it out of their counts the same way, so the three agree. A service the configuration starts that exited 0 still counts as not running, since a `docker stop` ends one that way too. One-shot runs remove such containers once they finish (see [logs/task-cleanup.md](../logs/task-cleanup.md)), so this covers a removal that failed and containers an older manager left. |
| 20 | `ASSUMED` | *(2026-11-04, from the build; accepted 2026-11-04)* `metrics.textfile` is recorded as `policy.metrics_textfile`, beside the doctor's schedule and switch, which are the other settings that decide a timer. See [logs/prometheus-metrics.md](../logs/prometheus-metrics.md) (unlisted, attempt 1, 18:20, the field). |
| 21 | `ASSUMED` | *(2026-11-04, from the build and the review; accepted 2026-11-04)* **Refines decision 10.** The directory may hold no whitespace, quote, backslash, `%` or `$`. It is written into the unit's `ExecStart=` line, where systemd reads `%` as a specifier and expands `$`, so each would run the command with a directory other than the one stored. See [logs/prometheus-metrics.md](../logs/prometheus-metrics.md) (decision 10, attempt 1, 18:20, and attempt 2, 18:54). |
| 22 | `ASSUMED` | *(2026-11-04, from the build; accepted 2026-11-04)* `morzer_service_running` counts the containers `Running()` accepts, running and not unhealthy, as `ls --status` and the fleet row count them, so an unhealthy service alerts as not running. See [logs/prometheus-metrics.md](../logs/prometheus-metrics.md) (unlisted, attempt 1, 18:20, running). |
| 23 | `ASSUMED` | *(2026-11-04, from the build; accepted 2026-11-04)* For each operation type in the journal, the last-operation families describe the newest record whose status is terminal. Its finish time is the timestamp, and success is 1 only for `succeeded`, so a compensated or interrupted operation reads 0. See [logs/prometheus-metrics.md](../logs/prometheus-metrics.md) (unlisted, attempt 1, 18:20, the last operation). |
| 24 | `ASSUMED` | *(2026-11-04, from the build; accepted 2026-11-04)* A state that does not load is printed or written as `morzer_up 0`, its reason goes to the log, and `morzer metrics` exits 0. A file that cannot be written is an error, and the unit fails. See [logs/prometheus-metrics.md](../logs/prometheus-metrics.md) (unlisted, attempt 1, 18:20, the exit). |
| 25 | `ASSUMED` | *(2026-11-04, from the build; accepted 2026-11-04)* **Refines decision 6.** `morzer_release_info`'s `mode` is the installation's own word: `dev` on a sandbox, and `production` where the installation records none. A rule then matches the value every other surface shows. See [logs/prometheus-metrics.md](../logs/prometheus-metrics.md) (decision 6, attempt 1, 18:29). |
| 26 | `ASSUMED` | *(2026-11-04, from the build and the review; accepted 2026-11-04)* **Refines decision 10.** With `metrics.textfile` set, `doctor` warns when the status file is missing or more than ten minutes old, whether or not a timer is installed, and it names the timer to check. It warns that the directory is not writable only when it runs as root, as the units do, because `access(2)` answers for the user who asked. See [logs/prometheus-metrics.md](../logs/prometheus-metrics.md) (decision 10, attempt 1, 18:29, and attempt 2, 18:54). |
| 27 | `ASSUMED` | *(2026-11-04, from the review; accepted 2026-11-04)* **Refines decision 16.** A metrics run writes its file only while the installation's setting still names its directory, which it reads again just before the write. A run whose directory is no longer the setting's removes its file there instead, so an old unit or a run under way when the setting changed retires what it wrote. `--textfile` is the timer's flag in practice, and writing anywhere else is a print and a redirect. See [logs/prometheus-metrics.md](../logs/prometheus-metrics.md) (decision 16, attempt 2, 18:54). |

## 12. Phasing

- **P1** (§5.1–§5.4): `morzer metrics`, `metrics.textfile`, the metrics timer,
  the doctor file, `doctor`'s check, and the monitoring page. One PR. It needs
  decisions 1–5. *(2026-11-04: built in #265.)*
