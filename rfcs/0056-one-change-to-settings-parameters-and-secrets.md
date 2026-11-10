# RFC 0056 — One change to settings, parameters and secrets

- **Status:** 📝 Draft — proposed 2026-11-10, and ruled by the author
  2026-11-10: decisions 13, 14 and 15 as recommended.
- **Scope:** Adds one operation that changes installation settings, operator
  parameters and secrets together. It reads a change document on stdin and
  records, renders, converges and health-checks once. If any step fails, it
  puts every previous value back. It covers:
  - the document and how it is validated;
  - which services the change touches;
  - the operation's steps and how each one is undone;
  - how a secret is put back without looking freshly rotated;
  - what `--dry-run`, the journal and the attestation show;
  - `config set` of a parameter as a one-name caller of the new operation,
    and why the `secret` commands stay as they are.

  **Contract changes:**
  - a new command, `config apply`;
  - the `SecretStore` port gains a write that keeps a given `changed_at`.

  Neither the manifest, nor the installation schema, nor the hook ABI changes.
  It does not fetch a change from anywhere. That is
  [RFC 0027](0027-desired-state-in-a-repository.md) decision 5, and a design
  that pulls a signed change set has to reopen it. It does not make the
  document a desired state. It does not generate secrets, change recipients
  or move the release version. §8 names each of these.
- **Related:**
  - code:
    - [`internal/lifecycle/ops/config.go`](../internal/lifecycle/ops/config.go)
      (`ConfigSet`, `configSteps`, `stepRecreateServices`);
    - [`internal/lifecycle/ops/settings.go`](../internal/lifecycle/ops/settings.go)
      (`SetSettings`, `planSettings`, `journalled`);
    - [`internal/cli/secret.go`](../internal/cli/secret.go) (`secret set`,
      `restartDependents`) and
      [`internal/cli/secret_edit.go`](../internal/cli/secret_edit.go);
    - [`internal/ports/secrets.go`](../internal/ports/secrets.go) and
      [`internal/adapters/secrets/sopsage/sopsage.go`](../internal/adapters/secrets/sopsage/sopsage.go);
  - RFCs:
    - [0003](0003-secrets-recovery-and-onboarding.md) (secrets and the argv
      rule);
    - [0007](0007-operator-parameters.md) (parameters);
    - [0025](0025-attesting-an-installation.md) (attestation);
    - [0027](0027-desired-state-in-a-repository.md) (desired state, and the
      boundary this stays inside);
    - [0040](0040-keeping-an-installation-down.md) (stop);
    - [0042](0042-settings-an-operator-changes-after-init.md) (settings);
    - [0050](0050-restarting-a-service-and-an-interactive-exec.md)
      (`refuseUnstartedServices`);
    - [0053](0053-several-profiles-per-installation.md) (profiles).
- **Discussion:** [#328](https://github.com/morzecrew/morzer/issues/328).
  Its bug half, parameters re-creating a service behind an inactive profile,
  is fixed separately in
  [#329](https://github.com/morzecrew/morzer/pull/329).

---

## 1. Summary

An operator can make one change, such as dotdash's switch to an OpenVPN
upstream, as one command:

```sh
morzer config apply < switch-to-openvpn.yaml
```

The document names settings, parameters and secrets to set, and names to
unset. It is validated whole before the deployment lock is taken. Under the
lock, one `config` operation records the settings and parameters, stores the
secrets, renders the configuration and the secret files once, and converges
the affected services once. It then runs the release's health checks. If any
step fails, every value is put back, the files are rendered again and the
services are converged again. A secret goes back with its previous
`changed_at`, so `doctor` does not read a rollback as a rotation.

`config set` of a parameter becomes a one-name caller of the same operation,
and so gains the release's health checks (decision 14). `config set` of a
setting, and the `secret` commands, keep working as they do (decision 15).

## 2. Motivation

**Three operations where there is one change.** dotdash's switch to an
OpenVPN upstream (morzecrew/dotdash#63) takes three commands today, in an
order that cannot change:

1. `secret set upstream_openvpn`, while the `openvpn` profile is still off.
2. `config set installation.profile=xdns,openvpn`, which starts `vpn` while
   the gate still dials the proxy.
3. `config set upstream_protocol=openvpn`, which moves the gate to the
   tunnel.

Each command is its own operation, passes its own checks, or unwinds on its
own. So every state in between has to work on its own. The bundle therefore
carries both upstreams' wiring at once, and its docs spell out an order the
operator must follow. Going back runs the steps in reverse. As one operation,
the switch is one plan, one render, one converge, one health check, and one
unwind if it fails.

**`config set` refuses to mix the two kinds.** A setting and a parameter in
one call is refused outright, with `parameters and installation settings are
set separately` ([`internal/cli/config.go:186`](../internal/cli/config.go)).
The comment there gives the reason: the two have different machinery, so a
mixed command would half-apply on a failure with no single thing to report.
The reason is right about today's machinery. This RFC gives the mixed change
one machinery.

**More products change several names at once.** Products that come as
several bundles change several names together more often. A desired-state
design, in which an installation applies a signed change set to itself, is
being drafted. It needs this operation underneath: a change from elsewhere
has to apply whole or not at all.

## 3. Current state

Verified against `main` at `d83a3ed4`.

**Three entry points, three kinds of machinery.**

- *Parameters.* `ConfigSet` (`config.go:126`) is a journalled `config`
  operation under the deployment lock. Its steps (`configSteps`,
  `config.go:351`) are three:
  - record the parameters, undone by saving the previous installation;
  - render the configuration;
  - re-create the services the changed parameters declare, with
    `up --force-recreate` (`stepRecreateServices`, `config.go:417`).

  It runs no release health check. The re-create's `up --wait` is the only
  wait. It is attested (`attestConfig`).
- *Settings.* `SetSettings` (`settings.go:625`) is not an engine operation.
  It takes the deployment lock and saves the installation. Three changes are
  the exception and run as a journalled `config` operation (`journalled`,
  `settings.go:792`):
  - a domain change renders and re-creates every running service;
  - a profile change runs `apply`'s steps under the new profiles, behind a
    restore point (`planSettings`, `settings.go:845`);
  - turning off the signature requirement, a downgrade an incident review
    has to be able to find (RFC 0042 decisions 5 and 9).

  A setting that changes the supervisor's units reconciles them after the
  save.
- *Secrets.* `secret set` (`secret.go:94`) is not an operation at all. It
  takes no deployment lock and writes no journal record. It stores the value
  under the store's own `secrets` lock (`sopsage.go:74-77`). It then renders
  and restarts the secret's declared services (`restartDependents`,
  `secret.go:101`). `secret edit` changes several secrets in one editor
  session, but calls `Set` or `Remove` once per name
  (`secret_edit.go:324-329`), so a failure part way leaves some names changed.

**What each one restarts.** A parameter reaches a container as part of its
definition, so it needs `up --force-recreate`. A secret reaches it as a
mounted file, so `restart` re-reads it. A secret's restart keeps to the
services the active profiles start (#325). #329 does the same for a
parameter's re-create. It moves the lookup that `refuseUnstartedServices`
(`restart.go:160`) does for `morzer restart` into one helper,
`startedServices`. A service only an inactive profile's overlay defines is
unknown to Compose, which refuses it. A service behind an inactive Compose
`profiles:` entry is started when it is named. Both were measured on Compose
5.5.1 for #325 and #329.

**Undo lives in memory.** A render keeps each file it replaces in the
operation's state (`configBackup`, `apply.go:766`). A failed step's undo puts
them back (`restoreConfigBackups`, `apply.go:720`). The engine undoes the
failed step first. So when the re-create fails, its own undo runs while the
installation and the files still hold the new values. `stepRecreateServices`
therefore puts the files back itself before it re-creates with the previous
installation (#239). A killed manager loses that state. Its unfinished record
then blocks the next operation (`gateUnfinished`, `apply.go:1408`).

**A secret's change time is stamped on every write.** `Set` records
`changed_at` as now (`sopsage.go:336`), and `doctor`'s rotation age reads it.
Putting an old value back through `Set` would make a rolled-back credential
look freshly rotated.

**Reading a secret value.** A piped value is read whole, bounded at 1 MiB,
with exactly one trailing newline stripped (`readSecretValue`,
`commands.go:1261`). That is what `echo value | morzer secret set x`
produces. There is no flag for a value, because argv is world-readable
through `/proc` (`secret.go:72-74`). RFC 0003 keeps values off `sops`'s argv
for the same reason.

**RFC 0027's boundary.** Its decision 2 says a committable desired-state file
cannot hold a secret value. Decision 3 says a future `apply -f` is the
existing operation with a different argument source, or it is not built.
Decision 5 rules out any pull, watch or schedule.

## 4. Goals / Non-goals

**Goals**

- A change to settings, parameters and secrets applies whole or not at all.
  An earlier step's success is never left standing after a later step fails.
- One render, one converge and one health check per change.
- A secret value never reaches argv, the journal, the plan or an
  attestation.
- A rollback leaves no trace on a secret's metadata.
- The services touched are the union of what each part of the change needs.
  Of those, only the ones the configuration after the change starts are
  touched.

**Non-goals**

- **A desired state.** The document names a change, not an installation. It
  is not produced by `installation describe` and is not round-tripped. RFC
  0027 decisions 2 and 3 stand.
- **Fetching a change.** The operation reads stdin. Pulling, verifying and
  scheduling a change set belong to the design that needs them, which has to
  reopen RFC 0027 decision 5.
- **Atomicity across a killed manager.** A step that fails is undone. A
  process that is killed leaves an unfinished record, as every operation does
  (§9).

## 5. Design

### 5.1 The document

One YAML document on stdin:

```yaml
settings:
  installation.profile: xdns,openvpn
parameters:
  upstream_protocol: openvpn
secrets:
  upstream_openvpn: |
    client
    remote vpn.example 1194
unset:
  settings: []
  parameters: [legacy_proxy]
  secrets: [upstream_socks_password]
```

- **Stdin only.** There is no path argument and no URL. `< file` and a pipe
  both work, and a value never reaches argv (decision 2).
- **Strict decode.** An unknown top-level key, or a key under `unset` other
  than the three lists, is refused. So is a value that is a list or a map.
- **Scalars are their source text.** `http_port: 9000` and
  `http_port: "9000"` set the same value, and `yes` stays `yes`. The value is
  then parsed by the parameter's own type, as `config set` parses it. A
  `text` parameter takes a block scalar and is normalised as RFC 0054
  decision 2 says.
- **A secret value loses exactly one trailing newline**, as a piped
  `secret set` value does. So `|` gives the same bytes as
  `printf '%s\n' "$v" | morzer secret set`.
- **Bounded at 1 MiB in total**, the bound a piped secret already has.
- **Nothing to change** is reported as no change, and no operation runs.

### 5.2 Validation

Everything is checked before the lock, and every problem is reported in one
refusal, so a document is fixed in one pass. Each check runs again under the
lock against the installation as it stands then, as `config set` and
`SetSettings` already do.

- A setting name `config set` does not know is refused. So is a value its
  setting refuses, including turning off the signature requirement against
  the policy (`checkSignaturePolicy`).
- A parameter the release does not declare is refused, as is a value its
  type refuses.
- **A secret the release does not declare is refused.** `secret set` stores
  one, which is never rendered. In a change document such a name is likelier
  to be a typo, and a typo stored silently is a credential the operator
  believes is in force.
- An empty secret value is refused, as `secret set` refuses it. Unsetting is
  `unset.secrets`.
- Unsetting a secret the release declares `required` is refused without
  `--force`, as `secret remove` refuses it.
- A name both set and unset in one document is refused.

### 5.3 What the change touches

The plan depends on the installation before and after the change. Each case
below is what one of today's commands already does for its own part.

- **The profiles change and something is running.** The steps are `apply`'s
  under the new profiles, behind a restore point, as `planSettings` builds
  them. Every service is re-created, which covers every parameter and secret
  in the change.
- **Otherwise, while something is running**, the services re-created with
  `up --force-recreate` are the union of:
  - every running service, if the domains change (RFC 0042);
  - the services the changed parameters declare;
  - the dependents of the changed secrets.

  Only the ones `startedServices` lists for the installation after the change
  are kept (#325, #329). Re-creating a secret's dependent re-reads its
  mounted file as a restart would.
- **A change that holds only secrets** restarts their dependents, as
  `secret set` does, and does not re-create them.
- **While the product is stopped**, the change is recorded, stored and
  rendered, and nothing is re-created or restarted. `morzer start` brings it
  up with the new values (RFC 0040).
- **When nothing has been created yet**, the configuration is rendered and
  the secrets are not. This is how `config set` and `secret set` behave
  today. A render of the secrets would refuse over every required secret not
  set yet.

### 5.4 The steps

Without a profile change, on a running product:

1. **record installation.** The settings and parameters are saved in one
   write. Undo: save the previous installation.
2. **stage secrets.** First the previous value and `changed_at` of every
   name the change touches are read into the operation's state. Then each
   name is set or removed. Undo: put each name back (§5.5), then render the
   secret files again from the restored store if they exist. The render is
   part of this undo because `apply`'s render-secrets undo does not put
   files back: it keeps them while the product runs and removes the
   directory when nothing runs (`apply.go:467-473`). Without it, a failure
   at step 4 would leave the store old and the rendered files new.
3. **render secrets.** This is `apply`'s step, with its undo.
4. **render configuration.** This is `apply`'s step. It keeps a backup of
   each file it replaces.
5. **re-create services**, or restart them for a change that holds only
   secrets. Undo: put back the secret values (§5.5), the rendered secret
   files and the configuration files, then re-create with the previous
   installation. The engine undoes the failed step first, so this step's undo
   cannot rely on the steps before it having been undone (#239).
6. **health checks.** This is `apply`'s step. A failure undoes everything
   before it.

With a profile change, the steps are those of `planSettings`, in this order:
step 1, then the restore point, then step 2, then `apply`'s steps under the
new profiles. Step 2 comes after the restore point so that its undo runs
first. The restore point's undo re-renders the secrets from the store and
brings the old profiles' services up (`restorePreviousRuntime`,
`update.go`). With the store not yet put back, those services would start
with the new secrets. While the
product is stopped, or nothing runs, steps 5 and 6 are left out. They are
also left out when the change reaches no service, such as a change of
settings alone that touches neither the domains nor the profiles. Steps 2 and
3 run only for a change that holds secrets.

A setting that changes the supervisor's units reconciles them after the
operation succeeds, as `SetSettings` does. A failure there comes after the
commit and is reported as it is today. It is not undone.

### 5.5 Putting a secret back

Each name the change touched is written back with its previous value and its
previous `changed_at`. A name that did not exist before is removed. This
needs one new port method, a write that takes the timestamp to record:

```go
// Restore puts a secret back as it was: the value, and the change time
// recorded with it. Not Set, which stamps now and would make a rolled-back
// credential read as freshly rotated.
Restore(ctx context.Context, name string, value domain.Secret, changedAt time.Time) error
```

It is not a byte-for-byte restore of the encrypted file. `secret set` takes
only the store's own lock, so it can land while this operation runs. A
restore of the whole file would quietly undo that change to a different name.
Putting back each name this change touched leaves every other name as it is.
If a concurrent `secret set` changes the same name, the rollback wins. That
is last-writer-wins, the rule `config set` and `secret edit` already follow
(RFC 0054 decision 8).

### 5.6 What the operator sees

- **`--dry-run`** prints the step list. It also prints the configuration
  diff that `config set --dry-run` prints. A secret appears by name, marked
  `set` or `removed`, never with a value, a length or a fingerprint.
- **The summary** names the changed names by section, then what was
  re-created or restarted. It is in the tense of what happened, as
  `configSummary` already is.
- **The journal record** names every changed setting, parameter and secret in
  its flags, by section, and never a value.
- **The attestation** is the `config` statement `config set` files, with
  its schema unchanged (RFC 0025). v3's `config` holds parameter names and a
  rendered digest (`AttestedConfig`, `attestation.go:219`). Each schema
  version refuses a field it does not name (`attestation.go:39-44`), so
  secret names stay out of it. They are in the journal record. A statement
  that names them is a v4 predicate (§8).
- **One notification** is sent for the operation.
- **Exit codes** are the ones `config set` returns: compensated on an
  undone failure, and manual intervention when an undo fails.

### 5.7 `config set` and the `secret` commands as callers

`config set <parameter>=value` is a change with one entry, so it becomes a
caller of this operation. That gives one implementation instead of two, and
gives `config set` the release's health checks, which it does not run today
(decision 14). `config unset` of a parameter and `config edit` reach it
through the same `ConfigSet` (`config.go:250`, `config_edit.go:56`).

`config set <setting>=value` keeps `SetSettings`. Most settings change what
the manager does next and reach no service, which is why `SetSettings` is
deliberately not an engine operation (`settings.go:611`). The three that
reach the deployment or the trust policy are journalled as they are today.

The `secret` commands stay as they are (decision 15). As callers they would
take the deployment lock. They would then wait behind an update and be
refused while an operation is unfinished. They would also become journalled.
That changes when an operator can rotate a credential during an incident,
which is exactly when an unfinished operation is likeliest.

### 5.8 Alternatives considered

- **Lift `config set`'s refusal and run its two machineries in sequence.**
  This is the half-apply the refusal exists to prevent: a parameter recorded
  and its services re-created, and then a setting that fails, with no single
  record to report.
- **A `--secret-file name=path` flag on `config set`.** This keeps argv free
  of the value, but a path names a file the operator must create, protect
  and remove. A change that holds several secrets becomes several files.
  stdin already carries a value safely, and one document holds the whole
  change.
- **Restore the encrypted state file byte for byte.** This is simpler, and it
  keeps `changed_at` by construction. But it undoes any `secret set` that
  landed meanwhile under the store's own lock (§5.5). It would be correct
  only if every secret write took the deployment lock, which is decision 15.
- **Extend `morzer apply` with a change argument.** `apply` converges the
  installation as recorded and takes no values. RFC 0027 decision 3 reserves
  `apply -f` for a desired state, which is a different document with a
  different rule on secrets.

## 6. Tests

- **Whole or nothing, step by step.** The fault-injection loop the suite
  already runs for `apply` makes each step fail in turn. After each failure,
  the installation, the stored secrets, the rendered configuration and secret
  files, and the services' last `Up` match the state before the change.
- **The dotdash switch.** One document that changes the profiles, a
  parameter and a secret produces one operation, one record and one health
  check. The services come up under the new profiles. The same document with
  the health checks failing leaves the old profiles running with the old
  values.
- **The undo order.** A failure at the configuration render leaves the
  store and the rendered secret files both old. A profile change that fails
  its health checks brings the old profiles' services up reading the old
  secret files.
- **A rollback keeps `changed_at`.** A secret's `LastChanged` after a failed
  change equals its value before the change. This is a `SecretStore`
  contract leg, run against the fake and against sops-age.
- **A concurrent `secret set` survives a rollback.** A `secret set` of
  another name lands between the stage and a failure. It is still in force
  after the undo.
- **Nothing leaks.** A secret value appears in neither the plan nor the
  summary, journal, attestation or notification. Each is scanned as
  `TestAConfigChangeFilesAStatement` (`attest_ops_test.go:81`) scans a
  statement for a parameter value, with `leakscan_test.go`'s digest
  blanking.
- **The services touched.** One case for each item of §5.3. Each runs with a
  dependent behind an inactive profile, which is left out.
- **Validation.** Every refusal in §5.2 is checked, and so is a document
  with several problems, which reports all of them at once.
- **Sabotage.** Each of these must turn the suite red:
  - drop the secret undo from step 5;
  - drop the render from the secret stage's undo;
  - stage the secrets before the restore point;
  - put a secret back with `Set`;
  - restore the encrypted file whole;
  - leave the health checks out.

## 7. Docs

- `commands.md`: the new command, its document and its refusals.
- `changing-configuration.md`: a how-to for a change that spans settings,
  parameters and secrets, with the OpenVPN switch as the example. It also
  says when `config set` and `secret set` are still the simpler tool.
- `secret-commands.md`: a pointer for a secret that changes together with
  other names.
- `parameters.md`: the document's parameter and setting sections, with the
  scalar-as-text rule.
- `pages/docs/explanation/secrets.md`: the change document is one more place
  plaintext passes through. It is held in the manager's memory during the
  operation and never written to disk by the manager.

## 8. Out of scope

- **Fetching, verifying or scheduling a change.** RFC 0027 decision 5. The
  desired-state design reopens it and builds on this operation.
- **Generated secrets in the document.** `secret generate` and `rotate` stay
  their own commands. A generated value would have to be reported to the
  operator, and that has no place in a change that may be scripted.
- **The release version.** A change that also updates the release is
  `morzer update`, which has its own plan and rollback.
- **Recipients.** Adding or removing one re-encrypts the state and is not a
  change to the deployment.
- **Secret names in the attestation.** The v3 `config` predicate has no
  field for them and refuses one it does not name. Naming them is a v4
  predicate, with a schema an auditor validates against, and belongs to RFC
  0025.
- **Several documents, or several installations, in one call.**

## 9. Risks

- **A killed manager is not covered.** A step failure is undone. A kill is
  not, because the undo state lives in memory, as for every operation (§3).
  The unfinished record holds the next operation until the operator acts.
  Accepted: the alternative, persisting secret values for undo, puts
  plaintext on disk.
- **Documents holding secret values end up in files.** An operator who keeps
  `switch.yaml` with a credential in it has a credential in a file. The docs
  say to pipe the value from where it lives, such as a password manager or a
  file already protected, rather than to keep the change document.
- **A large union is an outage.** A change that touches the domains or the
  profiles re-creates every service, as each of those does alone today. The
  plan shows it before anything runs.
- **Health checks add time.** A change waits for the release's checks, which
  `config set` does not today. As a caller (decision 14), `config set` waits
  too.
- **Read as `morzer apply`.** `config apply` sits under `config`, and its help
  and reference say it applies a change document, not the installation. The
  name was ruled with this risk named (decision 13).

## 10. Unresolved questions

#330's three open questions were decisions 13, 14 and 15. The author ruled
all three as recommended on 2026-11-10. Implementation is free to settle the
exact plan text and the order of validation messages.

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `LOCKED` | A change to settings, parameters and secrets is one `config` engine operation under the deployment lock. It records once, renders once, converges once and health-checks once, and a failed step undoes every value in it. This is the point of the design, and the desired-state work builds on it. Consequence: a step that cannot be undone cannot join the operation. |
| 2 | `LOCKED` | The document comes from stdin only. There is no path argument, no URL and no fetch. A secret value never reaches argv, as `secret set` already guarantees, and nothing here touches RFC 0027 decision 5. |
| 3 | `LOCKED` | The document is a change, not a desired state. It names only what changes, may carry secret values, is not produced by `installation describe`, and is not round-tripped. RFC 0027 decisions 2 and 3 stand unchanged. |
| 4 | `ASSUMED` | The document has `settings`, `parameters` and `secrets` maps and an `unset` block of three lists. It is decoded strictly. A scalar is taken as its source text and parsed by its own type. A secret value loses exactly one trailing newline, as a piped `secret set` value does. The whole document is bounded at 1 MiB (§5.1). |
| 5 | `ASSUMED` | Validation runs before the lock and again under it, and reports every problem at once. A secret the release does not declare is refused, unlike `secret set`. So are an empty secret value and the removal of a required secret without `--force`, as `secret set` and `secret remove` refuse them (§5.2). |
| 6 | `ASSUMED` | What is touched (§5.3): a profile change runs `apply`'s steps behind a restore point. Otherwise, while running, the union of every running service on a domain change, the parameters' declared services and the secrets' dependents is kept to what `startedServices` lists after the change, and re-created. A change of secrets alone restarts. While stopped, or before anything is created, nothing is converged. |
| 7 | `ASSUMED` | A secret is put back by name, with its previous value and `changed_at`, through a new `SecretStore.Restore`. A name that was absent is removed. It is not a byte restore of the encrypted file, which would undo a concurrent `secret set` of another name (§5.5). |
| 8 | `ASSUMED` | The release's health checks run after the converge, and their failure undoes the change. `config set` today relies on `up --wait` alone. |
| 9 | `ASSUMED` | The re-create's undo puts back the secrets, the rendered secret files and the configuration files before it re-creates with the previous installation. The engine undoes the failed step first, as `stepRecreateServices` already handles for configuration files (#239). The secret stage's own undo also renders the files again, since `apply`'s render-secrets undo never puts them back. On a profile change the stage comes after the restore point, so its undo runs before the restore point brings the old services up (§5.4). |
| 10 | `ASSUMED` | `--dry-run` prints the steps and the configuration diff. A secret appears by name with `set` or `removed`, never a value, a length or a fingerprint. |
| 11 | `ASSUMED` | The journal record names every changed name by section in its flags, never a value. The operation files the `config` statement `config set` files, with the v3 predicate unchanged, so secret names are in the journal and not the attestation (§5.6). It sends one notification. |
| 12 | `ASSUMED` | A setting that changes the supervisor's units reconciles them after the operation succeeds, as `SetSettings` does. A failure there is reported, not undone. |
| 13 | `ASSUMED` | The command is `config apply`, because it is a `config` operation and reads as one. Rejected: `config change`, and an `apply` flag, which RFC 0027 decision 3 reserves for a desired state. Risk: read as `morzer apply` (§9). Was `OPEN`; ruled by the author 2026-11-10: as recommended. |
| 14 | `ASSUMED` | `config set` of a parameter becomes a one-name caller of this operation, as P2, and with it `config unset` of a parameter and `config edit`. That gives one implementation, and `config set` gains the health checks of decision 8. It also makes `config set` slower by the time the checks take. `config set` of a setting keeps `SetSettings`, which is deliberately not an engine operation for a setting that reaches no service (§5.7). Was `OPEN`; ruled by the author 2026-11-10: as recommended. |
| 15 | `ASSUMED` | `secret set`, `generate`, `rotate` and `edit` do not become callers, for now. They would take the deployment lock, wait behind an update, be refused while an operation is unfinished, and become journalled. That changes when a credential can be rotated during an incident. Revisit if the desired-state design needs secret changes journalled. Was `OPEN`; ruled by the author 2026-11-10: as recommended. |
| 16 | `ASSUMED` | A killed manager leaves an unfinished record that `gateUnfinished` holds, as every operation does. Whole-or-nothing covers a failed step, not a kill, because undo state lives in memory and persisting it would put secret values on disk (§9). |

## 12. Phasing

- **#329, independent and first.** `config set` keeps its re-create to the
  services the active profiles start. It is the bug half of #328 and needs
  none of this.
- **P1.** The operation and its command: decisions 1–12 and 16,
  `SecretStore.Restore` with its contract leg, and the docs in §7.
- **P2.** `config set`, `config unset` and `config edit` of a parameter as
  callers (decision 14).

The `secret` commands are not scheduled (decision 15).
