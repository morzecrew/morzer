---
title: Signing and publishing
icon: lucide/upload
summary: Building, signing and packing a bundle, and where to put it
---

# Signing and publishing

A bundle is a directory. Publishing it means summing it, signing it, packing it,
and putting it somewhere your users can reach.

Three commands, in this order, and the middle one is not morzer's:

```sh
morzer release build   ./my-product
minisign -Sm ./my-product/SHA256SUMS
morzer release archive ./my-product
```

The order is forced rather than chosen. The signature is a file *inside* the
bundle — a sibling would not survive being packed and unpacked — so it has to
exist before the bundle is packed. And morzer does not sign, so the step in the
middle is yours.

## Build it

```sh
morzer release build ./my-product
```

Writes `SHA256SUMS` over every file in the bundle, then verifies the result the
same way `release verify` does, so a broken bundle fails on your machine rather
than on a customer's.

It writes **in place**: the bundle directory is modified. Your CI checks out
fresh so it never notices; running it in a working tree leaves a file to commit
or ignore.

A bundle that already carries a signature is refused, because regenerating the
list necessarily invalidates any signature over it. `--force` discards the
signature rather than building around it — keeping one that no longer verifies
produces exactly the artifact the chain exists to prevent.

## Sign it

The signature covers the `SHA256SUMS` that lists every other file. Two small
steps, each checkable by hand with standard tools, rather than one bespoke
signature only this program can verify.

```sh
minisign -Sm ./my-product/SHA256SUMS
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

The list has to be complete. A bundle that ships a `SHA256SUMS` naming only some
of its files is refused, naming the ones it left out — an unlisted file is a
file the signature does not cover, and the file an attacker adds is a file
nobody listed. `release build` produces a complete list by construction.

## Pack it

```sh
morzer release archive ./my-product
# wrote demo-1.3.0.tar.zst
```

`tar.zst` is the format the manager reads. A bundle and its archive produce the
**same content digest**, so a digest you record from the directory verifies the
archive and vice versa — publishing does not change what a release *is*.

Two archives of the same tree are byte-identical, and `SOURCE_DATE_EPOCH` is
honoured. See [entry order and reproducibility](../reference/release-commands.md#release-archive)
for what that costs and what it buys.

### If you roll your own `tar`

Supported, and sometimes necessary — a pipeline that cannot run morzer still
has to produce a bundle. Two rules it must keep, both of which the commands
above keep for you:

```sh
cd ./my-product
find . -type f ! -name SHA256SUMS ! -name SHA256SUMS.minisig \
    -exec sha256sum {} + | sed 's| \./| |' > SHA256SUMS
minisign -Sm SHA256SUMS

{ echo manifest.yaml
  find . -type f ! -path ./manifest.yaml | sed 's|^\./||' | LC_ALL=C sort
} > ../entries.txt
tar --zstd -cf ../demo-1.3.0.tar.zst --no-recursion -T ../entries.txt
```

**`manifest.yaml` must be the first entry** if your bundle carries images: the
extraction budget is read from the manifest before anything large is extracted,
which only works while it arrives first. `tar -C ./my-product .` emits entries
in directory order, which no `tar` implementation specifies.

**The checksum list must be complete.** If your pipeline writes one by hand,
verify it with `sha256sum -c SHA256SUMS` *and* by comparing its line count
against the same file set the list covers:

```sh
find . -type f ! -name SHA256SUMS ! -name SHA256SUMS.minisig | wc -l
wc -l < SHA256SUMS
```

Nothing but regular files and directories, either way. Symlinks, hardlinks and
device nodes are refused at extraction, so an archive containing one will not
install.

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
