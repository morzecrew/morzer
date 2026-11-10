# RFC 0057 — Desired state an installation pulls and applies to itself

- **Status:** 📝 Draft — proposed 2026-11-10 from dotdash's needs (a network
  of servers, relays, canaries and clients that share facts). It was adopted
  the same day, with §3 checked against the code. Ruled by the author
  2026-11-10: decisions 11–15, where the draft meets ruled decisions of RFC
  0025, RFC 0026 and RFC 0027, as recommended. Decisions 18–22, the draft's
  own questions, stay `OPEN`.
- **Scope:** How one party (an *authority*) can change the parameters, secrets
  and selected settings of many installations without any of them accepting
  an inbound connection, running an agent, or trusting anything but a key
  pinned locally. Each installation fetches a signed, encrypted
  *desired-state document* addressed to it from a target it already uses,
  checks it, and applies it to itself as one atomic operation, with the
  plan, journal, health checks and unwinding `config set` already has. The
  result goes back the way every other fact leaves a machine: in its fleet
  row and an attestation. It builds on
  [RFC 0056](0056-one-change-to-settings-parameters-and-secrets.md),
  `config apply`: one operation that changes secrets, parameters and
  settings together. Folded in, because the feature can't work without them:
  - **a public age recipient in the fleet row,** so that an authority can
    encrypt to a machine it has only read about;
  - **facts a machine publishes:** values a release declares, such as a
    relay's public key and the address devices reach it at, encrypted to the
    authority, so that adding a machine needs nothing copied by hand in
    either direction.

  **Contract changes:**
  - the manifest gains `managed` and `published`;
  - the installation gains the `sync.*` settings and the last applied and
    refused serials, which is a state schema bump;
  - the fleet row gains `age_recipient` and a `sync` block, and a signed
    `facts.age` object is published beside it (decision 14);
  - the attestation predicate gains the document's serial, digest and
    authority key id, which is a v4 predicate (decision 15);
  - new commands: `sync now`, `sync status`, `state publish`, `state ls` and
    `state facts`.

  **Reverses or amends:** [RFC 0027](0027-desired-state-in-a-repository.md)
  decision 5 ("no pull, no watch, no schedule") and
  [RFC 0026](0026-fleet-as-a-read-model.md) decisions 2 and 8. It keeps RFC
  0026 decision 6b, the roster as the trust anchor, which the draft would
  have bypassed. Each is a row in §11.
- **Related:**
  - [fleet.md](../pages/docs/reference/fleet.md): read-only by design; this
    RFC keeps that. Nothing here acts on a remote machine.
  - [unattended-updates.md](../pages/docs/operating/unattended-updates.md):
    the precedent for a machine acting on itself from a timer, behind a
    pinned key.
  - [attestations.md](../pages/docs/reference/attestations.md): the signed
    record each apply leaves.
  - RFCs:
    - [0025](0025-attesting-an-installation.md): the attestation predicate;
    - [0026](0026-fleet-as-a-read-model.md): the fleet row, the roster and
      the dev-mode drop list;
    - [0027](0027-desired-state-in-a-repository.md): the desired-state file
      and its boundary;
    - [0028](0028-the-machines-signing-identity.md): the machine key and its
      predecessors;
    - [0052](0052-trusting-a-releases-key-on-first-use.md): pinning a key at
      `init`;
    - [0053](0053-several-profiles-per-installation.md): profiles, which a
      document may set;
    - [0056](0056-one-change-to-settings-parameters-and-secrets.md):
      `config apply`, the operation a document is applied with.
  - morzer#324, fixed in 0.15.1, and morzer#329, on main: a secret's or a
    parameter's dependents behind an inactive profile are no longer
    restarted or re-created.
- **Origin:** A draft by the author, written from dotdash's roster and relay
  work, and numbered 0056 before RFC 0056 took that number. Adopted
  2026-11-10. §3 is checked against `main` at `9dd02e2e`. Where the draft
  restated RFC 0056, it now follows RFC 0056's ruled decisions, and each
  such place says so.

---

## 1. Summary

An installation can be told, from elsewhere, what some of its parameters and
secrets should be, and it applies that to itself. An authority writes a
document per installation, encrypts it to that installation's own age
recipient, signs it with a minisign key, and puts it on a target, an S3
prefix like the ones backups and fleet rows already use. Each installation,
on a timer, fetches the document addressed to it and checks it:

- the signature, against an authority key pinned at `init` or set locally;
- that it names this installation;
- that it is newer than the last one applied;
- that it hasn't expired;
- that it touches only names the release lets be managed.

It then applies everything in it as one operation, or nothing. The outcome
(applied, or refused with the reason) goes into the next fleet row and into
an attestation, so the authority reads its results with the read-only
`fleet ls` it already has.

Nothing listens. Nothing on the authority's side can run a command on a
machine; it can only propose values the machine's own release declared as
manageable, which the machine checks and applies with its own health checks
as the judge.

## 2. Motivation

Products built on morzer increasingly come as several cooperating bundles
on several machines. dotdash is the concrete case: one or more servers
abroad, relays inside a censored country, canaries on mobile lines, and
Linux clients. They share facts:

| Fact | Lives on | Today |
| --- | --- | --- |
| The device list (`vless_clients`) | every server and every relay | copied by hand: an SSH loop between servers, and a relay's copy re-pasted from the server's `relay-config` output after every device change |
| The servers' keys (`reality_key`, `vless_seed`, …) | every server; their public halves on every relay | copied by hand after each rotation; until then every relay is cut off |
| Relay lines (`relays`) | the server | pasted from each relay's `relay-entry` output, again after any move |
| CDN hosts, device pools (planned) | the server, and what each device receives | parameters edited by hand |
| A canary's target | each canary | `canary-config` output, per target |

The network dotdash aims for: one or a few upstream proxies with clean ISP
addresses, a couple of servers, and many relays, managed from one place,
where adding a relay or a server takes minutes rather than an evening of
copying. Every relay added multiplies the table above.

Each is a parameter or a secret that one place should own and every other
place should follow. Doing it by hand is slow, and it fails exactly when it
matters: during a shutdown, when relays are being replaced and keys rotated
under pressure. Every product with more than one bundle per deployment will
have the same problem. dotdash just has it first.

The obvious fix, a control plane that pushes to nodes, is what morzer
refuses ([fleet.md](../pages/docs/reference/fleet.md), "what this will never
do"), and for good reasons that are sharper still for dotdash:

- an inbound control channel on a relay is a detection signal, and a seizure
  target;
- a privileged agent is a second source of truth;
- an authorisation model for remote actions is exactly what morzer exists
  without.

The pull model in unattended updates shows the alternative: the machine acts
on itself, from a timer, behind a key it was told to trust, and refuses what
it can't verify. This RFC extends that from releases to configuration.

One precondition is designed on its own, as RFC 0056. Today a secret, a
parameter and a setting change in separate operations, and `config set`
refuses a setting and a parameter together ("parameters and installation
settings are set separately"). Switching dotdash's upstream to OpenVPN takes
three operations in a fixed order, and each intermediate state has to be
healthy on its own. A remotely authored change will routinely touch several
names, including secrets and profiles, so it must apply whole or not at
all. RFC 0056 specifies `config apply` for that. This RFC applies documents
with it.

morzer 0.15.1 fixed the secret half of restarting behind an inactive profile
(#324). #329, merged after 0.15.1, does the same for a parameter's
re-create.

## 3. Current state

Checked against `main` at `9dd02e2e`.

**What the draft relied on, confirmed.**

- **Keys a machine holds** (`explanation/secrets.md`, `attestations.md`):
  - an **age identity** (`/etc/<product>/age/identity`, 0400), which is one
    of at least two recipients of the encrypted secret store; the recovery
    key is the other. `installation import` creates a new machine age key
    and re-encrypts the store for it and the recovery recipients
    (`recovery.go:745-760`);
  - a **minisign machine key** that signs attestations and fleet rows
    (RFC 0026 decision 6a, RFC 0028). A rebuilt machine signs with a new key
    and records its predecessor (`attestations.md:373`). A predecessor match
    is provenance, not validity (`attestations.md:168-180`).
- **Trusted keys:** a release signing key, pinned at `init` (RFC 0052) or set
  locally, with `release.require_signature`.
- **Fleet** (`reference/fleet.md`, RFC 0026):
  - a signed status row per installation, at
    `fleet/<product>/<id>/status.json` on its backup targets;
  - `fleet ls` reads them, and is read-only;
  - a write-only, prefix-scoped credential is recommended for machines
    (`fleet.md:78-82`).
- **Unattended updates** (`operating/unattended-updates.md`):
  - a timer that acts only when the release declares `rollback_safe: true`
    and the installation requires signatures;
  - `update.auto_apply` is refused without a signature requirement at the
    moment it is set (`unattended-updates.md:98`);
  - a version that failed is **not** retried until
    `morzer update --to <version>` installs it or the channel moves
    (`unattended-updates.md:84`). The draft said the opposite;
  - a tick that cannot take the deployment lock exits 0 and tries at the
    next tick (`unattended-updates.md:165`).
- **Targets** (`reference/backup-targets.md`): the URL grammar (`s3://`,
  `file://`, …) and credential documents, shared by backups and fleet rows
  (RFC 0026 decision 3).
- **Changing configuration:**
  - `config set` is an operation. It takes the deployment lock, plans under
    `--dry-run`, journals each step, re-creates the services a parameter
    declares that the active profiles start (#329), and unwinds on failure;
  - `secret set` re-renders and restarts a secret's declared services, under
    the store's own lock only;
  - settings (dotted names) and parameters are set separately;
  - text parameters exist (RFC 0054).
- **RFC 0056**, merged 2026-11-10 as a proposal and not built, specifies
  `config apply`:
  - one `config` operation for settings, parameters and secrets;
  - the document comes from stdin only (decision 2, `LOCKED`);
  - it is a change, not RFC 0027's desired state (decision 3, `LOCKED`);
  - `config set` of a parameter becomes a caller, and the `secret` commands
    do not (decisions 14 and 15, ruled);
  - secret names stay out of the v3 attestation (decision 11).

**Ruled decisions the draft meets.**

- **RFC 0027 decision 5 (`LOCKED`):** "No pull, no watch, no schedule. A
  git-pull loop is a second scheduled network operation with none of 0016's
  gate." This RFC is a pull on a schedule. Its gate is the pinned authority
  key, the release's `managed` list and the health checks (decision 11).
- **RFC 0026 decision 2 (`LOCKED`):** "The reader cannot act. No
  `fleet update`, no `fleet exec`, no fan-out." The reader stays unable to
  act. `state publish` is a new writer whose documents every machine applies
  to itself (decision 12).
- **RFC 0026 decision 6b (`LOCKED`):** "The reader's trust anchor is the
  roster, never the row." The attacker RFC 0026 names is another machine
  with write access to the shared bucket, which holds a valid key of its
  own. The draft pins a machine key from its row on first sight (§5.4,
  decision 13).
- **RFC 0026 decision 8 (`LOCKED`):** "No new state". The row is derived
  from what `status` and `ls` compute. The `sync` block and `facts` are new
  state (decisions 12 and 14).
- **fleet.md, "What the row never carries":** "Parameter values,
  hostnames, …" (`fleet.md:165-175`). `facts` hold a relay's address and
  public halves, encrypted (decision 14).
- **RFC 0026 decision 7 (`LOCKED`):** dev mode drops what reaches
  production, through one list (`SandboxDrops`,
  `internal/domain/sandbox.go:64`). `sync.*` reaches production's documents
  prefix, so it joins the list (decision 16).
- **The attestation predicate refuses unknown fields:** "Each schema refuses
  any field it does not name" (`attestation.go:39-44`). The v3 `config`
  predicate holds parameter names and a rendered digest only
  (`attestation.go:219`). A serial, digest and authority key id need a v4
  predicate (decision 15).

**Missing:**
- an operation that changes several names atomically (RFC 0056 specifies
  it, unbuilt);
- any way for a signed, encrypted value set to reach a machine;
- a declaration of which names a release lets be managed;
- any feedback channel for such a change.

## 4. Goals / Non-goals

**Goals**

1. An installation applies a desired-state document addressed to it, pulled
   from a target, only if it is signed by a key pinned locally, names this
   installation, is newer than the last applied, is within its validity
   window, and changes only names the release declares manageable.
2. A document's changes, whether parameters, secrets or the settings the
   release allows, apply as **one operation**: one plan, one re-render, one
   re-creation of the union of affected services, health checks, and full
   unwinding on failure.
3. The same atomic change is available locally, unsigned, as
   `morzer config apply` (RFC 0056).
4. Results are visible from elsewhere without anything listening: the fleet
   row carries the last applied and last refused document, and each apply
   leaves an attestation.
5. No new inbound connection, agent, daemon or listener; one more timer.
6. Losing the authority key bounds the damage to the declared manageable
   names; it can never change trust anchors, recipients, update policy or
   the sync configuration itself.
7. Encrypted end to end: the target sees ciphertext and sizes; only the
   addressed machine can read its document.

**Non-goals**

- Running commands, tasks or arbitrary hooks on a machine from elsewhere.
- Installing or rolling back releases through a document (unattended
  updates already do this; a desired version is a possible later extension,
  §8).
- A GUI, an API server, or a database on the authority's side.
- Distributing arbitrary files.
- Consensus between authorities, or more than one authority per
  installation.
- Real-time propagation: the timer's interval is the latency.

## 5. Design

### 5.1 Concepts

- **Authority:** whoever holds a minisign secret key whose public key an
  installation trusts for desired state. Usually an operator's machine, or
  a manager bundle (a product-level tool such as a dotdash manager) that
  computes documents from its own records.
- **Document:** a YAML or JSON body naming one installation, a serial, a
  validity window, and the changes; encrypted to that installation, then
  signed.
- **Manageable name:** a parameter, secret or setting the installed
  release's manifest declares that a document may change (§5.6).
- **Sync:** the machine-side timer that fetches, verifies and applies.

### 5.2 The document

```yaml
api_version: selfhost/v1alpha1
kind: desired-state
product: dotdash-relay
installation: inst_01M72B61SSWFT54B6KTJC26YD0
serial: 1794310000123          # strictly increasing; a timestamp in ms works
issued_at: 2026-11-10T12:00:00Z
expires_at: 2026-11-17T12:00:00Z   # refused after this; bounds replay and freezing
# Each section may be absent. Only names listed change; others are untouched.
parameters:
  dotdash_servers: example.com,b.example.com
  dotdash_public_key: RWQ...
settings:
  installation.profile: own-site   # only settings the release lists (§5.6)
secrets:
  vless_clients: |
    0b6c... phone pool=a
    7f21... laptop pool=a
  dotdash_short_id: 2f9a...
unset:
  parameters: [dotdash_address]   # back to the release's default
  secrets: []
```

- **The body below the envelope is RFC 0056's change document.** The
  envelope fields are `api_version`, `kind`, `product`, `installation`,
  `serial`, `issued_at` and `expires_at`. The `settings`, `parameters`,
  `secrets` and `unset` sections, and their validation, are RFC 0056 §5.1
  and §5.2.
- **Merge semantics:** names not mentioned are untouched, and removal is
  explicit (`unset`). An authoritative "everything else to defaults" mode is
  left out: it turns an authority's omission into an outage.
- **Each document is cumulative** (decision 24). It carries every value the
  authority manages for this installation, and every name it wants unset,
  not only what changed since the previous document. A machine applies only
  the newest document it finds and can miss any number before it. A
  document holding only a change to `y` would drop a change to `x`
  published before it and never fetched. `state publish` builds the
  document from the authority's whole record for the installation. Values
  that already match change nothing: RFC 0056 runs no operation for a
  change with nothing in it.
- `serial` is compared with the last **applied** serial for this
  installation, which is stored locally. A document whose serial is not
  greater is refused as a replay.
- **The applied serial commits with the change** (decision 25). It is
  written in the operation's record step, in the same installation write as
  the settings and parameters (RFC 0056 §5.4, step 1). That step runs for
  every document, including one that holds only secrets. Its undo puts the
  previous serial back. So no crash can leave serial 20 applied under a
  stored watermark of 10, where a still-valid serial 15 would be accepted
  as newer. A killed operation leaves an unfinished record that holds the
  next sync, as every operation does (RFC 0056 decision 16).
- `expires_at` bounds how long a captured document stays valid, and lets a
  machine notice an authority gone quiet (§5.9).
- Size limit: 1 MiB after decryption, the bound RFC 0056 decision 4 sets for
  a change document (decision 20).

### 5.3 Encryption and signature

- The body is encrypted with **age** to the installation's own age recipient
  (its public key) and, optionally, to additional recipients the authority
  names, such as its own key so it can re-read what it sent. Only the
  addressed machine, and whoever holds the authority's recipients, can read
  it.
- The **ciphertext** is signed with **minisign** (detached `.minisig`), so a
  machine verifies before it decrypts, and the target can hold nothing the
  machine will parse unless it is signed. The signature's trusted comment
  carries `product`, `installation` and `serial` in the clear, so a machine
  can drop a document meant for another before decrypting, and a reader of
  the target sees which installation a document is for without reading it.
- Layout on the target, beside the fleet rows:

  ```text
  state/<product>/<installation-id>/state.age
  state/<product>/<installation-id>/state.age.minisig
  ```

- Both machine and authority keys are those morzer already uses: age for
  data, minisign for statements. No new cryptography.

### 5.4 How the authority learns a machine's recipient

The fleet row gains two public fields:

```json
"age_recipient": "age1...",          // this machine's, for desired state
"sync": { ... },                      // §5.9
```

The row is already signed by the machine key. The authority trusts that key
only from the roster, the file `fleet ls --expect` already reads, which
lists each installation with the key it signs with (decision 13). A row whose
signature does not check out against the roster's key is never used: its
recipient is not encrypted to, and `state publish` refuses the installation.
When a machine is rebuilt (`installation import`), it signs with a new key
and has a new recipient. Its new key enters the roster out of band, as
`fleet.md` describes for any new key. Until then, documents encrypted to the
old recipient fail to decrypt and are reported as such (§5.9).

This closes the loop without a listener. The authority reads what it needs
to write from the read-only view, and writes to the same place the machine
already reads from.

**Why the roster, and not the row (RFC 0026 decision 6b).** A row that
carries the key verifying it verifies itself, and another machine with write
access to the bucket holds a valid key of its own. The draft pinned a key on
first sight. Then such a writer could post a row for an installation id the
authority had not seen, and the authority would encrypt documents to it,
shared keys included. A successor recorded in a row is provenance, not trust
(RFC 0028), so a rebuilt machine is a roster change too.

### 5.5 Configuring sync on a machine

Settings, local and root-only, never manageable by a document:

```sh
morzer config set sync.target=s3://bucket/prefix   # credentials as for backups
morzer config set sync.authority=RWQ...            # minisign public key
morzer config set sync.recipient=age1...           # the authority's, for facts (§5.10)
morzer config set sync.interval=15m                # default 15m, with jitter
morzer config set sync.enabled=true
```

- Or at `init`: `--sync-target`, `--sync-authority`, `--sync-recipient`.
- `sync.enabled=true` is refused unless `sync.authority` is set, as
  `update.auto_apply` is refused without a signature requirement.
- Changing `sync.authority` is a local, root-only act. A document can never
  carry it (§5.6), so a compromised authority can't hand itself to someone
  else.
- `morzer sync now` runs one cycle immediately; `morzer sync status` prints
  the last applied and refused serials with reasons.
- **`sync.target` takes its own credential**, read-only and scoped to
  `state/<product>/<installation-id>/`. The fleet credential is write-only
  by recommendation (`fleet.md:78-82`) and cannot read. A machine that can
  list other installations' documents learns which installations exist,
  even though it cannot read them (decision 17).
- **`installation import --mode dev` drops `sync.*`**, beside the backup and
  notify targets, through the one list RFC 0026 decision 7 created
  (decision 16). A sandbox rebuilt from a production export keeps
  production's installation id and would read production's documents
  prefix.

### 5.6 What a release lets be managed

A new manifest field, ignored by older managers:

```yaml
managed:
  parameters: [dotdash_servers, dotdash_port, dotdash_public_key, dotdash_address, site, probe_url]
  secrets: [vless_clients, dotdash_id, dotdash_encryption, dotdash_short_id]
  settings: [installation.profile, installation.domains]
```

- **Absent means nothing is manageable:** a release opts in, name by name.
  The vendor knows which names are safe to accept from elsewhere: ones that
  don't reach a command line, or that only a template renders as data.
- Settings are limited to a fixed set morzer allows a document to touch at
  all: `installation.profile` and `installation.domains`. Even a release
  can't make `release.*`, `update.*`, `sync.*`, `policy.*`, recipients,
  backup or notify targets manageable.
- A document naming anything outside the release's list is refused whole,
  not partly applied.
- `morzer release verify` checks that every listed name exists.

### 5.7 Applying: `config apply`, one operation

RFC 0056 specifies the operation, and this RFC relies on it. The same
operation backs local and remote changes:

```sh
morzer config apply < change.yaml            # local, unsigned: the body of §5.2 without the envelope
morzer config apply --dry-run < change.yaml  # the plan
```

The local form reads stdin only (RFC 0056 decision 2). The draft's
`config apply change.yaml` is that decision's rejected path argument. A sync
hands the decrypted body to the operation directly, and never writes it to
disk.

The operation's steps, plan and undo are RFC 0056 §5.3–§5.5. What a sync
adds:

1. **Validate.** Every name is also manageable (§5.6), and the whole
   document is refused otherwise.
2. **On failure,** at any step, the operation unwinds as RFC 0056 specifies,
   and the document is recorded as refused, with the step and the error. A
   refused serial is not retried; only a newer serial is considered.

`config set` of a parameter becomes a one-name caller of the same operation
(RFC 0056 decision 14). The `secret` commands stay as they are (RFC 0056
decision 15). A secret can still be changed by a document, since a sync goes
through the operation and not through `secret set`. The dotdash switch in §2
becomes one `config apply` with a secret, a parameter and the profile.

### 5.8 The sync cycle

Each tick (`sync.interval`, with jitter):

1. Fetch `state.age.minisig` and `state.age` for this installation. Absent:
   nothing to do (not an error). Unreachable: record that, and change
   nothing.
2. Verify the signature against `sync.authority`. Check the trusted
   comment's product and installation against this machine's, and its serial
   against the last applied and last refused.
3. Decrypt with the machine's age identity. Parse, check the body's
   product, installation and serial against the comment's, and check
   `expires_at`.
4. Apply (§5.7) with the manageability check on.
5. Publish the fleet row with the outcome (§5.9). The operation's
   attestation names the document's serial, its digest, and the authority's
   key id, in a `desired_state` block of a v4 predicate that RFC 0025
   defines. The v3 predicate has no field for these and refuses one it does
   not name (decision 15).

The sync timer and `update.auto_apply` never run concurrently: both take the
deployment lock. The one that cannot take it exits 0 and tries again at its
next tick, as an unattended update already does
(`unattended-updates.md:165`). A document whose names a newly installed
release doesn't declare manageable is refused under that release and
reported, never half-applied.

### 5.9 Feedback

The fleet row's `sync` block:

```json
"sync": {
  "authority_key_id": "B4C3...",
  "applied": {"serial": 1794310000123, "at": "2026-11-10T12:00:41Z"},
  "refused": {"serial": 1794310099000, "at": "...", "step": "check", "reason": "health check through-dotdash failed: ..."},
  "last_fetch": {"at": "...", "result": "ok | absent | unreachable | bad-signature | undecryptable | expired | replay | unmanageable"}
}
```

- `doctor` warns when sync is enabled and the last applied document expires
  within a day or has expired, or when the target has been unreachable for
  longer than three intervals. Withholding documents is the one thing a
  hostile target can do, and this makes it visible.
- Notify targets get "refused" and "expired" events, as they get failed
  updates.
- A refusal's `reason` in the row names the step, a class such as
  `invalid` or `health-check`, and the names involved. It never holds the
  error's text. A parameter's refusal quotes the value it refused, and the
  row never carries a parameter value, a hostname or a secret
  (`TestARowCarriesNoParameterValueOrSecret`, `fleet_test.go:136`). The
  full message stays in the local journal, which `sync status` prints
  (decision 23).

### 5.10 Facts going back

Adding a machine needs facts to flow the other way too. A new dotdash relay
makes its own REALITY key at `init`. The servers need its public half, its
short id and the address devices reach it at; today that is `relay-entry`'s
output, pasted into each server's `relays` by hand. A release names a task
that prints those facts:

```yaml
published:
  task: relay-entry   # run without arguments; stdout is key=value lines
```

- morzer runs the task after every successful operation (init, update,
  `config apply`, a sync) and publishes its output as the machine's facts.
  The output is limited to 64 KiB, and a failed run keeps the previous facts
  and is reported in the row.
- The facts are age-encrypted to `sync.recipient`, an age recipient of the
  authority's that is set locally beside `sync.authority`. A short id is not
  for every reader of the bucket. Without `sync.recipient`, nothing is
  published.
- The facts are signed with the machine key, so the authority knows they
  come from the key the roster lists for that installation.
- Facts are meant to be public halves and addresses. morzer can't tell a
  secret from a fact; a vendor that publishes a credential publishes it to
  the authority, and the docs say so.
- A task run without arguments must find what it needs itself. dotdash's
  `relay-entry` would read the relay's address from its domain under the
  `own-site` profile.
- **They are published beside the row, not in it** (decision 14), as
  `fleet/<product>/<id>/facts.age` with its `.minisig`. The draft put them in
  the row. fleet.md says the row never carries hostnames or parameter
  values, and RFC 0026 decision 8 says the row holds no new state. Beside it,
  the row stays a row, and the same write-only credential publishes both.
  fleet.md's rule is restated as "never in plaintext".

With this, adding a relay is:

1. On the new machine: `morzer init dotdash-relay … --sync-target s3://… \
   --sync-authority RWQ… --sync-recipient age1…`, and its DNS record.
2. Its first fleet row appears with its age recipient, and its facts
   beside it.
3. The authority adds the machine's key to the roster, out of band, as
   `fleet.md` describes (`fleet publish --dry-run --json` prints it on the
   machine). Until it does, `state ls` reports the row as unexpected and
   `state publish` refuses the installation.
4. The authority publishes the relay's document (the servers, their public
   key, the devices of its pool), and new documents for each server, adding
   the relay's line to `relays`.
5. Within one interval both sides apply, and the next subscription fetch
   carries the new relay.

A server joins the same way. Its document carries the shared keys, encrypted
to it alone.

### 5.11 The authority's side

morzer gains commands that need no installation of their own, as `fleet ls`
already is:

```sh
morzer state publish --target s3://bucket/prefix --expect roster.yaml \
  --product dotdash-relay --installation inst_... --file state.yaml \
  --key ./authority.minisign.key
morzer state ls --target s3://bucket/prefix --expect roster.yaml   # rows joined with sync outcomes
```

- **`publish`** reads the installation's fleet row and checks the row's
  signature against the key the roster lists for it. An installation the
  roster does not list, or a row that does not verify, is refused
  (decision 13). It takes `age_recipient` from the row, stamps a serial (a
  timestamp unless given), and fills `expires_at` from a default validity,
  `--valid-for`, defaulting to 7 days. Then it encrypts, signs and uploads.
  `--file` names a plaintext document on the authority's machine. That
  machine holds the authority key already, and every value in the document
  is one it chose to send.
- **`ls`** shows, per installation, the applied and refused serials and the
  reasons, which is the read the authority needs to know it worked. A row
  the roster does not list is shown as unexpected, as `fleet ls` shows it.
- **`state facts`** decrypts an installation's facts with the authority's
  age identity and prints them, for a manager to read.

A product's own manager (for dotdash, the device roster in its part 7, not
morzer's `roster.yaml`) is just a
program that computes documents and calls `state publish`. morzer doesn't
need to know what a relay is.

### 5.12 Alternatives considered

- **A push agent with an API** (Remnawave-style panel and nodes): rejected
  by morzer's own principles, and worse for dotdash (an inbound port on
  every relay, a seizure and detection target).
- **Git as the transport** (machines pull a repository): heavier on the
  machine, and secrets in Git need the same age-and-sign envelope anyway.
  S3 targets already exist for backups and fleet rows.
- **Per-group documents** (one document for all relays): fewer files, but a
  group needs its own recipient set and membership, which brings back the
  bookkeeping this removes. Per-installation documents with merge
  semantics stay simple, and an authority can generate N of them from one
  template. Decision 19 keeps it open.
- **Authoritative mode** (unlisted names reset): rejected for v1; an
  omission becomes an outage.
- **Signing the plaintext:** the machine would decrypt unsigned input before
  knowing it's genuine. Signing the ciphertext lets it verify first.
- **Trusting a machine key from its row on first sight** (the draft's
  §5.4): one prompt instead of a roster entry. Rejected: a writer to the
  bucket can pose as a new installation and receive shared secrets
  (decision 13).

## 6. Tests

- **Unit:**
  - document parsing and validation;
  - signature checks: good, wrong key, wrong installation in the comment,
    tampered ciphertext;
  - serial monotonicity, against applied and refused;
  - two documents published between polls: the second, being cumulative,
    puts both changes in force;
  - an apply that fails and is undone leaves the previous serial stored,
    and the serial is in the same record write as the change;
  - expiry;
  - manageability: in the list, out of it, a setting morzer never allows;
  - age round trip to a machine recipient and an extra recipient;
  - fleet row fields;
  - facts: the task's output encrypted to `sync.recipient`, a failed run
    keeping the previous facts, nothing published without a recipient;
  - the dev-mode drop list includes `sync.*`, through the test that already
    classifies every installation field (RFC 0026 decision 7).
- **`config apply`:** RFC 0056 §6 owns the operation's tests. This RFC adds
  the sync's: a document with an unmanageable name refused whole, and a
  refusal recorded with its step.
- **Acceptance rig:** two installations of different products, and a MinIO
  bucket.
  - An authority publishes documents; both apply them within one interval;
    `state ls` shows the applied serials.
  - Tamper with a document: refused as `bad-signature`.
  - Replay an older document: refused as `replay`.
  - Let a document expire: `doctor` warns.
  - A document whose change fails a health check: unwound, refused with the
    reason, and fleet shows it.
  - Rebuild a machine with `import`: the old document is `undecryptable`;
    after re-encrypting to the new recipient, it applies.
  - The sync timer and `update.auto_apply` contend for the lock; both
    complete across ticks.
  - Join a third installation with `--sync-*` at `init`: `state ls` lists
    it as new, `state facts` prints its facts, and a document built from
    them reaches another installation and applies.
  - A second machine with write access posts a row for an unknown
    installation id: `state publish` refuses it until the roster lists it.
- **Leaks:** no parameter value, hostname or secret appears in a fleet
  row's `sync` block, including a refusal whose error quoted a value. This
  extends `TestARowCarriesNoParameterValueOrSecret`. No secret value appears
  in a notification or an attestation, scanned as
  `TestAConfigChangeFilesAStatement` scans a statement.

## 7. Docs

- **`operating/managing-from-elsewhere.md`:** the model (pull, sign, encrypt,
  apply to itself), setting it up, and what a lost authority key can and
  cannot do.
- **`reference/desired-state.md`:** the document, the layout, the fleet
  `sync` block, the refusal reasons.
- **`reference/manifest.md`:** `managed`, `published`.
- **`reference/fleet.md`:**
  - `age_recipient`, `sync`, and the `facts.age` object beside the row;
  - "What this will never do" and "What the row never carries", restated
    as decisions 12 and 14 rule;
  - the sandbox drop list, with `sync.*`.
- **`operating/changing-configuration.md`:** `config apply`, and that a
  change to several names is one operation (RFC 0056 §7).
- **`reference/installation-commands.md`**, at `installation describe`: how
  a desired-state document differs from `describe`'s file. It is a change,
  not a state; it carries values; it is never committed.

## 8. Out of scope

- A desired release version in the document (unattended updates cover
  updates; adding `release:` is a natural later extension, once this is
  proven).
- Rotating the authority key through a document (a signed "next key" is
  possible later; v1 re-pins locally).
- Several authorities per installation, or quorum signatures.
- Tasks or one-off actions from elsewhere.

## 9. Risks

- **A compromised authority key** can change every manageable name on every
  installation that trusts it, within the documents' validity. Bounds:
  - releases list few names;
  - morzer never lets trust, update or sync settings be managed;
  - health checks unwind breaking changes;
  - each change leaves an attestation;
  - re-pinning `sync.authority` locally cuts it off.

  A vendor that lists a name whose value reaches a command line hands the
  authority code execution. `release verify` should warn about parameters a
  template uses unquoted in a command (best effort).
- **A writer to the bucket posing as a machine.** Every machine that
  publishes rows can write to the bucket. The roster closes this: a row for
  an installation it does not list, or signed by another key, is never
  encrypted to (decision 13). It costs a roster entry per new or rebuilt
  machine, which is the out-of-band step RFC 0026 already requires.
- **A hostile target** can withhold documents (freezing) or serve old ones;
  replay is refused by serial, and freezing is surfaced by expiry and
  `doctor`. It can't read or forge documents.
- **Local edits against managed names:** `config set` of a manageable name
  on a machine that syncs would be silently overwritten at the next newer
  document. Decision 6 makes such edits warn, and tells the operator to
  change it at the authority.
- **Clock skew** affects `expires_at`; leave a day's slack in the default
  validity, and use the serial, not wall time, for ordering.
- **Facts in the bucket.** Encrypted, a relay's address is still a fact the
  bucket holds about the network. A bucket that leaks shows ciphertext,
  sizes and which installations exist (decision 14).

## 10. Unresolved questions

The draft's five questions are decisions 18–22, and stay `OPEN` for the
author. Decisions 11–15, where it met ruled decisions of other RFCs, were
ruled on 2026-11-10 as recommended. Implementation is free to settle the
exact refusal wording and the jitter's distribution.

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `LOCKED` | Pull only. Nothing on a managed machine listens, and nothing outside it acts on it. This keeps RFC 0026 decision 1's reason (no listener, no inbound connection), while the machine now also reads its own documents. |
| 2 | `ASSUMED` | Documents are per installation, with merge semantics and explicit unset. |
| 3 | `LOCKED` | Encrypt to the machine's age recipient; sign the ciphertext with minisign; check the signature before decrypting. |
| 4 | `LOCKED` | The authority key is pinned locally (`sync.authority`) and never changed by a document. |
| 5 | `LOCKED` | A release declares `managed` names, and absent means none. morzer forbids trust, update, sync, policy, recipient and target settings regardless. |
| 6 | `ASSUMED` | Locally, `config set` on a managed name warns while sync is enabled, and names the authority as the place to change it; `--force` proceeds. |
| 7 | `LOCKED` | Every change of several names is one operation, RFC 0056's `config apply`. Restarts and re-creations cover active services only. The local form reads stdin (RFC 0056 decision 2). `config set` of a parameter is a caller and the `secret` commands are not (RFC 0056 decisions 14 and 15). |
| 8 | `ASSUMED` | The fleet row carries `age_recipient` and the `sync` outcome; `doctor` warns on expiry and a target unreachable too long. |
| 9 | `ASSUMED` | Defaults: interval 15 minutes with jitter; validity 7 days. |
| 10 | `ASSUMED` | A release may name a task whose output becomes the machine's facts, encrypted to the authority's recipient and signed with the machine key. They are published beside the row (decision 14). |
| 11 | `LOCKED` | This reverses RFC 0027 decision 5 (`LOCKED`: no pull, no watch, no schedule) for documents signed by a locally pinned authority key. Its gate is the pinned key, the release's `managed` list and the health checks, as 0016's gate is for updates. Reversing this back withdraws sync from every installation that relies on it. Was `OPEN`; ruled by the author 2026-11-10: as recommended. |
| 12 | `LOCKED` | This amends RFC 0026 decisions 2 and 8 (both `LOCKED`). The reader, `fleet ls`, still cannot act. `state publish` is a new writer whose documents each machine applies to itself, within its release's `managed` names, behind its own health checks. The row gains the `sync` block and `age_recipient`, which are new state. fleet.md's "What this will never do" is restated: a writer may propose values, and only the machine applies them. Was `OPEN`; ruled by the author 2026-11-10: as recommended. |
| 13 | `LOCKED` | The authority trusts a machine's key, and so its recipient, only from the roster (`--expect`), keeping RFC 0026 decision 6b (`LOCKED`). `state publish` refuses an installation the roster does not list or a row that does not verify against its key, and a rebuilt machine's new key enters the roster out of band. Rejected: the draft's first-sight pinning, under which a bucket writer holding a valid key of its own could pose as a new installation and receive shared secrets (§5.4). Was `OPEN`; ruled by the author 2026-11-10: as recommended. |
| 14 | `ASSUMED` | Facts are a separate signed object beside the row, `fleet/<product>/<id>/facts.age` with its `.minisig`, written with the same write-only credential. fleet.md's "the row never carries" stands for the row and is restated as "never in plaintext" for the bucket. Rejected: the draft's `facts` field in the row, which RFC 0026 decision 8 and fleet.md rule out. Was `OPEN`; ruled by the author 2026-11-10: as recommended. |
| 15 | `ASSUMED` | A sync's statement names the document's serial, digest and authority key id in a `desired_state` block of a v4 predicate, present only on a sync's statement. The v3 predicate refuses a field it does not name (`attestation.go:39-44`), so RFC 0025 defines v4. Was `OPEN`; ruled by the author 2026-11-10: as recommended. |
| 16 | `ASSUMED` | `installation import --mode dev` drops `sync.*` with the backup and notify targets, through `SandboxDrops` (RFC 0026 decision 7). The rule is reach: a sandbox keeps production's installation id and would read production's documents prefix. |
| 17 | `ASSUMED` | `sync.target` takes its own credential, read-only and scoped to `state/<product>/<installation-id>/`. The fleet credential stays write-only (`fleet.md:78-82`). |
| 18 | `OPEN` | Is a timestamp serial enough, or should the authority keep a counter per installation? Timestamps need no state on the authority's side, but two authority machines could issue out of order. |
| 19 | `OPEN` | Per-group documents (§5.12): leave for later, or design the layout to allow `state/<product>/groups/<name>/` now? |
| 20 | `OPEN` | The document size limit, given text parameters such as a device list of thousands of lines. Assumed: 1 MiB after decryption, RFC 0056's bound for a change document. A `text` parameter is bounded at 64 KiB on its own (RFC 0054 decision 4), so a large list is a secret, bounded at 1 MiB. |
| 21 | `OPEN` | Should `installation.domains` be manageable at all? It re-creates every service and may change certificates; it's useful for dotdash's relays, but heavy. |
| 22 | `OPEN` | Should a refused serial block newer serials whose changes include the refused one, or should every newer serial be tried? The draft tries every newer serial. |
| 23 | `ASSUMED` | A refusal in the fleet row carries the step, a class and the names involved, never the error's text, which can quote a parameter value. The row's rule that it never carries a parameter value, a hostname or a secret stands, and the leak test covers the `sync` block. The full message stays in the local journal. |
| 24 | `ASSUMED` | Each document is cumulative: every value the authority manages for the installation and every name it wants unset, every time. A machine applies only the newest and may miss any before it, so a delta document would lose a change published before it (§5.2). |
| 25 | `ASSUMED` | The applied serial is written in the operation's record step, in the same installation write as the settings and parameters, for every document, and its undo restores the previous serial. A change and its serial commit together, so a crash cannot leave a change in force under an older watermark that a replay could pass (§5.2). |

## 12. Phasing

1. **P1, RFC 0056, `config apply`.** Designed and ruled on its own. Atomic
   multi-name changes locally, including profiles and domains. Re-creations
   limited to active services are already on main (#329). Standalone value:
   it removes the workarounds dotdash shipped (a three-step upstream switch,
   parameters that declare no services).
2. **P2, fleet `age_recipient`.** One public field, and the roster as the
   authority's trust anchor for it (decision 13).
3. **P3, sync on the machine.** Settings, timer, fetch, verify, decrypt,
   apply, the `sync` block, `doctor`, the `managed` manifest field, and
   `sync.*` in the sandbox drop list.
4. **P4, the authority's commands.** `state publish`, `state ls` and
   `state facts`.
5. **P5, facts.** The `published` manifest field, `sync.recipient`, and the
   `facts.age` object beside the row. With it, adding a machine needs
   nothing copied by hand beyond its roster entry.

Each phase ships and is useful alone. P1 is worth doing even if P3 never
lands. P4 needs the v4 predicate (decision 15) only for its attestation
fields.
