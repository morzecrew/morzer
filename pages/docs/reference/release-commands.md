---
title: release
icon: lucide/package
summary: The release command group — list, show, verify, fetch, prune
---

# `morzer release`

Inspects and manages release bundles.

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

## release verify

```sh
morzer release verify <path>
```

Validates a bundle's manifest and checks its integrity, without installing
anything. This is the command a bundle vendor runs in their own CI.

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

`https://` and `oci://` parse but have no adapter in this build; the refusal
names what is available. Plaintext `http://` is refused outright — fetch the
bundle out of band and pass a path.

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
| **Checksum** | Is this the artifact that was pinned, and does every file match the `SHA256SUMS` the bundle ships? |
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
