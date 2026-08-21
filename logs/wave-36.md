# Wave 36 · The field with no readers

Executed RFC 0027 against branch `wave-36-providers-and-the-log-decomposition`.
`Installation.Providers`, carried out of wave 35 as "RFC 0027's question, not
gated on P3".

**Drift count: 0.**

Where building this disagreed with the design for it, written at the moment it
happened. Nothing here is revised afterwards to agree with what was later
settled, and nothing here has been folded back into the RFC's own text. The rows
proposed below are put forward for the author to accept or refuse; execution does
not write them into a decision table itself.

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

**Deliberately not applied:** RFC 0023 decision 11 is `LOCKED` and settles that
the runtime is recorded in a new `Installation` field *rather than* in
`Providers`. This change touches the area that row governs and does not
contradict it — row 11's own rationale describes `Providers` as a field with
"zero writers and zero readers, carrying two contradictory documented meanings",
which is the argument for removing it. There is no entry citing D-11 because
there is no contradiction to report, and the block format records contradictions
rather than agreements. `tasks/wave-36.json` therefore declares the row without
`paths`, so the silence check reports it as **skipped** rather than as passed.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-08-21T08:30:48Z
attempt: 1
claim: Installation.Providers has no production reader or writer and four mutually contradictory documented meanings, and no decision row in any RFC says whether the field should exist at all
evidence: `grep -rn "\.Providers" --include="*.go" internal/ test/ cmd/ | grep -v "m\.Providers\|Manifest\.Providers"` — one hit, internal/domain/parameter_test.go:226, a test fixture
action: decided
proposal: ASSUMED — `Installation.Providers` is removed. A serialised field that nothing reads, documented in four incompatible ways, is one an older manager can find, recognise and act on wrongly; that is the hazard RFC 0023 decision 11 accepted when it refused to record the runtime there.
```

The four meanings, none of which agrees with another: `describe.go` calls it
"declared by the release manifest, not chosen by the operator"; a repair test
calls it "from the flags"; `sandbox_test.go` calls it "which adapters to use;
names, not endpoints"; and RFC 0027 §12.1 excludes it from the declarative
document because it is "declared by the release". `Manifest.Providers` — a
different field of the same type — is genuinely read and defaulted, and stays.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-08-21T08:31:02Z
attempt: 1
claim: removing a serialised field needs no installation schema bump here, because state is decoded permissively and no code reads the field
evidence: internal/infra/state/state.go:66
action: decided
proposal: ASSUMED — a schema bump is for the read path. Dropping a field nothing reads changes no read, and an older manager meeting state written without it finds the zero value it already ignored. Schemas 9 and 10 were bumped because an older manager would have *acted* on what it read; there is no such reader here.
```

State is read with a plain `json.Unmarshal`, not a strict decoder, so an
installation file still carrying `"providers": {...}` decodes cleanly after the
field is gone. The published document schema is unaffected: it never had the
key.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-08-21T08:31:19Z
attempt: 1
claim: RFC 0027 §12.1 records a measurement that this change makes false — three Installation fields excluded from the declarative document becomes two — and the RFC's prose may not be edited to match what was built
evidence: rfcs/0027-desired-state-in-a-repository.md:257
action: decided
proposal: ASSUMED — §12.1's count is a measurement dated 2026-08-12 and stays true as of that date. The excluded set is now `schema_version`, `created_at`, `signing` and `attestation_salt`; `providers` leaves it by ceasing to exist rather than by being carried.
```

```divergence
decision: unlisted
grade: UNLISTED
class: discovery
at: 2026-08-21T08:31:41Z
attempt: 1
claim: the exclusion map that guards RFC 0027's central claim is checked in one direction only — a field added and forgotten fails the build, a reason naming a field that no longer exists does not
evidence: `sed -n '228,237p' internal/domain/describe.go` — accountFields ranges over the installation's fields and looks each up in reasons; nothing ranges over reasons
action: decided
proposal: ASSUMED — the accounting is checked in both directions. A reason naming a field that no longer exists is a claim about a document that has moved on, which is exactly what `sandbox_test.go` already refuses for its own table.
```

Found by removing a field rather than by adding one, which is why it survived:
every previous change to `Installation` moved in the direction the check covers.
`sandbox_test.go` classifies the same struct and asserts both directions —
*"%s is classified and no longer exists; the table is describing a document that
has moved on"* — so the two sibling accountings disagreed about their own
completeness, and only the stricter one would have caught this change.

## Self-audit — 2026-08-21

Scope: the whole wave — one field removed, four call sites, one new detection
branch, and the log decomposition committed ahead of it on the same branch.

**Drift count: 0.** No entry here is classed `drift`, and nothing this wave
built contradicts a decision any document settled. The count is repeated rather
than assumed unchanged, because a group that does not restate it cannot be told
from one whose author never looked.

**Sabotage sweep: 2 mutations, 1 killed, 1 survived.**

| # | Mutation | Result |
|---|---|---|
| S1 | Put `Providers` back on `Installation` | **Killed.** `TestEveryInstallationFieldIsAccounted` and `TestEveryInstallationFieldIsClassifiedForASandbox` both fail, naming the field and each of its four sub-fields. |
| S2 | Replace the new reverse-direction assertion's body with `_ = real[field]` | **Survived**, and the why is the finding rather than a shrug. |

S2's survival is the expected shape for a detection branch: it is the only code
that checks that direction, so nothing else can fail when it is gutted. That is
exactly why it was driven **red** before being believed — the stale `Providers`
key was reintroduced and the test failed with the message it exists to print.
A green-only run of that test would have proved nothing about it, which is the
trap `TestAFieldNobodyAccountedForIsReported` was written for one directory
over, against the same detector, in the opposite direction.

**Verification.** `just ci` green at 86.6% (floor 84). Container lane green,
including `./test/suite` at 187.8s — the package holding `repair_test.go`, which
this wave edited. `schemagen` regenerates all four schemas byte-identical, so
the published surface is provably untouched.

**What this wave did not find.** Nothing in `Manifest.Providers`, which is read,
defaulted and validated, and which shares the `Providers` type with the field
just removed. The type stays because the manifest still needs it; a reader
meeting `domain.Providers` now finds exactly one struct that embeds it instead
of two, one of which meant nothing.

## Carried into the next unit

- **`rfc_index.py check` is not wired into `ci`.** The updated `rfc-writer`
  declares it as the `rfc-index` gate and it fails against this collection:
  **369 decision rows across 23 RFCs carry no grade**, and the six tables that
  do grade put `Grade` in the third column where the checker reads the second.
  Making it green means grading 369 historical decisions, which is a unit of its
  own and not a gate-wiring step. `log-check` is wired; this is not.
- **A plan does not validate the bundle it plans against** (D-055, wave 34).
- **`FieldRemovalRelease` is a single-member design with no members** (D-052).
- **`saveInstallation` writes its report before the state store** (wave 31), the
  oldest item in this collection.
- **`release.draft: true` means a human publishes**, and that human is the last
  reader of the notes (release 0.3.0).

## Correction — 2026-08-21, after the entries above

**The paragraph at line 29 names `tasks/wave-36.json`, and that file no longer
exists.** It is left standing rather than edited: this log is append-only, and a
record quietly adjusted to match what is true now is worth less than one that
shows what was believed when it was written.

What was wrong with it. The task file declared three decisions and no `paths`,
and `paths` is the only thing the checker reads a task file for. Run with it and
without it, the checker returned the same verdict and the same coverage — three
decisions skipped either way, no silence checked either way. It was ceremony
standing where a gate was supposed to be.

It also carried a second job it was never designed for: `just log-check` used
the presence of `tasks/*.json` to decide which logs to check. That made checking
**opt-in**, so a log nobody remembered to declare went unchecked, and the
failure failed open. Demonstrated rather than reasoned about: a deliberately
broken log with an illegal `LOCKED`/`departed` pair, a sentence for evidence and
a wrong drift count passed silently under that rule, and fails with three
problems under the one that replaced it.

The gate is now default-on for every log in `logs/`, and skips only files
carrying the migration header that says they predate this format — so the
exclusion lives in the file it applies to, and nothing outside a log decides
whether that log is checked.

**The substance of the paragraph stands.** RFC 0023 decision 11 is `LOCKED`,
this wave touched the area it governs, and there is still no entry citing it,
because there is still no contradiction to report and the block format records
contradictions. Confirmed against the checker's own rule while making this
change: `check_silence` matches an entry to a decision by exact id (`ident not
in logged`), so declaring D-11's paths would demand an entry citing D-11 — and
the only honest one would say that nothing disagreed, which is not what an entry
is for. That limitation was never the task file's doing, and removing it changes
nothing about it.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-08-21T09:42:10Z
attempt: 1
claim: the published page listing what `installation describe` leaves out is a hand-written copy of the code's exclusion map, and it disagreed with it in three of five rows while claiming to be machine-maintained
evidence: `git show HEAD:pages/docs/reference/installation-commands.md | sed -n '276,282p'` — lists schema_version, created_at, providers; the map holds SchemaVersion, CreatedAt, Signing, AttestationSalt
action: decided
proposal: ASSUMED — the page's exclusion table is checked against `installationFieldsNotDescribed` by `tools/docscheck`, the way the support-bundle inventory and the command index already are. Until it is, the table is hand-maintained and the page should not claim otherwise.
```

Only one of the three was this wave's doing. `providers` went stale when the
field was removed; `signing` and `attestation_salt` were **never documented at
all**, and `attestation_salt` is the one exclusion with a security reason — a
page explaining what a committable file leaves out, which omits the field whose
whole reason for being left out is that publishing it would make an attestation
digest brute-forceable.

The sentence under the table said *"The list is not maintained by hand: a field
added to an installation and not accounted for one way or the other fails the
build."* True of the map in the source. This table is a copy of it, and the copy
is exactly what nothing checks — so the claim that made the table trustworthy
was the reason nobody re-read it. Corrected to say what is actually guaranteed,
which is now both directions in the code (see the entry above) and neither of
them here.

**Drift count: 0.** The stale row is a consequence of this wave's removal rather
than a departure from a decision: nothing settled that the page carried this
table, and `docs-check` has no rule that reads it.
