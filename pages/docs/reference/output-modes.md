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
| `TERM` is `dumb` or unset | Plain |
| `CI` set to anything but `false` or `0` | Plain |
| `INVOCATION_ID` set — a systemd unit | Plain |
| stdout or stderr is not a terminal | Plain |
| otherwise | Rich |

`NO_COLOR`, `CLICOLOR=0` and `--no-color` are not in that table on purpose.
They turn colour off; they do not turn the renderer off. That is what the
convention asks for — "prevent the addition of ANSI colour" — it is what
`--no-color` has always done, and it keeps `status --watch` usable for anyone
who exports `NO_COLOR` in their shell profile. Nothing is lost by it: every
state in the live view carries a symbol as well as a colour.

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

## Every report goes through the mode

A command produces a value; the renderer decides how it looks. There is no
command that writes its own result to stdout, and that is enforced rather than
reviewed: a test fails the build on a direct print from the command layer.

What this guarantees, per report:

- **`--json` is the value itself**, unreshaped. No view touches it, so a change
  to how something looks is never a change to what a script reads.
- **Plain is line-oriented and stable in a log.** It is not "rich without
  colour": rich may draw a bordered callout where plain writes a labelled block,
  because a box drawn in a journal is noise that outlives the terminal.
- **Rich carries every field plain does.** Same rule as above, now per report
  rather than per event.

Two things on stdout are deliberately not reports, because they exist to be
substituted into a shell:

```sh
port=$(morzer config get http_port)
key=$(morzer secret recipients generate-recovery-key ./recovery.key)
```

Those print the value and a newline in every non-JSON mode. `morzer completion
<shell>` is the third: it is a script being emitted through a pipe, and an
envelope around it would produce something no shell can source.

## The measure

Text wraps at **100 columns however wide the terminal is**, and a wider terminal
gets whitespace rather than more space between things that belong together.
Extra width buys columns, never padding.

A table is the exception that proves it: one whose columns genuinely need more
than 100 uses them, packed left, ending where its content ends. What nothing
does is stretch to the screen — a check and the sentence explaining it at
opposite edges of a 380-column display is unreadable, and it is what the
diagnostic table used to do.

Narrow terminals **drop columns rather than wrapping cells**, in a declared
order, and say which they dropped. A wrapped cell destroys the alignment that is
the only reason to draw a table. Columns are dropped only when the terminal's
width is actually known: in a pipe, where nothing was going to be truncated,
every column is printed.

`COLUMNS` is honoured when set, which is also how the rendering tests pin every
width without a terminal.

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
