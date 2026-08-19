# RFCs

Design proposals for morzer. Each RFC captures a design before or while it is
built: the problem, the state of the code today, the locked decisions with
their rationale, and what is deliberately excluded.

## Live design work

Everything else in the table below has landed. Most of the directory has, which
is the point of this section: a reader looking for what is still being decided
should not have to pick those rows out of the whole design history.

Deliberately without a count. The sentence here used to carry one — "22 of 28",
pinned to 0.1.0 — and it was wrong by the next release, which is the argument
against putting a number anywhere it has to be maintained.

A list rather than a table, deliberately: the index table below is parsed, and a
second table of the same shape is a second set of rows for the same numbers.

- **[0023 — Runtimes beyond Compose](0023-runtimes-beyond-compose.md).** P1a
  shipped: the leak inventory and the boundary checker. **P1b is two-thirds
  answered.** A rootless Podman host settled §12 items 5 and 6 — 0010's volume
  capture names no host path, so rootless storage roots break nothing, and
  0011's registry is reachable over plain HTTP with `--tls-verify=false`.
  **Item 4 is measured as of 2026-08-18, and P1b is complete.** It asked what a
  tmpfs holds at boot; the answer is nothing, always, which turned out not to be
  the question — the ordering decides it, and `EnvironmentFile=-` is what makes a
  misconfigured boot report success (decisions 21 and 22). **P3 is no longer gated.**
  **P2 is partly shipped**: the manifest gained `runtimes:` with `runtime:`
  deprecated and still read, decision 8 resolved against the option §4.1
  preferred, and the runtime fixed at `init` at installation schema 9. `import`
  now refuses a runtime this manager cannot drive, `doctor` reports the one an
  installation is fixed to, and the `tools.Docker` leak is closed by an optional
  capability rather than a rename — there was no name in it to rename.
  **P2 is complete but for one thing.** Its last item turned out to be a data
  path rather than a tidy-up: `runtimes:` could not express a project while the
  deprecated block's default supplied one, so the documented migration renamed
  every volume of any deployment whose project was not its product name.
  Runtimes now take an opaque `options` map, the installation records what it
  was created with and refuses a release that changes it, and
  `<PRODUCT>_COMPOSE_PROJECT` is supplied by the runtime rather than promised by
  the core hook ABI. The leak inventory is **17**, down from 19 at P1.
  **P2 is complete.** The deprecation of `runtime:` finally says something: it
  stops being read in **0.4.0**, and `release verify`, `init` and `update` say so
  — no other command does, because a manifest the operator did not write and
  cannot change is not something to be told about on every invocation. The
  scaffold, which had been emitting the deprecated block all along, now writes
  `runtimes:` and declares the manager version it needs. That declaration is what
  exposed a manager built between tags understating its own version badly enough
  to refuse the bundle its own scaffold had just written. **What remains of this
  RFC is P1b item 4, and P3 behind it.**
- **[0024 — The support bundle](0024-the-support-bundle.md).** ✅ Complete. The
  inventory, the archive, redaction proved against seeded values, `support
  redact --check`, an archive encrypted to the vendor and unreadable by the
  machine that wrote it, and now signed with 0028's key and readable back by
  `support inspect`. The window did close, so the declaration went through
  0018's `extensions` namespace — measured against a released binary, where a
  top-level field refuses the whole bundle. `inspect` will not verify against
  the key the archive names, which is 0026 §3.6's finding in a second artifact.
- **[0027 — Desired state in a repository](0027-desired-state-in-a-repository.md).**
  P1 shipped as `installation describe`. **P2 is gated on a user who is not the
  author asking for it**, and if that never happens the correct outcome is that
  P2 is never built.
- **[0028 — The machine's signing identity](0028-the-machines-signing-identity.md).**
  P1 shipped: the key exists and signs an artifact, which is why it shipped
  beside its consumer rather than ahead of it. P2 is rotation, wanted the first
  time somebody believes a host was compromised.
- **[0029 — macOS as a development host](0029-macos-as-a-development-host.md).**
  P1 shipped: the tree compiles and vets for darwin on both architectures, gated
  in CI, and `install.sh`'s "build from source" advice is true. P2 — a published
  darwin archive and the tier around it — is gated on somebody who is not the
  author asking for it.

**An RFC retires from this section the moment it is ✅ Complete or ❌ Rejected**,
and its row in the index below carries it from then on. The file never moves and
is never deleted: 330 cross-references between RFCs use bare filenames, a
rejection that leaves no record gets re-proposed, and a shipped design is the
only account of why the code is shaped the way it is.

## Where execution disagreed

[EXECUTION-LOG.md](EXECUTION-LOG.md) records the places building something
disagreed with the design for it, grouped by the wave that found them and
classified by whether the design could have known. It is deliberately not part of
any RFC: an RFC edited to match what was built is a document that has stopped
recording that a decision ever changed, and a separate file is the only way the
change stays visible from both ends.

It carries no number, has no status, and does not appear in the table below —
the one resident of this directory that is not an RFC. Its decision rows are
proposals until an author accepts or refuses them, and the log says which.

## Allocating a number

The next free number is **0031**. Before creating an RFC, glance at the table
below (or `ls` this directory) and take the next unused integer — numbers
collide when minted in parallel. Update this table in the same change.

Filename: `NNNN-kebab-title.md`. Keep the `# RFC NNNN — Title` H1 and the
number in the filename in sync.

## Index

| # | Title | Status | One-line routing description |
|---|---|---|---|
| [0001](0001-update-and-rollback.md) | Update and rollback | ✅ Complete | Moving between releases: `update` and `rollback` as engine operations, with a rollback that refuses rather than guesses when the database cannot come back with the containers. |
| [0002](0002-rich-terminal-renderer.md) | Rich terminal renderer | ✅ Complete | What an operation looks like at a terminal: a live step list behind the rich mode, with plain output as the reference it may not diverge from. |
| [0003](0003-secrets-recovery-and-onboarding.md) | Secrets, recovery and onboarding | ✅ Complete | Making the offline recovery key usable — exporting an installation's identity, importing it onto a rebuilt machine, and editing secrets without leaving plaintext behind. |
| [0004](0004-distribution-and-verification.md) | Distribution and verification | ✅ Complete | How a release bundle reaches a machine and what is checked when it arrives: several transports behind one registry, signature verification, and an offline path. |
| [0005](0005-continuous-integration-and-release.md) | Continuous integration and release automation | ✅ Complete | The build pipeline: what runs on a change, what runs on a tag, and how CI and a developer's machine are kept from drifting apart. |
| [0006](0006-documentation-site.md) | Documentation site | ✅ Complete | Where the documentation lives and how it is kept true: a versioned site split by audience, with a build that fails on drift from the code it describes. |
| [0007](0007-operator-parameters.md) | Operator parameters | ✅ Complete | The knobs an operator may set after install — declared by the release, typed and validated, and delivered to Compose, templates and hooks under one name. |
| [0008](0008-test-coverage-program.md) | Testing the claims: a coverage programme to 95% | ✅ Complete | A deliberate programme to raise test coverage, and to make every advertised security property name the test that enforces it rather than the paragraph that claims it. |
| [0009](0009-backup-targets.md) | Backup targets: getting a backup off the machine | ✅ Complete | Getting a backup off the machine that took it: a target port with several schemes, and the credential problem that restoring from one creates. |
| [0010](0010-compose-volume-capture.md) | Capturing Compose volumes | ✅ Complete | Backing up a project's named volumes, and the consistency question that decides whether a copy of a running volume is a backup at all. |
| [0011](0011-bundled-container-images.md) | Bundled container images | ✅ Complete | Installing a release on a machine that cannot reach the vendor's registry, by letting the bundle carry its own container images. |
| [0012](0012-packing-images-into-a-bundle.md) | Packing images into a bundle | ✅ Complete | The vendor's side of a bundle that carries images: a command that copies them out of the registry into the bundle and re-signs the tree. |
| [0013](0013-bundle-authoring-experience.md) | Bundle authoring experience | ✅ Complete | What a bundle vendor meets: a `verify` that catches what it currently passes, and a scaffold whose output verifies unedited. |
| [0014](0014-building-a-release-bundle.md) | Building a release bundle | ✅ Complete | The two steps between a bundle source tree and something publishable, and why signing has to sit between them. |
| [0015](0015-notifications.md) | Notifications | ✅ Complete | Telling somebody an operation finished, for the operations that run with nobody watching. |
| [0016](0016-update-checking-and-unattended-updates.md) | Update checking and unattended updates | ✅ Complete | Learning that a newer release exists, and the gate that decides which ones may be installed without a human. |
| [0017](0017-recovery-artifacts.md) | Recovery artifacts | ✅ Complete | How many artifacts it takes to rebuild a lost machine, and making the one every operator already has carry the identity. |
| [0018](0018-the-pre-1-0-manifest-surface.md) | The pre-1.0 manifest surface | ✅ Complete | A one-time sweep of the manifest before the first tag freezes it, since strict decoding makes every field at every level permanent. |
| [0019](0019-the-command-surface.md) | The command surface | ✅ Complete | The surface an operator meets before any capability: how `--help` is organised, how output is rendered, and where the command reference is found. |
| [0020](0020-several-installations-on-one-machine.md) | Several installations on one machine | ✅ Complete | One machine holding more than one installation: saying which are there, and refusing to guess which one a command was meant for. |
| [0021](0021-into-the-running-deployment.md) | Into the running deployment | ✅ Complete | The commands for when the deployment is running and something is wrong — logs, process state, resource use, and a command inside a container. |
| [0022](0022-bootstrapping-the-manager.md) | Bootstrapping the manager | ✅ Complete | Getting the manager onto a machine in the first place: an install script that verifies what it downloaded, and instructions somebody has run. |
| [0023](0023-runtimes-beyond-compose.md) | Runtimes beyond Compose | 🚧 In progress | Grading the runtime port by writing a second implementation of it — rootless Podman with Quadlet — on the theory that a port with one adapter is a guess rather than an abstraction. |
| [0024](0024-the-support-bundle.md) | The support bundle | ✅ Complete | One redacted archive an operator can hand to a stranger, because every command a vendor has runs before the bundle leaves their machine and none after. |
| [0025](0025-attesting-an-installation.md) | Attesting an installation | ✅ Complete | Making the manager's evidence leave the machine as a signed statement, and binding the feature to a verifier that can fail for a reason other than corruption. |
| [0026](0026-fleet-as-a-read-model.md) | Fleet as a read model | ✅ Complete | Seeing many machines without a control plane: each publishes one signed row to a bucket, the reader can never act, and a roster names who is expected and which key each signs with. |
| [0027](0027-desired-state-in-a-repository.md) | Desired state in a repository | 🚧 In progress | One file that fully determines an installation, written to be read rather than applied — and the boundary written down so applying it stays gated. |
| [0028](0028-the-machines-signing-identity.md) | The machine's signing identity | 🚧 In progress | The key three other RFCs assumed a machine already had: what may sign for itself, why that does not reopen the rule keeping release keys off hosts, and what a rebuild does to a chain. |
| [0029](0029-macos-as-a-development-host.md) | macOS as a development host | 🚧 In progress | Making `GOOS=darwin` compile, and then bounding what that means: a tier for authoring and evaluating bundles on a Mac, never for running a production installation. |
| [0030](0030-unit-enablement-is-the-operators.md) | Unit enablement is the operator's | ✅ Complete | Where the manager's authority over its own units ends: `systemctl disable` sticks until `init --repair`, an installation can declare no backup timer, and the units stay in `/etc/systemd/system`. |

## Status legend

- 📝 **Draft** — proposed, not started
- 🚧 **In progress** — partially shipped
- ✅ **Complete** — fully shipped
- ❌ **Rejected / withdrawn**
