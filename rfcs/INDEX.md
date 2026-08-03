# RFCs

Design proposals for morzer. Each RFC captures a design before or while it is
built: the problem, the state of the code today, the locked decisions with
their rationale, and what is deliberately excluded.

## Allocating a number

The next free number is **0005**. Before creating an RFC, glance at the table
below (or `ls` this directory) and take the next unused integer — numbers
collide when minted in parallel. Update this table in the same change.

Filename: `NNNN-kebab-title.md`. Keep the `# RFC NNNN — Title` H1 and the
number in the filename in sync.

## Index

| # | Title | Status | One-line |
|---|---|---|---|
| [0001](0001-update-and-rollback.md) | Update and rollback | 📝 Draft | Adds `update` and `rollback` as engine operations: fetch, verify against a recorded digest, gate on `compatibility`, take a pre-update backup, swap the release pointer, and compensate by reverting it. Wires the already-written but uncalled `CheckUpgrade`/`AssessRollback`. Never rolls a database back automatically — the assessment reports containers, schema and restore-required separately, and refuses rather than guesses. |
| [0002](0002-rich-terminal-renderer.md) | Rich terminal renderer | 📝 Draft | Adds the Bubble Tea live step list behind the existing `ModeRich`, which currently falls back to plain. The renderer is a bus subscriber with no back-channel: it cannot influence control flow, a panic in it is contained, and plain mode stays the reference for what must be conveyed. Rendering-only — no engine, port or event-schema changes. |
| [0003](0003-secrets-recovery-and-onboarding.md) | Secrets, recovery and onboarding | 📝 Draft | Completes the secret surface with `secret edit` over tmpfs, rotation policy reporting, and installation export/import for rebuilding a machine from an offline recovery key. Adds the `huh` init wizard as a strictly optional front-end over existing flags, so every path stays scriptable without a TTY. |
| [0004](0004-distribution-and-verification.md) | Distribution and verification | 📝 Draft | Adds `tar.zst`, HTTPS and OCI release sources plus minisign signature verification behind the existing `ReleaseSource`/`Verifier` ports, and makes the `require_signature` installation policy fail closed. Ships reproducible amd64/arm64 builds via goreleaser, an offline install path, and a published JSON Schema for manifests. |

## Status legend

- 📝 **Draft** — proposed, not started
- 🚧 **In progress** — partially shipped
- ✅ **Complete** — fully shipped
- ❌ **Rejected / withdrawn**
