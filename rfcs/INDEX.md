# RFCs

Design proposals for morzer. Each RFC captures a design before or while it is
built: the problem, the state of the code today, the locked decisions with
their rationale, and what is deliberately excluded.

## Allocating a number

The next free number is **0023**. Before creating an RFC, glance at the table
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

## Status legend

- 📝 **Draft** — proposed, not started
- 🚧 **In progress** — partially shipped
- ✅ **Complete** — fully shipped
- ❌ **Rejected / withdrawn**
