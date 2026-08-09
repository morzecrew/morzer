---
title: release
icon: lucide/package
summary: The release command group — list, show, new, verify, build, archive, fetch, prune
---

# `morzer release`

Inspects, manages and packs release bundles.

A release is identified by `(name, version)` **plus** its content digest. The
digest is computed over every file's path, executable bit and contents, sorted —
so the same version appearing with a different digest is an error, not a
warning. That is what makes a rollback target unambiguous.

## release list

Lists installed releases, newest first, marking the one currently pointed at.

The release store keeps more than the running release so that `rollback --to`
has somewhere to go. How many is the manifest's `retention.releases`.

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

It reports **every** violation in one pass rather than the first, because a
bundle author fixing one problem should not discover the next one on the retry.

`--signing-key` is how a vendor checks their own signing pipeline in CI: it
needs no installation on the machine, because there is no policy to read there.
On a deployment host, `policy.signing_keys` in `installation.yaml` decides
instead. See [Verification](#verification).

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
