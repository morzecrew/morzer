# RFC 0019 — The command surface

- **Status:** 📝 Draft
- **Scope:** The surface an operator meets before they meet any capability: how
  `morzer --help` is organised, how output is rendered in each mode, how the
  command reference is found, and how shell completion gets installed. Covers
  `internal/cli`'s output discipline (59 direct `fmt.Fprint` calls against 5
  renderer dispatches), cobra command groups, a generated command index in
  `pages/docs/reference/`, and `completion install`. Adds no capability: every
  command that exists after this RFC exists before it, and no port, schema or
  state file changes. Deliberately not a redesign of what the commands *do* —
  [0020](0020-several-installations-on-one-machine.md) and
  [0021](0021-into-the-running-deployment.md) add commands, and this RFC is what
  makes their additions land somewhere an operator can find.
- **Related:** [`internal/cli/root.go`](../internal/cli/root.go),
  [`internal/ui/mode.go`](../internal/ui/mode.go),
  [`internal/ui/tty/`](../internal/ui/tty),
  [`internal/ui/plain/`](../internal/ui/plain),
  [`tools/docscheck/`](../tools/docscheck),
  [`pages/docs/reference/commands.md`](../pages/docs/reference/commands.md),
  [0002](0002-rich-terminal-renderer.md) (the renderer this extends),
  [0008](0008-test-coverage-program.md) (docs-check's origin),
  [0018](0018-the-pre-1-0-manifest-surface.md) (the manifest's equivalent pass)

---

## 1. Summary

Three changes to the surface, no change to the capabilities behind it.

**Grouping.** The fifteen top-level commands are presented as one alphabetical
list, so `release` sits between `init` and `restore` and nothing tells a reader
which three commands they need on day one. Cobra groups them into five named
sections ordered by when an operator meets them.

**One rendering boundary, and a visual language behind it.** `internal/cli`
prints directly 59 times and asks the renderer 5 times, so `--plain` and rich
mode produce identical bytes for almost every command, and `internal/ui` — the
package that owns presentation — is bypassed by the majority of the program's
output. Every command's output moves behind a view in `internal/ui`, and those
views are built from five components with one capped measure, one colour role
set and one density rule — so `secret list`, `release list` and `doctor` stop
being three people's idea of a table.

**A findable reference.** `docs-check` proves every command is *mentioned* on
some page. It cannot prove a command is findable, and the four reference pages
that carry commands have no index between them. One generated command index page
lists every command with its one-line summary and links to the page that
explains it; `docs-check` gains the assertion that the index is complete and
current.

Plus one small thing an operator asks for and cannot have: `morzer completion
install` writes the completion script where their shell will read it, instead of
printing a script and a paragraph of instructions.

## 2. Motivation

### The output modes are a promise the program does not keep

`ui.ResolveMode` decides between rich, plain and JSON for every invocation,
reading `--json`, `--plain`, `--quiet`, `CI`, `TERM` and whether the streams are
terminals ([`internal/ui/mode.go`](../internal/ui/mode.go)). The decision is
careful, tested, and documented as a contract in
[`output-modes.md`](../pages/docs/reference/output-modes.md).

Then most commands ignore it:

```text
$ grep -rn "fmt.Fprint" internal/cli/*.go | wc -l
59
$ grep -rn "app.rich()" internal/cli/*.go | wc -l
5
```

The five are the operation view, the plan view, `status`, `doctor` and `config
list`. The
fifty-nine are everything else — `release list`, `secret list`, `config get`,
`installation export`, `backup list`, the init wizard's summary. For those, rich
mode and plain mode are the same bytes, which means the rich mode an operator
sees is a step list during `apply` and nothing else. It also means the
architecture's rule — presentation lives in `internal/ui` — is followed by 8% of
the program's output.

This is not an argument for decoration. It is an argument that a mode which
exists, is resolved, is documented and is tested should be the thing that decides
what output looks like, in one place, for every command. Today a contributor
adding a command has no boundary to cross: `fmt.Fprintf(app.Stream.Out, ...)`
compiles, and the mode is silently ignored.

### What that looks like on a real terminal

`just demo` on a wide screen, and three of the four things it prints are wrong in
a different way.

**`doctor` stretches to fill whatever it is given.** The table computes

```go
message := max(width/3, 24)
description := max(width-message-12, 20)
```

so the description column *is* the terminal. At 100 columns (the fallback) a
35-character check name is followed by 20 spaces and then its message — fine. At
200 columns it is 87 spaces. At 380 it is 207. The check and the sentence
explaining it end up at opposite edges of the screen, which is the "sparse"
complaint, and it is arithmetic rather than taste.

**`doctor` is also long.** Twenty-nine checks, every one on its own row whether
it passed or not, then a remedy block, then a summary. The information an
operator wants — 8 warnings, here they are, here is what to do — is spread over
about forty-five lines, twenty-one of which say a thing is fine.

**`secret list` is a `printf`.** Literally:

```go
fmt.Fprintf(app.Stream.Out, "%-28s %-14s %6s  %s\n",
    "NAME", "FINGERPRINT", "LENGTH", "LAST CHANGED")
```

in `internal/cli/secret.go`, in every mode, with the column widths hard-coded —
so a 30-character secret name silently breaks the alignment, and rich mode has
nothing to do with it. `release list` and `backup list` each have their own
version of the same idea.

**The recovery key is a bare print.** The one moment in the product where losing
what is on screen loses the machine gets `fmt.Fprintf` of two sentences to
stderr, with no more visual weight than a progress line.

**Meanwhile, one renderer in the same package already does the right thing.**
`RenderNotes` wraps at a fixed 80 columns and says why: *"Fixed rather than
measured from the terminal: these are printed after an operation from a command
that may be running under a timer, in a pipe or in a CI log, where 'the terminal'
is 80 columns by convention and nothing else."* That reasoning is correct and
applies to every other view; `doctor` is two files away from it, stretching to
380.


### Fifteen commands, one list, no order

```text
apply  backup  completion  config  doctor  help  init  installation
release  restore  rollback  secret  status  update  version
```

That is what `morzer --help` prints today. The three an operator needs first
(`init`, `apply`, `status`) are scattered through it; `completion` and `help`,
which cobra generates and this project does not document, sit in the middle of
it; and `installation` — export and import, the recovery path — reads like the
place you go to install something.

Cobra has supported command groups since 1.6 and this project pins 1.10.2, so
this is a presentation change with no mechanism to build.

### The docs prove mentions, not findability

`docs-check` walks the real cobra tree and fails the build when a command or a
non-hidden flag is not mentioned by any page
([`tools/docscheck/main.go`](../tools/docscheck/main.go), `checkCommands`). That
gate is why this repository has no undocumented commands. What it asserts is
`strings.Contains(prose, "morzer release prune")` — a command named in a sentence
in the middle of a page passes.

The consequence is the operator's complaint: commands are documented across
`commands.md`, `installation-commands.md`, `release-commands.md` and
`secret-commands.md`, with no page that lists all of them, and a reader who wants
to know what exists has to read four pages and hope. The gate that made the
coverage true is also what made it invisible: coverage was achieved by mentioning
things, and mentioning is not a table of contents.

### Completion exists and installing it is a paragraph of shell

`morzer completion bash|zsh|fish|powershell` works — cobra generates it and it is
one of the two commands `docs-check` explicitly excludes from coverage. Using it
means knowing where your shell reads completions from, which differs per shell,
per distribution, and per whether the user is root. Every tool that has solved
this has solved it the same way, and the operator asked for it by name.

## 3. Current state

Verified against the code, not from memory.

| Thing | State |
| --- | --- |
| Top-level commands | 15 registered in [`root.go:449`](../internal/cli/root.go), no `GroupID` anywhere in the tree |
| Cobra version | 1.10.2 — `AddGroup` available since 1.6 |
| Mode resolution | `ui.ResolveMode`, [`internal/ui/mode.go`](../internal/ui/mode.go); three modes — `ModeRich`, `ModePlain`, `ModeJSON`. `--quiet` is not a fourth: it resolves to plain and suppresses the summary at `App.finish` |
| Renderer dispatch in the CLI | 5 sites: two in `render.go` (the operation view and the plan), and one each in `status`, `doctor` and `config list` |
| Direct prints in the CLI | 59 sites across 11 files; `commands.go` 13, `secret.go` 11, `root.go` 8, `installation.go` 8 |
| Rich renderer | `internal/ui/tty`: step list with spinner, progress bar, output tail (`view.go`), plus `RenderStatus`, `RenderDoctor`, `RenderConfig`, `RenderPlan` |
| Plain renderer | `internal/ui/plain`: `RenderStatus`, `RenderDoctor`, `RenderConfig` |
| JSON | One envelope, `app.jsonData`, published by `App.finish` |
| Completion | cobra's generated `completion` command, excluded from `docs-check` coverage by name |
| Docs pages | 34, nav-complete both directions, 43 checks green |
| Command reference pages | 4 (`commands.md`, `installation-commands.md`, `release-commands.md`, `secret-commands.md`) |

Two facts worth stating because they change what this RFC has to build:

- **The mode contract already exists and is right.** Nothing here proposes to
  change how a mode is chosen, only that the chosen mode reaches the output.
- **`docs-check` already walks the cobra tree.** The generator this RFC needs —
  enumerate every command, its summary and its flags, from the real tree — is
  most of a program that already exists and runs on every CI job.

## 4. Goals / Non-goals

**Goals**

- A `--help` whose first screen tells an operator what to run first.
- One boundary for output: `internal/cli` decides *what* to say, `internal/ui`
  decides how it looks in each mode, and a contributor cannot skip it by
  accident.
- One page that lists every command, generated from the binary, with the
  assertion that it is current.
- `completion install` for the shells cobra generates.

**Non-goals**

- **New capabilities.** Nothing here adds a command that does something new.
  `logs`, `stats` and `ls` are [0021](0021-into-the-running-deployment.md) and
  [0020](0020-several-installations-on-one-machine.md); this RFC is the surface
  they land on.
- **An interactive TUI.** `morzer` is run by operators at a prompt and by systemd
  units on a timer; a full-screen application would be a second program to
  maintain for the first case and useless for the second. The step view is
  already a bubbletea program and stays the only one.
- **Colour-scheme configuration.** The theme is one file, respects `NO_COLOR` and
  `--no-color`, and a per-operator palette is a preference file this project does
  not have and does not want to grow.
- **Rewriting the four reference pages.** They are Diátaxis-correct reference for
  their domains. The index links to them; it does not replace them.

## 5. Design

### 5.1 Command groups

Five groups, ordered by when they are met rather than alphabetically:

```text
Getting started:
  init         Create a new installation
  apply        Converge the system to the installed release
  status       Show what is deployed and whether it is working

Operating:
  update       Install a new release over the current one
  rollback     Return to the previous release
  config       Read and change the release parameters, and installation settings
  secret       Manage the encrypted secret state
  doctor       Run read-only diagnostics

Data:
  backup       Back up the database, volumes, configuration and secret state
  restore      Restore from a backup

Bundles:
  release      Inspect and manage release bundles

Machine:
  installation Export and rebuild an installation's identity
  version      Print version, commit, and supported manifest API versions
```

`help` and `completion` fall into cobra's ungrouped section at the bottom, which
is where generated commands belong.

Groups are declared with `root.AddGroup(&cobra.Group{ID: "start", Title: "Getting started:"})`
and each command carries `GroupID`. A command added without one lands in the
ungrouped section — visible, unstyled, and obviously unfinished, which is the
failure mode to prefer over a silent alphabetical merge.

**Alternatives considered.** *Alphabetical with a "start here" paragraph in the
long help.* Rejected: the paragraph is already there and the complaint is that
the list underneath it does not reflect it. *Grouping by noun (release, backup,
secret) with verbs underneath* — the `git`/`docker` shape. Rejected because it
would move `apply`, `update` and `rollback` under nouns nobody thinks in: an
operator updates a machine, not a release object.

### 5.2 One rendering boundary

The rule: **a command produces a value; `internal/ui` renders it.** Concretely,
every `RunE` ends in one of two shapes —

```go
// An operation: the engine's events drive the view, the result is the summary.
return app.runOperation(cmd.Context(), func(ctx context.Context) (ops.Result, error) { ... })

// A report: a value, rendered per mode.
report, err := ops.ConfigList(cmd.Context(), app.Deps)
if err != nil {
    return err
}
return app.render(report)
```

`App.render(v any) error` replaces the `switch { case app.json != nil: ...; case
app.rich(): ...; default: ... }` block that five commands write out today and
fifty-nine skip. It dispatches on the value's type through a view registry in
`internal/ui`:

```go
// internal/ui: one view per report type, three renderings each.
type View[T any] struct {
    Rich  func(w io.Writer, t *theme.Theme, v T)
    Plain func(w io.Writer, v T)
    // JSON needs no function: the value is the contract, encoded by the
    // envelope. A view that wanted to reshape it for JSON would be
    // declaring a second contract for the same data.
}
```

Three consequences worth stating because they are the point:

- **A new command cannot forget the mode.** `app.render` is the only way to put a
  report on stdout, and a type with no registered view fails at startup (§6),
  not in front of an operator.
- **`--json` stays exactly what it is.** The value *is* the JSON contract, as
  today. This RFC does not get to reshape any machine-readable output; a view
  that reshaped it would be a breaking change wearing a refactor's clothes.
- **Plain is not "rich without colour".** It stays line-oriented and stable in a
  log, which is what a systemd unit and a CI job read. The distinction already
  exists in `internal/ui/plain`; this extends it rather than inventing it.

The boundary is the plumbing. What comes through it is §5.3.

### 5.3 The measure is capped

```go
// internal/ui
const MaxContentWidth = 100

// ContentWidth is what a view may draw inside.
func ContentWidth() int { return min(TerminalWidth(), MaxContentWidth) }
```

Every view lays out inside `ContentWidth()`. A 380-column terminal gets
whitespace to the right of the content, not 207 spaces inside a row.

This is not a new opinion; it is the one `RenderNotes` already holds, applied
consistently. Wide measure is a legibility problem, not an aesthetic one — the
eye loses the line on the return sweep, which is why typographic practice puts
the measure at 45–75 characters (Bringhurst) and why `glamour`, already a
dependency here, defaults to 80. [clig.dev](https://clig.dev/) says to check
`COLUMNS` "for output that's dependent on screen size (e.g. tables)"; checking it
and then *filling* it is the failure mode this fixes.

Three refinements, because a flat cap is not enough on its own:

- **Wide terminals buy columns, not padding.** Above ~160 columns a table may add
  a column it would otherwise drop (a `LAST CHANGED` beside `LENGTH`), or a
  listing may go two-up. The measure caps a *line*; it does not forbid using the
  space for content.
- **Narrow terminals degrade by dropping columns, in a declared order**, rather
  than wrapping mid-cell. Each table declares which columns are essential; below
  the width that fits them, the rest are dropped and a footer says so.
- **`COLUMNS` is honoured** as today, so the golden tests can pin every width
  without a pty.

### 5.4 Five components, and everything is one of them

The consistency the current output lacks is not a style guide, it is a
vocabulary. Five components in `internal/ui`, each with a rich and a plain
rendering, and no view draws anything else:

| Component | What it is | Used by |
| --- | --- | --- |
| `Heading` | A section title with its spacing rule | every multi-section view |
| `Fields` | Label/value block, labels padded to the longest, values wrapped inside the measure | `status`, `release show`, `version`, the init summary |
| `Table` | Column widths computed from the data, capped, truncated with `…`, essential columns declared | `secret list`, `release list`, `backup list`, `config list`, `installation ls` |
| `Checks` | Marker, description, message — with the message on a continuation line when it does not fit, never pushed to the far margin | `doctor`, `release verify` |
| `Callout` | A bordered block for something the operator must act on or keep | the recovery key, a refused candidate, `--force` warnings |

`Table` is the one that removes the most duplication: four hand-rolled `printf`
tables with hard-coded widths become one implementation whose widths come from
the data. The 28-character `%-28s` in `secret list` is a bug waiting for a long
secret name, and it exists in three other places with three other constants.

`Callout` is the one that adds something the product does not have. The recovery
key is the moment where losing the screen loses the machine, and today it has
less visual weight than a step line:

```text
  ╭─ keep this ────────────────────────────────────────────────╮
  │ The private key is at tmp/keys/recovery.key (0400).        │
  │                                                            │
  │ Move it off this machine. If this VM is lost, this key is  │
  │ how its secrets are recovered — and it is the only way.    │
  ╰────────────────────────────────────────────────────────────╯
```

In plain mode the same content is a prefixed block with no border, because a box
drawn in a journal is noise. In JSON it is the same fields it is today.

### 5.5 Density: `doctor` says what is wrong first

Twenty-nine checks, twenty-one of which passed, is a report that buries its own
finding. Default output collapses a passing group to its heading:

```text
  configuration   ✓ 5 checks
  storage         ✓ 3 checks
  secrets         ✓ 4 checks
  tools           ✓ 2 checks
  network         ! container registry is reachable
                    cannot reach registry.example/demo/app: …
  runtime         ! release images are available offline
                    2 of 2 pulled images are not local: …
                  ! all services are running
                    cannot read service status
  …
  21 ok, 8 warning, 0 failed
```

A group with nothing to report is one line. A group with something shows only
the checks that have something — failures and warnings always, in full, with
their messages beneath them rather than beside them. `--verbose` expands
everything, which is what a support engineer asking for output wants.

This follows clig.dev's rule directly: *"Display output on success, but keep it
brief … it's usually best to err on the side of less"*, and *"only in verbose
mode"* for the rest. It is also the difference between a report an operator
reads and one they scroll past.

The remedy block stays — it is the best thing `doctor` prints — and moves above
the summary line so the last thing on screen is the count.

### 5.6 Colour, symbols, and how far "fancy" goes

**Colour has roles, and views may not invent one.** The theme already defines
exactly the set that is needed — `OK`, `Fail`, `Warn`, `Active`, `Dim`, `Bold`,
`Detail`, `Added`, `Removed`, `Highlight` — and the rule this RFC adds is that a
view calls one of those or none. clig.dev: *"Don't overuse it — if everything is
a different color, then the color means nothing and only makes it harder to
read."*

**Colour is never the only carrier.** Every state that has a colour has a symbol
(`✓`, `!`, `✗`, `»`) and a word in the summary line. That is already true in
`checkMarker` and its comment says why; it becomes a rule for every component.

**Symbols stay in the box-drawing and common-symbol ranges.** No Nerd Font
glyphs, no powerline separators: they render as tofu on a stock machine, and the
machines this program runs on are stock by definition. Box drawing, arrows,
check marks and bullets are safe; anything that needs a patched font is not.

**Where fancy is welcome:** borders on callouts, a summary bar, aligned tables,
one accent colour per state, generous vertical rhythm (a blank line between
sections, never between rows), and the step view's existing spinner and progress
bar. **Where it is not:** anything that changes plain output, anything that
requires the cursor to move outside the step view (so the output survives being
piped into a pager, which `doctor`'s comment already claims), anything animated
in a report that exits, and gradients or 24-bit colour that degrade badly on a
16-colour terminal.

### 5.7 A generated command index

One page, `pages/docs/reference/index.md`, generated by extending the existing
docs tooling:

```markdown
| Command | What it does | Reference |
| --- | --- | --- |
| `morzer init` | Create a new installation | [commands](commands.md#init) |
| `morzer release prune` | Remove old releases beyond the retention policy | [release](release-commands.md#prune) |
```

Generated from the cobra tree — the same walk `docs-check` already performs — so
a command cannot be added without appearing here, and a `Short` that drifts from
the page it links to is a diff in a generated file rather than a discovery.

`docs-check` gains one check: the generated index matches what the tree produces
right now. This is the `just schemas` pattern the repository already uses for the
JSON Schemas — generate, commit, fail the build on drift — rather than a second
mechanism.

**Alternatives considered.** *Generate the whole reference from cobra help.*
Rejected: the reference pages carry the reasoning that makes a flag
understandable ("re-creates rather than restarts: a published port is fixed when
a container is created"), and no `Short` string will ever hold that. The index
generated, the explanations written, is the split that keeps both honest.

### 5.8 `completion install`

```text
morzer completion install [bash|zsh|fish]   # default: $SHELL
morzer completion install --print-path      # where it would write, and nothing else
```

Writes the generated script to the per-user location for the shell — the
XDG-correct one where the shell defines it, the conventional one where it does
not — creating the directory when it is missing, and prints the one line the
operator must add to their rc file when the shell requires it. Idempotent:
running it twice rewrites the same file.

Never writes outside the user's home without `--system`, which writes the
distribution's system-wide completion directory and needs the privileges to do
it. A completion installer that silently required root would be a command that
fails on every machine where the operator is not root, which is most of them
after `init`.

PowerShell is generated by cobra and not installed by this: the location is a
profile script this program has no business editing.

### 5.9 Help text conventions

Three rules, applied to every command in the pass, none of them new inventions —
they are what the best-written commands here already do:

1. **`Short` completes "this command will …"** and fits in the group listing
   without wrapping at 80 columns.
2. **`Long` opens with what it does, then what it refuses.** The refusals are the
   part an operator needs before running it, and they are already written into
   most commands.
3. **Examples where the invocation is not obvious**, using cobra's `Example`
   field so they render in one place rather than inside prose.

## 6. Tests

- **Every command is grouped.** Walk the tree; a non-generated command with an
  empty `GroupID` fails. This is the test that keeps §5.1 true in a year.
- **Every report type has a view.** A registry test enumerates the types passed
  to `app.render` and asserts a registered view for each — the compile-time half
  is the type parameter, this is the runtime half.
- **No direct printing to stdout from `internal/cli`.** A source-level test (or a
  `forbidigo` rule in `.golangci.yml`, which is already configured) fails on
  `fmt.Fprint*(app.Stream.Out, ...)` outside the view layer. Stderr narration is
  exempt: it is not the result.
- **Mode fidelity per view.** For each report type, render all three modes and
  assert: plain contains no escape codes, JSON round-trips to the same value,
  rich contains every field plain does. The third is the one that catches a rich
  view that quietly drops a column.
- **Golden renders at four widths — 60, 80, 100 and 400 columns.** `COLUMNS` is
  already honoured, so this needs no pty. Two assertions no golden file makes on
  its own: **no rendered line exceeds `ContentWidth()`**, and **no line contains a
  run of more than N spaces** between content. The second is the test that would
  have caught `doctor`'s 207-space gap, and it is the kind of defect a golden
  file happily records forever.
- **60 columns degrades by dropping declared-inessential columns**, never by
  wrapping a cell mid-word. Asserted per table.
- **`doctor` collapses.** A report with everything passing renders one line per
  group; adding a single warning expands exactly that group and nothing else;
  `--verbose` expands all. Verified against a report fixture rather than a live
  run, so the density rule is pinned independently of what the checks find.
- **No view calls a colour outside the theme's roles.** A source-level check on
  `internal/ui`, in the same family as the forbidigo rule above.
- **Every state that has a colour has a symbol.** Render with colour disabled and
  assert each status is still distinguishable — the accessibility half, and the
  one that quietly rots.
- **The generated index is current.** `docs-check` regenerates and diffs.
- **`completion install --print-path`** resolves per shell against a fake HOME,
  and the written file is what `completion <shell>` prints.

## 7. Docs

- `pages/docs/reference/index.md` — the generated index (new).
- `pages/docs/reference/output-modes.md` — gains the rule that every report is
  rendered per mode, and what plain guarantees.
- `pages/docs/get-started/installation.md` — a line about `completion install`.

## 8. Out of scope

- **A `--format=template` / `--format=table` flag.** Three modes are already
  three contracts to keep; a fourth that lets an operator write their own is a
  templating language in the CLI. `--json` plus `jq` is the answer and it is
  already documented. *What would change this:* evidence that operators are
  parsing plain output in scripts, which would mean plain is being used as a
  contract it does not promise to be.
- **Renaming commands.** `installation` reads like a verb; it is not one. Renames
  break every runbook, and this RFC's whole premise is that the surface is
  findable, not that it is renamed. *What would change this:* a pre-1.0 breaking
  pass, which is [0018](0018-the-pre-1-0-manifest-surface.md)'s territory for the
  manifest and would be its own RFC for the CLI.
- **Interactive prompts beyond `init`'s wizard.** The wizard exists because
  `init` has irreversible consequences and many inputs. Nothing else does.

## 9. Risks

- **A refactor that touches every command is a refactor that can break every
  command.** Mitigated by the mode-fidelity tests landing *before* the moves, and
  by `test/clitest` already asserting the output of most commands: the suite is
  the safety net, and a move that changes bytes fails it. Where bytes must
  change, they change in plain mode's favour — a stable line format — and the
  test is updated with the reasoning in the commit.
- **"Better UX" is unfalsifiable, so most of this is written as things you can
  check.** 100% of report output through the mode boundary; every command
  grouped; no rendered line wider than the measure; no gap wider than N spaces;
  `doctor` at one line per passing group; one index page. What deliberately is
  *not* falsifiable is whether the result looks good — that is a judgement, it is
  the point of §5.4 and §5.6, and the honest way to settle it is to render the
  five components and look at them before fifteen commands are built on top
  (P2a).
- **A component vocabulary can be a straitjacket.** Five components will not fit
  everything, and the failure mode is a view contorted into `Table` when it wanted
  something else. The rule is that a sixth component is a normal change to
  `internal/ui`, not a forbidden one; what is forbidden is a view drawing its own
  layout inline, which is how the current four incompatible tables happened.
- **Generated docs rot differently.** A generated page is only as good as the
  `Short` strings behind it. The mitigation is that the generator makes bad
  `Short` strings *visible* — they land in a table next to their siblings — where
  today they hide at the bottom of `--help`.

## 10. Unresolved questions

- **Does `app.render` dispatch on type or does each command name its view?** Type
  dispatch is invisible at the call site, which is either elegant or a trap
  depending on how many report types share a shape. Implementation may settle
  this; the constraint that must hold is that a report with no view cannot ship.
- **Where does `--quiet` sit?** Today it collapses into plain plus suppression at
  `App.finish`. If a view can be asked for "the one line", quiet becomes a mode
  rather than a filter — better, but it is a fourth rendering per type and may
  not earn it.

## 11. Decisions

| # | Decision | Why |
| --- | --- | --- |
| 1 | Five command groups, ordered by first encounter, not alphabetically | The first screen of `--help` is the only documentation many operators read; ordering it by what to run first is the cheapest possible improvement and needs no new mechanism (cobra 1.10.2 is pinned). |
| 2 | A command with no `GroupID` renders ungrouped rather than failing to compile | Cobra offers no compile-time hook, and a test that fails the build (§6) is the same guarantee without a fork. The ungrouped section is visibly wrong, which is the point. |
| 3 | Every report goes through `app.render`; `internal/cli` does not print reports | One boundary that a contributor cannot skip by accident. Today the mode is resolved for every invocation and honoured by 8% of output — the contract exists and is not kept. |
| 4 | `--json` output is the value itself, unreshaped by any view | The JSON envelope is a published contract. Letting a view reshape it would make presentation changes into breaking changes, which is exactly the coupling this boundary exists to remove. |
| 5 | Plain stays line-oriented and stable, not "rich minus colour" | It is what systemd journals and CI logs read. A plain mode that inherited rich's layout would break every log grep the day the layout improved. |
| 6 | The command index is generated from the cobra tree and committed | Same pattern as `just schemas`: drift is a build failure rather than a discovery. A hand-maintained index is the thing that was already tried, by four separate pages. |
| 7 | Generated index, hand-written explanations | A `Short` string cannot carry the reasoning that makes a flag understandable, and generating the whole reference would delete the reasoning this project's docs are built on. |
| 8 | `completion install` writes to the user's location by default; `--system` for the machine's | A completion installer that needed root would fail on the machine an operator uses daily. Both paths are explicit; neither is guessed. |
| 9 | No fourth output format, no per-operator theming, no full-screen TUI | Each is a contract or a configuration surface with an ongoing cost, and none is what "hard to see what is available" asked for. |
| 10 | No command renames in this RFC | Renames break runbooks and are a pre-1.0 breaking pass, not a findability pass. Stated so the two are not conflated during execution. |
| 11 | Content is capped at 100 columns; a wider terminal gets whitespace, not padding | `RenderNotes` already holds this position for the same reason and states it. A stretched table puts a check and its explanation at opposite edges of a 380-column screen — 207 spaces between them, by arithmetic. |
| 12 | Extra width buys columns or a second column of content, never wider cells | The cap is on the *measure*, not on the use of the screen. Otherwise a wide terminal is punished for being wide. |
| 13 | Narrow terminals drop declared-inessential columns rather than wrapping cells | A wrapped cell destroys the alignment that is the only reason to use a table. Each table declares its essential columns, so the degradation is a decision rather than whatever the width happens to do. |
| 14 | Five components — heading, fields, table, checks, callout — and no view draws its own layout | The current output is four hand-rolled `printf` tables with hard-coded widths, and the `%-28s` in `secret list` breaks on a 30-character name. A sixth component is a normal change; inline layout is not. |
| 15 | `doctor` collapses passing groups by default; `--verbose` expands | Twenty-nine rows, twenty-one of them "fine", buries the eight that are not. clig.dev: keep success output brief, put the rest behind verbose. |
| 16 | Colour has roles and views may not invent one, and no state is carried by colour alone | The theme already defines the roles. Symbol plus colour plus a word in the summary is what makes a monochrome terminal, a colour-blind reader and a piped log all get the same information. |
| 17 | Box-drawing and common symbols only — no Nerd Font or powerline glyphs | They render as tofu on a stock machine, and stock Linux hosts are exactly what this program runs on. |

## 12. Phasing

- **P1 — Groups and help conventions.** Cobra groups, the grouped-command test,
  a `Short`/`Long` pass. Self-contained, no output changes, one small PR.
- **P2 — The rendering boundary.** `app.render`, the view registry, the
  mode-fidelity tests, and the forbidigo rule. The commands move behind it in
  batches; the rule lands last, when the last direct print is gone.
- **P2a — The components, before the commands move.** `ContentWidth`, the five
  components, the golden-width tests and the no-wide-gap assertion, with `status`
  and `doctor` as the first two consumers. This is the phase to look at output and
  argue about it, while two views use the components rather than fifteen.
- **P3 — The rest of the views, and the density rule.** Every remaining report
  behind a component; `doctor`'s collapse and `--verbose`; the recovery key as a
  callout; the four hand-rolled tables deleted. Gated on P2a.
- **P4 — The generated index and `docs-check`'s drift assertion.** Independent of
  P2 and P3; gated on P1 only because the groups are what the index's sections
  mirror.
- **P5 — `completion install`.** Independent of everything above. Small enough to
  ride along with any of them.
