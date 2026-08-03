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

It reports **every** violation in one pass rather than the first, because a
bundle author fixing one problem should not discover the next one on the retry.

## release fetch

```sh
morzer release fetch <ref>
```

Fetches a release bundle into the release store.

!!! note "Local directories only, for now"

    `selfhost/v1alpha1` supports local directory references. `https` and `oci`
    sources, archive extraction and signature verification are designed in
    [RFC 0004](https://github.com/morzecrew/morzer/blob/main/rfcs/0004-distribution-and-verification.md)
    and not yet built.

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
