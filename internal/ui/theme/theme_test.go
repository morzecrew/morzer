package theme

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEveryStateIsDistinguishableWithoutColour(t *testing.T) {
	// The property that makes NO_COLOR a supported target rather than a
	// degraded one: if two states share a symbol, a monochrome terminal
	// cannot tell "done" from "failed".
	for _, set := range []Symbols{UnicodeSymbols, ASCIISymbols} {
		seen := map[string]string{}
		for name, symbol := range map[string]string{
			"OK": set.OK, "Fail": set.Fail, "Active": set.Active,
			"Pending": set.Pending, "Skipped": set.Skipped,
			"Compensated": set.Compensated,
		} {
			assert.NotEmpty(t, symbol, "%s has no symbol", name)
			if other, clash := seen[symbol]; clash {
				t.Errorf("%s and %s share the symbol %q", name, other, symbol)
			}
			seen[symbol] = name
		}
	}
}

func TestStylesAreTheIdentityWithoutColour(t *testing.T) {
	plain := New(false, true)
	require.False(t, plain.Colour)

	// Views always call the style and let it decide, rather than branching
	// on colour themselves. That only works if the uncoloured style is
	// exactly the identity -- otherwise the monochrome path grows escapes
	// nobody notices until a log file is unreadable.
	for name, render := range map[string]func(string) string{
		"OK": plain.OK, "Fail": plain.Fail, "Warn": plain.Warn,
		"Active": plain.Active, "Dim": plain.Dim, "Bold": plain.Bold,
		"Detail": plain.Detail, "Added": plain.Added,
		"Removed": plain.Removed, "Highlight": plain.Highlight,
	} {
		assert.Equal(t, "text", render("text"), "%s added markup with colour off", name)
	}
}

func TestUnicodeIsDecidedConservatively(t *testing.T) {
	env := func(pairs map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := pairs[k]; return v, ok }
	}

	cases := []struct {
		name string
		vars map[string]string
		want bool
	}{
		{"a UTF-8 locale", map[string]string{"TERM": "xterm-256color", "LANG": "en_GB.UTF-8"}, true},
		{"spelled utf8", map[string]string{"TERM": "xterm", "LC_ALL": "C.utf8"}, true},
		{"LC_ALL wins over LANG", map[string]string{
			"TERM": "xterm", "LC_ALL": "C", "LANG": "en_GB.UTF-8"}, false},
		{"the C locale", map[string]string{"TERM": "xterm", "LANG": "C"}, false},
		{"no locale at all", map[string]string{"TERM": "xterm"}, false},

		// The Linux virtual console renders a fixed 512-glyph font with
		// no braille and no check mark, whatever the locale claims.
		{"the linux console", map[string]string{"TERM": "linux", "LANG": "en_GB.UTF-8"}, false},
		{"a dumb terminal", map[string]string{"TERM": "dumb", "LANG": "en_GB.UTF-8"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, Unicode(env(tc.vars)))
		})
	}

	assert.False(t, Unicode(nil), "no way to look up means no assumption")
}

func TestSymbolSetsShareAWidth(t *testing.T) {
	// A step changing state must not shift the line it is on, and the two
	// sets are chosen so a column stays aligned in either.
	for _, set := range []Symbols{UnicodeSymbols, ASCIISymbols} {
		for _, s := range []string{set.OK, set.Fail, set.Active, set.Pending} {
			assert.Equal(t, 1, len([]rune(s)), "%q is not one cell", s)
		}
	}
	for _, frames := range [][]string{UnicodeSpinner, ASCIISpinner} {
		for _, f := range frames {
			assert.Equal(t, 1, len([]rune(f)), "spinner frame %q is not one cell", f)
		}
	}
}
