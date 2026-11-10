# desired-state-sync · RFC 0057 P2–P5

On branch `desired-state-sync` from `main` at `09a3c3d3`, building
[RFC 0057](../rfcs/0057-desired-state-an-installation-pulls-and-applies-to-itself.md)
P2–P5. P1 is RFC 0056, which shipped in #333. Decisions 11–15 were ruled by the
author 2026-11-10. Decisions 18–22 are `OPEN`, and the RFC says they stay
open for the author.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## Plan

Proposed as three pull requests, each green and useful alone, in this order.

### PR A — the machine side (P2, P3)

Domain:

- `internal/domain/fleet.go`:
  - `FleetRow` gains `AgeRecipient`, `Sync` (the §5.9 block) and
    `FactsDigest`;
  - `FleetSchemaVersion` goes from 1 to 2. A manager reading schema 1 refuses
    a schema 2 row as written by a newer manager (`Validate`), which is the
    rule RFC 0026 already has.
- `internal/domain/installation.go`:
  - `Installation.Sync`: `target`, `credentials`, `authority`, `recipient`,
    `interval` and `enabled`;
  - the watermark: applied and refused serials with their authority key id,
    the step and the class, and the last fetch with its result and time;
  - `InstallationSchemaVersion` goes from 18 to 19, with the migration.
- `internal/domain/manifest.go`:
  - `Manifest.Managed`, with parameters, secrets and settings;
  - morzer's fixed allow-list for settings (`installation.profile`,
    `installation.domains`, decision 5, and decision 21 if ruled);
  - a `min_manager_version` floor for `managed`, checked by `release verify`
    beside `combinable`'s.
- `internal/domain/sandbox.go`: `sync.*` added to `SandboxDrops` (decision 16).
- `internal/domain/attestation.go` and `schemas/morzer-attestation-v4.json`:
  - the v4 predicate, which adds a `desired_state` block of `serial`,
    `digest` and `authority_key_id`, present only on a sync's statement
    (decision 15);
  - v3 and earlier stay readable.

Operations:

- `internal/lifecycle/ops/settings.go`:
  - the `sync.*` settings, all refused from a document by decision 5's list;
  - `sync.enabled=true` refused without `sync.authority`;
  - `sync.authority` changed clears the stored serials, in the same
    journalled write (decision 27).
- `internal/lifecycle/ops/sync.go` (new): `SyncNow`, the §5.8 cycle:
  - list the installation's prefix and take the newest complete
    `<serial>.age` and `.minisig` pair (decision 26);
  - verify the signature against `sync.authority`, including that the
    trusted comment's serial matches the object name;
  - check the comment's product, installation and serial against this
    machine and the watermark;
  - decrypt with the machine's age identity, parse with RFC 0056's
    `ParseConfigChange` behind the envelope, and check the envelope and
    `expires_at`;
  - check the `managed` names;
  - apply through `runChange`, with the serial written in the record step
    for every document (decision 25), and a refusal recorded with its step
    and class (decision 23);
  - each `last_fetch` result of §5.9 recorded in state.
- `internal/lifecycle/ops/config_apply.go`: `runChange` takes the
  installation fields a caller adds to the record step, so that a document
  holding only secrets still writes its serial (decision 25).
- `internal/lifecycle/ops/fleet_row.go`: the row's `age_recipient` from the
  machine recipient, the `sync` block from state, and decision 23's
  refusal classes.
- `internal/lifecycle/ops/doctor.go`: a warning when sync is enabled and the
  last applied document expires within a day or has expired, and when the
  target has been unreachable for more than three intervals.
- `internal/lifecycle/ops/notify*.go`: `refused` and `expired` events.
- Supervisor: a `sync` timer pair, through `UnitParams.SyncTimer` and
  `SyncInterval`, in `internal/adapters/supervisor/systemd`, with jitter
  from `RandomizedDelaySec`. The timer and `update.auto_apply` both take the
  deployment lock, and a tick that cannot take it exits 0, as an unattended
  update does.

Signing:

- `internal/ports/signer.go`: the checker returns the trusted comment once
  the signature verifies, so that a machine reads `product`, `installation`
  and `serial` only from authenticated text.

CLI:

- `morzer sync now` and `morzer sync status`.
- `init --sync-target`, `--sync-authority` and `--sync-recipient`.

Docs and changelog:

- `operating/managing-from-elsewhere.md` (new).
- `reference/desired-state.md` (new).
- `reference/manifest.md` for `managed`, and `reference/fleet.md` for the
  new fields.
- `reference/parameters.md` for the `sync.*` settings.
- `CHANGELOG.md` Unreleased: Added, and Changed for the fleet row schema 2
  and state schema 19.

Tests:

- unit tests for each check in the cycle;
- the suite with a test authority key and age identity;
- the docker-tagged suite and MinIO for the acceptance rig's machine-side
  cases.

### PR B — the authority side (P4)

- `internal/ports/target.go`: `ObjectStore.DeleteObject`, for decision 26's
  pruning. It goes in all three transports and the contract suite. The
  port's comment says there is no delete "because nothing needs one"; this
  is the thing that needs one.
- `morzer state key generate`: an unencrypted minisign key (0600) for the
  authority, with the public line printed.
- `morzer state publish --target … --expect roster.yaml --product … --installation … --file … --key …`:
  - refuses an installation the roster does not list, or a row that does
    not verify against its key (decision 13);
  - takes `age_recipient` from the verified row and stamps the serial and
    `expires_at`;
  - encrypts, then writes the ciphertext and then its signature;
  - prunes all but the newest three pairs.
- `morzer state ls --target … --expect roster.yaml`: rows joined with sync
  outcomes, and unexpected rows.
- Docs, changelog and tests, including the acceptance rig's end-to-end cases
  with MinIO.

### PR C — facts (P5)

- `Manifest.Published.Task`, with its floor.
- After a successful operation, the task's output (64 KiB at most) is
  encrypted to `sync.recipient`, signed with the machine key and published
  as `fleet/<product>/<id>/facts.age` with its `.minisig`. The row names its
  digest (decision 28).
- `morzer state facts --identity …` checks the signature against the
  roster's key and the digest against the newest verified row, then
  decrypts.
- Docs, changelog and tests.

## Decisions the plan needs that the spec does not settle

The author's rulings, with a recommendation each (decisions 18–22):

- **18, the serial.** Recommended: a timestamp in milliseconds by default,
  with `--serial` for an authority that keeps counters. Decision 27 already
  bounds the damage of one that runs ahead.
- **19, per-group documents.** Recommended: later. The layout stays
  `state/<product>/<id>/`, and a group prefix can be added without moving
  it.
- **20, the size limit.** A ciphertext is read back through `GetObject`,
  which refuses more than `MaxObjectBytes` (1 MiB, `target.go:152`). RFC
  0056's 1 MiB applies to the plaintext. Recommended: `state publish`
  refuses a ciphertext over 1 MiB, which leaves the plaintext a little
  under it.
- **21, `installation.domains` manageable.** Recommended: yes, as the draft
  lists it. A release opts in per name, and the plan shows the re-create
  of every service before anything runs.
- **22, a refused serial.** Recommended: try every newer serial, as the
  draft does. Documents are cumulative (decision 24), so a newer one
  supersedes the refused one whole.

Gaps found reading the code:

- **`managed` needs a manager-version floor** (spec-gap). §5.6 says older
  managers ignore the field, but the manifest is decoded strictly
  (`internal/release/load.go:199-200`), so an older manager refuses the
  manifest. Proposed: a `min_manager_version` floor, checked by
  `release verify`, as `combinable` and `text` have. The same goes for
  `published` in PR C. Not load-bearing: the precedent decides it.
- **`sync.target`'s credentials** (spec-gap). §5.5 says "credentials as for
  backups", where a target's credential is a named secret
  (`internal/domain/installation.go:717`), and lists no setting for it.
  Proposed: `sync.credentials=<secret>`, as `backup target add --credentials`
  takes one. Not load-bearing.
- **The authority's key** (spec-gap). §5.11 takes `--key` and says nothing of
  where the key comes from. The minisign adapter refuses an encrypted key
  (`internal/adapters/sign/minisign/minisign.go`, `loadKey`). Proposed:
  `morzer state key generate`, and `--key` takes that key or one from
  `minisign -G -W`. Not load-bearing.
- **The v4 predicate's schema** (spec-gap). Decision 15 has RFC 0025 define
  v4, and RFC 0025 does not yet. Proposed: built here as a `desired_state`
  block, with the schema in `schemas/` beside v3's and rows proposed for RFC
  0025. Contained: it touches only a sync's statement.

Readiness gate: none of the gaps is load-bearing on its own, but decisions
18–22 are the author's, and 21 and 22 change behaviour. Build waits for those
rulings and for the PR split.

Ruled by the author 2026-11-10: decisions 18–22 as recommended, the four gaps
as proposed, and the three pull requests. PR A follows.

## Build

```divergence
decision: 18
grade: OPEN
class: spec-gap
at: 2026-11-10T17:57:07Z
attempt: 1
claim: the serial is a timestamp in milliseconds by default, and state publish takes --serial for an authority that keeps counters; ruled by the author as recommended.
evidence: rfcs/0057-desired-state-an-installation-pulls-and-applies-to-itself.md:829
action: decided
proposal: ASSUMED — a timestamp serial in milliseconds by default, overridable with --serial; decision 27 bounds an authority whose serials run ahead
```

```divergence
decision: 19
grade: OPEN
class: spec-gap
at: 2026-11-10T17:57:07Z
attempt: 1
claim: per-group documents are left for later, and the layout stays per installation, which a group prefix can be added beside without moving it; ruled by the author as recommended.
evidence: rfcs/0057-desired-state-an-installation-pulls-and-applies-to-itself.md:830
action: decided
proposal: ASSUMED — documents are per installation only; per-group documents are a later design
```

```divergence
decision: 20
grade: OPEN
class: discovery
at: 2026-11-10T17:57:07Z
attempt: 1
claim: a ciphertext is read back through GetObject, which refuses more than MaxObjectBytes, 1 MiB, while RFC 0056 bounds the plaintext at 1 MiB; a 1 MiB plaintext would encrypt to more than a machine reads. state publish refuses a ciphertext over 1 MiB, leaving the plaintext a little under it. Ruled by the author as recommended, for now.
evidence: internal/ports/target.go:152
action: decided
proposal: ASSUMED — state publish refuses a ciphertext over MaxObjectBytes, 1 MiB, so the plaintext is bounded a little under RFC 0056's 1 MiB
```

```divergence
decision: 21
grade: OPEN
class: spec-gap
at: 2026-11-10T17:57:07Z
attempt: 1
claim: installation.domains is one of the two settings morzer lets a release make manageable, as the draft lists it; ruled by the author as recommended.
evidence: rfcs/0057-desired-state-an-installation-pulls-and-applies-to-itself.md:832
action: decided
proposal: ASSUMED — morzer's fixed list of settings a document may touch is installation.profile and installation.domains
```

```divergence
decision: 22
grade: OPEN
class: spec-gap
at: 2026-11-10T17:57:07Z
attempt: 1
claim: a refused serial does not block a newer one; every newer serial is tried, and since documents are cumulative a newer one supersedes the refused one whole. Ruled by the author as recommended.
evidence: rfcs/0057-desired-state-an-installation-pulls-and-applies-to-itself.md:833
action: decided
proposal: ASSUMED — every serial newer than the last applied and the last refused is tried
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-10T17:57:07Z
attempt: 1
claim: §5.6 says older managers ignore the managed field, but the manifest is decoded with DisallowUnknownField, so an older manager refuses a manifest that carries it. The release needs a min_manager_version floor, as combinable and text have, and so does published in PR C. Agreed by the author before the build; decision 5 itself (releases opt in name by name, morzer forbids trust and target settings) is followed.
evidence: internal/release/load.go:200
action: decided
proposal: ASSUMED — a release that declares managed, or published, needs a min_manager_version floor of the manager that ships it, checked by release verify
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-10T17:57:07Z
attempt: 1
claim: §5.5 says sync.target takes credentials as for backups, where a target's credential is a named secret, and lists no setting that names it.
evidence: internal/domain/installation.go:725
action: decided
proposal: ASSUMED — sync.credentials names the secret holding the sync target's credential document, as backup target add --credentials does
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-10T17:57:07Z
attempt: 1
claim: §5.11 takes --key for the authority and says nothing of where the key comes from; the minisign adapter refuses a passphrase-encrypted key.
evidence: internal/adapters/sign/minisign/minisign.go:240
action: decided
proposal: ASSUMED — morzer state key generate writes an unencrypted minisign key for the authority, and --key takes that key or one from minisign -G -W
```

```divergence
decision: 15
grade: ASSUMED
class: spec-gap
at: 2026-11-10T17:57:07Z
attempt: 1
claim: decision 15 has RFC 0025 define the v4 predicate, and RFC 0025 does not. It is built here as a desired_state block of serial, digest and authority_key_id, present only on a sync's statement, with its schema beside v3's.
evidence: internal/domain/attestation.go:44
action: departed
proposal: ASSUMED — the v4 predicate adds a desired_state block, built with RFC 0057 P3; rows for RFC 0025 record it
```
