package suite

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/secrets/sopsage"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/agecrypt"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/release"
)

// Encrypting the support bundle (RFC 0024 P4).
//
// The property §3.4 promises is not "the archive is encrypted" -- it is that
// the archive is unreadable *by the machine that wrote it*. An archive sitting
// in a ticket system, an email thread or a bucket is then not readable by the
// ticket system, the mail provider, or an attacker who has the live host. So
// these tests decrypt, and they check who cannot.

// vendorIdentity mints the key a vendor would publish and hold offline.
func vendorIdentity(t *testing.T) (identityPath, publicKey string) {
	t.Helper()
	identityPath = filepath.Join(t.TempDir(), "vendor-identity")
	public, err := sopsage.GenerateIdentity(identityPath)
	require.NoError(t, err)
	return identityPath, public
}

// declareSupportBlock appends a block to the release manifest's existing
// `extensions` mapping and re-records the release.
//
// Re-recorded rather than only rewritten: the resolver checks the release
// digest and deliberately refuses a directory modified after installation, so a
// test that edited the manifest and stopped would be testing the broken-release
// path while believing it was testing this one.
func declareSupportBlock(t *testing.T, h *harness, block string) {
	t.Helper()
	ctx := context.Background()

	path := filepath.Join(h.Release.Root, release.ManifestFileName)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(raw), "\nextensions:\n",
		"the fixture manifest has no extensions block to extend")
	require.NoError(t, os.WriteFile(path, append(raw, []byte(block)...), 0o600))

	rel, err := release.Load(h.Release.Root)
	require.NoError(t, err)
	h.Release = rel

	require.NoError(t, h.Deps.State.SetCurrentRelease(ctx, domain.ReleaseRecord{
		SchemaVersion: domain.InstallationSchemaVersion,
		Name:          rel.Name(),
		Version:       rel.Version(),
		Digest:        rel.Digest,
		Root:          rel.Root,
		InstalledAt:   domain.NewTime(h.Deps.Now()),
	}))
}

func declareRecipients(t *testing.T, h *harness, keys ...string) {
	t.Helper()
	block := "  " + domain.SupportExtension + ":\n    recipients:\n"
	for _, k := range keys {
		block += "      - " + k + "\n"
	}
	declareSupportBlock(t, h, block)
}

// The archive the vendor can read and the machine cannot.
//
// This is the claim of §3.4 in one test. A bundle encrypted to the machine's
// own key would look identical in every report field and would be worth
// nothing: compromising the live host would yield the diagnostics as well.
func TestAnEncryptedArchiveIsUnreadableByTheMachineThatWroteIt(t *testing.T) {
	h := newHarness(t)
	h.install()
	ctx := context.Background()

	vendorKey, vendorPublic := vendorIdentity(t)
	declareRecipients(t, h, vendorPublic)

	dir := t.TempDir()
	report, err := ops.SupportBundle(ctx, h.Deps, ops.SupportOptions{Dir: dir})
	require.NoError(t, err)

	// One file, and it is the encrypted one.
	//
	// The plaintext archive is assembled in a staging directory precisely so
	// that a readable copy of everything never appears, even briefly, under
	// the name an operator is watching for in the directory they are about
	// to attach a file from. Building it at the destination and encrypting
	// in place afterwards would pass every other assertion here.
	written, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(written))
	for _, e := range written {
		names = append(names, e.Name())
	}
	assert.Equal(t, []string{filepath.Base(report.Path)}, names,
		"the destination directory holds something besides the encrypted archive")

	assert.True(t, report.Encrypted, "the report does not say the archive is encrypted")
	assert.Equal(t, []string{vendorPublic}, report.Recipients)
	assert.True(t, strings.HasSuffix(report.Path, ".tar.zst"+agecrypt.Extension),
		"an encrypted archive is not named as one: %s", report.Path)

	// Not readable as an archive. Asserted before the successful decrypt,
	// because a test that only proves the vendor can open it would pass on
	// the day the bytes were written in the clear under an `.age` name.
	raw, err := os.ReadFile(report.Path)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "installation.yaml",
		"the archive's entry names are readable, so it is not encrypted")
	assert.True(t, bytes.HasPrefix(raw, []byte("age-encryption.org/")),
		"the file at the archive path is not an age file")

	// A key the manifest does not name does not open it, asserted
	// unconditionally.
	//
	// This stood as `if the machine's identity file exists` and the file
	// never exists -- the suite harness wires a fake secret store, so the
	// branch was skipped on every run and the assertion inside it could not
	// fail. A key minted here cannot be skipped, and it carries the same
	// property: the archive is readable by the declared recipients and by
	// nobody else, which is what makes "unreadable by the machine that
	// wrote it" true rather than aspirational.
	outsiderKey, _ := vendorIdentity(t)
	var out bytes.Buffer
	in, err := os.Open(report.Path)
	require.NoError(t, err)
	defer func() { _ = in.Close() }()
	assert.Error(t, agecrypt.Decrypt(&out, in, outsiderKey),
		"a key the manifest never named opened the archive")
	assert.Zero(t, out.Len(), "a failed decrypt still produced output")

	// The vendor's identity does, and what comes out is the archive.
	plain := filepath.Join(t.TempDir(), "support.tar.zst")
	decryptTo(t, report.Path, vendorKey, plain)

	entries := archiveEntries(t, plain)
	require.Contains(t, entries, "installation.yaml")
	require.Contains(t, entries, "meta.json")
}

// A failed write leaves no plaintext behind, wherever in the write it failed.
//
// An archive already at that name *is* replaced, deliberately: the plaintext
// path renames into place and overwrites too, and having the two disagree about
// that would be a difference nobody could predict from the flag they passed.
//
// The staging directory is the whole reason this holds: the plaintext archive
// is assembled inside it and dies with it, so a failure anywhere in the
// encryption step leaves the operator's directory as it was. Building the
// plaintext at the destination and encrypting in place would satisfy every
// other test in this file and would strand a readable copy of everything here,
// under a name nobody is looking at, on exactly the run that went wrong.
//
// The failure has to land after the plaintext archive has been assembled, which
// is the only window in which there is plaintext to strand. A destination that
// cannot be written to at all fails before that window opens and would prove
// nothing: it would pass whether the archive were staged in a temporary
// directory or built here.
//
// Inside that window the encrypted write has two exits and they clean up
// differently, so both are driven. The harness clock is fixed, so the archive's
// name is known and either can be occupied.
func TestAFailedWriteLeavesNoPlaintextInTheOperatorsDirectory(t *testing.T) {
	const archiveName = "support-demo-inst_01TESTINSTALLATION-20260803T120000Z.tar.zst" +
		agecrypt.Extension

	// failedWrite runs a bundle whose destination has been occupied by occupy,
	// and returns what the operator's directory holds afterwards along with the
	// refusal.
	//
	// The refusal is returned because each leg has to prove *where* it failed.
	// An assertion that only reads the directory passes whether the write died
	// at the step the leg names or at an earlier one -- and a leg that fails
	// earlier than it claims tests a window that was already closed.
	failedWrite := func(t *testing.T, occupy func(t *testing.T, archive string)) (string, []string, error) {
		t.Helper()
		h := newHarness(t)
		h.install()

		_, vendorPublic := vendorIdentity(t)
		declareRecipients(t, h, vendorPublic)

		dir := t.TempDir()
		occupy(t, filepath.Join(dir, archiveName))

		_, refusal := ops.SupportBundle(context.Background(), h.Deps, ops.SupportOptions{Dir: dir})
		require.Error(t, refusal, "the encrypted write reported success it did not achieve")

		written, err := os.ReadDir(dir)
		require.NoError(t, err)
		names := make([]string, 0, len(written))
		for _, e := range written {
			names = append(names, e.Name())
		}
		return dir, names, refusal
	}

	// The ciphertext file cannot be created, so the encryption never starts --
	// but the plaintext tar has already been assembled by then, which is the
	// window this whole arrangement exists to close.
	t.Run("before the ciphertext exists", func(t *testing.T) {
		_, names, refusal := failedWrite(t, func(t *testing.T, archive string) {
			t.Helper()
			require.NoError(t, os.Mkdir(archive+".partial", 0o700))
		})
		assert.Contains(t, domain.AsError(refusal).Message, "cannot create",
			"this leg no longer fails where it says it does")
		assert.Equal(t, []string{archiveName + ".partial"}, names,
			"the failed run left something behind in the operator's directory")
	})

	// The far end of the same window: encryption runs to completion and the
	// rename fails, which is the only exit where a finished file exists under
	// the temporary name. It must not survive -- an operator who found it
	// would have an archive the command said it had not written, and a `.partial`
	// nobody can tell apart from a live one.
	t.Run("after the ciphertext exists, at the rename", func(t *testing.T) {
		dir, names, refusal := failedWrite(t, func(t *testing.T, archive string) {
			t.Helper()
			require.NoError(t, os.Mkdir(archive, 0o700))
			require.NoError(t, os.WriteFile(
				filepath.Join(archive, "occupant"), []byte("not the archive"), 0o600))
		})
		require.Contains(t, domain.AsError(refusal).Message, "cannot move the archive into place",
			"this leg failed before the rename, so it proves nothing about it")
		assert.Equal(t, []string{archiveName}, names,
			"the failed rename left its temporary file behind")

		// What was standing in the way is untouched, which is the other half
		// of "the directory is as it was".
		occupant, err := os.ReadFile(filepath.Join(dir, archiveName, "occupant"))
		require.NoError(t, err, "the failed run disturbed what stood at the archive's name")
		assert.Equal(t, "not the archive", string(occupant))
	})
}

// Two recipients both open it, which is what a vendor with a rotation plan or
// two support engineers actually has.
func TestEveryDeclaredRecipientCanOpenTheArchive(t *testing.T) {
	h := newHarness(t)
	h.install()
	ctx := context.Background()

	firstKey, firstPublic := vendorIdentity(t)
	secondKey, secondPublic := vendorIdentity(t)
	require.NotEqual(t, firstPublic, secondPublic)
	declareRecipients(t, h, firstPublic, secondPublic)

	report, err := ops.SupportBundle(ctx, h.Deps, ops.SupportOptions{Dir: t.TempDir()})
	require.NoError(t, err)
	require.True(t, report.Encrypted)

	for name, key := range map[string]string{"first": firstKey, "second": secondKey} {
		t.Run(name, func(t *testing.T) {
			plain := filepath.Join(t.TempDir(), "support.tar.zst")
			decryptTo(t, report.Path, key, plain)
			require.Contains(t, archiveEntries(t, plain), "meta.json")
		})
	}
}

// Decision 3a, which is LOCKED: declared-but-unusable is a refusal, and never
// a fall back to plaintext.
//
// The failure being prevented is quiet, which is why it needs its own test per
// shape: an operator whose vendor asked for encryption gets a plaintext archive
// and no reason to look twice, at the moment they are attaching it to a ticket.
// Every case asserts the directory is *empty* afterwards, because a refusal
// that still wrote the archive would be the same leak with an error message
// over it.
func TestAMalformedRecipientDeclarationIsRefusedAndWritesNothing(t *testing.T) {
	ns := "  " + domain.SupportExtension + ":\n"

	for name, tc := range map[string]struct{ block, says string }{
		"the block declares no recipients": {
			block: ns + "    contact: support@vendor.example\n",
			says:  "declares no recipients",
		},
		// The namespace with nothing under it at all, which decodes to a
		// nil map rather than an empty one -- a different shape reaching
		// the same lookup, and the one a vendor produces by writing the
		// key and being interrupted.
		"the block is empty": {
			block: ns,
			says:  "declares no recipients",
		},
		// `recipients:` with nothing after it, which is what a vendor
		// produces by writing the key and stopping. It decodes to nil,
		// not to an empty list, so it reaches a different branch than
		// `recipients: []` does.
		"recipients has no value at all": {
			block: ns + "    recipients:\n",
			says:  "not a list",
		},
		// The shape an operator actually mistypes: a real key with
		// characters missing, long enough that the message abbreviates
		// it rather than quoting the whole line back.
		"a recipient is a truncated key": {
			block: ns + "    recipients:\n      - age1kyh0pf3u8hxepjrgy9k4zuv7tua0frkj6vsnn9jld7ms\n",
			says:  "not an age recipient",
		},
		"recipients is a single value": {
			block: ns + "    recipients: age1notalist\n",
			says:  "not a list",
		},
		"recipients is a mapping": {
			block: ns + "    recipients:\n      primary: age1nope\n",
			says:  "not a list",
		},
		"recipients names nobody": {
			block: ns + "    recipients: []\n",
			says:  "names nobody",
		},
		"a recipient is empty": {
			block: ns + "    recipients:\n      - \"\"\n",
			says:  "is empty",
		},
		"a recipient is not a key": {
			block: ns + "    recipients:\n      - not-an-age-key\n",
			says:  "not an age recipient",
		},
		"a recipient is a number": {
			block: ns + "    recipients:\n      - 42\n",
			says:  "not an age recipient",
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.install()
			declareSupportBlock(t, h, tc.block)

			dir := t.TempDir()
			_, err := ops.SupportBundle(context.Background(), h.Deps,
				ops.SupportOptions{Dir: dir})

			require.Error(t, err,
				"a manifest that asked for encryption unusably produced an archive")
			assert.Contains(t, domain.AsError(err).Message, tc.says)

			written, readErr := os.ReadDir(dir)
			require.NoError(t, readErr)
			assert.Empty(t, written,
				"the refusal wrote an archive anyway, which is the leak it was refusing")
		})
	}
}

// The preview refuses the same manifest the real run would.
//
// A preview that succeeded where the archive would fail is a preview that
// cannot be trusted for the one question it is asked: what will happen.
func TestAPreviewRefusesAMalformedDeclarationToo(t *testing.T) {
	h := newHarness(t)
	h.install()
	declareSupportBlock(t, h, "  "+domain.SupportExtension+":\n    recipients: []\n")

	_, err := ops.SupportBundle(context.Background(), h.Deps,
		ops.SupportOptions{Preview: true})
	require.Error(t, err, "the preview accepted a manifest the archive would refuse")
	assert.Contains(t, domain.AsError(err).Message, "names nobody")
}

// The preview names the recipients, in full, before the archive exists.
//
// The refusal above catches a key that cannot be parsed. Nothing catches one
// that parses and belongs to the wrong party, and the only thing that can is an
// operator reading the key and comparing it with what their vendor published --
// which they can only do before the file is written.
func TestAPreviewNamesTheRecipientsBeforeTheArchiveExists(t *testing.T) {
	h := newHarness(t)
	h.install()

	_, vendorPublic := vendorIdentity(t)
	declareRecipients(t, h, vendorPublic)

	dir := t.TempDir()
	report, err := ops.SupportBundle(context.Background(), h.Deps,
		ops.SupportOptions{Preview: true, Dir: dir})
	require.NoError(t, err)

	assert.True(t, report.Encrypted,
		"the preview does not say the archive would be encrypted")
	assert.Equal(t, []string{vendorPublic}, report.Recipients,
		"the preview does not name who could read the archive")
	assert.Empty(t, report.Path, "a preview reported a path")

	written, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, written, "a preview wrote something")
}

// The archive's own index agrees with the bytes around it, both ways.
//
// `meta.json` is the file somebody opens to decide whether an archive can be
// passed on, and it carries an `encrypted` field for that decision alone. A
// reader who trusts a `false` inside ciphertext forwards a bundle a vendor
// deliberately restricted; a reader who trusts a `true` on a plaintext archive
// stops protecting one that needs it. Neither direction is worth having half
// of, so both are asserted here.
//
// The field cost nothing to be right about until this phase: it was `false` on
// every archive the command had ever produced, so the terminal report and the
// file could not disagree.
func TestTheArchivesOwnIndexAgreesWithHowItWasWritten(t *testing.T) {
	encryptionOf := func(t *testing.T, archive string) bool {
		t.Helper()
		var meta struct {
			Encrypted bool `json:"encrypted"`
		}
		body, present := archiveEntries(t, archive)["meta.json"]
		require.True(t, present, "the archive carries no meta.json")
		require.NoError(t, json.Unmarshal([]byte(body), &meta))
		return meta.Encrypted
	}

	t.Run("encrypted", func(t *testing.T) {
		h := newHarness(t)
		h.install()

		vendorKey, vendorPublic := vendorIdentity(t)
		declareRecipients(t, h, vendorPublic)

		report, err := ops.SupportBundle(context.Background(), h.Deps,
			ops.SupportOptions{Dir: t.TempDir()})
		require.NoError(t, err)
		require.True(t, report.Encrypted)

		plain := filepath.Join(t.TempDir(), "support.tar.zst")
		decryptTo(t, report.Path, vendorKey, plain)
		assert.True(t, encryptionOf(t, plain),
			"meta.json inside an encrypted archive reports it as plaintext")
	})

	t.Run("plaintext", func(t *testing.T) {
		h := newHarness(t)
		h.install()

		report, err := ops.SupportBundle(context.Background(), h.Deps,
			ops.SupportOptions{Dir: t.TempDir()})
		require.NoError(t, err)
		require.False(t, report.Encrypted)

		assert.False(t, encryptionOf(t, report.Path),
			"meta.json in a plaintext archive claims it is encrypted")
	})
}

// A release that will not resolve cannot be asked what it declares, and the
// archive says so rather than reading as "your vendor asked for nothing".
//
// This is the case the design had no row for. It happens on exactly the machine
// this command exists for -- a broken release is when somebody needs a support
// bundle -- and it must not silently produce plaintext for an installation
// whose vendor did ask for encryption.
func TestAnUnresolvableReleaseSaysEncryptionWasNotApplied(t *testing.T) {
	h := newHarness(t)
	h.install()

	// The digest stops matching, which is what the resolver refuses.
	require.NoError(t, os.WriteFile(
		filepath.Join(h.Release.Root, "morzer.yaml"), []byte("name: tampered\n"), 0o600))

	report, err := ops.SupportBundle(context.Background(), h.Deps,
		ops.SupportOptions{Dir: t.TempDir()})
	require.NoError(t, err, "a broken release must still produce an archive")

	assert.False(t, report.Encrypted)
	assert.Empty(t, report.Recipients)

	reasons := map[string]string{}
	for _, o := range report.Omitted {
		reasons[o.Name] = o.Reason
	}
	require.Contains(t, reasons, "encryption",
		"the archive is plaintext because the manifest could not be read, and does not say so")
	assert.Contains(t, reasons["encryption"], "not applied")
}

// An installation that never had a release is not an installation whose release
// failed, and the archive does not say it was.
//
// Both states arrive here as `!HasRelease`, and both genuinely mean no vendor
// declaration could apply -- so both are stated, because a failed first `apply`
// is precisely a machine whose vendor may have asked for encryption that this
// archive did not get. What must not be shared is the *reason*: telling an
// operator who has never applied a release that resolution failed sends them
// looking for a broken release directory that does not exist, in the archive
// they were about to send to somebody who would look for it too.
func TestAMissingReleaseAndABrokenOneGiveDifferentReasons(t *testing.T) {
	encryptionReason := func(t *testing.T, report ops.SupportReport) string {
		t.Helper()
		for _, o := range report.Omitted {
			if o.Name == "encryption" {
				return o.Reason
			}
		}
		t.Fatal("the archive is plaintext with no vendor declaration read, and does not say so")
		return ""
	}

	t.Run("never installed", func(t *testing.T) {
		h := newHarness(t)
		require.NoError(t, h.Deps.State.SaveInstallation(context.Background(), domain.Installation{
			SchemaVersion: domain.InstallationSchemaVersion,
			ID:            "inst_01NORELEASEYET",
			Product:       "demo",
			CreatedAt:     domain.NewTime(h.Deps.Now()),
			Policy:        domain.DefaultPolicy(),
		}))

		report, err := ops.SupportBundle(context.Background(), h.Deps,
			ops.SupportOptions{Dir: t.TempDir()})
		require.NoError(t, err)
		assert.False(t, report.Encrypted)

		reason := encryptionReason(t, report)
		assert.NotContains(t, reason, "could not be resolved",
			"an installation with no release is reported as one whose release broke")
		assert.Contains(t, reason, "no release")
		assert.Contains(t, reason, "plaintext")
	})

	t.Run("broken", func(t *testing.T) {
		h := newHarness(t)
		h.install()
		require.NoError(t, os.WriteFile(
			filepath.Join(h.Release.Root, "morzer.yaml"), []byte("name: tampered\n"), 0o600))

		report, err := ops.SupportBundle(context.Background(), h.Deps,
			ops.SupportOptions{Dir: t.TempDir()})
		require.NoError(t, err)

		assert.Contains(t, encryptionReason(t, report), "could not be resolved",
			"a release that failed to resolve is reported as one that was never installed")
	})
}

// A manifest that declares nobody produces a plaintext archive, on purpose.
//
// Decision 3 keeps this available: the operator posting to a forum is the case
// §2 rests on, and refusing would break it. Asserted so that the encryption
// work above cannot quietly turn the default into a refusal.
func TestNoDeclarationStillProducesAPlaintextArchive(t *testing.T) {
	h := newHarness(t)
	h.install()

	report, err := ops.SupportBundle(context.Background(), h.Deps,
		ops.SupportOptions{Dir: t.TempDir()})
	require.NoError(t, err)

	assert.False(t, report.Encrypted)
	assert.Empty(t, report.Recipients)
	assert.True(t, strings.HasSuffix(report.Path, ".tar.zst"),
		"a plaintext archive is named as though it were encrypted: %s", report.Path)
	require.Contains(t, archiveEntries(t, report.Path), "meta.json")
}

// decryptTo opens an encrypted archive with an identity file.
func decryptTo(t *testing.T, encrypted, identityPath, plain string) {
	t.Helper()

	in, err := os.Open(encrypted)
	require.NoError(t, err)
	defer func() { _ = in.Close() }()

	out, err := os.Create(plain)
	require.NoError(t, err)
	defer func() { _ = out.Close() }()

	require.NoError(t, agecrypt.Decrypt(out, in, identityPath),
		"the declared recipient cannot open the archive")
	require.NoError(t, out.Sync())
}
