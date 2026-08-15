package suite

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	signer "github.com/morzecrew/morzer/internal/adapters/sign/minisign"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// Signing the support bundle and reading it back (RFC 0024 P4b).
//
// The half worth testing hardest is not that a signature verifies -- it is
// decision 11, which is a refusal: `inspect` must never verify against the key
// the archive itself names. A test suite that only checked the happy path would
// pass just as well against an implementation that read `meta.json`'s
// `signing_key` and checked the archive against itself, which is the
// implementation a reasonable person writes.

// signingSupportHarness is a machine that can sign and check.
func signingSupportHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.install()
	h.Deps.Signer = signer.New(h.Paths.SigningKeyFile(), "demo")
	h.Deps.Checker = signer.NewChecker()
	return h
}

// unsignedSupportHarness is an installed machine with no signer at all, which
// is what a build without one and a machine that has never minted a key both
// look like from here.
func unsignedSupportHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.install()
	return h
}

// bundleInto writes an archive and returns its path.
func bundleInto(t *testing.T, h *harness, dir string) ops.SupportReport {
	t.Helper()
	report, err := ops.SupportBundle(context.Background(), h.Deps, ops.SupportOptions{Dir: dir})
	require.NoError(t, err)
	return report
}

func TestABundleIsSignedBesideItself(t *testing.T) {
	h := signingSupportHarness(t)
	dir := t.TempDir()

	report := bundleInto(t, h, dir)

	require.True(t, report.Signed, "the archive went out unsigned on a machine that can sign")
	require.NotEmpty(t, report.SigningKey)
	require.Equal(t, report.Path+".minisig", report.SignaturePath)

	sig, err := os.ReadFile(report.SignaturePath)
	require.NoError(t, err, "the signature the report names is not on disk")
	body, err := os.ReadFile(report.Path)
	require.NoError(t, err)

	assert.True(t, h.Deps.Checker.Check(body, sig, report.SigningKey),
		"the signature does not verify against the key the report names")
}

// Decision 9: the signature covers the file that leaves, so a byte changed
// anywhere in it is caught -- including in the parts a reader never decodes.
func TestASignatureCoversTheWholeFile(t *testing.T) {
	h := signingSupportHarness(t)
	dir := t.TempDir()

	report := bundleInto(t, h, dir)
	body, err := os.ReadFile(report.Path)
	require.NoError(t, err)
	sig, err := os.ReadFile(report.SignaturePath)
	require.NoError(t, err)

	tampered := append([]byte(nil), body...)
	tampered[len(tampered)/2] ^= 0xFF

	assert.False(t, h.Deps.Checker.Check(tampered, sig, report.SigningKey),
		"a byte flipped in the middle of the archive still verified")
}

// Decision 12: an archive is still written by a machine that cannot sign, and
// the report says so rather than leaving it to a missing file.
func TestAMachineThatCannotSignStillWritesTheArchive(t *testing.T) {
	h := unsignedSupportHarness(t) // no Signer wired
	dir := t.TempDir()

	report := bundleInto(t, h, dir)

	require.NotEmpty(t, report.Path, "no archive was written")
	require.FileExists(t, report.Path)
	assert.False(t, report.Signed)
	assert.Empty(t, report.SigningKey)
	assert.Empty(t, report.SignaturePath)
	assert.NoFileExists(t, report.Path+".minisig")
}

// Decision 13, and the reason it is a decision: `--preview` is documented as
// writing nothing, and the resolver every other signing path uses mints a key
// on a machine that has never signed.
func TestAPreviewMintsNoSigningKey(t *testing.T) {
	h := signingSupportHarness(t)
	ctx := context.Background()

	require.NoFileExists(t, h.Paths.SigningKeyFile(),
		"the fixture already has a signing key, so this test cannot see one being minted")

	report, err := ops.SupportBundle(ctx, h.Deps, ops.SupportOptions{Preview: true})
	require.NoError(t, err)

	assert.NoFileExists(t, h.Paths.SigningKeyFile(),
		"--preview minted a signing key; it is documented as writing nothing")
	assert.False(t, report.Signed,
		"a preview on a machine with no key claimed the archive would be signed")
}

// The other half of decision 13: once a key exists, a preview reports honestly
// that an archive would be signed, and still writes nothing.
func TestAPreviewReportsTheKeyItWouldSignWith(t *testing.T) {
	h := signingSupportHarness(t)
	ctx := context.Background()

	key, err := h.Deps.Signer.EnsureKey(ctx)
	require.NoError(t, err)

	report, err := ops.SupportBundle(ctx, h.Deps, ops.SupportOptions{Preview: true})
	require.NoError(t, err)

	assert.True(t, report.Signed)
	assert.Equal(t, key.Line, report.SigningKey)
	assert.Empty(t, report.Path, "a preview wrote an archive")
	assert.Empty(t, report.SignaturePath)
}

func TestInspectListsWhatTheBundleWrote(t *testing.T) {
	h := signingSupportHarness(t)
	dir := t.TempDir()

	written := bundleInto(t, h, dir)
	read, err := ops.SupportInspect(context.Background(), h.Deps,
		ops.SupportInspectOptions{Path: written.Path})
	require.NoError(t, err)

	assert.Equal(t, written.Product, read.Product)
	assert.Equal(t, written.InstallationID, read.InstallationID)
	assert.Equal(t, written.ManagerVersion, read.ManagerVersion)

	// The same components, in the same order, with the same sizes: the
	// reader and the writer are describing one archive to two people who
	// are about to compare notes across a ticket.
	//
	// Every component except `meta.json`, which is the one file that cannot
	// appear in its own list -- so the writer has exactly one row more, and
	// the reader accounts for it as the index rather than dropping it.
	writtenEntries := make([]ops.SupportEntry, 0, len(written.Entries))
	var index ops.SupportEntry
	for _, e := range written.Entries {
		if e.Name == "meta.json" {
			index = e
			continue
		}
		writtenEntries = append(writtenEntries, e)
	}
	require.NotEmpty(t, index.Name, "the writer did not report meta.json at all")
	require.Equal(t, len(writtenEntries), len(read.Entries),
		"the reader and the writer disagree about how many components there are")
	for i := range writtenEntries {
		assert.Equal(t, writtenEntries[i].Name, read.Entries[i].Name)
		assert.Equal(t, writtenEntries[i].Bytes, read.Entries[i].Bytes)
		assert.Equal(t, writtenEntries[i].Redactions, read.Entries[i].Redactions)
	}
	assert.Equal(t, index.Bytes, read.IndexBytes,
		"the index size the reader reports is not the one the writer wrote")
}

// Run on the machine that produced the archive, with state recording the key --
// which is what `init` leaves behind. The installation's record is the anchor
// and the verdict names it.
func TestInspectVerifiesAgainstTheInstallationsOwnKeys(t *testing.T) {
	h := signingSupportHarness(t)
	ctx := context.Background()

	written := bundleInto(t, h, t.TempDir())
	recordSigningKey(t, h, written.SigningKey)

	read, err := ops.SupportInspect(ctx, h.Deps,
		ops.SupportInspectOptions{Path: written.Path})
	require.NoError(t, err)

	assert.True(t, read.Signature.Present)
	assert.Equal(t, ops.SignatureSourceInstallation, read.Signature.Source)
	assert.Equal(t, domain.SignedByCurrentKey, read.Signature.Result.Outcome)
	assert.Equal(t, written.SigningKey, read.Signature.Result.Key)
}

// The same machine before anything wrote the key into state, which is every
// installation that reached schema 6 by migration and has signed since: the
// signer mints on demand and only `init`/`init --repair` record the result.
//
// The key on disk is still a trust anchor the archive's producer did not
// supply, so the check happens -- under a source that does not claim state
// backed it.
func TestInspectFallsBackToTheKeyOnDiskAndSaysSo(t *testing.T) {
	h := signingSupportHarness(t)
	ctx := context.Background()

	written := bundleInto(t, h, t.TempDir())

	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	require.False(t, inst.Signing.HasKey(),
		"state already records a key, so this test is not exercising the fallback")

	read, err := ops.SupportInspect(ctx, h.Deps,
		ops.SupportInspectOptions{Path: written.Path})
	require.NoError(t, err)

	assert.Equal(t, ops.SignatureSourceMachineKey, read.Signature.Source,
		"a check against the key on disk was reported as one against installation state")
	assert.Equal(t, domain.SignedByCurrentKey, read.Signature.Result.Outcome)
}

// recordSigningKey writes the machine's key into installation state, which is
// what `init` does and what a migrated installation has never had done.
func recordSigningKey(t *testing.T, h *harness, key string) {
	t.Helper()
	ctx := context.Background()
	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Signing.PublicKey = key
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))
}

// **Decision 11.** The archive names a key; checking against it establishes
// nothing, because whoever wrote the archive wrote the name.
//
// The forgery is complete and self-consistent: a different installation signs
// an archive and names its own key inside it. An implementation that trusted
// `meta.json` would report this as verified, which is the whole point.
func TestInspectRefusesToVerifyAgainstTheArchivesOwnKey(t *testing.T) {
	victim := signingSupportHarness(t)
	forger := signingSupportHarness(t)
	ctx := context.Background()

	forged := bundleInto(t, forger, t.TempDir())
	require.NotEmpty(t, forged.SigningKey)

	// The forged archive is internally consistent -- its signature verifies
	// against the key it names. That is the trap.
	body, err := os.ReadFile(forged.Path)
	require.NoError(t, err)
	sig, err := os.ReadFile(forged.SignaturePath)
	require.NoError(t, err)
	require.True(t, victim.Deps.Checker.Check(body, sig, forged.SigningKey),
		"the fixture is not the case this test is about: the forgery does not "+
			"even verify against its own key")

	// Read by the victim's installation, which has a key of its own and has
	// never seen the forger's.
	key, err := victim.Deps.Signer.EnsureKey(ctx)
	require.NoError(t, err)
	recordSigningKey(t, victim, key.Line)

	read, err := ops.SupportInspect(ctx, victim.Deps,
		ops.SupportInspectOptions{Path: forged.Path})
	require.NoError(t, err)

	assert.Equal(t, ops.SignatureSourceInstallation, read.Signature.Source)
	assert.Equal(t, domain.Unverifiable, read.Signature.Result.Outcome,
		"an archive signed by a key this installation has never seen verified anyway; "+
			"the check used the key the archive supplied")
	assert.Equal(t, forged.SigningKey, read.Signature.ClaimedKey,
		"the claimed key is not reported, so a reader cannot go and check it out of band")
}

// A vendor's laptop: no installation here, no --key given. The answer must be
// "not checked", never a verdict.
func TestInspectWithNothingToCheckAgainstSaysSo(t *testing.T) {
	producer := signingSupportHarness(t)
	written := bundleInto(t, producer, t.TempDir())

	// A machine with a checker and no installation at all, which is what a
	// vendor running this on a received file has.
	// Deliberately *not* installed: a vendor's laptop has no installation
	// state, which is the case decision 11 is really about.
	vendor := newHarness(t)
	vendor.Deps.Checker = signer.NewChecker()

	read, err := ops.SupportInspect(context.Background(), vendor.Deps,
		ops.SupportInspectOptions{Path: written.Path})
	require.NoError(t, err)

	assert.True(t, read.Signature.Present, "the signature file was not noticed")
	assert.Equal(t, ops.SignatureSourceNone, read.Signature.Source)
	assert.Equal(t, written.SigningKey, read.Signature.ClaimedKey,
		"without a claimed key a reader has nothing to go and ask for")
}

// --key is the vendor's path: a key obtained from the operator, not from the
// file.
func TestInspectVerifiesAgainstTheKeyTheCallerNames(t *testing.T) {
	producer := signingSupportHarness(t)
	written := bundleInto(t, producer, t.TempDir())

	vendor := newHarness(t) // no installation, as a vendor's machine has none
	vendor.Deps.Checker = signer.NewChecker()

	read, err := ops.SupportInspect(context.Background(), vendor.Deps,
		ops.SupportInspectOptions{Path: written.Path, ExpectedKey: written.SigningKey})
	require.NoError(t, err)

	assert.Equal(t, ops.SignatureSourceExpectedKey, read.Signature.Source)
	assert.Equal(t, domain.SignedByCurrentKey, read.Signature.Result.Outcome)
}

// The same path with the wrong key: the vendor was told to expect one key and
// the archive was signed by another, which is the finding --key exists to
// produce.
func TestInspectReportsAKeyMismatchAsAFailure(t *testing.T) {
	producer := signingSupportHarness(t)
	other := signingSupportHarness(t)
	ctx := context.Background()

	written := bundleInto(t, producer, t.TempDir())
	otherKey, err := other.Deps.Signer.EnsureKey(ctx)
	require.NoError(t, err)
	require.NotEqual(t, written.SigningKey, otherKey.Line)

	vendor := newHarness(t)
	vendor.Deps.Checker = signer.NewChecker()

	read, err := ops.SupportInspect(ctx, vendor.Deps,
		ops.SupportInspectOptions{Path: written.Path, ExpectedKey: otherKey.Line})
	require.NoError(t, err)

	assert.Equal(t, ops.SignatureSourceExpectedKey, read.Signature.Source)
	assert.Equal(t, domain.Unverifiable, read.Signature.Result.Outcome)
}

// --key takes a file too, because that is what minisign writes and what a
// vendor is most likely to have on disk.
func TestTheExpectedKeyCanBeAFile(t *testing.T) {
	producer := signingSupportHarness(t)
	written := bundleInto(t, producer, t.TempDir())

	// minisign's public key file: an untrusted comment, then the key.
	keyFile := filepath.Join(t.TempDir(), "operator.pub")
	require.NoError(t, os.WriteFile(keyFile,
		[]byte("untrusted comment: minisign public key\n"+written.SigningKey+"\n"), 0o600))

	vendor := newHarness(t)
	vendor.Deps.Checker = signer.NewChecker()

	read, err := ops.SupportInspect(context.Background(), vendor.Deps,
		ops.SupportInspectOptions{Path: written.Path, ExpectedKey: keyFile})
	require.NoError(t, err)

	assert.Equal(t, ops.SignatureSourceExpectedKey, read.Signature.Source)
	assert.Equal(t, domain.SignedByCurrentKey, read.Signature.Result.Outcome)
}

// An archive whose `.minisig` did not travel is distinguishable from one that
// was never signed, because the remedies differ: chase the missing file, or
// accept that the machine had no key.
func TestAMissingSignatureFileIsNotAnUnsignedArchive(t *testing.T) {
	h := signingSupportHarness(t)
	written := bundleInto(t, h, t.TempDir())
	require.NoError(t, os.Remove(written.SignaturePath))

	read, err := ops.SupportInspect(context.Background(), h.Deps,
		ops.SupportInspectOptions{Path: written.Path})
	require.NoError(t, err)

	assert.False(t, read.Signature.Present)
	assert.NotEmpty(t, read.Signature.ClaimedKey,
		"the archive still names the key that signed it, which is how a reader "+
			"tells a lost signature from a machine that never had a key")
}

// meta.json carries the bound, so the reader handed this file with no
// documentation is told what the signature does and does not prove.
func TestTheArchiveCarriesWhatItsSignatureProves(t *testing.T) {
	h := signingSupportHarness(t)
	written := bundleInto(t, h, t.TempDir())

	read, err := ops.SupportInspect(context.Background(), h.Deps,
		ops.SupportInspectOptions{Path: written.Path})
	require.NoError(t, err)

	require.NotEmpty(t, read.Signature.Bound)
	assert.Equal(t, domain.SupportSignatureBound, read.Signature.Bound)
	assert.Contains(t, read.Signature.Bound, "not a key to verify against",
		"the bound does not carry decision 11, which is the mistake it exists to stop")
}

// An unsigned archive says nothing about signatures rather than carrying a
// bound describing a proof that does not exist.
func TestAnUnsignedArchiveCarriesNoBound(t *testing.T) {
	h := unsignedSupportHarness(t) // no signer
	written := bundleInto(t, h, t.TempDir())

	h.Deps.Checker = signer.NewChecker()
	read, err := ops.SupportInspect(context.Background(), h.Deps,
		ops.SupportInspectOptions{Path: written.Path})
	require.NoError(t, err)

	assert.Empty(t, read.Signature.Bound)
	assert.Empty(t, read.Signature.ClaimedKey)
	assert.False(t, read.Signature.Present)
}

// Not a support bundle at all: a refusal that says which of the two problems it
// is, rather than a decoder error the operator has to interpret.
func TestInspectRefusesAFileThatIsNotABundle(t *testing.T) {
	h := signingSupportHarness(t)
	path := filepath.Join(t.TempDir(), "notes.tar.zst")
	require.NoError(t, os.WriteFile(path, []byte("this is not a zstd frame"), 0o600))

	_, err := ops.SupportInspect(context.Background(), h.Deps,
		ops.SupportInspectOptions{Path: path})
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(domain.AsError(err).Message), "not a support bundle")
}

// ----------------------------------------------------------------------------
// Encrypted archives: §6 asks for the round trip both ways.

// The vendor's path end to end: an archive they can read, signed by a machine
// that cannot read it back.
func TestInspectReadsAnEncryptedArchiveWithTheVendorsKey(t *testing.T) {
	h := signingSupportHarness(t)
	identity, public := vendorIdentity(t)
	declareRecipients(t, h, public)

	written := bundleInto(t, h, t.TempDir())
	require.True(t, written.Encrypted, "the fixture did not produce an encrypted archive")
	require.True(t, written.Signed)

	vendor := newHarness(t) // no installation, as a vendor's machine has none
	vendor.Deps.Checker = signer.NewChecker()

	read, err := ops.SupportInspect(context.Background(), vendor.Deps,
		ops.SupportInspectOptions{
			Path:         written.Path,
			IdentityFile: identity,
			ExpectedKey:  written.SigningKey,
		})
	require.NoError(t, err)

	assert.True(t, read.Encrypted)
	assert.NotEmpty(t, read.Entries, "the archive decrypted to no components")
	assert.Equal(t, ops.SignatureSourceExpectedKey, read.Signature.Source)
	assert.Equal(t, domain.SignedByCurrentKey, read.Signature.Result.Outcome)
}

// **Decision 9's payoff.** The signature covers the ciphertext, so authenticity
// is established by a party holding no recipient key at all — a vendor's intake
// deciding whether to hand the file to an age implementation in the first place.
//
// This is the test that would fail if the signature covered the plaintext, and
// it is the whole argument for A14.
func TestAnEncryptedArchivesSignatureChecksWithoutTheKeyToReadIt(t *testing.T) {
	h := signingSupportHarness(t)
	_, public := vendorIdentity(t)
	declareRecipients(t, h, public)

	written := bundleInto(t, h, t.TempDir())
	require.True(t, written.Encrypted)

	body, err := os.ReadFile(written.Path)
	require.NoError(t, err)
	sig, err := os.ReadFile(written.SignaturePath)
	require.NoError(t, err)

	// No identity anywhere in sight: these are the bytes as they sit in a
	// ticket system, and they check out.
	assert.True(t, h.Deps.Checker.Check(body, sig, written.SigningKey),
		"the signature over an encrypted archive cannot be checked without decrypting it, "+
			"so the signature covers the plaintext rather than the file that leaves")
}

// An encrypted archive with no identity is a refusal that says what to pass,
// not a decoder error about a zstd frame.
func TestInspectRefusesAnEncryptedArchiveWithNoIdentity(t *testing.T) {
	h := signingSupportHarness(t)
	_, public := vendorIdentity(t)
	declareRecipients(t, h, public)

	written := bundleInto(t, h, t.TempDir())

	_, err := ops.SupportInspect(context.Background(), h.Deps,
		ops.SupportInspectOptions{Path: written.Path})
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Message, "encrypted")
	assert.Contains(t, strings.ToLower(domain.AsError(err).Message+" "+
		domain.AsError(err).Hint), "--identity",
		"the refusal does not name the flag that would have worked")
}

// The machine that wrote an encrypted archive still cannot read it back, and
// `inspect` is not a way around that — which is worth pinning, because a
// command whose job is to read archives is exactly where such a hole appears.
func TestInspectDoesNotLetTheMachineReadItsOwnEncryptedArchive(t *testing.T) {
	h := signingSupportHarness(t)
	_, public := vendorIdentity(t)
	declareRecipients(t, h, public)

	written := bundleInto(t, h, t.TempDir())

	_, err := ops.SupportInspect(context.Background(), h.Deps,
		ops.SupportInspectOptions{
			Path:         written.Path,
			IdentityFile: h.Paths.AgeIdentityFile(),
		})
	require.Error(t, err,
		"the machine that wrote the archive read it back with its own identity")
}

// ----------------------------------------------------------------------------
// What the sabotage sweep found untested.

// A crafted archive whose first `meta.json`-looking entry is a decoy.
//
// Found by sabotage: relaxing the index match from "is `meta.json`" to "ends
// with `meta.json`" passed every other test here, because no fixture had a
// second file whose name ends that way. This reader takes bytes from strangers
// and the first matching entry wins -- so a decoy at `logs/meta.json` would be
// read as the archive's own account of itself, and the real index below it
// would never be reached.
func TestADecoyIndexDeeperInTheArchiveIsNotTheIndex(t *testing.T) {
	h := signingSupportHarness(t)

	realIndex := `{"product":"demo","installation_id":"real","manager_version":"1.0.0",` +
		`"entries":[{"name":"manifest.yaml","title":"The resolved manifest",` +
		`"bytes":10,"redactions":0}]}`
	decoy := `{"product":"demo","installation_id":"decoy","manager_version":"1.0.0",` +
		`"entries":[]}`

	path := filepath.Join(t.TempDir(), "crafted.tar.zst")
	writeTarZstByHand(t, path, [][2]string{
		{"logs/meta.json", decoy},
		{"meta.json", realIndex},
	})

	read, err := ops.SupportInspect(context.Background(), h.Deps,
		ops.SupportInspectOptions{Path: path})
	require.NoError(t, err)

	assert.Equal(t, "real", read.InstallationID,
		"a decoy at logs/meta.json was read as the archive's index")
	assert.Len(t, read.Entries, 1)
}

// Inspecting is a read, and a read must not mint cryptographic material.
//
// Found by sabotage: swapping the fallback's `PublicKey` for `EnsureKey`
// survived every test, because each one had already caused a key to exist. The
// case that matters is the machine that has never signed -- where `EnsureKey`
// creates the identity every future signature is attributed to, as a side
// effect of looking at somebody else's file.
func TestInspectMintsNoSigningKey(t *testing.T) {
	producer := signingSupportHarness(t)
	written := bundleInto(t, producer, t.TempDir())

	// A second machine that can sign and never has.
	reader := signingSupportHarness(t)
	require.NoFileExists(t, reader.Paths.SigningKeyFile(),
		"the fixture already has a key, so this test cannot see one being minted")

	read, err := ops.SupportInspect(context.Background(), reader.Deps,
		ops.SupportInspectOptions{Path: written.Path})
	require.NoError(t, err)

	assert.NoFileExists(t, reader.Paths.SigningKeyFile(),
		"`support inspect` minted a signing key while reading somebody else's archive")
	assert.Equal(t, ops.SignatureSourceNone, read.Signature.Source,
		"a machine with no key of its own reported checking against one")
}

// `--key` outranks the installation's own record, and the case where that is
// observable is the one nothing covered: both anchors present, disagreeing.
//
// A vendor who names a key is asking "was this signed by the key my customer
// gave me". Answering "yes, by one of ours" instead answers a different
// question with the same word.
func TestTheNamedKeyOutranksTheInstallationsOwn(t *testing.T) {
	producer := signingSupportHarness(t)
	reader := signingSupportHarness(t)
	ctx := context.Background()

	written := bundleInto(t, producer, t.TempDir())

	// The reader is a fully set-up installation whose own key is not the
	// one that signed the archive.
	own, err := reader.Deps.Signer.EnsureKey(ctx)
	require.NoError(t, err)
	recordSigningKey(t, reader, own.Line)
	require.NotEqual(t, written.SigningKey, own.Line)

	read, err := ops.SupportInspect(ctx, reader.Deps, ops.SupportInspectOptions{
		Path:        written.Path,
		ExpectedKey: written.SigningKey,
	})
	require.NoError(t, err)

	assert.Equal(t, ops.SignatureSourceExpectedKey, read.Signature.Source,
		"the installation's own keys were consulted instead of the key that was named")
	assert.Equal(t, domain.SignedByCurrentKey, read.Signature.Result.Outcome)
}

// writeTarZstByHand builds an archive with entries in a chosen order, which is
// the one thing the production writer will not do.
func writeTarZstByHand(t *testing.T, path string, entries [][2]string) {
	t.Helper()

	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	require.NoError(t, err)
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: e[0], Mode: 0o600, Size: int64(len(e[1])), Typeflag: tar.TypeReg,
		}))
		_, err := tw.Write([]byte(e[1]))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, zw.Close())
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))
}

// ----------------------------------------------------------------------------
// Detection branches: the code that only runs when something is already wrong.

// A `meta.json` past the bound is refused rather than read, which is the branch
// that only executes when an archive is hostile or broken.
func TestAnOversizedIndexIsRefused(t *testing.T) {
	h := signingSupportHarness(t)

	// Just past the 8 MiB the reader will take. Built by hand because the
	// production writer will never produce one.
	huge := `{"product":"demo","installation_id":"x","entries":[],"pad":"` +
		strings.Repeat("a", 8<<20) + `"}`
	path := filepath.Join(t.TempDir(), "huge.tar.zst")
	writeTarZstByHand(t, path, [][2]string{{"meta.json", huge}})

	_, err := ops.SupportInspect(context.Background(), h.Deps,
		ops.SupportInspectOptions{Path: path})
	require.Error(t, err, "an index past the bound was read anyway")
	assert.Contains(t, domain.AsError(err).Message, "past the")
}

// An archive with no index is not a support bundle, and the refusal says which
// of the two problems it is rather than quoting a decoder.
func TestAnArchiveWithNoIndexIsRefused(t *testing.T) {
	h := signingSupportHarness(t)
	path := filepath.Join(t.TempDir(), "other.tar.zst")
	writeTarZstByHand(t, path, [][2]string{{"readme.txt", "not a bundle"}})

	_, err := ops.SupportInspect(context.Background(), h.Deps,
		ops.SupportInspectOptions{Path: path})
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Message, "no meta.json")
}

// A `--key` file with nothing in it is the operator's mistake, and it is
// reported as one -- the listing still prints, because the archive is fine and
// only the check did not happen.
func TestAnEmptyKeyFileLeavesTheSignatureUnchecked(t *testing.T) {
	producer := signingSupportHarness(t)
	written := bundleInto(t, producer, t.TempDir())

	empty := filepath.Join(t.TempDir(), "empty.pub")
	require.NoError(t, os.WriteFile(empty, []byte("   \n\n"), 0o600))

	vendor := newHarness(t)
	vendor.Deps.Checker = signer.NewChecker()

	read, err := ops.SupportInspect(context.Background(), vendor.Deps,
		ops.SupportInspectOptions{Path: written.Path, ExpectedKey: empty})
	require.NoError(t, err, "an unusable --key failed the whole command")

	assert.NotEmpty(t, read.Entries, "the listing was withheld over a key problem")
	assert.Equal(t, ops.SignatureSourceNone, read.Signature.Source,
		"an unusable --key produced a verdict")
}

// A build with no signature checker answers "not checked" rather than
// "unverifiable", which would name a finding that nothing established.
func TestABuildWithNoCheckerSaysItDidNotCheck(t *testing.T) {
	producer := signingSupportHarness(t)
	written := bundleInto(t, producer, t.TempDir())

	reader := signingSupportHarness(t)
	reader.Deps.Checker = nil

	read, err := ops.SupportInspect(context.Background(), reader.Deps,
		ops.SupportInspectOptions{Path: written.Path, ExpectedKey: written.SigningKey})
	require.NoError(t, err)

	assert.True(t, read.Signature.Present)
	assert.Equal(t, ops.SignatureSourceNone, read.Signature.Source)
	assert.NotEqual(t, domain.Unverifiable, read.Signature.Result.Outcome,
		"a build that cannot check reported the archive as unverifiable, "+
			"which is a finding about the archive rather than about the build")
}

// No path at all is a usage error naming the shape of the command.
func TestInspectWithNoPathSaysWhatToType(t *testing.T) {
	h := signingSupportHarness(t)
	_, err := ops.SupportInspect(context.Background(), h.Deps, ops.SupportInspectOptions{})
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Hint, "support inspect")
}

// The file is refused by size before it is read, which is the door every byte
// in this command comes through.
//
// Found by re-reading the header comment against the code: it claimed the input
// was streamed and bounded while the entry point called `os.ReadFile` on
// whatever the operator named.
func TestAnArchivePastTheBoundIsRefusedBeforeItIsRead(t *testing.T) {
	h := signingSupportHarness(t)

	path := filepath.Join(t.TempDir(), "enormous.tar.zst")
	f, err := os.Create(path)
	require.NoError(t, err)
	// Sparse: the size is what is checked, and writing 64 MiB of zeroes to
	// prove that would be a minute of disk for one branch.
	require.NoError(t, f.Truncate((64<<20)+1))
	require.NoError(t, f.Close())

	_, err = ops.SupportInspect(context.Background(), h.Deps,
		ops.SupportInspectOptions{Path: path})
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Message, "past the")
}

// A `--key` naming a directory is the operator's mistake, and saying "signature
// does NOT verify" would report it as the archive's.
func TestAKeyThatNamesADirectoryIsNotAVerdict(t *testing.T) {
	producer := signingSupportHarness(t)
	written := bundleInto(t, producer, t.TempDir())

	vendor := newHarness(t)
	vendor.Deps.Checker = signer.NewChecker()

	read, err := ops.SupportInspect(context.Background(), vendor.Deps,
		ops.SupportInspectOptions{Path: written.Path, ExpectedKey: t.TempDir()})
	require.NoError(t, err)

	assert.Equal(t, ops.SignatureSourceNone, read.Signature.Source,
		"a directory named as a key produced a verdict about the archive")
	assert.NotEqual(t, domain.Unverifiable, read.Signature.Result.Outcome)
}
