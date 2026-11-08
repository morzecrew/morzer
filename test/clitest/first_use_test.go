package clitest_test

import (
	"testing"

	"github.com/morzecrew/morzer/test/clitest"
)

// initArgs is `init` as NewInstalled runs it, plus extra.
func initArgs(r *clitest.Runner, extra ...string) []string {
	return append([]string{
		"init",
		"--release", r.Bundle,
		"--profile", "embedded",
		"--domain", "demo.example",
		"--no-recovery-recipient",
		"--install-units=false",
	}, extra...)
}

// An `init` given no key trusts the one the release ships, and prints it for
// the operator to compare with the vendor's (RFC 0052 decision 9).
func TestInitTrustsTheKeyAReleaseShipsAndPrintsIt(t *testing.T) {
	r := clitest.New(t)
	sk, key := minisignKeypair(t)
	shipKey(t, r.Bundle, key)
	signBundle(t, sk, r.Bundle)

	r.Run(initArgs(r)...).ExitCode(0).
		StderrContains("Trusted the signing key the release ships", key, minisignID(sk), "vendor publishes")

	r.Run("--json", "release", "trust", "list").ExitCode(0).
		FieldEquals("data.require_signature", true).
		FieldLen("data.keys", 1)
	r.Run("release", "trust", "list").ExitCode(0).StdoutContains(key)
}

func TestInitReportsTheKeyItTrustedInJSON(t *testing.T) {
	r := clitest.New(t)
	sk, key := minisignKeypair(t)
	shipKey(t, r.Bundle, key)
	signBundle(t, sk, r.Bundle)

	r.Run(append([]string{"--json"}, initArgs(r)...)...).ExitCode(0).
		FieldEquals("data.trusted_key", key).
		FieldEquals("data.trusted_key_id", minisignID(sk)).
		FieldEquals("data.trusted_key_source", "release")
}

// `--require-signature` is tri-state on `init`: unset, a trusted key requires a
// signature; `=false` says not to, and trusts no key the release ships.
func TestRequireSignatureIsTriStateOnInit(t *testing.T) {
	t.Run("=false trusts no shipped key", func(t *testing.T) {
		r := clitest.New(t)
		sk, key := minisignKeypair(t)
		shipKey(t, r.Bundle, key)
		signBundle(t, sk, r.Bundle)

		r.Run(initArgs(r, "--require-signature=false")...).ExitCode(0).
			NoOutputContains("Trusted the signing key", key)
		r.Run("--json", "release", "trust", "list").ExitCode(0).
			FieldEquals("data.require_signature", false).
			FieldLen("data.keys", 0)
	})

	t.Run("a key alone requires a signature, and the refusal names the opt-out", func(t *testing.T) {
		r := clitest.New(t)
		_, key := minisignKeypair(t)

		r.Run(initArgs(r, "--signing-key", key)...).Failed().
			OutputContains("not signed", "--require-signature=false")
	})
}
