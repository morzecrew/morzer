# RFC 0003 — Secrets, recovery and onboarding

- **Status:** 📝 Draft
- **Scope:** Completes the secret surface with `secret edit` (an `$EDITOR`
  session over a decrypted copy on tmpfs), rotation-policy reporting in `doctor`,
  and `installation export` / `installation import` so a machine can be rebuilt
  from an offline recovery key plus a backup. Adds an optional `huh` wizard as a
  front-end to `init`'s existing flags. Adds one `SecretStore` port method
  (`ReencryptFor`); no other contract changes. Explicitly **not** in scope: any
  change to the encryption format, KMS or Vault backends, and the restore
  operation itself, which already exists.
- **Related:** [`internal/adapters/secrets/sopsage/`](../internal/adapters/secrets/sopsage/),
  [`internal/cli/secret.go`](../internal/cli/secret.go),
  [`internal/lifecycle/ops/init.go`](../internal/lifecycle/ops/init.go),
  [`test/contract/secretstore.go`](../test/contract/secretstore.go),
  RFC [0001](0001-update-and-rollback.md) for the rollback path a restore backs

---

## 1. Summary

Three gaps close. `secret edit` lets an operator change several secrets in one
`$EDITOR` session without ever writing plaintext outside tmpfs. `installation
export` / `import` make "the VM is gone" a procedure rather than an improvisation:
export produces a portable, recovery-key-encrypted bundle of the installation
identity and secret state; import rebuilds a machine from it. And the `init`
wizard makes first-run interactive without making anything non-scriptable.

## 2. Motivation

**The recovery key has no exercised path.** `init` requires an offline recovery
recipient — refusing to proceed without `--no-recovery-recipient` — and
`doctor` warns when one is missing. But nothing in the repository uses one. The
sequence an operator would need after losing a machine is: install the manager,
recreate the installation with the *same* ID (there is no flag for that),
re-encrypt the secret state using the recovery key (there is no command for
that), then restore. Steps two and three do not exist. The manager insists on a
safeguard whose use is not implemented, which is a worse position than not
insisting: it produces confidence without capability.

**Editing several secrets means several commands.** Each `secret set` is a
separate decrypt-modify-encrypt cycle and a separate prompt. The spec's
`secret edit` exists precisely because rotating a related group is one logical
change.

**`init` is flag-only.** Every option has a flag — which is the important
property and must stay — but a first-time operator has to read `--help` to
learn that a recovery recipient is mandatory.

## 3. Current state

**Already built, verified against the tree.**

- `secret list | set | generate | rotate | remove | render | recipients
  list/add/remove | recipients generate-recovery-key` — all present in
  [`internal/cli/secret.go`](../internal/cli/secret.go).
- `Store.reencrypt` exists as an **unexported** method in
  [`recipients.go`](../internal/adapters/secrets/sopsage/recipients.go), used by
  `AddRecipient`/`RemoveRecipient`. It already does exactly what import needs —
  decrypt, then re-encrypt for a given recipient list — and is unreachable from
  outside the package.
- `SecretDeclaration.RotationPeriod` is parsed from `templates/secrets.yaml`,
  and `SecretMetadata.LastChanged` is populated by the real store. Nothing
  compares them: there is no rotation check in `doctor`.
- The contract suite already asserts the invariants an editor must not break —
  0400 files in a 0700 directory, stale-file pruning, refusal to remove the last
  or the machine recipient.
- Values reach `sops` over stdin, never argv.

**Not built.**

- `secret edit`, `installation export`, `installation import`.
- Any way to set the installation ID at `init` — it is generated unconditionally
  in `buildInstallation` ([`init.go`](../internal/lifecycle/ops/init.go)),
  reusing an existing one only on `--repair`.
- Any `huh` usage; no dependency on it.

**The fact that shapes the design:** `SetCurrentRelease` and the backup
manifest both key on the installation ID, and `Restore` refuses a backup from a
different installation without `--force`. So a rebuilt machine that generates a
fresh ID cannot cleanly restore its own backups. Import must therefore restore
the *identity*, not just the secrets — which is why this is one feature and not
two.

## 4. Goals / Non-goals

**Goals**

- Make the offline recovery key usable, end to end, with a test that proves it.
- Edit several secrets in one session without plaintext leaving tmpfs.
- Report secrets past their declared rotation period in `doctor`.
- Make `init` approachable interactively while every path stays flag-driven.

**Non-goals**

- **Changing the encryption format.** SOPS + age stays. This RFC adds no
  cryptography; it makes existing cryptography reachable.
- **KMS or Vault backends.** New providers behind `SecretStore`, not this RFC.
- **A secret-sharing or escrow scheme.** The recovery key is a file an operator
  stores somewhere safe. Splitting it across custodians is a policy the manager
  should not invent.
- **Making the wizard the default path.** It is a front-end over flags. If the
  two ever diverge, the flags are correct.

## 5. Design

### 5.1 `secret edit`

```text
morzer secret edit [name...]
```

1. Decrypt into a file in the secret render directory — tmpfs, `0700` dir,
   `0600` file (writable, unlike rendered secrets, because `$EDITOR` must save).
2. Run `$EDITOR` (then `$VISUAL`, then `vi`) against it, inheriting the terminal.
3. On clean exit: parse, diff against the pre-edit set, write only what changed,
   restart only the services declaring a dependency on a changed secret.
4. `defer` an unconditional shred-and-remove — including on panic, signal, and
   non-zero editor exit.

```go
// The temp file is created inside the existing 0700 render directory rather
// than os.TempDir(): /tmp is frequently not tmpfs, and a crash there would
// leave plaintext on a disk the operator believes is clean.
path := filepath.Join(d.Paths.SecretsRenderDir(), ".edit-"+randomSuffix()+".yaml")
defer func() {
    _ = overwriteThenRemove(path) // best effort; tmpfs makes it belt-and-braces
}()
```

Refusals: no `$EDITOR` and no TTY (there is no sensible interactive fallback);
a file that does not parse after editing (the original is left untouched and the
operator is told the parse error); an edit that removes a secret the release
declares `required` without `--force`.

The editor never sees the SOPS metadata block — only a plain `name: value`
mapping — so an operator cannot corrupt the encryption envelope by editing it.

### 5.2 Export and import

```text
morzer installation export <path> [--recipient <age1...>]
morzer installation import <path> --identity <recovery-key-file>
```

**Export** writes a single file containing the installation record (ID, product,
profile, domains, policy), the encrypted secret state, the recipient
annotations, and the current release pointer. It contains no plaintext secrets:
the secret state is re-encrypted for the recipients given, defaulting to the
existing set. Machine data is not included — that is what `backup` is for, and
duplicating it would produce two artifacts with different freshness that an
operator must reason about.

**Import** requires an identity that can decrypt the payload, and:

1. Refuses to overwrite an existing installation without `--force`.
2. Restores the installation **including its original ID** — the property that
   makes existing backups restorable, per §3.
3. Generates a *new* machine identity for the target host and re-encrypts the
   secret state for it, then adds it as a recipient. The old machine's key is
   removed: a decommissioned host must not retain access.
4. Leaves the release uninstalled. `update`/`apply` follow, then `restore`.

This is the one place the unexported `reencrypt` is needed from outside, so the
port gains:

```go
// ReencryptFor replaces the recipient set wholesale. Distinct from
// AddRecipient/RemoveRecipient, which preserve the rest of the set: recovery
// deliberately replaces a machine key that no longer exists.
ReencryptFor(ctx context.Context, recipients []Recipient) error
```

The contract suite gains a case asserting that after `ReencryptFor`, exactly
the named recipients can decrypt — enforced against both the fake and the real
sops-age store.

### 5.3 Rotation reporting

A `doctor` check comparing `SecretMetadata.LastChanged` against
`SecretDeclaration.RotationPeriod`. A secret past its period is a **warning**,
never a failure: the release author's rotation period is a policy recommendation,
and failing `doctor` — which is an exit code monitoring watches — over a
recommendation would train operators to ignore it.

```text
[warn] secret rotation is current: db_password is 94d old (policy: 90d)
       → rotate with `morzer secret rotate db_password`
```

Secrets with no declared period are not reported at all.

### 5.4 The `init` wizard

`huh` runs only when: stdin and stdout are a TTY, `--yes` was not passed, and at
least one required value is missing. Otherwise `init` behaves exactly as today.

The wizard collects product name, deployment profile (from the manifest's
declared profiles), domains, and the recovery recipient — offering to generate
one, with the resulting file path stated and a warning to move it off the
machine. It ends by printing **the equivalent command line**, so an operator who
ran it once can script it thereafter:

```text
equivalent to:
  morzer init --product demo --profile embedded --domain demo.example \
      --recovery-recipient age1vm6ncva…
```

`huh`'s accessible mode is honoured automatically for screen readers.

## 6. Tests

- **Contract suite** gains `ReencryptFor` cases — recipient set replaced
  exactly, decryptability follows the new set, empty list refused — run against
  both fake and real store.
- **Export/import round trip** in `test/suite`: export from installation A,
  import into an empty root with only the recovery key, assert the installation
  ID survives, the secrets decrypt to identical values, the old machine key is
  gone and the new one works.
- **The recovery scenario end to end**, which is the test this RFC exists for:
  init → backup → destroy the root entirely → import from export + recovery key
  → restore from backup → assert data and secrets match.
- **`secret edit`** driven by a fake editor (`EDITOR=/bin/sh -c '…'`): changed
  secrets written, unchanged ones untouched, only dependent services restarted,
  the temp file gone afterwards — including on a non-zero editor exit.
- **Rotation check** unit tests at the period boundary.
- **Wizard**: asserted to be skipped entirely when `--yes` or non-TTY. The
  interactive path is not golden-tested; the equivalent-command output is.

## 7. Docs

- README gains a "Losing a machine" section giving the recovery sequence as an
  ordered list. This is the documentation the feature exists to make true, and
  it should not be written until the test in §6 passes.
- The recovery-key generation output already warns to move the key off the
  machine; the wizard repeats it at the moment one is created.
- CHANGELOG: `Added` for export/import, edit and the wizard; a `Security` entry
  for the editing path, since it defines where plaintext may exist.

## 8. Out of scope

- **Automatic offsite export.** Uploading an export somewhere is a backup-target
  concern; the manager coordinates rather than transports. Named as the reason
  export writes a single self-contained file an operator can move.
- **Re-keying on a schedule.** Rotation is reported, not performed. Automatic
  rotation of a live credential without a coordinated restart is how a product
  goes down at 3am.
- **Editing the recipient list in `secret edit`.** Recipients have their own
  subcommand with refusal rules; letting an editor session drop the machine key
  would route around them.

## 9. Risks

- **`secret edit` is the one place plaintext hits a filesystem.** Mitigated by
  tmpfs, `0700`/`0600`, an unconditional deferred removal, and overwriting
  before unlink. Residual risk accepted and stated in the docs: on a host where
  `/run` is not tmpfs, this writes plaintext to disk briefly. `doctor` gains a
  check for exactly that.
- **Import restoring the original installation ID is deliberate and looks
  wrong.** It is what makes existing backups restorable. Two live machines
  sharing an ID would be a real problem, so import prints the ID and states that
  the source machine must be decommissioned.
- **The wizard becoming the real interface.** If flags start lagging behind it,
  the scriptability guarantee erodes. Mitigation: the wizard sets the same
  `InitOptions` struct the flags do, and prints the equivalent command every
  time.
- **A rotation warning nobody can act on.** If a release declares a period for a
  secret with no generator, `rotate` cannot help. The check names `secret set`
  in that case instead.

## 10. Decisions

| # | Decision |
| --- | --- |
| 1 | Import restores the original installation ID. Backups and restore key on it, so a rebuilt machine with a fresh ID could not restore its own backups — the entire point of the recovery path. |
| 2 | Import generates a new machine identity and removes the old one. A decommissioned host must not retain the ability to decrypt. |
| 3 | Export contains no machine data. `backup` owns data; two artifacts with different freshness would force the operator to reason about which is current. |
| 4 | `secret edit` works on a plain name/value mapping, never the SOPS envelope. An operator cannot corrupt the encryption metadata by editing it. |
| 5 | The edit temp file lives in the existing tmpfs render directory, not `os.TempDir()`. `/tmp` is frequently not tmpfs, and a crash there leaves plaintext on disk. |
| 6 | Rotation overdue is a warning, never a failure. `doctor`'s exit code is a monitoring signal; failing it over a policy recommendation trains operators to ignore it. |
| 7 | `ReencryptFor` replaces the recipient set wholesale, distinct from add/remove which preserve it. Recovery replaces a machine key that no longer exists. |
| 8 | The wizard is a front-end over flags, never a separate path. It sets the same options struct and prints the equivalent command line. Where the two could diverge, the flags are correct. |
| 9 | The README recovery section is written only after the end-to-end recovery test passes. Documenting a recovery procedure that has not been executed is how operators discover it does not work during an incident. |

## 11. Phasing

- **P1** — `ReencryptFor` on the port, both implementations, contract cases.
  Small, and unblocks the rest.
- **P2** — `installation export` / `import` plus the end-to-end recovery test.
  This is the milestone's reason for existing.
- **P3** — `secret edit`, and the `doctor` check for a non-tmpfs `/run`.
- **P4** — rotation reporting.
- **P5** — the `huh` wizard. Deliberately last: it is the only part that adds a
  dependency and changes nothing about what the tool can do.
