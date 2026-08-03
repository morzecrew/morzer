# RFC 0002 — Rich terminal renderer

- **Status:** 📝 Draft
- **Scope:** Implements the live step-list renderer behind the `ModeRich`
  output mode, which is currently resolved correctly and then silently falls
  back to plain. Adds `internal/ui/tty` (a Bubble Tea program), `internal/ui/theme`
  (lipgloss styles and symbols), and the `status --watch` full-screen view. No
  changes to the engine, the ports, the event schema, or the JSON output
  contract — the renderer is a bus subscriber and nothing else subscribes
  differently because of it. Explicitly **not** in scope: the `huh` init wizard
  (RFC [0003](0003-secrets-recovery-and-onboarding.md)), and any change to what
  plain mode prints.
- **Related:** [`internal/ui/mode.go`](../internal/ui/mode.go),
  [`internal/ui/plain/plain.go`](../internal/ui/plain/plain.go),
  [`internal/events/bus.go`](../internal/events/bus.go),
  [`internal/cli/root.go`](../internal/cli/root.go)

---

## 1. Summary

A Bubble Tea program renders operations as a live step list: completed steps
collapse to one line with a duration, the active step expands with a spinner,
progress bar and a tail of subprocess output, pending steps are dimmed. It
subscribes to the same event bus the plain presenter uses, draws to stderr, and
can be killed at any moment without affecting the operation underneath.

## 2. Motivation

`ui.ResolveMode` already returns `ModeRich` on an interactive terminal —
[`mode.go:105`](../internal/ui/mode.go) — and `internal/cli/root.go:313` then
ignores it:

```go
default:
    // ModeRich falls back to plain until the Bubble Tea renderer
    // lands. The information is identical; only the motion is missing.
```

So the mode resolution, the event vocabulary and the subscription plumbing are
all built and exercised; the renderer they were built for is the missing piece.
The events carry fields — `Progress`, `Detail`, `StepIndex`, `StepCount` — that
only a live view consumes. In plain mode `StepProgress` and `StepOutput` are
dropped entirely unless `--verbose`, because a line-per-event log of a
twelve-minute image pull is unreadable. That information exists and currently
reaches nobody.

## 3. Current state

- **Mode resolution is complete and honours everything it should**: `--json`
  wins outright, then `--plain`, `NO_COLOR`, `CLICOLOR=0`, `TERM=dumb`, `CI`,
  `INVOCATION_ID` (systemd), then a TTY check on both streams. Resolved once at
  startup and never re-evaluated.
- **The bus already contains the renderer's safety property.** `events.NewBus`
  returns a bus that recovers from sink panics and reports them through
  `OnPanic`; `internal/cli/root.go` logs the panic and drops the sink. There is
  a test — `TestPanickingSinkDoesNotStopTheOperation` — asserting an operation
  completes when a subscriber panics. That is precisely the "a failed presenter
  is logged and dropped" guarantee this renderer needs, and it is already
  covered.
- **`Sink.Handle` returns nothing.** A subscriber structurally cannot signal
  back into the engine, so "the UI is a subscriber, never a participant" is
  enforced by the interface rather than by convention.
- **`internal/ui/theme` does not exist.** `UseColor` in `mode.go` is written and
  has no caller — plain mode never colours anything.
- **Dependencies are absent**: no bubbletea, bubbles, lipgloss or teatest in
  `go.mod`. This RFC adds four direct dependencies, the largest single addition
  the project has made.

## 4. Goals / Non-goals

**Goals**

- A live step list for `apply`, `update`, `backup` and `restore`.
- Cancellation as a visible state, not an abrupt exit.
- Identical information to plain mode — rich may add motion, never facts.
- A renderer that can panic, be resized into nonsense, or be killed without the
  operation noticing.
- `status --watch` as a full-screen periodically-refreshing view.

**Non-goals**

- **Changing plain output.** Plain is the reference and the systemd/CI path. If
  rich shows something plain does not, plain is what gets fixed.
- **The alt-screen for operations.** Operation output stays in scrollback; an
  operator needs to scroll back through a failed update. Full-screen is reserved
  for genuinely interactive views (`status --watch`, a log viewer).
- **Interactive prompting from within an operation.** Every confirmation is a
  flag; an operation that stops to ask is an operation that cannot run under
  systemd.
- **Colour as the only signal.** Every state is distinguishable without colour,
  because `NO_COLOR` and monochrome terminals are supported targets.

## 5. Design

### 5.1 Program shape

The operation runs in a goroutine; the Bubble Tea program owns the terminal and
receives events through `p.Send`. The engine never imports `internal/ui`.

```go
// internal/ui/tty
type Model struct {
    opID, description string
    steps             []stepView   // built from OperationStarted.StepCount
    active            int
    output            ring          // last N lines of the active step
    spinner           spinner.Model
    progress          progress.Model
    cancelling        bool
    width, height     int
}

// Adapter: bus events become tea.Msg. This is the only coupling point, and it
// is one-directional.
func Subscribe(p *tea.Program) events.Sink {
    return events.SinkFunc(func(e events.Event) { p.Send(eventMsg{e}) })
}
```

`p.Send` is safe after the program has exited — it is a no-op — so a late event
from a cancelled operation cannot panic the sink.

### 5.2 Layout

```text
  morzer update 1.2.0 → 1.3.0

  ✓ verify bundle signature            0.1s
  ✓ validate manifest                  0.0s
  ✓ pre-update backup                 41.2s
  ⠹ pull images                       ▓▓▓▓▓▓░░░░  62%   backend@sha256:9f2c…
  · run migrations
  · start services
  · health checks

  ⏳ docker: pulling layer 7/11

  op_01J8Z9K2QW  ·  1m12s  ·  ctrl-c to cancel
```

- Completed steps collapse to one line with a duration; the active step expands
  with progress and the tail of subprocess output; pending steps are dimmed.
- Subprocess output is **truncated, never wrapped** — a wrapped 200-column
  docker line destroys the step list's alignment. The full output goes to the
  log.
- Symbols carry the state, colour reinforces it: `✓ ✗ ⠹ · ↺` degrade to
  `[ok] [!!] [..] [ ] [<]` when the terminal cannot render them.

### 5.3 Cancellation

Cancellation is a first-class state, not an exception. On `ctrl-c` the model
switches to a cancelling view — "waiting for child processes" — while the root
context cancellation propagates through the exec runner to the child process
groups. The program exits only when the operation goroutine reports finished,
and the process exits 130.

The renderer does **not** cancel anything itself. It observes; `main`'s
`signal.NotifyContext` owns cancellation. A renderer that could abort an
operation would be a participant.

### 5.4 Degradation and containment

| Condition | Behaviour |
| --- | --- |
| Renderer panics | Bus contains it, `OnPanic` logs it, sink is dropped, operation continues to completion with no display. |
| Terminal resized absurdly | Model clamps to a minimum width and keeps drawing. |
| Bubble Tea fails to start | Fall back to the plain presenter for the whole run, log the reason. |
| Terminal lost mid-operation | Writes to stderr fail silently; the operation is unaffected. |

The fallback in row three is why `plain.Presenter` stays constructed even in
rich mode — swapping to it is assigning a different sink, not rebuilding the
run.

### 5.5 Other views

- **Doctor** — a `lipgloss/table` grouped by category with `✓ ! ✗`, and the
  remedies section that plain mode already prints. Grouping logic is shared
  with `plain.RenderDoctor`, not reimplemented.
- **Plan (`--dry-run`)** — the step list with intended actions and a coloured
  diff for configuration changes. The diff already exists
  ([`ops/diff.go`](../internal/lifecycle/ops/diff.go)); this colours it.
- **`status --watch`** — the only alt-screen view. Periodic refresh, `q` to quit.
- **Release notes** — `glamour` rendering when a bundle ships `RELEASE.md`.
  Demand-gated: no bundle ships one yet, so this is named, not built.

## 6. Tests

- **`teatest`** golden tests over the model: a full successful operation, a
  failure with compensation, a cancellation. Deterministic because the model is
  driven by injected events rather than a real operation.
- **Model unit tests** for the parts that are pure: truncation at various
  widths, the output ring buffer, duration formatting, symbol degradation.
- **A parity test** asserting that for a fixed event sequence, every step ID and
  every error message appearing in plain output also appears in the rich model's
  final state. This is the mechanical enforcement of "rich never shows less".
- **No golden test on the exact frame bytes** — spinner phase and timing make
  that flaky, and the resulting churn trains people to regenerate goldens
  without reading them.

## 7. Docs

- README gains a short "Output modes" section with the resolution table.
- The `--plain` flag help states it is automatic under CI and systemd, so nobody
  adds it to a unit file believing it is required.
- No screenshots in the README: they go stale silently and cannot be diffed.

## 8. Out of scope

- **A log viewer.** `docker compose logs` already does this; wrapping it would
  be reimplementation rather than coordination.
- **Progress for steps that cannot report it.** Only image pulls and backups
  have meaningful fractions. Everything else gets a spinner, and `Progress: -1`
  already means "unknown" as distinct from zero.
- **Themes.** One theme, adaptive to light and dark via lipgloss. Configurable
  colour is a maintenance surface with no operator demand.

## 9. Risks

- **Four new dependencies for presentation only.** The largest dependency
  addition in the project, none of which serve correctness. Mitigation: they are
  confined to `internal/ui/tty` and `internal/ui/theme`; `depguard` can forbid
  them elsewhere, and plain mode must keep working with them removed.
- **Rich and plain drifting.** A field added to the rich view and not to plain
  breaks the "identical semantics" rule quietly. The parity test in §6 is the
  guard; it is the reason that test exists rather than being a nice-to-have.
- **A renderer bug reading as a manager bug.** An operator seeing a garbled step
  list will report a broken update. Mitigation: `--plain` in every error message
  that mentions display, and the panic path logs plainly.
- **Time spent here is time not spent on `update`.** RFC 0001 is the one that
  changes what the tool can do; this one changes how it looks doing it. Phasing
  (§11) puts this second deliberately.

## 10. Decisions

| # | Decision |
| --- | --- |
| 1 | The renderer subscribes to the existing bus and adds no event kinds. If a view needs data the events lack, the event gains a field — the renderer never queries the engine. |
| 2 | No alt-screen for operations. Output stays in scrollback because an operator needs to scroll back through a failed update. |
| 3 | Plain mode is the reference. Any information in rich but not plain is a bug in plain, enforced by a parity test rather than by review. |
| 4 | The renderer never cancels. It observes cancellation; `signal.NotifyContext` in main owns it. A renderer that could abort an operation would be a participant. |
| 5 | Subprocess output is truncated, never wrapped. A wrapped docker line destroys the step list's alignment, and the full text is in the log. |
| 6 | Symbols carry state, colour reinforces it. `NO_COLOR` and monochrome terminals are supported targets, not degraded ones. |
| 7 | Bubble Tea failing to start falls back to plain for the whole run rather than aborting. A display failure must never be an operation failure. |
| 8 | No exact-frame golden tests. Spinner phase and timing make them flaky, and flaky goldens train people to regenerate without reading. |

## 11. Phasing

- **P1** — `internal/ui/theme`: styles, symbols, degradation. No behaviour
  change; `plain` may adopt the symbols.
- **P2** — the operation view and its `teatest` coverage, wired into
  `ModeRich`. This is the bulk.
- **P3** — doctor table and plan diff colouring.
- **P4** — `status --watch`.
- **P5** — `glamour` release notes, gated on a bundle actually shipping a
  `RELEASE.md`.

Scheduled after RFC 0001: that RFC changes what the tool can do, this one
changes how it looks doing it, and the live view is more useful once there is a
long multi-step `update` to watch.
