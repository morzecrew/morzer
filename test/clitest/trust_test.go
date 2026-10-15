package clitest_test

import (
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/test/clitest"
)

// A key is judged by the verifier the command line wires, not by the one the
// suite builds for itself: a production chain that lost its minisign member
// would accept any key, and every suite test would still pass.
func TestReleaseTrustJudgesKeysWithTheWiredVerifier(t *testing.T) {
	r := clitest.NewInstalled(t)
	const key = "RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3"

	r.Run("release", "trust", "add", key[:len(key)-4]).
		ExitCode(domain.ExitUsage).
		StderrContains("not a valid minisign public key")

	r.Run("release", "trust", "add", key).ExitCode(0)
	r.Run("release", "trust", "list").ExitCode(0).StdoutContains(key, "not required")
}
