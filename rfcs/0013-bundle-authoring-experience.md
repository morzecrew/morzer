# RFC 0013 — Bundle authoring experience

- **Status:** 📝 Draft — **design locked** 2026-08-08. Both questions in §10 are
  resolved into decisions 12 and 13; the motivating failure (§2) is demonstrated
  against a real bundle.
- **Scope:** Makes a vendor's own CI able to catch the bundle errors that
  currently reach the customer, and stops the authoring surface fighting the
  editor. Five changes: `release verify` parses every template it currently
  only checks the existence of; the manifest examples carry a
  `yaml-language-server` modeline pointing at the JSON Schema this repository
  already generates and publishes; templates get a `.tmpl` suffix so an editor
  stops linting Go template syntax as YAML; the secret schema stops living
  in `templates/`, where it is not a template; and `release new` scaffolds a
  bundle that already carries all four, because otherwise every bundle written
  after this RFC needs the same retrofit it performs. No format change, and no
  manifest field change **of its own** — every path this RFC touches is one the
  vendor already names. The scaffold does emit one field it does not define:
  `metadata.release_notes`, added by
  [0018 decision 4](0018-the-pre-1-0-manifest-surface.md), which P5 is therefore
  gated on.
  Does **not** add a render-preview command (§8), and does not touch how
  templates render at install time.
- **Related:** [`internal/cli/release.go`](../internal/cli/release.go) (`verify`) ·
  [`internal/release/load.go`](../internal/release/load.go) (`Load`,
  `LoadSecretSchema`) ·
  [`internal/adapters/render/gotemplate/render.go`](../internal/adapters/render/gotemplate/render.go)
  (`missingkey=error`, `funcs`) ·
  [`internal/schema/schema.go`](../internal/schema/schema.go) (the generated
  schemas and their `$id`) · [`schemas/`](../schemas) ·
  [`testdata/bundle/`](../testdata/bundle) ·
  [`pages/docs/authoring/`](../pages/docs/authoring) ·
  [0004](0004-distribution-and-verification.md) (which shipped the schemas) ·
  [0007](0007-operator-parameters.md) (the three bundle-facing ABIs) ·
  [0014](0014-building-a-release-bundle.md) (which stamps the version the
  scaffold leaves as a placeholder) ·
  [`internal/cli/init_wizard.go`](../internal/cli/init_wizard.go) (the
  *operator's* wizard, which is what exists instead of a vendor scaffold)

---

## 1. Summary

`release verify` — the command whose help says it is "the command a bundle
vendor runs in their own CI" — reports `bundle is valid` for a bundle whose
template cannot render. This RFC makes it parse templates, and optionally
render them against a synthetic context, so a broken template fails on the
vendor's machine rather than during an operator's `apply`.

Alongside it, three cheap changes to the authoring surface: point vendors at
the JSON Schema already published for their manifest, name templates so editors
treat them as templates, and stop keeping a non-template in `templates/`.

And `morzer release new`, which writes a skeleton carrying all three. Without
it those three are conventions taught by documentation, which every bundle
written from now on acquires by hand or not at all; with it they are what a
bundle starts as.

## 2. Motivation

**The vendor's CI gate cannot catch the failure it exists to catch.**
Demonstrated against the tree at `d81d1c1` — a copy of `testdata/bundle` whose
template was replaced with:

```yaml
server:
  url: {{ .Installation.URL      # unterminated action
  broken: {{ .Nope.Missing }}    # no such field in the render context
```

```
$ morzer release verify ./badbundle
demo 1.2.0
sha256:4f27a89282a0d21464a07aa69b94f15f5d18313eb8f271536b4b4a8083f17d84

bundle is valid
```

`verify` loads and validates the manifest, checks that every referenced file
*exists*, and computes the digest. It never parses a template. The renderer runs
with `missingkey=error`
([`render.go:41-44`](../internal/adapters/render/gotemplate/render.go)) — the
right setting, and it means `{{ .Paths.Dta }}` is a hard failure — but that
failure arrives during `apply`, on the operator's machine, part-way through a
journaled operation.

**The editor fights the author.** `templates/application.yaml` is a Go template
that produces YAML. Named `.yaml`, every editor's YAML language server parses
`{{- range .Domains }}` and reports errors that are not errors: *"Implicit keys
need to be on a single line"*, *"Block collections are not allowed within flow
collections"*. A vendor writing their first bundle sees a file full of red that
their tooling insists is broken and the manager insists is fine.

**Beside it sits a file that is not a template at all.**
`templates/secrets.yaml` is real YAML — the secret schema, referenced by the
manifest as `secrets.schema`, consumed by `LoadSecretSchema`. One directory,
one extension, two unrelated kinds of file, and only one of them is a template.

**A vendor's first bundle starts as a copy-paste from a documentation page.**
There is no scaffold. `morzer init` is the *operator's* installation wizard
([`init_wizard.go`](../internal/cli/init_wizard.go)); nothing addresses the
vendor. `authoring/your-first-bundle.md` is the starting point, and it is
assembled from `testdata/bundle/` through `--8<--` snippet includes — so the
supported way to begin is to read a page built out of the test fixture and
retype it. That is a reasonable state for a project with no conventions to
teach. This RFC introduces three, which changes the arithmetic: every bundle
written after it will need the same retrofit §5.2–§5.4 perform, unless
something writes them in from the start.

**A schema is generated, published, and pointed at by nothing.**
[0004](0004-distribution-and-verification.md) shipped generated JSON Schemas
with `$id: https://morzecrew.github.io/morzer/schemas/…`
([`schema.go:36`](../internal/schema/schema.go)). The reference documentation
names the two filenames
([`manifest.md:330-331`](../pages/docs/reference/manifest.md)) and stops. No
example carries a modeline, and no page tells a vendor how to wire one up — so
the completion, hover text and inline validation the schema could give every
editor are left unused. A modeline would also have caught the unquoted-`mode`
trap that decoded to the wrong permission for months.

## 3. Current state

Verified against `d81d1c1`.

| Fact | Where |
| --- | --- |
| `verify` validates the manifest, checks referenced files exist, computes the digest — no template parsing | [`release.go:204-218`](../internal/cli/release.go) |
| Its own help calls it "the command a bundle vendor runs in their own CI" | [`release.go:210`](../internal/cli/release.go) |
| `Load` checks a declared template exists, and nothing more | [`load.go:259`](../internal/release/load.go) |
| Rendering uses `missingkey=error`, so an unknown field fails at render time | [`render.go:41-44`](../internal/adapters/render/gotemplate/render.go) |
| Templates and the secret schema are arbitrary vendor-named paths (`template:`, `secrets.schema`) — no extension is required anywhere | [`manifest.go:198-202`](../internal/domain/manifest.go), [`load.go:135-139`](../internal/release/load.go) |
| Schemas are generated into `schemas/` with a published `$id` | [`schema.go:32-36`](../internal/schema/schema.go), [`schemas/`](../schemas) |
| Docs name the schema files but never show a modeline; no example carries one | [`manifest.md:330-331`](../pages/docs/reference/manifest.md) |
| Docs embed `testdata/bundle/templates/secrets.yaml` and `manifest.yaml` via `--8<--` snippet includes, in four places | [`your-first-bundle.md:135,159`](../pages/docs/authoring/your-first-bundle.md), [`manifest.md:356,362`](../pages/docs/reference/manifest.md) |
| Every Go mention of `templates/secrets.yaml` is prose in a comment, plus one test fixture string — nothing functional | [`schema.go:41,76`](../internal/schema/schema.go), [`secret.go:109`](../internal/domain/secret.go), [`load.go:130`](../internal/release/load.go) |
| There is no vendor-side scaffold. `init` is the operator's wizard; `release` has `list`, `show`, `verify`, `fetch`, `prune` | [`init_wizard.go`](../internal/cli/init_wizard.go), [`release.go`](../internal/cli/release.go) |
| `Load` requires only `manifest.yaml` and validates declared paths — a bundle may carry any other file, so a skeleton is unconstrained by the format | [`load.go`](../internal/release/load.go) (`Load`, `checkReferencedFiles`) |

The last two rows are what make §5.3 and §5.4 cheap: the paths are vendor-named
and the only couplings are documentation snippets, which `docs-check` already
gates.

## 4. Goals / Non-goals

**Goals**

- A template that cannot parse fails `release verify`.
- A vendor editing a manifest gets completion and validation from the schema
  this repository already publishes.
- An editor treats a Go template as a Go template.
- `templates/` contains templates.
- A new bundle starts with all of the above already true, rather than acquiring
  them by reading a page.

**Non-goals**

- **Changing how templates render.** `missingkey=error` and the function set
  stay exactly as they are. This RFC adds a gate in front of them.
- **A manifest format change.** No new field, no changed field. Every path this
  touches is already vendor-named.
- **Validating rendered output as YAML.** A template may legitimately produce
  something that is not YAML; the manifest does not say what a target is.
- **A render-preview command.** §8.

## 5. Design

### 5.1 `verify` parses what it currently only counts

`release verify` gains a template pass: for every `configuration[].template`,
parse it with the same `template.New(...).Funcs(funcs())` construction the
renderer uses, and fail with the parse error, the file, and the line.

Parsing is unconditional and free — it needs no context, no installation and no
network, which is what makes it safe to put in the path a vendor runs on every
commit.

**Rendering against a synthetic context is a second, opt-in step**
(`--render-check`). It builds a `TemplateData` with plausible values for every
field and runs the template, so `{{ .Paths.Dta }}` and a missing secret are
caught rather than merely a syntax error.

It is opt-in and separately named because it cannot promise what parsing
promises. A synthetic context has to invent values — one domain or none, a
parameter that has a default and one that does not — and a template branching
on those (`{{- if .Domains }}`) exercises only the branch the synthetic values
choose. Making it the default would turn "verify passed" into a guarantee it
cannot keep, which is the failure this whole RFC is about. The documentation
must say it is a smoke test.

**It stays opt-in permanently** (decision 12). The obvious future move — "the
synthetic context has matured, make it default" — treats this as a maturity
question, and it is not one. The values are invented however good they get, so
the guarantee never becomes real; a better context widens what the smoke test
covers without ever making it a promise.

The refusal is fail-closed either way: a template that cannot be read, parsed,
or (under `--render-check`) rendered fails the command.

#### Alternatives considered

**Render against the real context.** There isn't one — `verify` runs with no
installation, which is what lets a vendor use it in CI. That is a feature, not
a gap.

**Parse during `Load` instead of in `verify`.** Rejected: `Load` runs on the
operator's machine during `apply` too, and a parse failure there is the failure
this RFC is trying to move earlier, not a place to add work. `verify` is the
vendor-side command.

### 5.2 Point vendors at the schema that already exists

The example manifests gain a first line:

```yaml
# yaml-language-server: $schema=https://morzecrew.github.io/morzer/schemas/selfhost-v1alpha1-manifest.json
```

and the secret schema example gains the equivalent for
`selfhost-v1alpha1-secrets.json`. `authoring/your-first-bundle.md` explains what
the line does and that it is optional — it is a comment, so a manifest without
it is unaffected, and a vendor whose editor does not read modelines loses
nothing.

This is the cheapest item in the RFC and probably the highest daily value: it
is one comment line, and it turns an existing published artifact into
completion, hover documentation and inline validation in every editor that
speaks the protocol.

### 5.3 Templates are named like templates

`testdata/bundle/templates/application.yaml` becomes `application.yaml.tmpl`,
and the manifests that reference it follow. `.yaml.tmpl` rather than `.tmpl`
so an editor can still infer the output language, and because the double
extension is the convention Helm, Hugo and `envsubst` users already recognise.

**This is a convention, not a format change** — and deliberately so even though
nothing has been released and it *could* be enforced. The manifest names the
path, so dictating a file extension would be the manager telling vendors how to
name files it was handed the location of. A vendor whose build system emits
`config.yaml.gotmpl`, or who templates a `.env`, is not doing anything wrong.
What changes is what the examples and the documentation teach; vendors who
prefer `.yaml` may keep it and keep the editor noise.

`authoring/your-first-bundle.md` gains a short note on the editor association
(`*.yaml.tmpl` → Go template) for the two editors most vendors use.

### 5.4 The secret schema leaves `templates/`

`templates/secrets.yaml` becomes `secrets.schema.yaml` at the bundle root — it
is a declaration the manager reads, not something rendered — and the manifest's
`secrets.schema` follows. Same reasoning as §5.3: the path is vendor-named, so
this changes the example and the documentation rather than the format.

Four documentation snippet includes reference the old path
([`your-first-bundle.md:135`](../pages/docs/authoring/your-first-bundle.md),
[`manifest.md:362`](../pages/docs/reference/manifest.md), and the manifest
includes beside them), and four Go comments describe the schema as
`templates/secrets.yaml`. Both sets move with it; `docs-check` fails on a
snippet path that no longer resolves, which is the gate that keeps this honest.

### 5.5 `release new` — where the conventions take hold

```
morzer release new ./my-product --name demo --vendor example
```

Writes a skeleton that already carries §5.2's modeline, §5.3's `.yaml.tmpl`
naming and §5.4's schema location:

```text
my-product/
  manifest.yaml          # modeline on line 1, version 0.0.0,
                         #   metadata.release_notes: RELEASE.md
  VERSION                # 0.0.0, agreeing with the manifest
  RELEASE.md             # release-notes stub
  compose/compose.yaml
  templates/app.yaml.tmpl
  secrets.schema.yaml
```

The manifest **declares** the release-notes file rather than the scaffold
merely dropping one beside it. That field is
[0018 decision 4](0018-the-pre-1-0-manifest-surface.md)'s, not this RFC's, and
it is why P5 is gated on 0018: writing `RELEASE.md` without the declaration
would leave the one file in the tree that nothing points at, which is the
condition [0002](0002-rich-terminal-renderer.md) P5 was already stuck behind.

`RELEASE.md` earns its place for a reason outside this RFC.
[0002](0002-rich-terminal-renderer.md) built `glamour` rendering of release
notes and left P5 unshipped, "gated on a bundle actually shipping a
`RELEASE.md`" — a gate nothing was ever going to open, because no bundle ships
a file no tooling creates and no page mentions.
[`release.ReleaseNotesFileName`](../internal/release/load.go) has been a
constant the whole time. A stub in the scaffold opens it by default, and
[0016 P2](0016-update-checking-and-unattended-updates.md) is where it acquires
a job: the moment an operator decides whether to apply a staged update is the
moment they want to read what changes.

**The version is a placeholder, deliberately.** `0.0.0` in both files, which
[0014 §5.2](0014-building-a-release-bundle.md) overwrites at build time. A
scaffold that wrote `1.0.0` would be inviting the vendor to hand-maintain the
one field the tooling should own.

**It scaffolds a skeleton, not a product.** No image guessing, no Compose
service inference, no hook stubs beyond what the manifest declares. A skeleton
that verifies clean and deploys nothing useful is honest; one that pretends to
know the vendor's architecture produces a bundle they have to un-write. The
generated Compose file is one service with a commented `image:` line pointing at
the `<PRODUCT>_IMAGE_*` variable it should use.

**The gate that keeps it correct is `release verify`.** The scaffold's output
must pass `verify --render-check` — §5.1's new gate — immediately and with no
edits. That is one assertion, and it couples the two halves of this RFC: a
scaffold that drifts from what the verifier accepts fails, and a verifier that
becomes stricter than the scaffold fails too. Neither can move alone.

#### Alternatives considered

**Copy `testdata/bundle` and substitute the name.** Tempting, because that
fixture is installed, updated, backed up and rolled back against real Docker on
every CI run, so it is the most-tested bundle in existence. Rejected as the
*default*: it carries five hooks, three Compose files and two profiles, and a
vendor's first act would be deleting most of it. It stays available as
`--from-example`, where the argument for it is strongest — a vendor who wants a
working reference rather than a starting point — and where it costs nothing,
since the fixture is already maintained.

**Put scaffolding in its own RFC.** Rejected, and this is the reason it is here:
§5.2–§5.4 are retrofits for bundles that exist and *defaults* for bundles that
do not. Landing them without a scaffold means the conventions are documentation
until someone writes one, and every bundle authored in between needs the same
retrofit performed by hand. The scaffold is not adjacent to this RFC; it is
where its conventions stop being advice.

## 6. Tests

- **The motivating case, as a test**: the bundle from §2 — unterminated action,
  unknown field — must fail `verify`. It currently passes, so this is
  verified-red by construction.
- **Parse-only vs `--render-check`**: a template that parses but references a
  field nobody defined passes the first and fails the second. Without this pair
  the two modes could collapse into one and nobody would notice.
- **A valid bundle still passes both**, driven by `testdata/bundle`, so the new
  gate cannot start refusing correct bundles.
- **The renamed paths load**: the existing bundle fixtures exercise
  `template:` and `secrets.schema` after §5.3 and §5.4, which the suite already
  does as a side effect of using them.
- **`docs-check`** is the test for the snippet moves; it fails on an
  unresolvable `--8<--` path already.
- **The scaffold verifies clean**: `release new` into a temp directory, then
  `release verify --render-check` on the result, asserting success with no
  edits. This is the assertion that couples §5.5 to §5.1 — either half moving
  without the other fails it.
- **The scaffold carries the conventions**: its manifest's first line is the
  modeline, its template ends in `.yaml.tmpl`, and its secret schema is not
  under `templates/`. Asserted against the file tree rather than by reading the
  generator, so a generator rewritten without them fails.
- **`--from-example` produces a bundle the suite already installs**, which is
  its whole justification.

Not tested: that a modeline produces completion in any particular editor. That
is a claim about editors, and asserting it here would be theatre.

## 7. Docs

- `authoring/your-first-bundle.md`: the modeline, the `.tmpl` convention and
  its editor association, the moved schema path — and it now **opens** with
  `release new` rather than with a manifest to retype. The page keeps its
  fragment-by-fragment walkthrough, because explaining what the scaffold wrote
  is still the thing a first-time vendor needs; what changes is that they read
  it beside a working tree rather than in place of one.
- `reference/release-commands.md`: `verify`'s template pass and
  `--render-check`, with the honesty that the latter is a smoke test rather
  than a guarantee — the docs-drift gate requires the flag to be documented
  anyway.
- The four stale Go comments naming `templates/secrets.yaml`, including
  `LoadSecretSchema`'s, which describes a hardcoded path the function does not
  use.

## 8. Out of scope

- **A render-preview command** (`morzer release render --template x`) that
  prints what a template produces. Genuinely useful, and largely subsumed by
  `--render-check`, which already has to build a context and render. Reopens if
  vendors ask to *see* the output rather than know it rendered — at which point
  it is a flag on the same code, not a new command.
- **Validating that rendered output parses as YAML.** The manifest does not say
  what a target is: a template may legitimately produce an INI file, an
  environment file, or a script. Guessing from the extension would be wrong for
  the case this RFC exists to fix.
- **A schema for the Compose file or the hook scripts.** The other two of
  [0007](0007-operator-parameters.md)'s three ABIs. Same shape of idea, larger
  surface, and the manifest is where vendors spend their first hour.
- **Bundling a language server or editor plugin.** The modeline gets the
  benefit from tooling vendors already have.

## 9. Risks

- **`--render-check` invites over-trust.** A vendor who reads "render check
  passed" as "this will render on the customer's machine" has been misled by a
  flag this RFC introduced. The mitigation is naming and documentation, both of
  which are weaker than a guarantee — which is why it is opt-in and why parsing
  is the part that is unconditional.
- **The synthetic context is a maintenance surface.** It must grow whenever
  `TemplateData` does, and a field added without a synthetic value silently
  narrows what `--render-check` exercises. Worth a test that walks the context's
  fields rather than a hand-maintained list.
- **Renaming examples touches the acceptance scenario and the docs snippets.**
  Cheap, gated by `docs-check`, and still the kind of change that breaks a
  fixture path nobody remembered.
- **The conventions describe a layout no bundle is obliged to follow.** Nothing
  has been released, so this is not about stranding anyone — it is that the
  pages will show `.yaml.tmpl` and `secrets.schema.yaml` while the manifest
  accepts any path, and a reader can easily take the examples for requirements.
  The pages must say the paths are the vendor's to choose, or the convention
  hardens into a believed rule that `verify` does not enforce.

## 10. Unresolved questions

Both are resolved as of 2026-08-08 and recorded as decisions 12 and 13. Kept
here because what was open is part of the record.

1. ~~Should `--render-check` become the default once the synthetic context has
   proven itself?~~ → decision 12. **No, and not later either.** The question
   was posed as a maturity question and it is not one: a synthetic context
   cannot promise what a real one delivers no matter how good it gets, because
   the values are invented. Time does not fix that, so deferring the question
   only meant a future maintainer would answer it wrongly.
2. ~~Where does the synthetic context live?~~ → decision 13. In the render
   adapter, beside the real context it imitates.

What implementation is still free to settle: the synthetic values themselves,
and how the walk over `TemplateData`'s fields (§9) is spelled.

## 11. Decisions

| # | Decision | Rationale and consequence |
| --- | --- | --- |
| 1 | `verify` parses every declared template, unconditionally | It is free, needs no context, and closes the demonstrated hole where a broken template reports `bundle is valid`. |
| 2 | Rendering against a synthetic context is opt-in (`--render-check`), not default | A synthetic context cannot promise what a real one delivers; making it default would turn "verify passed" into a guarantee it cannot keep — the exact failure this RFC exists to fix. Consequence: the docs must call it a smoke test, and unresolved question 1 revisits it. |
| 3 | Parsing lives in `verify`, not in `Load` | `Load` also runs during `apply` on the operator's machine; adding work there moves the failure later, not earlier. |
| 4 | The schema modeline is added to examples and documented, not enforced | It is a comment; a manifest without it is unaffected and an editor that ignores it loses nothing. Turns an already-published artifact into daily value. |
| 5 | `.yaml.tmpl` for templates, as a **convention** rather than a requirement | Not for compatibility — nothing is released, so it could be enforced. The manifest names the path, so requiring an extension would be the manager dictating filenames it was handed the location of, and a vendor templating a `.env` or emitting `.gotmpl` is not misbehaving. Double extension keeps the output language inferable and matches what Helm/Hugo users expect. |
| 6 | The secret schema moves out of `templates/` | It is not a template; it is read, not rendered. Same convention-not-format reasoning as decision 5. |
| 7 | Rendered output is not validated as YAML | The manifest does not declare a target's format, and a template may legitimately produce something else. |
| 8 | `release new` ships **in this RFC**, not a later one | Without a scaffold, decisions 4–6 are documentation rather than defaults, and every bundle authored between this RFC and a later scaffold acquires them by hand or not at all. The scaffold is where the conventions stop being advice. |
| 9 | The scaffold writes version `0.0.0` in both `manifest.yaml` and `VERSION` | A placeholder [0014 §5.2](0014-building-a-release-bundle.md) stamps at build time. Writing a real-looking version would invite hand-maintaining the one field the tooling should own. |
| 10 | The scaffold is a **skeleton**, not a working product; `--from-example` copies `testdata/bundle` for those who want one | A generated bundle that guesses an architecture produces work to undo. The example path costs nothing because that fixture is already exercised on every CI run. |
| 11 | The scaffold's output must pass `verify --render-check` unedited, asserted by a test | Couples §5.5 to §5.1 in both directions: neither the scaffold nor the verifier can move without the other. |
| 12 | `--render-check` is **permanently** opt-in — not deferred, decided | A synthetic context invents its values, so it can never promise what a real install delivers; that is structural, not a maturity gap time closes. Making it default would turn "verify passed" into a guarantee it cannot keep, which is the exact failure this RFC exists to fix. Consequence: there will never be a `--no-render-check`, and anyone proposing the flip is re-opening decision 2. |
| 13 | The synthetic context lives in the **render adapter**, beside the real one | They imitate each other, so they should drift together; a context that lives away from the renderer it mimics goes stale silently. Consequence: the adapter carries code only `verify` calls, which is the cost accepted for adjacency. |
| 14 | The scaffold writes a `RELEASE.md` stub **and declares it** as `metadata.release_notes` | [0002](0002-rich-terminal-renderer.md) P5 is gated on "a bundle actually shipping a `RELEASE.md`", which no bundle does because nothing creates one — a gate that could not open by itself. The field is [0018 decision 4](0018-the-pre-1-0-manifest-surface.md)'s, so P5 is gated on 0018: a stub with nothing pointing at it recreates the condition P5 was already stuck behind. Consequence: 0002 P5 becomes schedulable, and [0016 P2](0016-update-checking-and-unattended-updates.md) is where it pays off. |

## 12. Phasing

- **P1 — `verify` parses templates**, with the §2 bundle as the red test. This
  is the whole safety value; everything after it is ergonomics.
- **P2 — the modeline and its documentation.** One line and a page, independent
  of P1.
- **P3 — the renames**, examples and docs together, gated by `docs-check`.
- **P4 — `--render-check`** and the synthetic context, last because it carries
  the over-trust risk and the maintenance surface, and because P1 already stops
  the failure that motivated the RFC.
- **P5 — `release new`**, after P2 and P3, because it emits what those two
  decide and would otherwise have to be rewritten when they land. Its
  verify-clean test needs P1; its `--render-check` assertion needs P4, and
  degrades to plain `verify` if P4 is dropped. **Also gated on
  [0018](0018-the-pre-1-0-manifest-surface.md) P2**, which defines the
  `metadata.release_notes` field the skeleton declares — scaffolding the stub
  without it would ship a file nothing points at.

P1 alone is worth shipping. P4 is the only phase that could be dropped without
losing the point — and if it is, P5 keeps its weaker gate rather than losing
one.
