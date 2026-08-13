package cli

import "golang.org/x/sys/unix"

// termios_linux.go's counterpart. See it for why the prompt keeps its own ioctl
// pair instead of `x/term.ReadPassword`.
const (
	getTermios = unix.TIOCGETA
	setTermios = unix.TIOCSETA
)
