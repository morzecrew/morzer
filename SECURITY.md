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

## What is not

- **A malicious root user on the same machine.** Anyone with root can read the
  age identity and every rendered secret. Nothing here defends against that.
- **Attacks against the Docker daemon itself.**
- **A malicious release bundle from a trusted vendor.** Hooks run as root by
  design — that is what a hook *is*. Signature verification, once it lands,
  proves a bundle came from the holder of a key. It does not prove the bundle is
  safe to run, and it is not intended to.
- **The secrecy of a recovery key stored on the machine it protects.** Move it
  elsewhere; `init` says so when it generates one.

## Known gaps

Stated here rather than discovered during an incident:

- Signature verification is designed but not implemented, so setting
  `require_signature: true` currently refuses every operation instead of
  enforcing signing. See [RFC 0004](rfcs/0004-distribution-and-verification.md).
- The offline recovery recipient that `init` requires has no import path yet:
  the key can be created and registered, but rebuilding a machine from it is not
  implemented. See [RFC 0003](rfcs/0003-secrets-recovery-and-onboarding.md).
