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

```divergence
decision: 16
grade: ASSUMED
kind: resolved
at: 2026-11-15T11:12:27Z
attempt: 1
claim: A group nested in the one that owns RunDir's parent gives its members that group's access, and the check read only direct members and primary groups. It now reads the owning group's `NestedGroups` too and counts each nested group as a writer, by name. On the author's M3 Mac, `ls -ld /private/var/run` printed `drwxrwxr-x root daemon` and `dscl . -read /Groups/daemon GroupMembership` printed `GroupMembership: root`, so a stock machine passes with no direct member but root
evidence: internal/adapters/dirservice/dirservice.go:53-75
action: decided
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-15T11:40:33Z
attempt: 1
claim: The earlier entry left a link swapped in for RunDir's entry to `doctor` alone, and only `doctor` ran `storage.run-directory`, so `apply` and the edit session rendered on a Mac where another account could move RunDir aside between two of its steps, or had made RunDir itself. Every render and every edit session now refuses while an account other than root can write where RunDir's entry sits, or while RunDir is not root's, before it creates anything under RunDir; `doctor` reports the same condition
evidence: internal/lifecycle/ops/ops.go:304-312
action: decided
proposal: UNLISTED — RunDir is prepared only while root alone can write where its entry sits and RunDir is root's, refused otherwise at every render and edit session, not reported by doctor alone
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-15T11:40:33Z
attempt: 1
claim: A refused `init --repair` to another release left the current link and the current-release record naming the release it had staged, whose directory its compensation then removed, so the installation pointed at nothing. Reproduced with an undeclared `--profile` against the 1.3.0 fixture; the platform refusal of a Linux-only profile reaches the same step. The stage step now records the release it replaced and its compensation puts back the link and the record before it removes what it created
evidence: internal/lifecycle/ops/init.go:1007-1021
action: decided
proposal: UNLISTED — a stage step that makes a release current records the one it replaced, and its compensation makes that one current again
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-15T11:40:33Z
attempt: 1
claim: The release diff listed `platforms` only where a release declared them, so a release dropping `[linux, darwin]` read as a declaration removed rather than as a release that runs on Linux alone, and an explicit `[linux]` beside none read as a change. It lists the platforms the release runs on, `linux` where none are declared, as the gates read them
evidence: internal/lifecycle/ops/release_diff.go:166-171
action: decided
proposal: UNLISTED — the release diff compares the platforms each release runs on, not the ones it declares
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-15T12:14:43Z
attempt: 2
claim: The earlier entry made a failed repair put back the release it replaced, and `write-installation` kept the installation it had written for the other release, so a repair that failed at `init-secrets` left settings for one release beside another; reproduced with a domain changed by a repair to the 1.3.0 fixture. The step now records the installation it replaced, and its compensation saves that one back. On Linux, where `/run` is root's alone, RunDir's owner is no longer checked: only the Mac's shared parent makes a RunDir someone else made possible, and the acceptance run's `--root` layout, made as its user and driven as root, was refused by it
evidence: internal/lifecycle/ops/init.go:612-645
action: decided
proposal: UNLISTED — a repair's compensation puts back the installation and the release it replaced together; RunDir's owner is checked where RunDirWriters is, the Mac
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

```divergence
decision: 5
grade: ASSUMED
kind: resolved
at: 2026-11-15T12:49:23Z
attempt: 2
claim: Decision 5's gate ran before the input it guards. `secret set` and `secret edit` checked it before the prompt or the editor and stored after, so an override withdrawn while either was open let a value be stored that no render would write; both check again once the input is in, before they store. `init`'s wizard could write a recovery key before `Init` refused the disk, so the wizard checks the gate before it asks anything. The secret commands take no deployment lock, as before this work, so a setting changed in the instant between the second check and the store is not excluded
evidence: internal/cli/secret_edit.go:86-92
action: decided
```

```divergence
decision: 5
grade: ASSUMED
kind: resolved
at: 2026-11-15T13:36:13Z
attempt: 3
claim: The wizard's gate read the installation the command was wired to, so one that allowed an unencrypted disk let the wizard write a recovery key for another product it was then told to make, which `Init` refused. The gate is asked again right before the key is written, of the installation for the product chosen, which also honours a setting withdrawn while the wizard waited. And `config apply` gated on every secret the document named, so one that withdrew the override and repeated a stored value could not apply; it gates on the secrets whose value changes, the ones it stores
evidence: internal/lifecycle/ops/config_apply.go:205-218
action: decided
```

## The engine

On branch `rfc-0055-engine` from `main` at `faf30f7e`: decisions 18, 21 and 22,
and the author's ruling on registry credentials, asked when this pull
request was planned.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-15T17:46:17Z
attempt: 1
claim: Decision 18 gives the CLI a config of the manager's own, which replaces root's `~/.docker/config.json`, so root's registry credentials would reach neither `docker pull` nor the OCI source, which reads the same file. Docker Desktop's credential store is `docker-credential-desktop`, a binary another user owns. Ruled by the author: the manager's config carries root's `auths`, and a `credsStore` or `credHelpers` entry only when its `docker-credential-<name>` passes `ReplaceableByNonRoot`; one that does not is dropped, and `doctor` names it.
evidence: internal/adapters/source/oci/oci.go:509-515
action: decided
proposal: ASSUMED — on darwin the manager's Docker config carries root's `auths`, and a credential helper only when the helper passes `ReplaceableByNonRoot`; a dropped one is named by `doctor`
```

```divergence
decision: 18
grade: ASSUMED
class: spec-gap
at: 2026-11-15T17:46:17Z
attempt: 1
claim: Decision 18 has preflight make the copies. `init` loads a bundle's images with no preflight, and `doctor` and `status` ask the engine with none, so they would run Docker Desktop's own CLI or nothing. The copies are made at the first `docker` command any operation runs, once its `VarDir/manager` exists, and checked as the decision says; preflight and `doctor` report the CLI in use.
evidence: internal/lifecycle/ops/init.go:452
action: departed
proposal: ASSUMED — the copies are made and checked at the first engine command of any operation, not in preflight alone
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-15T17:46:17Z
attempt: 1
claim: `init --release oci://` downloads the bundle before it knows the product, so before any `VarDir/manager` exists to hold the manager's config. On darwin `DOCKER_CONFIG` names `VarDir/manager/docker` from the start. Until that exists the OCI source finds no config and reaches the registry anonymously, so root's own config, and any helper it names, is never read before it is checked. A private registry is then reached once `init` has created the directory, or through a bundle fetched first.
evidence: internal/cli/root.go:1024-1027
action: decided
proposal: ASSUMED — on darwin `DOCKER_CONFIG` is `VarDir/manager/docker` from the first command; before it exists a download is anonymous
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-15T17:46:17Z
attempt: 1
claim: Decision 22 gives every process the settled CLI's directory first on `PATH`, and says nothing of a hook run when no CLI could be settled, after a refused signature check say. A hook then would find Docker Desktop's own CLI by name. Such a hook is refused, naming why the CLI could not be settled, as every `docker` command is.
evidence: rfcs/0055-macos-as-a-host-for-an-installation.md:702
action: decided
proposal: ASSUMED — on darwin a hook is refused while no CLI can be settled, as an engine command is
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-15T17:46:17Z
attempt: 1
claim: Decision 18 names `Docker.app` and not where it is. Only `/Applications/Docker.app` is read, where the installer puts it; a copy elsewhere is not Docker Desktop to the manager, and decision 21's `PATH` rule applies.
evidence: rfcs/0055-macos-as-a-host-for-an-installation.md:698
action: decided
proposal: ASSUMED — Docker Desktop is `/Applications/Docker.app` alone; elsewhere, decision 21 applies
```

## Review of the engine

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-15T18:24:16Z
attempt: 2
claim: The earlier entry set `DOCKER_CONFIG` at the first command and wrote the config only when the CLI was settled, so on an installation an `update` downloaded its release with the config an earlier run left: stale credentials, and a helper named by a check this process never made, run by name from root's PATH. Every command on an installation now rebuilds the config from root's own before anything reaches a registry, removing the old one first. It links each kept helper into the directory first on PATH, so the name resolves to the file checked.
evidence: internal/adapters/dockerdesktop/cli.go:157-180
action: decided
proposal: ASSUMED — on darwin every command on an installation rebuilds the manager's Docker config before any registry is reached, and runs each kept credential helper through a link first on PATH
```

```divergence
decision: 21
grade: ASSUMED
class: discovery
at: 2026-11-15T18:24:16Z
attempt: 1
claim: `ReplaceableByNonRoot` reads owners and mode bits. On a Mac an ACL can grant a named user write or delete on an entry without setting a mode bit, so a root-owned CLI on PATH, a plugin, a helper, or a directory on the way to one, passes while that user could replace it. The rule is shared with the manager's own binary; reading a Mac's ACLs needs `getattrlist`, which `golang.org/x/sys/unix` does not wrap, and is a change to the rule of its own. Docker Desktop's copies are not affected: they are checked by signature.
evidence: internal/infra/atomicfs/atomicfs.go:505-512
action: departed
proposal: ASSUMED — on darwin `ReplaceableByNonRoot` also refuses an entry that carries an ACL granting anyone but root write, delete or append
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-15T18:46:26Z
attempt: 3
claim: Attempt 2 removed the config before rebuilding it, so a command reading it meanwhile, which takes no deployment lock, reached a private registry without credentials. It also linked the helpers into the copies' `bin`, first on PATH, where an earlier copy of Docker Desktop's CLI stayed ahead of a CLI found on PATH since. The config is now replaced in one rename and removed only when it cannot be rebuilt. The links a new config names exist before it, and stale ones go after it. The helpers have a directory of their own, after the settled CLI's on PATH.
evidence: internal/adapters/dockerdesktop/cli.go:163-178
action: decided
proposal: ASSUMED — on darwin the manager's Docker config is replaced atomically at every command, and removed only when it cannot be rebuilt; checked helpers are linked in a directory of their own, after the settled CLI's on PATH
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-15T19:14:25Z
attempt: 4
claim: A helper is run by name, so a name not linked resolves on root's PATH: one left out, one linked before that a command reading the earlier config still runs, and `osxkeychain`, which the CLI looks for when its config names no store and no credentials. Each now resolves, in the helpers' directory first on PATH, to a stand-in that holds no credentials. The settled CLI's directory comes after it, since it can hold a helper nobody checked. `DOCKER_CONFIG` names nowhere until the config is rebuilt. Nothing is written under a manager directory whose way down someone other than root could change. A CLI found on PATH removes Docker Desktop's earlier copies, whose plugin would load first. `Verified` is empty until the acceptance run records a release.
evidence: internal/adapters/dockerdesktop/cli.go:471-502
action: decided
proposal: ASSUMED — on darwin every credential helper name the CLI may run resolves in the manager's helpers directory, to a checked helper or to a stand-in holding no credentials
```

```divergence
decision: 18
grade: ASSUMED
class: discovery
at: 2026-11-15T19:14:25Z
attempt: 1
claim: Decisions 18 and 21 check the CLI and its Compose plugin. The CLI also searches its system plugin directories, `/usr/local/lib/docker/cli-plugins` among them, and a command that lists plugins, `docker info` say, runs each candidate's metadata command. The manager runs no such command, so only a hook that does would run one there, and one someone else could replace would run as root. Checking every candidate there, or saying in the authoring docs that a hook does not list plugins, as decision 22 does for a CLI named by path, is the author's to choose; which of Docker Desktop's links sit there is to be measured on a Mac.
evidence: internal/adapters/dockerdesktop/cli.go:64-71
action: departed
proposal: OPEN — refuse while a candidate in the CLI's system plugin directories can be replaced by someone other than root, or document that a hook does not list plugins
```

## The file-sharing probe

On branch `rfc-0055-sharing` from `main` at `4e65c170`.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-16T01:35:36Z
attempt: 1
claim: §5.3 has preflight and `doctor` list `EtcDir`, `VarDir`, `OptDir` and `RunDir` from a container, and decision 16 has preflight write each directory a container writes. Neither says which directories those are, where the check runs, or how often. The probe is one container from the volume helper image with no network: the four directories bound read-only and listed, and `DataDir`, the one directory the darwin layout gives the engine's user for containers to write, bound and written. The rendered secrets directory is not probed apart: it may not exist yet, and listing `RunDir` reaches the way to it. It runs only where the engine reads the host as one user, and at every `apply`, a running deployment included, since re-creating a container binds again.
evidence: rfcs/0055-macos-as-a-host-for-an-installation.md:281-285
action: decided
proposal: UNLISTED — on an engine that reads the host as one user, `runtime.file-sharing` lists `EtcDir`, `VarDir`, `OptDir` and `RunDir` and writes `DataDir` from one container at every `apply` and every `doctor`, fatal in both
```

```divergence
decision: 16
grade: ASSUMED
class: discovery
at: 2026-11-16T01:35:36Z
attempt: 1
claim: `DataDir` is given to the engine's user as services start, after preflight. So on the first `apply` a container cannot yet write it, and a probe writing it would refuse every new installation. `apply`'s preflight gives it first, the same idempotent change every start makes. `doctor` changes nothing: it writes `DataDir` only once that user owns it, and lists it otherwise.
evidence: internal/lifecycle/ops/ops.go:417-429
action: departed
proposal: ASSUMED — `apply`'s preflight gives `DataDir` to the engine's user before probing it; `doctor` writes it only once that user owns it
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-16T01:35:36Z
attempt: 1
claim: The volume operations refuse when the helper image is not on the machine rather than pull it during a backup. The probe runs on the first `apply`, before anything would have pulled it, so it lets the engine pull the pinned image. A probe that cannot run, for an image that cannot be pulled, an unpinned override or an engine that does not answer, warns and checks nothing, since `up` then refuses the same bind in the engine's words. Only what a container could not reach fails.
evidence: internal/adapters/runtime/compose/volumes.go:715-745
action: decided
proposal: UNLISTED — the probe pulls the pinned helper image when it is absent, and a probe that cannot run warns rather than refuses
```

## The binary

On branch `rfc-0055-release` from `main` at `5e072bc0`. It lands as two pull
requests: `pidStart`, the darwin archive, `install.sh` and `self-update`
first, then the acceptance run's darwin path and its record. The plan found
four questions the RFC does not settle, and the author ruled them on
2026-11-16 as the plan leaned.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-16T20:01:35Z
attempt: 1
claim: §5.8 has `pidStart` convert `p_starttime` to the unit the record stores, which on Linux is clock ticks since boot. On darwin that conversion needs `kern.boottime`, which the kernel moves when the clock is stepped, so a start converted after a step no longer equals the one recorded before it: a live holder reads as a contradiction and its lock is taken while it runs. Ruled by the author: on darwin the record stores `p_starttime` itself, in microseconds since the epoch, which is fixed for a process's life. A record is read only on the host that wrote it, and the field's contract is the kernel's start time for the PID, not a unit (`internal/ports/state.go:111-122`).
evidence: rfcs/0055-macos-as-a-host-for-an-installation.md:453-454
action: decided
proposal: ASSUMED — on darwin `pidStart` is `p_starttime` in microseconds since the epoch, compared only on the host that recorded it
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-16T20:01:35Z
attempt: 1
claim: Decision 20 leaves the darwin archive out of a release with no acceptance record, so the newest release may have none. `self-update` and `install.sh` take the newest release, and nothing says what they do on a Mac then. Ruled by the author: they refuse, naming the release and saying it publishes no darwin/arm64 archive, and point at `--to` and `--version` for one that does. Refused: taking the newest release that has one, which would make "latest" a different version on each platform.
evidence: internal/adapters/managerfeed/github/github.go:82-95
action: decided
proposal: ASSUMED — when the release `self-update` or `install.sh` would take has no archive for the machine, they refuse, naming the release and pointing at `--to` or `--version`
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-16T20:01:35Z
attempt: 1
claim: Decision 9 has the acceptance run on the author's Mac, and §5.10 gives the script a darwin path for its tmpfs and ownership checks. On Linux the script runs as its user under a `--root` it creates. On darwin two of P1's checks refuse that: `storage.run-directory`, since that user can write where `RunDir` sits (`internal/lifecycle/ops/doctor.go:916-935`), and the engine, which keeps no CLI under a manager directory another user can change. Ruled by the author: on darwin the script requires `sudo` and creates its root under directories only root can change. Its own `docker`, `docker compose` and registry calls run as `$SUDO_USER`, since root has no Compose plugin until the manager copies one (S3).
evidence: internal/adapters/dockerdesktop/cli.go:180-190
action: decided
proposal: ASSUMED — on darwin the acceptance run requires `sudo`, keeps its root under directories only root can change, and makes its own engine calls as `$SUDO_USER`
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-16T20:01:35Z
attempt: 1
claim: Decision 20 names the record, the output of decision 9's run, and not how it is made. Ruled by the author: `just acceptance-record <version>` runs the acceptance and writes its output to `.github/macos-acceptance/v<version>.txt`, headed by `sw_vers`, Docker Desktop's version and the commit it ran at. Only a run that passes writes it, since the file's presence is what builds the archive. The release run checks only that the file exists, as decision 20 says.
evidence: .goreleaser.yaml:21-28
action: decided
proposal: ASSUMED — `just acceptance-record <version>` writes the record from a passing run, headed by the macOS and Docker Desktop versions and the commit; the release run checks only that it exists
```

## The rendered configuration on a Mac

On branch `rfc-0055-config-readable` from `main` at `c8af9465`, found in
review of the acceptance run's darwin path.

```divergence
decision: 16
grade: ASSUMED
class: drift
at: 2026-11-16T21:58:31Z
attempt: 1
claim: Decision 16 says a file a container reads has to be readable by more than root, since no group holds the user who runs Docker Desktop alone, and accepts that other accounts can read it. The darwin layout rendered configuration as Linux does, root's at the release's mode, `0640` in the demo, which that user cannot read, so a bind of it fails as a `0400` file in a `0755` directory did (§13, the third run). Where the engine reads the host as one user, each rendered configuration file now adds read for others to the release's mode, and stays root's. No released manager rendered one on a Mac, so no installation holds a file at the old mode.
evidence: internal/lifecycle/ops/apply.go:691-697
action: departed
proposal: ASSUMED — on darwin rendered configuration is root's, at the release's mode with read for others added, since a container reads it as the user who runs Docker Desktop
```

**Drift count: 1.** The darwin layout built the rendered configuration's mode
otherwise than decision 16 said, above.
