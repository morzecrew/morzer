//go:build docker

package suite

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	signer "github.com/morzecrew/morzer/internal/adapters/sign/minisign"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/test/dockerlab"
)

// The whole of wave 15, end to end: an `apply` produces a statement, the
// statement is signed with this machine's key, and the **real minisign binary**
// verifies it.
//
// This is the test the two RFCs exist for. RFC 0028 refuses to ship a signing
// key ahead of a consumer, and RFC 0025 refuses to ship an unsigned statement:
// either alone would be a mechanism nothing exercises. Verifying with our own
// code would prove neither, because a signature we both make and check can be
// wrong in one consistent way and pass.

// signingHarness is the fake-adapter harness with a real signer wired in, which
// is the one adapter these tests cannot fake without testing nothing.
func signingHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.Deps.Signer = signer.New(h.Paths.SigningKeyFile(), "demo")
	return h
}

// applied runs an apply on an installation that has a key and a salt.
//
// mutate runs after the installation exists and before it is saved, which is
// the only window in which a test can add something apply will read -- h.install
// writes state, so a test that set a field first would have it overwritten.
func applied(t *testing.T, h *harness, mutate ...func(*domain.Installation)) domain.Installation {
	t.Helper()

	inst := h.install()

	key, err := h.Deps.Signer.EnsureKey(context.Background())
	require.NoError(t, err)
	inst.Signing.PublicKey = key.Line
	inst.AttestationSalt = "0123456789abcdef0123456789abcdef"
	for _, m := range mutate {
		m(&inst)
	}
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	_, err = ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)
	return inst
}

// attestations lists the statements on disk, newest-agnostic: there is one.
func attestations(t *testing.T, h *harness) []string {
	t.Helper()
	entries, err := os.ReadDir(h.Paths.AttestationsDir())
	require.NoError(t, err)

	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			out = append(out, filepath.Join(h.Paths.AttestationsDir(), e.Name()))
		}
	}
	return out
}

func TestApplyEmitsAStatementTheRealMinisignVerifies(t *testing.T) {
	dockerlab.Require(t)

	h := signingHarness(t)
	inst := applied(t, h)

	files := attestations(t, h)
	require.Len(t, files, 1, "apply emitted %d statements, want 1", len(files))
	doc := files[0]

	sig := doc + ".minisig"
	require.FileExists(t, sig, "the statement was written unsigned")

	// The real binary, over the document as written, with the key from
	// installation state -- which is the exact gesture an auditor holding
	// the two files would perform.
	dir := filepath.Dir(doc)
	out, err := minisignRun(t, dir,
		"minisign", "-Vm", filepath.Base(doc), "-P", inst.Signing.PublicKey)
	require.NoError(t, err, "minisign rejected the attestation:\n%s", out)
	require.Contains(t, out, "Signature and comment signature verified", out)
}

// TestATamperedStatementStopsVerifying is the verified-red half. Without it the
// test above passes for a signature over the wrong bytes.
func TestATamperedStatementStopsVerifying(t *testing.T) {
	dockerlab.Require(t)

	h := signingHarness(t)
	inst := applied(t, h)
	doc := attestations(t, h)[0]

	// The change an attacker would actually make: claim a verification the
	// document deliberately says nothing about.
	body, err := os.ReadFile(doc)
	require.NoError(t, err)
	tampered := strings.Replace(string(body),
		`"digest_pinned"`, `"signature_verified": true, "digest_pinned"`, 1)
	require.NotEqual(t, string(body), tampered, "the fixture did not contain the field to tamper with")
	require.NoError(t, os.WriteFile(doc, []byte(tampered), 0o644))

	out, err := minisignRun(t, filepath.Dir(doc),
		"minisign", "-Vm", filepath.Base(doc), "-P", inst.Signing.PublicKey)
	require.Error(t, err, "minisign accepted an edited attestation:\n%s", out)
}

// The document's shape, asserted against what it must never contain.
func TestTheStatementCarriesNamesAndTheBoundAndNoValues(t *testing.T) {
	dockerlab.Require(t)

	h := signingHarness(t)

	// A parameter whose *value* is the kind of thing that must not travel.
	applied(t, h, func(inst *domain.Installation) {
		inst.Parameters = map[string]string{"http_port": "18443"}
	})

	body, err := os.ReadFile(attestations(t, h)[0])
	require.NoError(t, err)

	var stmt domain.Statement
	require.NoError(t, json.Unmarshal(body, &stmt))

	assert.Equal(t, domain.StatementType, stmt.Type)
	assert.Equal(t, domain.PredicateType, stmt.PredicateType)

	// The bound travels with the artifact, because the reader who needs it
	// most is the one who found the file without the documentation.
	assert.Equal(t, domain.AttestationBound, stmt.Predicate.Bound)

	assert.Contains(t, stmt.Predicate.Config.ParameterNames, "http_port")
	assert.NotContains(t, string(body), "18443",
		"a parameter value reached the attestation")

	// And the signer is named, so a reader can check the signature without
	// having the installation to hand.
	assert.NotEmpty(t, stmt.Predicate.Installation.SigningKey)

	// apply verifies no signature -- that happens when a release is staged
	// -- so the document must say nothing rather than say "false", which an
	// auditor would read as a failed check.
	assert.Nil(t, stmt.Predicate.Verification.SignatureVerified,
		"the document claims a verification apply never performed")
	assert.NotContains(t, string(body), "signature_verified",
		"an unestablished check was rendered as a finding")

	// And it does not describe an update that did not happen.
	assert.Empty(t, stmt.Predicate.Release.FromVersion,
		"an apply reported moving away from a version")
}

// A machine with no salt emits no digest rather than an unsalted one.
//
// The failure this guards is quiet and bad: an unsalted digest over a handful
// of ports and booleans is brute-forceable back to its inputs, and it would
// appear on exactly the machines nobody is watching -- the ones that reached
// schema 6 by migration and never ran `init` again.
func TestAMachineWithNoSaltEmitsNoDigestRatherThanAnUnsaltedOne(t *testing.T) {
	dockerlab.Require(t)

	h := signingHarness(t)
	inst := h.install()

	key, err := h.Deps.Signer.EnsureKey(context.Background())
	require.NoError(t, err)
	inst.Signing.PublicKey = key.Line
	inst.AttestationSalt = "" // the migrated machine
	require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), inst))

	_, err = ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)

	body, err := os.ReadFile(attestations(t, h)[0])
	require.NoError(t, err)

	var stmt domain.Statement
	require.NoError(t, json.Unmarshal(body, &stmt))
	assert.Empty(t, stmt.Predicate.Config.RenderedDigest,
		"a machine with no salt published a digest that can be brute-forced")
}

// A machine that has never minted a key acquires one when it first signs.
//
// This is RFC 0028 §5.6 rather than a convenience: the migration mints nothing,
// so an installation upgraded into schema 6 has no key, and "a manager upgrade
// that silently generates cryptographic material is a surprise -- producing a
// signed artifact is a request". Emission is that request.
//
// Without this the migrated machines -- every installation in the field -- would
// have emitted unsigned statements forever, which is the shape of gap review
// found rather than tests did.
func TestAMachineWithNoKeyMintsOneWhenItFirstSigns(t *testing.T) {
	dockerlab.Require(t)

	h := signingHarness(t)
	h.install()

	// No key, no recorded key: the migrated machine.
	require.NoFileExists(t, h.Paths.SigningKeyFile())

	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err, "apply failed on a machine with no signing key")

	files := attestations(t, h)
	require.Len(t, files, 1)
	require.FileExists(t, files[0]+".minisig",
		"a machine that could have minted a key emitted an unsigned statement")

	var stmt domain.Statement
	body, err := os.ReadFile(files[0])
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &stmt))
	require.NotEmpty(t, stmt.Predicate.Installation.SigningKey)

	// And the document names the key that actually signed it, which is the
	// property that survives state and disk disagreeing.
	out, err := minisignRun(t, filepath.Dir(files[0]),
		"minisign", "-Vm", filepath.Base(files[0]),
		"-P", stmt.Predicate.Installation.SigningKey)
	require.NoError(t, err, out)
}

// A build with no signer at all still files the record, unsigned.
//
// Writing the statement is the job; signing it is what a signer adds. Making
// the first conditional on the second would mean such a build silently kept no
// record at all.
func TestABuildWithNoSignerStillFilesTheRecord(t *testing.T) {
	dockerlab.Require(t)

	h := signingHarness(t)
	h.Deps.Signer = nil
	h.install()

	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)

	files := attestations(t, h)
	require.Len(t, files, 1)
	assert.NoFileExists(t, files[0]+".minisig")

	var stmt domain.Statement
	body, err := os.ReadFile(files[0])
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &stmt))
	assert.Empty(t, stmt.Predicate.Installation.SigningKey)
}

// A converged apply digests the same configuration as the one that changed it.
//
// The digest is a drift detector, and `stepRenderConfiguration.Check` returns
// true when every rendered target already matches the file on disk -- so the
// engine skips Execute on exactly the runs where nothing changed. Reading the
// configuration only from the executing path meant those runs digested nothing,
// and two applies of identical configuration produced two different digests: a
// drift detector that reports drift every second run.
func TestARepeatedApplyDigestsTheSameConfiguration(t *testing.T) {
	dockerlab.Require(t)

	h := signingHarness(t)
	applied(t, h)

	first := attestations(t, h)
	require.Len(t, first, 1)

	// The second apply converges onto a system that is already converged,
	// which is the case systemd runs at every boot.
	_, err := ops.Apply(context.Background(), h.Deps, ops.Options{})
	require.NoError(t, err)

	files := attestations(t, h)
	require.Len(t, files, 2, "the second apply filed no statement")

	digests := make([]string, 0, 2)
	for _, f := range files {
		body, err := os.ReadFile(f)
		require.NoError(t, err)
		var stmt domain.Statement
		require.NoError(t, json.Unmarshal(body, &stmt))
		digests = append(digests, stmt.Predicate.Config.RenderedDigest)
	}

	require.NotEmpty(t, digests[0], "the first apply recorded no configuration digest")
	assert.Equal(t, digests[0], digests[1],
		"identical configuration produced two different digests, so the digest tracks "+
			"whether the step had work to do rather than what the configuration is")
}
