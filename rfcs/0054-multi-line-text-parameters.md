# RFC 0054 — Multi-line text parameters

- **Status:** 📝 Draft — proposed 2026-11-09. Decisions 4, 5 and 11 are
  `OPEN` for the author to rule.
- **Scope:** Adds a `text` parameter type for multi-line values that are not
  secret, such as routing rules, allow and deny lists, or a fragment of a
  product's own configuration. It covers:
  - what the type accepts and how it normalises a value;
  - how an operator sets one: `config set`, a new `--set-file` flag on
    `config set` and `init`, and a new `config edit <name>` that opens the
    value in `$EDITOR`;
  - how the value reaches templates, Compose and hooks;
  - what `config list`, `config get`, `describe` and `release diff` show;
  - the manager-version floor a release declaring the type needs.

  It changes the manifest, by one new value in `parameters.<name>.type`. It
  adds one template function, `lines`. It changes neither the installation
  schema nor the hook ABI: a text value is a string in the same map as every
  other parameter, delivered under the same name. It does not narrow
  `string`, which already accepts embedded newlines. It does not let
  `config edit` open several parameters at once, give installation settings
  a text form, or add a second way to deliver a value as a file. §8 names
  each of these.
- **Related:** [`internal/domain/parameter.go`](../internal/domain/parameter.go)
  (`ParameterType`, `Parse`);
  [`internal/lifecycle/ops/config.go`](../internal/lifecycle/ops/config.go)
  (`ConfigSet`, `mergeParameters`);
  [`internal/cli/config.go`](../internal/cli/config.go);
  [`internal/cli/secret_edit.go`](../internal/cli/secret_edit.go) (the
  editor session this reuses);
  [`internal/lifecycle/ops/ops.go`](../internal/lifecycle/ops/ops.go)
  (`runtimeConfig`, the `<PRODUCT>_PARAM_<NAME>` export);
  [`internal/adapters/render/gotemplate/render.go`](../internal/adapters/render/gotemplate/render.go)
  (`funcs`), [`synthetic.go`](../internal/adapters/render/gotemplate/synthetic.go);
  [`internal/lifecycle/ops/release_diff.go`](../internal/lifecycle/ops/release_diff.go);
  [`internal/ui/views/config.go`](../internal/ui/views/config.go);
  [0007](0007-operator-parameters.md) decisions 2, 3, 4, 7 and 10 (the
  parameter surface), [0003](0003-secrets-recovery-and-onboarding.md)
  decision 20 (single-value `secret edit`),
  [0018](0018-the-pre-1-0-manifest-surface.md) decision 1 (the version
  floor), [0027](0027-desired-state-in-a-repository.md) (the installation
  document `describe` prints).
- **Discussion:** [morzecrew/morzer#315](https://github.com/morzecrew/morzer/issues/315).
- **Origin:** A client bundle for dotdash (morzecrew/dotdash) running mihomo,
  whose routing rules are one rule per line and are stored as secrets today
  (#315).

---

## 1. Summary

A release declares a parameter of type `text`:

```yaml
parameters:
  rules:
    type: text
    description: mihomo routing rules, one per line
    services: [mihomo]
```

An operator sets it from a file or an editor:

```sh
morzer config set --set-file rules=rules.txt
morzer config edit rules
```

The value is UTF-8 text of up to 64 KiB. Line endings become LF and trailing
newlines are removed; nothing else is trimmed, so indentation survives. A
template reads it whole as `.Parameters.rules`, or line by line with
`{{ range lines .Parameters.rules }}`. Compose and hooks get it as
`<PRODUCT>_PARAM_RULES`, like every other parameter. `config list` shows the
first line and a line count; `config get`, `--json` and `describe` show the
whole value. A release declaring `text` needs a `min_manager_version` of at
least the release that ships this.

## 2. Motivation

- **A secret is the only home for a list of lines today.** The parameter
  types are `port`, `int`, `bool`, `string`, `enum`, `duration` and `bytes`
  ([`parameter.go:31`](../internal/domain/parameter.go)). dotdash's mihomo
  rules (`DOMAIN-SUFFIX,example.ru`, one per line) are stored as secrets and
  reach mihomo through a file rule provider pointed at `/run/secrets/<name>`
  (#315). That works, and #301 made `secret edit <name>` open such a value as
  plain text, but the value is in the wrong place:
  - **A template cannot read it.** `secretFile` resolves a secret to its
    rendered path ([`render.go:257`](../internal/adapters/render/gotemplate/render.go)),
    never its content, so the bundle cannot render the rules into mihomo's
    own configuration.
  - **It is hidden.** `config list`, `describe` and `installation.yaml`
    carry no secret value, by design. An operator cannot see which rules
    are set without decrypting them.
  - **It gets secret semantics it does not need:** encryption, rotation,
    and backup to the recovery key.
- **`string` takes a multi-line value but mangles it.** Its `Parse` is
  `strings.TrimSpace` and nothing else
  ([`parameter.go:128`](../internal/domain/parameter.go), `:157`), so
  `config set rules="$(cat rules.txt)"` is accepted today. But it strips
  the leading indentation a YAML fragment needs. It has no size limit,
  although every value is in the environment of every Compose call. And
  `config list` prints the whole value into one table cell.

## 3. Current state

- **A parameter is a string in a map.** `Installation.Parameters` is
  `map[string]string`, saved as JSON
  ([`state.go:95`](../internal/infra/state/state.go)) and reported in
  `installation.yaml` through `goccy/go-yaml`
  ([`ops.go:773`](../internal/lifecycle/ops/ops.go)). Measured with that
  encoder, a multi-line value round-trips exactly in each of five cases:
  with and without a trailing newline, several trailing newlines, leading
  indentation, and a tab. The encoder writes a `|`, `|-` or `|+` block
  scalar, falling back to a double-quoted string when the value starts with
  a space or contains a tab. Nothing in the state or the report needs to
  change for a text value.
- **`Parse` normalises every value**
  ([`parameter.go:127`](../internal/domain/parameter.go)), and
  `ResolveParameters` re-validates every recorded value on every operation.
  Narrowing a type therefore fails a recorded value at the next `apply`.
- **Delivery is one map in three places.** `runtimeConfig` exports every
  declared parameter as `<PRODUCT>_PARAM_<NAME>`
  ([`ops.go:1160-1172`](../internal/lifecycle/ops/ops.go)). Compose gets it
  in its process environment, filtered by `exec.FilteredEnv`
  ([`compose.go:110`](../internal/adapters/runtime/compose/compose.go)). No
  env file is involved, so #315's concern about newlines in an env file does
  not arise. Hooks get the same names
  ([`ports/hooks.go:89-93`](../internal/ports/hooks.go)), and templates get
  `.Parameters`. Measured:
  - Docker Compose 5.5.1 interpolates a multi-line value from the process
    environment intact, into both `environment:` and `labels:`
    (`docker compose config` renders it as a `|-` block).
  - On Linux 6.6, one environment string of 128 KiB fails `exec` with
    "argument list too long" (the kernel's `MAX_ARG_STRLEN`); 131 000
    bytes succeeds. `getconf ARG_MAX` is 2 MiB for argv and environment
    together.
  - Go's `os/exec` refuses a NUL byte in an environment value:
    `exec: environment variable contains NUL`.
- **The template functions are a small, deliberate set**
  ([`render.go:251-301`](../internal/adapters/render/gotemplate/render.go)).
  `indent` exists; nothing splits a string into lines.
- **`config set` is an engine operation** (RFC 0007 decision 7). It locks,
  plans under `--dry-run`, records, renders, and re-creates the declared
  `services` ([`config.go:117-206`](../internal/lifecycle/ops/config.go),
  `configSteps` at `:343`). Its description is `set <names>` (`:140`). The
  journal records names, never values, for every parameter type, and so
  does the attestation (`ParameterNames`,
  [`attestation.go:220`](../internal/domain/attestation.go)).
- **`config set` takes `name=value` arguments only**
  ([`cli/config.go:140-197`](../internal/cli/config.go)), split on the first
  `=` by `ParseAssignments`. `init` takes `--set name=value`, repeatable
  ([`commands.go:224`](../internal/cli/commands.go)). The wizard asks for no
  parameters; it only echoes `--set` in its equivalent command
  ([`init_wizard.go:426-427`](../internal/cli/init_wizard.go)).
- **`secret edit <name>` is the session to reuse**
  ([`secret_edit.go:110-125`](../internal/cli/secret_edit.go)). It chooses
  `$VISUAL` then `$EDITOR`, refuses without a terminal, and opens the value
  alone, byte for byte. A newline the editor added is taken back off a value
  that had none, and an emptied file changes nothing (RFC 0003 decision 20).
  It does not check whether the value changed while the editor was open.
- **Where a value is shown:**
  - `status` shows no parameters at all
    ([`status.go:145-157`](../internal/lifecycle/ops/status.go)).
  - `config list` draws a table with the value in a column
    ([`views/config.go:41-58`](../internal/ui/views/config.go)).
  - `config get` prints the value alone, for `$(...)`.
  - `describe` prints the installation document, which is meant to be read,
    diffed and committed (RFC 0027,
    [`describe.go:12-30`](../internal/domain/describe.go)), and carries
    `parameters` whole.
- **`release diff` compares parameter declarations by a description string**
  (`changes`, [`release_diff.go:101-119`](../internal/lifecycle/ops/release_diff.go)),
  which is `<type>, default <default>` (`:125`). A multi-line default would
  appear whole in a one-line row.
- **The render check invents one value per type** (`syntheticValue`,
  [`synthetic.go:149`](../internal/adapters/render/gotemplate/synthetic.go)),
  all single-line.

## 4. Goals / Non-goals

**Goals**

- A multi-line value that is not secret can be declared, set, seen and
  templated as a parameter.
- Saving an unchanged value in any editor changes nothing.
- The value reaches Compose, hooks and templates under the names every
  parameter already uses.
- A value that would break delivery is refused when it is set, not when
  Compose next runs.

**Non-goals**

- Changing `string`. It keeps accepting what it accepts today (decision 15).
- Structured values. A `text` value is lines, and the template decides what
  they mean. A list or map type would need a schema for its elements, which
  is a different RFC.
- Moving anything out of secrets. A vendor that wants to change a secret
  into a text parameter does so in its own release, and the operator sets
  the value again.

## 5. Design

### 5.1 The type

`text` joins the closed set (RFC 0007 decision 2), with a default, a
description, `required` and `services` exactly as the other types have.
`values` stays enum-only. RFC 0007 decision 3 applies unchanged: a text value
is not a secret. It is written to `installation.yaml` in the clear, and is
visible in `describe`, `config list --json` and `docker inspect`.

`Parse` for `text`:

1. Refuse a value that is not valid UTF-8. `encoding/json` replaces invalid
   bytes with U+FFFD when the state is saved, so such a value would change
   silently on its first save.
2. Refuse a NUL byte, because no Compose call or hook could then run.
3. Turn every CRLF into LF.
4. Remove trailing newlines. Nothing else is trimmed: leading whitespace,
   indentation and trailing spaces on a line are kept.
5. Refuse a result longer than 64 KiB (decision 4).

```text
error: parameter "rules": the value is 70.2 KiB, and a text parameter holds at most 64 KiB
hint: every parameter is passed to each Compose call and hook in its environment
```

Step 4 is what makes an edit idempotent. An editor that adds a final newline
and one that does not produce the same value, so saving an unchanged file is
a no-op.

### 5.2 Setting a value

- **`config set name=value`** accepts a text value, as it accepts any other
  type. This is how a script passes one with `$'…'` or `"$(…)"`.
- **`--set-file name=path`** reads the value from a file. It is repeatable,
  and `-` reads standard input, at most once per command. It is on
  `config set`, where it combines with `name=value` arguments in one
  operation, and on `init`, beside `--set`. Naming the same parameter twice
  in one command is refused, as `ParseAssignments` already refuses it. The
  flag takes any parameter type. Before the type's `Parse` runs, it refuses
  a file holding a NUL byte or invalid UTF-8, whatever the type. `string`
  accepts both today. A NUL would stop every Compose call and hook, and
  invalid bytes would change on the state's first save (§5.1). A file is the
  first way either could realistically arrive, so the refusal sits at the
  file boundary, and `string` itself is not narrowed (decision 15). A dotted
  name is refused here: installation settings have no file form (§8).
- **`init`'s equivalent command** prints `--set-file name=<path>` for a value
  that came from a file, never the value itself.

### 5.3 `config edit <name>`

```text
$ morzer config edit rules
(editor opens on the rules value, 42 lines)
✓ set rules (42 → 44 lines); re-created mihomo
```

- It takes one name, and only a `text` parameter. Any other type is refused
  with a hint naming `config set`, since its value is one line anyway.
- It needs a terminal, as `secret edit` does. The non-interactive form is
  `--set-file`.
- It opens the effective value: the recorded one, or the release's default.
  The file holds that value alone plus one final newline, in a directory of
  its own under the system temporary directory. The directory is removed
  however the editor exits, because editors leave swap and backup files
  beside the one they were given. It is not on tmpfs: the value is not
  secret.
- On save, the content goes through `Parse`.
  - A value equal to the effective one changes nothing:
    `no change: rules already matches`.
  - An emptied file changes nothing, as in `secret edit` (RFC 0003
    decision 20). The hint depends on the declaration:
    - **With a default,** it names `config set rules=`, which records an
      empty value, and `config unset rules`, which returns to the default.
    - **With no default,** it says the parameter cannot be left empty.
      `mergeParameters` refuses an empty value for such a parameter as
      missing (`MissingValues`,
      [`config.go:259`](../internal/lifecycle/ops/config.go)), as for every
      type (§8).
  - A refusal from `Parse` names the reason. Nothing has changed at that
    point, and the message says so.
  - Any other value runs the same operation as `config set rules=<value>`:
    locked, planned under `--dry-run`, journaled, re-creating the declared
    `services`.
- A non-zero exit from the editor, or a file the editor removed, changes
  nothing. The second case reuses `secret edit`'s hint about editors that
  save by rename.
- The session holds no lock while the editor is open (decision 8).

### 5.4 Delivery

- **Templates** get the value whole as `.Parameters.<name>`. A new function,
  `lines`, splits it on LF and returns an empty list for an empty value:

  ```yaml
  rules:
  {{- range lines .Parameters.rules }}
    - {{ printf "%q" . }}
  {{- else }} []
  {{- end }}
  ```

  The `else` branch renders `rules: []` for an empty value. Without it the
  key would have no value, which YAML reads as null rather than as an empty
  list. `printf "%q"` writes each line as a double-quoted string. Unquoted, a
  line such as `a: b` would become a mapping and `hello # note` would lose
  its comment. Go's escapes are a subset of YAML's double-quoted ones:
  `"a: b"`, `"hello # note"`, a backslash, a quote, a tab and non-ASCII
  text all parsed back exactly. The existing `quote` function is not safe
  here, because it escapes `"` and not `\` (§8).

  `indent` already covers a fragment pasted whole, as in
  `{{ .Parameters.fragment | indent 4 }}`. sprig's `splitList "\n"` is not
  added: it returns one empty element for an empty value, and a `range`
  over it renders one empty rule.
- **Compose and hooks** get `<PRODUCT>_PARAM_<NAME>`, as every type does
  (decision 5). A product that needs a file instead, such as mihomo's file
  rule provider, gets one from the mechanism that already makes files: a
  `configuration:` template whose body is `{{ .Parameters.rules }}`,
  rendered to a target the Compose file mounts.

### 5.5 What the operator sees

- **`config list`** shows a text value as its first line, then a dim
  `(N lines)`. An empty value shows as empty. `--json` carries the whole
  value.
- **`config get <name>`** prints the whole value and one final newline, so
  `morzer config get rules > rules.txt` writes back what `--set-file` reads.
- **`describe`** carries the value whole. It is the installation as a file
  that can be diffed and committed (RFC 0027), and a summary there would
  make it describe something other than the installation. `goccy/go-yaml`
  writes it as a block scalar (§3).
- **A default is shown parsed,** in `config list`, `config get` and
  `config edit`, so each shows what the deployment receives. Today
  `ConfigList` parses a recorded value but echoes the default unparsed
  ([`config.go:57`](../internal/lifecycle/ops/config.go)), while delivery
  parses it (`ResolveParameters`). A text default written as a `|` block
  ends in a newline, so it would print differently from what Compose gets.
  Parsing the default fixes this for every type: a port default of `08`
  lists as `8`, the value delivered.
- **`status`** is unchanged, since it shows no parameters.
- **The `config set` and `config edit` summary** gives a text parameter's
  line counts before and after, as in `set rules (42 → 44 lines)`.

### 5.6 What a vendor sees

- **The floor.** A manifest declaring a `text` parameter must have
  `compatibility.min_manager_version` of at least
  `TextParametersMinManagerVersion`. An older manager refuses the type as
  unknown (`parameter.go:93-95`), so without the floor the vendor would learn
  it from an operator. Validated as `combinable`'s floor is.
- **The render check** gives a text parameter without a default a
  two-line synthetic value, so a template that assumes one line is rendered
  with more than one.
- **`release diff`** describes a text default by its line count and the
  first 12 hex digits of its SHA-256, as in
  `text, default of 12 lines (sha256 3f9a1c0b77de)`. `changes` compares the
  descriptions, so a change on any line is still listed, and the row stays
  one line.
- **`release show`** lists the type like any other.

### 5.7 Alternatives considered

- **A flag on `string`** (`multiline: true`). This makes one type parse two
  ways, decided by a second field. Every consumer of `Type` would then need
  to read both fields to know what a value can hold. A type of its own is
  what the closed set is for.
- **Narrowing `string` to one line** and adding `text` beside it. Any
  recorded `string` value with a newline would fail at the next `apply`
  (§3), with nothing the operator could do short of
  `config set`. The gain is a guarantee that no bundle has asked for.
- **Exporting a path to a rendered file** (`<PRODUCT>_PARAM_<NAME>_FILE`)
  instead of the value. This is a second delivery path, alongside the
  `configuration:` templates that already render files. The variable works
  (§3), and one name per parameter for Compose, hooks and templates is RFC
  0007 decision 4.
- **`config edit` with no name or several**, opening a mapping as
  `secret edit` does. A multi-line value in a mapping needs a block scalar,
  and the obvious edit folds it into one line. That is the trap #301 fixed
  for secrets.
- **`config set name=@path`**, as curl does. An existing `string` value
  that starts with `@` would change meaning.

## 6. Tests

- **Domain.**
  - `Parse` for `text`: the CRLF, trailing-newline, indentation, NUL,
    UTF-8 and size cases, including exactly 64 KiB and one byte over.
  - A `text` default that fails `Parse` is refused at manifest validation.
  - The floor is required when `text` is declared, and not required
    otherwise.
- **State.** A text value with leading spaces, a tab and blank lines
  round-trips through the JSON state and through `installation.yaml`. The
  `doctor` comparison of the two reports no drift for it.
- **Render.** `lines` handles empty, one line and several lines.
  `.Parameters.<name>` renders whole. The synthetic value has two lines.
- **Suite, against the fake runtime:**
  - `config set --set-file` records the value, puts it in the runtime's
    environment, and re-creates the declared services.
  - `-` reads standard input.
  - An unchanged file is a no-op that journals nothing.
  - Refusals change nothing.
- **CLI.** `config edit`, driven by a scripted editor as `editSecretsWith`
  is ([`secret_edit.go:83`](../internal/cli/secret_edit.go)). The cases:
  - changed, unchanged, emptied;
  - an editor that exits non-zero, and one that removes the file;
  - a non-text parameter, and no terminal;
  - the temporary directory is gone afterwards in every case.
- **Real Compose.** A `docker`-tagged test runs `docker compose config` with
  a multi-line `<PRODUCT>_PARAM_<NAME>` and asserts that the value
  interpolates intact. This pins §3's measurement on the container suites
  job.
- **Not tested:** the 2 MiB total across all values (§9). The acceptance run
  is not extended; the Compose test above covers the one path only real
  Docker exercises.

## 7. Docs

- **`reference/manifest.md`:** the `text` row in the parameter types, the
  normalisation, the limit and the floor.
- **`reference/parameters.md`:**
  - the type;
  - `--set-file`;
  - `config edit`;
  - what `config list`, `config get` and `describe` show.

  It states in so many words that a text value is not secret and appears in
  `docker inspect`.
- **`reference/commands.md`:** `config edit`, and `--set-file` on
  `config set` and `init`.
- **`reference/variables.md`:** `lines`, and a text value in
  `<PRODUCT>_PARAM_<NAME>`.
- **An authoring snippet:** a list of rules rendered with `lines`, and a
  value rendered to a file for a product that reads one.
- **`CHANGELOG.md`:** Added entries for the type, `config edit` and
  `--set-file`.

## 8. Out of scope

- **`config edit` with several names, or none** (§5.7). Demand-gated, and it
  would need a format that does not fold lines.
- **A text form for installation settings.** `installation.domains` is a
  list already, taken comma-separated, and no setting holds lines.
- **The init wizard asking for parameters.** It asks for none today
  (§3). A release with a required text parameter is initialised with
  `--set-file`.
- **A diff view** of old and new values in the journal or the summary. The
  journal holds no parameter values (§3), and the operator has just seen the
  change in their editor. The summary gives line counts (decision 11).
- **An optional parameter that may be empty.** A declaration with no default
  counts an empty value as missing, for every type, so `init` and
  `config set` refuse it. A vendor therefore cannot declare "empty unless the
  operator sets it". A text parameter that may be empty needs a non-empty
  default. Changing what "no default" means is a decision for every type,
  not for this one.
- **`quote` escaping a backslash.** It escapes only `"`
  ([`render.go:281`](../internal/adapters/render/gotemplate/render.go)), so
  `quote` on a value containing `\` can produce an invalid or different
  YAML string. This is true of every value today, not only text. It is its
  own fix, and the examples here use `printf "%q"`.
- **A check of the rendered output.** The render check does not parse a
  rendered YAML file, so a text value pasted into a scalar position without
  `indent` is still found by the product, not by the manager. This is the
  same as for every template today.
- **Refusing invalid UTF-8 in a `string` given as `name=value`.**
  `--set-file` refuses it, and NUL, for every type (§5.2). A shell argument
  cannot hold a NUL, but `$'\xff'` can hold invalid UTF-8. Refusing that in
  `string` itself would narrow the type, which falls under decision 15.

## 9. Risks

- **The environment has a total limit.** At 64 KiB each, about 30 text
  values fill the 2 MiB `ARG_MAX`. Every Compose call and hook would then
  fail with "argument list too long". The per-value limit keeps one value
  safe. The total is accepted rather than enforced: no bundle is near it,
  and a manifest-level cap would refuse releases for values nobody has set.
  The docs state the limit.
- **A credential put in a text parameter is exposed** in `docker inspect`,
  `installation.yaml` and `describe`. This is the same as for `string`
  today, and the docs say so (RFC 0007 decision 3).
- **Last write wins.** Two operators editing the same value at once, or an
  edit racing a `config set`, keep the later save. This is accepted, as in
  `config set` and `secret edit` (decision 8).
- **`installation.yaml` gets harder to read.** A value that starts with a
  space or contains a tab is written double-quoted with escapes (§3). The
  report stays exact, but it no longer reads as lines.

## 10. Unresolved questions

The `OPEN` rows are for the author:

- **Decision 4:** the size limit.
- **Decision 5:** delivery to Compose and hooks.
- **Decision 11:** whether a diff view is needed.

These are #315's three open questions, each with a recommendation. The
floor's value is set by whichever manager release ships this.

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `LOCKED` | `text` is a new type in the closed parameter set (RFC 0007 decision 2), and not a secret (RFC 0007 decision 3, unchanged). The value is a string in `Installation.Parameters`, so neither the installation schema nor the hook ABI changes. Rejected: a flag on `string`, and narrowing `string` (§5.7). Reversing this after release breaks every bundle that declares the type. |
| 2 | `ASSUMED` | `Parse` for `text` turns CRLF into LF, removes trailing newlines and trims nothing else. Leading whitespace is kept, unlike every other type, because a YAML fragment's indentation is its meaning. Removing trailing newlines is what makes an unchanged save a no-op whatever the editor adds. |
| 3 | `ASSUMED` | `Parse` for `text` refuses invalid UTF-8 and NUL. Invalid bytes would be replaced on the state's JSON save, so the value would change silently. A NUL would make every Compose call and hook fail with `exec: environment variable contains NUL`. Both measured (§3). |
| 4 | `OPEN` | The size limit. Recommended: 64 KiB per value, measured after normalisation, with no limit on the total (§9). Measured on Linux 6.6: a 128 KiB environment string fails `exec`, and every value is in the environment of every Compose call and hook. 64 KiB leaves room under that limit. Larger content is a file the bundle ships, or a secret. |
| 5 | `OPEN` | How Compose and hooks get a text value. Recommended: `<PRODUCT>_PARAM_<NAME>`, as for every type. The manager passes the process environment and no env file (`compose.go:110`), and Compose 5.5.1 interpolates the value intact (§3). A product that needs a file gets one from a `configuration:` template, the mechanism that already renders files. Rejected: a `_FILE` path variable, which is a second delivery path (§5.7). |
| 6 | `ASSUMED` | Templates get `.Parameters.<name>` whole and a new `lines` function, which returns an empty list for an empty value. sprig's `splitList` is not added: its empty-value case yields one empty element. |
| 7 | `ASSUMED` | `config edit <name>` takes one `text` parameter and needs a terminal. It opens the effective value alone, plus a final newline, in a temporary directory that is removed afterwards. It follows RFC 0003 decision 20: an unchanged or emptied file changes nothing. The hint for an emptied file names `config set <name>=` only for a parameter with a default, since one with no default refuses an empty value as missing. A saved change is the `config set` operation. Other types are refused with a hint naming `config set`. |
| 8 | `ASSUMED` | `config edit` holds no lock while the editor is open, and does not check whether the value changed meanwhile: last write wins, as with `config set` and `secret edit`. Holding the deployment lock for an editor session would block timers and every other operation for as long as the editor stays open. |
| 9 | `ASSUMED` | `--set-file name=path` on `config set` and `init`, repeatable, with `-` for standard input once. It takes any parameter type, and refuses a dotted setting. Whatever the type, it refuses a file holding a NUL byte or invalid UTF-8 before the type's `Parse` runs, because `string` accepts both and decision 3's reasons apply to every value. One flag name on both commands, as in Helm's `--set-file`. Rejected: `name=@path`, because it changes the meaning of an existing value that starts with `@`. `init`'s equivalent command prints the flag, not the value. |
| 10 | `ASSUMED` | `config list` shows the first line and `(N lines)`. `config get`, `--json` and `describe` show the whole value. `describe` must, because it is the installation document (RFC 0027). This departs from #315, which proposed a summary in `describe` and `status`; `status` shows no parameters today and is unchanged. `config list`, `config get` and `config edit` show a default as `Parse` normalises it, for every type, as delivery already does. Today `ConfigList` echoes it (`config.go:57`). |
| 11 | `OPEN` | Whether old and new values need a diff view. Recommended: no. The journal and the attestation hold parameter names, never values, for every type (`config.go:140`, `attestation.go:220`), so there is nothing there to diff. The `config set` and `config edit` summary gives line counts before and after (§5.5). |
| 12 | `ASSUMED` | A manifest declaring `text` needs `compatibility.min_manager_version` of at least `TextParametersMinManagerVersion`, validated as `CombinableProfilesMinManagerVersion` is. This assumes the next release, 0.15.0, ships it. |
| 13 | `ASSUMED` | The render check gives a text parameter without a default a two-line synthetic value. |
| 14 | `ASSUMED` | `release diff` describes a text default by its line count and the first 12 hex digits of its SHA-256. `changes` compares descriptions, so a change on any line is still listed, and the row stays one line. |
| 15 | `ASSUMED` | `string` is not narrowed. It accepts embedded newlines today (`parameter.go:128`, `:157`), and narrowing it would fail every recorded value that has one at the next `apply`. The docs steer multi-line values to `text`. |

## 12. Phasing

- **P1**, one PR: §5.1–5.6, the docs in §7 and the tests in §6.
  `config edit` could be split out, since it is only an input method. But it
  is the reason #315 was written, and the editor session already exists.
- **P2**, in dotdash: move the rules from secrets to a text parameter in a
  release that raises its floor. Not this repository's work, and gated on P1
  being released.
- §8's items are demand-gated.
