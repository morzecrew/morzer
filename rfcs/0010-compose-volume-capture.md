# RFC 0010 — Capturing Compose volumes

- **Status:** 📝 Draft — **design locked, one decision deliberately open** (§5.2)
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

**This is the open decision.** (b) means downtime by default and a manifest
field vendors must learn. Somebody has to decide whether a nightly backup that
stops the stack for ninety seconds is acceptable for this product's audience.
It is not a decision this RFC should make alone, and it is the reason the
status line says design-locked-with-one-open.

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

An escape hatch for the operator whose registry is not reachable: a
configuration key naming a different image, since any image with a POSIX `tar`
will do.

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
declares nothing gets its stack stopped during a backup. That is the safe
behaviour and it is the wrong behaviour for somebody running a nightly backup
of a busy service. It is the open decision for exactly this reason.

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
| 10 | **Open:** whether cold-by-default is acceptable for this product's audience, or whether the first milestone should ship (c) — complement-only — and defer stopping the stack. §5.2. |

## 11. Phasing

| Phase | What | Gated on |
| --- | --- | --- |
| **P1** | Enumerate volumes, capture cold, encrypt, record in the manifest, restore with the running-service refusal | Decision 10 |
| **P2** | The `backup.volumes` manifest declaration, `hot` and `exclude`, the vendor-facing docs | P1 |
| **P3** | The space check, the `doctor` growth warning, the helper-image checks | P1 |

**P1 is gated on decision 10 and should not start before it is made.** Building
cold-by-default and then discovering the audience will not accept the downtime
means rebuilding it as a complement-only feature, and the two have different
manifests, different components and different documentation.
