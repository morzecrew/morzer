# RFC 0058 — Values a document gives the release an installation updates to

- **Status:** 📝 Draft — proposed 2026-11-12, and ruled by the author the
  same day: decisions 1–4, 6 and 12 as recommended. Nothing is built.
- **Scope:** Lets a desired-state document
  ([RFC 0057](0057-desired-state-an-installation-pulls-and-applies-to-itself.md))
  give values for the parameters and secrets that a staged release manages and
  the installed one does not. A fleet that follows a channel can then cross a
  release that adds a managed parameter with no default, or renames one,
  without an operator at each machine running `update --set`
  ([RFC 0007](0007-operator-parameters.md) decision 12). The machine keeps
  such a parameter as *pending*, apart from the recorded values. The next
  update to a release that declares it takes it as if it were given with
  `--set`. **Contract changes:**
  - installation state gains a pending section, with a schema bump;
  - the fleet row names the pending names and the staged release.

  No manifest or document schema changes: a document is still
  [RFC 0056](0056-one-change-to-settings-parameters-and-secrets.md)'s change
  document. **Not covered:**
  - settings, such as an `installation.profile` that only the next release
    declares;
  - a local `config set --for`, the local half of #346's option 2;
  - a sync that installs a release;
  - bringing back values that a later `rollback` retires.
- **Related:**
  - [RFC 0057](0057-desired-state-an-installation-pulls-and-applies-to-itself.md)
    §5.6, §5.8 and decisions 5, 24, 25, 29 and 37;
  - [RFC 0056](0056-one-change-to-settings-parameters-and-secrets.md) §5.2
    and decision 5;
  - [RFC 0007](0007-operator-parameters.md) decisions 12–14;
  - [RFC 0016](0016-update-checking-and-unattended-updates.md), unattended
    updates;
  - code: [`ops/sync.go`](../internal/lifecycle/ops/sync.go),
    [`ops/config_apply.go`](../internal/lifecycle/ops/config_apply.go),
    [`ops/update.go`](../internal/lifecycle/ops/update.go),
    [`ops/channel.go`](../internal/lifecycle/ops/channel.go),
    [`domain/release.go`](../internal/domain/release.go) and
    [`domain/fleet.go`](../internal/domain/fleet.go).
- **Discussion:** [#346](https://github.com/morzecrew/morzer/issues/346),
  option 2; #347 and #348 built option 1.

---

## 1. Summary

The sync checks a document against the installed release, as it does today.
Some names that release does not manage may be ones the **staged** release
manages. The sync no longer refuses those. It keeps their parameter values
as pending, checked against the staged release's declarations, and it
stores their secrets. The next update takes the pending values for the
names its release declares, as if they were given with `--set`. That update
can be a manual `update` or the unattended tick. Then the update clears
pending. A name that neither release manages is still refused, and the
whole document with it. The fleet row names what is pending and which
release is staged, so the authority knows when to send.

## 2. Motivation

#347 gave `update` a way to take a value, and made an update that lacks one
refuse before any downtime. Under RFC 0057, that leaves a fleet with no path
forward. Take release N+1, which adds a managed parameter `X` with no
default. dotdash-canary 0.7.0 is this case: it renames every one of its
target's parameters, so none of the new names can have a default. On each
machine running N, with sync and `update.auto_apply` on:

1. The authority publishes a document naming `X`. N does not manage `X`, so
   the sync refuses the document whole ([`sync.go:382`](../internal/lifecycle/ops/sync.go)).
   RFC 0057 decision 37 lets a newer serial be tried, but every newer
   serial that names `X` is refused the same way while N is installed.
2. The channel stages N+1. The tick's assessment finds no value for `X`.
   The tick leaves N+1 staged, with the reason "waits for
   `morzer update --to N+1 --set X=<value>`"
   ([`channel.go:690-711`](../internal/lifecycle/ops/channel.go)).
3. Only an operator at the machine can run that command, with a value that
   the authority holds. For a fleet, that is a person at every machine, and
   the whole reason for sync was to need none.

The rename without a default has the same shape. A rename with a default is
quieter. The update installs N+1 and retires the old name's value. The new
name runs on its default until the authority's next document, so there is
a window running on a value nobody chose.

Secrets follow the same pattern. A local `secret set` stores a name the
running release does not declare (#346), but a document cannot.
`config apply` refuses a secret the release does not declare
([`config_apply.go:420-422`](../internal/lifecycle/ops/config_apply.go)).
An update to a release that adds a required secret with no generator is
then refused by `load-secrets`
([`apply.go:353`](../internal/lifecycle/ops/apply.go), and
[`update.go:662-664`](../internal/lifecycle/ops/update.go)).

## 3. Current state

- **The sync validates against the installed release only.**
  - `checkManaged` refuses a document that names anything the release's
    `managed` list does not allow, whole, and reports the names
    ([`sync.go:380-397`](../internal/lifecycle/ops/sync.go)).
  - `config apply` then refuses a parameter the release does not declare
    ([`config_apply.go:379-381`](../internal/lifecycle/ops/config_apply.go)).
    An unset is the exception: it may name a recorded stale value (line
    373-375).
- **A refused serial blocks nothing newer** (RFC 0057 decision 37;
  [`sync.go:255-261`](../internal/lifecycle/ops/sync.go)). Every serial newer
  than both the last applied and the last refused is tried.
- **An update takes values only from `--set`.** `withUpdateParameters`
  ([`update.go:475-503`](../internal/lifecycle/ops/update.go)):
  - merges the given values over the recorded ones;
  - refuses before the backup when `MissingValues` finds a declared
    parameter with no value and no default;
  - leaves recording to `record-parameters`
    ([`update.go:738`](../internal/lifecycle/ops/update.go)), which runs
    after the converge.

  A resume is held to the values its run was given, by digest (RFC 0007
  decision 14).
- **Recorded values must all be declared.** The update drops values the new
  release does not declare from its in-memory resolution, "every consumer
  validates the whole recorded map, so one retired name would refuse the
  converge" ([`update.go:612-623`](../internal/lifecycle/ops/update.go)).
  `retire-parameters` makes that durable. A value for a name the installed
  release does not declare therefore cannot wait in
  `Installation.Parameters`
  ([`installation.go:391`](../internal/domain/installation.go)).
- **The staged release is known on the machine.** `UpdateCandidate` records
  what the channel last pointed at. `IsStaged` holds when its bundle is on
  disk, verified and not refused
  ([`release.go:183-238`](../internal/domain/release.go)). The record is
  derived and disposable, so losing it costs one fetch. It is not installation
  state.
- **The fleet row reports the installed release only:** `version`
  ([`fleet.go:80`](../internal/domain/fleet.go)). Its `sync` block carries the
  applied and refused serials, and a refusal's step, class and names
  (lines 129-143). The staged release does not appear in it.
- **`update --set` skips RFC 0057 decision 29.** While sync is enabled, that
  decision refuses a local `config set` or `config apply` naming a managed
  parameter unless `--force` is given. `update.go` has no such check, so
  `update --set` of a managed name goes through on a synced machine. #347
  left this gap, and §5.3 closes it.

## 4. Goals / Non-goals

**Goals**

- An authority can move a fleet across a release that adds or renames a
  managed parameter with no default. It publishes documents, and the
  channel and the tick do the rest.
- The same for a managed secret that the next release adds without a
  generator.
- Nothing about which release a machine installs, or when, moves to the
  authority.
- A name that no release on the machine manages is still refused, so a typo
  is still caught.

**Non-goals**

- **Choosing the release.** The channel and `update.auto_apply` keep that.
  Documents may not touch `update.*` (RFC 0057 decision 5, `LOCKED`).
- **A local pending value.** A local operator gives the value to the update
  that needs it, with `update --set` (RFC 0007 decision 12). Decision 12 here
  asks the author to confirm this.
- **Settings.** No release declares `installation.profile` or
  `installation.domains` for its successor, and a profile the next release
  adds is chosen with `update --profile`.

## 5. Design

### 5.1 Which release a name is checked against

A sync sorts each name in a document by the releases that manage it:

| Managed by the installed release | Managed by the staged release | Result |
| --- | --- | --- |
| yes | either | Applied now, as today |
| no | yes | **Pending**: kept for the update (§5.2) |
| no | no, or nothing is staged | The document is refused whole, as today |

A name the installed release declares but does not manage, and which the
staged release manages, is pending too. The value is for the staged release
and is not applied now.

A document is still accepted or refused as a whole. In an accepted document,
every name has one defined fate, and none is applied by halves. "Staged"
means `UpdateCandidate.IsStaged()`. The pending names are checked against
that release's `managed` list, and their values against its declarations,
as `--set` values are checked against the release being installed. A value
of the wrong type refuses the whole document.

### 5.2 What pending holds

```yaml
# installation state
pending:
  release: 0.7.0          # what the values were checked against; reported, not binding
  parameters:
    dotdash_server: example.com
```

- **Parameters** go in `pending.parameters`, as parsed values, apart from
  `parameters`. Nothing renders them, and `config diff` and `doctor` ignore
  them.
- **Secrets** go into the secret store, as a local `secret set` of an
  undeclared name puts them there. The store is not tied to a release, and
  the next release finds the secret already present. Its `generate-secrets`
  step then leaves it alone. Decision 4 asks whether secrets belong in
  scope at all.
- **Merge semantics are RFC 0057's.** A name a document mentions is set. A
  name it does not mention is left alone. A name in `unset.parameters`
  removes a pending value.
- **Written with the serial.** The sync's operation records pending in the
  same installation write as the applied values and the serial (RFC 0057
  decision 25). Its undo restores the previous pending. A document that
  holds only pending names still runs that operation, with a record step
  and no converge.

### 5.3 The update takes them

An update to release R, whether manual or the tick's:

- Takes the pending values for every name that R declares, as if they were
  given with `--set`:
  - they are checked against R's declarations;
  - they are counted by the refusal of a missing value;
  - `record-parameters` records them;
  - the journal names them, with their digests (RFC 0007 decision 14).
- Applies to the tick's assessment
  ([`channel.go:690-711`](../internal/lifecycle/ops/channel.go)) too. A
  pending value counts as given there, so the tick installs R.
- Refuses before anything runs when R refuses a pending value, naming the
  parameter. This happens when R is not the release the value was checked
  against. The tick waits on the same reason.
- Clears pending once `record-parameters` has run. The names R declares are
  recorded. The rest are dropped, and the journal names them, as
  `retire-parameters` names what it retires. An update that fails or rolls
  back never reaches that step, so pending is kept.
- Gets the decision 29 check (decision 6). While sync is enabled, `--set`
  of a managed name, or of a name with a pending value, is refused without
  `--force`, naming the authority's key id.

A resume reads pending as it is when the resume runs. It is held to the
digests its run journalled. If a sync changed a pending value in between,
the resume is refused naming it. A fresh update then takes the new value,
because an interrupted update does not block a new operation.

### 5.4 Reporting

- `config list` shows pending values in their own section. Each one carries
  the release it was checked against, and the value, as `config list` shows
  values today.
- `status --json` names the pending names.
- The fleet row:
  - gains `staged`, the version `IsStaged` names, beside `version`;
  - its `sync` block gains `pending`, the names, never a value (RFC 0057
    decision 23).

  With these, an authority knows that a machine has staged N+1, so a
  document naming N+1's names will be taken, and which names are waiting.

### 5.5 Alternatives considered

- **A bridge release, with no change to morzer.** The vendor ships N+½. It
  declares the new names as managed, with placeholder defaults the templates
  ignore. The authority sets the names. N+1 then drops the defaults.
  - It works today.
  - It costs the vendor a release per rename, and a placeholder each
    template has to recognise.
  - A machine whose channel moves past N+½ before a sync lands reaches the
    deadlock anyway.
- **A release-scoped section in the document**, such as
  `next: {release: 0.7.0, parameters: …}`.
  - It is explicit about the target.
  - It changes the document schema, which machines decode strictly
    ([`change_document.go:46-50`](../internal/lifecycle/ops/change_document.go)),
    so an older machine would refuse every document carrying it.
  - The authority has to know which release each machine will stage. A
    machine that stages another release leaves the section unused.
  - §5.1 reaches the same result by asking the machine, which knows.
- **Keeping the ciphertext for the update to read again.** No new state is
  needed. But the update would verify, decrypt and check the expiry of a
  document in the middle of an operation. A document that expired before
  the tick strands the update, exactly as today.
- **A sync that installs the staged release with the document.** The
  authority would then choose when machines update. RFC 0057 decision 5
  keeps that out of documents.
- **`config set --for <release>`** (#346 option 2). This is the local
  version. `update --set` already covers it. The pending section here could
  serve it later, if a local operator ever needs to give a value before
  updating rather than with the update.

## 6. Tests

`test/suite`, against the suite's sync fixture and a staged release that
adds a managed parameter with no default:

- Pending with a staged release:
  - the document is accepted, with the value pending and not rendered;
  - the tick installs, and the value is recorded and journalled by name and
    digest;
  - pending is clear afterwards.
- With no release staged: refused whole, naming the parameter, as today.
- A name that neither release manages: refused whole.
- A pending value whose type the staged release refuses: refused whole.
- A failed update keeps pending. The next tick retries and installs.
- An update to a different release:
  - a pending value it refuses blocks the update before anything runs;
  - a name it does not declare is dropped, and the journal names it.
- `unset.parameters` of a pending name removes it.
- `update --set` of a pending or managed name while sync is enabled is
  refused without `--force` (decision 6).
- A resume after a sync changed a pending value is refused, and a fresh
  update proceeds.
- The rename case: N manages `server`, and N+1 manages `dotdash_server` with
  no default. The document is applied and pending on N. The tick installs
  N+1, and `server` is retired.
- A secret that only the staged release manages is stored. The update finds
  it, and `generate-secrets` leaves it alone.
- The fleet row names `staged` and the pending names. The leak test covers
  `pending`.

## 7. Docs

- `operating/managing-from-elsewhere.md`: a section on crossing a release
  that adds or renames a managed name.
- `reference/desired-state.md`: how a sync sorts names (§5.1), and what the
  fleet row's `staged` and `pending` mean.
- `reference/commands.md`:
  - `update` takes pending values;
  - `--set` of a managed name needs `--force` while sync is enabled;
  - `config list` has a pending section.
- `operating/unattended-updates.md`: a pending value satisfies the reason
  #347 added, so the tick installs.
- `reference/manifest.md`, `managed`: a document may give values for a
  release's managed names once that release is staged, before it is
  installed.

## 8. Out of scope

- **Settings for the next release.** There is no case for them yet. A
  profile the next release adds is chosen with `update --profile`.
- **Values a later `rollback` retires.** A rollback to N retires `X` as
  today, and pending was cleared when N+1 installed. If the channel stages
  N+1 again, the authority's next serial makes `X` pending again. Restoring
  `X` would make `retire-parameters` keep values for a release that may
  never return.
- **Machines updated by hand from a bundle.** With no channel candidate
  there is no staged release, and the operator has `update --set`. Whether
  `update --stage` counts as staged is decision 3's question.

## 9. Risks

- **A value pending for a release that never installs.** The channel can
  move past it. The value then waits until a document unsets it or an update
  clears it, and `config list` and the fleet row show it meanwhile.
- **Misreading the feature as the authority installing releases.** It does
  not: the machine's channel stages, and its own tick installs. The docs say
  this in those words.
- **Pending values are plaintext state,** as parameters are (RFC 0007
  decision 3). A secret goes to the store, never to `pending`.
- **An older manager after a downgrade** reads a newer state file. The schema
  bump (decision 11) refuses it, which is RFC 0007 decision 9's reason.
- **The authority does not read `staged` yet.** `state publish` and
  `fleet ls` gain it with this RFC. An older authority sees the refusals it
  sees today.

## 10. Unresolved questions

Decisions 1–4, 6 and 12 are for the author, each with a recommendation.
Implementation is free to settle the field names, the refusal wording and
how `config list` lays out the pending section.

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `ASSUMED` | Where the values wait. **Recommended:** a `pending` section of installation state, apart from `parameters`, since every consumer validates the whole recorded map (`update.go:612-623`). Alternatives (§5.5): a release-scoped section in the document, which changes its schema and asks the authority to know the target; or keeping the ciphertext for the update to read again, which an expired document defeats. Was `OPEN`; ruled by the author 2026-11-12: as recommended. |
| 2 | `ASSUMED` | Which release a name the installed release does not manage is checked against. **Recommended:** the staged release, `UpdateCandidate.IsStaged()`, by its `managed` list and declarations. A name neither release manages refuses the document whole, as today. Was `OPEN`; ruled by the author 2026-11-12: as recommended. |
| 3 | `ASSUMED` | With nothing staged. **Recommended:** refuse whole, as today. The fleet row's new `staged` tells the authority when to send, and decision 37 of RFC 0057 lets the newer serial through. Alternative: hold the document without recording a refusal until a release is staged or the document expires. That holds every value it carries for the installed release too, and keeps a typo retrying until expiry. Also open: whether a release `update --stage` put in the store counts as staged. Was `OPEN`; ruled by the author 2026-11-12: as recommended. The question of `update --stage` follows from decision 2 as ruled: it stages through the channel's own path, which writes the one `UpdateCandidate` the tick does ([`channel.go:200-246`](../internal/lifecycle/ops/channel.go)), so a release it put in the store counts as staged. |
| 4 | `ASSUMED` | Secrets. **Recommended:** in scope. A secret the staged release manages is stored now, as a local `secret set` of an undeclared name is, and `generate-secrets` leaves a present secret alone. Alternative: parameters only, which leaves the required secret with no generator stuck as #346 was. Was `OPEN`; ruled by the author 2026-11-12: as recommended. |
| 5 | `ASSUMED` | An update takes the pending values for the names its release declares as if they were given with `--set`. They are checked, counted by the refusal of a missing value and by the tick's assessment, recorded by `record-parameters`, and journalled by name and digest. This applies to a manual `update` and to the tick alike. |
| 6 | `ASSUMED` | `update --set` on a synced machine. **Recommended:** RFC 0057 decision 29 applies to it. `--set` of a managed name, or of a name with a pending value, is refused without `--force`, naming the authority's key id. This closes the gap #347 left (§3). Alternative: an explicit `--set` simply wins over pending. That lets a local value contradict the authority without a word until the authority's next document overwrites it. Was `OPEN`; ruled by the author 2026-11-12: as recommended. |
| 7 | `ASSUMED` | An update clears pending once `record-parameters` has run. The names its release declares are recorded, and the rest are dropped and named in the journal. An update that fails or rolls back keeps pending. A release the update installs that refuses a pending value refuses the update before anything runs, naming the parameter. |
| 8 | `ASSUMED` | A later `rollback` retires values as today, and nothing restores them to pending. The authority's next serial does, once the channel stages that release again. |
| 9 | `ASSUMED` | The fleet row gains `staged`, the staged release's version, and its `sync` block gains `pending`, the names only, never a value. RFC 0057 decision 23's leak rule covers both. |
| 10 | `ASSUMED` | Pending is written in the sync operation's record step, with the applied values and the serial (RFC 0057 decision 25), and its undo restores the previous pending. A document holding only pending names runs that step and no converge. |
| 11 | `ASSUMED` | `InstallationSchemaVersion` is bumped, for RFC 0007 decision 9's reason: an older manager would read the file and drop `pending` silently. |
| 12 | `ASSUMED` | Who writes pending. **Recommended:** only a sync. A local operator gives the value to the update that needs it, with `update --set`, so local `config apply` and `config set` keep refusing a name the installed release does not declare. Alternative: local `config apply` writes pending too, through the same operation, which is #346's option 2 at the cost of a second way to give an update its values. Was `OPEN`; ruled by the author 2026-11-12: as recommended. |
| 13 | `ASSUMED` | A resume reads pending as it is when it runs, and is held to the digests its run journalled (RFC 0007 decision 14). A resume after a sync changed a pending value is refused naming it, and a fresh update takes the new value. |

## 12. Phasing

One PR, gated on the author's rulings on decisions 1–4, 6 and 12:

- the sync's sorting (§5.1);
- the pending section and its record step;
- the update and the tick taking pending values;
- decision 6's check;
- `config list`, `status` and the fleet row fields;
- the docs in §7.

Decision 6 fixes a gap that exists today, so it can land first on its own if
the author wants it sooner.
