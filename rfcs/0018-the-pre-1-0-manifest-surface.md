# RFC 0018 — The pre-1.0 manifest surface

- **Status:** 🚧 In progress — P1 shipped 2026-08-08 (the two-pass decode) and
  P2 shipped 2026-08-09 (the five fields). P3 (`bundle.uncompressed_size`)
  follows [0011](0011-bundled-container-images.md)'s schedule, since it is only
  useful once 0011 reads it. Unresolved questions 2 and 3 are resolved into
  decisions 10 and 11. Written as a deliberate one-time sweep. Written as a deliberate one-time sweep
  of the release manifest before the first tag, because manifest decoding is
  strict and **recursive** (verified, §3), so every field at every level is
  frozen the moment a manifest exists that this project did not write.
- **Scope:** Two things. First, a **two-pass decode** so that a manager meeting
  a field it does not know reports the release's `min_manager_version` instead of
  `unknown field` at line 22 — which converts every future field from a break
  with a misleading error into a documented upgrade requirement. Second, the
  fields that should exist before the surface freezes: five that are
  **asymmetries in what is already there**, plus a home for the extraction
  budget [0011](0011-bundled-container-images.md) locked without deciding where
  it lives. Records the fields deliberately **not** added, so they are not
  re-proposed. Does **not** change what any existing field means, does **not**
  touch the secret schema's shape, and does **not** attempt to make the format
  extensible in general — §5.1 is the honest bound on what is achievable.
- **Related:** [`internal/domain/manifest.go`](../internal/domain/manifest.go) ·
  [`internal/domain/parameter.go`](../internal/domain/parameter.go) ·
  [`internal/domain/version.go`](../internal/domain/version.go)
  (`Compatibility`, `CheckUpgrade`) ·
  [`internal/release/load.go`](../internal/release/load.go) (`ParseManifest`,
  `checkReferencedFiles`) ·
  [`pages/docs/reference/manifest.md`](../pages/docs/reference/manifest.md) ·
  [0011](0011-bundled-container-images.md) (`images.from`, the extraction
  budget) · [0016](0016-update-checking-and-unattended-updates.md)
  (`database_schema_produces`, and the strict-decoding cost this responds to) ·
  [0013](0013-bundle-authoring-experience.md) (the scaffold that will emit these
  fields) · [0002](0002-rich-terminal-renderer.md) / [0007](0007-operator-parameters.md)

---

## 1. Summary

Manifest decoding uses `DisallowUnknownField`, and it applies at every nesting
level. Once a manifest exists that this project did not write, every field is
permanent and every new one is a hard break.

Nothing has been released. This RFC spends that window once: it makes the
break *legible* when it eventually happens, and it adds the fields whose absence
is an inconsistency rather than a missing feature — a parameter that cannot be
required while a secret can, a `requirements` block that bounds memory and disk
but not CPU, a release-notes file every other bundle file would have to declare.

## 2. Motivation

**The freeze is real and it is recursive.** Verified against the tree at
`5440081`, by adding one unknown key under an existing section of
`testdata/bundle`:

```
$ morzer release verify ./bundle
error: …/manifest.yaml is not a valid manifest:
  [22:3] unknown field "future_field"
    21 | runtime:
  > 22 |   future_field: hello
```

So there is no reserved area and no nesting trick: a new section bought today
gives its *children* no protection tomorrow. This matters because it is the
opposite of what most formats do, and an implementer may assume otherwise.

**The error is the wrong one.** `min_manager_version` exists on `Compatibility`
and is checked by `CheckUpgrade` ([`version.go:305`](../internal/domain/version.go))
— which runs on an **already-decoded** manifest. The decode fails first, so the
mechanism designed to say "you need a newer manager" can never speak. An
operator whose vendor shipped a release using a field their manager predates is
told about a typo.

**Three asymmetries in the current surface**, each of which is a vendor unable
to say something the format nearly lets them say:

- A secret can be `required`; a parameter cannot. A parameter with no default
  resolves silently to the empty string with source `release`
  ([`config.go:58`](../internal/lifecycle/ops/config.go)), so "the operator must
  choose this" is inexpressible — despite parameters existing precisely to be
  operator choices ([0007](0007-operator-parameters.md)).
- `requirements` bounds `memory` and `disk` and not CPU.
- A health check has a `timeout` and no grace period, so a product that takes
  ninety seconds to become ready and a product that is dead are distinguishable
  only by making the timeout long enough to delay noticing the second.

**And one file that would be the exception to a rule the format otherwise
keeps.** Every path a bundle ships is *declared* and existence-checked —
templates, hooks, the secret schema, Compose files — with a declared-but-missing
file a validation error ([`load.go`](../internal/release/load.go),
`checkReferencedFiles`). `RELEASE.md` would be the only one found by convention,
and [0002 P5](0002-rich-terminal-renderer.md) and
[0016 §5.7](0016-update-checking-and-unattended-updates.md) now depend on it
existing.

## 3. Current state

The complete surface, verified against `5440081`. Both documents decode with
`yaml.Strict()` and `DisallowUnknownField`.

| Section | Fields |
| --- | --- |
| top level | `api_version`, `kind`, `extensions` |
| `metadata` | `name`, `version`, `description`, `vendor` |
| `providers` | `runtime`, `secrets`, `backup`, `health` — each `{name, version}` |
| `runtime` | `project`, `files`, `profiles` |
| `requirements` | `architectures`, `os`, `tools`, `memory`, `disk`, `ports` |
| `parameters.<name>` | `type`, `default`, `description`, `values`, `services` |
| `images` | `<name>: <ref>` |
| `configuration[]` | `template`, `target`, `mode` |
| `secrets` | `source`, `render_to`, `schema` |
| `operations.<name>` | `kind`, `service`, `command`, `timeout` |
| `backup.volumes.<name>` | `consistency` |
| `health.checks[]` | `name`, `type`, `url`, `address`, `command`, `timeout` |
| `compatibility` | `database_schema_min`, `database_schema_max`, `rollback_safe`, `min_manager_version`, `upgrade_from` |
| `retention` | `releases`, `backups` |

The secret schema, a second strict-decoded document: `api_version`, and
`secrets[]` of `{name, description, required, generator{kind,length,alphabet},
file, services, rotation_period}`.

Three facts that shaped this RFC:

| Fact | Evidence |
| --- | --- |
| Strict decoding is **recursive** | The `verify` run in §2 |
| `min_manager_version` is read **after** decoding, so it cannot explain a decode failure | [`version.go:305`](../internal/domain/version.go) (`CheckUpgrade`) |
| A parameter with no default resolves to `""` with source `release` — it is not required, it is empty | [`config.go:58`](../internal/lifecycle/ops/config.go) |

**And one thing that is working correctly, recorded because it looks broken.**
`extensions` is consumed by nothing outside its own namespace validation
([`manifest.go:649`](../internal/domain/manifest.go)), which reads as a dead
field. It is not. Its purpose is to be *tolerated*: a vendor annotating a
manifest with their own namespaced data would otherwise have it rejected as an
unknown field, and their tooling reads `manifest.yaml` directly. "Passed through
untouched" ([`manifest.md:279`](../pages/docs/reference/manifest.md)) means not
interpreted, not delivered somewhere — accurate, and easy to misread. §5.4 draws
the one useful consequence.

## 4. Goals / Non-goals

**Goals**

- A manager meeting an unknown field says which manager version is needed.
- Close the asymmetries in §2, so the format does not have to be broken later to
  say something it nearly says now.
- Give [0011](0011-bundled-container-images.md)'s extraction budget a home
  chosen deliberately rather than late.
- Record what was considered and rejected, with what would reopen each.

**Non-goals**

- **A generally extensible manifest.** §5.1 makes the failure legible; it does
  not make old managers accept new fields, and nothing can.
- **Changing any existing field's meaning.** Additions only.
- **Reshaping the secret schema.** Its fields were reviewed and no asymmetry was
  found.
- **Speculative fields.** §8 is as much the deliverable as §5.2 is.

## 5. Design

### 5.1 Two-pass decode

`ParseManifest` gains a lenient first pass over a minimal shape:

```go
type manifestPreamble struct {
    APIVersion    APIVersion `yaml:"api_version"`
    Compatibility struct {
        MinManagerVersion Version `yaml:"min_manager_version"`
    } `yaml:"compatibility"`
}
```

Decoded **without** `DisallowUnknownField`, so it succeeds on any manifest whose
YAML parses. If `min_manager_version` exceeds this manager, refuse naming both
versions. Otherwise decode strictly as today, and an unknown field means what it
has always meant: a typo.

**What this buys, precisely.** An operator whose vendor shipped a release built
for a newer manager is told to upgrade, instead of being told about a typo in a
file they did not write. That is the whole win, and it is worth having.

**What it does not buy**, stated because the temptation to oversell it is the
main risk here: an old manager still cannot install a release using fields it
does not know. Adding manifest fields after the first tag remains a real cost —
this makes the cost *comprehensible*, not absent.

**Its one failure mode**: a vendor who adds a new field and forgets to raise
`min_manager_version` puts their customer back in the original confusing state.
No mechanism can catch that from inside the manifest; `release verify` on the
vendor's own CI runs with *their* manager, which by definition knows the field.
The documentation must say that raising the floor is part of adopting a new
field.

Cost is one extra decode of a small file, and the duplication with
`CheckUpgrade` is deliberate: the later check has the installed version and the
release version and can report richly; this one exists only to make a decode
failure legible.

### 5.2 The five fields

**`parameters.<name>.required: bool`** — when true, an operator who sets no
value is refused by name at `init` and `apply`, rather than silently receiving
`""`. Declaring `required: true` **and** a `default` is a validation error: it
is a vendor saying two contradictory things, and picking a winner would hide the
mistake.

**`requirements.cpus: int`** — preflight, with the same disposition `memory` and
`disk` already have, so a release needing four cores can say so.

**`metadata.release_notes: <path>`** — declared, not conventional. Existence is
checked by `checkReferencedFiles` like every other declared path, so a bundle
promising notes and shipping none fails `verify` on the vendor's machine. Absent
means no notes; there is deliberately **no** fallback to looking for
`RELEASE.md`, because a convention layered under a declaration is the ambiguity
this field exists to remove. [0013](0013-bundle-authoring-experience.md)'s
scaffold emits the field pointing at the stub it already writes, and
`release.ReleaseNotesFileName` becomes that default value rather than a lookup.

**`health.checks[].start_period: <duration>`** — failures before it elapses mean
"not ready yet" rather than "unhealthy". Distinct from `timeout`, which bounds a
single attempt. Without it, a product with a slow first boot and a product that
is dead are the same observation.

**`metadata.support_url: <https url>`** — where an operator goes when something
is wrong, for a product whose operator is not its vendor. `https` only, matching
the refusals `ParseRef` and [0015](0015-notifications.md) already make. Surfaced
by `status` and by `doctor` when a check fails — **not** appended to every error
hint, which would be a vendor URL in every log line.

### 5.3 A `bundle:` block for the extraction budget

```yaml
bundle:
  uncompressed_size: 12GiB
```

[0011 decision 11](0011-bundled-container-images.md) sizes the extraction budget
from a declaration read out of the tar stream, and never said where it lives.

`requirements` is the tempting home and is wrong: everything in it describes the
*host* — architectures, OS, tools, memory, disk. The budget describes the
*artifact*. A top-level `bundle:` block says that, and gives future
artifact-format declarations somewhere obvious to go.

**That last clause is a naming argument, not a compatibility one.** §2 verified
that nesting confers nothing: `bundle.anything_else` added after the first tag
is exactly as much of a break as a new top-level section. The block is worth
having because it is where a reader will look, not because it reserves space.

Reuses `ByteSize`, which already parses `12GiB` and already carries the
inherited-YAML-base documentation from an earlier defect.

**Its validation matters more than its location.** The value is read from the
archive *before the signature is checked*, so it is attacker-controlled input
in the strictest sense. Two rules, both in
[0011 decision 16](0011-bundled-container-images.md):

- It may only ever **lower** the extraction ceiling — the effective limit is
  `min(declared, hard_cap)`. A declaration that would raise it is not honoured,
  and a bundle needing more than `hard_cap` is refused rather than trusted.
- Absent means the default ceiling, not "unbounded". A missing field must never
  be the permissive reading of anything that gates untrusted bytes.

Manifest-level validation is the ordinary kind — non-negative, parses as a
`ByteSize` — and is *not* where the security lives. The clamp is, and it lives
in the extractor, because by the time the manifest validates it has already
been read out of the untrusted stream.

### 5.4 What `extensions` is, and the one thing it is good for

Recorded because §3 shows it looking dead and it is not: `extensions` exists so
that a vendor's own namespaced keys do not trip strict decoding. The manager
interprets none of it.

The useful consequence: **`extensions.<namespace>` is a real forward-compatible
area for *experimental* manager fields.** An old manager tolerates
`extensions.morzer.dev/thing`; a newer one can read it. That is an ugly home for
a first-class field and a perfectly good one for a mechanism still proving
itself — and it means a future feature does not have to choose between a break
and waiting for a major version.

Not proposed as policy, just recorded, because the option is invisible from the
code and would otherwise be rediscovered the hard way.

## 6. Tests

- **The two-pass decode reports the version, not the field.** A manifest with
  `min_manager_version` above this build and an unknown field must produce the
  version refusal — and one with an unknown field and *no* raised floor must
  still produce `unknown field`. The pair, or the first message swallows the
  second and every genuine typo becomes a confusing version error.
- **`required` without a value is refused by name**, and `required` plus
  `default` fails validation. Both, since the second is what stops `required`
  from becoming decorative.
- **A declared-but-missing `release_notes` fails `verify`**, driven through
  `checkReferencedFiles` alongside the existing template and hook cases, so it
  cannot be the one declared path that is not checked.
- **`start_period` distinguishes slow-start from dead**: a check failing inside
  the period does not fail the operation; the same check still failing after it
  does.
- **The budget is read from the archive's first entry** — this is
  [0011](0011-bundled-container-images.md)'s test, and it becomes reachable only
  once the field has a name.
- **The schema regenerates and the docs gate passes.** `docs-check` already
  fails the build on an undocumented manifest field
  ([0006](0006-documentation-site.md)), so every field here is gated by
  construction rather than by remembering.

## 7. Docs

- `reference/manifest.md`: the six new fields. Gated — an undocumented manifest
  field fails the build.
- `authoring/publishing.md`: raising `min_manager_version` is part of adopting a
  new field, and why (§5.1's failure mode).
- `reference/manifest.md`'s `extensions` section: reword "passed through
  untouched" so it cannot be read as "delivered somewhere", and record the
  experimental-field use.
- The JSON Schema regenerates from the types, so editors get the new fields with
  no separate step — which is [0013 §5.2](0013-bundle-authoring-experience.md)'s
  modeline paying off immediately.

## 8. Out of scope

These were considered in the sweep and rejected. Recorded with what reopens
each, because an unrecorded rejection is re-proposed.

- **`parameters.<name>.min` / `max`.** `values` covers enums, and an
  out-of-range integer fails at the product with a better message than the
  manager could give. *Reopens if* operators are found setting nonsense values
  that the product accepts and then misbehaves on.
- **`parameters.<name>.immutable`.** A real hazard — changing a data directory
  after install can strand state — but no evidence anyone has hit it, and
  `services` already forces a re-create for the cases that matter. *Reopens on
  the first incident.*
- **`configuration[].owner` / `group`.** `mode` exists; ownership of a rendered
  file is usually the consuming container's business. *Reopens if* a vendor
  needs a host-side file owned by a specific uid.
- **Release recall or withdrawal.** A vendor saying "do not install 1.4.2" needs
  the manager to consult something after resolving a version, which is a new
  trust relationship, not a field
  ([0016 §8](0016-update-checking-and-unattended-updates.md)).
- **Per-image platform.** A `pack` flag, not a manifest field
  ([0012 decision 9](0012-packing-images-into-a-bundle.md)).
- **Reshaping the secret schema.** Reviewed in the same sweep; `required`,
  `services` and `rotation_period` are all present and no asymmetry was found.

## 9. Risks

- **This RFC reads as permission to stop worrying about the format.** §5.1 makes
  a future break legible, not free, and the fields here are the ones a sweep
  found — not a guarantee that none will be wanted later. The Summary and §5.1
  both say so, and someone will still quote this RFC as if it settled the
  question.
- **A vendor forgets to raise `min_manager_version`.** Their customer is back to
  `unknown field`, and their own CI cannot catch it because `verify` runs with a
  manager that knows the field. Documentation only.
- **Five fields is five more surfaces.** Each needs a schema entry, a docs row, a
  validation rule and a test, and each is permanent. The defence is that all
  five close asymmetries rather than adding capability — but "it is symmetric"
  is a seductive argument for adding anything.
- **`required` interacts with `--set` on `init`.** A release with required
  parameters cannot be installed non-interactively without them, which is
  correct and will surprise someone automating an install against a new release.
- **The sweep is one person's imagination.** It found asymmetries because those
  are findable by inspection. A missing field that is not asymmetric with
  anything is exactly what this exercise cannot surface, and the honest mitigation
  is §5.4's experimental area rather than a claim of completeness.

## 10. Unresolved questions

Questions 2 and 3 are resolved as of 2026-08-09, into decisions 10 and 11. Kept
here because what was open is part of the record.

1. **Should the preamble pass also refuse an unrecognised `api_version` before
   the strict decode?** It would turn "unknown field" into "this bundle uses
   `selfhost/v2`, which this manager does not read" — the same improvement one
   level up. Against: `api_version` is already validated after decoding with a
   good message, and two checks for one thing is how they drift apart.
2. ~~Does `requirements.cpus` mean physical cores, logical CPUs, or a cgroup
   quota?~~ → decision 10. **Logical CPUs, narrowed by a cgroup quota where one
   is in force.**
3. ~~Should `start_period` be per-check or a single value on `health`?~~ →
   decision 11. **Per-check**, which decision 5 had already committed to by
   naming the field `health.checks[].start_period`; what was actually open was
   whether to move it, and the answer is no.

## 11. Decisions

| # | Decision | Rationale and consequence |
| --- | --- | --- |
| 1 | `ParseManifest` decodes **twice**: a lenient preamble for `api_version` and `min_manager_version`, then strictly | The mechanism designed to say "you need a newer manager" runs after decoding and so can never speak. Consequence: a future manifest field is a legible upgrade requirement rather than a report about a typo — and *not* a free change; §5.1 bounds the claim. |
| 2 | `parameters.<name>.required`, and `required` + `default` is a **validation error** | Secrets can be required and parameters cannot, though parameters exist to be operator choices; today an unset one is silently `""`. Refusing the contradictory pair stops `required` becoming decorative. |
| 3 | `requirements.cpus` | `memory` and `disk` are bounded and CPU is not, for no reason other than nobody adding it. |
| 4 | `metadata.release_notes` is **declared**, with no `RELEASE.md` fallback | Every other path a bundle ships is declared and existence-checked; a convention layered under a declaration reintroduces the ambiguity the field removes. Consequence: [0013](0013-bundle-authoring-experience.md)'s scaffold emits the field, not just the file. |
| 5 | `health.checks[].start_period` | Without it, slow-to-start and dead are the same observation, and the only lever is a timeout long enough to delay noticing the second. |
| 6 | `metadata.support_url`, `https` only, surfaced by `status` and failing `doctor` checks | The operator is not the vendor; "where do I get help" has no home today. Not appended to error hints — that is a vendor URL in every log line. |
| 7 | The extraction budget lives at **`bundle.uncompressed_size`**, and may only ever **lower** the ceiling | `requirements` describes the host; this describes the artifact. The block is where a reader will look — it reserves nothing, because §2 verified nesting confers no forward compatibility. The clamp to `min(declared, hard_cap)` lives in the extractor, not in manifest validation: the value is read from the archive before the signature is checked, so it is attacker-controlled, and absent must mean the default ceiling rather than unbounded. See [0011 decision 16](0011-bundled-container-images.md). |
| 8 | The six rejected fields in §8 stay rejected, each with a named trigger | An unrecorded rejection is re-proposed. |
| 9 | `extensions` is **left exactly as it is** | It is not dead: it exists to be tolerated, so a vendor's namespaced keys do not trip strict decoding. Consequence recorded rather than adopted: it is a usable home for *experimental* manager fields, which is invisible from the code. |
| 10 | `requirements.cpus` means **logical CPUs, narrowed by a cgroup quota** where one is in force | Those are the three things a machine can mean by "how many CPUs", and the one that decides how much parallelism the product gets is the last that applies. A manager running inside a container sees every host CPU through the OS and may use far fewer. Consequence: the check reads `/sys/fs/cgroup/cpu.max` and falls back to the OS count, so it is a Linux answer — which is the only platform this manages. |
| 11 | `start_period` stays **per-check** | Decision 5 named the field `health.checks[].start_period` and the implementation followed it; what was open was whether to hoist it to `health`. Per-check matches `timeout`, which is already per-check, and a sibling concept placed one level up is the kind of asymmetry this whole RFC exists to remove. Consequence: a product wanting one value writes it on the check that is actually slow, rather than on all of them. |
| 12 | An unset **required** parameter fails preflight, so `apply` refuses too | Not only `init` and `config`, which is where the no-default rule already refused. A release that introduces a required parameter must fail to *deploy* rather than deploy with an empty value the product then misreads; the currently running release keeps serving, which is the safe side. Consequence: an unattended update that meets a new required parameter refuses rather than proceeding — which is the behaviour [0016](0016-update-checking-and-unattended-updates.md) P3 wants anyway. |

## 12. Phasing

- **P1 — the two-pass decode.** Independent of every field below, and the item
  that keeps its value if this RFC's field list turns out to be incomplete —
  which §9 says it may.
- **P2 — the five fields**, together, since they share a schema regeneration and
  a docs gate.
- **P3 — `bundle.uncompressed_size`**, which is only useful once
  [0011](0011-bundled-container-images.md) reads it, and should land with 0011 P1
  rather than ahead of it.

P1 and P2 want to land before the first tag. P3 follows 0011's schedule.
