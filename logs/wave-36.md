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
