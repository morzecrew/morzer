# text-parameters · RFC 0054 P1

On branch `text-parameters-p1` from `main` at `8ff4e369`, building
[RFC 0054](../rfcs/0054-multi-line-text-parameters.md) P1: §5.1–§5.6, the
docs in §7 and the tests in §6, as ruled by the author 2026-11-09
(decisions 4, 5 and 11 as recommended). Closes #315.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## Plan

Domain:

- `internal/domain/parameter.go`:
  - `ParamText`, added to `ParameterTypes` (decision 1).
  - `Parse` for `text`: invalid UTF-8 and NUL refused, CRLF to LF,
    trailing newlines removed, nothing else trimmed (decisions 2, 3), and
    at most `TextParameterLimit` (64 KiB) after normalisation (decision 4).
  - `ParameterFileValue(name, data)`: the `--set-file` boundary, which
    refuses NUL and invalid UTF-8 whatever the type (decision 9).
  - `LineCount` and `FirstLine`, for the summary, `config list` and
    `release diff`.
- `internal/domain/manifest.go`: `TextParametersMinManagerVersion =
  "0.15.0"` and its floor check beside `combinable`'s (decision 12).

Render:

- `internal/adapters/render/gotemplate/render.go`: `lines`, which splits on
  LF and returns an empty list for an empty value (decision 6).
- `synthetic.go`: a two-line synthetic value for `text` (decision 13).

Operations:

- `internal/lifecycle/ops/config.go`:
  - `ConfigList` parses the default as it parses a recorded value
    (decision 10, every type).
  - `configSummary` gives a text parameter's line counts before and after
    (decision 11, §5.5).
- `release_diff.go`: `describeParameter` gives a text default as a line
  count and a 12-hex SHA-256 prefix (decision 14).
- `init.go`: `InitOptions.ParameterFiles`, the paths `--set-file` read,
  used only by the wizard's equivalent command (decision 9).

CLI:

- `internal/cli/config.go`: `config set --set-file name=path`, repeatable,
  with `-` once for stdin. A dotted name is refused, as is a name given both
  as `name=value` and by file (decision 9). `config set` with neither is
  still "nothing to change".
- `internal/cli/config_edit.go` (new): `config edit <name>` on the
  `secret edit` session pieces (`editorCommand`, `runEditor`, the terminal
  check), with a `configEditWith` seam for a scripted editor (decisions 7,
  8; §5.3). One `text` parameter only. The value plus a final newline goes
  in a fresh directory under the system temp dir, removed however the
  editor exits. Unchanged and emptied files change nothing, with the
  emptied-file hint that depends on whether a default exists. Anything else
  runs `ops.ConfigSet`.
- `internal/cli/commands.go`: `init --set-file`, merged with `--set`, with
  duplicates refused.
- `internal/cli/init_wizard.go`: `EquivalentCommand` prints
  `--set-file name=<path>` for a file-sourced value.
- `internal/ui/views/config.go`: a text value is shown as its first line
  plus a dim `(N lines)`; empty stays empty (decision 10).

Docs, schema, changelog:

- `reference/manifest.md`: the type, its normalisation, limit and floor.
- `reference/parameters.md`: `--set-file`, `config edit`, and what each
  view shows, including that a text value is not secret.
- `reference/commands.md`: `config edit`, and `--set-file` on `config set`
  and `init`, plus the regenerated command index.
- `reference/variables.md`: `lines`, and a text value in
  `<PRODUCT>_PARAM_<NAME>`.
- An authoring snippet: the quoted `lines` list, and a value rendered to a
  file.
- `schemas/` regenerated. `CHANGELOG.md` Unreleased: Added for the type,
  `config edit` and `--set-file`; Fixed for `config list`/`get` showing a
  default as delivered.

Tests (§6):

- Domain: every `Parse` case (CRLF, trailing newlines, indentation, NUL,
  UTF-8, exactly 64 KiB and one byte over), `ParameterFileValue`, the
  floor, and a bad text default refused.
- State: a text value with leading spaces, a tab and blank lines
  round-trips through the JSON state and `installation.yaml`, and `doctor`
  reports no drift.
- Render: `lines` cases, and the two-line synthetic value.
- Suite (fake runtime): `config set --set-file` records the value, delivers
  it in the runtime environment and re-creates the declared services; `-`;
  an unchanged file journals nothing; refusals change nothing; the summary's
  line counts; `config list` shows a default parsed.
- CLI: `config edit` against a scripted editor, covering changed,
  unchanged, emptied (both hints), non-zero exit, removed file, non-text
  parameter and no terminal, with the temp directory gone in every case.
  Also `init --set-file` and the equivalent command.
- Real Compose: a `docker`-tagged suite test, so that
  `compose.Runtime.Validate` (`docker compose config`) carries a multi-line
  `<PRODUCT>_PARAM_<NAME>` intact.

Decisions the spec does not settle:

- None load-bearing. §5.6 says `release show` "lists the type like any
  other", but `release show` lists no parameters
  (`internal/ui/views/release.go` has parameters only in the diff), so
  there is nothing to build. A one-line text value shows as `(1 line)`,
  following decision 10's wording.

Readiness gate: zero load-bearing gaps.

## Build

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-09T14:48:51Z
attempt: 1
claim: a refused value's reason never reaches the operator. `printError` prints an error's Message and Hint and never its cause, and `mergeParameters` and `ResolveParameters` wrap a `Parse` refusal as `parameter "<name>"` with the type's description as the hint. So `config set http_port=70000` says only `parameter "http_port"`, and a text value over the limit would never say why. §5.1 writes the refusal as `parameter "rules": the value is …`. Both wrappers now carry the reason in the message, for every type, and keep the refusal's own hint where it has one.
evidence: internal/cli/root.go:505-511
action: decided
proposal: ASSUMED — a refused parameter value names its reason in the message, `parameter "<name>": <reason>`, for every type. The hint is the refusal's own when it has one, and the type's description otherwise.
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-09T15:12:41Z
attempt: 2
claim: the entry above was wrong for every type but `text`. An error's message travels beyond the terminal: `fleet publish` puts it in a row on a shared target, and the other types' refusals quote the value they refuse (`%q is not a port number`). With the reason in the message, `TestARowCarriesNoParameterValueOrSecret` found a parameter value in a published row. The type's description in the hint, and nothing quoted in the message, is what kept the value at home. A `text` refusal names a size or a byte class and never the value, so only it puts its reason in the message, as §5.1 writes.
evidence: test/suite/fleet_test.go:136-161
action: decided
proposal: ASSUMED — supersedes the proposal above. A refused `text` value names its reason in the message, `parameter "<name>": <reason>`, with the refusal's own hint. The other types keep `parameter "<name>"` with the type's description as the hint, because their refusals quote the value, and a message reaches fleet rows.
```
