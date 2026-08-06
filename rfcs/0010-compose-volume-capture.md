# RFC 0010 — Capturing Compose volumes

- **Status:** ✅ Complete — shipped 2026-08-05. Decision 10 resolved in favour of
  cold-by-default (§5.2 b), refined to per-service quiescing; §12 records what
  the implementation changed about the design.
- **Scope:** Lets a backup include the contents of the Compose project's
  volumes, so a release that ships no backup hook — or one that covers only its
  database — still produces a restorable backup. Covers how a volume is read
  without a client for whatever wrote it, the consistency problem that makes
  this dangerous, how the manifest declares what is safe to copy hot, sizing,
  and the manager's first image of its own. Explicitly **not** in scope:
  replacing the hook contract (hooks stay authoritative for anything with a
  transaction log), where a backup goes (RFC 0009), or incremental capture.
- **Related:** [`internal/adapters/backup/hookbackup/`](../internal/adapters/backup/hookbackup/),
  [`internal/adapters/runtime/compose/`](../internal/adapters/runtime/compose/),
  [`internal/ports/backup.go`](../internal/ports/backup.go),
  RFC [0007](0007-operator-parameters.md) for the manifest-declaration
  precedent, RFC [0009](0009-backup-targets.md) for where the result goes

---

## 1. Summary

A `volumes` component. The manager enumerates the Compose project's named
volumes, reads each one through a helper container, and adds the result to the
backup beside whatever the hook produced.

The whole RFC hangs on one question — **what does it mean to copy a volume
while something is writing to it** — and §5.2 is that question with three
answers and a recommendation.

## 2. Motivation

Two gaps, one of which is worse than it looks.

**A release with no backup hook produces no backup.** `hookbackup.Create`
refuses outright:

```go
spec, ok := e.release.Manifest.Operation(domain.OpBackup)
if !ok {
    return ports.BackupRef{}, domain.BackupError(domain.ErrUnsupported,
        "this release declares no backup operation")
}
```

That is correct today — the manager genuinely cannot know how to dump the
product's database — but it means an operator running a vendor who never wrote
the hook has `morzer backup` and nothing behind it.

**A release with a backup hook usually backs up only its database.** The hook
author thinks about Postgres. They do not think about the uploads volume, the
generated-thumbnails volume, the Caddy certificate store, or the queue's spool
directory. Those are on named volumes, they are not in the dump, and nobody
notices until a restore produces a working database and an application with no
files.

The second is the one worth the RFC. A backup that restores the database and
loses every uploaded document is a backup that passed verification and did not
work.

## 3. Current state

Verified against the code.

**The manager already knows the project.** `ports.RuntimeConfig` carries the
Compose project name and files, and `compose.Runtime.Validate` returns the
resolved configuration as `Rendered.Config` — the merged document that names
every volume. Nothing reads the volume list from it yet.

**Nothing in the manager reads a volume.** `captureManagedComponents` copies
files the manager itself owns — `installation.yaml`, `application.yaml`, the
encrypted secret state, the manifest. `recordArtifacts` checksums what the hook
wrote. Neither touches Docker.

**The components are a closed set** in `ports`: `database`, `files`, `config`,
`secrets`, `manifest`. `files` exists and is documented as coming from the
hook. This RFC adds `volumes` rather than reusing `files`, because the two have
different consistency stories and a restore needs to tell them apart.

**Every image is pinned by digest** and comes from the release manifest.
`Manifest.Validate` refuses anything else. The manager has never needed an
image of its own, which §5.4 changes.

**Backups are encrypted** (RFC 0008 §18 / the backup encryption work) and each
component is checksummed as stored. A volume tarball is just another component
and inherits both.

## 4. Goals / Non-goals

**Goals**

- A release with no backup hook can still produce a restorable backup.
- A release with a database hook does not silently lose its uploads.
- The operator can tell, from the backup manifest, which volumes were captured
  and whether each was captured hot or cold.
- Restoring a volume is refused unless the service using it is stopped.

**Non-goals**

- **Replacing the hook.** Anything with a transaction log — Postgres, MySQL,
  MongoDB, etcd — is the hook's job and stays the hook's job. §5.2 explains why
  a volume copy is not a database backup.
- **Bind mounts.** A bind mount points at an arbitrary host path that may be
  enormous, may be shared, and may be outside anything the manager manages.
  Named volumes only; a bind mount is reported, not captured (§8).
- **Incremental capture.** A full tarball each time. See RFC 0009 §8 for where
  that conversation belongs.
- **Cross-host restore of a volume.** A volume tarball restores onto a project
  with the same volume names. Restoring into a differently-shaped release is a
  migration and the release's own problem.

## 5. Design

### 5.1 What gets captured

From `compose config`, the project's **named volumes**, minus anything the
manifest excludes. Each becomes one component:

```
20260804T174743Z/
  backup.json
  database.sql.age           from the hook
  volumes/demo_uploads.tar.age
  volumes/demo_caddy_data.tar.age
```

The `ComponentRecord` gains what a restore needs to be safe:

```go
type VolumeRecord struct {
    Volume    string   // the Compose volume name
    Services  []string // services that mount it, from the resolved config
    Consistency Consistency // "cold" or "hot"
}
```

`Services` is not decoration. It is what lets a restore refuse to write into a
volume while a container has it open, and what lets the operator see, from the
manifest alone, that the uploads volume was captured while the app was running.

### 5.2 The consistency problem, which is the whole RFC

**Copying a live volume gives you a crash-consistent copy, not an
application-consistent one.** It is byte-for-byte what a power cut would have
left. Postgres will usually replay its WAL and come up, because that is what it
is built to do. "Usually" is not a property a restore path should have, and
other engines vary.

So a volume copy of a running database is not a database backup, and this RFC
must not let anyone believe it is. Three ways to be honest about that:

**(a) Quiesce.** `compose stop` → copy → `compose start`. Application-consistent
for everything, because nothing is writing. Costs downtime proportional to the
volume size.

**(b) Declare it.** The manifest says which volumes are safe to copy hot:

```yaml
backup:
  volumes:
    uploads:    { consistency: hot }   # write-once files
    caddy_data: { consistency: hot }
    pgdata:     { consistency: exclude } # the hook owns this
```

The vendor knows which is which. The manager refuses to guess: an undeclared
volume is captured **cold**, because the safe default is the slow one.

**(c) Complement, never replace.** Volumes cover what hooks do not, and the
hook stays authoritative. Cheapest and least likely to mislead, but it leaves
the no-hook release with nothing.

**Recommendation: (b) with (a) as its default.** A volume the vendor has not
classified is stopped before it is copied, which is correct and slow; a vendor
who wants a fast nightly backup declares `hot` for the volumes where it is
true and takes responsibility for that word. It follows RFC 0007's precedent
exactly — the vendor declares, the manager enforces, the operator sees the
result — and it degrades to (a) for a release that declares nothing, which is
every release that exists today.

**This was the open decision, and it was resolved in favour of (b).** The
argument that carried it: reliability first, with the flexibility made explicit
on both sides rather than assumed. The vendor declares `hot` and `exclude`; the
operator has `--no-downtime`, which **skips and reports** rather than silently
downgrading to a hot copy.

Two refinements the implementation added, both recorded in §12:

- **Only the services that mount a cold volume are stopped**, not the whole
  stack. `VolumeRecord.Services` already existed to make a restore refusal
  precise, and it makes the quiesce precise for free — capturing a certificate
  store stops the web server, not the database.
- **All cold volumes share one stop-and-start**, so a project with four
  undeclared volumes has one downtime window rather than four. The total is the
  same; the number of dips an operator sees is not.

### 5.3 Reading a volume

A volume is not on the host filesystem in any way the manager may rely on
(`/var/lib/docker/volumes` is an implementation detail, and is inaccessible
under rootless or a remote daemon). The supported way is a container:

```sh
docker run --rm \
  -v <volume>:/src:ro \
  -v <staging>:/dst \
  <helper-image> tar -C /src -cf /dst/<volume>.tar .
```

Read-only on the source, so a helper that misbehaves cannot write into the
product's data. The tarball lands in staging, then goes through the same
encryption every other component does.

Restore is the same in reverse, and **refuses while any service in
`VolumeRecord.Services` is running**. Untarring into a volume a container has
open is how a restore corrupts the thing it was restoring.

### 5.4 The manager's first image

§5.3 needs a container image, and that is a new category. Every image today
comes from the release manifest, pinned by digest, and `apply --startup` must
work with no network on a rebooted machine.

So the helper image:

- is **pinned by digest** in the manager's own source, the way `dockerlab`
  pins its fixtures;
- is **checked for locally before use**, via the `HasImage` path `Pull`
  already uses to make offline applies work;
- is **reported by `doctor`** when absent, with the command to pull it, so an
  operator preparing an air-gapped machine learns about it before backup night
  rather than during it;
- is **busybox or equivalent** — `tar` and nothing else. The smaller the image,
  the smaller the thing an operator has to trust and cache.

An escape hatch for the operator whose registry is not reachable:
`MORZER_VOLUME_HELPER_IMAGE` names a different image, since any image with a
POSIX `tar` will do. An environment variable rather than a state field, because
the backup that needs it is the scheduled one: a systemd drop-in reaches that
without regenerating a unit or migrating state. §12 records how it got there:
the hatch existed in the code first and was reachable from nothing.

### 5.5 Size, which is where the existing design stops working

The retention policy counts **backups**, not bytes. `DirSize` walks the tree.
`Verify` re-reads every component. All three are fine for a directory of
database dumps and none of them are fine for a hundred gigabytes of uploads
copied nightly.

This RFC does not solve that, and must not pretend to. What it does:

- **Measures before it copies.** The volume's size is read first, and a backup
  that would exceed the free space on `BackupsDir()` is refused before anything
  is written — with the numbers, because "no space left on device" halfway
  through is a worse message than "this needs 140 GiB and you have 60 GiB".
- **Warns in `doctor`** when the backup directory's growth rate implies the
  retention count will not fit.
- **Names the successor.** Deduplicating capture is RFC 0009 §8's restic
  conversation, and volume capture is the thing that will force it. Say so here
  rather than discovering it in a support thread.

## 6. Tests

| Level | What |
| --- | --- |
| Container | A Compose project with a named volume, a file written into it, a backup, the volume destroyed, a restore, the file read back — the same shape as the Postgres round trip |
| Container | The volume tarball in the backup is **encrypted**, and the file's contents do not appear in it |
| Consistency | A `cold` volume: the service is stopped during capture and running afterwards, asserted by polling `Status` mid-backup |
| Refusal | Restoring into a volume whose service is running is refused **by name** |
| Refusal | A backup that would not fit is refused before anything is written, with both numbers in the message |
| Refusal | A bind mount is reported and not captured, so an operator is not silently short a volume |
| Offline | With the helper image absent and no network, the backup fails with the pull command rather than a Docker error |

`dockerlab` already starts Compose projects with named volumes — the
volume-preservation test written for RFC 0008 does exactly this — so the
fixture exists.

## 7. Docs

- [operating/backups](../pages/docs/operating/backups.md): what `volumes`
  covers, why it is not a database backup, and what `consistency: hot` means
  the vendor is promising.
- A new authoring page for the `backup.volumes` declaration, beside the
  parameters one.
- The claims table: the restore refusal, the encryption of the tarball, the
  space check.
- The reference page for the helper image and how to pre-pull it for an
  air-gapped install.

## 8. Out of scope, and what would change that

**Bind mounts.** Reported, never captured. A bind mount is an arbitrary host
path: it can be `/`, it can be a network mount, it can be shared with something
the manager knows nothing about. Capturing one means the manager deciding how
much of somebody's filesystem to copy. What would change it: a declaration in
the manifest naming a bind mount as part of the product's data, at which point
it is the vendor's claim rather than the manager's guess.

**Database volumes.** `consistency: exclude` is expected to be the common
declaration for them, and the docs will say so plainly. What would change it:
nothing in this RFC. A vendor who declares their Postgres volume `hot` has made
a claim about their own product, and the manifest is where they make it.

**Live snapshots.** LVM, btrfs and ZFS can snapshot atomically, which would
give application-consistent capture without downtime. Out of scope because it
is host-specific, needs privileges the manager does not otherwise want, and
would make backup behaviour depend on the filesystem somebody chose at install
time. What would change it: enough operators on ZFS to make it worth a
provider.

## 9. Risks

**The headline risk is somebody believing this replaces their database
backup.** A volume copy that restores cleanly nine times out of ten is worse
than one that never works, because it teaches the wrong lesson. Mitigations:
the default is cold; `hot` is a word the vendor has to write; the docs say it
in the operating guide rather than a footnote; and the manifest records which
volumes were captured how, so a post-incident review can see what was promised.

**Downtime by default will surprise people.** §5.2 (b) means a release that
declares nothing has services stopped during a backup. That is the safe
behaviour and it is the wrong behaviour for somebody running a nightly backup
of a busy service. Accepted rather than designed away — decision 10 — with
three things holding it down: only the services that mount a cold volume are
stopped rather than the whole stack, they are stopped once for all of them
rather than once per volume, and `--no-downtime` is the operator's way out.
The risk that remains is real and is now shaped differently: `--no-downtime`
**skips and reports** rather than downgrading to a hot copy, so the operator
who cannot afford the window gets a backup that is honestly short a volume, and
the only way to have both is the vendor declaring `hot`.

**The helper image is a new supply-chain edge.** One more digest to pin, one
more thing to have cached offline, one more image an attacker would like to
replace. Mitigated by pinning, by the local-first check, and by choosing an
image whose whole content is `tar`.

**Size will break retention before anything else does.** §5.5 measures and
refuses rather than solving it. The first operator with a large uploads volume
will want dedup, and that is a different RFC — but it should not be a surprise
when they arrive.

## 10. Decisions

| # | Decision |
| --- | --- |
| 1 | `volumes` is a new component, not an extension of `files`. They have different consistency stories and a restore has to tell them apart. |
| 2 | Named volumes only. Bind mounts are reported and never captured; §8 records what would change that. |
| 3 | A volume is read through a container with the source mounted **read-only**. The host's Docker storage layout is an implementation detail and is never touched. |
| 4 | The default consistency is **cold**: an undeclared volume is captured with its services stopped. The safe default is the slow one. |
| 5 | `consistency: hot` is a claim the *vendor* makes in the manifest, not a guess the manager makes. It follows RFC 0007's declaration precedent. |
| 6 | Restoring into a volume is refused while any service that mounts it is running, named by service. |
| 7 | The helper image is pinned by digest in the manager's source, checked locally before use, and reported by `doctor` when absent — so an air-gapped install learns about it before it needs it. |
| 8 | A backup that would not fit is refused **before** anything is written, with the required and available figures in the message. |
| 9 | This does not replace the hook for anything with a transaction log, and the documentation says so where an operator will read it rather than in a footnote. |
| 10 | **Resolved: cold-by-default**, per §5.2 (b) with (a) as its default, refined so that only the services mounting a cold volume are stopped rather than the whole stack. The operator's escape hatch is `--no-downtime`, which **skips and reports** a volume rather than downgrading it to a hot copy — because a hot copy of an undeclared volume is the vendor's claim being made on their behalf, which decision 5 forbids. |
| 11 | `volumes` is in `AllComponents`, so every backup captures them without being asked. The motivating failure is an operator who does not know their uploads are missing; an opt-in component would have left them exactly where they were. |

## 11. Phasing

| Phase | What | Gated on | Status |
| --- | --- | --- | --- |
| **P1** | Enumerate volumes, capture cold, encrypt, record in the manifest, restore with the running-service refusal | Decision 10 | ✅ |
| **P2** | The `backup.volumes` manifest declaration, `hot` and `exclude`, the vendor-facing docs | P1 | ✅ |
| **P3** | The space check, the `doctor` growth warning, the helper-image checks | P1 | ✅ |

P1 was gated on decision 10 for a reason that held: cold-by-default and
complement-only have different manifests, different components and different
documentation, and building one to discover the other was wanted means building
it twice. The decision was made first, and all three phases shipped together.

## 12. What the implementation changed

Written after building it. Everything here is a place where the design above was
incomplete or wrong, kept rather than edited away so the next reader can see
which parts of an RFC survive contact.

**Quiescing is per service, not per project.** §5.2 (a) says `compose stop` →
copy → `compose start`, meaning the whole stack. The implementation stops only
the services that mount the cold volumes, because §5.1 was already recording
`VolumeRecord.Services` to make the restore refusal precise and the same list
makes the capture precise for nothing. Strictly less disruptive and no less
correct: a volume is only written by containers that mount it.

**One downtime window, not one per volume.** The cold volumes' services are
unioned and stopped once. Hot volumes are captured *before* the window opens,
because there is no reason for a volume the vendor said may be read live to be
read while the product is down.

**The helper writes to stdout, not to a staging bind mount.** §5.3's
`docker run -v <staging>:/dst ... tar -cf /dst/<volume>.tar` puts a root-owned
file into a directory the manager may not run as root in — and the manager then
cannot overwrite or remove it, so the plaintext copy of somebody's uploads
survives the backup that encrypted it. The tar comes out on stdout instead and
the manager writes the file itself. That needed `exec.Command.Stdout`, a raw
byte path added alongside the line scanner, which would otherwise have split the
stream on 0x0a bytes that are data and held the whole volume in memory to do it.

Two things fell out of that pipe which the RFC did not anticipate. A write
failure — a disk filling partway through a capture — used to be invisible: the
process exited zero and the truncated tarball was checksummed as if it were the
volume, so it verified. It now fails the command. And the pipe is *closed* on
that failure rather than drained, because draining meant reading the remaining
hundred gigabytes into `io.Discard` for an outcome already decided. Closing it
kills the child with SIGPIPE, which is why the write error has to be reported
ahead of the exit status: otherwise a full disk surfaces as "exited with code
141" and sends an operator looking for a bug in `tar`.

**A restore replaces rather than merges.** §5.3 says "the same in reverse",
which read as untar-over-the-top. That leaves files the backup does not contain
beside files it does, producing a volume that matches no point in time — and
beside a database restored to an exact one, that is how a record without its
file is made. The volume is emptied first.

That makes `RestoreVolume` **destructive and unrecoverable**, which §5.3 does not
say and should: the volume is emptied *before* the tar is extracted, so a
failure partway through leaves the volume holding neither what it had nor all of
what the backup held. There is no rollback and no staging copy — the manager
does not have a second volume's worth of space to promise one. The mitigations
are the ones already in the path rather than a repair: the backup is verified and
decrypted before anything is emptied, the restore is refused while any service
holds the volume open, and the whole operation is behind `--force` and a typed
installation id. An operator whose restore fails here restores again from the
same backup; the volume is not left in a state anything else can use.

**The space check saturates rather than wraps.** Found by the test for decision
8: summing volume sizes overflowed `int64`, came out negative, compared as
*smaller* than the free space, and turned the refusal into a pass. The one
direction that check must never fail in.

**Wrapping an error dropped its remedy.** Also found by a test — the one for the
air-gapped machine. "The helper image is not here" carried `docker pull <ref>`
as its hint; wrapping it as "cannot capture volume uploads" produced an error
whose hint was empty, because `AsError` reports the outermost structured error.
An operator on the machine where that message matters most got a diagnosis and
nothing to do about it. `domain.Error.WithHintFrom` now carries a cause's remedy
through a wrap that has none of its own.

**The backup manifest records what was *not* captured.** Not in the RFC. §8 says
a bind mount is "reported", and the only place a report survives to be read
during an incident is the manifest itself — so `Uncaptured` names every volume
left out and why: excluded by the vendor, skipped by `--no-downtime`, or a bind
mount that was never a candidate.

**The backup manifest schema went to 3.** Not called for in the RFC, and
necessary: a manager that predates volumes reads a schema-2 backup, does not
know what `ComponentVolumes` means, decrypts the tarballs into the staging
directory and hands them to a restore hook that was never told about them. The
database comes back, the uploads do not, and nothing says so.

**A missing helper image fails the whole backup.** The alternative — take
everything else and omit the volumes — was considered and rejected: a backup
that silently covers less than it claims is the failure this component exists to
prevent. The operator who wants one anyway scopes it with `--component`.

**`Stop` and `Start` joined the `Runtime` port.** `Down`/`Up` were the only pair
available and both are wrong here: `Down` removes containers and networks, and
`Up` reconciles against the declared configuration, so resuming a stack after a
backup could recreate a container whose definition had drifted. A backup must
not be the thing that applies a change nobody asked for.

**`FreeSpace` moved from `preflight` to `atomicfs`.** An adapter measures before
it copies, and an adapter may not import the lifecycle layer. `preflight.FreeSpace`
remains as a delegating name so its callers did not move with it.

**The new port had two implementations and no shared battery.** `VolumeInspector`,
`VolumeCapturer` and the added `Stop`/`Start` are one contract implemented by
`compose.Runtime` and by the in-memory fake — and every consistency assertion in
this RFC's work was made against the fake, which was never checked against the
real adapter on any of it. `test/contract/runtime.go` existed and was not
extended. Fixed by adding fourteen legs run against both, which found that the
fake did **not** honour the port's promise that `Volumes` are sorted: it returned
insertion order, so a test could depend on an ordering production never produces,
and a regression in the adapter's sorting would have been invisible to every
fake-backed test. The backup manifest records volume components in that order.

The state predicates moved to `ports.ServiceState` in the same change. They read
the runtime's own vocabulary, so the runtime's port is where they belong — and it
is what lets the contract suite hold every implementation to one reading of
`exited` versus `paused`, rather than the backup engine holding a private opinion
about strings another package produces.

**A paused container is neither running nor stopped, and both halves missed it.**
Found by the self-audit, and the worst defect in the branch. The refusal and the
quiesce both asked `state == "running"`, so a paused service — frozen mid-write
with its file handles open — was invisible to both: a restore untarred straight
over a volume two paused containers were holding and reported success, and a
cold capture read a volume a paused container had open while recording the copy
as `cold`, which is the one claim the whole component rests on. The predicate is
now written as what does *not* occupy a volume (exited, created, dead, absent),
so a state this manager has never seen refuses rather than permits. `docker
compose pause` is a thing operators do during maintenance, which is exactly when
they also take a backup.

**The no-hook refusal checked intent rather than outcome.** Also found by the
audit. The gate passed when volumes were merely *in scope* — which they always
are — so a release with no hook whose data lives on bind mounts got past it and
produced a backup holding the configuration and nothing of the product. `backup
list` offers that, and somebody eventually restores it. The refusal now fires on
what was actually captured.

**§5.4's escape hatch was written and never wired.** `WithHelperImage` existed,
was called only from its own tests, and carried a doc comment describing itself
as "the escape hatch for the operator" — an operator who had no way to reach it.
It is now `MORZER_VOLUME_HELPER_IMAGE`, an environment variable rather than a
flag or a state field, because the backup that needs it is the scheduled one and
a systemd drop-in reaches that without regenerating a unit or migrating state.

**A volume name is release-supplied and becomes a path.** Not considered in the
RFC. The name comes out of a Compose file somebody else wrote and is joined into
the backup directory as `volumes/<name>.tar`, so a name containing a separator
would write outside it. Compose is unlikely to accept one — and "the other tool
probably rejects it" is not a containment argument, which is the same reasoning
`blob.Fetch` records for the guard it applies to component paths. Refused by
name, the way a hook artifact outside the backup directory already is.

**Only the services that are actually running are stopped.** Found by asking
what a backup of an already-stopped deployment does — a normal thing to take
before maintenance. The quiesce stopped and started the whole service list
unconditionally, and `compose start` on a service with no container exits
non-zero: so the backup captured its volumes perfectly, then deleted them and
reported that it could not bring back a product nobody had taken down. It also
means a backup no longer starts a service the operator had deliberately stopped.

**The container packages share one Docker daemon and were racing for it.** The
contract battery added several project lifecycles to `test/suite`, and
`test/clitest` — which contains no Docker reference and reads as a pure CLI
suite — began failing with "cannot restart services". It reaches Docker without
looking like it: `secret rotate` shells out to `docker compose restart` through
the real adapter and expects exit zero. Reproduced deterministically by putting a
failing `docker` shim on PATH, which produces that exact error. The fix is `-p 1`
on the docker-tagged recipes: Go runs packages in parallel by default, and these
cannot be. The coupling predates this work; the load that exposed it does not.

**The stop timeout is injectable, and finding out why cost an hour.** A service
gets two minutes to shut down cleanly before it is killed — generous on purpose,
because a database being quiesced for a volume copy is exactly the process that
should be allowed to flush. But the container fixture's PID 1 is a shell loop,
and the kernel does not deliver a signal with a default action to PID 1, so it
never sees SIGTERM and every quiesce waited out the full two minutes. Setting
`stop_grace_period` in the fixture's Compose file does not help: `compose stop
--timeout` overrides it. Injecting the timeout took the container suite from
742 seconds to 189.

**A restore scoped away from volumes does not decrypt them.** Staging decrypts
every component, because the hook ABI predates scoping and a hook that reads
more than it was told to would break. Volumes are new, so no hook can be reading
them — and a `--component database` restore that decrypted a hundred gigabytes
of uploads in order to delete them unread is a long wait for nothing.
