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

```divergence
decision: 15
grade: ASSUMED
class: spec-gap
at: 2026-11-10T18:21:54Z
attempt: 1
claim: decision 15 has a sync's statement name the document it applied, but RFC 0056 row 18 files a config statement only when the configuration was rendered, and a document that changes only secrets renders none. A sync therefore files its statement whenever its operation ran, and with nothing rendered the statement carries no rendered digest, which is omitted rather than computed over nothing.
evidence: internal/domain/attestation.go:444
action: departed
proposal: ASSUMED — a sync's operation always files its statement with desired_state; the rendered digest is present only when it rendered the configuration
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-10T18:41:36Z
attempt: 1
claim: §5.8 says a pair whose name and comment disagree is refused as bad-signature, and §5.7 that a refused serial is not retried, without saying whether a pair that fails its signature is recorded as refused. Its serial is anybody's who can write to the bucket, so recording it would let a writer with no key refuse serials on the authority's behalf. A bad-signature outcome is recorded in last_fetch only, and only a serial whose signature verified is ever recorded as refused. Entry written after the code, in the same commit.
evidence: internal/lifecycle/ops/sync.go:29-33
action: decided
proposal: UNLISTED — only an authenticated serial is recorded as refused; a pair that fails its signature, or whose signed comment names another installation or serial, moves no watermark
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-10T18:41:36Z
attempt: 1
claim: §5.5 says `sync now` runs one cycle immediately and does not say whether it runs while sync.enabled is false. A cycle applies documents, and the switch is the operator's way to stop that, so `sync now` is refused while the switch is off. Entry written after the code, in the same commit.
evidence: internal/lifecycle/ops/sync.go:100-102
action: decided
proposal: UNLISTED — `sync now` is refused while sync.enabled is false, with the settings that turn it on as the hint
```

```divergence
decision: 23
grade: ASSUMED
class: spec-gap
at: 2026-11-10T18:41:36Z
attempt: 1
claim: decision 23 keeps a refusal's full message "in the local journal", but a document refused before its operation ran (decrypt, parse, expiry, managed names) has no journal record. The message, redacted of secret values, is kept beside the refusal in installation state, with the operation id when one ran. The fleet row's block is built field by field and never carries it.
evidence: internal/domain/sync.go:91-94
action: departed
proposal: ASSUMED — the refusal's message is kept in local state beside the refused serial, never in the row
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-10T18:41:36Z
attempt: 1
claim: an object store's listing prefix may not end in a slash (blob.guardObjectKey refuses what path.Clean changes), so the documents prefix is listed as `state/<product>/<id>` and the keys are filtered by the slashed prefix. A scoped read credential's policy names that prefix, which the docs say.
evidence: internal/adapters/target/blob/object.go:89-98
action: decided
proposal: UNLISTED — the machine lists `state/<product>/<id>` and reads only keys directly under `state/<product>/<id>/`
```

```divergence
decision: 6
grade: ASSUMED
class: spec-gap
at: 2026-11-10T18:41:36Z
attempt: 1
claim: decision 6 is missing from the plan for PR A, and it is machine side. It says a local `config set` on a managed name "warns while sync is enabled ... --force proceeds", which only means something if, without --force, the change does not proceed. It is built as a refusal naming the authority's key id, which --force overrides, for `config set` and `config apply`. The `secret` commands stay as they are (RFC 0056 decision 15).
evidence: rfcs/0057-desired-state-an-installation-pulls-and-applies-to-itself.md:817
action: departed
proposal: ASSUMED — while sync is enabled, a local `config set` or `config apply` naming a managed parameter or setting is refused unless --force, with the authority named; `secret set` is unchanged
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-10T19:04:41Z
attempt: 1
claim: the plan for PR A put `sync.recipient` on the machine side, and RFC 0057 §12 phases it in P5, with the facts it is for. Shipped in PR A it is a setting that does nothing, which an operator would set expecting facts to be published. It is taken out of PR A (it was committed in 18fbb6a6) and goes in with facts in PR C, with `init --sync-recipient`. The FleetRow `facts_digest` the plan listed for PR A moves with it, for the same reason.
evidence: rfcs/0057-desired-state-an-installation-pulls-and-applies-to-itself.md:855-857
action: decided
proposal: UNLISTED — `sync.recipient`, `init --sync-recipient` and the row's `facts_digest` ship with facts (P5), not with the machine side
```

```divergence
decision: 8
grade: ASSUMED
class: drift
at: 2026-11-10T19:31:00Z
attempt: 1
claim: §5.8 step 5 has a cycle publish the fleet row with its outcome, and the cycle as first built did not: the outcome reached the row only at the next hourly fleet publish. Found by the self-audit, not while building, and fixed in 78ed5bb2 and 85933e3b.
evidence: internal/lifecycle/ops/sync.go:108
action: departed
proposal: ASSUMED — unchanged; a cycle publishes the row to the installation's backup targets when its outcome is news (a document applied or refused, or a result unlike the last), and a cycle that recorded nothing publishes none
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-10T19:31:00Z
attempt: 1
claim: §5.7 records a document as refused when its operation fails at any step, and is silent on a failure before any step runs. A refused serial is never tried again, so refusing a document for this machine's own failure, such as a secret store it cannot read, would make the authority republish it. Only a failure the document caused is a refusal there; any other fails the cycle, refuses nothing, and the next tick tries again. Found by the self-audit; fixed in 2028d975.
evidence: internal/lifecycle/ops/sync.go:194-198
action: decided
proposal: UNLISTED — before the operation runs, only a document's own invalid content refuses it; a failure of the machine's own refuses nothing and exits non-zero
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-10T19:31:00Z
attempt: 1
claim: §5.9 has doctor warn on a target unreachable for three intervals, and is silent on a target that cannot be resolved at all, such as one whose credential secret is missing. That target failed every tick and recorded nothing, so doctor warned of nothing. It is recorded as unreachable with the reason as a warning. Found by the self-audit; fixed in 9ded0c10.
evidence: internal/lifecycle/ops/sync.go:229-233
action: decided
proposal: UNLISTED — a sync target that cannot be resolved is recorded as unreachable
```

## Self-audit, 2026-11-10

Scope: the whole branch against origin/main 09a3c3d3, 24 commits, about 4,300
lines across source, tests, docs and the changelog.

Findings, each fixed on the branch with a test that failed first:

1. **A cycle stopped under the lock reported an operation that never ran.** A
   cycle that waited and found its document applied meanwhile, or refused
   under a newly installed release, sent `operation.finished` as a failed
   `config`. Fixed in 3c456836.
2. **A local failure before the operation refused the document for good**
   (spec-gap entry above). Fixed in 2028d975, which also made an invalid
   value refused before the operation exit 0, as the docs said it would.
3. **A target with a missing credential was silent** (spec-gap entry above).
   Fixed in 9ded0c10.
4. **No row was published after a cycle**, which §5.8 step 5 asks for (drift
   entry above). Fixed in 78ed5bb2, with 85933e3b for a cycle that recorded
   nothing.
5. **A refusal found while the authority was re-pinned was notified** although
   it was rightly not recorded. Fixed in 33161c49.

Checks performed:

- Sabotage, 19 mutations of the load-bearing checks: signature verification,
  the comment's product, installation and serial, the envelope match, expiry,
  replay, a refused serial not retried, the complete pair, the serial recorded,
  the stale check under the lock, managed names, the local refusal of a managed
  name, the row's field-by-field copy, doctor's silence window, expired
  notified once, the row publish, the no-operation report, the watermark
  cleared on a re-pin, and the notify-only-when-written guard. All 19 killed.
- Coverage of the new code with the suite and the unit tests together, which
  found the dry-run paths, a target failing part way, a re-pin mid-cycle and
  `sync status`'s printer untested. Each now has a test.
- `go test ./...`, `golangci-lint run ./...`, the docs check, the changelog
  validator, and the docker-tagged scoped-credential test against RustFS.

What I would still distrust: the jitter and period of the sync timer are
checked as rendered unit text, never against a running systemd; and the
scoped-credential test proves one RustFS policy shape, not every store's.

**Drift count: 1.**

```divergence
decision: 22
grade: OPEN
class: drift
at: 2026-11-10T20:05:11Z
attempt: 1
claim: §5.7 says only a serial newer than a refused one is considered, and the cycle skipped only the refused serial itself. With the refused pair withdrawn, an older document the authority had signed, or an older refused one, passed both checks and was applied. Found in review; fixed in 91570d2d.
evidence: internal/lifecycle/ops/sync.go:259
action: decided
proposal: OPEN, ruled 2026-11-10 — every serial newer than both the last applied and the last refused is tried; one below either is a replay
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-10T20:05:11Z
attempt: 1
claim: as with a local failure before the operation, the RFC is silent on a machine that cannot read its own age identity, and the cycle recorded the authentic document as undecryptable, which is never tried again. A missing, unreadable or malformed identity now fails the cycle and refuses nothing; a ciphertext no identity matches is still refused. Found in review; fixed in bf6b51a4.
evidence: internal/lifecycle/ops/sync.go:299
action: decided
proposal: UNLISTED — an identity this machine cannot read refuses no document; only a ciphertext its identity does not match is undecryptable
```

## Review, 2026-11-10

Found in review of #335 and fixed, each with a test that failed first:
the decision 22 drift above; a non-canonical or zero serial (abaf1c4b);
`init --repair` dropping the sync block (69591510); the wizard's printed
command without `--sync-*` (509c05f1); hand-edited state not validated
(e4914996); `.` and `..` as credential names (1f1653b9); the local refusal
skipped under the lock (c6d6245b); the identity spec-gap above (bf6b51a4); an
applied document recorded as refused after a unit failure (2650c6f8); a
no-op document not publishing the row (ca9e9d58); `sync.expired` sent twice
by two cycles (8ee05261); a sandbox keeping sync settings without a target
(4ff22896). The acceptance scenario's row-schema check moved to 2 (ba780aaa).

**Drift count: 2.**

## Build, PR B (P4)

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-10T20:59:59Z
attempt: 1
claim: §5.11 has `state publish` stamp the serial and the expiry into a document `--file` names, and does not say what the file holds. It holds a change document without the envelope: the command writes the envelope, checks the file as a change document, and checks the whole document as a machine will read it before anything is encrypted.
evidence: internal/lifecycle/ops/state.go:256
action: decided
proposal: UNLISTED — `state publish --file` takes a change document; the envelope is the command's, and the composed document is checked before it is encrypted
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-10T20:59:59Z
attempt: 1
claim: §5.3 writes each pair once and §5.7 refuses a serial not newer than the last applied, and neither says what `state publish` does with such a serial. It refuses one not newer than the newest complete pair on the target, and one already present there, since the machine would refuse it as a replay.
evidence: internal/lifecycle/ops/state.go:165
action: decided
proposal: UNLISTED — `state publish` refuses a serial not newer than the newest published, and one already published
```

```divergence
decision: 26
grade: ASSUMED
class: spec-gap
at: 2026-11-10T20:59:59Z
attempt: 1
claim: decision 26 has `state publish` keep the newest three pairs and does not say how it removes the rest or what a failed removal means. It removes each pair's signature before its ciphertext, so a pair half removed is not a document, removes lone objects below the cutoff too, and reports a failed removal without failing the publish, which has already written its document.
evidence: internal/lifecycle/ops/state.go:269
action: departed
proposal: ASSUMED — a prune removes each signature before its ciphertext, and its failure is reported, never a failed publish
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-10T20:59:59Z
attempt: 1
claim: §5.3 lets the authority encrypt to additional recipients, such as its own key, and §5.11 names no flag for them. `state publish --recipient`, repeatable, adds them beside the machine's.
evidence: internal/lifecycle/ops/state.go:68
action: decided
proposal: UNLISTED — `state publish --recipient <age1...>` encrypts to it as well as the machine
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-10T20:59:59Z
attempt: 1
claim: §6 lists acceptance-rig cases for P4, and the rig is a shell scenario that runs the demo product on its port. This PR builds them as docker-tagged suite tests against a real object store instead, which run here and in CI's container job: publish through the roster, apply with a credential that reads only one prefix, `state ls`, and pruning. The rig's cases that need two products, a rebuilt machine or facts are left for the acceptance rig, the last of them for P5.
evidence: test/suite/sync_docker_test.go:83
action: decided
proposal: UNLISTED — P4's end-to-end cases run as docker-tagged suite tests against a real object store; the rig gains the multi-product and rebuild cases later
```

## Self-audit, PR B, 2026-11-10

Scope: `ObjectStore.DeleteObject` in every transport and the contract, the
`state` operations, commands and view, docs, changelog and tests.

- Sabotage, 12 mutations: all killed but two, each an equivalent mutant.
  Taking out `state publish`'s check that the row verified survives, because
  `fleetRowAt` hands back no row for any unverified one once the roster keys
  the entry, and an unkeyed entry is refused before. Writing the signature
  before the ciphertext survives, because a machine needs both objects of a
  pair, so neither order ever exposes half of one. The check and the order
  stay, as the decision states them.
- The object contract's two new legs pass on the fake, localdir, the registry,
  RustFS and the SSH server.
- `go test ./...`, lint, the docs check and the changelog validator pass.

What I would still distrust: a prune against a store whose listing lags its
writes, and an authority publishing from two machines at once, which the
serial check reads before it writes.

**Drift count: 2.**
