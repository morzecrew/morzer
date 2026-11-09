# RFC 0055 — macOS as a host for an installation

- **Status:** 📝 Draft — proposed 2026-11-09. The `OPEN` rows are the author's
  to rule, and P1 is gated on the P0 measurements (§12).
- **Scope:** Makes macOS a supported host for an installation that runs
  unattended, so a release such as dotdash's client can be installed, updated,
  rolled back and backed up on a Mac with the guarantees Linux gives, and
  stated plainly where they are weaker. It reverses
  [RFC 0029](0029-macos-as-a-development-host.md) decision 1 ("never a
  production host") and supersedes decisions 4 and 6. It also builds the parts
  of RFC 0029's P2 that a supported host needs: the darwin archives, `doctor`
  on darwin and a macOS unit lane. **Contract changes:** the manifest gains
  `requirements.platforms` and a per-profile restriction, behind a 0.16.0
  floor, and a release that does not opt in is refused on macOS. **New
  code:** a launchd supervisor adapter, the darwin layout, the real
  `pidStart`, darwin `FilesystemType`, and darwin checks in `doctor`. **Not
  covered:** Windows, which is a separate investigation, and whole-machine
  routing on a Mac. No Docker engine on macOS
  can route the Mac's traffic, so a TUN profile stays Linux-only (§5.7).
  Rootless or per-user installations, and any change to the Linux layout,
  the hook ABI or how a Linux-only release behaves, are also out.
- **Related:** [RFC 0029](0029-macos-as-a-development-host.md) (the tier this
  replaces, and the measurements it made),
  [RFC 0010](0010-compose-volume-capture.md) (enumerate the permissive side),
  [RFC 0032](0032-secrets-a-non-root-container-can-read.md) (secret `owner`),
  [RFC 0053](0053-several-profiles-per-installation.md) (profiles and
  `combinable`), [RFC 0023](0023-runtimes-beyond-compose.md) (the runtime port),
  [`internal/domain/paths.go`](../internal/domain/paths.go),
  [`internal/domain/secret.go`](../internal/domain/secret.go),
  [`internal/ports/supervisor.go`](../internal/ports/supervisor.go),
  [`internal/adapters/supervisor/systemd/systemd.go`](../internal/adapters/supervisor/systemd/systemd.go),
  [`internal/infra/lock/pidstart_darwin.go`](../internal/infra/lock/pidstart_darwin.go),
  [`internal/lifecycle/preflight/mounts.go`](../internal/lifecycle/preflight/mounts.go),
  [`internal/lifecycle/ops/self_update.go`](../internal/lifecycle/ops/self_update.go),
  [`.goreleaser.yaml`](../.goreleaser.yaml), [`install.sh`](../install.sh).
- **Origin:** The author's aim, 2026-11-09: run the dotdash client bundle
  (morzecrew/dotdash `client/`, main at `cf3536d`) on the machines its users
  have, which are Macs and Windows PCs as often as Linux boxes.

---

## 1. Summary

A release that declares `requirements.platforms: [linux, darwin]` installs on a
Mac with the same commands as on Linux. It runs as root, under the Linux paths
reached through `/private`: `/etc/<product>`, `/var/lib/<product>` and
`/opt/<product>`. The one exception is the ephemeral directory, which moves
from `/run/<product>` to `/var/run/<product>` because the sealed system volume
has no `/run`. Containers run under a Docker engine in a Linux VM, Docker
Desktop first.

A launchd adapter takes the systemd adapter's place: it converges at boot,
retrying until the engine answers, and runs the backup, update, doctor, fleet
and metrics schedules, catching up a run missed while the Mac was off. Two
guarantees are weaker, and `doctor` reports both on every run:

- **Decrypted secrets are on disk.** macOS has no tmpfs. They sit on a
  FileVault-encrypted volume, and `doctor` fails when FileVault is off.
- **No profile can route the Mac's traffic.** A container's host network is
  the VM's.

A release that does not opt in is refused on a Mac before anything is staged.
So is a profile the release marks Linux-only.

None of this is built until a spike on a real Mac (P0) has measured three
things: which paths the engine shares into containers, what owner a container
sees for a rendered secret, and whether root's CLI reaches the engine. Each
can change the design, and the docs cannot settle any of them.

## 2. Motivation

- **The bundle exists, and its users are on Macs.** dotdash's client is a
  mihomo proxy for one machine or a network, configured from a subscription
  URL. Its base profile publishes a SOCKS5 and HTTP port on `127.0.0.1:7890`
  (`client/compose/compose.yaml:28`), which is exactly what a laptop needs. A
  person with a Mac can run it today only by giving morzer a Linux box.
- **RFC 0029 closed the door on purpose, and its reasons can now be answered.**
  It refused a production tier for three reasons:
  - no supervisor (decision 4);
  - no memory-backed secrets (decision 5);
  - no Docker in CI on Apple silicon (§3.5).

  The first is an adapter behind a port that was built for a second init
  system (`internal/ports/supervisor.go`: "so a container-only or
  non-systemd host is a new adapter"). The second is a weaker guarantee,
  which can be stated and checked rather than hidden (§5.4). The third is a
  verification gap, and a self-hosted lane closes it (§5.10).
- **Without a design, the gaps get filled ad hoc.** The binary already
  builds for darwin (`just darwin-check`). Someone will run it against Docker
  Desktop. Today that refuses every `owner` (`internal/domain/secret.go:337`)
  and fails on `/run` (§3). The next step would be a pile of
  workarounds, each one an unstated weakening.

## 3. Current state

- **The tree compiles and vets for darwin** on amd64 and arm64, gated in
  `just ci` (`justfile` `darwin-check`, RFC 0029 P1). The darwin-specific
  files are `termios_darwin.go`, `memory_darwin.go`, `space_darwin.go` and
  `pidstart_darwin.go`. Nothing on darwin has run under test: RFC 0029
  decision 9 declined a macOS lane.
- **No darwin build is published.** `.goreleaser.yaml:27` is `goos: [linux]`.
  `install.sh:156` refuses `Darwin` and points at `go build`, and
  `self_update.go:81` refuses anything but linux/amd64 and linux/arm64.
- **`requirements.os` exists, and it only advises.** It lists the
  distributions a release was tested on, each an `id` and a `version`
  constraint (`OSRequirement`, `internal/domain/manifest.go:576`). `preflight`
  compares it with `/etc/os-release` and only ever warns, and an empty list
  passes (`internal/lifecycle/preflight/preflight.go:193`). macOS has no
  `/etc/os-release`, so on a Mac the check warns that it cannot identify the
  host.
- **The layout is fixed to Linux paths.** `DefaultPaths`
  (`internal/domain/paths.go:59`) returns `/etc/<p>`, `/var/lib/<p>`,
  `/run/<p>` and `/opt/<p>`, and `RunDir` is "expected to be tmpfs". On macOS,
  `/etc` and `/var` are symlinks into `/private`. `/opt` is a firmlink to the
  data volume, so root can create it. `/run` cannot be created, because the
  root of the system volume has been read-only and sealed since Catalina. The
  only supported way to add an entry there is `/etc/synthetic.conf`, which
  takes effect after a reboot.
- **Bundles name the layout literally.** dotdash's client binds
  `/etc/dotdash-client/config.json` by its path (`compose.yaml:33`). Compose
  gets no variable for a second configuration file: `<PRODUCT>_CONFIG_FILE`
  names one (`pages/docs/reference/variables.md`). Its manifest declares
  `render_to: /run/dotdash-client/secrets` (`manifest.yaml:41`), and its
  Compose file reaches the secrets through `${DOTDASH_CLIENT_SECRETS_DIR}`
  (`compose.yaml:66-69`). A layout that moved `EtcDir` would break the first;
  one that keeps `/run` breaks the second.
- **`--root` relocates every path** (`PathsUnder`, `paths.go:72`), and
  configuration targets get the prefix through `configTarget`
  (`internal/lifecycle/ops/ops.go:175`). A Compose file that binds
  `/etc/<p>/...` by its literal path then misses the rendered file, so
  `--root` is no route for a bundle like this one.
- **Secret ownership is refused on darwin.** `SecretSchema.OwnerRefusal`
  (`secret.go:337`) refuses any declared `owner`, because "what a container
  sees for a file given to another user is not measured yet". `Render` calls
  it first (`sopsage.go:450`). `apply` checks every rendered file is `0400`
  and owned as declared (`internal/lifecycle/ops/apply.go:430-451`).
  dotdash's client declares `owner: "65532:65532"` on all four of its
  secrets.
- **The supervisor is systemd.** It installs `<p>.service`, which runs
  `apply --startup` at boot (`systemd.go:541`). It also installs five
  service-and-timer pairs, for backup, update, fleet, doctor and metrics
  (`systemd.go:508-518`). `Available` is false wherever systemd is absent,
  and every call site handles a nil supervisor (RFC 0029 §3.2).
- **The lock's liveness check is a stub on darwin.** `pidStart` returns 0
  (`pidstart_darwin.go:34`), which falls back to the PID alone. Its comment
  defers the real `unix.SysctlKinfoProc` to "P2 … with something on a Mac to
  run it".
- **The ephemeral-storage check reads `/proc/mounts`** (`mounts.go:27`). On
  darwin it cannot answer, and an empty answer reads as OK (RFC 0029 §3.4).
- **What Docker engines on macOS do**, from their documentation:
  - Containers run in a Linux VM.
  - Docker Desktop's host networking is opt-in from 4.34, works at layer 4
    only, and gives a container the VM's network, not the Mac's
    (docs.docker.com, host network driver).
  - Docker Desktop shares `/Users` by default; a third-party source adds
    `/private`, `/tmp` and `/var/folders`, unverified.
  - Its ownership mapping for bind mounts under VirtioFS is undocumented, and
    issue reports conflict.
  - It runs as the logged-in user and does not start before a login. Colima
    and Lima can run as a launchd daemon at boot.
  - It needs a paid subscription for an organisation over 250 people or
    $10 million a year.
- **CI cannot run Docker on Apple silicon.** GitHub's arm64 macOS images
  expose no nested virtualisation (RFC 0029 §3.5), and that holds today.

## 4. Goals / Non-goals

**Goals**

- A release that opts in installs, applies, updates, rolls back, backs up,
  restores and is checked by `doctor` on a Mac, with the commands and journal
  it has on Linux.
- An installation on a Mac comes back after a reboot and keeps its schedules
  without anyone running morzer.
- Every guarantee that is weaker on macOS is named in `doctor` on every run,
  and is a failure wherever a check can make it one. The docs name it too.
- dotdash's client, base profile, runs on a Mac with its four owned secrets.
- macOS is verified against a real Docker engine on Apple silicon before each
  release.

**Non-goals**

- **Whole-machine routing on a Mac.** No profile run in a container can do
  it (§5.7). It needs the proxy running natively, which is a runtime that is
  not Compose, and that is another RFC.
- **Windows.** It is being investigated separately. Nothing here assumes the
  answer, and `requirements.platforms` takes no `windows` value until a design
  gives it one.
- **A per-user or rootless installation on a Mac.** The manager runs as root,
  as on Linux, because it gives secrets away and writes `/private/etc`
  (§5.3). A per-user layout would be a second layout with its own guarantees.
- **A RAM disk for secrets.** It needs a second lifecycle to create, mount
  and clean up after a crash, before the engine's file sharing can see it. It
  is named in §8 as the escape hatch, and it is not built.
- **Any change to Linux.** Paths, units and checks stay as they are, and so
  does a release that does not declare `requirements.platforms`. Platform
  differences go in per-OS files (RFC 0029 decision 8).

## 5. Design

### 5.1 A release opts in

```yaml
requirements:
  platforms: [linux, darwin]  # default: [linux]
runtimes:
  compose:
    profiles:
      tun: [compose/tun.yaml]
    platforms:
      tun: [linux]            # this profile is refused on darwin
```

- `requirements.platforms` lists the platforms a release has been made for.
  Absent, it is `[linux]`, so every release published so far is refused on a
  Mac. The refusal comes at `init`, `update` and `rollback`, before anything
  is staged. It names the platform and the release, and its hint says the
  vendor declares `darwin` once they have run it there. This is RFC 0010's
  rule: the permissive side is the one enumerated.
- It is a key of its own, not a value of `requirements.os`. That list holds
  `{id, version}` distributions, warns, and passes when empty (§3), and this
  one refuses, with absence meaning `[linux]`. One field cannot mean both. A
  release may declare both, with `os` naming the Linux distributions it was
  tested on. On darwin, `system.os` has no distribution to compare, so it
  says the list names Linux distributions rather than warning that the host
  is unknown.
- `runtimes.<runtime>.platforms` narrows single profiles. A profile listed
  there is refused on a platform it does not name, as `init --profile`
  refuses an undeclared profile (RFC 0053). This happens wherever a selection
  is made or checked, so a stored selection is refused at `update` too. A
  profile not listed runs wherever the release does.
- `release verify` checks the shape: values from `{linux, darwin}`, a
  profile's list within `requirements.platforms`, and the floor. It cannot
  check that a release works on a Mac, and the docs say so.
- Declaring either key needs `compatibility.min_manager_version: "0.16.0"`,
  so a 0.15.0 manager refuses the unknown key instead of ignoring it. The
  floor is the next minor, and if a patch release comes first that is a
  logged departure.

### 5.2 The layout

On darwin, `DefaultPaths` is a per-OS file returning:

| Directory | Linux | darwin | Why |
| --- | --- | --- | --- |
| `EtcDir` | `/etc/<p>` | `/etc/<p>` (`/private/etc/<p>`) | Bundles bind it by its literal path |
| `VarDir` | `/var/lib/<p>` | `/var/lib/<p>` (`/private/var/lib/<p>`) | Same path; `/var/lib` is created |
| `OptDir` | `/opt/<p>` | `/opt/<p>` | A firmlink, so root can create it |
| `RunDir` | `/run/<p>` | `/var/run/<p>` (`/private/var/run/<p>`) | No `/run` on the sealed volume |

The manager writes the canonical path through the symlink, and
`atomicfs` resolves it. What Compose is handed, and what a bind names, is the
path the bundle wrote. Whether the engine accepts `/etc/...` or needs
`/private/etc/...` is spike S1.

A declared `render_to` under `/run/` is read under `/var/run/` on darwin. It
is the one translation, it is documented, and the variable Compose gets says
where the files went. A bundle that names its secrets by a literal `/run` path
in Compose, rather than by `<PRODUCT>_SECRETS_DIR`, has not been made for
darwin and should not declare it. A `synthetic.conf` entry for `/run` is
rejected in §5.11.

### 5.3 The engine, and root

The manager runs as root and drives the engine through the `docker` CLI, as on
Linux. On a Mac that means:

- root's `docker` has to reach a daemon that belongs to a user. Docker Desktop
  provides `/var/run/docker.sock` once its "default Docker socket" setting is
  allowed. Spike S3 measures it.
- `preflight` and `doctor` check that the daemon answers as root and that its
  file sharing covers `EtcDir`, `VarDir`, `OptDir` and `RunDir`. A bind outside
  the shared paths fails as "mounts denied". The check is to run a throwaway
  container that binds each directory and lists it, using the bundled helper
  image the volume capture already uses. Reading Docker Desktop's settings
  file is the alternative, and it is undocumented.
- `doctor` warns when the engine's version is outside the range the
  self-hosted lane last verified (§5.10), since its sharing and ownership
  behaviour is the part least pinned by documentation.
- **Which engines are supported is decision 7.** The lean is Docker Desktop
  alone in P1, because it is what the people this is for have installed.
  Colima, OrbStack and Rancher Desktop then work if they pass the same checks,
  and they are not claimed.

### 5.4 Secrets on a disk

There is no memory-backed filesystem on macOS. On darwin, decrypted secrets
render under `/var/run/<p>/secrets`, `0700` and `0400` as on Linux, and every
rendered-file check stays. What changes is stated and checked:

- `FilesystemType` learns darwin through `statfs(2)`'s `f_fstypename`, as
  RFC 0029 §5.6 designed. `apfs` stops falling through to "cannot determine".
- `doctor` reports `secrets.ephemeral-storage` as a warning on darwin. It is not
  actionable, so the hint says the platform has no memory-backed filesystem
  and does not say "mount a tmpfs".
- `doctor` checks FileVault (`fdesetup status`) and **fails when it is
  off**. Decrypted secrets on an unencrypted disk are a credential anyone with
  the disk can read.
- **Every render refuses on darwin while FileVault is off.** The check sits
  where `OwnerRefusal` does, first in the store's `Render`
  (`sopsage.go:450`), so no path that writes plaintext can miss it. Four
  paths render today: `apply` (`apply.go:407`), a failed `update`'s
  compensation, which renders the old release's secrets back
  (`update.go:1188`), `secret render` (`internal/cli/secret.go:241`), and the
  restart after `secret set`, `generate`, `rotate` and `edit`
  (`restartDependents`, `secret.go:490`).
- **The override is a setting, `secrets.allow_unencrypted_disk`, not a
  flag.** The renders that matter most run unattended, `apply --startup` at
  boot and a scheduled update, and nobody is there to pass a flag. The store
  is given the setting when it is built. Setting it is a downgrade, so it
  runs as a journaled `config` operation, as turning
  `release.require_signature` off does (`signatureDropped`,
  `internal/lifecycle/ops/settings.go:797`). `doctor` keeps failing while
  FileVault is off, set or not (decision 5).
- Whether macOS clears `/private/var/run` at boot is spike S5. If it does,
  boot behaves as on Linux, where the render is redone by `apply --startup`.
  If it does not, `stop`, `installation remove` and the boot job remove the
  secrets directory themselves.

### 5.5 Secret ownership

`OwnerRefusal` exists because nobody has measured what a container sees.
Spike S2 measures it under Docker Desktop with VirtioFS. The test is a `0400`
file owned on the Mac by `65532:65532`, mounted as a Compose `secrets:` file
into a container running as `65532`, then as `0` and as `1000`. There are
three outcomes, and the decision follows the measurement:

- **The host uid passes through.** `65532` reads it and the others do not.
  The refusal is lifted for the supported engine, and ownership means what it
  means on Linux.
- **Everything maps to the container's user.** Every uid reads it, so `owner`
  restricts nothing. The refusal is replaced by a warning in `doctor`, and the
  docs say `owner` is not enforced on macOS.
- **Everything maps to root.** `65532` cannot read it. The refusal stays, and
  dotdash's client cannot run on a Mac until it reads its secrets as root.
  That is a vendor change, with its own trade-off.

`apply`'s ownership check compares the host's owner with the declaration, and
it stays the same in all three cases. It checks the file morzer wrote, not
what the VM shows.

### 5.6 The supervisor

`internal/adapters/supervisor/launchd` implements `ports.Supervisor` with
LaunchDaemons in `/Library/LaunchDaemons`, labelled `dev.morzer.<p>` and
`dev.morzer.<p>-backup` and so on, one per systemd unit:

- **Boot convergence retries until the engine answers.** Docker Desktop is
  not running at boot; it starts at login. Its `/var/run/docker.sock` is a
  symlink that can exist before the engine answers, so an event on that path
  says nothing reliable about readiness, and the job waits on none.
  `dev.morzer.<p>` runs `apply --startup` from `RunAtLoad`, with `KeepAlive`
  set to `{SuccessfulExit: false}` and a `ThrottleInterval` of 30 seconds, so
  launchd starts it again after every failed attempt. That is the systemd
  unit's `Restart=on-failure` and `RestartSec=30s` (`systemd.go:546-548`).
  An attempt that cannot reach the engine exits with the runtime's exit code,
  7, before it takes the lock, so an operator's command never queues behind
  a wait for a login. The containers' own restart policies bring them back
  when the engine starts anyway. The job exists for the render and the
  checks, as it does on Linux.
- **Exit 12 still stops the retries.** launchd has no counterpart of
  `RestartPreventExitStatus=12`, and restarting on it would bury the reason
  under a retry every 30 seconds. The lean: an `apply --startup` that ends in
  exit 12 records it, and while the record stands the job exits 0 at once
  without converging, at a retry or at the next boot. An operator's `apply`
  clears it. The reason stays in the log, the journal and `doctor`.
- **Schedules.** The timers become `StartCalendarInterval`. launchd's grammar
  is a set of minute, hour, weekday, day and month fields. It is not systemd's
  `OnCalendar`, so `ValidateSchedule` accepts what translates exactly (fixed
  times daily, weekly, monthly, or hourly at a minute) and refuses the rest at
  `config set`, naming the form it takes. The port already exists for this
  question (`supervisor.go`, `ValidateSchedule`).
- **A run missed while the Mac was off is caught up.** launchd starts a
  `StartCalendarInterval` job missed during sleep when the Mac wakes, but not
  one missed while it was off. The systemd timers for backup, update, fleet
  and doctor are `Persistent=true` for that case (`systemd.go:581`), and
  metrics is not. On darwin each of those four jobs writes a stamp under
  `VarDir` when it runs, as systemd keeps a stamp per persistent timer. Once
  boot convergence succeeds, so the engine is up, `apply --startup` runs
  `launchctl kickstart` once for each job whose latest scheduled time is
  later than its stamp. Metrics is not caught up, as on Linux.
- **Logs.** `StandardOutPath` and `StandardErrorPath` go under
  `VarDir/logs`, since the manager logs JSON as it does under journald.
- `Available` is true where `launchctl` runs and the manager is root.
  `Status` reads `launchctl print system/<label>`.

### 5.7 Profiles that route the machine

A container on macOS runs in the engine's Linux VM:

- its `network_mode: host` is the VM's network, at layer 4 only;
- `/dev/net/tun` is the VM's device;
- a TUN interface there routes the VM's traffic, never the Mac's.

dotdash's `tun` profile (`client/compose/tun.yaml:13-15`) therefore cannot do
on a Mac what it promises, and starting it would look like success. It is the
case `runtimes.compose.platforms` exists for: the vendor marks it `[linux]`, and
morzer refuses it on darwin.

On a Mac, dotdash's client is its proxy. Applications use `127.0.0.1:7890`
directly, or through the macOS system proxy settings. Setting those is the
operator's. A hook that changes the Mac's network settings would be the
manager reaching outside its installation, and it is not built here.

### 5.8 The lock, and other darwin stubs

- `pidStart` becomes `unix.SysctlKinfoProc`'s `p_starttime`, converted to the
  unit the record stores. The stub's own argument for waiting, that nothing
  runs it, ends with the macOS lane (§5.10), and an unattended host needs
  stale-lock detection that works. Its test runs on that lane, and asserts a
  live process's start time is stable, and that a recorded start differing by
  one tick is a contradiction.
- `memory_darwin.go` and `space_darwin.go` exist. They gain tests on the
  macOS lane, which is the first time they run.

### 5.9 The binary

- `goreleaser` adds `darwin` for `amd64` and `arm64`, and `install.sh` takes
  `Darwin` into the archive name it already computes (RFC 0029 §5.4, which
  measured both edits).
- `self_update.go` accepts darwin. On Apple silicon the binary has to be
  signed, and Go's linker signs darwin/arm64 binaries ad hoc, cross-compiled
  included. A file `curl` downloads carries no quarantine attribute, so
  Gatekeeper does not check it. Spike S6 confirms both on the macOS versions
  supported. Notarisation is out (§8).
- Hooks are the vendor's: they run under macOS's userland, BSD tools and
  `/bin/sh`. A release that declares darwin owns their portability, and the
  docs say so. dotdash's `gateway` check reads `/proc` and runs `iptables`
  (`client/hooks/gateway:13-17`); it belongs to the `tun` profile, and the
  vendor gates it.

### 5.10 Verification

Hosted Apple-silicon runners cannot run Docker (§3), so three lanes split the
work:

- **Hosted `macos-latest`, every push.** `go test` for the packages that need
  no Docker, which proves the darwin syscall paths (statfs, sysctl, termios,
  memory) that cross-compiling cannot. This is RFC 0029 §5.7's lane.
- **A self-hosted Apple-silicon runner with Docker Desktop, required on
  release PRs and on a nightly schedule** (decision 9). It runs
  `just acceptance` under the darwin layout. The acceptance script gains a
  darwin path for its tmpfs and ownership checks. The runner is a real Mac:
  someone has to provide it, and the docs say macOS is verified there and
  nowhere else.
- **`darwin-check`** stays.

A release with the self-hosted lane red does not ship macOS archives. A lane
that cannot run is a stated gap. A lane quietly removed would be a false
claim.

### 5.11 Alternatives considered

- **Keep RFC 0029's tier and run production on Linux only.** It is coherent
  and costs nothing, but it leaves the client's users needing a second
  machine for a proxy their laptop could run. It loses on the goal, not on
  the design.
- **An Apple-native layout** (`/Library/Application Support/morzer/<p>`).
  It loses because bundles bind `/etc/<p>` by its literal path. Every bundle
  would need rewriting, or the manager would have to rewrite Compose files,
  and it does not touch them.
- **`synthetic.conf` for `/run`.** It keeps every path identical. It loses
  because it edits a system file the operator may already use, needs a
  reboot, is hard to undo cleanly, and the result is a symlink to the same
  disk. That buys identical paths with no gain in what the guarantee is.
- **A RAM disk under `/Volumes`.** The engine shares `/Volumes`, so it
  would work. It loses for now because it is a second lifecycle (create,
  size, mount before the engine, reattach after a crash, never leak), on top
  of a FileVault check that already answers who can read the disk. §8 names
  what reopens it.
- **Run the proxy natively and skip containers on a Mac.** It is the only
  route to whole-machine routing. It loses here because it is a new runtime
  (images become binaries, `restart` becomes launchd's), which is RFC 0023's
  territory and a separate RFC.
- **`id: macos` in `requirements.os`.** It reuses a field that exists. It
  loses because that field is the other way round: it warns rather than
  refuses, and an empty list allows every host (§3). Turning it into a gate
  would refuse a Linux release that lists one distribution on every other
  distribution, and leaving it advisory would let every release that never
  declared it run on a Mac.
- **`WatchPaths` on the engine socket for boot convergence.** It reacts to
  the engine appearing rather than polling. It loses because the socket is a
  symlink that can exist before the engine answers, and an attempt that fails
  on the only event is never retried.

## 6. Tests

- **Domain:**
  - `requirements.platforms` and `runtimes.<r>.platforms`: shape, default,
    the floor;
  - beside `requirements.os`, which keeps its warning on Linux and its
    darwin wording;
  - the refusal at `init`, `update` and `rollback` for the release and for a
    profile, a stored selection included;
  - `release verify`'s messages.
- **Layout:** `DefaultPaths` per OS, the `render_to` translation, and
  `PathsUnder` unchanged. The darwin cases run on the macOS lane.
- **launchd adapter:**
  - plist rendering, golden files against the systemd unit set;
  - `ValidateSchedule`: every accepted form and the first refused one;
  - `Status` parsing from captured `launchctl print` output;
  - the exit-12 record: set by `apply --startup`, honoured at the next
    start, cleared by `apply`;
  - the catch-up: a stamp older than the latest scheduled time starts the
    job once, a current stamp starts nothing, and metrics is never caught
    up.
- **Secrets:** the FileVault gate on each of the four render paths, and the
  setting that overrides it, journaled; `doctor`'s darwin wording; the
  ownership rule S2 decides, as a contract case per outcome.
- **The macOS lane** runs `pidStart`, `FilesystemType`, memory and space for
  real.
- **The self-hosted lane** runs the acceptance run under the darwin layout:
  - a release declaring darwin installs and survives an update, a rollback, a
    backup and a restore, and a reboot of the runner on the nightly;
  - a release that does not declare darwin is refused;
  - a `[linux]` profile is refused.
- **Sabotage:** drop the default `[linux]`, skip the profile refusal on
  `update`, render through `secret render` with FileVault off, drop
  `KeepAlive` from the boot job, and translate `render_to` on Linux. Each
  must turn a test red.

## 7. Docs

- **A new "Platforms" page** says what macOS is and is not:
  - root, the paths, the engine supported;
  - secrets on an encrypted disk;
  - no machine-wide routing;
  - what is verified, and where.
- **Authoring:** `requirements.platforms`, `runtimes.<r>.platforms`, how
  they differ from `requirements.os`, hook portability, and why a bundle that
  names `/run` literally in Compose is not darwin-ready.
- **Reference:**
  - `manifest.md` for both keys and the floor;
  - `commands.md` for the refusal;
  - `parameters.md` for `secrets.allow_unencrypted_disk`;
  - the darwin `doctor` checks;
  - the launchd schedule forms beside the systemd ones.
- **Get started:** installing on a Mac, with Docker Desktop's "default Docker
  socket" setting.
- **Wording that must hold:** "verified on Apple silicon with Docker Desktop
  <version>". Never "works on macOS" alone.

## 8. Out of scope

- **Whole-machine routing on a Mac.** It needs a native runtime, and a design
  for running a vendor binary as a launchd job with the guarantees an image
  gives (digest, signature, rollback). That is reopened by an RFC for that
  runtime.
- **A RAM disk for secrets.** It is reopened if FileVault turns out not to be
  enough for a vendor, or if the engine stops sharing `/Volumes`.
- **Notarisation and a signed installer package.** `curl | sh` delivers an
  unquarantined binary. A `.pkg` would be its own distribution channel, and
  it is reopened by a user who cannot use the script.
- **Engines other than Docker Desktop being claimed.** They are reopened by
  someone running the self-hosted lane against one.
- **The dotdash client's own changes:** declaring `platforms`, marking `tun`
  `[linux]`, and, if S2 says root, reading its secrets as root. They belong
  in morzecrew/dotdash.

## 9. Risks

- **The spike invalidates the layout.** If the engine will not share
  `/private/etc`, then `EtcDir` cannot stay where bundles bind it. That is
  why P0 runs first and P1 waits for it. A failed S1 sends this back to the
  author with the measurement, not into a workaround.
- **Docker Desktop changes under us.** Its sharing, socket and ownership
  behaviour are undocumented in places and change between versions. The
  self-hosted lane pins the version it ran, the docs name it, and `doctor`
  checks the engine's version against the verified range.
- **"Production" is read as "the same as Linux."** Secrets touch an encrypted
  disk. Convergence waits for a login while the engine is Docker Desktop.
  Routing is per-application. The Platforms page, and `doctor` on every run,
  say each of these. The word "production" appears in the docs only with
  them.
- **Docker Desktop's licence.** An organisation over the threshold needs a
  subscription to run it, even for one proxy. The docs say so, and the engine
  decision (7) keeps Colima as the next one to verify.
- **A self-hosted runner is a machine someone maintains,** and a runner that
  is down blocks a release's macOS archives. It does not block Linux: the
  darwin archives are dropped from that release, and the changelog says so.
- **WSL2 is `linux` to this design.** A manager in a WSL2 distro is the Linux
  build, so a release declaring `[linux]` installs there. A `[linux]` TUN
  profile would then start and route only the WSL VM, the same false success
  §5.7 refuses on a Mac. Telling WSL apart, as its own
  `requirements.platforms` value or a check in `doctor`, belongs to the
  Windows design. Until then the docs say a TUN profile under WSL2 routes
  nothing of Windows.
- **`requirements.platforms` defaults strictly.** Every published bundle is
  refused on a Mac until its vendor opts in. That is the intent. Error text
  that sounds like a manager bug would be the risk, so the refusal names the
  vendor's step.
- **`platforms` and `os` read alike.** A vendor may write `os: [darwin]` and
  expect it to opt in. It fails to decode wherever the manifest is read,
  `release verify` included, since an `os` entry is a mapping ("string was
  used where mapping is expected"). The authoring page sets the two side by
  side.

## 10. Unresolved questions

The P0 spike settles these on a real Mac with Docker Desktop. Each one names
the decision it can overturn:

- **S1:** do binds of `/etc/<p>/…`, `/private/var/lib/<p>/…`, `/opt/<p>/…` and
  `/private/var/run/<p>/…` work by default, and by which spelling?
  (Decision 4.)
- **S2:** what owner does a container see for a `0400` file owned by
  `65532:65532`? (Decision 6.)
- **S3:** does root's `docker` reach Docker Desktop through
  `/var/run/docker.sock`, and what does enabling that take? (Decision 7.)
- **S4:** does a LaunchDaemon with `RunAtLoad`, `KeepAlive`
  `{SuccessfulExit: false}` and a 30-second `ThrottleInterval` retry until
  Docker Desktop answers after a login, and stop once the exit-12 record
  makes it exit 0? Does `launchctl kickstart` from the boot job start a
  missed backup? (Decision 8.)
- **S5:** is `/private/var/run` cleared at boot? (§5.4.)
- **S6:** does an ad-hoc-signed darwin/arm64 binary from `curl` run, and does
  `self-update` replacing it keep running? (§5.9.)

Who runs the spike, and on which Mac and macOS version, is the author's to
say.

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `LOCKED` | macOS is a supported host for an installation that runs unattended. This supersedes RFC 0029 decision 1, and RFC 0029's decisions 4 (no launchd) and 6 (refuse unrooted on darwin) with it. Each weaker guarantee is stated in `doctor` and the docs, never left implicit. Reversing this later means withdrawing darwin archives from operators who depend on them. |
| 2 | `LOCKED` | A release runs on macOS only if it declares `requirements.platforms` containing `darwin`, and the default is `[linux]`. Every existing bundle is refused on a Mac until its vendor opts in (RFC 0010's rule). |
| 3 | `OPEN` | The per-profile restriction's shape. The lean is `runtimes.<runtime>.platforms: {<profile>: [platform…]}` beside `profiles` and `combinable`, refused wherever a selection is made or checked. The alternative is to make each `profiles` entry a mapping with `files` and `platforms`, which changes a shape every bundle uses. Both need the 0.16.0 floor. |
| 4 | `ASSUMED` | On darwin the layout keeps `/etc/<p>`, `/var/lib/<p>` and `/opt/<p>` through `/private` and the `/opt` firmlink. `RunDir` is `/var/run/<p>`, and a declared `render_to` under `/run/` is read under `/var/run/`. It rests on S1. If the engine will not share those paths, this row is superseded with the measurement. |
| 5 | `OPEN` | Secrets on disk. The lean: they render under `/var/run/<p>/secrets`; `doctor` warns that the platform has no memory-backed filesystem and **fails** when FileVault is off; and every render refuses with FileVault off, the check first in the store's `Render` so `apply`, `update`, `secret render` and the restart after a `secret` change all reach it. The override is the setting `secrets.allow_unencrypted_disk`, not a flag, because the boot job and a scheduled update render with nobody to pass one; setting it is a journaled downgrade. The alternative is a warning alone. A RAM disk is out (§8). |
| 6 | `OPEN` | Secret `owner` on macOS follows S2's measurement (§5.5): lifted if the host uid passes through; replaced by a stated non-enforcement if every uid reads the file; kept refused if root owns it in the VM. The author rules which outcome is acceptable before S2 runs, so the measurement decides and nobody argues it afterwards. |
| 7 | `OPEN` | The supported engine. The lean is Docker Desktop alone in P1, verified on the self-hosted lane. Others work if they pass preflight and are not claimed. The alternative is Colima first: it starts at boot with no login and has no licence threshold, but it is not what the bundle's users have installed. |
| 8 | `ASSUMED` | The supervisor is a launchd adapter of LaunchDaemons, one per systemd unit. Boot convergence runs from `RunAtLoad`, and `KeepAlive` `{SuccessfulExit: false}` retries it every 30 seconds until the engine answers, with no path event involved; a recorded exit 12 makes it exit 0 until an operator's `apply`. Schedules translate to `StartCalendarInterval` exactly or are refused by `ValidateSchedule`, and the four jobs systemd marks `Persistent` are caught up at boot from their stamps. It rests on S4. |
| 9 | `OPEN` | Verification. The lean has three parts: a hosted `macos-latest` unit lane on every push; a self-hosted Apple-silicon runner with Docker Desktop, required on release PRs and nightly; and darwin archives dropped from a release whose self-hosted lane is red. The author says whose Mac the runner is. Without a runner, this RFC does not ship P1, because a production claim with no real-engine run is the thing RFC 0029 §5.7 called worse than none. |
| 10 | `LOCKED` | No profile is offered as routing a Mac's traffic. A TUN or `network_mode: host` profile on macOS acts on the engine's VM, so a release marks it Linux-only, and whole-machine routing is a native-runtime RFC. |
| 11 | `ASSUMED` | `pidStart` on darwin uses `unix.SysctlKinfoProc` and is tested on the macOS lane, which ends the reason the stub gave for waiting. |
| 12 | `ASSUMED` | The darwin archives cover `amd64` and `arm64`, `install.sh` and `self-update` accept darwin, and the binary relies on Go's ad-hoc signature with no notarisation. It rests on S6. |
| 13 | `ASSUMED` | The manager runs as root on macOS, as on Linux. A per-user installation is a separate layout with separate guarantees, and is not designed here. |
| 14 | `ASSUMED` | Linux is unchanged by construction: per-OS files, no runtime branches on `GOOS` beyond those that exist (RFC 0029 decision 8). A Linux-only release behaves as today. |
| 15 | `ASSUMED` | The floor for `requirements.platforms` and `runtimes.<r>.platforms` is 0.16.0, the next minor. A release in between moves it, logged as a departure. |

## 12. Phasing

**P0 — the spike.** S1–S6 run on a real Mac with Docker Desktop, and the
results are appended to this RFC as measurements. Decisions 4, 6, 7 and 8 are
ruled or superseded on them. P1 waits for S1, S2 and S3. If any of those
three fails, this RFC goes back to the author with the measurement.

**P1 — an installation on a Mac.**
- The manifest keys, their refusals and the floor (§5.1).
- The darwin layout and the `render_to` reading (§5.2).
- The engine preflight (§5.3), the FileVault gate and `doctor` on darwin
  (§5.4).
- The ownership rule from S2 (§5.5).
- `pidStart` (§5.8), the archives, `install.sh` and `self-update` (§5.9).
- The macOS unit lane and the self-hosted acceptance lane (§5.10).

Without a supervisor, an installation converges when an operator runs `apply`,
and the engine restarts its containers. P1 alone is usable, and it says so.

**P2 — the supervisor.** The launchd adapter, boot convergence and schedules
(§5.6), with the self-hosted lane's reboot run.

**P3 — the client bundle**, in morzecrew/dotdash: `requirements.platforms`,
`tun` marked `[linux]`, and whatever S2 asks of its secrets. It is gated on P1
being released.
