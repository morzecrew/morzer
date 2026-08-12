# RFC 0027 — Desired state in a repository

- **Status:** 🚧 In progress — **P1 shipped 2026-08-12** as
  `morzer installation describe`; §12.1's question was answered first and the
  answer was yes. P2 remains gated on §6, which the project cannot manufacture,
  and is not scheduled.
- **Scope:** One file that fully determines an installation, written by
  `installation export --declarative` and reviewable, diffable and committable
  because secrets are references by construction. Covers the schema, the
  round-trip discipline, and — specified but not proposed — what `apply -f` would
  have to be if it were ever built. Deliberately not reconciliation, not a pull
  loop, not templating or overlays, not multi-machine anything, and not a
  replacement for `installation.yaml`.
- **Related:** [`internal/schema`](../internal/schema) (the JSON Schema
  generator), [`internal/lifecycle/ops/recovery.go`](../internal/lifecycle/ops/recovery.go)
  (`installation export`), [0007](0007-operator-parameters.md) (parameters, and
  the defect that motivated them), [0016](0016-update-checking-and-unattended-updates.md)
  (`mode`, installation settings, the gate on unattended action),
  [0017](0017-recovery-artifacts.md) (the one-producer round-trip discipline),
  [0020](0020-several-installations-on-one-machine.md) (`--config` precedence)
- **Origin:** Drafted 2026-08-10; adopted 2026-08-12 with §12's measurements taken
  against the code.

---

## 1. Read this section before the rest

**This is the weakest of the five directions, and the draft is written to be
mostly refused.**

It has the highest ratio of conceptual appeal to demonstrated need. It competes
with Ansible for a job Ansible does adequately. Its most attractive phases are its
most dangerous ones. And it is the direction where scope creep enters the project
disguised as elegance: "the installation is already desired state, we just need a
file" is true, and it is also the first sentence of a much larger program.

So the phasing is deliberately inverted. **P1 is the only phase this RFC proposes
building.** P2 and beyond are specified so that the boundary is written down, and
gated on a condition stated in §6 that the project cannot fake.

## 2. Problem

0007's motivating find is the relevant history: an operator could change *nothing*
post-`init`. `installation.yaml` *"claimed edits took effect and was never read
back"*, and `Installation.Settings` reached templates with no writer. The fix was
`morzer config` as a journalled operation — the right fix, and it left the project
with a specific shape:

**The manager has a desired-state concept, and it is spread across three places.**
The state directory holds what happened. `installation.yaml` is operator-facing
and documented as possibly wrong — there is a `config.installation-file`
diagnostic *because those two drift*, and 0017 found that drift serious enough to
redesignate a whole backup component as forensic rather than recoverable. And the
parameters live behind `config`, whose history is a sequence of invocations.

What does not exist is **one file that fully determines an installation** — which
is what would make an installation reproducible, reviewable, and diffable, and
what would let a machine be rebuilt without a human remembering which `config set`
commands were run in which order.

That is a real gap. It is smaller than it looks, and §3 is the part worth
building.

## 3. P1: `morzer installation export --declarative`

One command. It reads a live installation and writes the file that would recreate
it:

```yaml
morzer: v1
product: acme
release:
  ref: oci://registry.example.com/acme/release
  version: 4.2.1
  digest: sha256:…
mode: prod
parameters:
  http_port: 8443
  site_name: "Acme Internal"
secrets:
  - name: db_password        # reference only — never a value
backup:
  targets: [ { url: "s3://…" } ]   # credentials by reference
notify:
  targets: [ { url: "…", credential: webhook_token } ]
settings:
  update.check: true
```

That is useful **on its own, with no reconciliation attached**:

- It makes an installation self-documenting. The answer to "what is this machine"
  becomes a file rather than four commands.
- It is reviewable and diffable. Two machines that should match can be compared
  with `diff`.
- It is committable — because secrets are references, by construction (decision 2).
- It gives 0026's roster a source, and 0024's support bundle a component.
- It is the honest artifact that `installation.yaml` was mistaken for.

And it is a **one-way door you can walk back from**: nothing consumes it yet, so
nothing depends on its shape being right the first time.

## 4. P2 and beyond, specified but not proposed

`morzer apply -f morzer.yaml` converges an installation to the file.

### 4.1 It is the same operation

Not a parallel path. The same step engine, the same journal, the same plan, the
same `--dry-run` and config diff. `apply -f` resolves the file into the arguments
`apply` and `config` already take, and everything downstream is unchanged.

**If it needs its own step sequence, this RFC is wrong** — that would mean the
file expresses something the existing operations cannot, which means the file is a
second system.

### 4.2 The file is not a second source of truth

The relationship has to be stated precisely, because getting it wrong recreates
the exact defect 0007 fixed:

- The **state directory** is authoritative for *what happened*. Unchanged.
- The **file** is authoritative for *what should be*, and only during an `apply -f`.
- `status` reports both and **names the difference**.

This promotes the existing `config.installation-file` drift diagnostic from a
warning about an accident to a designed, reported relationship. The file is never
read implicitly. A stale file on disk changes nothing until someone passes `-f`.

### 4.3 Immutable fields are refusals, not no-ops

`mode` is immutable (0016 decision, and immutable rather than one-way because both
transitions are dangerous in different shapes). Installation id is immutable.
Runtime kind would be, if [0023](0023-runtimes-beyond-compose.md) ships.

A file that appears to set one of those and silently does not is **0007's defect,
exactly**: a document that claims edits take effect and is never read back. So
setting them to a different value is a named refusal that says which field and
what the current value is. Setting them to the *same* value is fine, because a
round-tripped export must apply cleanly.

### 4.4 Secrets are references, permanently

Not "values are discouraged". Not "values are supported but warned about". The
schema has no place to put a value.

A repo-committable file that *can* hold a secret *will* hold a secret, and the
whole of 0003's careful bounding of the one place plaintext touches a filesystem —
a tmpfs directory overwritten however the editor exits — is undone by one
convenience field. `secret set` remains the only writer.

### 4.5 Nothing pulls

The manager does not watch a repository, clone one, or poll one. Reconciliation is
**invoked** — by a human, or by CI over ssh, from a machine that already holds the
credentials.

0016 built the project's one scheduled network operation and put an enormous gate
around it: off by default because it is a phone-home, `require_signature` and a
pinned key mandatory before auto-apply, a `rollback_safe` declaration and a schema
range rather than a version-range proxy. A git-pull loop is a second scheduled
network operation with a second credential on the machine and none of that gate,
and it would arrive as a small feature.

If unattended convergence from a repository is ever wanted, the honest design is
to extend 0016's gate to cover it — not to build a parallel one here.

## 5. Decisions

| # | Decision | Grade | Why |
| --- | --- | --- | --- |
| 1 | P1 ships alone | LOCKED | §1, §6. An export nobody applies back is still a machine that documents itself. |
| 2 | Secrets are references; the schema cannot express a value | LOCKED | §4.4. A committable file that *can* hold a secret will. |
| 3 | `apply -f` is the existing operation with a different argument source, or it is not built | LOCKED | §4.1. Its own step sequence would mean the file is a second system. |
| 4 | Immutable fields are named refusals; a round-tripped export applies cleanly | LOCKED | §4.3. A file that appears to set a field and silently does not is 0007's defect exactly. |
| 5 | No pull, no watch, no schedule | LOCKED | §4.5. A git-pull loop is a second scheduled network operation with none of 0016's gate. |
| 6 | The file participates in 0020's precedence chain rather than introducing a fourth selector | LOCKED | `--config` > `--product` > `MORZER_PRODUCT` > discovery. |
| 7 | `export --declarative` and any future `apply -f` share one schema with one producer, pinned by a round-trip test | LOCKED | 0017 established this: without a byte-for-byte equivalence test, *"one producer" decays quietly back into two.* |
| 8 | Where the command lives | LOCKED | Resolved 2026-08-12 as `installation describe`, a verb of its own. `export` produces an artifact whose purpose is to be unreadable without a recovery key; this produces one whose purpose is to be read and committed. Two artifacts differing in exactly the property an operator cares about, behind one verb and a flag, is how somebody publishes the wrong one. |

## 6. The gate on P2

P2 is not deferred pending capacity. It is gated on:

> **A user who is not the author has exported a declarative file, kept it in
> version control, and asked to apply it back.**

That is a condition the project cannot manufacture — but unlike the vendor gate
0024 had to dissolve, this one does not need dissolving, because **P1 is complete
and useful without it.** An export that nobody applies back is still a machine
that documents itself.

If the gate is never met, that is information: it means the file was wanted as
documentation and not as an interface, and the correct outcome is that P2 is never
built. Recording that in advance is what stops sunk cost from arguing for it
later — the same mechanism 0023 §6 uses for its escape hatch, and the same lesson
0002 §13 records.

## 7. Non-goals, and what reopens each

- **Multi-machine anything.** One file, one installation, one machine. Reopened by
  nothing; that is 0026's territory and 0026 refuses it there too.
- **Templating, inheritance, overlays, environments.** This is where every
  configuration format goes to die. *Reopens if:* someone demonstrates two
  installations that genuinely differ only in three values and cannot be handled
  by two files and a diff — and even then, the answer is probably still two files.
- **A repository layout convention.** The file is a file. Where it lives is the
  operator's business.
- **`morzer diff -f`** as a separate command. `apply -f --dry-run` already is it;
  0007 built the config diff and 0019 moved every report behind one renderer.
- **Reconciliation loops, drift auto-correction, continuous convergence.** §4.5.
- **Replacing `installation.yaml`.** The operator-facing file stays what it is.
  Two files with clearly different jobs is better than one file with two,
  which is how the drift diagnostic came to exist.

## 8. Tests

Decision 7's round-trip is the whole test story for P1: export a live
installation, recreate from the file, export again, and require the two exports to
be byte-identical. 0017 shows that without it the single-producer claim decays.
The acceptance deployment is the fixture, because it is the only installation with
every field populated.

## 9. Docs

A reference page for the schema, generated alongside it. §10 notes the word
"declarative" is probably wrong for the page title.

## 10. Phasing

- **P1 — `installation describe`.** ✅ Shipped 2026-08-12. The document type,
  the command, the generated JSON Schema (`schemas/selfhost-v1alpha1-installation.json`
  — §12.3's guess held: a second constructor, not a second generator), the
  completeness and stability tests, and a reference page. Ships alone.
- **P2 — `apply -f`** — *gated on §6, not scheduled.*
- **P3 — Refusals, `status` drift reporting** — with P2.

## 11. Risks

- **That P1 becomes an argument for P2 by momentum.** §6 is the mitigation and it
  only works if it is honoured when the file turns out nice.
- **That the schema is a third representation.** Decision 7's round-trip test is
  the only thing preventing it, and 0017 shows the test is not optional.
- **That it reads as a Kubernetes gesture.** The word "declarative" carries
  freight this project has spent 22 documents avoiding. The reference page should
  probably not use it; "an installation, as a file" says what it is.

## 12. What this draft owed a measurement

Taken 2026-08-12.

1. **Whether a live installation is fully expressible.** **Measured 2026-08-12:
   yes.** Eleven `Installation` fields are carried by the document, three are
   excluded with stated reasons — `schema_version` (state bookkeeping),
   `created_at` (history, not a choice), `providers` (declared by the release) —
   and none is unaccounted for. The accounting is read off the structs by
   reflection rather than from a list, so a field added later and forgotten
   fails the build. P1's central claim holds, which is what made it worth
   shipping.
2. **What 0016's `config` dot-dispatch actually covers.** Not measured. The split
   between parameters and installation settings determines the file's shape, and
   the index records that settings were bolted on when `update.check` shipped
   with no way to be enabled — so the split is known to be uneven.
3. **Whether the JSON Schema generator is reusable for a second document type.**
   **Looks reusable.** [`internal/schema/schema.go`](../internal/schema/schema.go)
   exposes `Manifest()` over a shared `render()` helper, with
   `tools/schemagen` as a thin caller — a second document type appears to be a
   second constructor rather than a second generator. Confirm when writing it.
4. **Whether `installation export` is the right home.** Unresolved, and now
   decision 8. Attaching a second, differently-shaped export to a command whose
   export is an encrypted identity bundle is a collision waiting to happen;
   `installation describe` is the likelier answer and 0019 owns the question.

## 13. Amendments

*(Empty.)*
