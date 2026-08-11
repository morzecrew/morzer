// Package ui resolves the output mode and holds the presenters.
//
// The governing rule: the UI is a subscriber, never a participant. Presenters
// subscribe to the event bus and render; nothing they do can influence control
// flow. A presenter that panics is logged and dropped, and the operation
// carries on.
package ui

import (
	"io"
	"os"
	"strings"
)

// Mode is the resolved output style. It is decided once at startup and never
// re-evaluated: a terminal that gains or loses its TTY mid-operation must not
// change how the rest of the run is rendered.
type Mode string

const (
	// ModeRich is the live step list with spinners and progress. Requires
	// a TTY on both streams. It carries no information plain does not:
	// the difference is motion, and anything visible only here is a bug in
	// plain rather than a feature of rich.
	ModeRich Mode = "rich"

	// ModePlain is one line per event: no ANSI, no cursor movement,
	// stable for logs and CI.
	ModePlain Mode = "plain"

	// ModeJSON emits exactly one JSON object on stdout at the end, with
	// events optionally streamed to stderr as JSONL.
	ModeJSON Mode = "json"
)

// ModeOptions are the inputs to mode resolution.
//
// Stdout and Stderr are `any` because that is what the caller has: a command
// runs against injected ui.Streams, and an embedder's buffer is not an
// *os.File. Asking the process's own descriptors instead would resolve rich
// mode from a terminal nothing is drawing on -- and then draw into the buffer
// while putting the real terminal in raw mode.
type ModeOptions struct {
	JSON      bool
	Plain     bool
	NoColor   bool
	Quiet     bool
	Stdout    any
	Stderr    any
	LookupEnv func(string) (string, bool)
}

// ResolveMode picks the output mode.
//
// Precedence, and the reasoning behind it:
//
//  1. --json wins outright. It is a machine contract, and a contract that
//     changed shape depending on whether a terminal was attached would not be
//     one.
//  2. --plain, NO_COLOR, CI, and TERM=dumb all force plain. These are the
//     signals an environment uses to say "do not draw"; honouring them without
//     requiring a flag is what makes the tool work in a pipeline unattended.
//  3. A TTY on both streams gets rich.
//  4. Everything else is plain.
func ResolveMode(opts ModeOptions) Mode {
	if opts.JSON {
		return ModeJSON
	}
	if opts.Plain {
		return ModePlain
	}

	lookup := opts.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}

	// NO_COLOR and CLICOLOR=0 are colour signals, and only colour: the
	// convention is "prevent the addition of ANSI colour", not "do not
	// draw". UseColor honours both, every state in the live view carries a
	// symbol as well as a colour, and --no-color -- the flag that means the
	// same thing -- has always left the renderer alone. Forcing plain here
	// made the two disagree, and made `status --watch` refuse with "needs a
	// terminal" at a terminal, for an operator whose only crime was
	// exporting NO_COLOR in their shell profile.
	//
	// What still forces plain is anything saying there is nothing to draw
	// on: TERM, CI, systemd, or a stream that is not a terminal.
	if term, _ := lookup("TERM"); term == "dumb" || term == "" {
		return ModePlain
	}
	// CI systems capture output to a log. Cursor movement in a log file is
	// noise, and every CI provider sets this.
	if v, set := lookup("CI"); set && v != "" && v != "false" && v != "0" {
		return ModePlain
	}
	// systemd gives a service no TTY, which the isTTY check below catches;
	// this is the explicit belt-and-braces for the same case.
	if _, set := lookup("INVOCATION_ID"); set {
		return ModePlain
	}

	stdout, stderr := opts.Stdout, opts.Stderr
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	if !IsTerminal(stdout) || !IsTerminal(stderr) {
		return ModePlain
	}

	return ModeRich
}

// isTTY reports whether f is a character device.
//
// Implemented with a stat rather than a dependency: the mode bits answer the
// question, and this is the only place the manager needs to ask it.
func isTTY(f *os.File) bool {
	if f == nil {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// UseColor reports whether styled output is appropriate.
func UseColor(mode Mode, noColor bool, lookup func(string) (string, bool)) bool {
	if mode != ModeRich || noColor {
		return false
	}
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if _, set := lookup("NO_COLOR"); set {
		return false
	}
	if v, set := lookup("CLICOLOR"); set && v == "0" {
		return false
	}
	return true
}

// Streams bundles the writers a presenter uses.
//
// The split is the one invariant every presenter must respect: stdout carries
// the result, stderr carries diagnostics and progress. That is what makes
// `morzer status --json | jq` work while a progress display is on screen.
type Streams struct {
	// Out is the result. In JSON mode it receives exactly one object.
	Out io.Writer

	// Err is diagnostics and progress rendering. Always.
	Err io.Writer

	// In is where the interactive commands read from: the `init` wizard's
	// form, and a secret piped to `secret set`.
	//
	// It is here rather than being `os.Stdin` at the point of use for the
	// same reason Out and Err are: a command that reaches past its own
	// streams is a command nothing can drive except a person at a keyboard.
	In io.Reader
}

// DefaultStreams returns the process's own streams.
func DefaultStreams() Streams {
	return Streams{Out: os.Stdout, Err: os.Stderr, In: os.Stdin}
}

// IsTerminal reports whether a stream is a terminal.
//
// Anything that is not an *os.File cannot be one, which is the answer a piped
// run and a test both need: no raw mode, no echo suppression, and no form that
// depends on either.
func IsTerminal(s any) bool {
	f, ok := s.(*os.File)
	return ok && isTTY(f)
}

// TerminalWidth returns a usable width for wrapping.
//
// COLUMNS first, because an operator who exports it means it. Then the terminal
// itself: stderr before stdout, since that is where the styled views are drawn
// and stdout may well be a pipe. The fallback is only reached when neither
// stream is a terminal, and in that case nothing is drawing a table anyway.
//
// It measures rather than assuming, which is not merely tidier: a table sized
// to a guessed 100 columns on an 80-column terminal wraps every row twice, and
// that is what the first real-terminal run of the doctor report looked like.
func TerminalWidth() int { return CurrentScreen().Width }

// fallbackWidth is what a view assumes when nothing reported a width.
//
// Only the measure uses it. Nothing may *degrade* against it -- see Screen.Known
// -- because a guessed width that drops a table column loses information in a
// pipe, which is the one place where nothing was ever going to be truncated.
const fallbackWidth = 100

func atoiSafe(s string) int {
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
		if n > 10000 {
			return 0
		}
	}
	return n
}
