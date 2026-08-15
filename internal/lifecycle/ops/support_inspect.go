package ops

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/agecrypt"
)

// `morzer support inspect` (RFC 0024 P4b).
//
// Reads back an archive `support bundle` produced, and answers two questions
// rather than one: what is inside it, and what its signature established. §3.5
// asks for the first and decision 11 shapes the second.
//
// **Every byte read here came from somewhere else.** The archive is a file an
// operator was handed, or one a vendor received from a stranger, so this is the
// second-largest parser of untrusted input in the manager after release
// extraction -- and unlike that one it never writes what it reads to disk.
// Nothing is extracted: one small file is taken out of the tar, and the
// plaintext of an encrypted archive exists only in memory. An `inspect` that
// unpacked into a directory would put a readable copy of an archive somebody
// deliberately encrypted onto the reviewer's filesystem, which is the property
// P4a was built for, undone by the command that reads it.
//
// Held in memory rather than streamed, and the bound is what makes that
// tolerable: the file is refused by size *before* it is opened, the decrypted
// plaintext is bounded as it arrives, and the zstd window is capped. An earlier
// draft of this comment claimed streaming, which read as though the size of the
// input did not matter -- it does, and the three limits below are where it is
// made not to.

// SupportInspectOptions are the flags `support inspect` honours.
type SupportInspectOptions struct {
	// Path is the archive to read.
	Path string

	// IdentityFile is the age identity to decrypt an `.age` archive with.
	//
	// Required for an encrypted archive and meaningless for a plaintext
	// one. The vendor holds this key; the machine that produced the archive
	// deliberately does not, which is the whole point of P4a.
	IdentityFile string

	// ExpectedKey is the signing key this archive is supposed to carry,
	// supplied out of band -- decision 11.
	//
	// A minisign public key line, or a path to a file holding one, which is
	// what a vendor has after their customer sent them a fingerprint. It
	// overrides the installation's own keys when both are available,
	// because a caller who names a key is asking a narrower question than
	// "is this one of ours".
	ExpectedKey string
}

// SupportSignatureSource says what the signature was checked against.
//
// A separate field from the outcome rather than a fifth SignatureOutcome,
// because "nothing to check against" is not something checking established --
// it is the absence of a check. Folding it into the outcome enum would also
// make every existing switch over that enum wrong on the day this shipped.
type SupportSignatureSource string

const (
	// SignatureSourceInstallation: checked against the keys this machine's
	// installation state records, current and retired.
	SignatureSourceInstallation SupportSignatureSource = "installation"

	// SignatureSourceExpectedKey: checked against the key the caller named
	// with `--key`.
	SignatureSourceExpectedKey SupportSignatureSource = "expected-key"

	// SignatureSourceMachineKey: checked against the signing key on this
	// machine's disk, when installation state records none.
	//
	// A separate source rather than folded into the one above, because it is
	// a weaker claim and the verdict has to say which was made. State's
	// record is written by `init` and repaired by `init --repair`, and a
	// machine that has signed without ever running either has a key on disk
	// that state does not know about -- reporting that as "this
	// installation's recorded keys" would credit a record that does not
	// exist. It is still a trust anchor the producer of the archive did not
	// supply, which is the only property decision 11 requires.
	SignatureSourceMachineKey SupportSignatureSource = "machine-key"

	// SignatureSourceNone: there was nothing to check against, so nothing
	// was checked.
	//
	// The case a vendor meets on their own laptop: no installation here, no
	// `--key` given, and the only key in sight is the one the archive names
	// -- which decision 11 refuses to use. Reported as its own state so the
	// answer is "you have not checked this yet" rather than a verdict.
	SignatureSourceNone SupportSignatureSource = "none"
)

// SupportSignature is what inspecting established about the archive's signature.
type SupportSignature struct {
	// Present is whether a detached signature was found beside the archive.
	Present bool `json:"present"`

	// Source is what it was checked against, or none.
	Source SupportSignatureSource `json:"source"`

	// Result is the outcome, and is meaningful only when Source is not
	// none. It reuses the attestation vocabulary deliberately: an operator
	// who has read `attest verify`'s output should not have to learn a
	// second set of words for the same three answers.
	Result domain.SignatureResult `json:"result"`

	// ClaimedKey is the key `meta.json` says signed the archive.
	//
	// Reported, never verified against (decision 11). It is what a reader
	// takes to the installation's operator to ask "is this your key", and
	// the field name says which of the two things it is.
	ClaimedKey string `json:"claimed_key,omitempty"`

	// Bound is the sentence the archive carries about what its signature
	// proves, quoted from the archive rather than from this binary, so a
	// reader is shown what the producer claimed rather than what this
	// version of the manager would have claimed.
	Bound string `json:"bound,omitempty"`
}

// SupportInspectReport is what `support inspect` prints and returns.
type SupportInspectReport struct {
	Path      string `json:"path"`
	Encrypted bool   `json:"encrypted"`

	Product        string `json:"product"`
	InstallationID string `json:"installation_id"`
	ManagerVersion string `json:"manager_version"`

	Signature SupportSignature `json:"signature"`

	Entries    []SupportEntry    `json:"entries"`
	Omitted    []SupportOmission `json:"omitted,omitempty"`
	TotalBytes int64             `json:"total_bytes"`

	// IndexBytes is how big `meta.json` itself is, reported separately
	// because it is the one file in the archive that cannot appear in its
	// own list.
	//
	// The asymmetry is inherent rather than an oversight, and it is worth
	// naming because it makes two honest tools print different counts for
	// one archive: `support bundle` lists the index as a component, because
	// it is a file it wrote, and `support inspect` lists what the index
	// enumerates, which is everything except itself. An entry for it here
	// would have to carry a redaction count no reader can know -- and a
	// zero in that column is precisely the misreading this feature is built
	// to prevent. So the size is reported and the row is not invented.
	IndexBytes int64 `json:"index_bytes"`
}

// maxInspectArchive bounds the plaintext this will hold.
//
// The archive is read into memory rather than onto disk, so this is the ceiling
// on what a hostile file can make the process allocate. Generous against the
// measured size of a real bundle -- §11.3 recorded 5,882 bytes compressed on a
// deployment with a full journal -- and small enough that a decompression bomb
// is refused rather than survived.
const maxInspectArchive = 64 << 20

// maxInspectEntries bounds how many tar headers are read before giving up.
//
// A bundle has eleven components and one file per service in `logs/`. An
// archive with a million empty entries is not one this command needs to serve,
// and reading it to the end to discover that `meta.json` is absent is the
// cheapest denial of service in any tar reader.
const maxInspectEntries = 10_000

// maxInspectMeta bounds `meta.json` itself.
const maxInspectMeta = 8 << 20

// SupportInspect reads an archive back.
func SupportInspect(
	ctx context.Context, d *Deps, opts SupportInspectOptions,
) (SupportInspectReport, error) {
	if strings.TrimSpace(opts.Path) == "" {
		return SupportInspectReport{}, domain.Usage("no archive was named").
			WithHint("morzer support inspect <file>")
	}

	body, err := readBoundedFile(opts.Path, maxInspectArchive)
	if err != nil {
		return SupportInspectReport{}, err
	}

	report := SupportInspectReport{
		Path:      opts.Path,
		Encrypted: strings.HasSuffix(opts.Path, agecrypt.Extension),
	}

	// The signature covers the file as it lies here -- ciphertext included
	// (decision 9) -- so it is checked against these bytes, before anything
	// is decrypted or parsed. That ordering is the point of signing what
	// leaves rather than what is inside: a reader can establish where the
	// file came from without holding a key and without feeding a stranger's
	// bytes to a decompressor first.
	sig, sigErr := readDetachedSignature(opts.Path)
	if sigErr != nil {
		return SupportInspectReport{}, sigErr
	}

	plain := body
	if report.Encrypted {
		plain, err = decryptSupportArchive(body, opts.IdentityFile)
		if err != nil {
			return SupportInspectReport{}, err
		}
	}

	meta, indexBytes, err := readSupportMeta(plain)
	if err != nil {
		return SupportInspectReport{}, err
	}
	report.IndexBytes = indexBytes

	report.Product = meta.Product
	report.InstallationID = meta.InstallationID
	report.ManagerVersion = meta.ManagerVersion
	report.Entries = meta.Entries
	report.Omitted = meta.Omitted
	for _, e := range meta.Entries {
		report.TotalBytes += e.Bytes
	}

	report.Signature = verifySupportSignature(ctx, d, opts, body, sig, meta)
	return report, nil
}

// supportMetaDocument is `meta.json` as a reader sees it.
//
// A type of its own rather than the anonymous struct the writer uses, because
// the two are not the same thing: the writer's shape is what this version
// produces, and this one has to read archives written by other versions. Every
// field is optional here for that reason -- an older archive has no
// `signing_key`, and that is a fact about it rather than a parse failure.
type supportMetaDocument struct {
	Product        string            `json:"product"`
	InstallationID string            `json:"installation_id"`
	ManagerVersion string            `json:"manager_version"`
	Encrypted      bool              `json:"encrypted"`
	SigningKey     string            `json:"signing_key"`
	SignatureBound string            `json:"signature_bound"`
	Entries        []SupportEntry    `json:"entries"`
	Omitted        []SupportOmission `json:"omitted"`
}

// readDetachedSignature reads `<archive>.minisig`, absent being a normal state.
func readDetachedSignature(path string) ([]byte, error) {
	// Bounded like everything else here. A minisign signature is a few
	// hundred bytes; a file of that name holding a gigabyte is not a
	// signature and reading it to find that out is the mistake.
	sig, err := readBoundedFile(path+minisigExt, maxInspectSignature)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// An unsigned archive, or a signature that did not travel with
		// the file. Both are ordinary and the report says which of the
		// two it cannot tell apart.
		return nil, nil
	case err != nil:
		return nil, err
	}
	return sig, nil
}

// maxInspectSignature bounds the detached signature.
const maxInspectSignature = 64 << 10

// readBoundedFile refuses by size before it reads.
//
// `os.ReadFile` grows a buffer to whatever is on disk, so checking afterwards
// checks after the damage. This is the same rule the extractor follows for a
// release bundle -- refuse while the bytes are arriving, not once they have --
// applied at the one door every byte in this command comes through.
//
// The not-exist error is returned unwrapped, because a caller distinguishes it:
// an archive with no signature beside it is an ordinary state, not a failure.
func readBoundedFile(path string, limit int64) ([]byte, error) {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, err
	case err != nil:
		return nil, domain.Usage("cannot read %s: %s", path, domain.AsError(err).Message)
	case info.IsDir():
		return nil, domain.Usage("%s is a directory, not a file", path)
	case info.Size() > limit:
		return nil, domain.Usage("%s is %d bytes, past the %d this will read",
			path, info.Size(), limit)
	}

	body, err := os.ReadFile(path) //nolint:gosec // stat-checked immediately above
	if err != nil {
		return nil, domain.Usage("cannot read %s: %s", path, domain.AsError(err).Message)
	}
	return body, nil
}

// decryptSupportArchive decrypts into memory, bounded.
func decryptSupportArchive(body []byte, identityFile string) ([]byte, error) {
	if strings.TrimSpace(identityFile) == "" {
		return nil, domain.SecretsError(nil,
			"this archive is encrypted and no identity was given to read it").
			WithHint("pass --identity <key file>; the key belongs to whoever the " +
				"release named as a support recipient, and is deliberately not " +
				"on the machine that produced the archive")
	}

	var out boundedBuffer
	out.limit = maxInspectArchive
	if err := agecrypt.Decrypt(&out, bytes.NewReader(body), identityFile); err != nil {
		if out.exceeded {
			return nil, domain.Usage(
				"the archive decrypts to more than %d bytes, which this will not read",
				maxInspectArchive)
		}
		return nil, err
	}
	return out.buf.Bytes(), nil
}

// boundedBuffer is an io.Writer that refuses past a limit.
//
// The limit has to be enforced *while* the bytes arrive rather than checked
// afterwards, which is the same rule `ExtractTarZst` follows and for the same
// reason: an archive that expands enormously has to be refused before it has
// been held, not diagnosed once it has.
type boundedBuffer struct {
	buf      bytes.Buffer
	limit    int
	exceeded bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.limit {
		b.exceeded = true
		return 0, errors.New("archive is larger than this command will read")
	}
	return b.buf.Write(p)
}

// readSupportMeta pulls `meta.json` out of a tar.zst held in memory, and
// reports how big it was.
func readSupportMeta(archive []byte) (supportMetaDocument, int64, error) {
	zr, err := zstd.NewReader(bytes.NewReader(archive),
		// The format lets an archive declare the window it wants, so a
		// small file can ask the decoder for gigabytes. Capped here as it
		// is for release bundles.
		zstd.WithDecoderMaxMemory(maxInspectArchive))
	if err != nil {
		return supportMetaDocument{}, 0, domain.Usage(
			"this file is not a support bundle: %s", domain.AsError(err).Message)
	}
	defer zr.Close()

	tr := tar.NewReader(zr)
	for i := 0; i < maxInspectEntries; i++ {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return supportMetaDocument{}, 0, domain.Usage(
				"this file is not a support bundle: %s", domain.AsError(err).Message)
		}
		// Compared against the base name so an archive that nests its
		// entries under a directory still reads, and so a crafted entry
		// called `logs/meta.json` cannot be mistaken for the index. The
		// writer produces `meta.json` at the root and nothing else.
		if filepath.Clean(hdr.Name) != supportMetaName {
			continue
		}
		if hdr.Size > maxInspectMeta {
			return supportMetaDocument{}, 0, domain.Usage(
				"%s in this archive is %d bytes, past the %d this will read",
				supportMetaName, hdr.Size, maxInspectMeta)
		}

		// Bounded by a limited reader rather than by the header's size:
		// a declared size is a claim, and this is the same rule the
		// extractor applies to a release bundle's entries.
		body, err := io.ReadAll(io.LimitReader(tr, maxInspectMeta))
		if err != nil {
			return supportMetaDocument{}, 0, domain.Usage(
				"cannot read %s out of the archive: %s",
				supportMetaName, domain.AsError(err).Message)
		}

		var doc supportMetaDocument
		if err := json.Unmarshal(body, &doc); err != nil {
			return supportMetaDocument{}, 0, domain.Usage(
				"%s in this archive is not readable: %s",
				supportMetaName, domain.AsError(err).Message)
		}
		return doc, int64(len(body)), nil
	}

	return supportMetaDocument{}, 0, domain.Usage(
		"this archive has no %s, so it is not a support bundle this can read",
		supportMetaName).
		WithHint("`morzer support bundle` writes one; an archive from anywhere " +
			"else is not one")
}

// verifySupportSignature decides what the signature established, and against
// what.
//
// The whole of decision 11 lives here. The archive names a key and that name is
// reported; it is never the key checked against, because the archive and the
// name were written by the same hand. What can be checked against is a key from
// somewhere the producer of the archive did not control: this installation's
// own record of its keys, or one the caller names.
func verifySupportSignature(
	ctx context.Context, d *Deps, opts SupportInspectOptions,
	body, sig []byte, meta supportMetaDocument,
) SupportSignature {
	out := SupportSignature{
		Present:    len(sig) > 0,
		Source:     SignatureSourceNone,
		ClaimedKey: meta.SigningKey,
		Bound:      meta.SignatureBound,
	}
	if !out.Present {
		out.Result = domain.SignatureResult{Outcome: domain.Unsigned}
		return out
	}
	if d.Checker == nil {
		// A build with no checker cannot answer, and saying so beats
		// reporting `unverifiable`, which would name a finding.
		return out
	}
	check := func(key string) bool { return d.Checker.Check(body, sig, key) }

	// `--key` first: a caller who names a key is asking a narrower question
	// than "is this one of ours", and answering the broader one instead
	// would report `signed-by-current-key` to somebody who asked whether it
	// was signed by the key their customer gave them.
	if strings.TrimSpace(opts.ExpectedKey) != "" {
		key, err := resolveExpectedKey(opts.ExpectedKey)
		if err != nil {
			// Reported as unchecked rather than failing the inspect:
			// the listing half is still worth printing, and a caller
			// who fat-fingered a path should see what is in the
			// archive along with the reason the check did not happen.
			d.warnf("the signature was not checked: %s", domain.AsError(err).Message)
			return out
		}
		out.Source = SignatureSourceExpectedKey
		out.Result = domain.VerifySignature(
			domain.Signing{PublicKey: key}, true, check)
		return out
	}

	// Otherwise this machine's own record, when there is an installation
	// here to ask. A vendor's laptop has none, which is the ordinary case
	// for the reader this artifact was built for -- so a missing
	// installation is not an error, it is the reason the answer is "not
	// checked".
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return out
	}
	if inst.Signing.HasKey() || len(inst.Signing.PreviousKeys) > 0 {
		out.Source = SignatureSourceInstallation
		out.Result = domain.VerifySignature(inst.Signing, true, check)
		return out
	}

	// State records no key, which is the ordinary condition of a machine
	// that reached schema 6 by migration and has signed something since:
	// the signer mints on demand, and only `init` and `init --repair` write
	// the result into state. The key is nonetheless on this machine's disk,
	// put there by this machine, and checking against it answers a real
	// question -- so the fallback exists, under a name that does not claim
	// state backed it.
	//
	// `PublicKey` and never `EnsureKey`: inspecting an archive is a read,
	// and a read that minted a signing key would be the same defect
	// decision 13 keeps out of `--preview`, in a command with even less
	// reason to write anything.
	if d.Signer == nil {
		return out
	}
	key, err := d.Signer.PublicKey(ctx)
	if err != nil || key.Line == "" {
		return out
	}
	out.Source = SignatureSourceMachineKey
	out.Result = domain.VerifySignature(domain.Signing{PublicKey: key.Line}, true, check)
	return out
}

// resolveExpectedKey takes `--key` as either the key itself or a file holding
// one.
//
// Both, because both are what a caller actually has: a fingerprint pasted from
// an email, or the `.pub` file minisign writes. Guessing between them by
// looking for a path separator would break a key that happens to contain one,
// so the test is whether a file of that name exists.
func resolveExpectedKey(value string) (string, error) {
	value = strings.TrimSpace(value)

	// A path that exists and cannot be read is the operator's mistake and
	// is reported as one. Falling through to "treat the string as a key"
	// would answer `signature does NOT verify` to somebody who named a
	// directory or a file they lack permission on -- a verdict about the
	// archive, produced by a problem with the key.
	if info, err := os.Stat(value); err == nil {
		if info.IsDir() {
			return "", domain.Usage("%s is a directory, not a key file", value)
		}
		if info.Size() > maxInspectSignature {
			return "", domain.Usage("%s is too large to be a key file", value)
		}
	}
	if body, err := os.ReadFile(value); err == nil { //nolint:gosec // stat-checked above
		// minisign's public key file is two lines: an untrusted comment
		// and the key. The key is the last non-empty line.
		lines := strings.Split(strings.TrimSpace(string(body)), "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			if line := strings.TrimSpace(lines[i]); line != "" {
				return line, nil
			}
		}
		return "", domain.Usage("the key file at %s is empty", value)
	}
	return value, nil
}
