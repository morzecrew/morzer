---
title: Monitoring with Prometheus
icon: lucide/activity
summary: Exporting what status and doctor know through the node exporter's textfile directory, and the alerts worth writing on it
---

# Monitoring with Prometheus

If the machine already runs Prometheus's
[node exporter](https://github.com/prometheus/node_exporter), the manager can
put what `morzer status` and `morzer doctor` know where the node exporter reads
it. Nothing listens on a port, and nothing is sent anywhere: the manager writes
files into the node exporter's textfile directory, and your Prometheus scrapes
the node exporter as it already does.

## Turning it on

Point the installation at the directory the node exporter's
`--collector.textfile.directory` names:

```sh
morzer config set metrics.textfile=/var/lib/prometheus/node-exporter
```

Common places for it:

| Where the node exporter came from | Textfile directory |
| --- | --- |
| Debian or Ubuntu package `prometheus-node-exporter` | `/var/lib/prometheus/node-exporter` |
| The upstream release, run by hand | Whatever `--collector.textfile.directory` says; there is no default |

The directory has to exist already; the manager does not create it. From then
on:

- `<product>-metrics.timer` runs `morzer metrics --textfile <dir>` every minute
  and writes `morzer-status-<product>.prom`.
- The scheduled `doctor` writes `morzer-doctor-<product>.prom` after each run,
  at `doctor.schedule`, which is each morning by default. Setting
  `metrics.textfile` installs the doctor timer even on a machine with no notify
  target. That run contacts what every `doctor` run contacts: the release's
  image registry, its health endpoints, and with consent the vendor's channel
  and GitHub.

Each file is written to a temporary name and renamed into place, so the node
exporter never reads half of one. Several installations on one machine each
write their own two files into the same directory.

`morzer metrics` with no flag prints the same text to stdout, for a quick look
or for a collector of your own.

## What is exported

Every series carries `product`. No label carries a message, a host name, a URL
or a parameter value.

From the status file, every minute:

| Metric | Labels | Value |
| --- | --- | --- |
| `morzer_up` | | 1 when the installation's state was read; 0 alone when it was not |
| `morzer_release_info` | `version`, `mode` | 1; the installed release, and `production`, or `dev` on a sandbox |
| `morzer_manager_info` | `version` | 1; the manager that wrote the file |
| `morzer_runtime_up` | | 1 when the runtime answered, 0 when it did not |
| `morzer_service_running` | `service` | containers of the service that are running and not unhealthy |
| `morzer_service_containers` | `service` | containers of the service |
| `morzer_health_check_ok` | `check` | 1 when the release's health probe passed |
| `morzer_stopped` | | 1 while the product is stopped by `morzer stop` |
| `morzer_operations_needing_attention` | | operations flagged for a human |
| `morzer_last_operation_timestamp_seconds` | `type` | when the newest finished operation of that type ended |
| `morzer_last_operation_success` | `type` | 1 when that operation succeeded; a compensated or interrupted one is 0 |
| `morzer_last_backup_timestamp_seconds` | | when the newest backup was taken |
| `morzer_update_staged` | | 1 while a release waits for approval |

From the doctor file, after each scheduled run:

| Metric | Labels | Value |
| --- | --- | --- |
| `morzer_doctor_check` | `check`, `category` | 0 ok, 1 warn, 2 fail |
| `morzer_doctor_last_run_timestamp_seconds` | | when the scheduled `doctor` last ran |

What is missing means something. With nothing installed, or nothing applied
yet, there are no runtime series at all. A runtime that did not answer is
`morzer_runtime_up 0` with no service series, never a row of zeros. An
installation with no backup yet has no backup timestamp, rather than one from
1970. A one-shot job behind a profile that a task or a migration started and
that finished is not exported as a service that is down.

The health probes are the release's own and run once a minute, so a vendor's
health endpoint is called that often.

## Alerts worth writing

```yaml
groups:
  - name: morzer
    rules:
      - alert: MorzerMetricsStale
        expr: time() - node_textfile_mtime_seconds{file=~"(.*/)?morzer-status-.*\\.prom"} > 600
        for: 5m
      # One per installation: a file never written, or removed, has no
      # mtime to go stale.
      - alert: MorzerMetricsMissing
        expr: absent(node_textfile_mtime_seconds{file=~"(.*/)?morzer-status-demo\\.prom"})
        for: 10m
      - alert: MorzerStateUnreadable
        expr: morzer_up == 0
        for: 5m
      - alert: MorzerRuntimeDown
        expr: morzer_runtime_up == 0
        for: 5m
      - alert: MorzerServiceNotRunning
        expr: morzer_service_running < morzer_service_containers and on(product) morzer_stopped == 0
        for: 5m
      - alert: MorzerHealthCheckFailing
        expr: morzer_health_check_ok == 0
        for: 5m
      - alert: MorzerBackupOld
        expr: >
          time() - morzer_last_backup_timestamp_seconds > 36 * 3600
          or (morzer_up == 1 unless on(product) morzer_last_backup_timestamp_seconds)
        for: 1h
      - alert: MorzerNeedsAttention
        expr: morzer_operations_needing_attention > 0
      - alert: MorzerDoctorCheckFailing
        expr: morzer_doctor_check == 2
```

The first two rules are the ones that catch everything else going quiet. When the
timer stops, or a run cannot write the file, the last file stays and the node
exporter's `node_textfile_mtime_seconds` stops advancing. Its `file` label is
the file's name in older node exporter releases and its full path in newer
ones, which is why the rule matches either. A file that was never written, or
was removed, has no such series at all, which is what the second rule, one per
installation, is for. The backup rule's second half fires for an installation
that has never taken a backup, since it has no backup timestamp to age. `morzer doctor` warns too, as
`metrics.textfile`, when the directory is missing or not writable, or when the
status file is more than ten minutes old.

## Turning it off

```sh
morzer config unset metrics.textfile
```

The timer goes, and so do both files, so nothing keeps reporting a state that
has stopped being written. `doctor.scheduled=false` keeps the status file and
removes the doctor timer and its file. Removing the installation removes its
files and leaves the directory, which belongs to the node exporter.
