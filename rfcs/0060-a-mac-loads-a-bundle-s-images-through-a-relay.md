# RFC 0060 — A Mac loads a bundle's images through a relay

- **Status:** 📝 Draft — accepted 2026-11-19, not started. Ruled by the
  author 2026-11-19: decisions 3 and 8 as leaned. Decisions 9 to 14 were added
  on #402's review, as the leans the author asked to be recorded; 12 and 13
  are measured before the code.
- **Scope:** Lets a Mac install and update a release whose images travel in
  the bundle, which 0.18.0 refuses there. The manager still serves the
  bundle's image layout from inside its own process on the Mac's loopback,
  and the daemon still pulls each image from it and verifies every blob as
  it does on Linux. What changes is the path between them: on a Mac, where
  Docker Desktop's daemon runs in a VM whose loopback is its own, a
  short-lived relay container on the VM's host network forwards the daemon's
  `127.0.0.1` to the manager's registry through `host.docker.internal`.
  **Contract changes:** a Mac no longer refuses a release that bundles its
  images; an offline Mac needs the volume helper image loaded first, as a
  backup there already does. No manifest, installation-state, hook ABI or
  document change, and nothing changes on Linux.
- **Related:** [RFC 0011](0011-bundled-container-images.md) (decisions 18,
  19 and 21) ·
  [RFC 0055](0055-macos-as-a-host-for-an-installation.md) ·
  [`internal/adapters/runtime/compose/ingest.go`](../internal/adapters/runtime/compose/ingest.go) ·
  [`internal/infra/ociserve/ociserve.go`](../internal/infra/ociserve/ociserve.go) ·
  [`internal/adapters/runtime/compose/sharing.go`](../internal/adapters/runtime/compose/sharing.go) ·
  [`internal/domain/manifest.go`](../internal/domain/manifest.go) (`Manifest.PlatformRefusal`) ·
  [`internal/cli/engine_darwin.go`](../internal/cli/engine_darwin.go) ·
  [logs/macos-host.md](../logs/macos-host.md) (UNLISTED, 2026-11-19T07:48:56Z)
- **Discussion:** #397, which shipped the refusal.

---

## 1. Summary

A Mac loads a bundle's images the way Linux does, through the daemon's own
pull out of the manager's in-process registry, with one container added for
the length of the ingest: the pinned busybox the manager already uses for
volumes, on the VM's host network, relaying each connection to the manager's
registry on the Mac's loopback through `host.docker.internal`. The refusal
#397 added on a Mac goes. Measured on Docker Desktop 4.44.1 (§13): the relay
carried an eight-layer pull end to end, and both other routes were ruled out.

## 2. Motivation

0.18.0 refuses a release that bundles its images on a Mac
(`internal/domain/manifest.go:922-934`, the platform preflight, and
`release ingest` at `internal/lifecycle/ops/ingest.go:63-67`). The refusal was
the author's ruling for the release, on the evidence the acceptance run gave:
`init` of a packed bundle failed at `ingest-images` with
`cannot load localhost:5000/demo/app out of the bundle`, because
`ociserve` binds `127.0.0.1:0` on the Mac (`ociserve.go:94`) and the daemon
pulls `127.0.0.1:<port>` inside Docker Desktop's VM (`ingest.go:112`), which is
another loopback. RFC 0011 exists for a customer who cannot reach the vendor's
registry, and a Mac is as likely to be one as a Linux server; the refusal
leaves those customers nothing to install.

## 3. Current state

- `Runtime.IngestImages` (`ingest.go:42`) asks which bundled images are absent,
  refuses a `DOCKER_HOST` that names another host (`requireLocalDaemon`,
  `ingest.go:204`), starts `ociserve` on the layout, and for each image runs
  `docker pull 127.0.0.1:<port>/<repo>@<digest>`, tags the digest-derived
  alias (RFC 0011 decision 19) and untags the loopback reference
  (`ingestOne`, `ingest.go:105`). The pull is what verifies every blob against
  the digest the manifest names (decision 21).
- The adapter already runs the pinned helper on a Mac: the file-sharing probe
  is a `docker run --rm` of `DefaultHelperImage`
  (`sharing.go:43-49`, `volumes.go:32`). It refuses an unpinned helper
  override. `doctor`'s `checkVolumeHelperImage` reports a helper that is not
  local (`internal/lifecycle/ops/doctor.go:2092`).
- The darwin engine is wired in `internal/cli/engine_darwin.go`, which builds
  Docker Desktop's runner. The compose adapter itself knows nothing of the
  platform.
- The refusal is one branch of `Manifest.PlatformRefusal`, so `init` refuses
  before it adopts the release, a plan and an update or rollback refuse before
  they start, `doctor`'s `system.platform` fails a restored one, and
  `release ingest` refuses before it binds anything.
- The macOS acceptance run asserts the refusal and keeps its registry up
  (`.github/scripts/acceptance.sh:1667`), and its restart phase installs the
  1.2.0 bundle rather than the packed one (logs/macos-host.md, decision 9,
  2026-11-19T07:48:56Z).

## 4. Goals / Non-goals

**Goals**

- A Mac installs, updates and `release ingest`s a release that bundles its
  images, with no registry reachable.
- The guarantee RFC 0011 decision 21 gives, a blob that is not what its digest
  says is refused, holds on a Mac because the daemon's own pull still makes it.
- Nothing changes on Linux.

**Non-goals**

- Loading images with `docker load`. Measured not to be trustworthy (§5.4).
- Touching the operator's Docker configuration: no `insecure-registries`, no
  certificate the daemon must trust.
- A relay on Linux. A Linux daemon shares the manager's loopback.

## 5. Design

### 5.1 The relay

On a Mac, `IngestImages` starts `ociserve` as today, on the Mac's
`127.0.0.1:<server>`, then a relay before the first pull:

```sh
docker run -d --rm --name morzer-ingest-<random> --network host \
  --label dev.morzer.ingest-relay=<installation> <helper> \
  timeout <bound> nc -lk -s 127.0.0.1 -p <relay> -e nc host.docker.internal <server>
```

`--network host` on Docker Desktop is the VM's network namespace, the one the
daemon's `127.0.0.1` lives in, and `host.docker.internal` from a container
reaches the Mac's loopback (§13, P4–P6). busybox `nc -lk -e` keeps listening
and runs one `nc` per connection, so the daemon's parallel layer fetches each
get their own (§13, Q3). Each pull is then
`docker pull 127.0.0.1:<relay>/<repo>@<digest>`, and the alias is tagged from
that reference as today. The relay is removed when the ingest ends, failed or
not, the same way the server is closed. It listens on `127.0.0.1` alone, so a
container on a bridge network cannot reach it through the VM's gateway
(decision 9). One left by a manager that was killed is removed by the next
ingest of the same installation, which the deployment lock makes the only one
running, and `timeout` ends it within the ingest's own bound in any case
(decisions 10 and 15).

The helper is never pulled: an ingest with no local helper is refused before
anything starts, naming `docker load` of it, which is the order the offline
install page already gives for backups. An unpinned helper override is refused,
as the file-sharing probe refuses one.

### 5.2 Where it applies

The relay is wired where the darwin engine is, `internal/cli/engine_darwin.go`,
as an option of the compose runtime, so the adapter does not branch on the
platform and Linux runs the code it runs today. A `DOCKER_HOST` naming another
host is still refused first.

### 5.3 The refusal goes

`Manifest.PlatformRefusal` loses its darwin branch; the platform preflight and
`release ingest` lose theirs with it. The proposed row in logs/macos-host.md
(2026-11-19T07:48:56Z) is superseded by this RFC's decisions.

### 5.4 Alternatives considered

- **The daemon pulls `host.docker.internal:<server>` itself.** It resolves the
  name and reaches the server, and then refuses plain HTTP to any registry not
  on its own loopback (§13, P8). Serving TLS would need a certificate the
  daemon trusts, and `insecure-registries` is the operator's Docker
  configuration. Both are changes to a machine the manager should not make.
- **`docker load` of the layout.** On the containerd image store a loaded
  image's ID is the pinned digest, and an alias can be tagged by it (§13, Q1),
  so identity could be checked. But a load whose layer did not match its
  digest exited 0, leaving the layer missing from the content store (§13, Q2),
  so the exit status cannot be trusted and every layer would need checking by
  hand. On the classic store a load keeps no registry digest at all, which is
  RFC 0011 decision 3's measurement. It would be a second mechanism, which
  decision 21 withdrew.
- **A relay built into the manager.** The manager cannot run in the VM; the
  relay has to be a container, and the helper is the one image a Mac already
  has to keep local.

## 6. Tests

- The compose adapter, against a scripted runner: with the relay option, the
  relay starts before the first pull and is removed after the last, also when a
  pull fails; each pull and tag names `127.0.0.1:<relay>`; an absent or unpinned
  helper is refused before the server starts. Without the option, the argv is
  today's, byte for byte.
- `domain`, suite and doctor tests that pinned the refusal now pin its absence
  on darwin, and the refusal of a release not made for darwin is unchanged.
- The compose adapter, scripted too: an ingest after one killed once its relay
  started removes that installation's leftover relay before starting its own,
  and leaves one relay at most; a relay that exits at start, or that is not
  created, is refused before the first pull, naming what it needs (decisions
  10, 11 and 13).
- The compose adapter, scripted: the relay's bound is the time left on the
  ingest's context, and with no deadline one pull's and one tag's limit for
  each pending image, so a relay outlasts the last pull of a multi-image
  ingest (decision 15).
- The macOS acceptance run goes back to the bundled scenario in full: the
  registry stopped, the local copy of `app` removed, `init` and `apply` with no
  registry, the alias running, `doctor` content. While the relay runs, a
  connection to a non-loopback address of the VM is refused (decision 9); its
  result is the measurement of the bound form, which §13's Q3 did not take.

## 7. Docs

The Platforms page, Running a release on a Mac, Bundled images and Installing
without a network lose the refusal, and say instead that a Mac loads a bundle's
images through a relay container of the volume helper image, which an offline
Mac loads first.

## 8. Out of scope

- A relay for a daemon on another host. `DOCKER_HOST=tcp://…` stays refused;
  the relay closes the gap between a VM and the Mac it runs on, not a network.
- Docker Desktop's opt-in host networking for the Mac's own interfaces. The
  relay uses the VM's namespace, which `--network host` gave on §13's Mac;
  whether it does with every Docker Desktop setting is measured (§10).

## 9. Risks

- **The relay listens on the VM's loopback for the ingest.** Any process in the
  VM's host network namespace, which is any container run `--network host`,
  could read the bundle's images while it runs. The images are what the
  daemon is about to store, and on Linux the manager's loopback is open to any
  local process in the same way. Bound to `127.0.0.1` (decision 9), it is not
  reachable from a bridge network through the VM's gateway, which an unbound
  `nc -l` would be.
- **A manager killed mid-ingest leaves the relay listening.** Its target is gone
  with the manager, so it forwards nothing, and the next ingest or `timeout`
  removes it (decision 10).
- **A port another container holds.** The relay could fail to bind its port
  (decision 3).
- **Throughput.** One `nc` per connection was measured for a small image only.
  A multi-gigabyte image is unmeasured; the acceptance run's image is small too.

## 10. Unresolved questions

- How the relay's port is chosen and a failure to bind it is detected
  (decision 3). Implementation settles it.
- Whether the acceptance run's restart phase goes back to the packed bundle
  (decision 8). The author rules.

Both were ruled on 2026-11-19 as leaned; decisions 3 and 8 carry the rulings.

- Which Docker Desktop settings the relay needs. §13's run did not record
  whether Docker Desktop's opt-in host networking was on, and Docker documents
  that Enhanced Container Isolation does not support host networking. The
  executor records the settings, measures the relay with host networking off
  and, where available, under Enhanced Container Isolation, and builds the
  check of decision 13 on what it finds.
- Whether a helper an offline Mac loads as the offline page says answers to its
  pinned reference, on each image store (decision 12). P2 suggests it does not
  on the containerd store, from `alpine` rather than the helper.
- Whether the relay bound to `127.0.0.1` carries the pull as the unbound one of
  §13's Q3 did. The acceptance run measures it (decision 9).

All three were measured on 2026-11-20, before the code (§13, S0 to B3). The relay
ran with Docker Desktop's opt-in host networking and Enhanced Container
Isolation both off, so it needs neither turned on. A loaded helper answers to
its ID and not to its pinned reference on the containerd store, and to neither
on the classic store. The bound relay carried the pull, and a bridge network
could not reach it.

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `ASSUMED` | On a Mac a bundle's images are loaded as on Linux: the daemon pulls each from the registry inside the manager's process on `127.0.0.1:0` (RFC 0011 decision 18), and its pull verifies every blob (decision 21). Only the path between them changes. |
| 2 | `ASSUMED` | The path is a relay container: the volume helper image, `--network host`, labelled with its installation, running `timeout <bound> nc -lk -s 127.0.0.1 -p <relay> -e nc host.docker.internal <server>`, started before the first pull of an ingest and removed when the ingest ends, failed or not. Decisions 9 and 10 say why the bind and the bound. |
| 3 | `ASSUMED` | How the relay's port on the VM's loopback is chosen, and how a relay that could not bind it is caught before the pull blames the bundle: a random port in the dynamic range, and another when the relay has exited at start, which is how a port another container holds shows. The executor logs what it builds. Was `OPEN`; ruled by the author 2026-11-19: as leaned. |
| 4 | `ASSUMED` | The helper is never pulled for an ingest. One not local is a refusal naming `docker load` of it, before the server starts; an unpinned override is refused as the file-sharing probe refuses one. Consequence: an offline Mac loads the helper before `init`, as it already must before a backup. |
| 5 | `ASSUMED` | The relay is wired with the darwin engine (`internal/cli/engine_darwin.go`) as an option of the compose runtime. The adapter does not branch on the platform, and Linux runs today's code unchanged. |
| 6 | `ASSUMED` | `Manifest.PlatformRefusal` loses its darwin branch for bundled images, and the platform preflight and `release ingest` with it. This supersedes the row proposed in logs/macos-host.md (UNLISTED, 2026-11-19T07:48:56Z). |
| 7 | `ASSUMED` | Rejected: the daemon pulling `host.docker.internal` (it refuses plain HTTP; the fix is the operator's Docker configuration), and `docker load` (exit 0 over a corrupted layer; a second mechanism decision 21 withdrew). §13 holds both measurements. |
| 8 | `ASSUMED` | The macOS acceptance run's restart phase keeps the 1.2.0 bundle it installs since #397, rather than returning to the packed bundle: the start at boot ingests nothing, and the run's bundled scenario covers the relay. Was `OPEN`; ruled by the author 2026-11-19: as leaned. |
| 9 | `ASSUMED` | The relay listens on `127.0.0.1` alone, `nc -s 127.0.0.1`: an unbound `nc -l` takes every address in the VM's host network, the bridge gateway among them, from which any container could read the bundle's images. The acceptance run checks that a connection to a non-loopback address of the VM is refused. Added on #402's review, 2026-11-19. |
| 10 | `ASSUMED` | A relay carries a label naming its installation and runs under busybox `timeout`, bounded by the ingest's own pull timeout. Each ingest first removes any relay of the same installation, which the deployment lock makes a leftover, never a live one; `timeout` ends one in any case. A relay never outlives its manager by more than that bound. Added on #402's review, 2026-11-19. The bound is superseded by decision 15. |
| 11 | `ASSUMED` | The relay needs the helper to carry busybox's `nc`, with `-l`, `-k`, `-s`, `-p` and `-e`, and `timeout`. The pinned default has the `nc` flags (§13, P7); the executor checks it has `timeout` too. A relay that exits at start is told apart from a port in use by what it printed, and refused naming the requirement, so an override without them fails before the pull, not during it. The `MORZER_VOLUME_HELPER_IMAGE` docs say so. Added on #402's review, 2026-11-19. |
| 12 | `ASSUMED` | Before the code, the executor measures, on each image store a Mac can run (containerd and classic), whether the helper loaded as the offline page prescribes, `docker save` of its pinned reference and `docker load`, answers to that reference, and whether its ID is the pinned digest. P2 was that procedure for `alpine`, not the helper, on the containerd store, where `docker save` writes an OCI archive whatever the image, and the loaded image did not answer to its reference; the helper itself and the classic store are unmeasured. Where it does not answer, the relay and volume capture run the helper by its ID only on a store where the measurement shows the ID is the pinned digest, and the offline page is corrected in the same change. A store where neither holds needs the helper pulled while the Mac has a network, and the refusal says so. Added on #402's review, 2026-11-19. |
| 13 | `ASSUMED` | Before the first pull, the ingest checks the relay end to end: that its container was created and is running, which Enhanced Container Isolation can refuse for `--network host` at creation, and that a request through it is answered. Either failing is a refusal naming the Docker Desktop settings the measurement (§10) finds the relay needs, so a missing prerequisite is reported before the pull, not as a bundle that would not load. Added on #402's review, 2026-11-19. |
| 14 | `ASSUMED` | A daemon on another host is caught whatever named it: `DOCKER_HOST` as today (`requireLocalDaemon`), and `DOCKER_CONTEXT` or the CLI configuration's current context by decision 13's check, since a relay started there cannot reach the Mac's loopback. The refusal names all three. Added on #402's review, 2026-11-19. |
| 15 | `ASSUMED` | The relay's `timeout` bound covers the whole ingest, not one pull. One relay serves every pull of an ingest, and those pulls run one after another, so a bound of one pull's limit would end the relay during a later pull that fits its own. The bound is the time left on the ingest's context, which the `ingest-images` step bounds to 60 minutes for `init`, `update` and `release ingest` alike; with no deadline on the context, it is one pull's limit and one tag's for each pending image. Supersedes decision 10's bound. Found on #402's review; ruled by the author 2026-11-20: as leaned. |

## 12. Phasing

One pull request: the relay in the compose adapter behind the darwin option,
the refusal removed, the tests and docs above, and the acceptance run's bundled
scenario restored on a Mac. Its record is the next macOS acceptance run.

## 13. Measurements

On the author's Mac, Docker Desktop 4.44.1, engine 28.3.2, 2026-11-19. The
first probe:

```
P0  engine 28.3.2, client 28.3.2
P1  image store: driver-type=io.containerd.snapshotter.v1
P2  load of a oci tar: alpine@sha256:d9e853e8… does NOT resolve: Loaded image ID: sha256:d9e853e8…
P4  the daemon reached a host-network container on 127.0.0.1: … 127.0.0.1:5056/probe/x:1: not found
P5  a bridge-network container reaches the Mac's 127.0.0.1 through host.docker.internal
P6  a host-network container reaches the Mac's 127.0.0.1 through host.docker.internal
P8  the daemon pulling host.docker.internal:5055: … http: server gave HTTP response to HTTPS client
P7  BusyBox v1.37.0 … -lk  With -e, provides persistent server …
```

P3 of the first probe corrupted nothing, the extracted blob being read-only;
the second probe repeated it as Q2:

```
Q1  pinned sha256:d9e853e8…; loaded Loaded image ID: sha256:d9e853e8…; probe/alias:1 is sha256:d9e853e8…
Q2  a corrupted layer LOADED: … failed to extract layer sha256:88b4fba6…: NotFound: failed to get reader
    from content store: content digest sha256:3f26bc2d…: not found
Q3  pulled through the relay in 0s (8 layer entries): … Status: Downloaded newer image for
    127.0.0.1:5058/probe/nginx:1
```

Q3 is the relay of §5.1 end to end: a registry published on the Mac's
`127.0.0.1:5057`, a busybox `nc -lk -p 5058 -e nc host.docker.internal 5057`
on the VM's host network, and the daemon pulling `127.0.0.1:5058`. The same
probe on Linux, with the relay's target on the host's own loopback, pulled the
same image.

The third probe, on the same Mac on 2026-11-20, took the measurements
decisions 9 to 13 ask for before the code:

```
S0  engine 28.3.2; image store: driver-type=io.containerd.snapshotter.v1
S1  settings-store.json: "LastContainerdSnapshotterEnable": 1743608262 "UseContainerdSnapshotter": true
H1  save wrote a oci tar; load said: Loaded image ID: sha256:9db7b599…
H2  the loaded helper does NOT run by its pinned reference: … No such image: busybox@sha256:9db7b599…
H3  pinned digest sha256:9db7b599…; loaded id sha256:9db7b599…; equal: yes
H4  the loaded helper runs by its id sha256:9db7b599…
R0  probe-relay-bound created: running exit=0
R0  probe-relay-open created: running exit=0
R1  the pull through the bound relay worked: … Status: Downloaded newer image for
    127.0.0.1:5058/probe/nginx:1
R2  from a bridge container, the bound relay: wget: can't connect to remote host
    (172.17.0.1): Connection refused
R3  from a bridge container, the unbound relay: reached 172.17.0.1:5059
T1  a relay under timeout 3 ended and was removed within 6s
```

H1 to H4 are the offline page's procedure for the pinned helper itself:
`docker save` of its reference, its removal by reference and by ID,
`docker load`, then `docker run --pull=never`. R1 is §5.1's bound form,
`timeout 300 nc -lk -s 127.0.0.1 -p 5058 -e nc host.docker.internal 5057`, run
`--network host`. R2 and R3 ask the VM's bridge gateway from a container on the
default bridge network, the bound relay first and an unbound one on 5059 as the
control. S1 found no key for Docker Desktop's opt-in host networking or for
Enhanced Container Isolation, and no `admin-settings.json` was present, so the
relay ran with both at their default, off. Enhanced Container Isolation needs a
Docker Business subscription and was not measured.

The same probe on Linux, engine 29.7.2 on the classic image store, with the
relay's target on the host's own loopback:

```
L1  save wrote a oci tar; load said: Loaded image ID: sha256:db287cb6…
L2  the loaded helper does NOT run by its pinned reference: … No such image
L3  pinned digest sha256:9db7b599…; loaded id sha256:db287cb6…; equal: no
L4  the loaded helper runs by its id sha256:db287cb6…
```

Its relay lines matched R0 to T1: R2 was refused at `172.17.0.1:5058`, and R3
reached `172.17.0.1:5059`. The pinned busybox, 1.37.0, on the same machine:

```
B1  nc -lk -s 127.0.0.1 -p <a port another relay holds>   nc: bind: Address already in use   (exit 1)
B2  nc -lk -Z -p 5072 -e cat                               nc: invalid option -- 'Z'
B3  command -v timeout                                     /bin/timeout
```

So the offline page's procedure leaves the helper unreachable by its pinned
reference on both stores, on Linux as on a Mac. Only on the containerd store is
its ID the pinned digest.
