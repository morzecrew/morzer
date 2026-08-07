// Package agecrypt encrypts and decrypts a stream to a set of age recipients.
//
// It exists so a backup artifact can be protected by the same keys that
// protect the secret state. That is the whole design: the recipient set is not
// a second thing to manage, it is the one `init` already insists on -- this
// machine's identity plus at least one offline recovery key -- so a backup
// that leaves the machine is readable by exactly the people who could already
// read the deployment's secrets, and by nobody else.
//
// Not a port. There is no second implementation to swap in: age is the
// project's encryption, chosen once in RFC 0003, and an interface here would
// be an abstraction over a single case. It sits in infra beside atomicfs for
// the same reason.
//
// Streaming throughout. A database dump is not something to hold in memory,
// and age's format is chunked AEAD, so encrypting a hundred gigabytes costs a
// buffer rather than a hundred gigabytes.
package agecrypt

import (
	"io"
	"os"
	"strings"

	"filippo.io/age"

	"github.com/morzecrew/morzer/internal/domain"
)

// Extension marks an encrypted artifact inside a backup.
const Extension = ".age"

// Encrypt writes the contents of src to dst, encrypted to every recipient.
//
// An empty recipient list is refused rather than treated as "no encryption".
// Silently writing plaintext when the caller asked for encryption is the
// failure this package exists to prevent, and it would be invisible until the
// day somebody read the file.
func Encrypt(dst io.Writer, src io.Reader, recipients []string) error {
	if len(recipients) == 0 {
		return domain.SecretsError(nil, "refusing to encrypt to an empty recipient set").
			WithHint("nothing would be able to read the result")
	}

	parsed := make([]age.Recipient, 0, len(recipients))
	for _, key := range recipients {
		r, err := age.ParseX25519Recipient(strings.TrimSpace(key))
		if err != nil {
			return domain.SecretsError(err, "%q is not a valid age recipient", short(key)).
				WithHint("age public keys start with `age1` and are 62 characters long")
		}
		parsed = append(parsed, r)
	}

	w, err := age.Encrypt(dst, parsed...)
	if err != nil {
		return domain.SecretsError(err, "cannot start encrypting")
	}
	if _, err := io.Copy(w, src); err != nil {
		// Closed on the error path too: the writer holds a buffer, and
		// leaving it open would leak it for the life of the process.
		_ = w.Close()
		return domain.SecretsError(err, "cannot encrypt")
	}
	// The close is what writes the final chunk and its authentication tag,
	// so a discarded error here is a truncated file that decrypts to
	// nothing.
	if err := w.Close(); err != nil {
		return domain.SecretsError(err, "cannot finish encrypting")
	}
	return nil
}

// Decrypt writes the contents of src to dst, using the identities in the file
// at identityPath.
//
// A failure here is not only "wrong key". age's format is authenticated, so a
// ciphertext that has been altered by a byte fails to decrypt rather than
// producing altered plaintext -- which is a stronger guarantee than the
// checksum in the backup manifest, and the reason the manifest checksums the
// stored bytes rather than the plaintext.
func Decrypt(dst io.Writer, src io.Reader, identityPath string) error {
	if identityPath == "" {
		return domain.SecretsError(nil, "no age identity was given to decrypt with")
	}

	f, err := os.Open(identityPath)
	if err != nil {
		return domain.SecretsError(err, "cannot read the age identity at %s", identityPath).
			WithHint("restore it from your backup, or pass --identity <key> to use " +
				"the offline recovery key instead")
	}
	defer func() { _ = f.Close() }()

	identities, err := age.ParseIdentities(f)
	if err != nil {
		// Never wrap the parse error: age quotes the offending line back
		// ("unknown identity type: %q"), and the offending line of an
		// identity file is a private key. The path is what the operator
		// needs; the bytes are what a log must never hold.
		return domain.SecretsError(nil, "the age identity at %s is not valid", identityPath).
			WithHint("the file should contain a line starting with AGE-SECRET-KEY-")
	}

	r, err := age.Decrypt(src, identities...)
	if err != nil {
		return domain.SecretsError(err, "cannot decrypt: no identity in %s matches", identityPath).
			WithHint("this backup was encrypted for a different set of keys; " +
				"try the offline recovery key with --identity")
	}
	if _, err := io.Copy(dst, r); err != nil {
		return domain.SecretsError(err, "cannot decrypt")
	}
	return nil
}

// short abbreviates a key for a message that already says enough.
func short(k string) string {
	k = strings.TrimSpace(k)
	if len(k) <= 20 {
		return k
	}
	return k[:12] + "…" + k[len(k)-6:]
}
