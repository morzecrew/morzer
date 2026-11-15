// Package filevault reads whether FileVault encrypts a Mac's disk, which is
// what protects decrypted secrets and the age identity there (RFC 0055
// decision 5).
package filevault

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/infra/exec"
)

// Binary is fdesetup by its path, so PATH does not choose what root runs.
const Binary = "/usr/bin/fdesetup"

// On reports whether FileVault is on, from `fdesetup status`.
func On(ctx context.Context, runner exec.Runner) (bool, error) {
	res, err := runner.Run(ctx, exec.Command{
		Argv:          []string{Binary, "status"},
		Timeout:       10 * time.Second,
		CaptureOutput: true,
	})
	if err != nil {
		return false, err
	}
	return parse(res.Stdout)
}

// parse reads the first line, which names the state. A volume being encrypted
// reads "FileVault is On." with a progress line after it, and one being
// decrypted "FileVault is Off."; anything else is not an answer.
func parse(out string) (bool, error) {
	first, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	switch {
	case strings.HasPrefix(first, "FileVault is On"):
		return true, nil
	case strings.HasPrefix(first, "FileVault is Off"):
		return false, nil
	}
	return false, fmt.Errorf("fdesetup status said %q", first)
}
