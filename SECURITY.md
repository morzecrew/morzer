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

Nothing has been released yet, so `main` is the only supported version. This
section will name a supported range once there is a tag.

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
