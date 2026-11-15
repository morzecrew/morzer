# macos-host · RFC 0055 P1, an installation on a Mac

On branch `rfc-0055-manifest` from `main` at `e2201861`, executing
[RFC 0055](../rfcs/0055-macos-as-a-host-for-an-installation.md) P1 (§12) with
decisions 16–18 as ruled on 2026-11-14. P1 lands as five pull requests: the
manifest's opt-in and the macOS unit lane, the darwin layout and secrets on a
disk, the engine, the binary and its lanes, and the Platforms page. The plan
found four questions the RFC does not settle, and the author ruled them on
2026-11-15; they are the UNLISTED entries below, and decisions 19–22.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## The plan's questions, ruled by the author

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-15T05:07:39Z
attempt: 1
claim: Decisions 16 and 17 give the rendered secrets and the directories a container writes to the user who runs Docker Desktop, and nothing says how a manager running as root finds that user. Ruled by the author: it is the owner of the socket `/var/run/docker.sock` names. The link is root's, and Docker Desktop recreates it as the engine starts. It is read at every render and every apply, and a link that is absent or names a socket root owns is refused.
evidence: spikes/macos-host/report.txt:16-18
action: decided
proposal: ASSUMED — on darwin the user who runs Docker Desktop is the owner of the socket `/var/run/docker.sock` names, read at every render and apply; an absent link, or one naming a socket root owns, is refused
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-15T05:07:39Z
attempt: 1
claim: Decision 9's ruling leaves the darwin archives out of a release that has no record of the acceptance run on the author's Mac, and nothing says how the release build knows. Ruled by the author: the release PR commits the run's output as `.github/macos-acceptance/v<version>.txt`, and the tag's release run builds darwin/arm64 only when that file is in the tagged tree.
evidence: .goreleaser.yaml:27
action: decided
proposal: ASSUMED — a release builds its darwin archive only when the tagged tree holds `.github/macos-acceptance/v<version>.txt`, committed in the release PR
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-15T05:07:39Z
attempt: 1
claim: Decision 7 lets an engine other than Docker Desktop work if it passes preflight, and decision 18 says only how root runs Docker Desktop's binaries, by copying them out of `Docker.app`. A Mac without `Docker.app` has a CLI another user may own, such as Homebrew's. Ruled by the author: root runs the `docker` CLI and the Compose plugin from `PATH` only if `ReplaceableByNonRoot` passes for both, and refuses otherwise.
evidence: internal/infra/atomicfs/atomicfs.go:451-469
action: decided
proposal: ASSUMED — without `Docker.app`, root runs the CLI and Compose plugin on `PATH` only when `ReplaceableByNonRoot` passes for both, and refuses otherwise
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-15T05:07:39Z
attempt: 1
claim: Decision 18 has the manager run only the checked copies, and hooks run as root with the manager's whole environment, so a hook that runs `docker` by name would find the binary inside `Docker.app` on `PATH`. Ruled by the author: every process the manager starts on darwin, hooks included, gets the copies' directory first on `PATH` and the manager's `DOCKER_CONFIG`.
evidence: internal/infra/exec/exec.go:538-541
action: decided
proposal: ASSUMED — on darwin every process the manager starts, hooks included, gets the checked CLI's directory first on `PATH` and the manager's `DOCKER_CONFIG`; a hook naming `/usr/local/bin/docker` by its path is the vendor's to avoid
```

## The manifest's opt-in

```divergence
decision: 15
grade: ASSUMED
class: discovery
at: 2026-11-15T05:07:39Z
attempt: 1
claim: The floor for `requirements.platforms` and `runtimes.<r>.platforms` was to be 0.16.0, the next minor when the RFC was written, and 0.16.0, 0.17.0 and 0.17.1 shipped first without the keys. The next minor is 0.18.0, and a 0.17 manager decodes the manifest strictly and refuses either key, so 0.18.0 is the floor.
evidence: CHANGELOG.md:10
action: departed
proposal: ASSUMED — the floor for both keys is 0.18.0
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-15T05:07:39Z
attempt: 1
claim: §5.1 refuses a release not made for this platform at `init`, `update` and `rollback` "before anything is staged". An update and a rollback read the bundle from the release store, so the refusal sits where the undeclared-profile refusal already sits, before the operation starts and after the copy, and `init` refuses right after reading its copy, before the release is adopted, as it refuses a missing parameter value. A machine whose state came from a Linux backup never passes through either, so `apply` and `doctor` also run a fatal `system.platform` check beside `system.architecture`.
evidence: internal/lifecycle/ops/update.go:139-147
action: decided
proposal: UNLISTED — the release's platform is refused beside the profile refusal at `update` and `rollback`, after reading the bundle at `init`, and by a fatal `system.platform` preflight check in `apply` and `doctor`
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-15T05:07:39Z
attempt: 1
claim: §5.1 gives the keys' values and that a profile's list sits within the release's, and is silent on a list written empty. `platforms: []` would name a release that runs nowhere, or read as absent and mean `[linux]`; both are surprising, so `release verify` refuses it, as it refuses a profile with no files. A key under `runtimes.<r>.platforms` must name a profile the runtime declares.
evidence: internal/domain/manifest.go:1596-1599
action: decided
proposal: UNLISTED — an empty `platforms` list is refused at either level, and `runtimes.<r>.platforms` names only declared profiles
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-15T05:07:39Z
attempt: 1
claim: §5.1 has `system.os` on darwin say the list names Linux distributions rather than warn that the host is unknown, and does not say whether it still warns. The platform gate has already decided the release runs on this host, so the check passes, and its detail says the list names Linux distributions.
evidence: internal/lifecycle/preflight/preflight.go:204-207
action: decided
proposal: UNLISTED — on darwin `system.os` passes, saying the release's list names Linux distributions
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-15T05:26:16Z
attempt: 1
claim: §5.1 refuses a narrowed profile wherever a selection is checked, and the `init` wizard offers every profile a bundle declares, so on a Mac it would offer one that `init` then refuses. The wizard offers only the profiles that run on this platform.
evidence: internal/cli/init_wizard.go:370
action: decided
proposal: UNLISTED — a question that offers profiles offers only those that run on this platform
```

```divergence
decision: 2
grade: LOCKED
kind: resolved
at: 2026-11-15T05:26:16Z
attempt: 1
claim: touched the governed area; a release runs on darwin only when `requirements.platforms` lists it, absent reads as `[linux]`, and every gate reads that one answer
evidence: internal/domain/manifest.go:868-889
action: decided
```

## Review of the opt-in

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-15T06:02:49Z
attempt: 1
claim: The profile refusal sat in the selection every runtime call makes, so on a Mac whose recorded profile the release narrows to Linux, `stop`, `status`, `doctor` and `installation remove` were refused, and so was the `config set installation.profile` the refusals name as the remedy, since a change reads the old selection to see what runs. Reproduced: on darwin, `config set installation.profile=external-db` over a recorded `embedded` narrowed to Linux failed with `profile "embedded" does not run on darwin`. The runtime's configuration checks no platform now, and deploying a selection is refused where configuration is rendered, and at `update`, `rollback`, `init` and `config set`.
evidence: internal/lifecycle/ops/ops.go:1183-1189
action: decided
proposal: UNLISTED — a profile this platform does not run is refused where a selection is deployed or recorded, never where what runs is read, stopped or removed
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-15T06:02:49Z
attempt: 1
claim: `init` refuses a release not made for this platform at its stage step, after fetching it, and the step's compensation removes the copy, so the operation exits 11, not 9, as it does for a `--set` the release does not declare. Its plan refuses before anything starts, exit 9, for a local bundle. A refusal before the operation would mean fetching the release twice, so this is left as built, and the docs say the refusal comes at `init` rather than before it.
evidence: internal/lifecycle/ops/init.go:938-943
action: decided
proposal: UNLISTED — `init` refuses a release not made for this platform at its stage step, exit 11 with nothing installed; its plan, `update` and `rollback` refuse before they start, exit 9
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-15T06:02:49Z
attempt: 1
claim: `write-installation`'s compensation removed `installation.yaml` and the state whether or not the step created them, so a repair refused in that step, an undeclared profile already or a narrowed one now, deleted the installation it was asked to repair. Reproduced with `init --repair --profile undeclared`: the state file was gone afterwards. The step records whether the installation existed and undoes only what it created.
evidence: internal/lifecycle/ops/init.go:625-635
action: decided
proposal: UNLISTED — init's write-installation step undoes only an installation it created
```

```divergence
decision: 5
grade: ASSUMED
kind: resolved
at: 2026-11-15T06:02:49Z
attempt: 1
claim: §5.4's darwin `FilesystemType` through `statfs(2)` lands with the opt-in rather than with the FileVault gate, because the macOS lane runs the test that needs it; it reads `f_fstypename` for the nearest existing directory
evidence: internal/lifecycle/preflight/mounts_darwin.go:15
action: decided
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-15T06:02:49Z
attempt: 1
claim: Decision 21 as first written checked "the Compose plugin it finds on PATH", and the docker CLI does not find plugins on `PATH`: it searches the directories its config names in `cliPluginsExtraDirs`, then its own and the system's. So the row now checks the plugin that CLI would load and names its directory in the manager's config, which the CLI searches first. Corrected in the row before it merged, as the review of this change pointed out.
evidence: rfcs/0055-macos-as-a-host-for-an-installation.md:701
action: decided
proposal: ASSUMED — decision 21 as corrected
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-15T06:41:56Z
attempt: 1
claim: With the runtime's configuration checking no platform, the render was the only place a deploy met the profile refusal, and a release with no configuration entries skips it, while `restart` and a parameter change re-create services without rendering at all. Reproduced: on darwin, `restart` of a recorded profile narrowed to Linux started it. The paths that start or re-create services now check the selection: every apply, update and rollback at the runtime's validation, `restart` before it starts, and the re-create after a parameter change. Compensation and restore put back what ran and do not ask.
evidence: internal/lifecycle/ops/ops.go:986-992
action: decided
proposal: UNLISTED — starting or re-creating services refuses a selection this platform does not run, rendered or not; putting back what ran does not
```
