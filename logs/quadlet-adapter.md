# quadlet-adapter · RFC 0023 P3, the Quadlet adapter

On branch `quadlet-adapter` from `main` at `0d17c47d`. Executes
[RFC 0023](../rfcs/0023-runtimes-beyond-compose.md) P3: a second
`ports.Runtime` implementation, rootless Podman with Quadlet, against the
runtime contract suite (§3 goal 1, §8), and an account of every place the port
had to change (§3 goal 2, §6).

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## Plan

### Baseline for §6

§6 measures P3 against "13 core methods and 8 optional capabilities"
(2026-08-17). `internal/ports/runtime.go` today has **14 core methods** (with
`ExecInteractive`, RFC 0050) and **12 optional capabilities** (with
`ImageIngester`, `ImageRemover`, `ServiceImager`, `DefinitionDrifter`,
`BundleReader`, `VolumeInspector` and `VolumeCapturer` counted). None of that
growth was forced by a second adapter, so it does not fire the hatch. It is the
surface P3 is now measured against.

### Adapter

- `internal/adapters/runtime/quadlet/` (new), beside `compose/`:
  - `Name()` is `quadlet`, the key `runtimes.quadlet` declares (decision 8).
  - `Validate` runs the Quadlet generator in dry-run over the bundle's unit
    files and refuses what it refuses. It has no side effects.
  - `Up` installs the units into the user's Quadlet directory, runs
    `systemctl --user daemon-reload`, and starts them. `Down`, `Restart`,
    `Stop` and `Start` go through `systemctl --user`.
  - `Exec`, `ExecInteractive`, `Stats`, `Pull`, `HasImage`, `RemoveImage` and
    `ProbeRegistry` go through `podman`. `Status` reads the units and the
    containers Quadlet names `systemd-<unit>`.
  - `Logs` keeps the framing the port fixes, `<container> | [<instant> ]<text>`.
  - `RunOneShot`: see G6.
  - The volume capabilities reuse the helper-container capture. It is the only
    mechanism that works under rootless (§12 item 5; D-007, outstanding).
  - `ImageIngester`: see G7.
  - `ToolRequirer` names `podman` and `systemctl`.
  - `HookVarSupplier` supplies nothing. `<PRODUCT>_COMPOSE_PROJECT` stays
    Compose's (decision 17).
- `internal/cli/root.go:992`: today the composition root builds `compose.New`
  alone. The adapter becomes one of several, and the installation's recorded
  runtime picks one by `Name()`, as decision 5's refusal already compares
  (G5).

### Manager-side

- Ordering behind the parameter file (decision 22, OPEN to P3): G3.
- `doctor`: a lingering account (§11; D-006, outstanding), and
  `net.ipv4.ip_unprivileged_port_start` against the ports the release requires
  (§11).
- Decision 12 expires here: G5.

### Tests

- `test/suite/runtime_podman_test.go`, build tag `podman`, beside
  `runtime_docker_test.go` (`//go:build docker`): the runtime contract suite
  against the adapter, unchanged (§8). An exemption it needs is a finding
  for §11, not a parameter.
- Unit tests in the package against a fake runner, as `compose/` has.
- No CI lane. §12 refuses to report a lane nobody has watched go red. The suite
  runs on the development host's rootless Podman, and P4 owns the acceptance
  stage.

### Docs and changelog

- `reference/manifest.md`: `runtimes.quadlet.files`, and what each file kind
  means.
- An operating page for a Podman host: lingering, ports below 1024, where the
  units live.
- `CHANGELOG.md`, Unreleased: Added.

## Measurements taken for this plan, 2026-11-12

On the development host: Podman 6.1.1, rootless, cgroups v2, netavark;
systemd 261; `Linger=yes`; `net.ipv4.ip_unprivileged_port_start = 1024`. No
container was started.

1. **Quadlet passes `${VAR}` through to `ExecStart`.** A `.container` with
   `PublishPort=${DEMO_PARAM_PORT}:80` and a `[Service] EnvironmentFile=`
   generates
   `ExecStart=/usr/bin/podman run --name systemd-%N --replace --rm ... --publish ${DEMO_PARAM_PORT}:80 --env-file ...`
   and carries the `EnvironmentFile=` into `[Service]`
   (`QUADLET_UNIT_DIRS=<dir> /usr/lib/podman/quadlet -dryrun -user`).
2. **systemd expands it from the file.**
   `systemd-run --user --pipe --wait -p EnvironmentFile=<file> /usr/bin/printf '[%s]\n' '${DEMO_PARAM_PORT}'`
   printed `[18443]` for `DEMO_PARAM_PORT=18443`.
3. **Stopping a Quadlet unit removes its container.** The generated service
   has `ExecStop=/usr/bin/podman rm -v -f -i systemd-%N`, and `ExecStart` runs
   with `--rm --replace`.

## Decisions the plan needs that nobody has settled

Seven. Four are load-bearing, so **the readiness gate fires**: RFC 0023 is not
ready to execute for P3. Nothing below is decided, and no code is written. Each
item states the question, what it touches, and a recommendation for the author.

**G1 — Who runs the manager for a rootless installation (load-bearing).**
Decision 2 (`LOCKED`) makes rootless the tested configuration. Everything the
manager owns assumes root:
- units in `/etc/systemd/system` (`supervisor/systemd/systemd.go:36`);
- `RunDir` at `/run/<product>` (`domain/paths.go:64`), with secrets rendered
  root-owned `0700` beneath it (`ops/apply.go:420`).

A rootless container sees the manager's user as its root, so a root-owned
`0700` secret is unreadable to it. The RFC never says which user the manager
is.
- (a) **The manager runs as the rootless user.** State goes in XDG
  directories, the render directory in `/run/user/<uid>/<product>` (a tmpfs),
  and the Supervisor's units are user units. This reaches decision 6, `Paths`,
  and #345's ownership check on the manager binary.
- (b) **The manager stays root** and drives the user's Podman across the user
  boundary (`systemctl --user -M <user>@`, `runuser`). Every rendered file it
  hands a container must then belong to that user.

Recommended: (a). It keeps one user per installation, and the
Supervisor/Runtime split of decision 6 survives as two user-scope managers.
(b) puts a privilege boundary inside every runtime call.

**G2 — How parameters reach unit fields that are not container environment
(load-bearing).** §4.3 delivers parameters to the container through an
`EnvironmentFile`. A port parameter also has to reach `PublishPort=`. Under Compose it gets
there because Compose interpolates `<PRODUCT>_PARAM_<NAME>` from its
environment (RFC 0007 decision 4), and Quadlet interpolates nothing itself. Measurements 1
and 2 show one way that needs no manager-side templating: the bundle writes
`${<PRODUCT>_PARAM_<NAME>}` in the unit, and systemd expands it from a
`[Service] EnvironmentFile=`. The names are the ABI, and §4.3 already says so.

Recommended: that way. The adapter adds the `[Service] EnvironmentFile=` line,
without the `-` (decision 21), to each unit it installs, as a drop-in, so a
vendor never writes the manager's path. Alternative: render the unit files as
Go templates with `.Parameters`. That is a second interpolation syntax, and
the vendor's files would differ between the bundle and the machine.

**G3 — Decision 22's mechanism (load-bearing; the row leaves it to P3).** The
product units must not start before the parameter file exists.

Recommended: the units the adapter installs are not enabled for any target.
It refuses or removes an `[Install]` section, and the manager's product unit
(`apply --startup`) renders and then starts them. That is ordered by
construction, as Compose is (§4.3). Alternative: `After=`+`Requires=` on a
render unit, the shape the venue measured. It works, and it adds a unit whose
only job is the ordering.

**G4 — `Start` cannot keep the stopped container (load-bearing; §6's stub
test).** `Stop` "has to be reversible by Start without recreating anything",
and `Start` must "put back exactly what was stopped" (`ports/runtime.go:67`,
and `Start`'s comment below it). Under Quadlet, a stop
removes the container (measurement 3), and a start runs a new one from the
loaded unit.
- (a) **Accept a recreation from the unchanged loaded definition.** No
  `daemon-reload` runs between `Stop` and `Start`, so nothing drifts, which is
  the hazard the contract names. The container's writable layer does not
  survive, and the docs say so.
- (b) **Pause and unpause instead.** This keeps the container. A backup then
  reads storage under a frozen process, which is crash-consistent rather than
  cleanly stopped. That changes what `Stop` means.
- (c) **Treat it as the stub §6 counts,** which fires the escape hatch.

Recommended: (a), with a contract-suite case that pins "no drift across
Stop/Start" for both adapters. The author should rule it before P3, because
§6 says the verdict has to be written down before sunk effort argues for the
abstraction.

**G5 — Choosing the runtime at `init` (decision 12 expires here).**
Recommended: a release declaring one runtime installs with it. A release
declaring both needs `init --runtime <name>` and is refused without it. No
probing heuristic. The installation records the choice (decision 11), and the
composition root picks the adapter whose `Name()` matches.

**G6 — `RunOneShot` under Quadlet.** Compose runs a service's definition to
completion (`compose run`). The Quadlet equivalent is a `.container` unit
started with `systemctl --user start --wait` and read for its exit status.
Recommended: that, with the unit declared like any other and never started by
`Up`. The manifest's `runtime-service` operations and tasks already name the
service (`domain/manifest.go:681`, `:929`), so this needs no new field. Not
load-bearing.

**G7 — Image ingest (D-001 and D-002, outstanding since 2026-08-16).**
Recommended: D-001. The ingest pulls from the loopback registry with
`--tls-verify=false` on that one command, as measured in §12 item 6, and never
on the general `Pull` path. Not load-bearing for the adapter's shape.

**Not in scope, noted:** RFC 0053 profiles have no Quadlet analogue of
`compose --profile`. The adapter's `Validate` refuses a non-empty
`cfg.Profiles` until a vendor asks, rather than ignoring it.

## What would unblock P3

Rulings on G1–G4, recorded in RFC 0023 as rows (G3 answers decision 22, and
G5 retires decision 12). G1 is a design choice. G2 and G4 rest on the
measurements above, which a short spike would carry end to end with a
container on a free port other than 18080: a `.container` started, stopped, started
again, and its published port read back.
