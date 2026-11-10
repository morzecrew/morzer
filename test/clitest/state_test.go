package clitest_test

import (
	"os"
	"testing"
)

// `state` needs no installation: it runs where the authority's key is, which
// is not a managed machine. Generating the key writes it once, readable only
// by its owner, and prints the public key the machines pin.
func TestTheAuthorityKeyIsGeneratedWithoutAnInstallation(t *testing.T) {
	r := withTwoInstallations(t)
	key := r.Path("authority.key")

	r.Run("state", "key", "generate", key).ExitCode(0).StdoutContains("RW")
	info, err := os.Stat(key)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("the authority key is readable by others: %v", mode)
	}

	r.Run("state", "key", "generate", key).ExitCode(2).SaysAll("already exists")
	r.Run("state", "publish", "--target", "file://"+r.Path("state"), "--expect", r.Path("missing.yaml"),
		"--product", "demo", "--installation", "inst_01", "--file", "-", "--key", key).Failed()
}
