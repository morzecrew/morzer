---
title: Output modes
icon: lucide/monitor
summary: How rich, plain and JSON output are chosen, what each one is for, and why plain is the reference
---

# Output modes

There are three, decided once at startup and never re-evaluated. A terminal
that gains or loses its TTY mid-operation does not change how the rest of the
run is rendered.

| Mode | What it is | When you get it |
| --- | --- | --- |
| Rich | A live step list with a spinner, progress and a tail of subprocess output | A terminal on both stdout and stderr, and nothing below has forced plain |
| Plain | One line per event: no ANSI, no cursor movement | Everything else |
| JSON | Exactly one object on stdout | `--json` |

## How the mode is chosen

In order, first match wins:

| Condition | Mode |
| --- | --- |
| `--json` | JSON |
| `--plain` | Plain |
| `NO_COLOR` set to anything, including empty | Plain |
| `CLICOLOR=0` | Plain |
| `TERM` is `dumb` or unset | Plain |
| `CI` set to anything but `false` or `0` | Plain |
| `INVOCATION_ID` set — a systemd unit | Plain |
| stdout or stderr is not a terminal | Plain |
| otherwise | Rich |

`--json` wins outright. It is a machine contract, and a contract that changed
shape depending on whether a terminal was attached would not be one.

Everything else is a signal an environment already uses to say *do not draw*.
Honouring them without requiring a flag is what makes the tool work unattended
in a pipeline, which is why **`--plain` is almost never needed in a systemd unit
or a CI job** — those cases are already covered by `INVOCATION_ID` and `CI`.

`--no-color` disables styling without disabling the live view. `--quiet` reduces
output to errors in every mode.

## Plain is the reference

Rich output may never carry information plain output omits. The difference
between them is motion, not content: the live view names the steps that have
not run yet, and plain names each as it starts, but nothing is visible only on a
terminal.

This is enforced by a test rather than by review. For a fixed sequence of
events, every event that changes the live view must also make the plain
presenter print something, and every word in the final rich frame must appear
somewhere in the plain output. A field added to one renderer and not the other
fails the build.

The direction matters: plain is what systemd journals, what CI logs, and what
gets pasted into a bug report. A detail visible only to whoever happened to be
watching a terminal is a detail nobody can reproduce.

## Degradation

Styling degrades in two independent steps, so a terminal that supports one and
not the other still reads.

- **Colour** is reinforcement, never the only signal. Every state carries a
  symbol as well, so `NO_COLOR` and a monochrome terminal lose nothing.
- **Symbols** fall back to ASCII when the locale does not advertise UTF-8, or
  when `TERM` is `linux` or `dumb`. `✓ ✗ ▸ · » ↺` become `+ x > . - <`. Both
  sets are the same display width, so a step changing state does not shift the
  line.

Subprocess output in the live view is **truncated, never wrapped**. A wrapped
200-column `docker` line destroys the step list's alignment, and the full text
is in the log either way.

## When the display fails

A rendering fault is never an operation fault.

| What happens | What you get |
| --- | --- |
| The renderer panics | The event bus contains it, logs it, and drops it. The operation runs to completion with no display. |
| The live view cannot start | Plain output takes the whole run, and the reason is logged. |
| The terminal is lost mid-operation | Writes fail silently; the operation is unaffected. |
| The terminal is resized absurdly | The view clamps to a minimum width and keeps drawing. |

None of these change an exit code. If output looks wrong, `--plain` gives the
same information in a form nothing can garble.

## Cancellation

Ctrl-C is drawn, not acted on. The live view switches to *cancelling — waiting
for child processes* while the interrupt propagates to the child process groups;
the view exits when the operation reports that it finished, and the process
exits [130](exit-codes.md).

The renderer cannot cancel anything itself. It is a subscriber to the event
stream and has no path back into the engine, which is what makes "the UI
observes, never participates" a structural fact rather than a convention.

## What the live views are

- **Operations** — `apply`, `update`, `rollback`, `backup`, `restore`, `init`,
  `installation export` and `import` draw a step list. No alternate screen: the
  output stays in the scrollback, because an operator whose update just failed
  needs to scroll back through it.
- **`--dry-run`** — the plan with a coloured configuration diff. Printed rather
  than animated: a plan is computed and then shown, and there is nothing to
  watch.
- **[`doctor`](commands.md#doctor)** — the diagnostics grouped into a table,
  with the remedies collected underneath.
- **[`status --watch`](commands.md#status)** — the only alternate-screen view,
  because it replaces its own frame every few seconds and has no output worth
  keeping.
