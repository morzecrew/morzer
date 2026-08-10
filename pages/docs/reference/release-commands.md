---
title: release
icon: lucide/package
summary: The release command group — list, show, new, verify, pack, build, archive, fetch, prune
---

# `morzer release`

Inspects, manages and packs release bundles.

A release is identified by `(name, version)` **plus** its content digest. The
digest is computed over every file's path, executable bit and contents, sorted —
so the same version appearing with a different digest is an error, not a
warning. That is what makes a rollback target unambiguous.

## release list

Lists installed releases, newest first, marking the roles that keep a release in
the store: `*` current, `-` previous, `+` staged by a channel poll and not yet
installed.

The release store keeps more than the running release so that `rollback --to`
has somewhere to go. How many is the manifest's `retention.releases`, which
counts the releases holding *none* of those roles — the three marked ones are
kept regardless.

## release show

```sh
morzer release show [version]
```

Shows a release manifest — the installed one when no version is given.

## release new

```sh
morzer release new <dir> [--name NAME] [--vendor VENDOR]
```

Scaffolds a bundle skeleton that passes `release verify` with no edits.

| Flag | Meaning |
| --- | --- |
| `--name` | Product name. Defaults to the directory's own name, and is validated the same way `metadata.name` is — it becomes `/etc/<name>` on someone's machine. |
| `--vendor` | Who publishes this release. Left as a `TODO` when omitted. |

```text
my-product/
├── manifest.yaml         # schema modeline on line 1, version 0.0.0
├── VERSION               # 0.0.0, agreeing with the manifest
├── RELEASE.md            # declared as metadata.release_notes
├── secrets.schema.yaml
├── compose/compose.yaml
└── templates/app.yaml.tmpl
```

**A skeleton, not a product.** No image guessing, no Compose service inference,
no hook stubs. A generated bundle that pretended to know your architecture
would be work to un-write; one that verifies clean and deploys nothing useful
is honest. Every value that must change before you publish is marked `TODO`.

The version is `0.0.0` deliberately — a placeholder
[`release build`](#release-build) stamps. A scaffold that wrote `1.0.0` would
be inviting you to hand-maintain the one field the tooling owns, and `build`
refuses `0.0.0` precisely so a forgotten `--version` cannot ship it.

It refuses to write over anything. Scaffold into an empty directory.

## release verify

```sh
morzer release verify <path>
```

Validates a bundle's manifest, **parses every template it declares**, and
checks its integrity, without installing anything. This is the command a bundle
vendor runs in their own CI.

The template pass is parsing, not rendering. A template that parses can still
fail against a real context — the render context has values only an
installation can supply — so a clean `verify` means the templates are
syntactically sound, not that they will produce what you expect. Parsing needs
no installation, no parameters and no network, which is what makes it safe to
run on every commit.

It matters because the alternative is worse: without it, a template with an
unterminated action passes `verify` and fails during an operator's `apply`,
part-way through a journaled operation, on a machine belonging to someone who
did not write it.

| Flag | Meaning |
| --- | --- |
| `--digest` | Expected content digest. A mismatch is an error. |
| `--signing-key` | minisign public key the bundle's signature must verify against. Repeat for several. |
| `--render-check` | Also render each template against a synthetic context. A smoke test — see below. |

It reports **every** violation in one pass rather than the first, because a
bundle author fixing one problem should not discover the next one on the retry.

### What `--render-check` checks, and what it does not

```sh
morzer release verify ./my-product --render-check
```

It builds a plausible render context — an installation id, a domain, the
production path layout, your declared parameters resolved, your declared
secrets — and renders every template against it.

**The values are invented.** A template that branches on them
(`{{- if .Domains }}`) exercises only the branch they choose, so a clean render
check does not mean the template will render on a customer's machine. That is
structural rather than a maturity gap: there is no real context here, which is
exactly what lets you run this in CI. It will never become the default, and
there will never be a `--no-render-check`.

What it does catch is the class parsing cannot: a reference to a field the
render context does not define, and a `secretFile` call naming a secret your
schema does not declare. Those fail on every machine, so catching them on yours
is free.

Two things are *not* invented, and they are why the check has teeth: secret
names come from the schema your manifest declares, and parameters from your own
`parameters:` block. A typo in either fails here.

A parameter you declared without a default gets an invented value that satisfies
its type, rather than the empty string an unset parameter really resolves to.
Otherwise `{{ required "choose a port" .Parameters.http_port }}` would fail the
check on a bundle that is entirely correct — the operator simply has not chosen
yet.

`--signing-key` is how a vendor checks their own signing pipeline in CI: it
needs no installation on the machine, because there is no policy to read there.
On a deployment host, `policy.signing_keys` in `installation.yaml` decides
instead. See [Verification](#verification).

## release pack

```sh
morzer release pack <bundle-dir> [--platform linux/amd64] [--force]
```

Copies every image the manifest marks `from: bundle` out of its registry into
an OCI layout under `<bundle-dir>/images/`, then regenerates `SHA256SUMS`.

| Flag | Meaning |
| --- | --- |
| `--platform` | Which platform to select from a multi-platform image, e.g. `linux/amd64`. Defaults to whatever the registry resolves. |
| `--force` | Discard an existing `SHA256SUMS.minisig`, which repacking invalidates. |

Credentials come from the ambient Docker configuration, exactly as a
`docker pull` on this machine would — so this runs on your build machine, and
your customers never need them. That is the whole point:
[bundled images](../authoring/bundled-images.md) exist so a customer can install
a release containing your private images without holding a credential for the
registry they came from.

**Idempotent.** The layout is content-addressed, so running it twice copies
nothing the second time, and re-running after adding an image adds only that
image's blobs.

### What it refuses

| Situation | Behaviour |
| --- | --- |
| No image is marked `from: bundle` | Refused. There is nothing to pack, and silently succeeding would suggest there was. |
| An image cannot be resolved or copied | Refused, and **`SHA256SUMS` is not regenerated** — so the half-populated tree fails `release verify` until a later pack completes. |
| The registry serves a different digest than the manifest pins | Refused, naming both. This is the check that makes a bundle's provenance mean anything. |
| An existing `SHA256SUMS.minisig` | Refused unless `--force`, which **deletes** it. A signature over a list that packing is about to invalidate is the exact failure the chain exists to prevent. |

On a partial write: `pack` writes in place, so a failure on the fourth image
leaves the first three copied. Blobs are content-addressed, so what that leaves
is *orphans* rather than corruption — and because the checksum list is
regenerated only after every image succeeds, the tree fails `release verify`
until a later pack finishes. Staging the whole layout to make a stronger promise
would double the disk for a multi-gigabyte bundle.

### Platform selection

A registry reference may resolve to a multi-platform index. `--platform` picks
one; the default is whatever the registry resolves for this machine, which is
least surprising for a vendor building on the architecture they ship and wrong
for one cross-building on arm64 for amd64 customers.

If you pin a multi-platform *index* digest and then select a platform, the copy
resolves to a per-platform manifest whose digest is not the one you pinned, and
`pack` refuses rather than quietly bundling an image the manifest does not
describe. Pin the digest you actually want to ship.

## release build

```sh
morzer release build <bundle-dir> [--force]
```

Writes `SHA256SUMS` over every file in the bundle, then verifies the result the
same way `verify` does.

| Flag | Meaning |
| --- | --- |
| `--version` | Version to stamp into `manifest.yaml` **and** `VERSION`. |
| `--version-from-git` | Derive it from `git describe`: `<next-patch>-dev.<distance>.g<sha>`. Mutually exclusive with `--version`. |
| `--allow-dirty` | With `--version-from-git`, permit a work tree with uncommitted changes; the version gains a `.dirty` identifier. |
| `--force` | Discard an existing `SHA256SUMS.minisig`, which regenerating the list invalidates. |

With neither version flag, the manifest's own version is used and nothing is
stamped — except `0.0.0`, which is refused. See
[Versioning](../authoring/publishing.md#versioning) for the scheme and why the
patch is bumped.

Stamping edits the manifest's `version:` line in place rather than re-encoding
the document, so comments and key order survive, and the result is re-parsed
before the command returns. A manifest whose `metadata.version` cannot be
located unambiguously — a flow-style mapping, say — is refused rather than
guessed at.

Writes **in place** — the bundle directory is modified. Copying a tree whose
`images/` layout may be tens of gigabytes to protect a checksum file is a poor
trade, so there is no `--out`.

The manifest is validated *before* anything is written. A checksum list over a
broken tree is evidence that the tree is exactly as broken as it is, signed and
shipped.

A bundle that already carries a signature is refused: regenerating the list
necessarily invalidates any signature over it. `--force` **deletes** the
signature rather than building around it — keeping one that no longer verifies
produces a bundle that fails on the customer's machine for a reason the vendor
cannot see from their own tree.

The list is written in the archive's entry order, and the checksums are bare
hex, so `sha256sum -c SHA256SUMS` reads it without this tool.

## release archive

```sh
morzer release archive <bundle-dir> [-o FILE]
```

Packs a bundle directory into the `.tar.zst` every transport reads, after
checking it the same way `verify` does. Writes
`<name>-<version>.tar.zst` beside the bundle unless `-o` says otherwise.

| Flag | Meaning |
| --- | --- |
| `--output`, `-o` | Where to write the archive. Defaults to `<name>-<version>.tar.zst` beside the bundle directory. It must be outside the bundle. |

This is the last of the three steps that publish a release:

```sh
morzer release build   ./my-product
minisign -Sm ./my-product/SHA256SUMS
morzer release archive ./my-product
```

Signing sits in the middle, and that is why these are separate commands rather
than one. The signature is a file *inside* the bundle — a sibling file would not
survive being packed and unpacked — so it has to exist before the bundle is
packed. And morzer does not sign: a manager that signs is a manager that holds
signing keys.

### What it refuses

| Situation | Behaviour |
| --- | --- |
| No `SHA256SUMS` | Refused. Archiving an unsummed tree produces a bundle the completeness rule cannot be applied to. |
| `SHA256SUMS` does not match the tree | Refused, by the same check `verify` runs. |
| `SHA256SUMS.minisig` older than `SHA256SUMS` | Refused. A signature over a tree that has since changed is the exact failure the chain exists to prevent. |
| No signature at all | **Warned, not refused.** Whether a signature is required is the operator's policy (`policy.require_signature`), and an unsigned bundle is legitimate for a vendor whose customers do not require one. |
| `-o` points inside the bundle | Refused. The archive would be a file the bundle contains and its own `SHA256SUMS` does not list. |

### Entry order is part of the format

The archive is written in a fixed order, and it is a property of a morzer
release archive rather than an implementation detail:

| # | Entry | Why here |
| --- | --- | --- |
| 1 | `manifest.yaml` | A reader sizes its extraction budget from the manifest before extracting anything else, which works only while it arrives first. |
| 2 | `VERSION` | Tiny, and lets a reader confirm identity from the first two entries. |
| 3 | `SHA256SUMS`, `SHA256SUMS.minisig` | The integrity evidence, available before the bytes it covers. |
| 4 | Everything else, `images/` last | The large content, extracted under a budget that is by now known. |

Within each of those four ranks, entries are sorted by their relative path.
The ranks exist for the budget read; the sort exists for reproducibility, and
neither substitutes for the other — directory traversal order is not specified
by any filesystem, so without the sort two archives of one tree could differ in
entry order alone.

If you roll your own `tar`, put `manifest.yaml` first.

### Reproducibility

Two archives of the same tree are byte-identical. Ownership is normalised to
uid/gid 0 with no owner names, the mode is reduced to the executable bit —
the only permission a release depends on and the only one the content digest
records — and every entry carries one timestamp, resolved in this order:

1. `SOURCE_DATE_EPOCH`, when set. The reproducible-builds convention wins where
   it is expressed. A value that is not a Unix timestamp is refused rather than
   ignored: a pipeline that sets the variable is asking for a specific time, and
   silently substituting a different one produces an archive that is
   reproducible by accident.
2. The commit date of the repository the bundle sits in, when there is one.
   `build` and `archive` are separate commands and nothing in a bundle records
   how its version was resolved, so this is the fact `archive` can actually
   observe — and it gives the same answer for the case the step exists for.
3. The epoch — a timestamp that is obviously not a build time, rather than one
   that looks like a real date and is not.

None of this changes the bundle's identity. The content digest covers contents,
paths and the executable bit, not timestamps, so normalising them changes the
archive's bytes and not the release's digest.

## release fetch

```sh
morzer release fetch <ref>
morzer release fetch ./demo-1.3.0.tar.zst
```

Fetches a release bundle into the release store, verifies it, and leaves it
inactive. `update --to <version>` installs it afterwards.

## Where bundles come from

A reference's scheme selects the source that handles it. This build supports:

| Reference | Meaning |
| --- | --- |
| `./bundle`, `/opt/x/releases/1.2.0` | An unpacked bundle directory. |
| `./demo-1.3.0.tar.zst`, `.tzst` | A zstd-compressed tar archive. |
| `file:///abs/path` | Either of the above, spelled explicitly. |
| `https://host/demo-1.3.0.tar.zst` | An archive published over TLS. |
| `oci://registry.example/demo/bundle:1.3.0` | An OCI artifact in a registry. |

Plaintext `http://` is refused outright — fetch the bundle out of band and pass
a path. A reference whose scheme this build has no adapter for is refused by
name, listing the ones it does have.

### Over HTTPS

TLS is not optional, and **a redirect may not leave it**. A server that answered
an `https://` request with a redirect to plaintext would route around the
refusal above at exactly the moment it matters, so the redirect is refused and
the chain is bounded.

A response body is capped while it is being read rather than trusted to match
its `Content-Length` — that header is a claim by the same server sending the
body. A 5xx or 429 is retried a few times, because a mirror restarting during a
deploy is ordinary; a 404 or a 401 is not, because repeating a request the
server answered definitively only delays the same answer.

An untrusted certificate is named as one rather than retried:

```text
error: the TLS certificate for https://releases.internal/demo.tar.zst is not
       trusted by this machine
hint:  install the issuing CA, or fetch the bundle out of band and install it
       from a path. There is no option to skip verification.
```

**There is no flag to skip verification, and there will not be.** A bundle
fetched over a connection nothing authenticated is a bundle from nobody in
particular, and a `--insecure` that existed would be reached for at exactly the
moment it should not be.

No credentials are sent. A bundle behind authentication is fetched out of band,
or published to a registry.

### From an OCI registry

```sh
morzer release fetch oci://registry.example/demo/bundle:1.3.0
morzer release fetch oci://registry.example/demo/bundle@sha256:…
```

The bundle is one layer of an OCI artifact, published with the media type
`application/vnd.morzer.release.bundle.v1.tar+zstd`. An artifact with a single
layer is taken whatever its type; with several, the media type is what selects
the bundle rather than a guess at ordering.

**A reference with no tag or digest is refused.** A bare repository resolves to
whatever `latest` points at today, which is the thing content-addressed identity
exists to prevent.

Credentials come from the ambient Docker configuration, so an operator who has
run `docker login` for the registry their *images* come from does not log in
again for the bundle that pins those images. Nothing is stored by the manager.

`release list` against an `oci://` reference enumerates the repository's version
tags. It is the only transport that can — a URL has no index — and tags that are
not versions, like `latest`, are skipped rather than offered as something to
install by number.

!!! note "What the registry proves, and what it does not"

    Both the manifest and the layer are checked against the digests the registry
    itself advertises, so a registry serving different bytes than it named is
    refused. That is a weaker claim than it sounds: it says the registry was
    self-consistent, not that the bundle is the vendor's. The bundle's own
    content digest and its signature are what answer that.

**The shape a bundle arrives in does not change its identity.** A directory and
its archive produce the same content digest, so a digest recorded from one
verifies the other. That is asserted by a test comparing every source against
the same bundle, because pinning a release would otherwise mean pinning a
transport too.

### The extraction budget

Extraction happens **before** the signature is checked — a bundle is written to
disk, then verified — so the extraction limits are the only thing standing
between a hostile archive and the disk, and the signature cannot be the
mitigation because it comes afterwards.

The default ceiling is 2 GiB in total and 1 GiB per file, which a bundle
carrying container images cannot live within. Such a bundle raises its own by
declaring what it expands to:

```yaml title="manifest.yaml"
bundle:
  uncompressed_size: 12GiB
```

Three rules, and each is the answer to a way of getting this wrong:

- **The archive must begin with `manifest.yaml`.** That is where the
  declaration lives, and it has to be readable before the bytes it bounds.
  `release archive` guarantees the order; an archive that does not honour it is
  **refused**, not quietly given the default ceiling — a fallback would make the
  ordering advisory, and an advisory guarantee decays.
- **A declaration may only ever lower the ceiling.** The effective limit is
  `min(declared, hard cap)`, where the hard caps are **50 GiB total and 5 GiB
  per file**. The declaration is made by the same unverified bytes the guard
  bounds, so a budget that could raise the ceiling is one an attacker sets. A
  bundle needing more than the cap is refused; raising the cap is a change to
  morzer, made once, in the open.
- **Absent means the default, not unbounded.** A missing field must never be the
  permissive reading of anything that gates untrusted bytes.

A declaration smaller than the default is honoured as the stricter bound you
asked for: an archive that exceeds its own declared size is refused. And before
anything is written, the declared ceiling is checked against free disk space, so
a bundle too large for the machine is a clean refusal rather than a full
filesystem.

### What extraction refuses

An archive is the largest attack surface here: a format chosen by whoever
produced the bundle, unpacked onto the host as root before anything in it has
been verified as safe to run. Refused, always:

- **Entries that escape the destination** — `../`, absolute paths. Extraction
  goes through a syscall-level root, so this fails at the kernel rather than at
  a string check somebody has to get right.
- **Symlinks and hardlinks.** The classic escape is a link out of the
  destination followed by a write through it.
- **Device nodes, FIFOs, sockets** — anything that is not a regular file or a
  directory.
- **More entries, a larger file, or a larger total than the limits allow**,
  enforced *during* extraction. A decompression bomb has to be refused while it
  is being written; noticing once the disk is full is noticing too late.

Permissions are normalised to `0755` or `0644`. The executable bit survives
because a hook must be able to run and the digest records it; nothing else about
a vendor's umask is carried onto your machine.

## Verification

Two independent checks, and a bundle failing either is refused.

| Check | Question it answers |
| --- | --- |
| **Checksum** | Is this the artifact that was pinned, and does every file match — and appear in — the `SHA256SUMS` the bundle ships? |
| **Signature** | Did a key this installation trusts sign that `SHA256SUMS`? |

They compose rather than replace one another, and neither is sufficient alone: a
signature covers `SHA256SUMS`, so on its own it cannot see a hook edited without
its checksum entry being edited too. Together, the chain runs
signature → `SHA256SUMS` → every file, and every link is checkable by hand with
`minisign -Vm` and `sha256sum -c`.

A signed bundle contains:

```text
release/
├── SHA256SUMS           # one line per file
├── SHA256SUMS.minisig   # detached minisign signature over it
└── ...
```

The signature travels **inside** the bundle so it survives being packed into an
archive and unpacked again. A sibling file would not.

Configure the keys per installation:

```yaml
policy:
  require_signature: true
  signing_keys:
    - RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3
```

or at `init`, with `--signing-key` and `--require-signature`. Keys live with the
installation and never with the release: a bundle naming the key allowed to sign
it would be a bundle authorising itself.

!!! warning "Signing proves provenance, not safety"

    A verified signature says the holder of that key published this bundle. It
    says nothing about whether the bundle is safe to run — hooks execute as root
    on your host either way. Signature verification narrows *who* can hand you a
    release; it does not narrow what a release can do.

minisign rather than cosign: cosign as a library assumes a transparency log and
an OIDC identity a self-hosted vendor may not have, while a minisign key is one
base64 line an operator can paste into a config. The manager only ever verifies
— there is no `morzer sign`, because the signing key belongs in a vendor's
release pipeline rather than on a deployment host.

## release ingest

```sh
morzer release ingest
```

Loads the images the installed release carries into the local image store. No
arguments: it acts on the release that is current, because that is the one a
deployment is about to run.

Nothing here touches the network. The manager serves the release's
`images/` layout to the container runtime over `127.0.0.1`, read-only, for the
length of the command, and the runtime pulls each image out of it — a real
registry pull, so the runtime verifies every blob it fetches against the digest
the manifest names.

**You rarely run this.** `init` and `update` run the same step, so a release
installed the ordinary way arrives with its images already loaded. It exists for
the case where they are not: a machine whose image store was pruned, a runtime
that was reinstalled, a converge that refuses with

```text
images the bundle carries are loaded
  1 of 1 bundled image(s) are not in the local image store: registry.example/demo/app
  → run `morzer release ingest` to load them out of the bundle
```

**Idempotent.** An image already present is not read again, so re-running after
a partial failure costs nothing for the images that succeeded.

### What it refuses

| Situation | Behaviour |
| --- | --- |
| The release bundles no images | Not an error. It says so and does nothing. |
| A blob whose bytes are not what the layout's index claims | Refused, naming the blob and the digest it actually hashes to. The runtime is what rejects the bytes; the manager is what tells you which ones. |
| The runtime has no local image store | Refused. A release marking images `from: bundle` will not find them anywhere else. |
| The runtime cannot reach this machine's loopback | Refused. A daemon on another host — a remote `DOCKER_HOST` — cannot pull from a server bound to this one, and there is no fallback behind it. |

### What it leaves behind

An image that travelled in the bundle ends up named
`<repository>:morzer-sha256-<digest>` rather than the `name@sha256:…` the
manifest pins:

```text
$ docker images
REPOSITORY                  TAG                          IMAGE ID
registry.example/demo/app   morzer-sha256-0000000000…    a1b2c3d4e5f6
```

That tag is the manager's, and it is not a cosmetic choice. A container runtime
records the repository it *pulled* an image from, and nothing can add a digest
reference for a repository it never contacted — `docker tag` refuses outright:
`refusing to create a tag with a digest reference`. So a bundled image cannot
answer to the reference its manifest pins, and Compose has to be given a name
that resolves.

The digest stays the identity. It is what the signature covers, what
`release show` reports, and what the tag is derived from — which is also why the
tag is the same on every machine and every run.

Because the tag is derived rather than invented, `apply` **refuses** rather than
pulls when a bundled image is missing. A tag is mutable; if a registry happened
to serve that name, a missing image would let a digest-pinned deployment
converge on bytes nobody verified.

## release prune

Removes old releases beyond the retention policy. The running release and the
one before it are never pruned.

| Flag | Meaning |
| --- | --- |
| `--keep` | Number of non-active releases to retain. `0` uses the manifest's policy. |

## What the loader enforces

Rules a bundle must satisfy before any of it runs:

- **Unknown manifest fields are an error.** A typo must not silently fall back
  to a default. Vendor keys live under `extensions.<namespace>`.
- **Images are pinned by digest.** A bare tag is rejected: an unpinned image
  makes a release mutable, and a mutable release makes rollback meaningless.
- **Paths are release-relative** and may not escape the root.
- **A declared-but-missing hook is a validation error**, and so is a hook
  without the executable bit. Both are knowable before the lock is taken.
- **Errors report line and column** from the source YAML.

See [Manifest](manifest.md) for the schema itself, and [Hook ABI](hooks.md) for
what the hooks are handed.
