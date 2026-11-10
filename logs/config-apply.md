# config-apply · RFC 0056 P1 and P2

On branch `config-apply` from `main` at `3c026b4d`, building
[RFC 0056](../rfcs/0056-one-change-to-settings-parameters-and-secrets.md) in
full: P1 (§5.1–§5.6, decisions 1–12 and 16, `SecretStore.Restore` with its
contract leg, the docs in §7, the tests in §6) and P2 (`config set`,
`config unset` and `config edit` of a parameter as callers, decision 14), as
ruled by the author 2026-11-10 (decisions 13, 14 and 15 as recommended).
Refs #328.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## Plan

Domain:

- `internal/domain/change.go` (new): `Change`, the document's body, and
  `ParseChange(data)`. It decodes strictly, with goccy/go-yaml's parser
  and AST, so that a scalar keeps its source text (decision 4): `9000`,
  `"9000"` and `yes` are each the text written. A list or a map as a value,
  an unknown top-level key, and an `unset` key other than the three lists
  are refused. A secret value loses exactly one trailing newline. The body
  lives in the domain so that RFC 0057's sync parses the same body.

Ports and adapters:

- `internal/ports/secrets.go`: `Restore(ctx, name, value, changedAt)` on
  `SecretStore` (§5.5, decision 7).
- `internal/adapters/secrets/sopsage/sopsage.go`: `Restore`, which is `Set`
  with the given `changed_at`, under the same store lock.
- `test/fakes/fakes.go`: the fake's `Restore`, with its failure switch.
- `test/contract/secretstore.go`: a leg saying a restored secret has its
  value and its `LastChanged` back, run against the fake and against
  sops-age.

Operations:

- `internal/lifecycle/ops/config_apply.go` (new): `ConfigApply(ctx, d,
  ConfigApplyOptions{Options, Change})`, one `config` operation under the
  deployment lock (decision 1).
  - **Validation** (decision 5) runs before the lock and again under it.
    Every problem is collected into one refusal:
    - each setting through its own `Apply` on a copy, then
      `checkSignaturePolicy`;
    - each parameter through its type's `Parse`, then `mergeParameters`
      for the cross-checks it already makes (resolution, missing
      defaults, stale unsets);
    - each secret against the release's schema, refusing an undeclared
      name, an empty value, and the unset of a required one without
      `--force`;
    - a name both set and unset.
  - **The plan** (§5.3, decision 6) builds the before and after
    installations:
    - a profile change on a running product reuses `planSettings`'s path,
      with the secret stage inserted after the restore point (decision 9);
    - otherwise, while running, one re-create of the union, kept to
      `startedServices` for the installation after the change (#329). The
      union is every running service on a domain change, plus the
      parameters' declared services, plus the secrets' dependents;
    - a change of secrets alone restarts the dependents that are started;
    - while stopped, or when nothing runs, nothing is converged.
  - **Steps** (§5.4):
    - `record-installation`: settings and parameters in one write;
    - `stage-secrets`: reads previous values and `changed_at`, sets or
      removes each name. Undo: `Restore` or `Remove` per name, then render
      the secret files again if the directory exists (decisions 7, 9);
    - `load-secrets` and `render-secrets`: `apply`'s steps;
    - `render-configuration`: `apply`'s step;
    - `converge`: the re-create or the restart. Undo: secrets, secret
      files and configuration files back, then re-create with the
      previous installation (decision 9);
    - `health-checks`: `apply`'s step (decision 8).

    Each step appears only when the change needs it (§5.4's last
    paragraph).
  - **Flags** name the changed names by section, never a value, plus
    `settingsFlags`'s downgrade record (decision 11).
  - **Afterwards** come one notification and the `config` statement
    (decision 11), then `refreshUnits` and `retireMetricsFiles` for the
    settings, after the operation succeeds (decision 12).
  - **The summary** names the changes by section and what was re-created
    or restarted, in the tense of what happened. On a failure it claims
    nothing (#329's rule).
- `internal/lifecycle/ops/config.go` (P2, decision 14): `ConfigSet` becomes
  a caller of the same operation with a parameters-only change. It keeps its
  summary (`configSummary`), its `Data` and its dry-run diff. It gains the
  health checks. `configSteps` and the parts of the old pipeline that nothing
  else uses go.
- `internal/lifecycle/ops/settings.go`: the per-setting apply and clear
  loops are factored out of `SetSettings` so that `ConfigApply` validates
  settings the same way. `SetSettings` keeps its behaviour, since a setting
  through `config set` stays out of the operation (§5.7).

CLI:

- `internal/cli/config.go`: `config apply`, which reads stdin only
  (decision 2), bounded at 1 MiB with the existing `readAll`
  (decision 4). It refuses a terminal on stdin, because a document is not
  typed at a prompt. `--dry-run` and `--force` come from the global flags.
  `config set` keeps refusing a setting and a parameter in one call, with a
  hint naming `config apply`.

Docs and changelog (§7):

- `pages/docs/reference/commands.md`: `config apply`, its document and its
  refusals.
- `pages/docs/operating/changing-configuration.md`: a how-to for a change
  that spans settings, parameters and secrets, with the OpenVPN switch,
  and when `config set` and `secret set` are still the simpler tool. It
  also says that `config set` now waits for the health checks.
- `pages/docs/reference/secret-commands.md`: a pointer for a secret that
  changes with other names.
- `pages/docs/reference/parameters.md`: the document's sections and the
  scalar-as-text rule.
- `pages/docs/explanation/secrets.md`: the change document is one more place
  plaintext passes through, held in memory and never written to disk by the
  manager.
- `CHANGELOG.md` Unreleased:
  - Added: `config apply`;
  - Changed: `config set` of a parameter runs the release's health checks.

Tests (§6):

- Domain: `ParseChange`, for strictness, scalars as text, block scalars,
  the trailing newline, and each refused shape.
- Contract: the `Restore` leg.
- Suite (`test/suite/config_apply_test.go`):
  - each step made to fail in turn, after which the installation, the
    store, the rendered configuration and secret files, and the last `Up`
    match the state before the change;
  - the dotdash switch: a profile, a parameter and a secret in one
    operation and one health check, and the same document failing its
    health checks with the old profiles left running;
  - the undo order:
    - a failure at the configuration render leaves store and files old;
    - a failed profile change brings the old services up on the old
      secret files;
  - `changed_at` kept on rollback;
  - a concurrent `secret set` of another name surviving a rollback;
  - no secret value in the plan, summary, journal, attestation or
    notification;
  - one case per item of §5.3, each with a dependent behind an inactive
    profile;
  - every refusal in §5.2, and a document with several problems.
- P2: the existing `config set` tests pass unchanged except where they
  assert the step list. `config set` now ends with health checks, and a
  failing check undoes the change.
- CLI: `config apply` from a pipe, refused at a terminal, refused over 1 MiB.
- Sabotage: §6's six, each run and seen red.

Decisions the spec does not settle:

- **Containers exist but none runs, with no stop record** (spec-gap). §5.3
  covers running, stopped, and nothing created. `config set` converges only
  when something runs (`projectRunning`). `secret set` renders and restarts
  whenever any container exists. Proposed: converge only when something
  runs, as `config set` does, since this is its operation; render the secret
  files whenever any container exists, as `secret set` does, so a container
  that comes back reads the new files. Not load-bearing.
- **The attestation for a change that renders no configuration** (spec-gap).
  Decision 11 says the operation files `config set`'s statement, but
  `attestConfig` is defined for an operation that rendered the configuration,
  and its digest is over that render. A change of secrets alone, or of
  settings that reach no service, renders none. Proposed: file the statement
  only when the configuration was rendered, as `SetSettings` already does
  (`plan.render`). Not load-bearing.
- **Unsetting a secret the release no longer declares** (spec-gap). §5.2
  refuses setting an undeclared secret but says nothing of unsetting one that
  is stored. Proposed: allowed when the name is stored, as `mergeParameters`
  allows unsetting a stale parameter, so a document can clean up after a
  release that dropped a secret. Not load-bearing.
- **A terminal on stdin** (unlisted). Proposed: refused with a hint to pipe
  or redirect the document. Not load-bearing.

Readiness gate: zero load-bearing gaps.

## Build

```divergence
decision: 6
grade: ASSUMED
class: spec-gap
at: 2026-11-10T15:25:04Z
attempt: 1
claim: §5.3 covers a running product, a stopped one and one with nothing created, but not containers that exist with none running and no stop record. config set converges only when something runs (projectRunning, apply.go:335); secret set renders whenever any container exists and restarts.
evidence: internal/cli/secret.go:482
action: departed
proposal: ASSUMED — config apply converges only when something runs, as config set does, and renders the secret files whenever any container exists, as secret set does, so a container that comes back reads the new files
```

```divergence
decision: 11
grade: ASSUMED
class: spec-gap
at: 2026-11-10T15:25:04Z
attempt: 1
claim: decision 11 has the operation file config set's statement, but attestConfig is defined for an operation that rendered the configuration and digests that render. A change of secrets alone, or of settings that reach no service, renders none.
evidence: internal/lifecycle/ops/settings.go:776
action: departed
proposal: ASSUMED — config apply files the config statement only when it rendered the configuration, as SetSettings does
```

```divergence
decision: 5
grade: ASSUMED
class: spec-gap
at: 2026-11-10T15:25:04Z
attempt: 1
claim: §5.2 refuses setting a secret the release does not declare and says nothing of unsetting one that is stored, which is how a document would clean up after a release that dropped a secret.
evidence: internal/lifecycle/ops/config.go:302
action: departed
proposal: ASSUMED — unset.secrets may name a secret the release does not declare when it is stored, as config unset may name a stale parameter; a name neither declared nor stored is refused as a typo
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-10T15:25:04Z
attempt: 1
claim: the spec does not say what config apply does when stdin is a terminal; reading would wait for an end of file nobody types.
evidence: internal/cli/commands.go:1270
action: decided
proposal: ASSUMED — config apply refuses a terminal on stdin, with a hint to pipe or redirect the document
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-10T15:49:06Z
attempt: 1
claim: §7 puts the new command's reference in commands.md, but commands.md documents no config command; config set, unset and edit are documented in parameters.md, and commands.md carries only their completion row.
evidence: pages/docs/reference/commands.md:1042
action: decided
proposal: ASSUMED — config apply's reference goes in parameters.md beside config set, and commands.md's completion table gains its row
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-10T15:50:01Z
attempt: 1
claim: the entry above said commands.md's completion table would gain a row for config apply. That table lists completions for arguments, and config apply takes none, so it gets no row; parameters.md carries its reference alone.
evidence: pages/docs/reference/commands.md:1036
action: decided
proposal: ASSUMED — config apply is documented in parameters.md and changing-configuration.md, with no row in the completion table
```
