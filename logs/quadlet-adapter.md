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
