# Security policy

## Reporting a vulnerability

Please report privately through
[GitHub's security advisories](https://github.com/morzecrew/morzer/security/advisories/new)
rather than opening a public issue.

Include what you did, what happened, and what you expected. A proof of concept
helps, but a clear description is enough — do not delay a report to build one.

You should get an acknowledgement within a few days. This is a small project;
there is no paid response team and no bounty.

## Supported versions

**The latest 0.x release, and nothing else.** There is no backport branch: a fix
ships in the next release and an operator on an older one upgrades to it.

While the major version is 0 there is no long-term support to offer and saying
otherwise would be a promise nobody here can keep. What *is* promised is that
upgrading is a supported path rather than a reinstall — an older installation's
state is migrated forward, and a manager refuses state written by a newer one
rather than misreading it.

| Version | Supported |
| --- | --- |
| 0.2.x | ✅ |
| 0.1.x | ❌ — upgrade to 0.2.x |
| `main` | Fixes land here first; it is not a release |

## Verifying a release

Released binaries ship a `SHA256SUMS` signed with minisign. The public key is
[`morzer.pub`](morzer.pub) in this repository:

```sh
minisign -Vm SHA256SUMS -p morzer.pub
sha256sum -c SHA256SUMS
```

**Keep your own copy of the key.** Published here, it is only as trustworthy as
this repository: anyone who could replace a release artifact could replace the
key beside it. What it does give you is continuity — a release signed by a
different key than the one you verified last time is worth stopping over. The
key does not change between releases.

This is a different key from the one an installation configures in
`policy.signing_keys`. That one belongs to whoever publishes the release bundles
you deploy, and is theirs to rotate.

## What is in the threat model

morzer is a deployment tool that runs as root, holds an installation's secrets,
and executes hooks that ship inside release bundles. The threats it is built to
resist:

- **A tampered release bundle.** Bundles are identified by a content digest over
  every file's path, contents and executable bit. `update` refuses a digest that
  does not match what was pinned, and a version already installed with different
  content is refused rather than overwritten.
- **Secret leakage.** Values never reach process arguments, logs, the operation
  journal, or machine-readable output. Rendered secrets are `0400` inside a
  `0700` directory on tmpfs, and generated configuration holds paths to secrets
  rather than the values.
- **Two operations racing.** Every mutating command takes an advisory lock
  recording who holds it.
- **A partially applied change.** Operations are step sequences that journal
  before and after each step and compensate on failure. What cannot be undone
  automatically stops and says so rather than guessing a repair.
- **Path traversal from a bundle.** Extraction and configuration rendering are
  confined by `os.Root`, so containment is enforced by the kernel rather than by
  string inspection.
- **A hostile archive.** `tar.zst` extraction refuses entries that escape the
  destination, symlinks and hardlinks, device nodes, FIFOs and sockets, and
  anything exceeding the entry, per-file or total size limits — enforced *during*
  extraction, so a decompression bomb is refused while it is being written rather
  than once the disk is full. Permissions are normalised to `0755` or `0644`, so
  a bundle cannot ship a world-writable file. Each of these is covered by a
  fixture built in the test that asserts its refusal.
- **A bundle arriving over a network.** TLS is not optional and a redirect may
  not leave it, so a server cannot downgrade a fetch to plaintext by asking.
  Bodies and registry layers are bounded while they are read rather than trusted
  to match a declared length, and registry content is checked against the digests
  the registry advertises for it.
- **A bundle from someone other than your vendor.** A detached minisign
  signature over the bundle's `SHA256SUMS` is verified against keys the
  *installation* configures, never keys the bundle names.
  `policy.require_signature` makes one mandatory, and the two checks compose:
  signature → `SHA256SUMS` → every file.

## The machine's signing key

Each installation mints an Ed25519 key at `init`, in minisign format, at
`/etc/<product>/signing/identity.key` (`0400`, in a `0700` directory). It signs
statements the machine makes *about itself* — starting with the attestation
written after each `apply`. The public half is recorded in installation state,
so `status --json` and an export both carry it.

This does not reverse the rule that keeps release signing off deployment hosts
([RFC 0004](rfcs/0004-distribution-and-verification.md) decision 8). That rule
protects a *vendor's* key, whose signatures every customer trusts. This one
speaks for one installation, to whoever reads that installation's artifacts, and
a host holding a key that can impersonate only itself has given nothing away.

**What a signature by it proves**, carried verbatim in every document it signs:

> This signature proves that a process holding this installation's signing key
> produced these bytes. It does not prove the bytes are true, it does not prove
> the machine was uncompromised when it signed, and it does not identify the
> operator.

There is no passphrase, deliberately: the key is used unattended by a timer, and
a passphrase for an unattended signer is a passphrase stored beside the key.

**Losing it is not recoverable, and that is an accepted cost.** Old signatures
stay verifiable against the recorded public key; only the ability to make new
ones is lost. That is a small enough loss that a recovery path — one more secret
to protect, travelling in an export — is the worse trade. The age identity is
the opposite case, and is why it gets ceremony this key does not.

**A rebuilt machine is a new signer.** An export never carries a private key, so
`installation import` mints a fresh one and records the predecessor's public key
under `signing.previous_keys`. A signature checking out against one of those is
*provenance* — "signed by a predecessor of this installation" — and never plain
validity. Collapsing the two would make rotation useless.

### Rotation protects the future and repairs nothing

The sentence to read before rotating after a suspected compromise:

**Rotating does not un-sign anything.** Every artifact bearing the old key stays
suspect, including the ones that look old, because whoever holds a stolen key
can back-date a statement as easily as write a current one. `retired_at` is
recorded for an operator reading their own timeline; it is **not** a check, and
no verifier may reject a signature for being dated after it — the date comes
from the artifact, and the artifact is what the forger writes.

An operator who still vouches for particular artifacts re-signs those. The rest
stay suspect. Closing this properly needs a chronology an attacker cannot write
— a countersignature from off the machine, or a transparency log — and neither
is built. See [RFC 0028](rfcs/0028-the-machines-signing-identity.md).

## What is not

- **A malicious root user on the same machine.** Anyone with root can read the
  age identity and every rendered secret. Nothing here defends against that.
- **Attacks against the Docker daemon itself.**
- **A malicious release bundle from a trusted vendor.** Hooks run as root by
  design — that is what a hook *is*. Signature verification proves a bundle came
  from the holder of a key. It does not prove the bundle is safe to run, and it
  is not intended to. Signing narrows *who* can hand you a release; it does not
  narrow what a release can do.
- **The secrecy of a recovery key stored on the machine it protects.** Move it
  elsewhere; `init` says so when it generates one.

## Known gaps

Stated here rather than discovered during an incident:

- Nothing checks a signature's *freshness*. A correctly signed older release can
  be presented in place of a newer one; only `compatibility.upgrade_from` and a
  pinned `--digest` stand in the way. See
  [RFC 0004](rfcs/0004-distribution-and-verification.md).
- A signature is verified *after* the archive is extracted, because it travels
  inside the bundle. Extraction is confined and bounded, so what an unsigned
  hostile archive can do is refuse to extract — but the ordering is worth
  knowing: the extractor runs on bytes nothing has authenticated yet.
- The manager sends no credentials over HTTPS. A bundle behind authentication
  has to be fetched out of band, or published to an OCI registry, where the
  ambient Docker credentials are used.
- `secret edit` and the `doctor` check for a `/run` that is not tmpfs are not
  implemented. See [RFC 0003](rfcs/0003-secrets-recovery-and-onboarding.md).
- An attestation records what the manager did; it does not record that a
  signature was *verified during that operation*, because signatures are checked
  when a release is staged and `apply` runs against one already on disk. The
  field is absent rather than `false` in that case, so an auditor cannot read
  "not established" as "checked and failed".
- An attestation leaves the machine as it is written, to the same targets the
  backups go to, and a push that fails does not fail the operation. So a machine
  lost between writing a statement and pushing it takes that statement with it;
  `morzer doctor` reports statements that are still only local, and `morzer
  attest push` sends them.
- A support bundle's redaction count is a smaller claim than it looks. Zero
  replacements in a file means no value this installation *currently* holds
  appeared in it — not that the file is clean. A secret that was rotated away,
  or one that was never declared to the manager, is not something it can
  recognise.
- A fleet row cannot be authenticated without a roster. Rows from several
  machines share one prefix, so a machine that overwrites its neighbour's row
  rewrites the payload, the embedded key and the signature together, and the
  result verifies perfectly against itself. A listing also cannot show a row
  that was never written or that somebody removed. `morzer fleet ls --expect`
  is what answers both, and without it the `signature` column says a signature
  is present rather than that it checks out.

## Dependency advisories with no code change

An advisory against a module this project depends on is not the same as a
vulnerability in this project, and the difference is worth writing down once
rather than re-deriving it each time a scanner reports one.

`govulncheck ./...` is the check that answers it: it resolves call paths, so it
distinguishes "the module is in the graph" from "this code reaches the
vulnerable symbol". Scorecard's vulnerabilities check works at module level and
cannot make that distinction, so it reports both alike.

- **[GO-2026-5932](https://osv.dev/GO-2026-5932) — `golang.org/x/crypto/openpgp`
  is unmaintained.** Not imported anywhere in this repository. `x/crypto` is
  here for `ssh` and `ssh/knownhosts`, which the SFTP backup target uses; the
  advisory covers a sibling package with no fixed version, since its remedy is
  to stop using it. `govulncheck` reports it as not called. Nothing to change,
  and no version to move to.

## Closed gaps

Kept here because a security policy that only ever grows is one nobody trusts to
be current.

- **A reachable XSS advisory in a markdown dependency** (closed 2026-08-12).
  `goldmark` before 1.7.17 carried [GO-2026-5320](https://osv.dev/GO-2026-5320),
  and it was reachable rather than merely present: `ui.RenderNotes` renders a
  release's notes through `glamour`, which reaches goldmark's HTML renderer.
  What a terminal does with the injected markup is another question, and not one
  worth resting on — the module is now 1.7.17 and `govulncheck` reports no
  reachable vulnerability. The golden renderings are unchanged by the upgrade.

- **`require_signature: true` refused every operation instead of enforcing
  signing** (closed 2026-08-03). Every part of the policy existed except a
  verifier that could satisfy it, so the one control that raised the bar made
  the tool unusable instead. A minisign verifier now enforces it, and an
  installation that requires a signature with no `policy.signing_keys`
  configured is refused where it is written rather than failing every later
  operation with a message about bundles.

- **The offline recovery key had no import path** (closed 2026-08-03). It could
  be created and registered, but nothing could rebuild a machine from it —
  a safeguard `init` insisted on and could not deliver. `installation export`
  and `installation import` close it, and the path is proven end to end against
  real age keys on every CI run: create an installation, delete its entire root,
  rebuild from the export plus the offline key, and read the secrets back with
  the new host's own identity. See
  [Recovering a lost machine](https://morzecrew.github.io/morzer/operating/recovering-a-lost-machine/).

- **The cross-installation restore guard could never fire** (closed 2026-08-03).
  `restore` requires `--force`, and that same flag was passed down as the
  authorisation to restore a backup belonging to a *different* installation — so
  every restore that reached the check had already disabled it. Restoring one
  deployment's data over another now needs `--allow-cross-installation`, which
  is a separate answer to a separate question. Found by the recovery test above,
  not by review.
