# RFCs

Design proposals for morzer. Each RFC captures a design before or while it is
built: the problem, the state of the code today, the locked decisions with
their rationale, and what is deliberately excluded.

## Allocating a number

The next free number is **0007**. Before creating an RFC, glance at the table
below (or `ls` this directory) and take the next unused integer — numbers
collide when minted in parallel. Update this table in the same change.

Filename: `NNNN-kebab-title.md`. Keep the `# RFC NNNN — Title` H1 and the
number in the filename in sync.

## Index

| # | Title | Status | One-line |
|---|---|---|---|
| [0001](0001-update-and-rollback.md) | Update and rollback | ✅ Complete | Adds `update` and `rollback` as engine operations: fetch, verify against a recorded digest, gate on `compatibility`, take a pre-update backup, swap the release pointer, and compensate by reverting it. Wires the already-written but uncalled `CheckUpgrade`/`AssessRollback` — P1–P2 shipped: both functions under test (which found two defects), a second bundle fixture, `update` with fault injection at six points, `rollback` with its three-question refusal, and `--to` on both — without which a second rollback returns to where the first started. Never rolls a database back automatically — the assessment reports containers, schema and restore-required separately, and refuses rather than guesses. |
| [0002](0002-rich-terminal-renderer.md) | Rich terminal renderer | 📝 Draft | Adds the Bubble Tea live step list behind the existing `ModeRich`, which currently falls back to plain. The renderer is a bus subscriber with no back-channel: it cannot influence control flow, a panic in it is contained, and plain mode stays the reference for what must be conveyed. Rendering-only — no engine, port or event-schema changes. |
| [0003](0003-secrets-recovery-and-onboarding.md) | Secrets, recovery and onboarding | 🚧 In progress — P1–P2 shipped | `installation export` / `import` make the offline recovery key usable: a machine whose root is deleted entirely is rebuilt from an export plus the key, keeping its original installation id so its own backups stay restorable — proven end to end against real age keys on every CI run. Found two defects doing it, both recorded in §12. `secret edit` over tmpfs, rotation reporting and the `huh` init wizard remain. |
| [0004](0004-distribution-and-verification.md) | Distribution and verification | ✅ Complete | Four transports behind one scheme-dispatching registry — directory, `tar.zst`, HTTPS and OCI — all producing the same content digest, so pinning a release does not pin a transport. Archive extraction refuses traversal, links, device nodes and bombs, each with a fixture built in the test that refuses it. minisign verification makes `require_signature` a working control. Adds a generated JSON Schema, an offline install path with a `doctor` check for it, and signed reproducible builds. Found that the OCI client does not verify blob contents; both of the RFC's dependency-weight predictions were wrong and are corrected in §9. |
| [0005](0005-continuous-integration-and-release.md) | Continuous integration and release automation | ✅ Complete | A change-gated `quality → test → docs → acceptance → coverage` topology invoking `just` recipes, so local and CI cannot drift. Fails the build when a real-adapter contract suite *skips*, which once let CI pass without ever exercising the production secret store. Tagged releases re-run CI as a reusable workflow and refuse a tag that is not on main. The acceptance job runs the whole lifecycle against real Docker in ~40s — and found on its first run that the manifest's digest-pinned images decided nothing at all. |
| [0006](0006-documentation-site.md) | Documentation site | 🚧 In progress — P1–P3 shipped | Moves the README into a versioned zensical site under `pages/`, split by Diátaxis and by audience — operators and bundle vendors were interleaved. Reference section and the `dev` deployment are live; `just docs-check` fails the build on a broken link, a page missing from the nav, or an undocumented command, flag, error code, exit code, manifest field or hook variable, all read out of the source. Prose sections, the vendor section extracted from `testdata/bundle/`, and versioned release deployment remain. |

## Status legend

- 📝 **Draft** — proposed, not started
- 🚧 **In progress** — partially shipped
- ✅ **Complete** — fully shipped
- ❌ **Rejected / withdrawn**
