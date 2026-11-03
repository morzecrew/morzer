# RFC 0047 — Exporting status and doctor to Prometheus

- **Status:** 📝 Draft — proposed, **awaiting the author's rulings** on the
  `OPEN` decisions in §11; not scheduled
- **Scope:** How a machine that already runs Prometheus's node exporter learns
  what `morzer status` and `morzer doctor` know, without anybody running them.
  It adds a command, `morzer metrics`, that prints an installation's state in
  the Prometheus text format. It adds one per-installation setting,
  `metrics.textfile`, that names the node exporter's textfile directory, and a
  timer that writes the status there every minute. And it has the scheduled
  `doctor` write its check results there too. Not covered: an HTTP endpoint
  (nothing here listens), pushing anywhere, per-container resource usage
  (cAdvisor and the node exporter already measure that), and logs. The new
  setting is stored in the installation, absent reads as off, and an older
  manager reads past it, so there is no installation schema change. The metric
  names and labels become a published contract.
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
minute and writes `morzer-<product>.prom` into that directory. The file
carries the release, the services and their health, the stop, the operations
that need a human, the last outcome of each kind of operation, the last
backup and a staged update. The scheduled `doctor` writes
`morzer-<product>-doctor.prom` with one series per check. Nothing listens and
nothing leaves the machine. Every label value is either the manager's own
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
morzer metrics                     # the status metrics, Prometheus text, on stdout
morzer metrics --textfile          # write them to metrics.textfile, atomically
morzer config set metrics.textfile=/var/lib/prometheus/node-exporter
```

`morzer metrics` builds what `status` builds: the state, the runtime's
services, and one round of health probes. It renders that as the Prometheus
text exposition format, version 0.0.4, with `# HELP` and `# TYPE` for each
family. It writes no state and takes no lock. It is an `installationScope`
command, so `--product` chooses among several installations.

`--textfile` writes the same text to `<metrics.textfile>/morzer-<product>.prom`.
It creates a temporary file in that directory under a name that does not end
in `.prom`, so the collector skips it, with mode 0644 so the node exporter's
user can read it, and renames it into place. A crash leaves the previous file,
never half of one.

`metrics.textfile` is an absolute path to an existing directory. The manager
does not create it, since the node exporter decides where it reads, and a
directory nobody reads would be a quiet failure. Unset, which is the default,
means no timer and no file.

### 5.2 The timer

With `metrics.textfile` set, the unit set gains `<product>-metrics.service`
and `<product>-metrics.timer`. The service runs
`morzer metrics --textfile --plain --log-format json`, and the timer fires
every minute (`OnCalendar=*:*:00`). The pair is reconciled like every other
timer: installed by `config set metrics.textfile=…`, removed by
`config unset`, and repaired by `init --repair`.

A run that cannot write the file fails, which the journal records, and leaves
the last file in place. The node exporter's own
`node_textfile_mtime_seconds{file="morzer-<product>.prom"}` then stops
advancing, and that is the staleness signal to alert on. `doctor` warns, as
`metrics.textfile`, when the directory is missing or not writable, or when the
file is older than ten minutes while the timer is installed.

### 5.3 The doctor file

While `metrics.textfile` is set, the scheduled `doctor` also writes
`morzer-<product>-doctor.prom`, the same way. The doctor timer is installed
when the installation has a notify target **or** `metrics.textfile` is set, and
`doctor.scheduled=false` still turns it off. Its schedule stays
`doctor.schedule`, daily by default. An operator who wants checks more often
sets that, and knows which checks contact the network.

Only the scheduled run writes the file. An operator's `doctor` at a terminal
may run as a user who cannot write the directory, or with flags that narrow
what it checks, and the file should say what the timer found.

### 5.4 The metrics

Every series carries `product`. Every other label value comes from a closed
vocabulary (operation types, check ids and categories, `mode`) or is a name the
manifest validated (services and health checks).

```text
# morzer-<product>.prom, every minute
morzer_up{product}                                   1 when the installation's state was read
morzer_release_info{product,version,mode}            1; version is the installed release
morzer_manager_info{product,version}                 1
morzer_runtime_up{product}                           1 when the runtime answered
morzer_service_running{product,service}              running containers of the service
morzer_service_containers{product,service}           containers of the service
morzer_health_check_ok{product,check}                1/0, the release's health probes
morzer_stopped{product}                              1 while stopped by `morzer stop`
morzer_operations_needing_attention{product}         count
morzer_last_operation_timestamp_seconds{product,type}  when the last one of each type finished
morzer_last_operation_success{product,type}          1/0 for that same operation
morzer_last_backup_timestamp_seconds{product}        the newest backup
morzer_update_staged{product}                        1 while a release waits for approval

# morzer-<product>-doctor.prom, each scheduled doctor run
morzer_doctor_check{product,check,category}          0 ok, 1 warn, 2 fail
morzer_doctor_last_run_timestamp_seconds{product}
```

A runtime that does not answer is `morzer_runtime_up 0`, with no service and no
health series, never a set of zeros. RFC 0026 learned that lesson about the
fleet row: a count of zero and "could not look" have to be told apart. A state
that does not load writes `morzer_up 0` and nothing else, so the file still
replaces the last good one rather than leaving it to read as current.

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
- `--textfile`: the write is atomic, no file ending `.prom` exists before the
  rename, the mode is 0644, an unwritable directory fails and leaves the
  previous file, and a symlink placed at the final name is replaced, not
  written through.
- The unit set: the metrics pair exists exactly while `metrics.textfile` is
  set, and the doctor pair exists with a notify target or the setting, and
  never with `doctor.scheduled=false`.
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

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `OPEN` | **The surface.** Recommended: a command, `morzer metrics`, printing the Prometheus text format, and a per-installation setting, `metrics.textfile`, naming the node exporter's textfile directory, which a timer writes `morzer-<product>.prom` into. Nothing listens. Alternative: an HTTP exporter, `morzer metrics serve`, with its own unit and port (§5.5). |
| 2 | `OPEN` | **The cadence and cost of the status file.** Recommended: a new `<product>-metrics.timer` every minute, running what `status` runs: state, the runtime, one round of health probes, and no `doctor`. Alternative: write the file only from the timers and operations that already run, with no new timer, so it can be a day old. |
| 3 | `OPEN` | **Exporting `doctor`.** Recommended: the scheduled `doctor` writes `morzer-<product>-doctor.prom`, and its timer is installed while `metrics.textfile` is set as well as while a notify target is, still at `doctor.schedule`. Alternative: leave `doctor` out, and export status only. |
| 4 | `OPEN` | **How a check's state is encoded.** Recommended: one series per check, `morzer_doctor_check{check,category}`, valued 0 ok, 1 warn, 2 fail, so "anything failing" is `== 2`. Alternative: the Prometheus enum convention, one series per check and state, valued 1 for the current state. |
| 5 | `OPEN` | **The identifying labels.** Recommended: `product` on every series and no installation id, since a product is unique on its machine and the instance label says which machine. Alternative: `installation_id` as well, which survives a product being re-initialised but adds a label no rule needs. |
| 6 | `ASSUMED` | Off by default. It needs no consent, because nothing leaves the machine; whoever scrapes the node exporter already reads the host. A sandbox exports too, with `mode="sandbox"` on `morzer_release_info`. |
| 7 | `ASSUMED` | No free text is exported: no check messages or remedies, no hostnames, no parameter values, no URLs. Label values are the manager's own vocabulary, check ids and categories, or service and health-check names the manifest validated. Every label value is escaped per the text format all the same. |
| 8 | `ASSUMED` | The file is written to a temporary name not ending in `.prom`, created exclusively in the same directory with mode 0644, and renamed into place. A failed write leaves the previous file, and the node exporter's `node_textfile_mtime_seconds` is the staleness signal; morzer exports no timestamp of its own for the status file. |
| 9 | `ASSUMED` | A runtime that does not answer is `morzer_runtime_up 0` with no service or health series. A state that does not load writes `morzer_up 0` alone. Neither is a set of zeros. |
| 10 | `ASSUMED` | `metrics.textfile` must be an absolute path to an existing directory; the manager does not create it. `doctor` checks it as `metrics.textfile`: missing or unwritable, or the file older than ten minutes while the timer is installed, warns. |
| 11 | `ASSUMED` | Only the scheduled `doctor` writes the doctor file; an operator's `doctor` does not. |
| 12 | `ASSUMED` | The renderer is written here, without a Prometheus client library; the text parser from `prometheus/common` checks its output in tests only. |
| 13 | `ASSUMED` | The metric names and labels in §5.4 are a contract. A rename adds the new name and keeps the old one for a minor release, marked deprecated in its `# HELP`. |

## 12. Phasing

- **P1** (§5.1–§5.4): `morzer metrics`, `metrics.textfile`, the metrics timer,
  the doctor file, `doctor`'s check, and the monitoring page. One PR. It needs
  decisions 1–5.
