package cli

import "golang.org/x/sys/unix"

// The ioctl requests that read and write a terminal's mode.
//
// Two constants in two files rather than `x/term.ReadPassword`, and that is a
// **departure from RFC 0029 decision 11**, which is graded ASSUMED and says
// execution may depart if `ReadPassword`'s behaviour differs from what the
// current prompt promises. It does, in the one way that matters:
// `ReadPassword` performs its echo flip *inside* the goroutine that reads,
// while `readPassword` performs it before the reader starts and restores it
// where the reader can no longer contradict it. That ordering is the race this
// code was written to close, and `prompt_test.go` synchronises on the flip
// being observable to pin it. Adopting `ReadPassword` for portability would
// have reintroduced a defect somebody had already fixed, in the one prompt that
// handles a secret.
//
// What is left is the smallest possible platform difference: darwin spells
// these requests `TIOCGETA`/`TIOCSETA` and has no `TCGETS` at all. Everything
// else the prompt touches -- `ECHO`, `ICANON`, `ISIG`, `ICRNL`,
// `IoctlGetTermios`, `IoctlSetTermios` -- is identical on both.
const (
	getTermios = unix.TCGETS
	setTermios = unix.TCSETS
)
