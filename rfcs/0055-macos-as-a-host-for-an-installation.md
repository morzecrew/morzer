# RFC 0055 — macOS as a host for an installation

- **Status:** 📝 Draft — proposed 2026-11-09, and ruled by the author
  2026-11-11: decisions 3, 5 and 7 as recommended; 6 before S2, accepting the
  first two outcomes; 9 with a release-time acceptance run by hand in place of
  a self-hosted runner; and 12 narrowed to `arm64`. The author runs the P0
  spike on an Apple-silicon Mac mini, and P1 is gated on its measurements
  (§12). P0 ran 2026-11-14 (§13). S1 failed, and S2 found an outcome §5.5
  did not list, so §12 sent the RFC back to the author, who ruled the same
  day: decisions 4 and 6 are superseded by 16 and 17, and decision 18 keeps
  root from running Docker Desktop's binaries where another user can replace
  them. With those rulings, P1 can start. Its plan found four questions this
  RFC did not settle, and the author ruled them on 2026-11-15 (decisions
  19–22). Building the engine found two more, and the author ruled them on
  2026-11-16 (decisions 23 and 24).
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
  the disk can read. So is the age identity, which `init` writes to
  `EtcDir/age/identity` (`AgeIdentityFile`, `paths.go:107`), beside the state
  it decrypts. Whoever has the disk and that file reads every secret, rendered
  or not.
- **`init` refuses on darwin while FileVault is off**, before any step
  writes, so no identity is created on an unencrypted disk. Its
  `--allow-unencrypted-disk` is the one flag form of the override: there is
  no installation yet to hold a setting, so `init` records it as the setting
  below, journaled with the rest of `init`.
- **An identity already on disk when FileVault is turned off** cannot be
  protected after the fact. `doctor` fails, and its hint says turning
  FileVault back on encrypts the volume in place, and that a disk that left
  the operator's hands meanwhile means the identity and every secret are to
  be treated as exposed. On Linux the identity sits on an unchecked disk, and
  this RFC leaves that as it is (§4).
- **Every render refuses on darwin while FileVault is off.** The check sits
  where `OwnerRefusal` does, first in the store's `Render`
  (`sopsage.go:450`), so no path that writes plaintext can miss it. Four
  paths render today: `apply` (`apply.go:407`), a failed `update`'s
  compensation, which renders the old release's secrets back
  (`update.go:1188`), `secret render` (`internal/cli/secret.go:241`), and the
  restart after `secret set`, `generate`, `rotate` and `edit`
  (`restartDependents`, `secret.go:490`).
- **A `secret` change checks the gate before it stores anything.** `secret
  set`, `generate`, `rotate` and `edit` save the new value first and render
  after (`rotate` at `secret.go:176-181`). With the check only in `Render`,
  the stored value would change while the running services keep the old one,
  and the command could not finish the change. So each of them runs the same
  check before it stores, and the check in `Render` stays for every other
  path.
- **The override is a setting, `secrets.allow_unencrypted_disk`, not a
  flag** once the installation exists. The renders that matter most run
  unattended, `apply --startup` at boot and a scheduled update, and nobody is
  there to pass a flag. The store
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
- **A run that cannot reach the engine leaves its stamp alone.** A calendar
  job can fire after boot and before a login starts Docker Desktop. If it
  stamped, it would move the stamp past a run missed while the Mac was off,
  and the catch-up would find nothing to do. So a job writes its stamp only
  once it has reached the engine. One that cannot exits 7 with the stamp as
  it was, and the next boot convergence catches the run up. Any other failure
  stamps, as a systemd timer's stamp moves whether its run succeeded or not.
- **The catch-up runs one job at a time.** Backup and update both take the
  deployment lock, and no unit passes `--wait` (`systemd.go:565`, `:605`), so
  two jobs kickstarted together would leave one refused with exit 4. The
  boot job releases the lock, then starts the overdue jobs in a fixed order,
  backup, update, fleet, doctor, and waits for each to exit, read from
  `launchctl print`, before the next.
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
    up;
  - a job that fires before the engine answers exits 7 and leaves its
    stamp, so the catch-up after convergence still runs it;
  - overdue backup and update run one after the other, in order, and
    neither exits 4.
- **Secrets:**
  - the FileVault gate at `init` before the identity is written, on each of
    the four render paths, and before a `secret` change stores its value, so
    a refused rotation leaves the stored value as it was;
  - `init --allow-unencrypted-disk` recording the setting, and the setting
    that overrides the gate, journaled;
  - `doctor`'s darwin wording, an identity on a disk whose FileVault was
    turned off included;
  - the ownership rule S2 decides, as a contract case per outcome.
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
  - `commands.md` for the refusal and `init --allow-unencrypted-disk`;
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
| 3 | `ASSUMED` | The per-profile restriction's shape. The lean is `runtimes.<runtime>.platforms: {<profile>: [platform…]}` beside `profiles` and `combinable`, refused wherever a selection is made or checked. The alternative is to make each `profiles` entry a mapping with `files` and `platforms`, which changes a shape every bundle uses. Both need the 0.16.0 floor. Was `OPEN`; ruled by the author 2026-11-11: as recommended. |
| 4 | `ASSUMED` | On darwin the layout keeps `/etc/<p>`, `/var/lib/<p>` and `/opt/<p>` through `/private` and the `/opt` firmlink. `RunDir` is `/var/run/<p>`, and a declared `render_to` under `/run/` is read under `/var/run/`. It rests on S1. If the engine will not share those paths, this row is superseded with the measurement. *(Superseded by decision 16.)* |
| 5 | `ASSUMED` | Secrets on disk. The lean: they render under `/var/run/<p>/secrets`; `doctor` warns that the platform has no memory-backed filesystem and **fails** when FileVault is off, since the age identity is on that disk too; `init` refuses with FileVault off before it writes the identity, unless `--allow-unencrypted-disk`, which records the setting below; and every render refuses with FileVault off, the check first in the store's `Render` so `apply`, `update`, `secret render` and the restart after a `secret` change all reach it, and a `secret` change runs the same check before it stores the value. The override is the setting `secrets.allow_unencrypted_disk`, not a flag, because the boot job and a scheduled update render with nobody to pass one; setting it is a journaled downgrade. The alternative is a warning alone. A RAM disk is out (§8). Was `OPEN`; ruled by the author 2026-11-11: as recommended. |
| 6 | `ASSUMED` | Secret `owner` on macOS follows S2's measurement (§5.5): lifted if the host uid passes through; replaced by a stated non-enforcement if every uid reads the file; kept refused if root owns it in the VM. The author rules which outcome is acceptable before S2 runs, so the measurement decides and nobody argues it afterwards. Was `OPEN`; ruled by the author 2026-11-11, before S2: the first two outcomes are acceptable. If root owns the file in the VM, the refusal stays, and reading its secrets as root is dotdash's change to make. *(Superseded by decision 17.)* |
| 7 | `ASSUMED` | The supported engine. The lean is Docker Desktop alone in P1, verified on the self-hosted lane. Others work if they pass preflight and are not claimed. The alternative is Colima first: it starts at boot with no login and has no licence threshold, but it is not what the bundle's users have installed. Was `OPEN`; ruled by the author 2026-11-11: as recommended, with Colima a candidate for P2 if S4 shows that Docker Desktop starting at login, not at boot, matters to an unattended Mac. |
| 8 | `ASSUMED` | The supervisor is a launchd adapter of LaunchDaemons, one per systemd unit. Boot convergence runs from `RunAtLoad`, and `KeepAlive` `{SuccessfulExit: false}` retries it every 30 seconds until the engine answers, with no path event involved; a recorded exit 12 makes it exit 0 until an operator's `apply`. Schedules translate to `StartCalendarInterval` exactly or are refused by `ValidateSchedule`, and the four jobs systemd marks `Persistent` are caught up at boot from their stamps, one at a time in a fixed order; a run that cannot reach the engine leaves its stamp, so it is caught up too. It rests on S4. |
| 9 | `ASSUMED` | Verification. The lean has three parts: a hosted `macos-latest` unit lane on every push; a self-hosted Apple-silicon runner with Docker Desktop, required on release PRs and nightly; and darwin archives dropped from a release whose self-hosted lane is red. The author says whose Mac the runner is. Without a runner, this RFC does not ship P1, because a production claim with no real-engine run is the thing RFC 0029 §5.7 called worse than none. Was `OPEN`; ruled by the author 2026-11-11, without a self-hosted runner: the hosted `macos-latest` lane on every push, which runs no engine; before each release, the acceptance run by hand on the author's Apple-silicon Mac mini with Docker Desktop, its result recorded in the release PR; and darwin archives left out of a release without that record. |
| 10 | `LOCKED` | No profile is offered as routing a Mac's traffic. A TUN or `network_mode: host` profile on macOS acts on the engine's VM, so a release marks it Linux-only, and whole-machine routing is a native-runtime RFC. |
| 11 | `ASSUMED` | `pidStart` on darwin uses `unix.SysctlKinfoProc` and is tested on the macOS lane, which ends the reason the stub gave for waiting. |
| 12 | `ASSUMED` | The darwin archives cover `amd64` and `arm64`, `install.sh` and `self-update` accept darwin, and the binary relies on Go's ad-hoc signature with no notarisation. It rests on S6. Narrowed by the author 2026-11-11: `arm64` only. Intel Macs are not supported. |
| 13 | `ASSUMED` | The manager runs as root on macOS, as on Linux. A per-user installation is a separate layout with separate guarantees, and is not designed here. |
| 14 | `ASSUMED` | Linux is unchanged by construction: per-OS files, no runtime branches on `GOOS` beyond those that exist (RFC 0029 decision 8). A Linux-only release behaves as today. |
| 15 | `ASSUMED` | The floor for `requirements.platforms` and `runtimes.<r>.platforms` is 0.16.0, the next minor. A release in between moves it, logged as a departure. |
| 16 | `ASSUMED` | On darwin the layout keeps decision 4's paths: `/etc/<p>`, `/var/lib/<p>` and `/opt/<p>` through `/private` and the `/opt` firmlink, `RunDir` at `/var/run/<p>`, and a declared `render_to` under `/run/` read under `/var/run/`. Docker Desktop reaches the Mac's files as the user who runs it, so a container can enter, read and write only what that user can (§13). Root keeps every directory it trusts. `EtcDir`, `VarDir`, `RunDir`, and every directory on the way to a bind, stay root's at `0755` on darwin. A directory bind needs that user to be able to list each directory above it; passing through is not enough (§13, the fourth run). That user owns only the directories a container writes, and the rendered secrets directory and its files, at `0700` and `0400`. Everything else keeps root as owner and its Linux mode: the age identity, the signing key, the state, the manager's staging and locks, the backups and the releases. So no directory on the way to a file root trusts is writable by anyone but root, the rule `ReplaceableByNonRoot` checks (`internal/infra/atomicfs/atomicfs.go:451`). The secrets directory's entry sits in `RunDir`, which only root can change, so that user cannot swap the directory for a link. Root writes into it through `os.Root`, as `Render` already does (`WriteFileInAs`). `/opt` is not in Docker Desktop's default file sharing, so the operator adds it. Preflight checks each directory a container writes by writing to it from a container, since a listing passes where a write fails. Consequence: other accounts on the Mac can list those `0755` directories. They can also read any file a container reads, because that file has to be readable by more than root while no group holds that one user. The secrets stay readable by root and that user alone. A dedicated group with the directories at `0750` would keep Linux's privacy; it is not measured. Ruled by the author 2026-11-14 on §13's measurements, superseding decision 4. The first wording gave that user `EtcDir`, `VarDir` and `RunDir` themselves; the author narrowed it the same day, before it merged, after the fourth run. |
| 17 | `ASSUMED` | Secret `owner` is not enforced inside a container on macOS. A container reads a rendered secret only when the user who runs Docker Desktop can, and then every container uid reads it (§13 S2, an outcome §5.5 did not list). Per decision 16, secrets render owned by that user, `0400` in a `0700` directory. On darwin, `OwnerRefusal` is replaced by a `doctor` warning, and the docs say `owner` is not enforced on macOS, as decision 6's accepted second outcome would have had it. §5.5's host-side check can no longer compare the file's owner with the declaration. What it checks instead is P1's to settle. Ruled by the author 2026-11-14 on §13's measurements, superseding decision 6. |
| 18 | `ASSUMED` | On darwin, root never runs Docker Desktop's CLI or Compose plugin from inside `Docker.app`. That user owns both binaries, and so does every directory from `Docker.app` down; `/Applications` is writable by the admin group (§13, the fifth run). So either could replace what root runs, which `ReplaceableByNonRoot` refuses for the manager's own binary. Preflight copies the CLI and the plugin into a directory under `VarDir/manager`, which only root can write. Before a copy is used, preflight checks it with `codesign --verify --strict` and requires Docker's team identifier, `9BNSXJN65R`. The manager runs only the copies, giving the CLI a config whose `cliPluginsExtraDirs` names the copied plugin's directory. When Docker Desktop's version changes, the copies are made and checked again. Refused: checking the signature in place and running the original, since the file can be replaced between the check and the run. Also refused: requiring `Docker.app` to be root's, which would likely break Docker Desktop's own updates, which run as that user. Ruled by the author 2026-11-14 on §13's fifth run. |
| 19 | `ASSUMED` | On darwin, the user who runs Docker Desktop is the owner of the socket `/var/run/docker.sock` names. The link is root's, and Docker Desktop recreates it as the engine starts (§13 S3 and S4). The manager reads it at every render and every `apply`, so a different user starting the engine later is followed. It refuses when the link is absent, or names a socket root owns, since file sharing as root is not measured. That user owns what decisions 16 and 17 give it. Refused: a setting filled at `init` from `SUDO_USER`, which does not follow a different user; and the owner of `/dev/console`, who may not be the one running the engine. Ruled by the author 2026-11-15 on P1's plan; see logs/macos-host.md. |
| 20 | `ASSUMED` | A release ships its darwin archive only when the tagged tree holds `.github/macos-acceptance/v<version>.txt`: the output of decision 9's acceptance run on the author's Mac, committed in the release PR. Without the file, the release run builds Linux alone, so "left out without that record" is checked by the build rather than remembered. Refused: building darwin always and deleting the asset from the draft by hand; and a separate workflow run after tagging that adds the archive and signs `SHA256SUMS` again. Ruled by the author 2026-11-15 on P1's plan; see logs/macos-host.md. |
| 21 | `ASSUMED` | On a Mac without `Docker.app`, root runs the `docker` CLI it finds on `PATH` only if `ReplaceableByNonRoot` passes for it and for the Compose plugin that CLI would load. The CLI finds plugins in its own directories, not on `PATH`, so the manager's config names the checked plugin's directory in `cliPluginsExtraDirs`, which the CLI searches before the others. Otherwise the manager refuses, naming the entry another user could replace. So decision 7's other engines work when their binaries are root's, and decision 18's reason holds for every engine. Refused: refusing whenever `Docker.app` is missing. Ruled by the author 2026-11-15 on P1's plan; see logs/macos-host.md. |
| 22 | `ASSUMED` | On darwin, every process the manager starts, hooks included, gets the directory of the CLI decision 18 or 21 settled first on `PATH`, and the manager's `DOCKER_CONFIG`. A hook that runs `docker` by name runs the checked copy. A hook that names `/usr/local/bin/docker` by its path runs what that names; that is the vendor's to avoid, and the authoring docs say so. Refused: leaving hooks the environment they inherit. Ruled by the author 2026-11-15 on P1's plan; see logs/macos-host.md. |
| 23 | `ASSUMED` | On darwin, `ReplaceableByNonRoot` also refuses an entry whose ACL grants anyone but root a right that lets them replace it, or give themselves one that does: `write`, `append`, `delete`, `writesecurity` or `chown` on the entry or on a directory on the way to it, and `add_file`, `add_subdirectory` or `delete_child` on such a directory. The rule reads owners and mode bits, and a Mac's ACL can let a named user replace a root-owned file that no mode bit lets them write, or change its ACL or its owner so that they can. So a CLI on `PATH`, its Compose plugin, a credential helper and the manager's own binary are refused when an ACL lets another user replace them. Docker Desktop's copies are checked by their signature, so decision 18 is unchanged. It lands in a pull request of its own, since the rule is shared and reading a Mac's ACLs needs a call `golang.org/x/sys/unix` does not wrap. Ruled by the author 2026-11-16 on P1's engine; see logs/macos-host.md (decision 21, 2026-11-15T18:24:16Z). |
| 24 | `ASSUMED` | On darwin, a hook is refused while an entry in a directory the manager's CLI searches for plugins, or a directory on the way to one, can be replaced by someone other than root, and the refusal names the entry. Those directories are the manager's own `cli-plugins`, the directory decision 21 names in `cliPluginsExtraDirs`, and the CLI's system plugin directories: `/usr/local/lib/docker/cli-plugins`, `/usr/local/libexec/docker/cli-plugins`, `/usr/lib/docker/cli-plugins` and `/usr/libexec/docker/cli-plugins`. A command that lists plugins, `docker info` say, runs every candidate in each of them as root, and decisions 18 and 21 check only the CLI and the Compose plugin the manager names, not what sits beside that plugin. The manager's own commands list no plugins, so they are not refused. The acceptance run records what those directories hold on the author's Mac. If Docker Desktop leaves a link there to a binary inside `Docker.app`, every hook is refused, and the author rules again on that measurement. Refused: saying in the authoring docs that a hook does not list plugins, as decision 22 does for a CLI named by its path, since `docker info` by name is what a hook would ordinarily run. Ruled by the author 2026-11-16 on P1's engine; see logs/macos-host.md (decision 18, 2026-11-15T19:14:25Z). |
| 25 | `ASSUMED` | On darwin, root runs a tool it finds by name only from a directory only root can change. From the start of every command, the manager's `PATH` keeps, after the directories decision 22 puts first, only the directories that pass `ReplaceableByNonRoot` and to which nobody but root can add an entry. `sops`, the tools preflight probes, `secret edit`'s editor and every hook find a tool there or not at all, and a tool found only in a dropped directory is refused, naming who could change it. Decision 21's lookup still searches the `PATH` root was given, so a CLI there is refused by name. `sudo` on a Mac keeps the user's `PATH`, with `~/bin` and Homebrew's directory, which are that user's, ahead of or beside `/usr/local/bin`, so without this the exposure decision 18 closes for the CLI stays open for every other tool. A hook that runs a Homebrew tool by name no longer finds it, which amends decision 22. A tool named by its path runs what the path names, as decision 22 has for a hook. Refused: checking `sops` alone, which leaves the editor and every hook the user's directories; and refusing to run with such a `PATH` at all. Ruled by the author 2026-11-17 on a measurement of a Mac; see logs/macos-host.md (UNLISTED and decision 22, 2026-11-17T16:12:14Z). |
| 26 | `ASSUMED` | On darwin, `doctor`'s `system.units` warns that nothing converges at boot and no schedule runs until the launchd adapter (P2). It names the units the installation would have under systemd: the start at boot, and each schedule it sets for backups, updates, fleet publishing, sync, `doctor` and metrics. The remedy is `sudo morzer apply` after the Mac starts, once Docker Desktop runs. A warning, since the installation runs. P1 reported ok, "systemd is not in use on this host", which left implicit the guarantee decision 1 has stated. Linux without systemd is unchanged. Ruled by the author 2026-11-17; see logs/macos-host.md (decision 1, 2026-11-17T10:45:03Z, and UNLISTED, 2026-11-17T16:12:14Z). *(Ended by P2's launchd adapter, as it said; see logs/macos-host.md.)* |
| 27 | `ASSUMED` | Every LaunchDaemon sets `PATH` to `/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin`. launchd starts a job with `/usr/bin:/bin:/usr/sbin:/sbin` (S4), where the `sops` the install page puts in `/usr/local/bin` is not found. Decision 25 then filters it as it filters the `PATH` `sudo` gives. Ruled by the author 2026-11-17 on P2's plan; see logs/macos-host.md (UNLISTED, 2026-11-17T21:52:58Z). |
| 28 | `ASSUMED` | A Mac's schedules run at the time they name, with no spread. Every systemd timer but metrics has a `RandomizedDelaySec`, and launchd has no counterpart. `doctor`'s `system.units` and the Platforms page say so, as decision 1 has every weaker guarantee stated. A fleet rarely has many Macs. Refused: a random sleep inside every unattended command. Ruled by the author 2026-11-17 on P2's plan; see logs/macos-host.md (UNLISTED, 2026-11-17T21:52:58Z). |
| 29 | `ASSUMED` | The unattended runs of `backup`, `update`, `fleet publish` and `doctor` write their stamp on every platform once they have reached the engine, and Linux does not read it. A run that cannot reach the engine exits 7 and leaves its stamp, `doctor` and `fleet publish` included, so a job that fires before a login is not reported as a failure. This amends decision 8, whose stamps §5.6 had written on darwin alone. Refused: a flag or an environment variable that only the plists set. Ruled by the author 2026-11-17 on P2's plan; see logs/macos-host.md (decision 8, 2026-11-17T21:52:58Z). *(Amended by decision 31.)* |
| 30 | `ASSUMED` | Each job writes its output to `VarDir/logs/<label>.log`, and `/etc/newsyslog.d/dev.morzer.<p>.conf`, installed and removed with the jobs, caps and rotates those logs, since launchd rotates no `StandardOutPath`. Ruled by the author 2026-11-17 on P2's plan; see logs/macos-host.md (UNLISTED, 2026-11-17T21:52:58Z). |
| 31 | `ASSUMED` | A scheduled run that cannot reach the engine exits 7 and leaves its stamp only where the supervisor catches it up later, which launchd's does. Elsewhere it carries on and reports as it always has, and writes no stamp. On Linux, decision 29 would otherwise stop the backup and update runs journalling and notifying a failure while dockerd is down, stop `doctor --unattended` sending `doctor.finished`, and stop `fleet publish` publishing the row that says the runtime did not answer, which decision 14 refuses. This amends decision 29. Ruled by the author 2026-11-18 on P2's second plan; see logs/macos-host.md (decision 29, 2026-11-18T08:03:55Z). |
| 32 | `ASSUMED` | Every `fleet publish` but a sync's is the fleet job's run: it stamps, and on a Mac it exits 7 while the engine does not answer, an operator's included. `fleet publish` has no `--unattended`, and decision 29 refuses a flag only the plists set. Its stamp is right either way, since the row it publishes is fresh. Refused: `fleet publish --unattended` in both supervisors' jobs, which changes Linux's unit. Ruled by the author 2026-11-18 on P2's second plan; see logs/macos-host.md (decision 29, 2026-11-18T08:03:55Z). |

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

## 13. Measurements

P0 ran on 2026-11-14, on the author's M3 Max MacBook Pro (`Mac15,11`)
rather than the Mac mini named above. The questions need Apple silicon, not a
particular model. The script ran as
[`spikes/macos-host/run.sh`](../spikes/macos-host/run.sh) stood at
`cf98c262`, and
[`spikes/macos-host/report.txt`](../spikes/macos-host/report.txt) is its whole
output from that run. Later commits add checks for a second run, which that
output does not include. Every number below comes from that file. Times are
UTC.

- macOS 26.5.2 (25F84), with FileVault and SIP on.
- Docker Desktop 4.44.1 (201842): engine 28.3.2, Compose 2.39.1-desktop.1.
- Docker Desktop's settings as installed, with "Start Docker Desktop when you
  sign in" on. The settings file holds no key for the socket or for file
  sharing.

### S3: root reaches the engine and does not find Compose

`/var/run/docker.sock` was in place before the spike changed anything. It is a
symlink owned by root, pointing at the user's `~/.docker/run/docker.sock`.
Root's `docker`, started with launchd's `PATH`, `HOME=/var/root` and nothing
else, answers `version` with 28.3.2, and so does the real LaunchDaemon in S4.
§5.3 does not say three things the manager meets there:

- `docker` is not on launchd's `PATH`, `/usr/bin:/bin:/usr/sbin:/sbin`. It is
  `/usr/local/bin/docker`, a symlink into `Docker.app`.
- launchd starts a daemon with no `HOME` at all.
- Root's CLI finds no Compose plugin. `docker compose version --short` fails
  with the CLI's top-level usage error, exit 125. The plugin's only directory
  is the user's `~/.docker/cli-plugins`. Pointing root's CLI at it, for
  example through `cliPluginsExtraDirs` in a config the manager owns, was not
  tried.

Root's Compose failed, so S1, S2 and S6 ran through the user's CLI. Every
refusal below is reported as `Error response from daemon` or by the
container, never by the CLI. Root's CLI was not used for them.

### S1: the paths are shared, except `/opt`, and no container writes to them

Each spelling was bound through Compose into a container running as root. The
container read a file root had written there, then wrote one:

- `/etc/<p>`, `/private/etc/<p>`, `/var/lib/<p>`, `/private/var/lib/<p>`,
  `/var/run/<p>` and `/private/var/run/<p>` mount under either spelling, and
  the read succeeds.
- `/opt/<p>` is refused: "mounts denied: The path /opt/morzer-spike is not
  shared from the host and is not known to Docker". `/opt` is not in Docker
  Desktop's default file sharing.
- **Every write fails** with `touch: /mnt/probe/written: Permission denied`.
  The target in each case is a `0755` directory that root created.

### S2: the fourth outcome

Docker Desktop lets a container read only the files that the Mac user who runs
it can read, whatever uid the container runs as. Each variant below was
mounted as a Compose `secrets:` file into containers running as 65532, 0 and
1000:

| Variant, on the Mac | 65532 | 0 | 1000 |
| --- | --- | --- | --- |
| The layout: `/private/var/run/<p>/secrets` `0700` root, the file `0400` `65532:65532` | not started | not started | not started |
| The same, under `/private/tmp` | not started | not started | not started |
| The directory `0755` | not started | not started | not started |
| The file `0444` | reads, sees `65532:0 444` | reads, sees `65532:0 444` | reads, sees `1000:0 444` |
| The file `0400`, owned by the user, `501:20` | reads, sees `65532:0 400` | reads, sees `65532:0 400` | reads, sees `1000:0 400` |

"Not started" means Compose failed before the container existed:
`invalid mount config for type "bind": stat …: permission denied`.

The deciding identity is uid 501, the user who runs Docker Desktop:

- When that user can read the file, every container uid reads it.
- When that user cannot, no container that mounts it starts.
- A container does not see the file's owner on the Mac: `501:20` showed as
  `65532:0` or `1000:0`. The group showed as `0` in every case, and the mode
  came through unchanged.
- S1's writes fit the same reading. The user cannot write to a `0755`
  directory that root owns, and neither could the container.

None of §5.5's three outcomes is this:

- The host uid does not pass through, so it is not outcome 1.
- Every uid reads the file only when the Mac user can, a condition outcome 2
  does not have.
- Root in the VM is not what refuses, so it is not outcome 3.

The ruling on decision 6 accepted the first two outcomes. It said nothing
about this one.

### S4: launchd behaves as §5.6 assumes

- **With the engine up**, the first attempt converged. It started the overdue
  backup with `launchctl kickstart`, and the backup ran once and exited 0,
  read back from `launchctl print`.
- **With Docker Desktop quit**, an attempt came every 30 seconds, at 19:07:29,
  19:07:59, 19:08:29 and 19:08:59, each exiting 7.
  - The socket's symlink stayed in place while the engine was down, so the
    symlink being there says nothing about the engine, as §5.11 assumed.
- **Once the engine was back**, the attempts stopped. None came in the three
  and a half minutes before the exit-12 step.
  - Two attempts fell between the end of step b's window and the start of step
    c's, so the report does not show them. The second converged. The final
    `runs = 9` from `launchctl print` counts them: 1 + 4 + 2 + 2.
- **With an exit 12**, the attempt recorded it. Thirty seconds later the job
  found the record and exited 0, and it was not started again.
- **Every attempt** saw uid 0, no `HOME`, and launchd's `PATH`.

### S4 across a restart, with FileVault on

The Mac booted at 19:16:13 and sat at the login screen for two minutes. The
author logged in at 19:18 by the clock.

- 19:18:11: the job's first attempt, after the login. None ran before it.
- 19:18:11 and 19:18:42: no socket.
- 19:19:12: the symlink was back, because Docker Desktop recreates it as it
  starts. The attempt converged and started the backup, which ran.

With FileVault on, the data volume stays locked until a login, and
`/Library/LaunchDaemons` is on that volume. That would explain why nothing ran
before the login. A restart with FileVault off was not run, so this explanation
is reasoned, not isolated.

Decision 5 requires FileVault unless the operator sets
`secrets.allow_unencrypted_disk`: without it `init` refuses, and `doctor`
fails either way. So on a Mac that keeps decision 5's default, a boot waits for
a login, whatever the engine. Decision 7's ruling made Colima a P2 candidate
"if S4 shows that Docker Desktop starting at login, not at boot, matters to an
unattended Mac". On such a Mac it does not, because Colima would wait for the
same login. That is a deduction: Colima was not run, and neither was a restart
with FileVault off, which is the one case where Colima's start at boot could
matter.

### S5: `/private/var/run` is cleared at boot

After the restart, both markers were gone, and so was `/var/run/<p>` itself.
The engine's socket was also gone until Docker Desktop started. §5.4's first
branch holds: the render is redone by `apply --startup`.

### S6: the binary runs, and self-update's rename keeps it running

The darwin/arm64 binary was cross-compiled in the pinned `golang` container
with goreleaser's flags and fetched with `curl`. It runs, exit 0:

- It carries `com.apple.provenance` and no `com.apple.quarantine`.
- Its signature is ad hoc and linker-signed.
- `spctl` rejects it, but that does not stop it running.

After a rename over it, the new version runs. Two contrasts:

- **Overwritten in place**, on the same inode, it still ran, exit 0. The
  SIGKILL the script expected did not happen on 26.5.2. `self-update` renames
  rather than overwrites, so nothing depends on this result.
- **With a browser's quarantine attribute**, a copy was killed, exit 137. An
  archive downloaded in a browser does not run until the attribute is
  removed, and the docs need to say so.

This is one macOS version. §5.9 says S6 should cover "the macOS versions
supported", and those still have to be named.

### What goes back to the author

§12 sends this RFC back if S1, S2 or S3 fails. S1 and S2 fail, for one
reason: Docker Desktop's file sharing reaches the Mac's files as the user who
runs it. It does not act as root or as the container's uid. This needs a
ruling:

- **Decision 4.** `/opt` is not shared by default, and no container can write
  to a directory that root owns. The paths in the layout work; root owning
  them does not. S1's directories were `0755`. The manager makes `VarDir` and
  `RunDir` `0750` root (`ManagedDirs`, `paths.go:216` and `:226`), which the
  user who runs Docker Desktop cannot even enter.
- **Decision 6.** The layout's secret cannot be mounted at all. This is the
  fourth outcome above, which the ruling did not cover.
- **§5.3's preflight** binds each directory and lists it. Here a listing
  passes and a write fails, so the check as written passes on this Mac.
- **§5.3 and S3.** Root reaches the engine through the socket. The Compose
  plugin is a gap that P1 has to close before root can run Compose.

These hold as measured:

- Decision 8.
- Decision 12, on one macOS version.
- §5.4's boot branch.

Decision 7's Colima condition is answered no by deduction from decision 5,
not by a measurement (S4 above).

Not measured:

- root's CLI for S1 and S2;
- a Docker Desktop later than 4.44.1;
- the file-sharing implementation by name (the settings name none);
- a bind of a directory that the Docker Desktop user owns, written to by a
  container;
- a restart with FileVault off;
- a release's `tar.zst` archive fetched with `curl` and unpacked by macOS's
  `tar`, which is what `install.sh` does.

### The second run

The second run used the same Mac and the same Docker Desktop, later the same
day. It ran `s3`, `s1`, `s2` and `s6` from `run.sh` at `c14b4c7b`, which has
the checks added after the first run. Its output follows the first run's in
`report.txt`.

- **S3: root's Compose works through a config.** The user's plugin is a
  symlink to `/Applications/Docker.app/Contents/Resources/cli-plugins/docker-compose`.
  Root's CLI was started with launchd's `PATH` and no `HOME`. Given
  `--config` pointing at a `config.json` whose `cliPluginsExtraDirs` names
  that directory, `docker compose version` answers 2.39.1-desktop.1. Without
  the config, it fails as in the first run.
- **The file-sharing layer is `fakeowner`.** Every bind and every secret
  mount reported that filesystem type inside the container.
- **S1: a container writes to a directory the user owns.** The directory was
  `0700`, owned by the user, inside a `0755` directory.
  - The container wrote a file and saw it owned by `0:0`.
  - A `chown 65532:65532` inside the container succeeded, and the container
    then saw `65532`.
  - On the Mac, the file was owned by `501:20` throughout.
  - Every directory root owns refused the write again, and `/opt` was again
    not shared.
- **S2: every uid reads the layout owned by the user, when the user can
  reach it.**
  - Inside `RunDir` at the manager's `0750`, owned by root: no container
    started, as with the layout root owns. The user cannot enter `RunDir`.
  - Inside a `0755` directory under `/private/tmp`: every uid read it. 65532
    and 0 saw `65532:0 400`, and 1000 saw `1000:0 400`.
- **S6: the release archive's path works.** macOS's `tar` wrote a `tar.zst`.
  After the `curl` download, `/usr/bin/tar` unpacked it with exit 0. The
  binary carried `com.apple.provenance` and no quarantine attribute, and it
  ran.

From the first run's "not measured" list, this run measured three things:
the bind of a directory the user owns, the file-sharing layer's name (as the
filesystem type a container sees), and the archive. Docker Desktop was still
4.44.1. S1 and S2 again ran through the user's CLI, because the engine
choice did not yet use root's config.

### The third run

The third run ran `s1` and `s2` from `run.sh` at `5f1633d1`, which drives the
engine with root's CLI and its config. Both phases reported `engine as root`.

- **Root's CLI measures what the user's did.** Every S1 and S2 row matches
  the second run's. File sharing acts as the user who runs Docker Desktop,
  whichever CLI asks for the bind.
- **ACL entries do not let a container reach a secret root owns.** The
  layout stayed root's: `RunDir` at `0750`, the secrets directory at `0700`,
  and the file `0400` and owned by `65532:65532`. ACL entries let the user
  list and search both directories and read the file. For every uid, no
  container started:
  `stat /host_mnt/private/var/run/morzer-spike/secrets-acl/token: permission denied`.
  - Without the entries, the same layout fails on the Mac's path,
    `/private/…`. With them, the error names the VM's path, `/host_mnt/…`.
    So the entries get past the first check, and the read through file
    sharing is still refused.
  - A `0755` directory holding a `0400` file the user cannot read fails the
    same way, on the VM's path.

### The fourth run

The fourth run ran `s1` and `s2` from `run.sh` at `e62681ba`, again with
root's CLI. It measured a directory that root keeps at `0711`: others may
pass through it but not list it.

- **S2: a secret passes through a `0711` directory.** The layout the user
  owns sat inside `RunDir` at `0711`, owned by root. Every uid read it.
  65532 and 0 saw `65532:0 400`, and 1000 saw `1000:0 400`.
- **S1: a directory bind does not pass through one.** Two directories sat
  inside a `0711` directory that root owns: one owned by the user, and one
  of root's at `0755` holding a file to read. Neither mounted. In both cases
  the engine did not see the source and tried to create it:
  `error while creating mount source path '/host_mnt/private/var/lib/morzer-spike/sealed/user': mkdir /host_mnt/private/var/lib/morzer-spike/sealed: permission denied`.
  Inside a `0755` directory, the first and second runs bound the same
  directories, and they worked.

A file bind needs the user who runs Docker Desktop to be able to pass through
each directory above it. A directory bind also needs that user to be able to
list each one.

### The fifth run

The fifth run ran `s3` and `s6` from `run.sh` at `37b879f4`.

- **Root runs Compose from Docker Desktop's own plugin directory.** Given a
  config naming `/Applications/Docker.app/Contents/Resources/cli-plugins`,
  root's CLI answered 2.39.1-desktop.1, and S6 ran through it.
- **What root runs belongs to the user who runs Docker Desktop.**
  - `/usr/local/bin/docker` is a link owned by root, in directories root
    owns.
  - What the link names,
    `/Applications/Docker.app/Contents/Resources/bin/docker`, is owned by
    that user (`misery7100:staff`). So is the Compose plugin, and so is every
    directory from `Docker.app` down.
  - `/Applications` is `root:admin 775`, so any administrator can rename
    `Docker.app` itself.
  - 371 entries in `Docker.app` are not root's, or are writable by others.
  - `ReplaceableByNonRoot` would refuse both binaries.
- **Both binaries are signed by Docker's team.** `codesign` reports
  `TeamIdentifier=9BNSXJN65R` for the CLI and the plugin, and
  `codesign --verify --strict` passes for each.
- **S6 behaved as in the runs through the user's CLI.** The build, the
  download, the archive, the rename, the overwrite in place and the
  quarantined copy all gave the same results.

So the "root's `docker`" of §5.3 is a binary that the user who runs Docker
Desktop, and any administrator, can replace. Root running it from there would
let either of them run code as root. `ReplaceableByNonRoot` exists to refuse
exactly that for the manager's own binary. This is one installation, from
Docker Desktop's disk image. Docker Desktop installed another way may leave
these files owned by root.
