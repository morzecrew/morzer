package ui_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/morzecrew/morzer/internal/ui"
)

// The mode resolution table is documented in reference/output-modes.md and read
// by anyone deciding whether to put `--plain` in a unit file. These pin every
// row of it.

// env builds a lookup over a fixed map, so a test does not depend on whatever
// the machine running it happens to export.
func env(pairs map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := pairs[k]
		return v, ok
	}
}

// tty is a character device, which is what ResolveMode checks for.
func tty(t *testing.T) *os.File {
	t.Helper()
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("cannot open %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// notATTY is a regular file, which is what a pipe or a redirect looks like.
func notATTY(t *testing.T) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestResolveModeFollowsItsDocumentedTable(t *testing.T) {
	interactive := map[string]string{"TERM": "xterm-256color"}

	cases := []struct {
		name string
		opts ui.ModeOptions
		want ui.Mode
	}{
		{
			"a terminal on both streams",
			ui.ModeOptions{Stdout: tty(t), Stderr: tty(t), LookupEnv: env(interactive)},
			ui.ModeRich,
		},
		{
			"--json wins outright, even at a terminal",
			ui.ModeOptions{JSON: true, Stdout: tty(t), Stderr: tty(t), LookupEnv: env(interactive)},
			ui.ModeJSON,
		},
		{
			"--json wins over --plain, because it is a machine contract",
			ui.ModeOptions{JSON: true, Plain: true, Stdout: tty(t), Stderr: tty(t),
				LookupEnv: env(interactive)},
			ui.ModeJSON,
		},
		{
			"--plain",
			ui.ModeOptions{Plain: true, Stdout: tty(t), Stderr: tty(t), LookupEnv: env(interactive)},
			ui.ModePlain,
		},
		{
			// A colour signal, and only colour. It used to force
			// plain, which made `status --watch` refuse with "needs
			// a terminal" at a terminal, and disagreed with
			// --no-color -- the flag that means the same thing and
			// has always left the renderer alone. What the view
			// loses is styling; every state carries a symbol too.
			"NO_COLOR set to anything, including empty",
			ui.ModeOptions{Stdout: tty(t), Stderr: tty(t),
				LookupEnv: env(map[string]string{"TERM": "xterm", "NO_COLOR": ""})},
			ui.ModeRich,
		},
		{
			"CLICOLOR=0",
			ui.ModeOptions{Stdout: tty(t), Stderr: tty(t),
				LookupEnv: env(map[string]string{"TERM": "xterm", "CLICOLOR": "0"})},
			ui.ModeRich,
		},
		{
			"TERM=dumb",
			ui.ModeOptions{Stdout: tty(t), Stderr: tty(t),
				LookupEnv: env(map[string]string{"TERM": "dumb"})},
			ui.ModePlain,
		},
		{
			"TERM unset",
			ui.ModeOptions{Stdout: tty(t), Stderr: tty(t), LookupEnv: env(nil)},
			ui.ModePlain,
		},
		{
			"CI, which every provider sets",
			ui.ModeOptions{Stdout: tty(t), Stderr: tty(t),
				LookupEnv: env(map[string]string{"TERM": "xterm", "CI": "true"})},
			ui.ModePlain,
		},
		{
			"CI=false is not CI",
			ui.ModeOptions{Stdout: tty(t), Stderr: tty(t),
				LookupEnv: env(map[string]string{"TERM": "xterm", "CI": "false"})},
			ui.ModeRich,
		},
		{
			"INVOCATION_ID, which systemd sets",
			ui.ModeOptions{Stdout: tty(t), Stderr: tty(t),
				LookupEnv: env(map[string]string{"TERM": "xterm", "INVOCATION_ID": "abc"})},
			ui.ModePlain,
		},
		{
			"stdout redirected to a file",
			ui.ModeOptions{Stdout: notATTY(t), Stderr: tty(t), LookupEnv: env(interactive)},
			ui.ModePlain,
		},
		{
			"stderr redirected, which is where the live view draws",
			ui.ModeOptions{Stdout: tty(t), Stderr: notATTY(t), LookupEnv: env(interactive)},
			ui.ModePlain,
		},
		{
			// The mode is resolved from the streams the command was
			// given, not from the descriptors this process happens
			// to own: an embedder handed buffers would otherwise get
			// the live renderer drawing into a buffer while the real
			// terminal was put in raw mode.
			"an embedder's buffers, at a terminal",
			ui.ModeOptions{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
				LookupEnv: env(interactive)},
			ui.ModePlain,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ui.ResolveMode(tc.opts); got != tc.want {
				t.Errorf("ResolveMode = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUseColourFollowsTheModeAndTheEnvironment(t *testing.T) {
	cases := []struct {
		name    string
		mode    ui.Mode
		noColor bool
		env     map[string]string
		want    bool
	}{
		{"rich, nothing objecting", ui.ModeRich, false, nil, true},
		{"rich but --no-color", ui.ModeRich, true, nil, false},
		{"rich but NO_COLOR", ui.ModeRich, false, map[string]string{"NO_COLOR": ""}, false},
		{"rich but CLICOLOR=0", ui.ModeRich, false, map[string]string{"CLICOLOR": "0"}, false},
		{"plain never colours", ui.ModePlain, false, nil, false},
		{"json never colours", ui.ModeJSON, false, nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ui.UseColor(tc.mode, tc.noColor, env(tc.env)); got != tc.want {
				t.Errorf("UseColor = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTerminalWidthPrefersAnExplicitColumns(t *testing.T) {
	t.Setenv("COLUMNS", "132")
	if got := ui.TerminalWidth(); got != 132 {
		t.Errorf("TerminalWidth = %d, want the exported COLUMNS", got)
	}

	// Nonsense is ignored rather than propagated: a width of 3 would render
	// every table as one character per line.
	for _, bad := range []string{"", "wide", "-5", "3", "999999999999"} {
		t.Setenv("COLUMNS", bad)
		if got := ui.TerminalWidth(); got <= 20 {
			t.Errorf("COLUMNS=%q gave width %d, which is unusable", bad, got)
		}
	}
}

func TestDefaultStreamsAreTheProcessStreams(t *testing.T) {
	s := ui.DefaultStreams()
	if s.Out != os.Stdout || s.Err != os.Stderr {
		t.Error("DefaultStreams must be the process's own, or the split that " +
			"makes `status --json | jq` work is not the one being used")
	}
}
