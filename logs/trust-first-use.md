# trust-first-use · RFC 0052 P1, the manager side

On branch `trust-first-use` from `main` at `8a8799d7`, building
[RFC 0052](../rfcs/0052-trusting-a-releases-key-on-first-use.md) P1: §5.1–§5.5
and decisions 1–13 and 15, with the operator docs. P2, the vendor side, landed
in #311 and supplies the key reader this uses (`ports.ShippedKeyReader`).

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## Plan

- `internal/lifecycle/ops/init.go`:
  - `InitOptions` gains `SignatureNotRequired`, for `--require-signature=false`,
    beside the existing `RequireSignature` (decision 5). Unset is both false, so
    existing callers keep their meaning. It also gains `ShippedKeyID`, the key
    the wizard showed (decision 10).
  - The up-front refusal of `--require-signature` with no key stays only for an
    `init` given no `--release` (decision 6).
  - `Init` loads the installation a repair finds, and works out what the stage
    step checks the release against: the policy plus `--signing-key` on a
    repair (decision 15), the given keys (decision 4), or the key the release
    ships on a first use (decisions 1, 3).
  - The stage step reads the shipped key from the copy it fetched, refuses one
    whose id is not the one the wizard approved, verifies against it with a
    signature required, and hands it to the write step through engine state.
    The write step records it with `require_signature: true`. With no key, it
    refuses after an explicit `--require-signature` and otherwise warns
    (decisions 6, 12).
  - The journal carries `trusted_key_id` and `trusted_key_source` flags;
    `Result.Data` carries `trusted_key`, `trusted_key_id` and
    `trusted_key_source` (decisions 9, 11).
  - The plan: a `PlanDetail` on the stage step naming the key a local bundle
    ships, or saying a remote one's would be trusted. Nothing where the stage
    step would not trust one (decision 10).
- `internal/lifecycle/ops/update.go`, `stepVerifyBundle`: a refusal of a
  release signed by an untrusted key it ships names that key's id (decision 8).
  An installation that trusts no key gets a notice naming the shipped key's id
  and the two commands, and trusts nothing (decisions 7, 13).
- `internal/cli/commands.go`: `--require-signature=false` sets
  `SignatureNotRequired` through `Flags().Changed`. After a first-use `init`, a
  callout prints the key, its id and where it came from (decision 9).
- `internal/cli/init_wizard.go`: the question, where the stage step would trust
  a key, defaulting to yes; no is `--require-signature=false`, and refused after
  `--require-signature`. `EquivalentCommand` prints `--require-signature=false`
  (decision 10).
- Tests in `test/suite` for every row of the §5.2 table and the RFC's other
  cases, `test/clitest` for the flag, the wizard and the printed key.
- Docs: the install page, the `init` reference, unattended updates, offline
  installs and the command list where they show the flags. The changelog.

How the code settles what the plan left to it, recorded here rather than as
entries because none of them is a decision the RFC would carry:

- The tri-state is two booleans rather than a `*bool`, so every existing
  `InitOptions{RequireSignature: true}` keeps its meaning unchanged.
- `update` names the shipped key only when the release's signature verifies
  against it, through the same `ShippedKey` reader, so the id it names is the
  key that signed the release and not merely a file in it.
- The plan said the wizard would read an archive's key with
  `ReadFirstArchiveEntry`. That reads only an archive's first entry, the
  manifest, so the wizard and the plan extract a local archive to a temporary
  directory, as `checkPlannedRelease` already does for a plan.

```divergence
decision: 15
grade: ASSUMED
class: spec-gap
at: 2026-11-08T17:40:36Z
attempt: 1
claim: decision 15 has a repair verify against the policy of the installation it finds, and says nothing of one whose state exists but cannot be read. Today such a repair goes on as if nothing were installed and verifies against its flags alone; under decision 15 there is no policy to verify against, so the repair is refused with the error that reading the state gave.
evidence: internal/lifecycle/ops/init.go:582
action: departed
proposal: ASSUMED — refines decision 15. A repair that stages a release refuses when the installation state it finds cannot be read, rather than staging against its flags alone.
```

```divergence
decision: 13
grade: ASSUMED
kind: resolved
at: 2026-11-08T17:40:36Z
attempt: 1
claim: decision 13 says `update` prints the notice and not how often. It prints on every update, interactive or from the timer, while the installation trusts no key and the release ships one, because a notice shown once and then silenced is one an unattended machine's operator never sees.
evidence: internal/lifecycle/ops/update.go:675
action: decided
```

```divergence
decision: 9
grade: ASSUMED
class: spec-gap
at: 2026-11-08T17:40:36Z
attempt: 1
claim: decision 9 has `init` print the key it trusted. `--quiet` suppresses every callout `init` prints, the recovery key's included, and `--json` prints none. The trusted key follows the same rule: `--json` carries it in `data`, and `--quiet` says nothing.
evidence: internal/cli/render.go:70-73
action: departed
proposal: ASSUMED — refines decision 9. The printed key is a callout like the recovery key's: suppressed by `--quiet`, and carried in `data` under `--json`.
```

```divergence
decision: 1
grade: LOCKED
kind: resolved
at: 2026-11-08T17:58:11Z
attempt: 1
claim: decision 1 honoured as narrowed: only an `init` that finds no installation state, given no `--signing-key` and not `--require-signature=false`, reads the shipped key, and the write step records it with `require_signature: true`; a repair of an installation that exists never reads it
evidence: internal/lifecycle/ops/shipped_key.go:49-69
action: decided
```

## Build, 2026-11-08

Built as planned, with the entries above. The trust logic is in
`internal/lifecycle/ops/shipped_key.go`: `initTrust` works out what the stage
step checks against, `checkFirstUseKey` holds the two refusals a first use
adds, and `refusedForShippedKey` and `noteShippedKey` are `update`'s side. The
stage step reads the shipped key from its own fetched copy. The CLI reads the
tri-state through `Flags().Changed`.

One addition the plan did not list. A script that passed `--signing-key` and no
`--require-signature` now meets a refusal on an unsigned release (decision 5,
and the RFC's third risk). The verifier's refusal is worded for an installation
("clear require_signature"), so the stage step refuses first, naming
`--require-signature=false`. It is the same refusal in kinder words, not a new
rule, so it is recorded here rather than as an entry.

Two existing tests changed with the behaviour they pinned:

- `test/clitest/trust_test.go`: `TestInitStoresAKeyAsTheParserReadsIt` installs
  an unsigned fixture with a key, so it now passes `--require-signature=false`.
- `test/suite/attest_signature_test.go`:
  `TestARepairKeepsTheCheckRecordedForTheSameRelease` repaired a keyed
  installation with no flags, which under decision 15 verifies against the
  policy and records its own check. It now removes the key first, so the
  repair verifies nothing, which is the case the test is about.

## Self-audit, 2026-11-08

Scope: the branch against `8a8799d7`: `ops` (init, update, the new
`shipped_key.go`), the CLI command and wizard, tests in `test/suite`,
`test/clitest` and `internal/cli`, five doc pages, the changelog and this log.

- **Sabotage:** fifteen mutations, each run against the tests written for it,
  each red:
  - the shipped key trusted on `update`;
  - the shipped key trusted on a repair;
  - the shipped key's checks skipped at the stage step;
  - the requirement left off after trusting;
  - a repair verified against its flags alone;
  - the wizard's key id not compared;
  - `update`'s refusal not naming the shipped key;
  - `update`'s notice dropped;
  - the plan's line dropped;
  - the refusal naming `--require-signature=false` dropped;
  - a given key not implying a required signature;
  - the up-front refusal kept when a release is given;
  - `--require-signature=false` read as unset;
  - the printed key dropped;
  - the wizard's no ignored.

  Two of those first failed to compile, which counts as red without proving
  anything. They were run again in a form that builds, and went red on the
  tests they target.
- **Docs:** a search for the old claim that a key without
  `--require-signature` installs unsigned releases found it in `publishing.md`,
  now corrected, and nowhere else.
- **Residue:**
  - A plan of a local archive extracts it twice (the shortcut comment in
    `init.go`).
  - A local bundle that shipped no key when the wizard ran, and ships one when
    the stage step reads it, is trusted without having been asked about. The
    wizard approves a key by id, and there was none to approve.
  - The wizard's key check and the non-interactive path differ by design:
    `EquivalentCommand` cannot carry the approved id, so the printed command
    trusts whatever key the release ships when it runs.
  - Not run: the demos and the Docker acceptance run.

## Review, 2026-11-08

```divergence
decision: 10
grade: ASSUMED
class: spec-gap
at: 2026-11-08T18:41:08Z
attempt: 1
claim: §5.5 has the wizard pass on the id of the key it showed, and the stage step refuse a shipped key with any other id. A minisign key id is eight bytes whoever makes the key chooses, so another key can carry the approved id and pass; and a local bundle where the wizard found no key passed on nothing, so a key added before staging was trusted without being asked about. The wizard now passes on the key it showed, or that it found none, and the stage step compares whole keys.
evidence: internal/lifecycle/ops/shipped_key.go:115
action: departed
proposal: ASSUMED — refines decision 10. The wizard passes on the whole key it showed for a local bundle, or that it found none, and the stage step refuses any other key, including one where the wizard found none.
```

Findings from the first review round, each reproduced red before its fix:

- **Fixed in 7416e2c2: the wizard approved a key by its id.** The entry above.
  A test makes a second key with the approved key's id and ships it; it was
  trusted. And the wizard, finding no key in a local bundle, held the stage
  step to nothing. Both are refused now. This also closes the residue the
  self-audit listed.
- **Fixed in 7271acc5: a repair read the policy before the lock.** A test
  removes a key while the repair takes the lock, through a locker that runs
  the change first; the repair verified against the removed key. The stage
  step now reads the policy under the lock. Decision 15's entry above still
  holds: state that cannot be read is refused, now by the stage step.
- **Fixed in 92daf89e: `update` named the shipped key whatever else was
  wrong.** A release signed by an untrusted key it ships, and tampered after
  signing, was refused as signed by an untrusted key. The refusal now names
  the key only when the release verifies against it in every other respect.
- **Fixed in fb60aa2a: `unattended-updates.md`** said a release that ships its key
  meets the auto-apply requirement. Only where `init` trusted it.

## Reconciliation, 2026-11-08

- **The decision 13 entry was wrong about the timer.** It says the notice
  prints on every update "interactive or from the timer". The timer never
  prints it. An installation that trusts no key cannot turn on
  `update.auto_apply`, which needs `release.require_signature`
  (`internal/domain/installation.go:1151`), which needs a key
  (`internal/domain/installation.go:1197`). The timer's staging, in
  `FollowChannel`, does not run `update`'s verify step, the only caller of
  `noteShippedKey` (`internal/lifecycle/ops/update.go:703`). RFC 0052's row 20
  says so.
