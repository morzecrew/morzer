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

## The darwin layout, and who owns what

On branch `rfc-0055-ownership`, stacked on `rfc-0055-manifest`.

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-15T05:38:25Z
attempt: 1
claim: Decision 16 gives the user who runs Docker Desktop the directories a container writes, which is `DataDir`. A vendor's hooks run as root and write into it, a restore's most of all, and a file root owns there is one no container can write on a Mac, since file sharing acts as that user. What a container writes already shows as that user through fakeowner. So before every `Up`, the manager walks `DataDir` and gives that user each entry another user owns.
evidence: internal/lifecycle/ops/backup.go:1182
action: decided
proposal: UNLISTED — on darwin, before the runtime starts services, `DataDir` and every entry in it another user owns are given to the user who runs Docker Desktop
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-15T05:38:25Z
attempt: 1
claim: Decision 16 needs `RunDir` at 0755 so that user can pass through it to the secrets, and a boot clears `/private/var/run` (§13 S5). `Render` then recreates it as the parent of the secrets directory, and `MkdirExact` gives a parent the leaf's mode, 0700, which no container mount passes through. Linux has the same defect at 0700 against its 0750, which `storage.directories` warns about after every reboot. Reproduced: `MkdirExact(<root>/run/demo/secrets, 0700)` leaves `<root>/run/demo` at 0700. Rendering now recreates the managed directories under `RunDir` with their own modes first, on both platforms.
evidence: internal/adapters/secrets/sopsage/sopsage.go:485
action: decided
proposal: UNLISTED — rendering recreates `RunDir` and the managed directories under it with their own modes before writing into them
```

```divergence
decision: 17
grade: ASSUMED
kind: resolved
at: 2026-11-15T05:38:25Z
attempt: 1
claim: Decision 17 replaces `OwnerRefusal` with a `doctor` warning and leaves P1 to settle what `apply`'s host-side check compares. The store is given the user who runs Docker Desktop when it is built and owns every rendered file and the directory by that user at each render, setting the declared owner aside, so `OwnerRefusal` is removed rather than bypassed. `apply` compares each file's owner with that same user, read again, where Linux compares the declaration.
evidence: internal/lifecycle/ops/apply.go:446
action: decided
```

```divergence
decision: 16
grade: ASSUMED
class: discovery
at: 2026-11-15T05:38:25Z
attempt: 1
claim: Decisions 4 and 16 read a declared `render_to` under `/run/` under `/var/run/` on darwin. Nothing renders to `render_to`: it is defaulted from `DefaultPaths` and checked to be absolute, and secrets render to `Paths.SecretsRenderDir()` on every platform. With `DefaultPaths` per platform, the default follows, and a declared value has nothing to translate.
evidence: internal/domain/manifest.go:1368-1369
action: departed
proposal: UNLISTED — `render_to` is not where secrets render, so no translation is built; the darwin layout's `RunDir` is what moves
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-15T05:38:25Z
attempt: 1
claim: Decision 16 says only root can change `RunDir`, and that holds for `RunDir` itself, root's at 0755. Its own entry sits in `/private/var/run`, which is `root:daemon 0775`, so a member of `daemon` could rename it away. On a default macOS that group's only member is root. The darwin `doctor` checks gain one that says so, in the pull request that adds them.
evidence: spikes/macos-host/report.txt:40
action: decided
proposal: UNLISTED — `doctor` on darwin warns when the group that can write `/private/var/run` has a member other than root
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

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-15T07:10:39Z
attempt: 1
claim: `stage-release`'s compensation removed the release directory whatever had made it, and a repair re-stages the installed release into the directory it already has, so any failure after that step, a refused `--profile` among them, deleted the installed release and left the current record pointing at nothing. Reproduced: after a refused `init --repair --profile undeclared`, `releases/1.2.0` was gone. A repair records that the directory existed before it fetched, and the compensation leaves it; a first init still clears what it staged.
evidence: internal/lifecycle/ops/init.go:877-880
action: decided
proposal: UNLISTED — a refused repair leaves the installed release's directory; only a directory init itself created is removed
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-15T07:10:39Z
attempt: 1
claim: The start paths' check read only the selection, so a restart or a parameter change with no profile recorded reached the runtime for a release not made for this platform, which only a state from another host can leave. They check the release first now, as init, update and rollback do. And the re-create a parameter change makes, refused before it touches a service, no longer has its undo re-create the selection it refused: the configuration backups go back, and the services stay as they were.
evidence: internal/lifecycle/ops/config.go:384-387
action: decided
proposal: UNLISTED — starting or re-creating services refuses a release this platform does not run as well as a selection, and a refused re-create's undo restores files without re-creating services
```

## Review of the ownership

```divergence
decision: 16
grade: ASSUMED
class: discovery
at: 2026-11-15T06:55:20Z
attempt: 1
claim: Decision 16 gives the secrets directory to the user who runs Docker Desktop, and `secret edit` made its plaintext session directories inside it and wrote them by path, so that user could swap a session for a link and redirect what root writes or read back. Sessions now live in `RunDir/edit`, root's at 0700 on the same tmpfs, created without following a link.
evidence: internal/domain/paths.go:195-200
action: departed
proposal: ASSUMED — `secret edit` sessions live in a root-only directory under RunDir, beside the secrets directory rather than in it
```

```divergence
decision: 16
grade: ASSUMED
class: discovery
at: 2026-11-15T06:55:20Z
attempt: 1
claim: RunDir's own entry is in `/private/var/run`, which its `daemon` group can write, and `MkdirExact` sets a mode through `chmod(2)`, which follows a link. So a member of that group could put a link where RunDir or the secrets directory belongs and have root change the mode of what it names. Those directories, and the edit sessions', are now created, and their modes set, through a descriptor opened with `O_NOFOLLOW`, which refuses a link in their place. A link swapped in for a directory above them is still followed, so the guarantee rests on that group having no member but root, which the darwin `doctor` checks will report.
evidence: internal/infra/atomicfs/atomicfs.go:302-318
action: departed
proposal: ASSUMED — RunDir and the directories under it are created and given their modes without following a link; a group writing /private/var/run with a member other than root is reported by doctor
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-15T06:55:20Z
attempt: 1
claim: The restart after a secret change, and `config apply`'s, start processes through the runtime's restart rather than `up`, so they skipped the hand-over of `DataDir` every start makes. Both go through `RestartServices`, which hands it over first. The walk also stops when its context does, and fails on an entry it cannot read rather than reporting a half-given tree.
evidence: internal/lifecycle/ops/ops.go:316-324
action: decided
proposal: UNLISTED — every restart through the runtime gives DataDir to the engine's user first, as every start does
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-15T07:49:46Z
attempt: 1
claim: A migration and a task run their service to completion through the runtime's one-shot, which starts that service and what it depends on before services start, so a fresh installation's database would start on a `DataDir` the engine's user cannot write. The one-shot hands `DataDir` over first, as a start and a restart do.
evidence: internal/lifecycle/ops/ops.go:332-341
action: decided
proposal: UNLISTED — every way the runtime starts a container, a one-shot included, gives DataDir to the engine's user first
```

```divergence
decision: 16
grade: ASSUMED
kind: resolved
at: 2026-11-15T07:49:46Z
attempt: 1
claim: The darwin `doctor` check the earlier entry promised lands here, beside the hardening it backs: `storage.run-directory` fails while an account other than root can write the directory RunDir's entry sits in, read from Directory Services. It counts the group's members, and the people's accounts whose primary group it is; macOS's own accounts below uid 500, such as `daemon`, whose primary group `daemon` is, are left out
evidence: internal/lifecycle/ops/doctor.go:897-925
action: decided
```

## The FileVault gate

On branch `rfc-0055-filevault`, stacked on `rfc-0055-ownership`.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-15T06:28:54Z
attempt: 1
claim: Decision 5 names four render paths and the four `secret` commands that store before they render. `config apply` (RFC 0056) is a fifth of each: it stores the values a document gives and then renders. Its render reaches the gate in the store, and like a `secret` change it would have stored first, so it checks the gate before the operation starts when the document gives a secret. The gate is one function, `RefuseUnencryptedDisk`, which the store calls first in `Render` and each of those callers calls before it stores.
evidence: internal/lifecycle/ops/config_apply.go:668
action: decided
proposal: UNLISTED — `config apply` of a document that gives a secret checks the FileVault gate before it starts, as a `secret` change does
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-15T06:28:54Z
attempt: 1
claim: `secret edit` decrypts the values it shows into a file under the secrets directory for the editor, which is plaintext on the same disk before anything is stored. So its check comes before the editor opens, not before the store, and a refused edit writes nothing at all.
evidence: internal/cli/secret_edit.go:198
action: decided
proposal: UNLISTED — `secret edit` checks the FileVault gate before it decrypts anything for the editor
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-15T06:28:54Z
attempt: 1
claim: Decision 5 does not say how the refusal reads. It is a secrets error, exit 6, as a render that cannot write a file is, naming FileVault and both ways out: turning it on, or setting `secrets.allow_unencrypted_disk`. `init --allow-unencrypted-disk` is accepted on Linux and records the setting, which nothing there reads, so one runbook serves both platforms.
evidence: internal/domain/errors.go:274-276
action: decided
proposal: UNLISTED — the FileVault refusal is exit 6 and names both remedies; the flag and the setting are accepted on Linux and change nothing there
```

## Review of the FileVault gate

```divergence
decision: 5
grade: ASSUMED
kind: resolved
at: 2026-11-15T08:06:18Z
attempt: 1
claim: Decision 5's gate reads the policy where it acts. `config apply` reads the policy the change leaves, so one document can allow an unencrypted disk and give a secret, and reads it again under the lock. `init` asks again under its lock, so a `config set` that withdrew the setting while a repair waited is honoured. A repair given `--allow-unencrypted-disk` keeps it over the policy it carries, which had dropped it.
evidence: internal/lifecycle/ops/init.go:629-635
action: decided
```
