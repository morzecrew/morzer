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

## The acceptance run on a Mac

On branch `rfc-0055-acceptance` from `main` at `c8af9465`. The plan found
three questions the RFC does not settle, and the author ruled them on
2026-11-17 as the plan leaned.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-16T21:32:47Z
attempt: 1
claim: The run under `sudo` needs a directory root alone can change, for `storage.run-directory` and the engine, that the user who runs Docker Desktop can list and that Docker Desktop shares. The user's `TMPDIR` under `/var/folders` is that user's, so it is refused. Ruled by the author: on darwin the work directory is `mktemp -d /private/var/morzer-acceptance.XXXXXX`, made 0755 by root. `/private/var` is root's, and Docker Desktop shares `/private` by default (§13 S1).
evidence: internal/lifecycle/ops/doctor.go:916-935
action: decided
proposal: ASSUMED — on darwin the acceptance run works in `/private/var/morzer-acceptance.XXXXXX`, root's and 0755
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-16T21:32:47Z
attempt: 1
claim: §5.10 gives the acceptance script a darwin path and does not say what becomes of its Linux-only steps. Ruled by the author: they are ported, and a step that cannot be is reported as skipped with the reason, never dropped. The restore killed while it writes a volume reads how far the docker CLI has read its stdin from `lsof -o` where Linux reads `/proc/<pid>/fdinfo/0`; if `lsof` gives no offset on the Mac, the step prints SKIPPED. The terminal session runs under BSD `script -q /dev/null`, and the signalled session gets a process group of its own from bash's job control, since macOS has no `setsid`.
evidence: .github/scripts/acceptance.sh:730-745
action: decided
proposal: ASSUMED — on darwin the acceptance script ports its Linux-only steps, and reports one it cannot port as skipped with the reason
```

```divergence
decision: 17
grade: ASSUMED
class: discovery
at: 2026-11-16T21:32:47Z
attempt: 1
claim: The step where a container that is not root reads the secret given to its user runs this machine's binary inside a Linux container and asserts RFC 0032's owners, which Docker Desktop does not carry into a container (§13 S2). Ruled by the author: on darwin it is skipped, and a step of its own checks decision 17 instead: the rendered secrets directory and each file are the engine user's, the owner of the socket `/var/run/docker.sock` names, at 0700 and 0400, and a service reads its secret.
evidence: .github/scripts/acceptance.sh:1269-1365
action: departed
proposal: ASSUMED — on darwin the acceptance run checks decision 17's owner and modes, and that a service reads its secret, in place of RFC 0032's owner step
```

```divergence
decision: 20
grade: ASSUMED
class: discovery
at: 2026-11-16T21:32:47Z
attempt: 1
claim: Ruled by the author with the plan: `just acceptance-record <version>` refuses a tree with anything uncommitted, since the record names the commit it ran at, and a record from a tree that differs from that commit would vouch for code nobody tagged.
evidence: logs/macos-host.md:716
action: departed
proposal: ASSUMED — `just acceptance-record` refuses a tree with uncommitted changes
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

## A Mac's ACLs

On branch `rfc-0055-acl` from `main` at `c8af9465`, building decision 23.
The plan found questions the row does not settle, and the author ruled them on
2026-11-17 as the plan leaned.

```divergence
decision: 23
grade: ASSUMED
class: discovery
at: 2026-11-16T22:06:50Z
attempt: 1
claim: Decision 23 says reading a Mac's ACLs needs a call x/sys does not wrap. `golang.org/x/sys` v0.50.0 has no `Getattrlist`, only `Setattrlist` and the `ATTR_CMN_EXTENDED_SECURITY` constant, and the build has cgo off. Ruled by the author: the ACL is read with `getattrlist(ATTR_CMN_EXTENDED_SECURITY)` through a libSystem trampoline, as x/sys calls `setattrlist`, and the filesec it returns is parsed in Go. The compiler permits that `//go:cgo_import_dynamic` form outside cgo-generated code. Refused: the raw system call, which Apple does not keep stable, and libSystem's `acl_*` calls, which take several trampolines and opaque handles. A reply the parser cannot read is an error, so the entry is not passed.
evidence: internal/infra/atomicfs/atomicfs.go:541-588
action: departed
proposal: ASSUMED — on darwin `ReplaceableByNonRoot` reads each entry's ACL with getattrlist through a libSystem trampoline, and an ACL it cannot read is an error
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-16T22:06:50Z
attempt: 1
claim: An ACL entry names a GUID, not a uid, and decision 23 says only "anyone but root". Ruled by the author: only the GUID of uid 0, `FFFFEEEE-DDDD-CCCC-BBBB-AAAA00000000`, is root, with no lookup of group membership. An entry naming a group counts whatever the group, everyone and wheel included, as a group bit does. An unknown GUID counts as someone other than root, and the refusal prints it.
evidence: internal/infra/atomicfs/atomicfs.go:504-511
action: decided
proposal: ASSUMED — of an ACL's entries only uid 0's GUID is root's; a group's, everyone's or an unknown GUID counts as someone else's
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-16T22:06:50Z
attempt: 1
claim: Decision 23 lists the rights and not which entries carry them. Ruled by the author: every allow entry counts, inherit-only ones and inherited ones included, since root creates files in the directories it checks, the CLI's copies among them, and they would inherit the grant. Deny entries grant nothing and are passed, as are audit and alarm entries. `generic_all` and `generic_write` count, since the kernel expands them to the rights listed. `readsecurity`, `readattr`, `writeattr` and `writeextattr` do not, since the list is closed.
evidence: rfcs/0055-macos-as-a-host-for-an-installation.md:704
action: decided
proposal: ASSUMED — every allow entry counts whatever its inheritance flags, generic_all and generic_write among its rights; deny, audit and alarm entries do not
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-16T22:06:50Z
attempt: 1
claim: A process that is not root may be refused an entry's ACL, which reads as `EACCES`. Ruled by the author: the entry cannot be checked, which is an error as an `lstat` that fails is, so a caller that passes what it cannot check now refuses there.
evidence: internal/infra/atomicfs/atomicfs.go:562-565
action: decided
proposal: ASSUMED — an ACL that cannot be read makes the entry one that cannot be checked
```

## The plugins a hook could list

On branch `rfc-0055-plugins` from `main` at `21ff2c66`, building decision
24. The plan found questions the row does not settle, and the author ruled
them on 2026-11-17 as the plan leaned.

```divergence
decision: 24
grade: ASSUMED
kind: resolved
at: 2026-11-16T22:35:40Z
attempt: 1
claim: Decision 24 has the author rule again if Docker Desktop leaves a link to a binary inside `Docker.app` in the CLI's system plugin directories. Measured on the author's Mac on 2026-11-17: `/usr/local/lib/docker/cli-plugins` and `/usr/local/libexec/docker/cli-plugins` do not exist, nor do `/usr/local/lib` and `/usr/local/libexec`, and `/usr/local` is `root:wheel` at `0755`. Docker Desktop leaves nothing there, so the row stands as written.
evidence: internal/adapters/dockerdesktop/cli.go:64-71
action: decided
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-16T22:35:40Z
attempt: 1
claim: Decision 24 refuses a hook while an entry in a plugin directory, or a directory on the way to one, can be replaced by someone other than root, and does not say what a directory that does not exist is. Someone who can write the nearest directory above it can make it and put a plugin in. Ruled by the author: a directory that does not exist is judged by the nearest one above it that does, by `ReplaceableByNonRoot`. The check runs before every hook, after the CLI settles, as the settle check does, and costs a few `lstat`s. Linux is unchanged: root's CLI there searches the same directories, and decision 24 is darwin's.
evidence: internal/cli/root.go:1019-1024
action: decided
proposal: ASSUMED — a plugin directory that does not exist is judged by its nearest existing ancestor; the check runs before every hook, on darwin only
```

## The docs

On branch `rfc-0055-docs` from `main` at `d27056e6`, writing §7's pages for
P1. The plan found three questions §7 does not settle, and the author ruled
them on 2026-11-17 as the plan leaned. Two more turned up while checking what
the pages would claim.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-17T10:45:03Z
attempt: 1
claim: §7 has the docs say "verified on Apple silicon with Docker Desktop <version>", never "works on macOS" alone, and no acceptance run has been made on a Mac yet: `dockerdesktop.Verified` stays empty until the release pull request that records one fills it. Ruled by the author: the pages say no manager release has passed the acceptance run yet, so none ships a macOS archive, and that `doctor`'s `runtime.engine-version` names the releases a manager passed on. The release pull request that commits `.github/macos-acceptance/v<version>.txt` fills `Verified`, that sentence and the changelog together. Quoting the spike's Docker Desktop is refused: the spike was not the acceptance run.
evidence: internal/adapters/dockerdesktop/cli.go:40-43
action: decided
proposal: ASSUMED — the docs name the Docker Desktop releases `dockerdesktop.Verified` lists, written by the release pull request that commits the acceptance record, and say none has passed until then
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-17T10:45:03Z
attempt: 1
claim: §7 asks for a Platforms page and for authoring guidance, and does not say whether they are one page. Ruled by the author: two, each linking the other. `explanation/platforms.md` says what an installation on a Mac is and is not, and an authoring how-to says how a release is made to run there, since one informs knowing and the other doing.
evidence: rfcs/0055-macos-as-a-host-for-an-installation.md:580-587
action: decided
proposal: ASSUMED — the Platforms page is an explanation, and making a release run on a Mac is a separate authoring how-to
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-17T10:45:03Z
attempt: 1
claim: Decision 12 has `install.sh` accept darwin, and its refusal when neither tar nor a `zstd` binary can decompress the archive names apt, apk and dnf alone, none of which a Mac has. Ruled by the author: the refusal also names `brew install zstd`, and the install page says so for a Mac. The hint shows only when extraction fails, so it holds whether or not a Mac's tar reads zstd.
evidence: install.sh:544-546
action: decided
proposal: ASSUMED — `install.sh`'s missing-zstd refusal names Homebrew beside the Linux package managers
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-17T10:45:03Z
attempt: 1
claim: Decisions 18 and 21 have root run only a docker CLI and Compose plugin only root can replace, and say nothing of the other tools root runs by name. The manager runs the first `sops` on `PATH`, unchecked, on Linux too. There it is the distribution's, and root's. On a Mac it is Homebrew's, under `/opt/homebrew`, which the user who installed Homebrew owns, so that user could replace what root runs at the next `sudo morzer`: the exposure decision 18 closes for the CLI. The same holds for a tool a hook runs by name past the CLI's directory (decision 22), and for the `zstd` that `install.sh` runs when tar cannot read the archive. Decided for this task: the install page has a Mac put `sops` where only root can write, and not take it from Homebrew, and says why. No code changes here; the check needs a row.
evidence: internal/adapters/secrets/sopsage/sopsage.go:138
action: decided
proposal: ASSUMED — on darwin, root runs `sops` only if `ReplaceableByNonRoot` passes for the one `PATH` names, as decision 21 has for the CLI, and `doctor` names it otherwise
```

```divergence
decision: 1
grade: LOCKED
class: drift
at: 2026-11-17T10:45:03Z
attempt: 1
claim: Decision 1 has each weaker guarantee stated in `doctor` and the docs, never left implicit, and §12 has P1, usable without a supervisor, say so. On a Mac `doctor` does not. There is no systemd, so `init` skips its units without a word, as in a container, and `system.units` reports ok, "systemd is not in use on this host". `backup.schedule`, `doctor.schedule`, `update.schedule` and `sync.interval` are accepted and never run, and nothing converges at boot. The pages in this task state it. `doctor` stating it is a change to code P1 already merged and is not built here; halted on it for the author.
evidence: internal/lifecycle/ops/doctor.go:1673-1674
action: halted
proposal: LOCKED (decision 1, unchanged) — on darwin, `system.units` warns that nothing converges at boot and no schedule runs until the launchd adapter (P2), and names the schedules the installation sets
```

**Drift count: 2.** P1 left the missing supervisor unstated in `doctor`,
which decision 1 says it states, above.

## What root runs by name, and the missing supervisor

On branch `rfc-0055-units` from `main` at `624f1603`, with `rfc-0055-tools`
after it, on the two findings under "The docs". The author measured a Mac on
2026-11-17 and ruled on both, and ruled the questions the plan raised as it
leaned.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-17T16:12:14Z
attempt: 1
claim: The finding under "The docs" has root run the first `sops` on `PATH` unchecked. Measured on the author's Mac on 2026-11-17, macOS 27.2.1: `sudo` resets the environment and sets no `secure_path`, so root keeps the user's `PATH`, `~/bin:~/.local/bin:/usr/local/bin:/opt/homebrew/bin:…`, and `/opt/homebrew/bin` is that user's at `0775`. So `sops`, the tools preflight probes, `secret edit`'s editor and what a hook runs by name are found where that user can write, `~/bin` ahead of `/usr/local/bin`, which is `root:wheel` at `0755`. Ruled by the author: root refuses a `sops` someone other than root could replace. Ruled as the plan leaned, for every tool and not `sops` alone: on darwin the manager's `PATH` keeps, after the directories decision 22 puts first, only the directories that pass `ReplaceableByNonRoot` and that nobody but root can add an entry to, from the process's start, before `init` makes the manager directory. A tool found only in a dropped directory is refused, naming it and who could change that directory. Decision 21's own lookup still searches the `PATH` root was given, so a CLI there is refused by name, not missing. A tool named by its path runs what the path names, as decision 22 has for a hook. Refused: checking `sops` alone, which leaves the editor and every hook the user's directories; and refusing to run with such a `PATH` at all, which every Mac with Homebrew has.
evidence: internal/adapters/dockerdesktop/cli.go:329
action: decided
proposal: ASSUMED — on darwin, root's `PATH` keeps only the directories only root can change, after decision 22's; a tool found only in a dropped one is refused by name
```

```divergence
decision: 22
grade: ASSUMED
class: spec-gap
at: 2026-11-17T16:12:14Z
attempt: 1
claim: Decision 22 puts the checked CLI's directory first on the `PATH` of every process the manager starts, and leaves the rest as root was given it. With the entry above, a hook gets the filtered `PATH`, so on a Mac a hook that runs a Homebrew tool by name, `jq` say, no longer finds it. That changes what a hook inherits there; the hooks reference and the authoring how-to say so. Ruled as the plan leaned.
evidence: internal/adapters/dockerdesktop/cli.go:321-337
action: departed
proposal: ASSUMED (decision 22, amended) — on darwin, the rest of a hook's `PATH` is the filtered one decision 25 keeps
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-17T16:12:14Z
attempt: 1
claim: `install.sh`, run as `curl … | sudo sh`, unpacks with the first `zstd` on `PATH` when tar cannot read the archive itself, and a Mac's tar cannot: measured on the author's Mac on 2026-11-17, bsdtar 3.5.3 refuses `--zstd` with no `zstd` on `PATH`. There it is Homebrew's, the user's. Ruled as the plan leaned: unchanged. `sudo` keeps the user's `PATH` for the pipeline too, so `sudo sh` is itself found on it, and the user's `curl` writes what root runs; a replaced `zstd` gives that user nothing the pipeline has not. The install page says the installer trusts the `PATH` it runs with. Refused: shipping the darwin archive as `.tar.gz`, which a Mac's tar reads alone, since it changes the archive's name, `self-update`'s unpacking and the installer for a check the pipeline cannot keep.
evidence: install.sh:538-541
action: decided
proposal: ASSUMED — `install.sh` runs the `zstd` on the `PATH` it was given; the install page says the pipeline trusts that `PATH`
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-17T16:12:14Z
attempt: 1
claim: The registry-credentials ruling under "On branch `rfc-0055-engine`" has the manager's config carry root's `auths`, read from `DOCKER_CONFIG`, then `HOME`, as the docker CLI reads it. Measured on the author's Mac on 2026-11-17: `sudo` keeps `HOME`, so that config is the user's `~/.docker/config.json`. A helper it names is still checked, so root runs nothing unchecked, but `runtime.cli`'s remedy names "root's ~/.docker/config.json", a file that is not the one read. Ruled as the plan leaned: the remedy names the file the manager read.
evidence: internal/lifecycle/ops/doctor.go:963
action: decided
proposal: ASSUMED — `doctor` names the config file the manager read `auths` from, which under `sudo` on a Mac is the user's
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-17T16:12:14Z
attempt: 1
claim: Ruled by the author on the halted entry for decision 1 under "The docs": on darwin, `system.units` warns that nothing converges at boot and no schedule runs, as that entry proposed. The ruling does not say what the warning names. Ruled as the plan leaned: the units this installation would have under systemd, from the parameters `unitParams` gives them, as the operator set them: the start at boot, which runs `apply --startup`, and each schedule set, backups, updates, fleet publishing, sync, `doctor` and metrics. The remedy is `sudo morzer apply` after the Mac starts, once Docker Desktop runs. A warning, since the installation runs and `doctor` exits 0. Linux without systemd, a container say, is unchanged.
evidence: internal/lifecycle/ops/doctor.go:1673-1674
action: decided
proposal: ASSUMED — on darwin, `system.units` warns, naming the start at boot and each schedule the installation sets, with `sudo morzer apply` after a restart as its remedy
```

```divergence
decision: 1
grade: LOCKED
kind: resolved
at: 2026-11-17T16:19:34Z
attempt: 1
claim: Decision 1 has the weaker guarantee stated in `doctor`. On darwin, `system.units` now warns that a Mac has no supervisor yet, naming `apply --startup` at boot and each schedule the installation sets, with `sudo morzer apply` as the remedy, as the author ruled on the halted entry under "The docs". Linux without systemd still reports ok.
evidence: internal/lifecycle/ops/doctor.go:1694-1704
action: decided
```

The rest of this section is on branch `rfc-0055-tools`, cut from
`rfc-0055-units` at `dd213d43`, building decision 25.

```divergence
decision: 25
grade: ASSUMED
kind: resolved
at: 2026-11-17T16:34:37Z
attempt: 1
claim: Built as decision 25 says. `Prepare` leaves off the `PATH` root was given each directory that fails `ReplaceableByNonRoot` or that anyone but root can add to, and each relative or missing one, before it looks for the manager directory. A tool the runner looks up or runs by name is refused when the file found is someone else's to replace, and when it was found only in a directory left off, naming the file and why, wrapped as a missing tool. Decision 21's lookup still searches the `PATH` root was given.
evidence: internal/adapters/dockerdesktop/cli.go:339-368
action: decided
```

```divergence
decision: 25
grade: ASSUMED
class: spec-gap
at: 2026-11-17T16:34:37Z
attempt: 1
claim: Decision 25 has a tool found only in a directory left off refused by name. That reaches what the manager runs through its runner, `sops` and the tools preflight and `doctor` probe. `secret edit` starts the editor with `os/exec` itself, so that a terminal session is not captured or timed out, and a hook's lookups are its shell's. Both find a tool on the kept `PATH` or not at all, and say so in their own words: "cannot run the editor", or the shell's "not found". The editor and hooks pages say why. Naming the refusal for the editor would route it through the runner, which changes its messages on Linux too.
evidence: internal/cli/secret_edit.go:443
action: departed
proposal: ASSUMED (decision 25, amended) — the refusal by name is for the tools the manager runs through its runner; the editor and hooks find a tool on the kept `PATH` or not at all
```

```divergence
decision: 22
grade: ASSUMED
kind: resolved
at: 2026-11-17T16:34:37Z
attempt: 1
claim: As the entry for decision 22 above amends it: every process the manager starts gets the checked helpers' directory and the settled CLI's first, then the directories decision 25 keeps, and none of the rest. With none kept, `PATH` holds those two alone, never an empty entry, which a shell reads as the working directory.
evidence: internal/adapters/dockerdesktop/cli.go:376-392
action: decided
```

```divergence
decision: 22
grade: ASSUMED
class: discovery
at: 2026-11-17T17:32:33Z
attempt: 1
claim: Decision 22 puts the directory of the CLI decisions 18 or 21 settled first on `PATH`. For a CLI on `PATH` that directory may be one decision 25 leaves off: `ReplaceableByNonRoot` passes a root-owned CLI in a sticky directory, which anyone can add a tool to, and putting that directory first gave a hook whatever anyone added. So the CLI on `PATH` is linked as `docker` in the manager's `bin`, where Docker Desktop's copy goes, and that directory comes first on `PATH` for either CLI. A hook running `docker` by name still runs the CLI settled; nothing else from its directory is found.
evidence: internal/adapters/dockerdesktop/cli.go:178
action: departed
proposal: ASSUMED (decision 22, amended) — the directory first on `PATH` is the manager's own `bin`, holding Docker Desktop's checked copy or a link to the CLI on `PATH`
```

```divergence
decision: 25
grade: ASSUMED
kind: resolved
at: 2026-11-17T17:57:06Z
attempt: 1
claim: Measured on the author's Mac on 2026-11-17, at `20aec75`, under `sudo`. Kept: `/usr/local/bin`, `/usr/bin`, `/bin`, `/usr/sbin` and `/sbin`. Dropped, each named with why: `~/.local/bin`, `~/.cargo/bin` and JetBrains' scripts directory, since the home directory is uid 501's; `/opt/homebrew/bin` and `/opt/homebrew/sbin`, since `/opt/homebrew` is; and `/System/Cryptexes/App/usr/bin`, since `/System/Volumes/Preboot/Cryptexes` is writable by every user. `~/bin`, the cryptexd bootstrap directories under `/var/run` and `/pkg/env/global/bin` do not exist there and are left off without a word. `vi`, `sh`, `jq` and `git` are found in `/usr/bin` and `/bin`; `zstd` is refused, naming Homebrew's and its owner; `sops`, `age` and `restic` are not installed for root and are missing. The row stands as written.
evidence: internal/adapters/dockerdesktop/cli.go:351
action: decided
```

## The supervisor, P2

On branch `rfc-0055-launchd` from `main` at `bbc917ed`, executing RFC 0055 P2
(§12): the launchd adapter, boot convergence and schedules (§5.6), with the
reboot run. The plan split P2 into three pull requests and found seven
questions the RFC does not settle. The author ruled all seven on 2026-11-17 as
the plan leaned; they are the first seven entries below.

The split, as built: the adapter with boot convergence, then the stamps and the
catch-up, then the docs, the acceptance run's reboot and port-probe steps, and
the close-out. The plan had boot convergence in the second pull request. It
moved to the first because the adapter installs the boot job, and without the
exit 7 before the lock and the exit-12 record that job would take the lock and
journal a failed `apply` every 30 seconds until a login, and retry an exit 12
every 30 seconds.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-17T21:52:58Z
attempt: 1
claim: launchd starts a job with `PATH=/usr/bin:/bin:/usr/sbin:/sbin` (S4), so a job never finds the `sops` the install page puts in `/usr/local/bin`, and the RFC does not say what a job's `PATH` is. Ruled by the author as the plan leaned: every plist sets `PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin`. Decision 25's filter still applies to it, and each of these directories is root's on the Mac measured.
evidence: spikes/macos-host/report.txt:86
action: decided
proposal: ASSUMED — every LaunchDaemon sets `PATH` to `/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin`, which decision 25 then filters as it filters the `PATH` `sudo` gives
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-17T21:52:58Z
attempt: 1
claim: Every systemd timer but metrics spreads its runs with `RandomizedDelaySec`, and launchd has no counterpart; the RFC does not say whether a Mac spreads them. Ruled by the author as the plan leaned: no spread on a Mac, stated in `doctor`'s `system.units` and on the Platforms page, as decision 1 has every weaker guarantee stated. A fleet rarely has many Macs. Refused: a random sleep inside every unattended command.
evidence: internal/adapters/supervisor/systemd/systemd.go:660
action: decided
proposal: ASSUMED — a Mac's schedules run at the time they name, with no spread; `doctor`'s `system.units` and the Platforms page say so
```

```divergence
decision: 8
grade: ASSUMED
class: spec-gap
at: 2026-11-17T21:52:58Z
attempt: 1
claim: §5.6 has the four persistent jobs write their stamps on darwin, and does not say how a job knows it is one. Ruled by the author as the plan leaned: the unattended runs of `backup`, `update`, `fleet publish` and `doctor` write their stamp on every platform once they have reached the engine, and Linux does not read it. A run that cannot reach the engine exits 7 and leaves its stamp, `doctor` and `fleet publish` included, so a job that fires before a login is not reported as a failure. Refused: a flag or an environment variable that only the plists set. Decision 14 holds Linux unchanged; where this would change what Linux reports, the second pull request logs it against decision 14 before building it.
evidence: rfcs/0055-macos-as-a-host-for-an-installation.md:410-411
action: departed
proposal: ASSUMED (decision 8, amended) — the unattended runs of the four persistent jobs write their stamps on every platform once they reach the engine; one that cannot exits 7 and leaves its stamp
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-17T21:52:58Z
attempt: 1
claim: §5.6 has `apply --startup` start the overdue jobs with `launchctl kickstart`, and the RFC does not say where that lives, since `ops` knows no launchd. Ruled by the author as the plan leaned: a small optional interface beside `ports.Supervisor`, which the launchd adapter implements: list the jobs overdue by their stamps, then start each and wait for it to exit. `apply --startup` calls it when the supervisor has it. systemd needs none, since `Persistent=true` catches up.
evidence: rfcs/0055-macos-as-a-host-for-an-installation.md:413-417
action: decided
proposal: ASSUMED — the catch-up is an optional port the launchd adapter implements and `apply --startup` calls once it has converged; a supervisor without it catches up on its own
```

```divergence
decision: 8
grade: ASSUMED
kind: resolved
at: 2026-11-17T21:52:58Z
attempt: 1
claim: §5.6 leans on a record of an exit 12 without saying where it is kept. Ruled by the author as the plan leaned: a file under `VarDir`. `apply --startup` writes it when it ends in exit 12, a later `--startup` exits 0 at once while it stands, and an operator's `apply` removes it, as S4 measured.
evidence: rfcs/0055-macos-as-a-host-for-an-installation.md:396-400
action: decided
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-17T21:52:58Z
attempt: 1
claim: §5.6 sends each job's output under `VarDir/logs`, and launchd neither rotates nor caps a `StandardOutPath`, so the logs grow until the disk is full; the RFC does not say what bounds them. Ruled by the author as the plan leaned: each job writes `VarDir/logs/<label>.log`, and one `/etc/newsyslog.d/dev.morzer.<p>.conf` caps and rotates them, installed and removed with the jobs.
evidence: rfcs/0055-macos-as-a-host-for-an-installation.md:428-429
action: decided
proposal: ASSUMED — each job logs to `VarDir/logs/<label>.log`, and `/etc/newsyslog.d/dev.morzer.<p>.conf`, installed and removed with the jobs, caps and rotates them
```

```divergence
decision: 9
grade: ASSUMED
kind: resolved
at: 2026-11-17T21:52:58Z
attempt: 1
claim: §6 has the acceptance run survive a reboot, and decision 9's ruling made that run one by hand on the author's Mac, which a script cannot restart and carry on through. Ruled by the author as the plan leaned: two phases. `just acceptance-record <version>` installs and checks, then asks for a restart and a login; `just acceptance-record --after-reboot <version>` then checks that the boot converged and a missed backup was caught up, and only then writes the record.
evidence: justfile:334
action: decided
```

The rest of this section is the first pull request, the adapter and boot
convergence, written before it was built.

```divergence
decision: 8
grade: ASSUMED
class: spec-gap
at: 2026-11-17T21:55:23Z
attempt: 1
claim: Decision 8 has one LaunchDaemon per systemd unit, and systemd's schedules are pairs, a oneshot service and the timer that starts it, while a launchd job carries its own schedule. One plist per unit would give each timer a job with nothing to run. So there is one job per service: `dev.morzer.<p>` for the boot job, and one per pair, `dev.morzer.<p>-backup` and so on, its schedule in its own plist.
evidence: internal/adapters/supervisor/systemd/systemd.go:962-971
action: departed
proposal: ASSUMED (decision 8, amended) — one LaunchDaemon per systemd service: the boot job, and one per service and timer pair, which carries the timer's schedule
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-17T21:55:23Z
attempt: 1
claim: Installing the units must not run them, and launchd runs a `RunAtLoad` job when it is loaded. systemd enables the product's service and starts only the timers, so `init` does not converge an installation before the operator's `apply`. The RFC does not say what installing loads. So installing loads each scheduled job it enables, reloads a loaded one whose plist changed so its new schedule holds at once, and never loads the boot job, which launchd loads at the next boot from `/Library/LaunchDaemons`. A reload stops a run in progress, as `launchctl bootout` does.
evidence: internal/adapters/supervisor/systemd/systemd.go:199-215
action: decided
proposal: ASSUMED — installing the units loads the scheduled jobs it enables and reloads a changed one, and leaves the boot job for launchd to load at the next boot
```

```divergence
decision: 8
grade: ASSUMED
class: spec-gap
at: 2026-11-17T21:55:23Z
attempt: 1
claim: The ruling above keeps the exit-12 record in a file and has an operator's `apply` remove it; it does not say on which platform it is honoured, or whether an `apply` that itself ends in exit 12 removes it. On Linux systemd's `RestartPreventExitStatus=12` already stops the retries, and honouring the record there would skip the convergence at the next boot, which decision 14 refuses. So the record is written and honoured on darwin alone, and removed by an operator's `apply` or `start` that succeeds, so one that ends in exit 12 again leaves the boot job held.
evidence: internal/adapters/supervisor/systemd/systemd.go:622-626
action: departed
proposal: ASSUMED (decision 8, amended) — the exit-12 record is a file under `VarDir`, written and honoured on darwin, and removed by an operator's `apply` or `start` that succeeds
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-17T21:55:23Z
attempt: 1
claim: §5.6 translates the calendar timers, and the sync timer is not one: it runs two minutes after boot and then every `sync.interval` after the last run. launchd's `StartInterval` runs a job every interval from when it was loaded, and a `RunAtLoad` sync would run before the engine answers. So the sync job runs every `sync.interval` from when it is loaded, the first an interval after boot, with no spread.
evidence: internal/adapters/supervisor/systemd/systemd.go:795-805
action: decided
proposal: ASSUMED — the sync job runs every `sync.interval` through `StartInterval`, the first an interval after it is loaded
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-17T21:55:23Z
attempt: 1
claim: Every systemd unit has a `TimeoutStartSec`, and every scheduled one is ordered after the product's service; launchd has neither a limit on how long a job runs nor an order between jobs, and the RFC does not say what replaces them. Nothing does: a job runs until the manager's own timeouts end it, and a schedule that fires while the boot job converges is refused the lock, exit 4, as an operator's command would be. Decision 1 has that stated, so the Platforms page says it.
evidence: internal/adapters/supervisor/systemd/systemd.go:636-647
action: decided
proposal: ASSUMED — a job on a Mac has no time limit and no order after the boot job; the Platforms page says so
```

```divergence
decision: 8
grade: ASSUMED
kind: resolved
at: 2026-11-17T22:15:08Z
attempt: 1
claim: Built as decision 8 and the entries above have it: a LaunchDaemon per systemd service, the boot job `RunAtLoad` with `KeepAlive` on a failed exit and a `ThrottleInterval` of 30, and each schedule a `StartCalendarInterval` translated exactly or refused by `ValidateSchedule`, naming the forms it takes. On darwin, `apply --startup` asks the engine for the project's containers before the lock and exits 7 when it does not answer; one that ends in exit 12 is recorded, and the next exits 0 without converging until an operator's `apply` or `start` succeeds. The catch-up and the stamps are the next pull request's.
evidence: internal/adapters/supervisor/launchd/launchd.go:261-274
action: decided
```

```divergence
decision: 1
grade: LOCKED
kind: resolved
at: 2026-11-17T22:15:08Z
attempt: 1
claim: Decision 1 has each weaker guarantee stated. The Platforms page says a Mac starts no job until a login with FileVault on, that its schedules run with no spread, have no time limit or order, and that a run missed while the Mac was off is not run, which the next pull request changes. `doctor`'s `system.units` says the spread a Mac does without in its ok, and warns while the start at boot is held.
evidence: pages/docs/explanation/platforms.md:132-161
action: decided
```

```divergence
decision: 14
grade: ASSUMED
kind: resolved
at: 2026-11-17T22:15:08Z
attempt: 1
claim: Linux is unchanged: the adapter is wired in `internal/cli/supervisor_darwin.go` alone, and the engine check before the lock, the exit-12 record and `system.units`' new wording each run only where the platform is darwin. The suite runs each on Linux too: a start at boot there journals its run without asking the engine first, and an exit 12 holds nothing.
evidence: test/suite/boot_test.go:45-83
action: decided
```

```divergence
decision: 26
grade: ASSUMED
kind: resolved
at: 2026-11-17T22:15:08Z
attempt: 1
claim: Decision 26's warning was for until the launchd adapter, and it ends with it: on darwin `system.units` now reads the jobs. What it named, the start at boot and each schedule set, is what the adapter installs, and a Mac where launchd cannot be asked, without root, warns that only root can read the jobs.
evidence: internal/lifecycle/ops/doctor.go:1676-1692
action: decided
```

**Not measured:** `Status` reads `launchctl print`'s own properties, the lines
one tab in. The fixture in `launchd_test.go` keeps the three lines S4 captured,
`state`, `runs` and `last exit code` (`spikes/macos-host/report.txt:89`), in
launchctl's layout around them, which S4 did not capture whole. Neither did it
capture `print-disabled`, whose two spellings the parser takes, nor
`program =`, which `UnitsManagerPaths` reads beside the plist and which adds
nothing when absent. The acceptance run on the author's Mac is where all three
are seen.

**Drift count: 2.** Nothing in P2 so far is drift.

## The stamps and the catch-up, P2

On branch `rfc-0055-catchup` from `main` at `e3d937db`, the second of P2's
three pull requests: the stamps of the four persistent schedules, and the
catch-up of a run missed while the Mac was off (§5.6, decisions 8 and 29). The
plan raised two questions, which the author ruled on 2026-11-18 as it leaned;
they are the first two entries below. The rest are gaps decided before code.

```divergence
decision: 29
grade: ASSUMED
class: spec-gap
at: 2026-11-18T08:03:55Z
attempt: 1
claim: Decision 29 has a scheduled run that cannot reach the engine exit 7 and leave its stamp, and does not say on which platform, while decision 14 holds Linux unchanged. On Linux that exit would change what is reported while dockerd is down: the backup and update runs would no longer journal or notify their failure, `doctor --unattended` would send no `doctor.finished`, and `fleet publish` would no longer publish the row saying the runtime did not answer. Logged against decision 14, as P2's first ruling asked. Ruled by the author as the plan leaned: a run defers with exit 7 only where the supervisor catches it up later, which launchd's does; elsewhere it carries on and reports as it does today, and writes no stamp. Linux keeps its reports, and gains one question to the engine per scheduled run, of at most 30 seconds, and a stamp it does not read. The rule is keyed on the supervisor, not the platform.
evidence: internal/lifecycle/ops/doctor.go:100-125
action: departed
proposal: ASSUMED (decision 29, amended) — a scheduled run that cannot reach the engine exits 7 and leaves its stamp only where the supervisor catches it up; elsewhere it runs and reports as before, with no stamp
```

```divergence
decision: 29
grade: ASSUMED
class: spec-gap
at: 2026-11-18T08:03:55Z
attempt: 1
claim: Decision 29 stamps the unattended runs of `backup`, `update`, `fleet publish` and `doctor`, and refuses a flag that only the plists set. Three of them have `--unattended`, which systemd's units pass too. `fleet publish` has no such flag, so nothing tells the fleet job's run from an operator's. Ruled by the author as the plan leaned: every `fleet publish` but a sync's is taken as the fleet job's. It stamps, and on a Mac it defers with exit 7 while the engine does not answer, an operator's run included. The stamp is right either way, since the row it publishes is fresh. Refused: `fleet publish --unattended` in both supervisors' jobs, which changes Linux's unit.
evidence: internal/adapters/supervisor/systemd/systemd.go:742
action: departed
proposal: ASSUMED — every `fleet publish` but a sync's is the fleet job's run, for its stamp and for decision 29's exit 7
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-18T08:03:55Z
attempt: 1
claim: §5.6 has a run write its stamp once it has reached the engine, and does not say how a run knows it has. A run that found out halfway through would already have taken the lock and journalled, which is the failure report decision 29 avoids. So a scheduled run asks the engine for the project's containers before it does anything else, as the start at boot does, bounded at 30 seconds, and stamps when the engine answers. A run that cannot read what that question needs, the installation or its release, stamps and carries on, as §5.6 has any failure but the engine's stamp. A dry run neither asks nor stamps.
evidence: internal/lifecycle/ops/startup.go:62-78
action: decided
proposal: ASSUMED — a scheduled run asks the engine first, as the start at boot does, and stamps when it answers
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-18T08:03:55Z
attempt: 1
claim: §5.6 catches a job up when its latest scheduled time is later than its stamp, and does not say what a job with no stamp is. That is a job installed and never run, or one installed before this release. systemd starts a persistent timer's stamp when the timer first starts, so the first run missed after installing is caught up. So a job without a stamp is taken as stamped when its plist was last written.
evidence: rfcs/0055-macos-as-a-host-for-an-installation.md:406-414
action: decided
proposal: ASSUMED — a job with no stamp is caught up from when its plist was last written
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-18T08:03:55Z
attempt: 1
claim: §5.6 has the boot job start the overdue jobs after it converges, and does not say what its exit is when a job cannot be started or fails. launchd starts the boot job again after any failed exit, so a non-zero exit would converge the installation again every 30 seconds and catch the same jobs up each time. So the boot job's exit is its convergence's. A job that cannot be started, or cannot be waited for, is a warning in the boot job's log, and a caught-up job's own failure is in its own log and notification.
evidence: internal/adapters/supervisor/launchd/launchd.go:262-273
action: decided
proposal: ASSUMED — the catch-up never fails the start at boot; what it cannot do is a warning in the boot job's log
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-18T08:03:55Z
attempt: 1
claim: §5.6 catches up each overdue job, and does not say what happens to one an operator disabled or launchd has not loaded. systemd starts no disabled timer, and kickstarting a job launchd does not have fails. So the catch-up skips a job an operator disabled, and one launchd has not loaded, which `doctor` already reports as not loaded.
evidence: internal/adapters/supervisor/launchd/launchd.go:663-682
action: decided
proposal: ASSUMED — the catch-up skips a job an operator disabled or launchd has not loaded
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-18T08:03:55Z
attempt: 1
claim: Decision 29 has `doctor --unattended` exit 7 when the engine does not answer, and `doctor --unattended` sends the notification outbox first, which needs no engine. Asking the engine first would keep a notification queued at boot, the boot job's own exit 12 say, until a login. So it sends the outbox, then asks the engine.
evidence: internal/lifecycle/ops/doctor.go:101-107
action: decided
proposal: ASSUMED — `doctor --unattended` sends the outbox before it asks the engine
```

```divergence
decision: 8
grade: ASSUMED
kind: resolved
at: 2026-11-18T08:30:09Z
attempt: 1
claim: Built as decision 8 and §5.6 have the catch-up: once `apply --startup` has converged and released the lock, the launchd adapter starts each of the backup, update, fleet and doctor jobs whose latest scheduled time is later than its stamp, one at a time in that order, and waits for each to exit, read from `launchctl print`. Metrics and sync are not caught up, as on Linux.
evidence: internal/adapters/supervisor/launchd/catchup.go:37-89
action: decided
```

```divergence
decision: 31
grade: ASSUMED
kind: resolved
at: 2026-11-18T08:30:09Z
attempt: 1
claim: Built as decision 31 has it: each scheduled run asks the engine first and stamps when it answers, and one that cannot reach it exits 7 only when the supervisor implements the catch-up. The rule reads the supervisor, not the platform.
evidence: internal/lifecycle/ops/stamp.go:26-39
action: decided
```

```divergence
decision: 32
grade: ASSUMED
kind: resolved
at: 2026-11-18T08:30:09Z
attempt: 1
claim: Built as decision 32 has it: every `fleet publish` but a sync's goes through the scheduled run's question and stamp.
evidence: internal/lifecycle/ops/fleet_publish.go:110-117
action: decided
```

```divergence
decision: 14
grade: ASSUMED
kind: resolved
at: 2026-11-18T08:30:09Z
attempt: 1
claim: Linux reports what it reported: with dockerd down and a supervisor that does not catch up, the backup journals its run, `doctor --unattended` notifies `doctor.finished`, and the fleet row carries the runtime's problem. What Linux gains is one question to the engine per scheduled run, at most 30 seconds, and a stamp nothing reads.
evidence: test/suite/catchup_test.go:142-168
action: decided
```

```divergence
decision: 1
grade: LOCKED
kind: resolved
at: 2026-11-18T08:30:09Z
attempt: 1
claim: Decision 1 has each weaker guarantee stated. The Platforms page says a Mac now catches up a missed run, that a scheduled run, or a `fleet publish` by hand, that cannot reach the engine exits 7 and waits for the start at boot, and that a Mac whose Docker Desktop never starts therefore sends no `doctor.finished` and lets its fleet row go stale. Commands and Fleet say the exit 7 beside each run.
evidence: pages/docs/explanation/platforms.md:145-164
action: decided
```

**Not measured:** the catch-up waits on `runs` and `state` from `launchctl
print`. S4 saw `state = not running` and `runs = 1` three seconds after a
kickstart (`spikes/macos-host/report.txt:89`). It did not capture `state =
running`, a job's `runs` before its first run, or what launchd does with a
calendar time a clock change skips. The acceptance run on the author's Mac is
where those are seen.

**Drift count: 2.** Nothing in this pull request is drift.

**Measured, 2026-11-18:** the author ran `spikes/macos-host/launchd-probe.sh`
on macOS 27.2.1, and its output is `spikes/macos-host/launchd-probe.txt`. It
settles what the two "Not measured" notes above left open, and agrees with each
assumption: a job launchd does not have exits 113; a job's own properties are
one tab in, and the coalitions below them carry a `state = active` of their own;
before a first run it prints `runs = 0` and `last exit code = (never exited)`;
`kickstart` returns with the job already `running` and `runs = 1`; a kickstart
of a running job exits 0 and starts nothing; a run that exits 3 prints `last
exit code = 3`; `print-disabled` prints `"<label>" => disabled` or `=> enabled`;
and `program =` is the first of the arguments. The tests now read those
captures (`internal/adapters/supervisor/launchd/testdata/print-*.txt`). What a
calendar time a clock change skips does is still not measured.

```divergence
decision: 8
grade: ASSUMED
kind: resolved
at: 2026-11-18T08:45:23Z
attempt: 1
claim: The wait the catch-up does is the one a Mac measured: a kickstarted job reads `state = running` with `runs` one more at once, a second kickstart adds no run, and it reads `state = not running` once it has exited.
evidence: spikes/macos-host/launchd-probe.txt:96-300
action: decided
```

## Review of the catch-up

The review of the pull request found four races in the catch-up and two runs
that asked the engine before an answer that needs none. Each was reproduced
failing before it was fixed. The entries below are the decisions those fixes
made that the RFC does not.

```divergence
decision: 8
grade: ASSUMED
kind: resolved
at: 2026-11-18T08:58:32Z
attempt: 2
claim: The catch-up raced the jobs it runs. Which jobs were due was read job by job, so a caught-up update, which installs the units again and rewrites every plist, hid the jobs after it that had no stamp. A job its own schedule started during the start at boot was passed over as not due while it still ran, so the next job could start beside it and be refused the lock. And a job whose own run finished between the read and the start ran twice. Now which jobs are due is read before any starts, and before each start the catch-up waits until none of the four jobs runs, then reads the stamp and the disabled jobs again. A job whose exit cannot be read starts nothing after it, and a run that left its stamp is reported as still due.
evidence: internal/adapters/supervisor/launchd/catchup.go:103-130
action: decided
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-18T08:58:32Z
attempt: 1
claim: The question a scheduled run asks reads the project before it reaches the engine, so a project that will not load, a missing `env_file` say, failed it with the engine up and was taken for the engine down: no stamp, and on a Mac an exit 7 naming a login. Nothing in the runtime's port asks the engine alone. So when the question fails, the run reads the project with `Runtime.Validate`, which needs no engine; a project that will not load is the run's own failure, and it stamps and fails as it would have. The start at boot keeps its question as P1 built it.
evidence: internal/lifecycle/ops/stamp.go:63-75
action: decided
proposal: ASSUMED — a scheduled run whose question fails while the project will not load is not the engine down; it stamps and fails on its own
```

```divergence
decision: 29
grade: ASSUMED
class: discovery
at: 2026-11-18T08:58:32Z
attempt: 1
claim: Asking the engine first put it ahead of two answers that need none: a stopped product's scheduled backup, skipped with exit 0 under RFC 0040, and a tick refused while an operation waits for a person. On a Mac with Docker Desktop quit, both became an exit 7 naming a login. Both now answer first, and stamp, as the timer's run that §5.6 has every failure but the engine's stamp.
evidence: internal/lifecycle/ops/channel.go:496-507
action: departed
proposal: ASSUMED (decision 29, amended) — a run whose answer needs no engine gives it before the engine is asked, and stamps
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-18T08:58:32Z
attempt: 1
claim: A calendar time a clock change repeats happens twice, and what launchd does with it is not measured. The latest time a schedule passed takes whichever of the two instants last passed, so a run missed at the first is still caught up, at the cost of at most one more run at the boot after the second. A leap day's schedule is looked for eight years back, since 2100 has none.
evidence: internal/adapters/supervisor/launchd/schedule.go:248-262
action: decided
proposal: ASSUMED — a repeated calendar time counts at either instant
```

**Drift count: 2.** Nothing the review found is drift.

```divergence
decision: 8
grade: ASSUMED
kind: resolved
at: 2026-11-18T10:06:10Z
attempt: 3
claim: A third look at the catch-up's races. A job seen idle could start while a later one was waited for, and the start that followed then ran beside it; the look now starts again after any wait. The stamp is read after the last look at a job, so a run its schedule finished up to that look is not started again, and a job launchd started itself is waited for and not reported as one the catch-up ran. A job whose plist is gone is not installed, whatever its stamp says. A short window remains between the last read of the stamp and `kickstart`, which only a lock every job takes could close; the jobs share none, since `fleet publish` and `doctor` take no lock.
evidence: internal/adapters/supervisor/launchd/catchup.go:175-197
action: decided
```

**Known limit:** a job with no stamp counts from when its plist was last
written, and installing the units writes every plist. So a job installed
before this release, missed while the Mac was off, caught up by a run that
exits before it reaches the engine, and then reinstalled before its next tick,
counts from the reinstall at the next boot and is not caught up. Keeping the
time a job was first installed apart from its plist would close it, and is a
design change for a later pull request; until the job's first run stamps, the
next tick is the fallback.

**Drift count: 2.** Nothing the third look found is drift.

## The acceptance run's restart, and the close-out, P2

On branch `rfc-0055-reboot` from `main` at `0acba199`, the last of P2's three
pull requests: the acceptance run's restart and its port-probe step, the
leftovers P2 named (shell completion and the `--install-units` help on a Mac),
and RFC 0055's close-out. The plan raised five questions, which the author
ruled on 2026-11-18 as it leaned; they are the entries below.

```divergence
decision: 9
grade: ASSUMED
kind: resolved
at: 2026-11-18T11:08:30Z
attempt: 1
claim: Decision 9's two-phase run needs an installation whose jobs launchd runs across the restart, and the acceptance run works under `--root`, where nothing does. Ruled by the author as the plan leaned: after the run's own scenario passes, phase 1 installs a second installation, persistent, under `/private/var/morzer-acceptance-reboot`, which is root's, from a copy of the manager binary inside that directory that only root can replace, with real jobs, `dev.morzer.demo` and the rest. It refuses to start while any `dev.morzer.demo*` plist is already in `/Library/LaunchDaemons`. A job runs it through `--config`, from which the manager derives the root (`internal/cli/root.go:1403`). The plan kept the throwaway registry running so nothing is pulled after the restart; the run's last stage already packs a bundle that carries its images and needs no registry, so the restart's installation installs that one, and the registry is not kept.
evidence: .github/scripts/acceptance.sh:1559-1568
action: decided
```

```divergence
decision: 9
grade: ASSUMED
kind: resolved
at: 2026-11-18T11:08:30Z
attempt: 1
claim: The second phase checks that a missed backup was caught up, and a run by hand cannot count on the Mac being off over a scheduled time. Ruled by the author as the plan leaned: phase 1 sets a daily backup time that has already passed today and dates the backup job's plist to yesterday. A job installed fresh has no stamp, so the first boot finds the backup overdue and the catch-up runs it through its own path. The record says the missed backup was made so rather than waited for.
evidence: internal/adapters/supervisor/launchd/catchup.go:143-155
action: decided
```

```divergence
decision: 9
grade: ASSUMED
kind: resolved
at: 2026-11-18T11:08:30Z
attempt: 1
claim: Decision 9's ruling has the second phase check that the boot converged and a missed backup was caught up, and does not say how. Ruled by the author as the plan leaned: no record is written unless the boot job ran during this boot and last exited 0, the journal holds an `apply` begun after the boot that succeeded, the backup's stamp is newer than the boot and a backup newer than the boot is listed, and the boot job's log names the job it caught up. Either way, the phase then removes the restart's installation and its jobs.
evidence: justfile:334
action: decided
```

```divergence
decision: 9
grade: ASSUMED
kind: resolved
at: 2026-11-18T11:08:30Z
attempt: 1
claim: RFC 0059 §8 leaves to this RFC's acceptance run whether the port probe agrees with Docker Desktop, which binds a published port in its own process rather than in the VM. Ruled by the author as the plan leaned: the run fails when they disagree. While a container publishes a TCP port and a UDP port, `network.ports` must refuse a release that needs them, as Docker refuses a second container publishing them; once both are free, the probe must pass them, as Docker then publishes them, since RFC 0059 decision 10 has the probe agree with Docker. The step runs on every platform, so Linux's lane runs it too, where it repeats what RFC 0059 §13 measured.
evidence: internal/lifecycle/preflight/preflight.go:383-399
action: decided
```

The author also ruled the close-out as the plan leaned: RFC 0055 is complete
when this pull request merges, since P0 to P2 are built and P3, the client
bundle, is morzecrew/dotdash's. The fourteen rows the execution proposed and
the RFC does not yet hold are appended, each citing its entry, as decisions
33 to 46.

```divergence
decision: 29
grade: ASSUMED
kind: resolved
at: 2026-11-18T11:21:17Z
attempt: 2
claim: Written after the fact, at the close-out: the third look at #390 made three decisions about what moves a stamp that the entries above did not record. A run cancelled on any path of the engine's question exits 130 and leaves its stamp, whatever the question answered. A read of the project that timed out settles nothing, so the engine's answer stands and the run defers. And a scheduled backup or tick that cannot read the installation stamps, unless it was interrupted or is a dry run, as §5.6 has every failure but the engine's stamp.
evidence: internal/lifecycle/ops/stamp.go:27-46
action: decided
```

The close-out of P2, after building this pull request:

```divergence
decision: 9
grade: ASSUMED
kind: resolved
at: 2026-11-18T11:23:29Z
attempt: 1
claim: Built as the rulings above have it. `just acceptance-record <version>` runs the scenario and, on a Mac, leaves the restart's installation under `/private/var/morzer-acceptance-reboot` with real jobs and a backup made due; `just acceptance-record --after-reboot <version>`, from the same commit and for the same version, checks the boot job, the journal, the backup's stamp, the backups and the boot job's log, removes the installation either way, and writes the record only when every check passed. The port step runs before the scenario's install, and passed on Linux against Docker 29.7.2, the probe and the engine agreeing on 18094/tcp and 18093/udp busy and free; a probe that passed every UDP port turned it red.
evidence: .github/scripts/acceptance.sh:1735-1774
action: decided
```

```divergence
decision: 1
grade: LOCKED
kind: resolved
at: 2026-11-18T11:23:29Z
attempt: 1
claim: Decision 1 has each weaker guarantee stated, and the Platforms page says what a Mac's release record now proves: the run across a restart, the start at boot's convergence after a login and its catch-up of a missed backup, and the port probe agreeing with Docker Desktop. The page's warning that no release has passed the run on a Mac stays until the release pull request that commits the first record.
evidence: pages/docs/explanation/platforms.md:173-180
action: decided
```

```divergence
decision: 14
grade: ASSUMED
kind: resolved
at: 2026-11-18T11:23:29Z
attempt: 1
claim: Linux is unchanged by this pull request's product code: completion's fallback on Linux still reads systemd's units, through a per-OS function, and the `--install-units` help names launchd only beside systemd. Linux's acceptance lane gains the port step, which checks the probe Linux already runs.
evidence: internal/cli/supervisor_linux.go:13-15
action: decided
```

**Not measured:** the restart itself. Both phases run only on a Mac, and the
second only after a restart and a login, so neither has run yet: the first run
of `just acceptance-record` on the author's Mac is the one that sees them, and
the record it writes is the release's. What launchd does with a calendar time a
clock change skips or repeats is not measured either.

**Drift count: 2.** Nothing in P2 is drift. RFC 0055 closes with the two from P1.

## Review of the restart's run

```divergence
decision: 9
grade: ASSUMED
kind: resolved
at: 2026-11-18T11:57:48Z
attempt: 2
claim: The review of this pull request tightened the restart's run. A restart clears `/var/run`, but not the restart installation's copy of it under a directory that outlives the restart, so the first phase takes its containers down and removes it, and the second fails unless the start at boot rendered the secrets again. A first phase that fails after it installed removes what it installed, so no job is left for the next boot. The removal names the installation's seven jobs, not every job under its prefix. And the second phase requires exactly one backup, taken after the boot: the first phase takes none, and the job logs carry no time to tell one boot from another, so a second backup says the Mac restarted more than once or its schedule came round as well.
evidence: .github/scripts/acceptance-reboot.sh:110-120
action: decided
```

**Found, not fixed:** a job's log on a Mac carries no time. The manager's JSON
log leaves the time out unless asked for it (`internal/infra/logging/logging.go:50`),
since journald stamps every line, and nothing asks for it; launchd writes a
job's output to its file as it comes. So `VarDir/logs/<label>.log` cannot say
when a line was written. It needs a decision, whether every log off journald
carries its time or only a Mac's jobs, and is for a later pull request.

## The directories above the layout, after the close-out

```divergence
decision: 16
grade: ASSUMED
class: drift
at: 2026-11-18T16:38:58Z
attempt: 1
claim: Decision 16 keeps every directory on the way to a bind root's at `0755` on darwin. The first lock on a machine, and the first journal record, created each missing directory above their own at `0750`, before `init` builds the layout, and `init` sets the managed directories' modes and leaves those parents as they are. So under `--root` the root and its `var` and `var/lib` were `0750`, which the user who runs Docker Desktop cannot list, and the first `apply` of the acceptance run on the author's Mac refused at the file-sharing check, naming `probe-root/etc/demo`. A Mac with no `/private/var/lib` would refuse the same way at the real root. Both now create a missing parent at `0755`, and their own directory at `0750`, as `MkdirExactNoFollow` already did. No released manager ran on a Mac, so no installation holds the old modes.
evidence: internal/infra/lock/lock.go:71-78
action: departed
proposal: ASSUMED (decision 16, unchanged) — a parent the lock or the journal creates is `0755` on every platform, and `init` sets the managed directories among them to their own modes
```

**Drift count: 3.** P1 left the parents its lock and journal create at `0750`.

```divergence
decision: 16
grade: ASSUMED
kind: resolved
at: 2026-11-18T17:01:01Z
attempt: 2
claim: The review of this pull request found the first attempt short twice. Under a umask of `077`, which `sudo` keeps from a user who set it, a parent created at `0755` is `0700`, and the user who runs Docker Desktop still cannot list it. And the lock created every parent of its own directory at `0755`, `VarDir` and `ManagerDir` among them, which the layout holds at `0750` on Linux. Now the lock of an installation and the journal create the directories above `VarDir` at `0755` whatever the umask, leave one that exists as it is, and create the layout's own at `0750`, until `init` sets their modes.
evidence: internal/infra/atomicfs/atomicfs.go:256-272
action: decided
```

**Drift count: 3.** The review found no new drift.

## The probe's listing, after the close-out

```divergence
decision: 16
grade: ASSUMED
class: discovery
at: 2026-11-18T21:21:47Z
attempt: 1
claim: Decision 16 keeps the age identity, the signing key, the state, the backups and the secrets directory root's, inside `EtcDir`, `VarDir` and `RunDir`, and the file-sharing check lists those three from a container. On the author's Mac, with #392's parents at `0755`, the container started and the check still refused all three, and passed `OptDir`, where nothing is root's alone. busybox's `ls` stats each entry it lists, and through Docker Desktop's file sharing an entry the user who runs it cannot open does not exist: `ls -a` in `etc/demo` printed `.` and `..` and `ls: /p/installation.yaml: No such file or directory` for each of root's, and exited 1, while a shell glob in the same container named every entry. So no layout decision 16 describes could pass the check. The check now reads a listing by what `ls` prints: a directory read prints at least `.`, and one that cannot be read prints nothing, which the pinned busybox showed for a `0311` directory. It runs no tool it did not run before.
evidence: internal/adapters/runtime/compose/sharing.go:57-61
action: departed
proposal: ASSUMED (decision 16, unchanged) — the check lists a directory as a container reads it, by its names, and an entry the engine's user cannot open, which a container does not see, does not fail it
```

**Drift count: 3.** The listing was a discovery: no run before the Mac's put a container in a directory holding one of root's.

## The record's binary, after the close-out

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-18T22:17:15Z
attempt: 1
claim: Decision 20 has the release PR commit the record, so the run comes before the tag, and nothing says what version the binary it runs reports. `just acceptance-record` built it through `build`, stamped by `git describe`, and on the author's Mac it reported `0.17.1-25-g83a8803`. On a Mac the run gives both bundles the floor darwin needs, `PlatformsMinManagerVersion`, 0.18.0, and `update` checks a release's floor where `init` and `apply` do not, so the update the run kills was refused at `check-compatibility` before its backup: `release 1.3.0 requires manager >= 0.18.0, this is 0.17.1-25-g83a8803`. Linux raises no floor, so CI never met it. Decided: the recipe builds the binary stamped with the version it records, as the tag's build will be. Refused: lowering the fixtures' floor, and letting a build `git describe` stamps pass a floor, since either runs something other than the release.
evidence: justfile:347-349
action: decided
proposal: ASSUMED — `just acceptance-record <version>` runs a manager stamped `<version>`, the release it records, so the floor a darwin bundle declares is met as it will be by that release
```

**Drift count: 3.** The version the recorded binary reports was a gap in the spec, not drift.

## The rendered secrets, after the close-out

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-19T05:55:52Z
attempt: 1
claim: Every apply, update and restore renders the secrets, and the render replaced each file, a temp file then a rename, whether or not its value had changed. Compose mounts each file into the containers that read it. On Linux a container keeps reading the file that was replaced. On the author's Mac, through Docker Desktop's file sharing, it finds none: a container that mounted a file read `one`, then after a rename over it `cat: can't open '/run/secrets/f': No such file or directory`, then `two` after `docker restart`. So in the acceptance run the app, created by the restore and left running by the update after it, could not read `/run/secrets/db_password`. Decided: a render writes a secret's file only when it is not already the file it would write, with its value, `0400` and its owner. Refused: writing in place, which a crash leaves half written and a container can read torn. A value that changes is still a new file, and the services that declare it are restarted, which the same measurement showed brings it back. The notice for a secret no service declares now says a Mac's container finds no file until it restarts.
evidence: internal/adapters/secrets/sopsage/sopsage.go:559-569
action: decided
proposal: ASSUMED — a render leaves a secret's file in place when it already holds the value, the mode and the owner it would be given, since through Docker Desktop's file sharing a container loses a bind-mounted file the host replaces, until it restarts
```

**Drift count: 3.** The replaced file was a discovery: no run before the Mac's read a secret from a container started before the last render.

## Bundled images on a Mac, after the close-out

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-19T07:48:56Z
attempt: 1
claim: RFC 0055 does not mention a release whose images travel in the bundle. RFC 0011 decision 18 serves them from a registry in the manager's process, on `127.0.0.1:0`, and the daemon pulls them from it. Docker Desktop's daemon runs in a VM whose loopback is its own, so on the author's Mac the acceptance run's `init` of the bundle it packs failed at `ingest-images` with `cannot load localhost:5000/demo/app out of the bundle`, and rolled back. The ingest's own hint already said a daemon in a VM cannot reach it, so this could have been known before any code. RFC 0011's table is ungraded, so its rows were treated as locked and the run halted, and the author ruled on 2026-11-19 that a Mac refuses such a release for now. Decided: on darwin, a release whose images travel in the bundle is refused wherever a release not made for darwin is: before it is adopted, in a plan, and before an update, a rollback or a start, naming the images and why. Refused for now: serving the layout where the VM can reach it, and loading it with `docker load`, which depart from RFC 0011 decisions 18 and 21 and need measuring first.
evidence: internal/adapters/runtime/compose/ingest.go:132-138
action: decided
proposal: ASSUMED — on darwin a release whose images travel in the bundle is refused, as one not made for darwin is, since Docker Desktop's daemon cannot reach the loopback RFC 0011 decision 18 serves them on. Ruled by the author 2026-11-19
```

```divergence
decision: 9
grade: ASSUMED
class: discovery
at: 2026-11-19T07:48:56Z
attempt: 1
claim: The entry of 2026-11-18T11:08:30Z had the restart's installation install the bundle the run packs, which carries its image and needs no registry. A Mac now refuses that bundle, by the entry above, so on a Mac the restart's installation installs the 1.2.0 bundle the run began with, while the throwaway registry is still up. Its images are in the engine's store before the restart and stay there across it, and the start at boot pulls nothing, which is what the packed bundle was there to ensure. The phase after the restart reads no image.
evidence: .github/scripts/acceptance-reboot.sh:1-127
action: departed
proposal: ASSUMED (decision 9, unchanged) — on a Mac the restart's installation installs a release that pulls its images, while the run's registry is up, and the start at boot pulls nothing
```

**Drift count: 3.** Neither was drift: the first was a gap in RFC 0055, the second a discovery the first forced.
