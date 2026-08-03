# RFC 0006 — Documentation site

- **Status:** ✅ Complete — P1–P3 shipped 2026-08-03, P4–P6 the same day.
  Twenty-three pages across five Diátaxis sections, drift-gated by
  `just docs-check`, published `dev` from main and `<minor>` plus `latest` from
  a tag. The README is 76 lines. Two design changes are recorded as amendments
  in §5.1 and §5.5.
- **Scope:** Moves operator- and vendor-facing documentation out of the 287-line
  README into a `pages/` site built with zensical, structured by Diátaxis and
  deployed to GitHub Pages — versioned, so an operator running 1.2.0 reads the
  1.2.0 docs. Adds a link-and-coverage check that fails the build when the docs
  drift from the code. The README shrinks to an orientation page. Adds the two
  deployment workflows; the CI topology they slot into belongs to RFC
  [0005](0005-continuous-integration-and-release.md). Explicitly **not** in
  scope: any Go source change, and the RFC directory, which stays as it is.
- **Related:** [`README.md`](../README.md),
  [`justfile`](../justfile),
  [`internal/ports/`](../internal/ports/),
  RFC [0005](0005-continuous-integration-and-release.md) (CI it plugs into)
- **Origin:** Site layout (`pages/` holding `zensical.toml`, `docs/`,
  `diagrams/`, `_includes/`, `overrides/`), the dev/release deployment split and
  the "docs floors" idea are ported from
  [morzecrew/forze](https://github.com/morzecrew/forze), which uses the same
  generator.

---

## 1. Summary

A documentation site under `pages/`, four Diátaxis sections deep, replacing
most of the README. Two audiences are served explicitly: the **operator** who
installs and runs the manager, and the **bundle vendor** who authors a release
against the manifest contract. Deployment is versioned — `main` publishes to
`dev`, a tag publishes a version and moves `latest`.

## 2. Motivation

The README is 287 lines and carries at least six distinct documents: an
orientation, a command reference, an exit-code table, an architecture
explanation, a secrets model, a bundle-authoring contract, a hook ABI, and a
testing guide. Nobody reads that top to bottom, and its structure makes the two
audiences collide: an operator wanting to know what `doctor` reports has to
scroll past a dependency-inversion argument, and a vendor wanting the hook ABI
has to scroll past `just` recipes.

It will also get worse rather than better. Four RFCs are in draft, each of which
adds operator-facing surface: `update`/`rollback` semantics, a rollback
assessment that needs explaining, a recovery procedure that is a numbered
sequence, a bundle-signing story for vendors. Appending those to the README
produces a 600-line file.

**The versioning problem is the sharper one.** The manifest contract is
versioned (`selfhost/v1alpha1`), the state schema is versioned, and the manager
declares which manifest versions it reads. A single always-latest README cannot
tell an operator running an older binary what *their* version does — and for a
tool whose entire job is careful version transitions, that is a poor advertisement.

## 3. Current state

- **README.md, 287 lines**, sections: status, what-it-is-not, install, try-it,
  commands table, exit codes, architecture, step engine, secrets, release
  bundles, hook ABI, testing, design proposals, changelog, licence.
- **`rfcs/`** — four design documents plus an index. These are *design* records
  for contributors, a different genre from user documentation, and they stay
  where they are.
- **`CHANGELOG.md`** — Keep a Changelog format, currently one `Unreleased`
  section.
- **No `pages/`, no site generator, no GitHub Pages deployment.**
- **No godoc-facing story.** Package comments are thorough (`internal/domain`,
  `internal/ports`, `internal/lifecycle/engine` all carry substantial ones) but
  everything is under `internal/`, so `pkg.go.dev` will publish nothing. **The
  site is the only documentation surface this project can have.**
- **`testdata/bundle/`** is a complete, valid, test-exercised example bundle.
  It is the best vendor documentation in the repository and is currently
  reachable only by browsing the source tree.

## 4. Goals / Non-goals

**Goals**

- Separate the operator and vendor audiences into their own sections.
- Version the site so an operator reads the docs for the binary they run.
- Shrink the README to what a visitor needs in thirty seconds.
- Make documentation drift a build failure rather than a discovery.
- Keep every example runnable, ideally by lifting it from `testdata/`.

**Non-goals**

- **Moving the RFCs into the site.** They are design records for contributors,
  written in a different register and with statuses that change. `rfcs/INDEX.md`
  stays the index.
- **API reference for internal packages.** Everything is under `internal/` by
  deliberate choice — nothing here is a public library until the contracts have
  survived a real product. Generating a reference for packages nobody can import
  would be busywork.
- **Deleting the README.** It stays as orientation and as what GitHub shows.
- **A blog, changelog site, or landing page with marketing copy.**

## 5. Design

### 5.1 Layout

Mirroring forze so the two repositories are navigable by the same person:

```text
pages/
├── zensical.toml           # site config: nav, theme, repo links
├── docs/
│   ├── index.md            # what this is, in one screen
│   ├── get-started/        # tutorial — install, first deployment, teardown
│   ├── operating/          # how-to — the operator's task index
│   ├── authoring/          # how-to — the bundle vendor's task index
│   ├── reference/          # commands, exit codes, manifest, hook ABI
│   ├── explanation/        # architecture, step engine, secrets model
│   ├── _stylesheets/
│   └── _javascripts/
├── diagrams/               # d2 sources, rendered at build time
├── _includes/
│   └── abbreviations.md
└── overrides/
```

> **Amendment, P6 (2026-08-03).** No `diagrams/`, no `overrides/`, and no d2.
>
> Diagrams are **mermaid in fenced code blocks**, which the theme already
> renders and already themes for light and dark. The d2 plan -- ported from
> forze, which needs it -- would have meant a binary installed in CI, a build
> step before every deploy, light and dark SVG variants, and a `_diagrams/`
> tree gitignored and kept in step with its sources. For flowcharts of six
> boxes that is a lot of machinery to maintain.
>
> The trade, stated rather than glossed: mermaid renders client-side from a CDN,
> so a reader who is offline or behind a blocker sees the diagram source instead
> of a picture. Every diagram on the site is a supplement to prose that stands
> without it, which is the condition that makes the trade acceptable.
>
> `overrides/` is unused because nothing needed a template override yet. It
> arrives with the version banner, if the version banner is ever wanted.

### 5.2 Diátaxis split, and where the README goes

The four sections are not decoration: each has a different contract with the
reader, which is what stops the current all-in-one-file problem recurring. The
`altitude-docs` skill governs how individual pages are written; this RFC only
decides where things live.

| Current README section | Destination |
| --- | --- |
| Status, what-it-is-not | `index.md` (condensed) |
| Install, try-it-without-touching-/etc | `get-started/` |
| Commands table | `reference/commands.md`, one page per command group |
| Exit codes | `reference/exit-codes.md` |
| Architecture, step engine | `explanation/` |
| Secrets model | `explanation/secrets.md` + `operating/secrets.md` |
| Release bundles, hook ABI | `authoring/` + `reference/manifest.md` |
| Testing | `contributing` page, or `CONTRIBUTING.md` per RFC 0005 |

**New pages the RFCs will need**, written as their features land rather than
speculatively: `operating/updating.md` and `operating/rolling-back.md` (RFC
0001), `operating/recovering-a-lost-machine.md` (RFC 0003 — explicitly gated on
its end-to-end test passing), `authoring/signing.md` (RFC 0004).

### 5.3 The vendor section is the one that does not exist yet

Everything about *operating* the manager is at least sketched in the README.
Nothing explains how to *author a bundle* beyond the manifest example. That
section is the one with the most new writing, and it has a ready-made spine:
`testdata/bundle/` is a complete valid bundle that the test suite exercises on
every run.

Pages are built **from** that bundle rather than alongside it — snippets are
extracted from the real files at build time, so a documentation example cannot
drift from a bundle that is under test.

### 5.4 Versioned deployment

Two workflows, ported in shape from forze:

| Trigger | Publishes to | Alias |
| --- | --- | --- |
| push to `main` | `/dev/` | — |
| push tag `v*` | `/<version>/` | `latest` |

A version selector lets an operator on 1.2.0 read 1.2.0. `latest` is what the
README and the repository link to. `dev` is explicitly labelled as unreleased in
the site header, so a search result landing there is not mistaken for shipped
behaviour.

`concurrency: { group: docs-deploy, cancel-in-progress: false }` — two
deployments racing corrupts the version index, and cancelling a half-finished
one is how the alias ends up pointing at nothing.

### 5.5 Docs floors

forze gates on `docs_floors.py`: every public symbol mentioned somewhere, the
nav resolving both ways, every relative link landing. The Go equivalent, as a
`just docs-check` recipe that CI runs:

1. **Link integrity** — every relative link between pages resolves; every link
   into the repository resolves. (The RFC directory already gets this treatment
   manually; here it is automated.)
2. **Nav completeness, both directions** — every page in `docs/` appears in the
   `nav`, and every `nav` entry exists. A page absent from the nav is a page
   nobody finds.
3. **Contract coverage** — every exported method on every interface in
   `internal/ports/`, every `domain.Code`, and every exit-code constant is
   mentioned by some page. These are the project's stated public contracts; one
   undocumented is a contract that exists only in the source.
4. **Command coverage** — every cobra command and every persistent flag is
   mentioned. Parsed from the command tree, so a new command fails the build
   until it is documented.

Check 3 is the one that earns its keep: the exit-code table and the manifest
schema are things the README already claims are stable contracts, and a stable
contract that drifts from its documentation silently is worse than an undocumented
one.

> **Amendment, P4–P6 (2026-08-03).** The drift gate grew with the site rather
> than being outgrown by it: 7 pages and 13 checks at P3, 23 pages and 29 checks
> now, still green and still catching things — every forward reference written
> while the sections were half-built was caught by the link check within
> seconds of being written.
>
> **Amendment, P3 (2026-08-03).** Check 3 shipped without the port-method
> clause, and with two surfaces the draft did not name. The clause contradicted
> **decision 8**: `internal/ports` holds 14 interfaces and about 60 methods,
> all internal by deliberate choice, and requiring an operator- and
> vendor-facing site to name every one of them would have produced exactly the
> internal API reference decision 8 forbids — or, more likely, an exemption
> table long enough to make the gate meaningless.
>
> What shipped instead covers what a bundle vendor actually writes against:
> every yaml-tagged field of `domain.Manifest` and `domain.SecretSchema`
> (collected by reflection over the struct graph) must be named on
> `reference/manifest.md`, and every variable `ports.HookEnvVars` produces must
> be named somewhere. Error codes and exit codes shipped as designed, read out
> of the domain package's AST rather than from a list. See decision 11.

### 5.6 What the README becomes

Roughly 60 lines: what morzer is, the what-it-is-not paragraph, install, a
five-line quickstart, a link to the docs site, and the badge row. Everything
else becomes a link. The measure of success is that adding a feature does not
add a README section.

## 6. Tests

- **`just docs-check`** is the test, run in CI (RFC 0005's `quality` job) and
  locally.
- **Build the site in CI on every PR touching `pages/`** — a broken nav or an
  unparseable config must not be discovered at deploy time.
- **Extracted snippets are verified against their source files**, so a snippet
  quoting `testdata/bundle/manifest.yaml` fails when the manifest changes and
  the page does not.
- **No screenshot tests.** The rich renderer (RFC 0002) is the only visual
  surface, and screenshots go stale silently.

## 7. Docs

Self-referential but real: `CONTRIBUTING.md` gains a short section on where a
given kind of change gets documented — which of the four sections, and the rule
that a new operator-facing flag needs a reference entry before it merges. Without
that, the floors in §5.5 become a chore to satisfy rather than a guide.

## 8. Out of scope

- **Translations.** No demand, and a stale translation is worse than none.
- **A search backend beyond the generator's built-in index.** Site scale does
  not justify it.
- **Publishing the RFCs to the site.** Named because it is the obvious
  suggestion: they are contributor-facing design records with mutable statuses,
  and mixing them into user documentation would put "📝 Draft" designs next to
  shipped behaviour.
- **Custom domain.** `morzecrew.github.io/morzer/` until someone wants otherwise.
- **Documenting `internal/` packages.** They are internal by choice; see §4.

## 9. Risks

- **Documentation moving further from the code it describes.** A `pages/` tree
  is easier to leave stale than a README someone edits while working. The floors
  in §5.5 are the mitigation and are the reason this RFC is not just "set up a
  site".
- **Versioned docs multiplying the maintenance surface.** Fixing a typo in three
  published versions is three deployments. Accepted: only `latest` and `dev` are
  actively maintained; older versions are frozen as what shipped, which is the
  point of publishing them.
- **The site becoming a second source of truth for the manifest schema.**
  RFC 0004 generates a JSON Schema from `domain.Manifest`; the reference page
  must be generated from or checked against that schema, never hand-maintained
  in parallel.
- **Effort displacing feature work.** This RFC ships no capability. Phasing puts
  the README shrink and the reference pages first — the parts that reduce
  ongoing cost — and the explanatory prose last.

## 10. Decisions

| # | Decision |
| --- | --- |
| 1 | `pages/` mirrors forze's layout, generator and deployment split. Two repositories with one navigation model cost less than two bespoke ones, and the shell helpers port directly. |
| 2 | Operator and vendor get separate sections. They are different people with different tasks, and the current README interleaves them, which is most of why it is hard to read. |
| 3 | Docs are versioned, `main` → `dev` and tags → `<version>` with a `latest` alias. A tool whose job is careful version transitions cannot document only its newest self. |
| 4 | `dev` is labelled unreleased in the site chrome. A search result landing there must not be mistaken for shipped behaviour. |
| 5 | Vendor examples are extracted from `testdata/bundle/` at build time, not written alongside it. That bundle is under test on every run; a hand-copied example is not. |
| 6 | Documentation drift fails the build. Link integrity, two-way nav completeness, and coverage of every port method, error code, exit code and command. |
| 7 | RFCs stay in `rfcs/` and are not published. They are contributor-facing design records with mutable statuses; publishing them would put draft designs beside shipped behaviour. |
| 8 | No API reference. Everything is under `internal/` by deliberate choice, so nothing is importable and a generated reference would document what nobody can use. |
| 9 | The README shrinks to roughly 60 lines and links out. Success is measured by a feature no longer adding a README section. |
| 10 | The manifest reference page is generated from, or checked against, RFC 0004's JSON Schema. Two hand-maintained descriptions of one schema will disagree. |
| 11 | Check 3 covers the manifest and secret schemas and the hook ABI environment, not the port methods the draft named. Port methods are internal by decision 8, and documenting all sixty would be the internal API reference that decision forbids. See the amendment in §5.5. |
| 12 | A contract counts as documented only when a page names it as an identifier — an inline code span — in its own prose, with fenced blocks stripped first. Free-text matching would pass on `name`, `version` and `command` by accident, and counting the included `testdata` example would let a fixture stand in for documentation of the field it happens to use. |
| 13 | The generator runs through `uvx` at an exact pin, with no `pyproject.toml` or lockfile. A Go repository does not acquire a Python project for one build tool, and the pin gives the reproducibility a lockfile was wanted for. |
| 14 | `mike` is squidfunk's zensical-aware fork, pinned by commit SHA. Upstream `mike` drives mkdocs and cannot build a `zensical.toml` site at all — verified, not assumed. A git dependency on a branch would be arbitrary code execution at whatever time a deploy happens to run. |
| 15 | The README shrinks in two steps, not one. P2 removed only the sections whose replacement pages exist; deleting architecture, the step engine and secrets before P4 and P6 have written their replacements would be losing content rather than relocating it. |
| 16 | Decision 10 is not yet satisfied and is not claimed to be. The manifest page is hand-written; what `docs-check` enforces is that its *field list* matches the Go structs. Generation from RFC 0004's JSON Schema is still the target, and the gate means a drifting page fails in the meantime. |
| 17 | Diagrams are mermaid, not d2. The theme renders and themes them already; d2 would add a CI binary, a pre-deploy build step, light/dark variants and a gitignored SVG tree. Every diagram supplements prose that stands without it, which is what makes client-side rendering acceptable. See §5.1. |
| 18 | Docs are versioned by *minor*, not by patch. A patch release rarely changes documentation, and three near-identical entries in a version selector help nobody. `latest` moves only for the newest released minor, so a backport refreshes its own line without sending every reader to older docs. |
| 19 | The README keeps a "Try it" section rather than linking straight out. It is what a visitor with thirty seconds actually wants, and three commands is cheaper to read than a page load. Everything else links. |

## 11. Phasing

- **P1** — `pages/` skeleton, `zensical.toml`, `index.md`, and the `dev`
  deployment workflow. Publishes something immediately, even if thin.
- **P2** — Move the reference material verbatim: commands, exit codes, manifest,
  hook ABI. Pure relocation, no rewriting, and it is what shrinks the README
  most.
- **P3** — `just docs-check` and its CI wiring. Before the prose, so everything
  written afterwards is covered from the start.
- **P4** — `get-started/` and `operating/`, rewritten rather than moved.
- **P5** — `authoring/`, extracted from `testdata/bundle/`. The largest piece of
  genuinely new writing.
- **P6** — `explanation/`, diagrams, and versioned release deployment.

P1–P3 are worth doing together: a site with only reference material and a
working drift check is already better than the current README, and the prose can
land page by page afterwards.

### What P1–P3 shipped

- `pages/` with `zensical.toml`, `index.md` and six reference pages: commands,
  the `secret` group, the `release` group, exit codes, the manifest schema and
  the hook ABI. `just serve-docs` / `just build-docs` / `just deploy-docs`.
- `.github/workflows/docs-dev.yaml` — `main` publishes `dev` through mike, in
  the gh-pages layout the versioned deployment in P6 will extend rather than
  replace. Verified end to end against a scratch repository before shipping.
- `tools/docscheck` and `just docs-check`, in `just ci` and in a change-gated CI
  job that also builds the site. Each of its seven failure modes was verified by
  perturbation — a new flag, a new manifest field, a new error code, a new exit
  code, a new hook variable, a removed nav entry, a broken link — because a gate
  that has never been seen to fail is not known to work.
- The README lost its command table's flag detail, the exit-code table, the
  manifest rules and the hook ABI: 290 lines to 256. The larger shrink is P4 and
  P6, when architecture, the step engine, secrets and testing have somewhere to
  go. Its stale status line — which still said `update` and `rollback` did not
  exist — was corrected in passing.

### What P4–P6 shipped

- **`get-started/`** — a verified download with both checks explained, and a
  first deployment that runs under `--root` so a reader can follow it on a
  laptop and delete it with `rm -rf`.
- **`operating/`** — updating, rolling back, secrets and backups, joining the
  offline install and recovery pages from earlier phases. Six task pages.
- **`authoring/`** — the section that did not exist in any form. Four pages,
  with every example extracted from `testdata/bundle/` at build time, so a
  vendor is reading something the acceptance run installs against real Docker
  on every push.
- **`explanation/`** — architecture, the step engine and the secrets model,
  with mermaid diagrams. This is where the README's long-form arguments went.
- **Versioned release deployment** — `docs-release.yaml` and
  `resolve-docs-version.sh`, verified against a synthetic tag history including
  the case that catches lexical sorting: `1.10` is newer than `1.4`.
- **The README is 76 lines**, from 290. Decision 9 said roughly 60 and measured
  success as "adding a feature no longer adds a README section", which holds:
  what is left is orientation, three demo commands, a table of links, and the
  status paragraph.

**Not** done, and worth naming: the version selector has one entry until there
is a tag, so the versioning is built and unexercised. `overrides/` does not
exist, so there is no "you are reading unreleased docs" banner on `dev` yet —
decision 4 is designed and unshipped.
