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

## Spike, 2026-11-13T10:10:39Z

This carries G2 and G4 end to end, as the section above asks. Nothing in it
is decided.

On the development host, Podman 6.1.1:
- a `.container` unit in `$XDG_RUNTIME_DIR/containers/systemd/`, which
  Quadlet reads for a rootless user and which a reboot clears;
- an image already on the host (`Pull=never`), publishing
  `127.0.0.1:${SPIKE_PARAM_PORT}:80`;
- a drop-in, `morzer-spike.container.d/10-params.conf`, that holds only
  `[Service] EnvironmentFile=<file>`;
- no `[Install]` section.

The unit, the drop-in and the file were removed afterwards. No container
was left behind.

1. **G2 holds end to end.** Quadlet merged the drop-in into `[Service]`, and
   the container published `127.0.0.1:18443` from `SPIKE_PARAM_PORT=18443` in
   the file. The vendor's unit never names a manager path.
2. **Decision 21 holds.** With the file removed, the start failed with
   `Failed to load environment files: No such file or directory`
   (`Result=resources`), and nothing ran.
3. **G4 is as measurement 3 said.**
   - After `systemctl --user stop`, no container was left.
   - The next start ran a new container on the same port: `4289f621dcac`,
     then `3b56d2d56c29`.
   - A file written into the first container's writable layer was gone.
4. **G3's shape needs nothing more.** `is-enabled` reports `generated`. Only
   the explicit start started the unit.
5. **New: systemd reads the parameter file at every start, not at
   `daemon-reload`.** With the file changed to `18444` and no reload, the next
   start published `18444`.
   - G4 (a) says nothing drifts because no `daemon-reload` runs between
     `Stop` and `Start`. That covers the unit. The values come from the file,
     so the file has to be unchanged between them too.
   - Under Compose, `start` keeps the ports the container was created with.
   - If G4 (a) is ruled, the contract case it names pins both. It must also
     rewrite the parameter file between `Stop` and `Start`, and expect the
     values from before.
   - Today the only `Stop`/`Start` pair in the manager is the cold volume
     capture in one backup call (`hookbackup/volumes.go:386-404`). That pair
     renders nothing. `morzer start` is an `apply`, not `Start`
     (`ops/stop.go:178`).
6. **Read, not measured: a stop also deletes anonymous volumes.** The
   generated `ExecStop` is `podman rm -v -f -i`, and `-v` removes the
   container's anonymous volumes. So a vendor `Volume=` with no name loses its
   data at every `Stop`, the cold capture's included.
   - G4 (a) then needs `Validate` to refuse a `.container` with an anonymous
     volume.
   - No spike ran this.

## Rulings, and a second spike, 2026-11-13

The author ruled G1–G4 on 2026-11-13, each as recommended. The rows go to
RFC 0023 as 26–29. G5–G7 stay with the build, and each is logged when it is
decided.

**Correction to spike item 5.** That item says the contract case has to
rewrite the parameter file between `Stop` and `Start` and expect the values
from before. Under Quadlet as planned, that case fails: the restarted
container takes the new values, as the item itself measured. The fix belongs
to the adapter, not the contract.
- The drop-in references the adapter's own copy of the parameter file. `Up`
  writes the copy, and nothing else does.
- `Stop`, `Start` and `Restart` then bring back the values the containers were
  created with, as Compose's `start` and `restart` do.
- The file the manager renders reaches the containers at the next `Up`, the
  way a changed `.env` reaches Compose.

Row 29 records this as part of the ruling's mechanism. The author ruled the
contract case; the copy is what lets Quadlet meet it.

**Spike 2: volumes across a stop.** On the same host, a transient `.container`
mounted an anonymous volume (`Volume=/anon`) and a named one
(`Volume=morzer-spike2-named:/named`). A file was written into each.
- After `systemctl --user stop`, the anonymous volume no longer existed, and
  the named one did.
- After the next start, `/anon/f` was gone and `/named/f` read back.
- Item 6 is now measured: a Quadlet `Stop` deletes an anonymous volume's data,
  where `ports/runtime.go:62` says `Stop` removes no volumes.
- The unit and the named volume were removed afterwards.

## What G1 reaches, and the plan for P3a, 2026-11-13

G1 (a) moves the manager, not only the runtime. P3 therefore splits in two:
- **P3a, the user layout.** The manager runs as a user other than root, with
  Compose, and nothing in it is Quadlet's. It can be tested by the suites that
  already run without root.
- **P3b, the Quadlet adapter.** This is the plan above, and it builds on P3a.

G1 raised three questions. The author ruled each on 2026-11-13, as
recommended (RFC 0023 rows 30–32):
- **H1, where.** One tree, `$XDG_DATA_HOME/morzer/{etc,var/lib,opt}/<product>`.
  The render directory is `$XDG_RUNTIME_DIR/morzer/<product>`. Both are
  namespaced because a product's name could collide: `/run/user/1000` on the
  development host already holds `containers`, `libpod`, `systemd`, `gnupg`,
  `pulse` and more (`ls /run/user/1000`).
- **H2, which.** The layout follows who runs the command. A user other than
  root gets the user layout when it holds an installation, or for `init`.
  Otherwise the system layout is used, as today.
- **H3, Compose.** Compose may use the user layout, as an untested
  configuration.

Already settled, so no question was asked: RFC 0032 row 11 documents a
declared secret owner under rootless and does not handle it. Without root,
the render refuses to give a file to another uid, with the hint at
`domain/secret.go:175`.

### P3a plan

- **Domain.** `internal/domain/paths.go` gains `UserPaths`: `PathsUnder` over
  the user tree, with `RunDir` moved to `$XDG_RUNTIME_DIR/morzer/<product>`.
  `$XDG_DATA_HOME` falls back to `~/.local/share`. `$XDG_RUNTIME_DIR` has no
  fallback (decision U1).
- **CLI layout resolution** (`internal/cli/root.go`):
  - Path resolution and `rewireForProduct` choose by effective uid (row 31).
  - `pathsFromConfig` maps a `--config` inside the user tree to `UserPaths`,
    so a user unit's `--config` gets the tmpfs render directory and not
    `<tree>/run`.
  - `--root` keeps meaning `PathsUnder`, so the tests are unchanged.
- **Supervisor** (`internal/adapters/supervisor/systemd/systemd.go`):
  - A user scope. Units go in `$XDG_CONFIG_HOME/systemd/user`, and every call
    is `systemctl --user`.
  - The product unit is `WantedBy=default.target` rather than
    `multi-user.target`.
  - `Available` asks whether the user's manager is running.
  - The composition root (`root.go:1065`) picks the scope from the layout.
  - Units still order against system targets: decision U3.
- **Manager binary** (#345):
  - `atomicfs.ReplaceableByNonRoot` gains a sibling that takes the uid the
    units run as. Under user units, a path that the user or root owns, and
    that no one else can write, is one only they can replace.
  - Callers: `ops/settings.go:1151`, `ops/manager_version.go:130-160` and
    `ops/self_update.go`.
- **Doctor:**
  - A user installation on an account that is not lingering is warned about:
    its units stop at logout and do not start at boot (§11; D-006).
  - `net.ipv4.ip_unprivileged_port_start` is checked against the ports the
    release publishes.
  - The metrics directory check (`ops/metrics.go:270`) asks as the user the
    units run as, which is not always root.
- **Tests:**
  - `domain` unit tests for `UserPaths`.
  - `clitest`: resolution by uid, `--config` inside the user tree, and `init`
    without root landing in the user tree. The harness sets `XDG_DATA_HOME`
    and `XDG_RUNTIME_DIR` to temporary directories, so no test reads a real
    home.
  - Supervisor tests: `--user` in every argv, the unit directory, and
    `default.target`.
  - Ownership tests: replaceability for a uid.
  - `test/suite`: one install and apply in the user layout against the fakes.
- **Docs:**
  - An operating page for a user installation: the layout, lingering, ports
    below 1024, and that hooks run as the user.
  - `reference/commands.md` on how a layout is chosen.
  - `CHANGELOG.md`, Unreleased: Added.

Decisions the plan leaves to the build. None is load-bearing, so the gate does
not fire. Each is logged when it is made:
- **U1.** With `$XDG_RUNTIME_DIR` unset, the user layout is refused rather
  than falling back to a disk directory, because `RunDir` holds decrypted
  secrets.
- **U2.** Whether `Paths` carries its scope as a field or derives it, as
  `Root()` is derived.
- **U3.** What the user units do with `After=`/`Wants=network-online.target`
  and `docker.service`. A user manager cannot order against system units.
- **U4.** What `morzer ls` shows to a user other than root: their own
  installations, the system ones, or both.

P3a ships before P3b, and each is its own PR.

## Review of #355, 2026-11-13

- **Row 28: a missing `[Install]` does not stop a dependency pull.** A unit
  outside the installation that `Requires=`, `Wants=` or `BindsTo=` one of the
  adapter's units starts it whenever that unit starts, before any render. The
  ruling stands, and the row now records the case:
  - the start fails loudly, because the parameter file is absent (row 21);
  - `apply --startup` starts the unit as usual afterwards;
  - the adapter resets a failed unit before it starts one, so a start limit
    those early pulls ran into does not refuse the manager's start;
  - the docs say outside units order against the product unit.

  The render unit G3 rejected would have turned the failure into a wait. The
  author did not ask to revisit that.
- **Row 29 against the port's text.** The port's comments say `Stop` removes
  no container and `Start` recreates nothing (`ports/runtime.go:62-79`), so
  row 29 recreating a container reads as a breach of the contract. The
  author's ruling (G4 a) is the answer, and §6 asked for exactly this verdict.
  The row now says what that leaves undone: the build restates those comments
  to the property row 29 protects, which is the definition, the values and
  every declared volume, but not a container's writable layer.
- **Row 32: two installations, one Compose project.** A user installation
  and a system installation of the same product, on Compose and one daemon,
  select the same project, because row 20 defaults it to the product's name.
  They then share its containers and named volumes. The author narrowed H3 on
  2026-11-13: Compose in the user layout needs a daemon that is the user's
  own (`docker info` lists `name=rootless`), and a system daemon reached
  through the `docker` group is refused at `init` and reported by `doctor`.
  The check is the Compose adapter's. P3a adds it, with a test against a fake
  `docker info`.

## The P3a build, 2026-11-25

On branch `user-layout` from `main` at `62423d97`. The P3a plan above
predates RFC 0055 (macOS as a host), so its anchors were refreshed against
`main` first:
- Layout resolution is `resolvePaths`, `pathsFromConfig` and `pathsFor` in
  `internal/cli/root.go`. `runRoot` is per OS (`domain/paths_default.go`,
  `domain/paths_darwin.go`).
- The supervisor is picked per OS in `internal/cli/supervisor_linux.go` and
  `supervisor_darwin.go`. The Linux one already receives the layout and
  ignores it.
- The manager-binary check goes through `Deps.replaceableByNonRoot`
  (`internal/lifecycle/ops/ops.go:441`), whose callers are
  `settings.go:1183`, `manager_version.go:151` and `self_update.go:420`.
- `secrets.render_to` defaults from `DefaultPaths` (`domain/manifest.go:1535`),
  but nothing reads it at runtime: the render and `SECRETS_DIR` use
  `Paths.SecretsRenderDir()`. It needs no code change. The docs say a bundle
  reaches its secrets through `SECRETS_DIR`.

The author agreed the leans on U1–U5 on 2026-11-25. Each entry is written
before the code it governs. U6–U9 are what the refreshed survey found.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-25T21:26:00Z
attempt: 1
claim: U1, a user layout with $XDG_RUNTIME_DIR unset has no tmpfs to render secrets into, and the rows do not say what happens then
evidence: rfcs/0023-runtimes-beyond-compose.md:354
action: decided
proposal: ASSUMED — a command that would manage the user layout is refused while $XDG_RUNTIME_DIR is unset or not absolute, with a hint naming /run/user/<uid>. Nothing falls back to a directory on disk, because RunDir holds decrypted secrets (domain/paths.go:50).
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-25T21:26:00Z
attempt: 1
claim: U2, the rows do not say how code knows it is managing a user layout
evidence: internal/domain/paths.go:93-95
action: decided
proposal: ASSUMED — Paths derives it, as it derives Root(): a layout whose RunDir is not <Root()>/<runRoot>/<product> is a user layout. No field is stored, so it cannot disagree with the directories.
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-25T21:26:00Z
attempt: 1
claim: U3, a user manager has no network-online.target, so the units' ordering on it means nothing there. The agreed lean dropped docker.service too, but in the user scope that name is rootless Docker's own user unit, which dockerd-rootless-setuptool.sh installs, and row 32 requires that daemon
evidence: internal/adapters/supervisor/systemd/systemd.go:612-614
action: decided
proposal: ASSUMED — user units drop After=/Wants=network-online.target and keep After=/Requires=docker.service, which there names the user's rootless daemon. The product unit is WantedBy=default.target, and the timers stay WantedBy=timers.target, which the user manager has too.
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-25T21:26:00Z
attempt: 1
claim: U4, the rows do not say what `morzer ls` lists for a user other than root
evidence: internal/lifecycle/ops/list.go:106
action: decided
proposal: ASSUMED — `ls` lists the layout row 31 picks, and says which layout it is, in its text and as `layout` in its JSON.
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-25T21:26:00Z
attempt: 1
claim: U5, RFC 0055 runs the manager as root on a Mac and leaves a per-user installation undesigned, and macOS sets no $XDG_RUNTIME_DIR
evidence: rfcs/0055-macos-as-a-host-for-an-installation.md:694
action: decided
proposal: ASSUMED — the user layout exists on Linux only, through per-OS files in internal/cli. On a Mac every command keeps the system layout, as today.
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-25T21:26:00Z
attempt: 1
claim: U6, a unit's --config names the tree by path, and pathsFromConfig can tell it is the user tree only by comparing it with $XDG_DATA_HOME/morzer. A user manager usually does not carry a data home set in a shell profile, so a unit would read the tree as a --root layout and render secrets under it, on disk
evidence: internal/adapters/supervisor/systemd/systemd.go:619
action: decided
proposal: ASSUMED — user units carry Environment=XDG_DATA_HOME naming the data home they were written for, so the unit resolves the tree the command that wrote it resolved.
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-25T21:26:00Z
attempt: 1
claim: U7, row 32 makes the daemon check the Compose adapter's, and nothing above the adapter can reach a check the port does not name
evidence: internal/ports/runtime.go:760
action: decided
proposal: ASSUMED — an optional capability, ports.EngineScopeProber, which the Compose adapter implements from `docker info`, in the shape of FileSharingProber. init and doctor call it only for a user layout. For §6 it is forced by row 32, not by the second adapter.
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-25T21:26:00Z
attempt: 1
claim: U8, the lifecycle layer derives another product's layout as PathsUnder(d.Paths.Root(), product), which for a user layout puts that product's RunDir in the tree, on disk
evidence: internal/lifecycle/ops/list.go:290
action: decided
proposal: ASSUMED — Paths.For(product) is the same layout for another product, keeping its render directory's parent. Every PathsUnder(d.Paths.Root(), …) above the CLI and pathsFor in it use it.
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-25T21:26:00Z
attempt: 1
claim: U9, the manager-binary checks run only where Root() is empty, so a user layout would skip them, while row 26 names that check as one the user units reach
evidence: internal/lifecycle/ops/settings.go:1180
action: decided
proposal: ASSUMED — for a user layout the checks run, and ask whether anyone but root and the user the units run as could replace the binary. The self-update elevation hint keeps asking about root alone, because it suggests sudo, which runs the binary as root.
```

### Self-audit of the P3a build, 2026-11-25

Scope: the branch against `origin/main`, 10 commits before this one, about
1,400 lines across domain, CLI, the systemd and Compose adapters, the
lifecycle layer, tests and docs. Every branch the build added was sabotaged
once, and each sabotage failed its test.

- **The self-update hint sent root into a user's tree.** A user
  installation whose binary only root can replace was told to re-run with
  `sudo … self-update --root <tree>`. Root would then lock and journal the
  user's installation and leave root's files in its tree. Fixed: for a user
  layout the hint installs a manager for the user and runs `init --repair`
  with it, and names no `sudo` (`internal/lifecycle/ops/self_update.go:419`).
  This refines U9, which kept that hint asking about root alone.
- **Lint.** `contextcheck` and `errcheck` flagged two of the new tests.
  Fixed.

Left as they were, and worth knowing:
- Completion of `--product` still lists the system's installations for a
  user, since it re-roots to `--root` or to `/`.
- Root given `--config` inside a user's tree gets a `--root` layout there,
  with its render directory in the tree, as any prefixed `--config` did
  before. Root cannot know that user's data home.
- The user scope's `Available`, which asks the user's manager, is not
  exercised by a test: every test that installs units relocates them, and
  none can start a user manager.

```divergence
decision: 26
grade: LOCKED
kind: resolved
at: 2026-11-25T21:51:12Z
attempt: 1
claim: a user installation's manager runs as its user, with its state and render directory in the user's directories and its units as user units, and the binary check holds those units to the user in root's place
evidence: internal/cli/supervisor_linux.go:16-22
action: decided
```

```divergence
decision: 30
grade: LOCKED
kind: resolved
at: 2026-11-25T21:51:12Z
attempt: 1
claim: a user installation is one tree under $XDG_DATA_HOME/morzer, laid out as --root lays one out, with its render directory at $XDG_RUNTIME_DIR/morzer/<product>
evidence: internal/domain/paths.go:91-95
action: decided
```

**Correction, 2026-11-25.** The audit above missed one more defect, which
`just ci` then found. The CLI test of a user `init` ran the real `docker
info`. Where no daemon runs, as in CI, the probe could not run and `init`
went ahead. On a host whose daemon is the system's, `init` was refused and
the test failed. The user runner now puts a stand-in `docker` on `PATH`, so
no test asks the host's daemon whose it is. A test of the refusal through
the CLI was added with it (`test/clitest/user_layout_test.go`).

## Review of #434, round 1, 2026-11-26

Bot review of the P3a branch found nine defects, and each was reproduced
and fixed in its own commit. Four of them were decisions the rows leave
open, recorded below as U10–U13. These entries were written after the
fixes they govern, not before. That is a lapse, recorded as one.

Left as it was, and worth knowing: a tree outside `$XDG_DATA_HOME/morzer`
is read as a tree of its own, so a `--config` into a user installation's
tree, from a command run with a different data home, renders its secrets
inside the tree, on disk. Only a mark the installation keeps of its own
layout could tell the two apart, which is a schema change. The docs page
now says to keep one data home (`pages/docs/operating/running-as-your-own-user.md:72-77`).

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-26T07:58:24Z
attempt: 1
claim: U10, row 32 says Compose needs the user's own daemon, and U7 asked only at init and in doctor; a unit's or a shell's command reaching the system's daemon later would change the system installation of the same product
evidence: internal/lifecycle/ops/ops.go:666-671
action: decided
proposal: ASSUMED — every command of a user installation that takes the deployment lock, and exec, which takes none, asks whose the engine is before it waits on or changes anything, in its own environment. A probe that cannot run does not refuse, since the command that needs the engine then fails on its own.
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-26T07:58:24Z
attempt: 1
claim: U11, row 30 places the tree at $XDG_DATA_HOME/morzer and does not say how a path names it; compared as spelled, a --config through a symlink's target read the tree as a tree of its own, with its secrets on disk
evidence: internal/cli/layout_linux.go:37-52
action: decided
proposal: ASSUMED — a root is the user's tree when it resolves to the same directory. An absolute $XDG_DATA_HOME is honoured with HOME unset, and a user layout with no absolute config home for its units is refused.
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-26T07:58:24Z
attempt: 1
claim: U12, user units carry paths from the user's home, where a space or a % is the user's to choose, and the units wrote every path as it stood
evidence: internal/adapters/supervisor/systemd/systemd.go:1113-1137
action: decided
proposal: ASSUMED — unit paths and Environment= values are written as one systemd word each, unchanged when nothing in them is special. A manager path with a quote, a backslash or a control character is refused at render, since systemd refuses such a command however it is quoted (`systemd-analyze --user verify`: "Executable path contains special characters").
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-26T07:58:24Z
attempt: 1
claim: U13, U9 held a user installation's units to the binary check but kept its skip when the manager does not answer, which for a user's manager does not mean the units never run
evidence: internal/lifecycle/ops/manager_version.go:140-145
action: decided
proposal: ASSUMED — a user installation's units are read whatever Available says, and a check that cannot read them fails, as it does for a system installation's.
```

## Review of #434, round 2, 2026-11-26

Four more defects, each fixed in its own commit: the daemon was asked
before the lock was held, a tree not yet made was not known by a symlink
target's spelling, a user tree holding only unreadable installations fell
back to the system layout, and completion's fallback read the system's
units for the user's tree. The first two refine U10 and U11.

Left as it is: a command still resolves the Docker context on each call,
so a context switched while it runs is not caught. Pinning the endpoint
the probe approved would mean resolving the context once and passing it to
every Docker call the adapter makes.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-26T08:30:18Z
attempt: 2
claim: U10, asked before the lock, a command that waited behind another ran on whatever daemon the context named by the time it held the lock
evidence: internal/lifecycle/ops/ops.go:686-691
action: decided
proposal: ASSUMED — as U10 attempt 1, except that a command that takes the deployment lock asks once it holds it, just before it changes anything.
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-26T08:30:18Z
attempt: 2
claim: U11, compared only when both sides resolved, a symlink target's spelling of a user tree not yet made read as a tree of its own
evidence: internal/cli/layout_linux.go:47-58
action: decided
proposal: ASSUMED — as U11 attempt 1, with each side resolved through its nearest existing ancestor.
```

## After #434, 2026-11-26

#434 merged during its third review round. That round found one more
defect, fixed on its own branch: a data home that links to a directory
not yet made did not resolve, so the user's tree was compared as a literal
path. It refines U11 again.

Still open, and the author's to decide: U10 lets a command go ahead when
`docker info` cannot be run or times out, since the command that needs the
engine then fails on its own. A daemon that times out on `info` and then
answers would run that command unchecked. Refusing instead would also
refuse every locked command that needs no engine, a config change say,
while the daemon is down, and `init` with no release yet
(`internal/lifecycle/ops/engine_scope.go:53-62`).

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-26T08:58:19Z
attempt: 3
claim: U11, a data home that links to a directory not yet made did not resolve, so the user's tree was compared as a literal path and a --config under the link's target read it as a tree of its own
evidence: internal/cli/layout_linux.go:54-69
action: decided
proposal: ASSUMED — as U11 attempt 2, with a link that does not resolve followed by hand, at most 40 links deep.
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-26T09:24:44Z
attempt: 4
claim: U11, a dangling link's target was cleaned before it was followed, so a `..` in it skipped the link ahead of it and named another tree
evidence: internal/cli/layout_linux.go:55-75
action: decided
proposal: ASSUMED — as U11 attempt 3, with a link's target resolved component by component, as the kernel resolves it, and links followed by hand only as far as the kernel's own 40.
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-26T09:53:07Z
attempt: 5
claim: U11, a dangling link's target ending in a slash had the slash trimmed before the split, so the link before it was joined back unfollowed
evidence: internal/cli/layout_linux.go:80-83
action: decided
proposal: ASSUMED — as U11 attempt 4, with a trailing slash split off as an empty name, so the link before it is followed.
```

## After #435, 2026-11-26

#435 merged as `da059ec0`, with U11 attempts 3 to 5.

The author ruled the open U10 question on 2026-11-26: keep it as built. A
user installation's command goes ahead when `docker info` cannot be run or
times out, and only a daemon that answers and is not the user's own is
refused. Refusing on a probe that did not run would refuse config changes
and `init` with no release while the daemon is down.

## The P3b plan, 2026-11-26

On branch `quadlet-p3b` from `main` at `da059ec0`. This builds the adapter
in "Plan" above, on the P3a layout. Rows 26 to 29 and 32 govern it. G5 to
G7 are decided in the build ("Rulings, and a second spike").

### Measured for this plan

Podman 6.1.1, the generator in dry run only
(`QUADLET_UNIT_DIRS=<dir> /usr/lib/podman/quadlet -dryrun -user`). Nothing
was started, and the directory was removed afterwards.

1. **Subdirectories are read.** Units under `<dir>/morzer/demo/` were
   generated.
2. **Names.**
   - `demo-web.container` becomes `demo-web.service`, and its container is
     `systemd-%N`, so `systemd-demo-web`.
   - `demo-data.volume` becomes the Podman volume `systemd-demo-data`,
     created by `demo-data-volume.service`.
   - `demo-net.network` becomes `systemd-demo-net`.
   - A `.container` that names either gets `Requires=` and `After=` on its
     unit.
3. **`[Service] Type=oneshot` in a `.container` is kept.** The run drops
   `-d` and `--sdnotify`, so the unit's start lasts as long as the
   container.
4. **`Volume=${DEMO_SECRETS_DIR}/token:/run/secrets/token:ro` passes
   through** as `-v ${DEMO_SECRETS_DIR}/token:...`, with no volume unit.
5. **`Notify=healthy` gives `--sdnotify=healthy`,** so a start waits for
   health.
6. **The generator is reached as
   `/usr/lib/systemd/user-generators/podman-user-generator`**, a link to
   `../../podman/quadlet` on this host. Distributions put the target
   elsewhere.

Read, not measured: the product unit and the backup service have
`Requires=docker.service`
(`internal/adapters/supervisor/systemd/systemd.go:651`, `:676`). A Quadlet
installation has no such unit, so its product unit would never start.

### Decisions the plan needs that nobody has settled

Two are load-bearing, so the readiness gate does not fire. Both are part of
the bundle format a vendor writes, so they are put to the author before
any code.

**Q1 — How a Quadlet installation's names are kept apart (load-bearing).**
Unit names are global in the user's manager. Container, volume and network
names are global in the user's Podman. Compose keeps installations apart
with the project (row 20), and Quadlet has nothing like it. Two products
that both ship `web.container` would replace each other's unit.
- (a) **Recommended.** Every file in `runtimes.quadlet.files` is named
  `<product>-<service>.<kind>`. The service is the name without the prefix,
  so the manifest's references to services (parameters, secrets, tasks,
  operations) mean the same under both runtimes.
  - `Validate` refuses a file without the prefix.
  - It also refuses a `ContainerName=`, `VolumeName=` or `NetworkName=`
    that does not start with it, and a named `Volume=` source that is
    neither a bundle `.volume` file nor prefixed.
  - The vendor's files reach the machine unchanged.
- (b) The adapter renames the files and rewrites what refers to them
  (`Requires=db.service`, `Volume=data.volume:`). Rejected for row 27's
  reason against templating: the files would differ between the bundle and
  the machine.
- (c) No namespace, and a collision is refused at install. Then two vendors
  that both ship `web.container` cannot share an account.

**Q2 — How a bundle keeps a one-shot out of `Up` (load-bearing).** Compose
does it with a profile, as the contract fixture's `migrate` shows
(`test/suite/runtime_docker_test.go:52`). Quadlet has no profiles, and `Up`
is given no manifest. So G6's "needs no new field" holds for `RunOneShot`
but not for `Up`.
- (a) **Recommended.** A `.container` with `[Service] Type=oneshot` is one
  `Up` never starts. It is systemd's own word for a unit that runs to
  completion, and measurement 3 shows Quadlet keeps it. `RunOneShot` runs
  any `.container`, as `compose run` runs any service.
- (b) An adapter option that lists the one-shots. That is a second place to
  say what the unit already can.

### Governed, and counted for §6

**The product unit's engine.** Row 14 sets the mechanism: the Supervisor
asks the runtime rather than naming Docker.
- A new optional capability names the unit a runtime's engine runs as.
  Compose answers `docker.service`, and Quadlet declines.
- The Supervisor writes `Requires=` and `After=` only when there is an
  answer.
- It is the first addition the second adapter forces. The surface today is
  14 core methods and 14 optional capabilities: the baseline in "Plan",
  plus `FileSharingProber` (RFC 0055, #377) and `EngineScopeProber`
  (row 32). Neither of those two was forced by the second adapter.
- §6 allows two additions. If Q1 and Q2 are ruled as recommended, the count
  is one, and no method is a stub.

### Decided in the build, each logged when it is made

- **Where the units live:** `$XDG_RUNTIME_DIR/containers/systemd/morzer/<product>/`.
  - The drop-ins and row 29's parameter copy sit beside the units, on the
    user's tmpfs.
  - A reboot clears all three, and `apply --startup` writes them again
    before it starts anything. That is row 28's order.
  - The adapter finds the directory through the variable Quadlet itself
    reads, so `RuntimeConfig` gains no field.
- **`Up`:**
  - It writes the units and two drop-ins: `EnvironmentFile=` naming the
    copy, without `-` (row 21), and a `Label=` with a hash of the unit's
    text with the values put in.
  - It then runs `daemon-reload`, resets any failed unit (row 28), and
    starts every unit that is not a one-shot.
  - A running unit whose hash changed is restarted, as Compose re-creates
    on its config hash. `ForceRecreate` restarts the named units.
    `RemoveOrphans` stops and deletes the product's units the release no
    longer declares.
  - `Wait` polls health for every container that has a healthcheck.
- **`RunOneShot` uses a transient copy of the unit,
  `<product>-<service>-run-<n>`.**
  - Its drop-in sets the argv (`Exec=`), the extra environment, the mounts,
    `Type=oneshot`, and stdout and stderr to separate files.
  - Facts read stdout alone (`internal/lifecycle/ops/facts.go:253`), so the
    two streams stay separate.
  - It is started with `systemctl --user start --wait`, and the exit code is
    read from `ExecMainStatus`.
  - Rejected: `podman run` built from the generator's `ExecStart`, which
    would mean parsing Podman's flags.
- **`Status` reads the units and their containers.** A unit with no
  container reports `exited`. `Stop` then keeps the count of services the
  contract asserts (`test/contract/runtime.go:386`).
- **`Logs`** reads the units' journal, framed `<container> | ...`.
- **`Down`** stops and deletes the units. With `Volumes` set, it also
  removes the `.volume` volumes.
- **`Validate`** runs the generator in dry run over a temporary copy of the
  files. It refuses:
  - a kind other than `.container`, `.volume` and `.network`;
  - an `[Install]` section (row 28);
  - an anonymous volume (row 29);
  - a relative host path, which would resolve against the copy;
  - any option, because none is known;
  - a non-empty `cfg.Profiles`.
- **Root:** a Quadlet installation exists only in the user layout. Wired for
  the system layout, the adapter refuses and says to run the command as the
  user (row 26). Rootful Podman stays row 2's variant, not built.
- **G5, choosing the runtime:**
  - `Deps` carries every adapter. The composition root, which already reads
    the installation (`internal/cli/root.go:1150`), wires the installation's
    adapter by `Name()`.
  - `init` picks from the release. A release declaring two runtimes needs
    `init --runtime`, and row 12 retires.
  - Import (row 13) refuses a runtime no adapter names.
  - `doctor` with no installation reports each runtime's tools.
- **G7, image ingest:** through the loopback registry, with
  `--tls-verify=false` on that one pull only (§12 item 6).
- **Volumes:** `Volumes` is read from the units. Capture, restore and size
  reuse the helper container, run with `podman`. The code is shared with
  Compose where Compose's argv stays byte for byte, and copied otherwise.
- **The tool catalogue** gains `podman`, with a catalogue entry in the leak
  inventory (row 7c).
- **Capabilities.**
  - Implemented: `ToolRequirer` (`podman`, `systemctl`), `ImageInspector`,
    `ImageRemover`, `ServiceImager`, `BundleReader`, `VolumeInspector`,
    `VolumeCapturer`, `ImageIngester`, and `DefinitionDrifter` through the
    label.
  - Declined: `HookVarSupplier`, `OptionResolver`, `FileSharingProber` and
    `EngineScopeProber`. A rootless Podman is the user's own by
    construction.
  - `RegistryProber` is implemented only if Podman can probe a registry
    without pulling. That is measured in the build.

### Port and contract (row 29, consequences 1 and 4)

- The comments on `Stop` and `Start` (`internal/ports/runtime.go:62-79`)
  are restated to what row 29 protects.
- A contract case: `Up` with a value, `Stop`, then `Start` with the value
  changed in `cfg.Env`. `Exec` must read the value from before. It runs
  against both real adapters, and against the fake if the fake can model
  it.

### Tests

- `internal/adapters/runtime/quadlet`: unit tests against a fake runner.
- `test/suite/runtime_podman_test.go`, `//go:build podman`:
  - It runs `RunRuntimeSuite`, `RunRecreateSuite` and the new case.
  - The Quadlet fixtures match the Compose ones.
  - It runs on this host's rootless Podman and user manager, through a
    `just` recipe. There is no CI lane (§12).
- G5: `clitest` for `init` choosing a runtime, with `--runtime` and without
  it, and an ops test for the import refusal.
- Supervisor: the product unit with and without an engine unit.

### Docs and changelog

- `reference/manifest.md`: `runtimes.quadlet`, the naming rule, one-shots,
  and `${...}`.
- An operating page for a Podman installation.
- `reference/commands.md`: `init --runtime`.
- `CHANGELOG.md`, Unreleased: Added.

### PRs

1. The engine capability and the Supervisor's use of it, the restated
   comments, and the new contract case on Compose and the fake.
2. The adapter, wired, with G5, its suite, the docs and the changelog.
   Until it lands there is one adapter, so row 12 stays in force.

## Rulings on Q1 and Q2, 2026-11-26

The author ruled both on 2026-11-26, as recommended. They are proposed as
rows for RFC 0023 when U1 to U13 and these are reconciled.
- **Q1 (a).** Every file in `runtimes.quadlet.files` is named
  `<product>-<service>.<kind>`, and the service is the name without the
  prefix.
  - `Validate` refuses a file without the prefix.
  - It also refuses a `ContainerName=`, `VolumeName=` or `NetworkName=`
    that does not start with it, and a named `Volume=` source that is
    neither a bundle `.volume` file nor prefixed.
- **Q2 (a).** A `.container` with `[Service] Type=oneshot` is one `Up`
  never starts. `RunOneShot` runs any `.container`.

## The P3b build, 2026-11-26

On branch `quadlet-p3b`. First PR: the engine capability, the restated
comments on `Stop` and `Start`, and the contract case for values across
them.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-26T13:58:50Z
attempt: 1
claim: the product unit and the backup service require docker.service whatever the runtime, so the Supervisor states which runtime the machine uses, and a Quadlet installation's product unit would never start
evidence: internal/adapters/supervisor/systemd/systemd.go:649-651
action: decided
proposal: ASSUMED — the runtime names the unit its engine runs as through an optional capability, ports.EngineUnitNamer, and the Supervisor requires and orders after that unit only when there is one. Compose answers docker.service, so its units render byte for byte as before. Counted for §6 as the first addition the second adapter forces.
```

Row 29's consequences 1 and 4 are in this PR as well:
- `RunStartKeepsValuesSuite` (`test/contract/runtime.go`) brings a service
  up with one value, stops it, and starts it with another value in
  `cfg.Env`. It expects the first value. It runs against Compose now and
  will run against Quadlet in the next PR. The fake models no values, so it
  is not run there.
- The comments on `Stop` and `Start` (`internal/ports/runtime.go`) now
  promise what row 29 protects. That is the definition, the values and every
  declared volume, but not a container's writable layer.

## The adapter, 2026-11-26

On branch `quadlet-adapter-build`, merging `quadlet-p3b` (#439) and
`compose-volume-helper` (#440). P3b now ships as three PRs rather than two:
#439, the helper shared with Compose (#440), and this adapter with its suite.
Choosing the runtime at `init` (G5), the tool catalogue, `doctor` with no
installation, the docs and the changelog follow in a fourth, when the adapter
is wired. Until then no command reaches it, so row 12 stays in force.

### Measured while building it, 2026-11-26

On the development host, Podman 6.1.1 and systemd 261, with scratch units
under `$XDG_RUNTIME_DIR/containers/systemd/morzer/`, removed afterwards.

1. **Quadlet reads only its own kinds of file.** A `parameters.env` beside
   the units was ignored, and `[Container]` keys in a drop-in were applied
   (dry run).
2. **A container's lines reach the user's journal with `CONTAINER_NAME`
   set,** at priority 6 for stdout and 3 for stderr. Podman's and systemd's
   own lines about the unit carry no `CONTAINER_NAME`.
3. **The parameter file takes `KEY="..."` with `\\`, `\"` and `\$` escaped.**
   A value holding a space, a quote, a backslash, a dollar and a percent sign
   reached the container unchanged.
4. **`Exec=` is expanded by systemd**, so a `$` meant for the container's
   shell has to be written `$$`.
5. **`podman stats --format '{{json .ContainerStats}}'` gives numbers,** not
   the display strings Compose parses. The user's cgroup here delegates
   `cpu memory pids` and not `io`, and Podman then reports block IO as zero.
6. **A stop leaves a unit failed with exit 143** when the container ends on
   SIGTERM, and 137 when a shell as PID 1 ignores it until the kill.
7. **Under a Quadlet unit, a one-shot's output reached neither the unit's
   stdout nor the journal with `LogDriver=passthrough`.** The same `podman run`
   under `systemd-run` wrote it to both, with each property of the generated
   unit added one at a time (`Type=oneshot`, `Delegate=yes`, `KillMode=mixed`,
   `PODMAN_SYSTEMD_UNIT`). With `LogDriver=k8s-file` and a log path of the
   run's own, every line was written as `<instant> <stream> <F|P> <text>`.

```divergence
decision: 28
grade: ASSUMED
kind: resolved
at: 2026-11-26T14:46:11Z
attempt: 1
claim: row 28 is honoured, and the units, their drop-ins and the parameter copy live in the user's runtime directory, which a reboot clears, so nothing an earlier boot left can start before `apply --startup` has rendered and installed it again
evidence: internal/adapters/runtime/quadlet/quadlet.go:135-152
action: decided
proposal: ASSUMED — as row 28, with the units in `$XDG_RUNTIME_DIR/containers/systemd/morzer/<product>/`; a unit outside the installation that requires one of them finds no unit at boot, rather than one that fails on a missing parameter file
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-26T14:46:11Z
attempt: 1
claim: a container that ends on the SIGTERM of a stop exits 143, and systemd counts that as a failure, so every Stop, the cold backup capture's included, left the unit failed and the user's manager degraded
evidence: internal/adapters/runtime/quadlet/install.go:100-107
action: decided
proposal: ASSUMED — the adapter's drop-in sets SuccessExitStatus=143 on every container unit
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-26T14:46:11Z
attempt: 1
claim: RunOneShot needs stdout apart from stderr, because facts are read from stdout alone, and a passthrough container's output under a Quadlet unit reached neither the unit's stdout nor the journal
evidence: internal/adapters/runtime/quadlet/oneshot.go:147-148
action: decided
proposal: ASSUMED — a one-shot runs as a transient copy of its unit that logs to a k8s-file of its own; the adapter reads stdout and stderr back from it, line by line in the order they arrived
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-26T14:46:11Z
attempt: 1
claim: a one-shot can run before the first Up, the first apply's migration does, when nothing is installed and its unit's volumes and networks do not exist
evidence: internal/adapters/runtime/quadlet/oneshot.go:64-68
action: decided
proposal: ASSUMED — before the first Up a one-shot installs the bundle, parameters included; after it, it writes only the bundle's volumes and networks, and leaves the running services' definitions and values as Up left them (row 29)
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-26T14:46:11Z
attempt: 1
claim: logs cite line ranges in compose/volumes.go, and the log checker fails a citation past a file's end, so moving the volume helper to a package of its own failed `just log-check` on logs/macos-host.md and logs/restore-resume.md, whose entries may not be edited
evidence: internal/adapters/runtime/compose/volumes.go:102
action: decided
proposal: ASSUMED — the helper stays in the Compose package as an exported VolumeHelper, and the Quadlet adapter imports it, as launchd imports the systemd supervisor (#440)
```

Declined for this PR, each for the wiring PR to settle:
- **`BundleReader`.** `ReadBundle(root, files)` is given no product, and a
  Quadlet file's service is its name without the product's prefix. A
  product name may itself contain a dash, so the prefix cannot be found
  from the files alone. Answering it needs the product passed in, a change
  to an existing capability's signature, and that is the next PR's question.
- **`ImageIngester`** (G7). The loopback registry Compose ingests through
  has to be shared the way the volume helper is.
- **`RegistryProber`.** Not measured yet: whether Podman can probe a registry
  without pulling.
- **`HookVarSupplier`, `OptionResolver`, `FileSharingProber`,
  `EngineScopeProber`, `EngineUnitNamer`.** Nothing to answer: no
  runtime-supplied hook variable, no options, no file sharing, a rootless
  Podman is the user's own, and there is no daemon unit.

**Drift count: 0.**

## The wiring, 2026-11-26

The author ruled the `BundleReader` question on 2026-11-26 as (a), the
recommendation: `ReadBundle` is given the product. Compose ignores it, and
Quadlet strips the prefix with it. With that, `release verify` checks a
Quadlet bundle's variables and its secrets' services as it checks a Compose
bundle's.

This PR (branch `quadlet-wiring`, on `quadlet-adapter-build`) makes the
adapter reachable:
- Every adapter this host can run is wired at the composition root, with
  Quadlet on Linux only.
- `init --runtime` chooses between the runtimes a release declares (G5).
- `installation import`, `ls` and `doctor` follow each installation's own
  runtime.
- The `podman` tool is added to the catalogue.
- `release verify` checks each declared runtime.
- The docs and the changelog are updated.

Image ingest (G7) and the registry probe stay out. A Quadlet release that
carries its images in the bundle meets the refusal that already exists for
a runtime with no `ImageIngester` (`internal/lifecycle/ops/ingest.go:181`).
Also out: the `init` wizard asking which runtime to use, and
`release new --runtime`.

```divergence
decision: 12
grade: ASSUMED
class: discovery
at: 2026-11-26T17:10:25Z
attempt: 1
claim: decision 12 expires with the second adapter, as the row says it would: with Compose and Quadlet both wired the manager knows which runtimes it drives from the adapters themselves, so a release declaring two no longer has to be refused
evidence: internal/lifecycle/ops/init.go:1398-1404
action: departed
proposal: ASSUMED — supersedes 12. A release declaring one runtime installs with it; one declaring several needs `init --runtime <name>`, naming a runtime it declares, and is refused without it. No probing. A runtime with no adapter on this host is refused at `init` rather than at the first operation (G5)
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-26T17:10:25Z
attempt: 1
claim: G5 has the composition root pick the adapter whose name the installation records, but every operation reads one wired runtime: `ls` asks it about every installation on the machine, and `init` and `installation import` learn the runtime only after the root has wired
evidence: internal/lifecycle/ops/list.go:249
action: decided
proposal: ASSUMED — the root wires every adapter this host can run, Compose first, and Deps.Runtime is the one the installation in hand records, chosen from its state when the command is wired. `ls` chooses per row. `init` and `installation import` choose when they learn the runtime, and refuse one this host has no adapter for
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-26T17:10:25Z
attempt: 1
claim: with no installation `doctor` checks the wired runtime's tools as fatal, so with two runtimes wired a host with Docker and no Podman, or the reverse, would fail checks for a runtime it may never use
evidence: internal/lifecycle/ops/doctor.go:1479-1494
action: decided
proposal: ASSUMED — with several runtimes wired, each runtime's tools are warnings that name the runtime, and one fatal check passes when some runtime has every tool it needs
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-26T17:10:25Z
attempt: 1
claim: ReadBundle is given no product, and a Quadlet file's service is its name without the product's prefix; a product name may hold a dash, so the prefix cannot be found from the files alone
evidence: internal/ports/runtime.go:657-661
action: decided
proposal: ASSUMED — ReadBundle(product, root, files), as the author ruled on 2026-11-26. It changes an existing optional capability's signature and adds none, so §6's count of additions the second adapter forced stays at one (EngineUnitNamer)
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-26T17:10:25Z
attempt: 1
claim: `release verify` reads the runtime files of the wired runtime only, so in a bundle declaring two runtimes one runtime's files go unchecked
evidence: internal/cli/release.go:583-591
action: decided
proposal: ASSUMED — `release verify` checks the files of every runtime the release declares that a wired adapter can read, and each problem names its runtime
```

**Drift count: 0.**

## Review of #441, 2026-11-26

The first review of the adapter found real defects, and they are fixed on
this branch. Two of the fixes are decisions, logged here.

Measured while fixing, on Podman 6.1.1 and systemd 261:
- Quadlet joins an `EnvironmentFile=` that starts with `${...}` onto the
  unit directory. It passes `Volume=${...}` and `Mount=` through unchanged.
- The generated `ExecStop=` removes the container by name, with no cidfile.
- systemd unloads a oneshot unit that succeeded. Afterwards `systemctl
  show` reads `ExecMainStatus=0` even when `SuccessExitStatus=` made a
  non-zero exit a success.

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-26T17:45:44Z
attempt: 1
claim: Up writes every service's files and the shared parameter copy, so with services named, a running service outside them whose definition the new files change would come back from its next Start with what no Up started; `config set` names only the services a parameter's declaration says it affects
evidence: internal/adapters/runtime/quadlet/lifecycle.go:81
action: decided
proposal: ASSUMED — beside row 29, an Up with services named also recreates any running service outside them whose definition hash the new files change, so what Start restores is what runs. A stopped service outside them still comes back with the new files
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-26T17:45:44Z
attempt: 1
claim: systemd unloads a oneshot that succeeded, so a status that SuccessExitStatus= counts as a success could not be read back from the unit
evidence: internal/adapters/runtime/quadlet/oneshot.go:216
action: decided
proposal: ASSUMED — the copy a one-shot runs writes systemd's $SERVICE_RESULT, $EXIT_CODE and $EXIT_STATUS to a file of the run's own from ExecStopPost=, and a success reports the status it wrote
```

**Drift count: 0.**

## The wiring, run against real Podman, 2026-11-26

The built binary was run against rootless Podman on the development host. It
ran `release verify`, `init`, `apply`, `status`, `logs`, `doctor` and
`installation remove`, on a bundle declaring both runtimes. Two things came out
of it.

- **`init` asked Docker about a Quadlet installation.** A user installation's
  `init` asks whether the engine is the user's own (row 32). It asked before
  the runtime was chosen, and again when it took the deployment lock, so the
  adapter it asked was always the default one. `init --runtime quadlet` was
  refused because the Docker daemon is the system's. Without `--runtime`, the
  same refusal came before "choose one with --runtime". Now `init` asks the
  runtime it installs with. It asks early when `--runtime` names that runtime
  or there is no release, and otherwise once the staged release has chosen
  it. The lock no longer asks for `init`.
- **Podman's store follows `XDG_DATA_HOME`.** With it set in the shell and not
  in the user's manager, `morzer` read a store the units did not use, and
  `apply` waited for containers until its deadline. The operating page says to
  keep the two alike. Nothing in the code changed.

```divergence
decision: 32
grade: ASSUMED
kind: resolved
at: 2026-11-26T18:12:58Z
attempt: 1
claim: row 32 is honoured for the runtime an installation is created on: a user's `init` refuses a system Docker daemon when it installs on Compose, and does not ask Docker at all when it installs on a runtime with no daemon of its own
evidence: internal/lifecycle/ops/init.go:248
action: decided
proposal: ASSUMED — as row 32, asked of the runtime `init` installs with, once it is known
```

**Drift count: 0.**
