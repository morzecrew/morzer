# RFC 0053 — Several profiles per installation

- **Status:** ✅ Complete — shipped 2026-11-09 in #314: P1. P2 is dotdash's
  work, not this repository's. Ruled by the author 2026-11-09: decisions 1
  and 2 confirmed, 3–16 as written, and §11's `OPEN` rows as recommended.
  Rows 19 and 20 were added by execution.
- **Scope:** Lets an installation run more than one deployment profile at once,
  so a bundle whose optional parts are independent no longer has to declare
  every combination as a profile of its own. It covers what a release declares
  to allow this (a `combinable` list beside `profiles`, with a manager-version
  floor), how a selection is checked and layered into the runtime's file list,
  what the installation records (installation schema 18), how `config set
  installation.profile` and `--profile` take a list, the refusal on update and
  rollback, and what templates, Compose files, `status`, `describe` and the
  journal see. It changes the manifest (one new field), the installation
  schema (`profile` becomes `profiles`), the render context (`.Profiles`, a
  `has` function, and a defined `.Profile` when several are selected), and the
  meaning of `<PRODUCT>_PROFILE` when several are selected. It does not change
  the runtime port, which already takes an ordered file list. It does not make
  `requirements.ports` aware of profiles. It does not add a secret that is
  required only under a profile, and it gives hooks no profile. §8 names each
  of these as a follow-up.
- **Related:** [`internal/domain/manifest.go`](../internal/domain/manifest.go)
  (`RuntimeDecl`, `FilesFor`, `ProfileNames`);
  [`internal/domain/installation.go`](../internal/domain/installation.go);
  [`internal/infra/state/state.go`](../internal/infra/state/state.go)
  (`migrateInstallation`);
  [`internal/lifecycle/ops/ops.go`](../internal/lifecycle/ops/ops.go)
  (`refuseUndeclaredProfile`, `runtimeConfig`, `templateData`);
  [`internal/lifecycle/ops/settings.go`](../internal/lifecycle/ops/settings.go);
  [`internal/adapters/render/gotemplate/render.go`](../internal/adapters/render/gotemplate/render.go),
  [`synthetic.go`](../internal/adapters/render/gotemplate/synthetic.go);
  [`internal/ports/compose_abi.go`](../internal/ports/compose_abi.go);
  [0013](0013-bundle-authoring-experience.md) decision 12 (render-check is a
  smoke test), [0018](0018-the-pre-1-0-manifest-surface.md) decision 1 (the
  version floor), [0023](0023-runtimes-beyond-compose.md) decision 7,
  [0042](0042-settings-an-operator-changes-after-init.md) decisions 15–25 (the
  profile as a setting).
- **Discussion:** [morzecrew/morzer#309](https://github.com/morzecrew/morzer/issues/309).
- **Origin:** The dotdash bundle (`morzecrew/dotdash` at `3ca9a8c`, the code its 0.3.0 releases),
  whose two independent overlays need three profiles.

---

## 1. Summary

A release may mark some of its profiles as able to run together:

```yaml
runtimes:
  compose:
    files: [compose/compose.yaml]
    profiles:
      xdns: [compose/xdns.yaml]
      xhttp-reality: [compose/xhttp-reality.yaml]
    combinable: [xdns, xhttp-reality]
```

An operator then selects several:

```sh
morzer config set installation.profile=xdns,xhttp-reality
```

The deployment is the base files, then the profiles' files in the order the
release lists them in `combinable`. Templates test `has "xdns" .Profiles`.
A selection may hold at most one profile that is not in `combinable`, so
profiles that are alternatives, such as the documented `embedded` and
`external-db`, can never be deployed together. A release that declares no
`combinable` behaves exactly as today. The installation records a list at
schema 18, and a recorded single profile migrates to a one-element list.

## 2. Motivation

- **Each independent overlay doubles the profile list.** dotdash declares
  `xdns`, `xhttp-reality` and `xdns-xhttp-reality`, and the last is the
  first two's files together (`bundle/manifest.yaml:22-29`). Its own comment
  says why: "One profile per installation, so both together is a profile of
  its own." Three overlays would need seven profiles.
- **Templates test every name that includes an overlay.** Both of dotdash's
  templates open with `or (eq .Profile "xdns") (eq .Profile
  "xdns-xhttp-reality")`, and the same for the other overlay
  (`bundle/templates/gate.json.tmpl:16-17`, `xray.json.tmpl:41-42`). Each
  new combined name has to be added to every such chain, in every template.
- **An operator adding or dropping one part has to know the combined name.**
  To turn off the DNS tunnel on an installation running
  `xdns-xhttp-reality`, the operator has to find `xhttp-reality`. Nothing
  connects the two names but the vendor's naming.
- **An optional part that needs a secret only when it is used has nowhere
  cheap to go.** morzer renders no file for an unset secret that is not
  required (`internal/adapters/secrets/sopsage/sopsage.go:472-481`), and a
  service that mounts the secret then fails under Compose. So the part
  becomes one more profile, which doubles the list again, or its secrets are
  generated as placeholders. dotdash's IPv6 upstream does the second
  (`bundle/secrets.schema.yaml:73-90`: "Generated as placeholders, because
  the gate mounts them whether or not the proxy is in use"). Combinable
  profiles make the profile route affordable. They do not close the gap
  themselves (§8).
- **The manager already half-knows.** The `init` wizard offers no profile
  first because "a profile may be an optional extra rather than one of
  several topologies" (`internal/cli/init_wizard.go:152-154`). The model
  has one slot for both of those kinds.

## 3. Current state

**Declaration.**

- A runtime declares `files` and `profiles`, a map from a name to the files
  it adds (`internal/domain/manifest.go:380-382`). The manifest is decoded
  strictly (`internal/release/load.go:200`), so an older manager refuses a
  new field outright. That is why new manifest fields have come with a
  `…MinManagerVersion` constant, and with a validator that refuses the field
  below that floor (`manifest.go:51-60`, `:1451-1456`).
- **Profile names are not validated.** The only rule is that a profile lists
  at least one file (`manifest.go:1303-1310`). Runtime names have a pattern
  (`manifest.go:1106`), but profile names have none, so a comma or a space
  in one is legal today.
- `ProfileNames` is the sorted union across runtimes. Disagreement between
  runtimes is left to the vendor (`manifest.go:283-297`).
- morzer's own docs present profiles as alternatives: `embedded` and
  `external-db`, "whether [the database] runs here is the operator's
  decision" (`pages/docs/authoring/a-three-tier-bundle.md:163-176`,
  `your-first-bundle.md:79-81`). Deploying both together has no meaning.

**Files.**

- `RuntimeDecl.FilesFor(profile string)` returns the base files, then the
  profile's files, skipping any file already listed. An unknown name is
  refused, "deploying the wrong topology quietly is worse than stopping"
  (`manifest.go:403-425`). `Release.RuntimeFilePaths` resolves them to
  absolute paths for the installation's runtime (`internal/domain/release.go:62-80`).
- `runtimeConfig(rel, inst, profile)` uses the override when one is given,
  and the recorded profile otherwise (`internal/lifecycle/ops/ops.go:1073-1095`).
  It is called from 22 places, and only two pass an override: apply's
  preflight and `up` (`apply.go:336`, `:784`).
- The runtime port already carries `Files []string` "in merge order"
  (`internal/ports/runtime.go:157-158`). The Compose adapter passes each as
  `--file` in that order (`internal/adapters/runtime/compose/compose.go:93-95`).
  `RuntimeConfig.Profiles` is Compose's own service profiles, which are a
  different thing (`runtime.go:169-171`).

**Record.**

- `Installation.Profile string`, `json:"profile,omitempty"`
  (`internal/domain/installation.go:306-307`). The schema is 17
  (`installation.go:118`). Every field added to the state has bumped it, for
  the write path: an older manager's `config set` rewrites the file and drops
  fields it does not know (`installation.go:36-44`, `:115-117`). An older
  manager refuses a newer schema (`installation.go:1066-1072`).
- Migrations are value-to-value on the decoded struct
  (`internal/infra/state/state.go:175-193`). Every case so far converts
  nothing. Each only raises the version (`state.go:189-393`).
- `doctor` compares the `installation.yaml` report with the state, field
  `profile` included (`internal/lifecycle/ops/doctor.go:390-392`).

**Changing it.**

- `installation.profile` is a setting. Its value is trimmed, checked against
  the installed release's files, and stored. `unset` stores empty, which
  means the base files (`internal/lifecycle/ops/settings.go:338-353`,
  `:482-497`).
- A change is journalled (`settings.go:786-788`). On a running product it
  applies the release under the new profile, behind a restore point, and
  re-creates every container (`settings.go:842-879`; RFC 0042 decisions 16,
  20, 22 and 23).
- `init --profile`, `apply --profile` and `update --profile` are single
  strings (`internal/cli/commands.go:203`, `:250`, `:430`). `--domain` is a
  string slice, taking a comma list or a repeated flag (`commands.go:204`).
- **`init` records `--profile` without checking it.** `init.go:543` is its
  only use outside the repair carry (`init.go:626-627`), and the first
  `apply` is what refuses an undeclared name. The comment on
  `checkProfileDeclared` says "`init --profile` refuses the same name"
  (`settings.go:479-481`), and that is not what the code does.
- A `--profile` that differs from the recorded one warns that it lasts the
  run (`ops.go:966-973`), and the journal records it as the flag `profile`
  (`apply.go:206-207`).

**Update and rollback.**

- `refuseUndeclaredProfile` refuses before any step when the target release
  does not declare the recorded profile or the override. Its hint names
  `config set installation.profile` (`ops.go:939-961`). `update` calls it
  at `update.go:126` and `rollback` at `rollback.go:101`.
- Under the lock, `apply`, `update` and `rollback` refuse when the recorded
  profile changed while they waited (`apply.go:1491-1503`; `apply.go:119`,
  `update.go:193`, `rollback.go:168`).
- A failed update's restore point brings the previous release back under the
  recorded profile, never under the override (`update.go:1037-1041`).

**What templates and runtime files see.**

- The render context has `.Profile`, the effective profile, which is the
  override when there is one, and `.Installation.Profile`, the recorded one
  (`internal/adapters/render/gotemplate/render.go:165-191`, `:215`, `:221`;
  `ops.go:1227-1229`). Rendering uses `missingkey=error` (`render.go:46`).
- **There is no `has` function.** The function set is `secretFile`,
  `default`, `required`, `join`, `quote`, `upper`, `lower`, `trim`,
  `contains`, `replace` and `indent` (`render.go:235-275`). `contains` is
  `strings.Contains`, so `contains .Profile "xdns"` already matches
  `xdns-xhttp-reality`. That shortens the template chains today, but it does
  nothing for the profile list.
- Compose files may interpolate `<PRODUCT>_PROFILE`, the effective profile
  (`internal/ports/compose_abi.go:18-27`, `ops.go:1111`). Hooks get no
  profile at all: the hook ABI has no such variable (`internal/ports/hooks.go:142-162`).

**Vendor-side checks.**

- `release verify` checks template references against the synthetic context's
  shape (`internal/adapters/render/gotemplate/references.go:26-45`), and
  only on the vendor's side: from `checkBundleIntegrity` in `verify`,
  `build`, `pack`, `push` and `archive` (`internal/cli/release.go:409-428`).
  `init` and `update` do not run it. An older manager meeting an unknown
  field or function fails at render, inside the step.
- `--render-check` already sees a profile, a synthetic one: the first
  declared name in sorted order (`synthetic.go:84-92`). It takes no flag for
  which profile to use (`release.go:395-397`).
- `requirements.ports` sees `.Parameters` and nothing else, by design: "A
  third field is a new RFC" (`internal/domain/manifest_template.go:9-23`,
  `:78-95`). It is resolved in apply's preflight and in `doctor`
  (`apply.go:310`, `doctor_machine.go:212`).

**What prints the profile.** `status`, as `profile` in the view and in
`--json` (`internal/lifecycle/ops/status.go:33`, `:150`;
`internal/ui/views/status.go:74`). `installation describe`, as `profile`
(`internal/domain/describe.go:66`, `:152`). `release show` lists the
declared names (`internal/ui/views/release.go:118-120`), and `release diff`
compares their sets (`release_diff.go:93`). Neither the Prometheus export
nor `logs`, `ps`, `stats` and `exec` prints a profile. Those four resolve
their files through `runtimeConfig` with no override
(`internal/lifecycle/ops/inspect.go:66`), so they follow the record.

## 4. Goals / Non-goals

**Goals**

- An installation runs any set of the profiles a release declares
  combinable, plus at most one other.
- A release that declares nothing new behaves exactly as today, under every
  name, field and variable.
- A selection the release cannot honour is refused before anything is
  written, with a hint naming what it can honour.
- dotdash can drop its combined profile without stranding an installation
  that runs it.

**Non-goals**

- **Deciding whether two overlays make sense together.** That is the
  vendor's claim, made by listing both in `combinable`. Compose merges their
  files by its own rules (§5.2).
- **Groups, conflicts or dependencies between profiles.** "At most one
  outside the list" covers alternatives plus overlays. A richer algebra waits
  for a bundle that needs one.
- **Changing the runtime port or the Compose adapter.** Both already take an
  ordered file list.

## 5. Design

### 5.1 What a release declares

`combinable` is a list of profile names beside `profiles`, in each runtime
that has them:

```yaml
runtimes:
  compose:
    files: [compose/compose.yaml]
    profiles:
      xdns: [compose/xdns.yaml]
      xhttp-reality: [compose/xhttp-reality.yaml]
    combinable: [xdns, xhttp-reality]
compatibility:
  min_manager_version: "0.15.0"   # the release that ships this RFC
```

Validation refuses, by field path:

- a name in `combinable` that this runtime's `profiles` does not declare;
- a name listed twice;
- a name that does not match `^[a-z][a-z0-9-]{0,31}$`, the runtime-name
  pattern (`manifest.go:1106`). This applies to names in `combinable` only,
  so no profile name that loads today is refused. It guarantees that nothing
  in a multi-name selection contains a comma;
- `combinable` with `compatibility.min_manager_version` below
  `CombinableProfilesMinManagerVersion`, in the form the
  `restartable` floor already uses (`manifest.go:1451-1456`). An older
  manager refuses the field as unknown. The floor turns that into "needs
  manager X" (RFC 0018 decision 1).

The order of `combinable` is the order in which the profiles' files are
layered (§5.2). It is the vendor's to choose, because the vendor knows which
overlay should win a scalar both set.

### 5.2 A selection, and the file list it produces

`FilesFor` takes a selection:

```go
// FilesFor returns this runtime's files for a selection of deployment
// profiles: the base files, then the one profile outside `combinable` if
// any, then the combinable ones in `combinable` order. A file already in
// the list is not added again.
func (d RuntimeDecl) FilesFor(profiles []string) ([]string, error)

// Selection checks names against this declaration and returns them in the
// order FilesFor layers them.
func (d RuntimeDecl) Selection(names []string) ([]string, error)
```

The rules, each a `ValidationError` naming the input:

| Input | Result |
| --- | --- |
| empty | the base files, as today |
| a name not declared | `unknown deployment profile "x"`, with today's hint listing the declared names (`manifest.go:414-417`) |
| a name twice | `profile "xdns" is selected twice` |
| an empty entry (`xdns,`) | `an empty profile name in the selection` |
| two names outside `combinable` | `profiles embedded and external-db cannot be deployed together`, with the hint `release 1.4.0 lets these run beside one other profile: xdns, xhttp-reality` or `release 1.4.0 declares no profile combinable` |

**The operator's order is not significant.** `xhttp-reality,xdns` and
`xdns,xhttp-reality` are the same selection. Both are layered in the
release's order, and both are stored and shown in that order. The files of
a deployment are always derived from the release being deployed, so a later
release that reorders `combinable` changes the layering, as a vendor's
release may. Equality between two records, in `refuseChangedProfile`,
`journalled` and `doctor`, compares sets.

**Overlapping keys are allowed.** dotdash's two overlays both add a `ports`
entry to the `gate` service (`compose/xdns.yaml:7-9`,
`compose/xhttp-reality.yaml:7-9`), and Compose merges the two. That is the
normal case for overlays, not a conflict. The manager does not inspect what
the files define. A merge Compose rejects fails at `up`, inside the step,
and is compensated like any failed `up`.

### 5.3 What the installation records

Schema 18 replaces `profile` with a list:

```json
{ "schema_version": 18, "installation": { "profiles": ["xdns", "xhttp-reality"] } }
```

- `Installation.Profiles []string`, `json:"profiles,omitempty"`, holds names
  in layering order. Absent means the base files.
- Migration 17 → 18 is the first that converts a value. A non-empty
  `profile` becomes a one-element `profiles`, and `profile` is cleared. The
  struct keeps `profile` as a field that is decoded and never written, so
  the value is still there for the migration to read. This is the shape the
  manifest kept `runtime:` in to refuse it (`manifest.go:1240-1247`).
- An installation recorded under a profile whose name contains a comma
  migrates to one element and keeps working. It can no longer be selected
  again through the comma syntax (§9).
- An older manager refuses a schema-18 state, as it refuses every bump
  (`installation.go:1066-1072`). Without the bump it would decode the file,
  ignore `profiles`, deploy the base files and remove the overlays'
  services, which is the silent change of topology the manifest exists to
  refuse.

### 5.4 Choosing a selection

```text
morzer config set installation.profile=xdns,xhttp-reality
morzer config set installation.profile=xdns           # drop one
morzer config unset installation.profile              # the base files
morzer init --release ./bundle --profile xdns,xhttp-reality
morzer apply --profile xdns --profile xhttp-reality   # this run only
```

- **`config set`** takes a comma list, as `installation.domains` does
  (`settings.go:319-336`). Entries are trimmed, checked by `Selection`
  against the installed release for this installation's runtime, and stored
  normalized. `Read` returns the comma-joined record, so `config get` prints
  what `config set` takes. A change runs as today's journalled `config`
  operation, with the converge, restore point and forced re-create of RFC
  0042 decisions 16, 20, 22 and 23, unchanged. The plan compares the old
  and new selections as sets, so a reordering alone changes nothing and
  journals nothing.
- **`--profile`** on `init`, `apply` and `update` becomes a string slice,
  as `--domain` is (`commands.go:204`). One name parses exactly as before.
  An override replaces the selection for the run. It is not merged with the
  recorded one, it is checked by the same rules, and it warns as today when
  it differs from the record. The journal flag `profile` holds it
  comma-joined.
- **`init`** checks the selection against the release it stages when
  `--release` is given, so an impossible combination is refused before an
  installation exists rather than at the first `apply` (§3).
  `init --repair` without `--profile` keeps the recorded selection, as
  today (`init.go:626-627`).
- **The wizard** keeps its single choice (`init_wizard.go:158-166`). A
  combination is a flag or a later `config set`.

### 5.5 Update and rollback

`refuseUndeclaredProfile` checks the recorded selection and the override
through `Selection` against the target release. An update or rollback is
refused before any step when the target:

- does not declare one of the recorded names, which is today's refusal with
  today's hint; or
- declares them all but does not let them run together, for example a
  release from before the vendor added `combinable`. The message is
  `release 0.3.0 does not let xdns and xhttp-reality run together`, and the
  hint names `config set installation.profile` with a selection both
  releases accept.

This is RFC 0042 decision 17 applied to a set: no fallback, so a topology
never changes as a side effect of an update. A failed update's restore point
still uses the recorded selection (`update.go:1037-1041`), which the
previous release accepted when it was recorded.

### 5.6 What templates and Compose files see

| Selection | `.Profiles` | `.Profile` | `<PRODUCT>_PROFILE` |
| --- | --- | --- | --- |
| none | `[]` | `""` | `""` |
| one | `["xdns"]` | `"xdns"` | `xdns` |
| several | `["xdns", "xhttp-reality"]` | **render error** | `xdns,xhttp-reality` |

- `.Profiles` is the effective selection in layering order, never nil.
  `.Installation.Profiles` is the recorded one, beside
  `.Installation.Profile`, as the pair is split today.
- `has` joins the function set: `has "xdns" .Profiles`, with the argument
  order sprig uses. It is one `slices.Contains`.
- **`.Profile` fails the render when several are selected**, with the
  message "`.Profile` names one profile and this installation runs several;
  use `.Profiles`". `.Profile` is used in two ways. dotdash compares it, and
  `synthetic.go:78` names that as the idiom (`eq .Profile "x"`). The
  repository's own test bundles print it
  (`testdata/bundle/templates/application.yaml.tmpl:17`). Against a joined
  value or an empty one, a comparison quietly takes the else branch, and a
  printed value is one the product has never seen. It becomes a method that
  returns `(string, error)`, which text/template stops on. The reference
  walker must resolve it as it resolves the field today
  (`references.go:26-45`).
- **`<PRODUCT>_PROFILE` carries the selection comma-joined.** A Compose file
  cannot fail, and the variable is interpolated, not compared. The hook ABI
  already joins a list the same way in `DOMAINS` (`hooks.go:157`). No new
  variable.

A release that declares no `combinable` can never have several selected, so
the third row never happens to it. That is what makes the existing meanings
of `.Profile` and `<PRODUCT>_PROFILE` safe without asking any vendor to
change.

### 5.7 What the operator sees

- `status --json` and `installation describe` keep `profile` as a string.
  It is now the setting's form: comma-joined, and identical to today for
  none or one. Each gains `profiles`, a list, as `domains` already is
  (`describe.go:66-67`). Under `--json`, no existing field changes meaning.
  `profile` is still the value `config set installation.profile` takes.
- `status` shows `profile  xdns, xhttp-reality`.
- `release show` marks the combinable names, and `release diff` reports a
  change to `combinable` (decision 17).
- The `installation.yaml` report carries `profiles`, and `doctor`'s drift
  check compares sets (`doctor.go:390-392`).

### 5.8 Vendor-side checks

- **`--render-check`'s synthetic selection** is every combinable profile
  together, in layering order, when the release declares any. Otherwise it
  is today's first sorted name (`synthetic.go:84-92`). That renders the
  all-on branch, the one an operator reaches and the vendor's CI otherwise
  never does. It also catches a leftover `.Profile` in an opted-in bundle,
  because with two or more combinable profiles that field now fails the
  render.
- **No `--profile` on `release verify`.** Render-check is a smoke test (RFC
  0013 decision 12), and a flag for choosing the selection is a new surface
  that no bundle has asked for yet (§8).
- **`requirements.ports` is unchanged** (§8).

### 5.9 Rollout

| Manager | Bundle | Outcome |
| --- | --- | --- |
| new | no `combinable` | as today. A second name is refused because none is combinable |
| new | `combinable` | several selectable |
| old | `combinable` | refused at load: the floor, or the unknown field |
| old | `.Profiles` or `has` with no `combinable` and no floor | the render fails inside `apply`/`update`, which compensates (decision 12) |
| old | state written by new | refused: schema 18 |

dotdash's path, which a vendor dropping a combined profile has to follow:

1. **Release N+1** declares `combinable: [xdns, xhttp-reality]`, raises
   `min_manager_version`, and **keeps** `xdns-xhttp-reality`. An
   installation that records it would otherwise be refused the update
   (§5.5). Its templates test `or (has "xdns" .Profiles) (has
   "xdns-xhttp-reality" .Profiles)` for that release.
2. Operators on `xdns-xhttp-reality` run `config set
   installation.profile=xdns,xhttp-reality`. The files are the same, and the
   converge re-creates the containers.
3. **Release N+2** drops `xdns-xhttp-reality` and simplifies its templates
   to `has "xdns" .Profiles`. An installation that still records the
   combined profile is refused the update, with the hint.

A rollback from N+1 to N with the combination recorded is refused, because N
does not let the two run together. Selecting `xdns-xhttp-reality` first
makes it possible. This is the "two changes" RFC 0042 decision 17 already
accepts for a renamed profile.

### 5.10 Alternatives considered

- **Any declared profiles combine, with no opt-in** (the issue's proposal).
  It is less to declare. But the documented `embedded` and `external-db`
  would then deploy together into a topology nobody wrote. Every existing
  template's `.Profile`, and every Compose file's `<PRODUCT>_PROFILE`, would
  also have a value for several that no vendor has tested. The opt-in costs
  one line in the manifest and keeps every existing bundle's meaning closed.
- **The operator's order decides the layering** (the issue's proposal). It
  is flexible, but two installations with the same parts would then deploy
  different merges. Reordering would become a change of topology with
  nothing visible to show for it, and the operator would have to know which
  overlay overrides which, which is the vendor's knowledge.
- **Overlays as a second map with their own setting**, `overlays:` beside
  `profiles:` and `installation.overlays` beside `installation.profile`.
  This has the cleanest semantics, and `.Profile` would never change. But
  moving `xdns` from a profile to an overlay is a rename the update refuses
  (§5.5). There is also no single command that swaps a profile for an
  overlay against a release that has only one of them, and two settings
  could disagree with each other.
- **A comma string in the existing `profile` field, with no schema bump.**
  An older manager would read `"xdns,xhttp-reality"` and refuse it as an
  unknown profile, so this fails closed. But it is an unclear refusal
  instead of the schema message. A legal profile name may contain a comma
  (§3), so the stored value would be ambiguous. And the state's own type
  would lie about what it holds.
- **`profile` for one, `profiles` for several.** No migration is needed. But
  there are two fields for one fact, and it still needs the bump, because an
  older manager would ignore `profiles` and deploy the base files.
- **`.Profile` as the first name, or the joined names, or empty, when several
  are selected.** Each of these makes `eq .Profile "x"` silently false or
  silently true. An error is the only one of the four that reaches the
  vendor before an operator does.
- **Compose's own service profiles (`--profile`)** in place of files. These
  select services. They cannot add a port to a service that always runs,
  which is what both of dotdash's overlays do.

## 6. Tests

- **Domain**: `Selection` and `FilesFor` cover every row of §5.2's table.
  Layering order follows `combinable` whatever order the input has. A file
  listed by two profiles, or by the base, is passed once. The manifest
  validator refuses an undeclared name, a duplicate or a bad name in
  `combinable`, and `combinable` below the floor. A profile name that loads
  today still loads.
- **State**: a schema-17 installation with `profile: embedded` loads as
  `profiles: [embedded]`, and one with no profile loads with none, in the
  pattern of `TestASchemaThreeInstallationStillLoads`. Schema 19 is refused.
- **Settings and the fake-adapter suite** (`test/suite/profile_setting_test.go`
  extended): `config set` of two combinable names applies under both. A
  reordered selection is a no-op. Two names outside `combinable` are refused
  with nothing changed. Dropping one name removes its services. A failed
  change brings the old selection back. An update or rollback to a release
  without `combinable` is refused with the hint. A selection changed while
  waiting for the lock refuses. A failed update under a list override
  restores the recorded selection.
- **Render**: `.Profiles` and `.Installation.Profiles` follow override and
  record. `has` works. `.Profile` errors with several and is unchanged for
  none or one. An existing template using `.Profile` still passes `release
  verify`'s reference check. The synthetic selection is all combinable
  profiles.
- **Runtime variables**: the builder sets `<PRODUCT>_PROFILE` comma-joined.
  The existing test asserting the builder against `ComposeVars` still holds.
- **Not tested**: a new acceptance case. The port already takes an ordered
  file list (`runtime.go:157-158`), acceptance already drives it with one
  profile, and Compose's merge of several `-f` files is Compose's own
  behaviour.

## 7. Docs

- `reference/manifest.md#runtimes`: `combinable`, its order, its floor, and
  "at most one profile outside it".
- `reference/parameters.md#changing-the-profile` and the settings table: a
  comma list, the operator's order not counting, and the update and rollback
  refusal for a combination.
- `reference/commands.md`: `--profile` on `init`, `apply` and `update` takes
  several.
- `reference/variables.md`: `.Profiles`, `.Installation.Profiles`, `has`,
  `.Profile`'s error, and `<PRODUCT>_PROFILE` comma-joined.
- `authoring/a-three-tier-bundle.md#profiles`: an overlay beside the two
  alternatives, so the page shows both kinds. It must say that a combined
  profile is dropped over two releases (§5.9), not one.
- `CHANGELOG.md`, `Unreleased`.

## 8. Out of scope

- **A secret required only when a profile is selected.** An optional
  service's secret can be `required: false` and mounted by its overlay
  alone. But an operator who selects the profile without setting the secret
  is caught only by Compose at `up`, inside the step. A secret-schema entry
  naming the profiles it is required under would refuse that before
  anything runs. It is a change to the secret schema with its own floor, and
  it is a follow-up. dotdash's placeholder secrets keep working until then.
- **`requirements.ports` that depend on a profile.** That needs `.Profiles`
  and `has` in the manifest's template context, which today holds
  `.Parameters` alone by design (`manifest_template.go:9-23`). It also
  needs a rule that an entry rendering empty is not a requirement. dotdash's
  case is one TCP port, which `compose up` reports clearly when taken. It is
  a follow-up when a second bundle asks.
- **`release verify --profile`**, which would render a chosen selection. It
  is the escape hatch if the all-on synthetic proves too narrow.
- **A profile in the hook ABI.** Hooks have none today, and nothing here
  needs one.
- **A multi-select in the wizard.** It is a convenience over `--profile` and
  `config set`.

## 9. Risks

- **The vendor's `combinable` claim can be wrong.** Two overlays may merge
  into something broken. Render-check's all-on synthetic exercises the
  templates, and a merge Compose rejects fails at `up` and is compensated. A
  merge Compose accepts but the product does not is the vendor's bug, as any
  profile's is today.
- **The schema bump refuses older managers on every machine the new one
  touches**, single-profile installations included. Every bump costs this.
  A manager downgrade needs the state from before the upgrade.
- **The first migration that converts a value.** A mistake here loses an
  installation's profile on load. The decode-only legacy field and the
  load tests in §6 are the guard.
- **`.Profile` becoming an error surprises a vendor who opted in without
  reading.** It surfaces in their own `release verify --render-check`
  before an operator meets it.
- **A profile named with a comma** can no longer be selected through
  `config set` or `--profile`. None is known, and a recorded one keeps
  working (§5.3).
- **"Profile" also names Compose's service profiles**
  (`runtime.go:169-171`). The docs keep saying "deployment profile".

## 10. Unresolved questions

- The floor's value. It is whichever manager release ships this, set at
  build time.
- The two follow-ups in §8 that this design shapes: a secret required under
  a profile, and profile-aware ports. Each needs its own RFC once a bundle
  asks for it.

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `LOCKED` | A release opts in per profile with `runtimes.<name>.combinable: [names]`. A selection holds any of those names plus at most one profile outside the list. A release without the field can only ever run one profile, so `.Profile` and `<PRODUCT>_PROFILE` keep today's meaning for every existing bundle. morzer's own documented profiles (`embedded`, `external-db`) are alternatives, and combining them has no meaning. Reversing this, so that any profiles combine, would reopen decision 6's `.Profile` behaviour and decision 7's variable for every bundle in the field. |
| 2 | `LOCKED` | The installation records `profiles`, a list, at installation schema 18. Migration 17 → 18 moves a non-empty `profile` into a one-element list, and `profile` stays as a decode-only field. Rejected: a comma string in `profile` (ambiguous, because profile names are unvalidated, and the refusal on an older manager is unclear), and `profile` plus `profiles` (two truths, and it still needs the bump). Changing this later is a second migration. |
| 3 | `ASSUMED` | Layering is the base files, then the one profile outside `combinable`, then the combinable ones in `combinable` order. A file already listed is skipped (`manifest.go:419-423`). The operator's order is not significant. Selections are stored and shown in layering order and compared as sets. Rejected: the operator's order (§5.10). |
| 4 | `ASSUMED` | An unknown name, a duplicate, an empty entry, and two names outside `combinable` are each refused, with the messages in §5.2. The unknown-name message and hint are today's. |
| 5 | `ASSUMED` | Combinable overlays may define the same keys. Compose merges them in layering order, and the manager does not inspect the files. dotdash's two overlays both extend `gate`'s `ports`. |
| 6 | `ASSUMED` | Templates get `.Profiles` and `.Installation.Profiles` (never nil) and a `has` function. `.Profile` is the name with one profile, empty with none, and fails the render with several. Rejected: first, joined or empty, each of which makes `eq .Profile` silently wrong. |
| 7 | `ASSUMED` | `<PRODUCT>_PROFILE` carries the selection comma-joined in layering order, as `DOMAINS` joins in the hook ABI. No new variable. It is unchanged for none or one. |
| 8 | `ASSUMED` | `config set installation.profile` takes a comma list, as `installation.domains` does. `--profile` on `init`, `apply` and `update` becomes a string slice, as `--domain` is. An override replaces the selection for the run, and the journal flag `profile` holds it comma-joined. |
| 9 | `ASSUMED` | `status --json` and `describe` keep `profile` as the setting's string (comma-joined, identical for none or one) and add a `profiles` list. No existing JSON field changes meaning. |
| 10 | `ASSUMED` | Update and rollback are refused before any step when the target does not declare every recorded name or does not let them run together. The hint names `config set installation.profile`. This is RFC 0042 decision 17 applied to a set. |
| 11 | `ASSUMED` | `combinable` needs `compatibility.min_manager_version` of at least `CombinableProfilesMinManagerVersion`, validated as `restartable`'s floor is. Its names must be declared, unique, and match `^[a-z][a-z0-9-]{0,31}$`. Only names in `combinable` are constrained, so no bundle that loads today is refused. |
| 12 | `ASSUMED` | `release verify` does not check whether a bundle uses `.Profiles` or `has` without `combinable` and below the floor. An older manager fails that render inside the step and compensates. The docs tell vendors that both need the floor. A check would need a list of the template surface tagged with the version that added each part, which does not exist. |
| 13 | `ASSUMED` | `--render-check` renders all combinable profiles together when any are declared, and today's first sorted name otherwise. `release verify` gets no `--profile` flag. |
| 14 | `ASSUMED` | `requirements.ports` stays `.Parameters`-only. Profile-aware ports are a follow-up (§8). |
| 15 | `ASSUMED` | `init` checks the selection against the release it stages when `--release` is given. Today it checks nothing, despite the comment at `settings.go:479-481`. The wizard keeps a single choice. |
| 16 | `ASSUMED` | No change to the runtime port, the Compose adapter or the hook ABI, and no new acceptance case: `RuntimeConfig.Files` is already ordered. |
| 17 | `ASSUMED` | How `release show` marks combinable names and how `release diff` reports a change to `combinable`. Recommended: a suffix in `show`, and a `combinable` entry in the diff's `profiles` changes. The implementation settles the exact output against the existing views. Was `OPEN`; ruled by the author 2026-11-09: as recommended. |
| 18 | `ASSUMED` | Whether runtimes in one release that disagree about `combinable` are refused. Recommended: no, as `ProfileNames` already leaves disagreement to the vendor (`manifest.go:283-287`), and a selection is checked against the installation's own runtime. Was `OPEN`; ruled by the author 2026-11-09: as recommended. |
| 19 | `ASSUMED` | Decision 6, refined: `.Installation.Profile` follows `.Profile`'s rule against the recorded selection: the name with one, empty with none, a render error with several. Left a string, `eq .Installation.Profile "xdns"` would be silently false under a selection that holds xdns, the failure decision 6 rejects for `.Profile`. Added by execution 2026-11-09 — see logs/several-profiles.md (decision 6, 2026-11-09T09:10:00Z). |
| 20 | `ASSUMED` | Decision 13, refined: `--render-check` renders every combinable profile together with the first other profile the release declares, sorted, when it declares any of each. Every combinable profile alone is not a selection an operator reaches when the release declares alternatives beside them, and a leftover `.Profile` would go uncaught. Added by execution 2026-11-09 — see logs/several-profiles.md (decision 13, 2026-11-09T09:52:17Z). |

## 12. Phasing

- **P1**, one PR: §5.1–5.8, the state migration, the docs in §7, and the
  tests in §6.
- **P2**, in dotdash: §5.9's two releases. Not this repository's work, and
  gated on P1 being released.
- Nothing is demand-gated within P1. §8's items are demand-gated.
