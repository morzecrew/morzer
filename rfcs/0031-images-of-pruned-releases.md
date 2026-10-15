# RFC 0031 — Images of pruned releases

- **Status:** 📝 Draft — written 2026-10-15, **P1 (a spike) is the next work**.
  Nothing is built.
- **Scope:** What happens to container images when a release is pruned. Today
  the release directory goes, including the image layout a bundle carried, and
  every image the runtime pulled or ingested for that release stays in its
  store for the life of the machine. This RFC removes those images when the
  release that needed them is pruned, and only when no release of *any*
  installation on the machine still names them. Covers the candidate set, the
  machine-wide exclusion, what the runtime port gains, the order of removal,
  failure semantics and what `release prune` reports. Deliberately not a
  general image garbage collector, not `docker image prune`, not automatic
  pruning on a production machine, and not deduplication of layers across
  releases ([0011](0011-bundled-container-images.md) §8 keeps that apart).
  One optional runtime capability; no manifest change; no installation schema
  change.
- **Related:** [`internal/lifecycle/ops/prune.go`](../internal/lifecycle/ops/prune.go)
  (`PruneReleases`, `InstalledReleases`),
  [`internal/lifecycle/ops/update.go`](../internal/lifecycle/ops/update.go)
  (`stepPruneReleases`, dev mode only),
  [`internal/cli/release.go`](../internal/cli/release.go) (`release prune`),
  [`internal/domain/image.go`](../internal/domain/image.go)
  (`RuntimeImageRefs`, `LocalAlias`),
  [`internal/ports/runtime.go`](../internal/ports/runtime.go) (`ImageInspector`,
  `ImageIngester`), [`internal/lifecycle/ops/list.go`](../internal/lifecycle/ops/list.go)
  (`DiscoverProducts`, `forInstallation`),
  [`tools/runtimecheck`](../tools/runtimecheck),
  [0011](0011-bundled-container-images.md) (§8 deferred this; decision 20),
  [0020](0020-several-installations-on-one-machine.md) (installations share a
  daemon), [0023](0023-runtimes-beyond-compose.md) (decision 7 and the §6
  port budget), [0001](0001-update-and-rollback.md) (`rollback --to`)
- **Origin:** Item 5 of the requests filed while packaging `erp-standalone` as a
  bundle, 2026-10-15. A dev VM following the rolling `main` channel pulls a new
  backend image, about 50 MB, on every update; dev mode prunes releases after
  every update and nothing removes their images, so the disk fills fastest on
  exactly the machine meant to be disposable.

---

## 1. Problem

Retention reclaims a release directory and leaves its images. On a production
machine that is slow, because releases are pruned only when an operator runs
`release prune`. On a dev machine following a channel it is the steady state:
every update pulls new digests, every update prunes, and the image store grows
by one release's private images per update until the disk is full. No command
the manager offers gets those bytes back. `docker image prune` does, and also
removes whatever else on the host it judges unused, which is not the
manager's to decide.

[0011](0011-bundled-container-images.md) §8 saw this coming and deferred it:
*"Garbage collection of ingested images. Images loaded for a release that was
later rolled back stay in the local store. Existing `docker` pruning applies; a
manager-owned lifecycle is a separate design."* This is that design, scoped to
the narrowest lifecycle that answers the complaint: an image goes when the
release that needed it goes, and not otherwise.

## 2. Current state

Verified against `bd74239`.

- **Pruning removes the directory and nothing else.** `PruneReleases`
  (prune.go:112-150) calls `atomicfs.RemoveAll(e.Root)` on each release beyond
  retention. Current, previous and staged releases are exempt (`Exempt`,
  prune.go:39), and the count is over non-exempt releases only. The directory
  holds the bundle's own image layout, `images/` (`release.ImagesDirName`,
  internal/release/imagelayout.go:23), so a bundled image's *layout* is already
  reclaimed. What is never reclaimed is the copy in the runtime's store.
- **What the store holds for a release** is exactly `Manifest.RuntimeImageRefs`
  (image.go:320-327): the manifest's digest-pinned reference for a pulled image,
  and for a bundled one the alias `<repo>:morzer-sha256-<hex>`
  (`ImageSpec.LocalAlias`, image.go:150-158), derived from the digest and so
  identical in every installation that bundles the same image. The only image
  removal in the codebase is the best-effort untag of ingest's loopback name
  (internal/adapters/runtime/compose/ingest.go:146-154).
- **Dev mode prunes after every update; production never does.** update.go:407-416
  appends `stepPruneReleases` only `if inst.IsDev()`, and says why: on a
  production host, retention is the operator's decision about disk they own.
  The step is `OnFailure: engine.Continue` (update.go:825-831).
- **`release prune --dry-run` exists** — `--dry-run` is the global flag,
  passed into `PruneReleases` (internal/cli/release.go:423) — but its JSON is
  `{"removed": …, "retained": …}` (release.go:428-430) with **no marker that it
  was a dry run**: a script reading `removed` cannot tell a plan from an act.
- **Prune takes no lock and journals nothing** (prune.go has no `withLock`).
- **Installations share one image store.** [0020](0020-several-installations-on-one-machine.md)
  says so twice (:31-33, and the non-goal at :156: *"They share a Docker
  daemon, a port space, a `/run` tmpfs and a disk"*). Two installations of one
  product, or two products built on a common base, reference the same digests,
  and a bundled alias is the same string in both. The runtime port answers
  `Status` for one project at a time (`ServiceState.Image`, ports/runtime.go:325),
  so nothing in the port can see another installation's containers, let alone
  workloads the manager did not start.
- **The machine can be enumerated.** `DiscoverProducts` (list.go:334) reads
  `<root>/etc/*/installation.yaml` and returns `Products` and `Undecidable` —
  directories this process could not open — and `forInstallation` (list.go:273)
  scopes a read-only `Deps` to another product. `ls` is built from both.
- **A retained release is a rollback target**, through `rollback --to` and
  `update --to`, and [0011](0011-bundled-container-images.md) decision 20
  makes a missing bundled image a refusal, never a pull. Removing a retained
  release's images would turn an offline rollback into a refusal; removing a
  pruned release's images costs nothing, because without its directory the
  manager cannot install that release at all.
- **The runtime port has no image listing or removal.** Image capabilities are
  optional and type-asserted: `ImageInspector.HasImage` (ports/runtime.go:423)
  and `ImageIngester` (:529). [0023](0023-runtimes-beyond-compose.md) decision 7
  (`LOCKED`): *"The port may grow methods; it may not grow `switch kind`"*, and
  `tools/runtimecheck` fails the build on `compose`, `docker`, `podman` or
  `quadlet` above `internal/adapters` (check.go:31). §6 of 0023 counts the
  whole port surface — 13 core methods and 8 optional capabilities — as the
  baseline the Quadlet adapter is measured against.

### 2.1 Measured: the daemon guards the last name, not the image

Docker 29.7.2, 2026-10-15, with a throwaway image built `FROM scratch`, tagged
twice, and a *stopped* container created from the first tag:

```text
$ docker image rm probe:a            # one of two names; a container uses the image
Untagged: probe:a                    # exit 0
$ docker image rm probe:b            # the last name
Error response from daemon: conflict: unable to remove repository reference
"probe:b" (must force) - container 24a6912ae17c is using its referenced image
76d69844b895                         # exit 1
$ docker image rm <id>
Error response from daemon: conflict: unable to delete 76d69844b895 (must be
forced) - image is being used by stopped container 24a6912ae17c   # exit 1
```

So an unforced removal will never delete bytes a container still needs — and
will **happily remove a name** while another name keeps the bytes. That is the
case that matters here: another installation's retained release resolves its
images *by name*, and a removed alias is a refused rollback there
(0011 decision 20) whatever the daemon kept. The daemon's refusal is a guard
for containers. It is not a guard for other installations, and the design may
not lean on it as one (decision 4).

Not measured, and P1's job: what `docker image rm repo@sha256:…` does to an
image the daemon recorded as a digest reference after a pull, which is how
every non-bundled image of a release is named.

## 3. Goals / Non-goals

**Goals**

- A pruned release's images leave the store with it, on both pruning paths:
  `release prune` and dev mode's automatic prune.
- No image a release of *any* installation on the machine still names is
  removed, and no image a container uses is deleted.
- An operator can see beforehand what would go (`--dry-run`) and afterwards
  what went and what was kept, with why.

**Non-goals**

- **A general garbage collector.** Images the manager did not put in the store
  for a release it is pruning are not its to judge — another product's,
  the busybox volume helper's, an operator's own. `docker image prune` exists
  for the host's owner.
- **Automatic pruning on a production machine.** update.go:407-413 is a
  deliberate asymmetry and this RFC keeps it: production images go only when an
  operator prunes.
- **Images named only in Compose files.** A vendor may hard-code an image the
  manifest never declares. It is not in `RuntimeImageRefs`, the manager never
  pulled it by that identity, and it is not a candidate.
- **Layer deduplication** ([0011](0011-bundled-container-images.md) §8, a
  different and larger design).

## 4. Design

### 4.1 The candidate set and the exclusion set

```text
candidates = ⋃ RuntimeImageRefs(r)   for r in the releases this prune removes
referenced = ⋃ RuntimeImageRefs(r)   for every release directory that remains,
                                     in every installation DiscoverProducts finds
remove     = candidates − referenced
```

`referenced` is every release directory still on disk, in this installation and
in every other, whatever its role — current, previous, staged, or merely
retained. Roles decide what *this* prune may delete; for the exclusion they do
not matter, because every directory present is something `rollback --to` or
`update --to` may install.

The manifests on disk *are* the reference count. No index of images is kept in
state: a second source of truth would be wrong exactly when a machine was
rebuilt by hand, which is the reason `DiscoverProducts` reads the filesystem
rather than an index (list.go:317-322).

### 4.2 When the machine cannot be read, nothing is removed

Removal is skipped entirely, and the report says why, when any of these holds:

- `DiscoverProducts` returns an `Undecidable` directory — one this process
  could not open, which may be an installation;
- a release directory in **any** installation, this one included, has a
  manifest that does not load. `InstalledReleases` skips such a directory for
  listing (prune.go:79-85), which is right for `release list` and wrong here:
  an unreadable manifest names images nobody can enumerate.

Release directories are still pruned; only the image step is withheld. Two
directories the manager itself writes meet the second rule, and both are
intended: the `.staging-*` directory of a fetch in progress (update.go:746),
whose images are about to be pulled and which is the race §7 is about; and a
tree `stage-release` moved aside as unreadable, which it keeps until the next
one needs moving (update.go:742). The consequence is stated rather than hidden:
a broken directory anywhere disables image removal machine-wide until somebody
deletes it, and the report names the directory so somebody can.

### 4.3 The capability

```go
// ImageRemover removes a name from the local image store.
//
// Optional and type-asserted, like ImageInspector and ImageIngester: a runtime
// with no local store has nothing to remove, and a mandatory method would make
// it answer a question it cannot have.
type ImageRemover interface {
	// RemoveImage removes ref from the local store. Never forced. A
	// reference the store refuses because a container uses its image is
	// ErrImageInUse, which the caller reports as kept and does not retry.
	RemoveImage(ctx context.Context, ref string) error
}
```

The Compose adapter answers it with `docker image rm <ref>`, without `-f`;
classifying the conflict response as `ErrImageInUse` is P1's to pin from the
daemon's real output. Podman's `podman image rm` has the same unforced
semantics, so the Quadlet adapter can implement it rather than stub it — the
condition [0023](0023-runtimes-beyond-compose.md) §6 actually cares about. A
runtime without the capability removes nothing, and the prune says so once.

### 4.4 Order

1. Read the manifests of the releases about to be pruned, and compute `remove`.
2. Remove the release directories, as today.
3. Remove the images in `remove`, one reference at a time.

Directories before images. The reverse order, interrupted, leaves a release on
disk whose images are gone — a rollback target that refuses (0011 decision 20).
This order, interrupted, leaves images nobody references, which is today's
state for every prune and costs only disk.

### 4.5 Failure never fails the prune

A reference that cannot be removed — in use, already gone, the daemon
unreachable — is reported as kept with the runtime's reason, and the prune
continues and exits 0. Image removal is reclamation; the release directories
were the operation. In dev mode's `prune-releases` step this is already the
step's policy (`OnFailure: engine.Continue`).

### 4.6 What is reported

```text
$ morzer release prune --dry-run
would remove 2 release(s): [1.4.0-dev.7.g3be286c 1.4.0-dev.6.g91aa0e2]
would remove 3 image(s):
  registry.example/erp/backend@sha256:4f1c…
  registry.example/erp/backend@sha256:77e0…
  registry.example/erp/worker@sha256:0b9d…
```

```json
{"dry_run": true, "removed": ["1.4.0-dev.7.g3be286c", "1.4.0-dev.6.g91aa0e2"],
 "retained": 3,
 "images": {"removed": ["…"], "kept": [{"ref": "…", "why": "in use by a container"}],
            "skipped": ""}}
```

`dry_run` is added whatever else changes. Without it `removed` reads the same
for a plan and an act (§2), which is a defect in today's output, not a feature
of this one. `images.skipped` carries §4.2's reason when the image step did not
run at all.

### 4.7 Alternatives considered

- **Lean on the daemon's in-use refusal alone.** Cheapest: try to remove every
  candidate unforced and let the daemon say no. Rejected on the measurement in
  §2.1 — the refusal protects the last name of an image a container uses, and
  another installation's *retained* release has no container, only a name.
- **`docker image prune` after each prune.** Removes dangling or all unused
  images host-wide: other products', the operator's, the volume helper's.
  Rejected as a decision about disk the manager does not own.
- **Remove by image ID, forced.** Deletes bytes under every name at once,
  including under running containers of other workloads. Rejected outright.
- **Keep an image refcount in installation state.** A second record of what the
  release manifests already say, wrong after any hand repair. Rejected for the
  reason `DiscoverProducts` has no index.
- **An opt-in setting.** Considered and not taken for P2: pruning a release
  already says the operator no longer wants it, and a pruned release's images
  cannot be installed by the manager without its directory. Reopens if an
  operator turns out to need them kept — decision 9.

## 5. Tests

- **Suite, with the fake runtime implementing `ImageRemover`** and recording
  calls:
  - one installation, three releases, retention 1 — only the pruned releases'
    private refs are removed, never one the retained or current release names;
  - two installations under one root sharing a digest and a bundled alias —
    pruning in one removes neither while the other's release directory exists,
    whatever that release's role;
  - an `Undecidable` directory, and an unloadable manifest in the *other*
    installation — no image removal is attempted and `images.skipped` says why;
  - directory removal failing — no image removal is attempted;
  - `ErrImageInUse` from the runtime — reported as kept, prune exits 0;
  - a runtime without the capability — nothing removed, one line saying so;
  - `--dry-run` — the same lists, `dry_run: true`, and the fake records no call.
- **Docker lane**, against a real daemon: a pulled digest reference and a
  bundled alias are removed; a reference whose image a stopped container uses
  comes back as `ErrImageInUse`, not as a generic runtime error.
- **Sabotage** of the exclusion (drop another installation from `referenced`)
  must fail the two-installation case — that row is the reason the design is
  not §4.7's first alternative.

## 6. Docs

- `reference/release-commands.md`, `release prune`: what goes with a release,
  what is kept and why, the JSON shape including `dry_run`.
- `operating/several-installations.md`: images are shared, and a prune in one
  installation never removes what another still names.
- `operating/unattended-updates.md`, dev mode: the store no longer grows with
  every update.
- Careful wording: "removed from the image store" means the *name*; the bytes
  go when no other name and no container holds them. Promising freed disk per
  removed reference would be false for shared base layers.

## 7. Risks

- **A race with another installation's update.** Its release is fetched into
  a `.staging-*` directory and renamed into place before its images are pulled
  or ingested (update.go:746-767; `stage-release` precedes `pull-images`), and
  while the staging directory exists §4.2 withholds removal. The window that
  remains is this prune computing `remove` *before* that directory appears and
  removing *after* the other installation pulled or ingested the same
  reference. Its converge then meets a missing image: a re-pull for a pulled
  reference, and for a bundled alias the refusal 0011 decision 20 prescribes,
  which `release ingest` repairs. Accepted for P2, and measured in P1;
  decision 11 is where it changes.
- **Non-morzer workloads that use the same image but have no container yet**
  lose the name and re-pull. The daemon protects any that have one (§2.1).
  Named, accepted: the manager pulled that reference for its own release.
- **Discovery reads one root.** `DiscoverProducts` looks under the root the
  command runs with. An installation under the hidden `--root` flag — the
  demos, the acceptance run, a developer's experiment — shares the daemon and
  is invisible to a prune at `/`, and the reverse. Production has no prefix
  (`TargetPrefix` is "empty in production", ops.go:132-139), so the gap is a
  test and development hazard. Named so P2's docker lane does not prune at `/`
  on a machine running a `--root` installation, and accepted otherwise.
- **The fail-safe can be permanent.** One broken release directory anywhere
  disables image removal everywhere (§4.2). Chosen over guessing; mitigated by
  naming the directory in every report.
- **Read as "frees disk".** Shared layers stay while anything uses them. §6's
  wording carries this.

## 8. Decisions

| # | Decision | Grade | Why |
| --- | --- | --- | --- |
| 1 | Images are removed only as part of pruning a release, never by a separate sweep of the store | ASSUMED | The manager knows why it put an image there only through a release; a sweep would judge images it did not place. |
| 2 | Candidates are `RuntimeImageRefs` of the releases this prune removes — pulled refs and bundled aliases — and nothing named only in Compose files | ASSUMED | It is exactly what the manager made the store hold for that release (image.go:320). An undeclared image was never pulled under the manager's identity. |
| 3 | Excluded: every `RuntimeImageRefs` of every release directory remaining in every installation `DiscoverProducts` finds, whatever its role | LOCKED | Any directory present is a `rollback --to` / `update --to` target, and a removed name is a refused offline rollback there (0011 decision 20). Reversing it deletes another installation's ability to roll back. |
| 4 | The daemon's unforced in-use refusal is a guard for containers, not for references; the design may not rely on it in place of decision 3 | LOCKED | Measured (§2.1): removing one of several names succeeds while a container uses the image. |
| 5 | An `Undecidable` directory, or any release manifest on the machine that does not load, withholds all image removal for that prune; directories are still pruned and the report names the cause | LOCKED | An unreadable manifest names images nobody can enumerate; guessing deletes another installation's images. Consequence accepted: one broken directory disables removal machine-wide until it is removed. |
| 6 | Removal is a new optional, type-asserted runtime capability `ImageRemover`; never forced | ASSUMED | 0023 decision 7 lets the port grow methods; optional means a runtime without a store declines rather than stubs, and Podman can implement it, which is 0023 §6's real test. |
| 7 | Order: compute the set from the manifests, remove directories, then images | ASSUMED | Interrupted, the reverse leaves a release whose images are gone; this order leaves only leaked images, which is today's state. |
| 8 | A failed image removal never fails the prune: kept, with the runtime's reason, exit 0 | ASSUMED | Reclamation is not the operation; a prune that failed over disk it could not reclaim would be retried into the same refusal. |
| 9 | On by default on both pruning paths — `release prune` and dev mode's `prune-releases` — with no opt-out in P2; production still never prunes automatically | ASSUMED | Pruning already says the release is unwanted, and the manager cannot install a pruned release's images without its directory. An opt-out is added when an operator asks for one, not before. |
| 10 | `release prune` reports images removed and kept, `--dry-run` lists them, and the JSON gains `dry_run` | ASSUMED | The JSON cannot tell a plan from an act today (release.go:428). Additive keys only, so existing readers keep working. |
| 11 | Whether `release prune` takes its own installation's deployment lock, and whether anything coordinates with another installation's fetch in progress | OPEN | Today it takes none (§2). P2 measures the window in §7 against a real daemon and decides; taking the own lock is the default if nothing argues otherwise. |
| 12 | Whether the report distinguishes "name removed, bytes kept" from "image deleted" | OPEN | The daemon reports both (`Untagged:` / `Deleted:`). Settle in P2 if the distinction turns out to matter to an operator reading it; otherwise report the name. |

## 9. Phasing

- **P1 — measure.** On a real daemon: what `docker image rm repo@sha256:…`
  does to a pulled image (named by digest, usually untagged) and to a bundled
  alias; the exact in-use conflict output, to pin `ErrImageInUse`; and the
  §7 race window. Ships the measurements in this RFC (§2.1 grows) and nothing
  else. A finding that contradicts decision 3 or 4 halts P2.
- **P2 — build.** `ImageRemover` on the port and the Compose adapter; the
  sets, the fail-safe, the order and the report in `PruneReleases`; the
  `dry_run` key; dev mode's step picks it up by calling the same function.
  Decisions 11 and 12 settled and logged.
- **Demand-gated.** An opt-out (decision 9); the Quadlet implementation, which
  lands with [0023](0023-runtimes-beyond-compose.md) P3 rather than ahead of it.
