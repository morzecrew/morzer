---
title: Signing and publishing
icon: lucide/upload
summary: Packing a bundle, signing it so operators can verify who published it, and where to put it
---

# Signing and publishing

A bundle is a directory. Publishing it means packing it, signing it, and putting
it somewhere your users can reach.

## Pack it

```sh
tar --zstd -cf demo-1.3.0.tar.zst -C ./my-product .
```

`tar.zst` is the format the manager reads. A bundle and its archive produce the
**same content digest**, so a digest you record from the directory verifies the
archive and vice versa — publishing does not change what a release *is*.

Nothing but regular files and directories. Symlinks, hardlinks and device nodes
are refused at extraction, so an archive containing one will not install.

## Sign it

The signature covers a `SHA256SUMS` file that lists every other file. Two small
steps, each checkable by hand with standard tools, rather than one bespoke
signature only this program can verify.

```sh
cd ./my-product
find . -type f ! -name SHA256SUMS ! -name SHA256SUMS.minisig \
    -exec sha256sum {} + | sed 's| \./| |' > SHA256SUMS
minisign -Sm SHA256SUMS
```

That leaves both files **inside** the bundle:

```text
my-product/
├── SHA256SUMS
├── SHA256SUMS.minisig
└── …
```

Inside rather than beside, so the signature survives being packed into an
archive and unpacked again on the other machine.

### The key

```sh
minisign -G -p morzer-bundles.pub -s morzer-bundles.key
```

The private half belongs in your release pipeline and nowhere else. There is no
`morzer sign` command on purpose: building signing into the manager would invite
the signing key onto a deployment host, which is the one machine it should never
be on.

Publish the public half where your users will look for it. They put it in their
installation:

```yaml title="installation.yaml"
policy:
  require_signature: true
  signing_keys:
    - RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3
```

!!! warning "Say what a signature means, and what it does not"

    It proves a bundle came from the holder of your key. It does not prove the
    bundle is safe to run — hooks execute as root on your users' machines either
    way. Signing narrows *who* can hand them a release; it does not narrow what
    a release can do. Do not let your documentation imply otherwise.

## Check it before you ship it

```sh
morzer release verify ./my-product --signing-key RWQf6LR…
```

Validates the manifest, checks every referenced file exists, verifies the
`SHA256SUMS` against the contents, and checks the signature. It needs no
installation on the machine, so it belongs in your CI as a required check.

```yaml title=".github/workflows/release.yaml"
- run: morzer release verify ./bundle --signing-key ${{ vars.SIGNING_KEY }}
```

## Publish it

| Where | Reference your users pass |
| --- | --- |
| Any HTTPS host | `https://releases.example/demo-1.3.0.tar.zst` |
| An OCI registry | `oci://registry.example/demo/bundle:1.3.0` |
| A file they copy | `./demo-1.3.0.tar.zst` |

**HTTPS** is a static file on any web server. TLS is not optional on the client
side and a redirect out of it is refused, so a plain-HTTP mirror will not work.
No credentials are sent, so a bundle behind authentication needs the registry
route or an out-of-band download.

**An OCI registry** is the one that lets `morzer release list` enumerate your
versions, because a registry keeps a tag list and a URL does not. Publish the
archive as a single-layer artifact:

```sh
oras push registry.example/demo/bundle:1.3.0 \
    --artifact-type application/vnd.morzer.release.bundle.v1 \
    demo-1.3.0.tar.zst:application/vnd.morzer.release.bundle.v1.tar+zstd
```

Credentials come from your users' ambient Docker configuration, so anyone who
has run `docker login` for the registry your *images* come from can already
fetch the bundle that pins them.

## Tell them the digest

```sh
morzer release verify ./my-product | tail -1
# sha256:bcca96e8020143562b3040e9c443f36ae57d8e2594c59b7f2499902416b211c5
```

Put it in your release notes. It is what turns

```sh
morzer update https://releases.example/demo-1.3.0.tar.zst
```

into

```sh
morzer update https://releases.example/demo-1.3.0.tar.zst --digest sha256:bcca96e8…
```

— the difference between "a release claiming to be 1.3.0" and "the release you
published as 1.3.0".

## Versioning

The version in your manifest is semantic and the manager compares it as such.
Two things follow that are easy to get wrong:

- **Never republish a version with different content.** The manager refuses a
  version already installed with a different digest, rather than overwriting it.
  That refusal is a feature, and it will land on your users.
- **Bump `database_schema_max` when a release can read a newer schema**, and set
  `rollback_safe: false` when its migrations are one-way. These are what let the
  manager refuse an unsafe rollback instead of corrupting a database quietly.
