package domain

import "strings"

// ShellQuote quotes a value for a POSIX shell only when it needs it, so the
// common case stays readable enough to copy. `!` counts, because an
// interactive bash expands it from history unless it is single-quoted.
func ShellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\"'\\$`*?[]{}();&|<>#~!") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
