# several-profiles · RFC 0053 P1

On branch `several-profiles` from `main` at `fbaed469`, building
[RFC 0053](../rfcs/0053-several-profiles-per-installation.md) P1: §5.1–§5.8,
the state migration, the docs in §7 and the tests in §6, as ruled by the
author 2026-11-09. Closes #309.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## Plan

Domain:

- `internal/domain/manifest.go`: `RuntimeDecl.Combinable []string`
  (`combinable`); `Selection(names)` and `FilesFor(profiles []string)` with
  §5.2's rules and messages (decisions 1, 3, 4);
  `CombinableProfilesMinManagerVersion = "0.15.0"`, and validation of
  `combinable`'s names, duplicates, pattern and floor (decision 11);
  `Manifest.CombinableNames()` for `release show`, `release diff` and the
  synthetic context (decisions 13, 17, 18). The legacy
  `RuntimeSpec.ComposeFiles` stays: the block it reads is refused, and only
  its own tests call it.
- `internal/domain/release.go`: `RuntimeFilePaths` takes a selection, and a
  `Release.Selection` adds the release's version to the hints §5.2 words with
  one (decision 4).
- `internal/domain/installation.go`: `Profiles []string` (`profiles`), the
  old `Profile` kept as a field that is decoded and never written, schema 18
  (decision 2).
- `internal/infra/state/state.go`: case 17 moves a non-empty `profile` into a
  one-element `profiles` and clears it (decision 2).
- `internal/domain/describe.go`: `profile` comma-joined, and `profiles`
  (decision 9).

Operations:

- `internal/lifecycle/ops/ops.go`: `Options.Profiles []string`;
  `refuseUndeclaredProfile` checks the record and the override through
  `Selection`, adding the refusal for names the target will not run together
  (decision 10); `warnOneRunProfile` compares sets (decision 3);
  `runtimeConfig` and `templateData` take a selection and resolve it to
  layering order; `<PRODUCT>_PROFILE` comma-joined (decision 7).
- `apply.go`: the journal flag `profile` comma-joined (decision 8);
  `refuseChangedProfile` compares sets (decision 3).
- `update.go`, `rollback.go`, `support.go`, `inspect.go` and the other
  `runtimeConfig` callers: a selection, nil for the record. The restore point
  keeps using the record (§5.5).
- `settings.go`: `installation.profile` splits a comma list, trims, checks it
  with `Selection` against the installed release, stores it in layering
  order, and reads back comma-joined; `journalled` and `planSettings` compare
  sets (decisions 3, 8).
- `init.go`: `InitOptions.Profiles`; with `--release`, the stage step checks
  `--profile` against the release it staged (decision 15); a repair without
  `--profile` keeps the record.
- `status.go`: `profile` comma-joined, and `profiles` (decision 9).
- `doctor.go`: the `installation.yaml` drift check compares sets (§5.7).
- `release_diff.go`: a `combinable` entry among the profile changes
  (decision 17).

Render:

- `internal/ports/render.go`: `TemplateData.Profiles []string` replaces
  `Profile`.
- `internal/adapters/render/gotemplate/render.go`: `.Profiles` and
  `.Installation.Profiles`, never nil; `.Profile` becomes a value method
  returning `(string, error)` that fails with several; `has` (decision 6).
- `synthetic.go`: every combinable profile together when any are declared,
  today's first sorted name otherwise (decision 13).
- `references.go`: no change expected. The walker already treats a value
  method as a field (`references.go:207-212`); a test pins that `.Profile`
  still passes.

CLI and views:

- `internal/cli/commands.go`: `--profile` becomes a string slice on `init`,
  `apply` and `update`, as `--domain` is (decision 8).
- `internal/cli/init_wizard.go`: the single choice fills a one-element
  selection, and `EquivalentCommand` prints a `--profile` per name
  (decision 15).
- `internal/ui/views/status.go`: `profile  xdns, xhttp-reality` (§5.7).
- `internal/ui/views/release.go`: a `(combinable)` suffix (decision 17).
- `internal/ports/compose_abi.go`: `PROFILE`'s comment says comma-joined.

Elsewhere:

- `schemas/`: regenerated with `just schemas` (the manifest gains
  `combinable`, the installation document `profiles`).
- Tests: every bullet in §6, in the packages it names.
- Docs: §7's pages, and any other page a search finds showing `profile` in
  `status --json`, `describe` or `--profile`. `CHANGELOG.md`, `Unreleased`.
- Not touched (decision 16): the runtime port, the Compose adapter, the hook
  ABI, acceptance.

### What the spec does not settle

1. **`.Installation.Profile` with several selected.** Load-bearing: it is
   template contract. The entry below.
2. **A schema-18 state that carries the old `profile`.** Only a hand edit, or
   code that set the old field, can produce one. `Validate` refuses it rather
   than ignoring it, so a value the manager would never deploy is not read as
   the base files.
3. **An `installation.yaml` written before schema 18.** The report carries
   `profile`, and `doctor` would call that drift until the next save rewrote
   it. The drift check reads the old field when `profiles` is absent.
4. **`init --profile` without `--release`.** There is no release to check
   against, so only an empty entry and a duplicate are refused, and the
   selection is stored in the given order. Sets are what records are compared
   as, so the order has no effect; the first `config set` stores layering
   order.
5. **The hint on the run-together refusal.** §5.5 asks for "a selection both
   releases accept". Computing one means guessing which single profile
   stands in for a combination. The hint names what the target allows, in
   §5.2's words, and the command that changes the selection.

One of the five is load-bearing, so the spec is ready to execute.

How the code settles what the plan leaves to it, recorded here rather than as
entries because none of them is a decision the RFC would carry:

- An override of nil means the record, as `""` did. pflag reads
  `--profile ""` as an empty slice, so that keeps its meaning.
- `CombinableNames` takes the runtimes in sorted order and each runtime's
  names in its own order, skipping names already seen. With one runtime it is
  that runtime's `combinable`, exactly.

```divergence
decision: 6
grade: ASSUMED
class: spec-gap
at: 2026-11-09T09:10:00Z
attempt: 1
claim: decision 6 makes `.Profile` fail the render with several selected and says nothing of `.Installation.Profile`, the recorded profile beside it (`render.go:186`, `:215`). Left a string, `eq .Installation.Profile "xdns"` is silently false under a selection that holds xdns, which is the failure decision 6 rejects for `.Profile`.
evidence: internal/adapters/render/gotemplate/render.go:182-191
action: departed
proposal: ASSUMED — refines decision 6. `.Installation.Profile` follows `.Profile`'s rule against the recorded selection: the name with one, empty with none, a render error with several.
```

Close-outs for decisions 1 and 2, both `LOCKED`, follow the build.

## Build, 2026-11-09

```divergence
decision: 13
grade: ASSUMED
class: spec-gap
at: 2026-11-09T09:52:17Z
attempt: 1
claim: decision 13 renders every combinable profile together, which is the all-on branch only when the release declares no other profile. With `combinable: [metrics]` beside `embedded` and `external-db`, the synthetic selection is `metrics` alone, `.Profile` renders, and an operator's `embedded,metrics` fails the render at apply: the leftover `.Profile` §5.8 says render-check catches is not caught. The synthetic selection is the first other declared profile, sorted, then every combinable one, which is a selection the release accepts and the branch an operator reaches.
evidence: internal/adapters/render/gotemplate/synthetic.go:90-104
action: departed
proposal: ASSUMED — refines decision 13. `--render-check` renders every combinable profile together with the first other profile the release declares, sorted, when it declares any of each, so a selection of one combinable profile beside an alternative is rendered too.
```

```divergence
decision: 1
grade: LOCKED
kind: resolved
at: 2026-11-09T09:59:27Z
attempt: 1
claim: decision 1 honoured: a release opts in with `runtimes.<name>.combinable`, a selection holds any of those names plus at most one profile outside the list, and a release without the field refuses a second name, so `.Profile` and `<PRODUCT>_PROFILE` keep their meaning for every existing bundle
evidence: internal/domain/manifest.go:471-504
action: decided
```

```divergence
decision: 2
grade: LOCKED
kind: resolved
at: 2026-11-09T09:59:27Z
attempt: 1
claim: decision 2 honoured: the installation records `profiles` at schema 18, the migration from 17 moves a non-empty `profile` into a one-element list and empties it, and `profile` is decoded and never written
evidence: internal/infra/state/state.go:394-403
action: decided
```

Built as planned, with the two entries above. The selection rules are in
`RuntimeDecl.selection` (`internal/domain/manifest.go`), which `FilesFor`,
`Release.RuntimeFilePaths` and `Release.Selection` share. `runtimeConfig` and
`templateData` resolve the effective selection to layering order through it,
so `.Profiles` and `<PRODUCT>_PROFILE` carry that order. `.Profile` is a value
method on the render context, through `ports.OneProfile`.

Where the build settled what the plan left open:

- **`init` checks a selection in the write step,** which loads it against the
  staged release and stores it in layering order. A check in the stage step,
  as the plan had it, was the same refusal one step earlier: the sabotage
  sweep removed it and every test stayed green, so it was taken out.
- **The three-tier tutorial shows an overlay as a snippet** and not in
  `testdata/bundle-web`. Declaring `combinable` there raises the example's
  floor to 0.15.0 before any released manager reads it, which is the reason
  the example bundle waited for 0.12.0 before declaring a preflight check.
- **`release show`** marks a name `(combinable)`; **`release diff`** reports
  `combinable` as one entry among the profile changes, its names joined in
  layering order, so a reordering shows (decision 17).

Tests that changed with the behaviour they pinned:

- `internal/adapters/render/gotemplate/references_internal_test.go`: the row
  for a field on a value with none used `.Profile.Name`. `.Profile` is now a
  method, whose result the walker does not follow, so the row uses
  `.Release.Vendor.Name`, and a new row pins that the profile fields pass.
- `test/contract/statestore.go`: the round trip records two profiles, so the
  battery pins that a selection keeps its order.
- `internal/ui/views/fixtures_test.go`: the troubled status records two
  profiles, and its goldens show `profile  embedded, xdns`.

## Self-audit, 2026-11-09

Scope: the branch against `fbaed469`: the domain (manifest, release,
installation, describe), the state migration, `ops` (init, settings, apply,
update, rollback, status, doctor, release diff and every `runtimeConfig`
caller), the render port and adapter, the CLI flags and wizard, the status and
release views, tests in `internal/...`, `test/suite`, `test/clitest` and
`test/contract`, the regenerated schemas, seven doc pages, the changelog and
this log.

- **Sabotage:** twenty-one mutations, each run against the tests written for
  it, each red:
  - layering in the operator's order;
  - two profiles outside `combinable` allowed;
  - a duplicate allowed;
  - the migration dropping the profile;
  - the old field ignored at schema 18;
  - selections compared in order;
  - the run-together refusal on update skipped;
  - `<PRODUCT>_PROFILE` joined with spaces;
  - `.Profile` taking the first name;
  - `.Installation.Profile` joining the names;
  - the synthetic selection dropping the other profile;
  - the floor not checked;
  - `init` keeping the given order;
  - `init` checking nothing;
  - `doctor` not reading a report from before 18;
  - `release diff` dropping `combinable`;
  - `release show` dropping the suffix;
  - `--profile` not trimmed;
  - `config set` keeping the given order;
  - the status view showing the setting's form;
  - the hint naming no release.

  Two needed a second run. The duplicate mutation first failed to compile,
  which counts as red without proving anything; run in a form that builds, it
  went red. The status view's survived: no test showed several profiles, so
  the troubled fixture now records two.
- **Docs:** every page a search for `profile` found was read; seven changed.
  `just docs-check`: 44 pages, 160 checks, no drift.
- **Residue:**
  - `CombinableNames` across runtimes that disagree may name a set no single
    runtime accepts. Only `release show`, `release diff` and the synthetic
    render read it, and decision 18 leaves disagreement to the vendor.
  - The decision 13 entry's evidence, `synthetic.go:90-104`, was written
    before the doc comment above the function grew by three lines; the
    function is at `:93-106`.

## Acceptance, 2026-11-09

The first `just acceptance` run failed at "a hand edit to installation.yaml
is reported, not obeyed". The stage edits the report's `profile: embedded`
line, which a schema-18 report no longer has: it lists `profiles`, one name
a line. The `sed` changed nothing and `doctor` rightly reported nothing.
`.github/scripts/acceptance.sh` now edits the list, and checks that the edit
landed before asking `doctor`, so a report whose shape changes again fails
there rather than looking like a `doctor` that misses drift.
