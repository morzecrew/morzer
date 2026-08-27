// Package minisign implements ports.Signer with a per-installation minisign
// key.
//
// This is the machine signing for itself, and it does not reverse RFC 0004
// decision 8. That decision keeps a *vendor's release key* -- one key whose
// signatures every customer trusts -- out of the manager and off deployment
// hosts. This key speaks for exactly one installation, to whoever reads that
// installation's artifacts, and a host holding a key that can only impersonate
// itself has given nothing away. The verifier package next door still never
// signs, and the comment there still holds.
//
// minisign format, so verification is `minisign -Vm <file> -P <key>` -- the
// command the installation page already teaches for checking a release. A
// second signature format would be a second thing to learn for the same
// gesture.
package minisign

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	gominisign "github.com/jedisct1/go-minisign"
	"golang.org/x/crypto/blake2b"

	"github.com/morzecrew/morzer/internal/domain"
)

// The minisign secret-key layout, which this file writes by hand.
//
// go-minisign parses a secret key and signs with one, but it cannot generate
// or encode one -- so the encoder is ours, and it is the only piece of format
// work in RFC 0028. It is deliberately small and deliberately tested against
// the real `minisign` binary in both directions (test/suite/signing_docker_test.go):
// checking it with our own reader would prove only that we are self-consistent.
//
// The payload is 158 bytes, base64'd on the second line of the file:
//
//	 0..2    signature algorithm, "Ed"
//	 2..4    KDF algorithm; 0x0000 means the key is not passphrase-encrypted
//	 4..6    checksum algorithm, "B2" (Blake2b-256)
//	 6..38   KDF salt
//	38..46   KDF opslimit, little-endian
//	46..54   KDF memlimit, little-endian
//	54..62   key id
//	62..126  Ed25519 secret key (seed || public)
//	126..158 Blake2b-256(sigalg || keyid || secret key)
//
// The checksum is computed even for an unencrypted key, because that is what
// the reference implementation verifies when it loads one. A key we wrote with
// a wrong checksum would be rejected by `minisign` while our own parser
// accepted it, which is exactly the divergence the interop test exists to
// catch.
const (
	secretKeyPayloadLen = 158
	publicKeyPayloadLen = 42

	commentPrefix = "untrusted comment: "
)

var (
	sigAlgEd      = [2]byte{'E', 'd'}
	kdfAlgNone    = [2]byte{0, 0}
	chkAlgBlake2b = [2]byte{'B', '2'}
)

// keypair is a freshly minted signing identity, in the two forms it is stored
// in: the secret key file's bytes, and the public key line that goes into
// installation state.
type keypair struct {
	secretFile []byte
	publicLine string
	keyID      string
}

// generate mints an Ed25519 key and encodes both halves.
//
// No passphrase, and that is decision 3 rather than an omission: a systemd
// timer signs unattended, so a passphrase would be a passphrase stored beside
// the key it protects. What protects this key is the file mode and the
// directory mode, which is the same answer as for the age identity.
func generate(comment string) (keypair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return keypair{}, domain.Internal(err, "cannot generate a signing key")
	}

	var keyID [8]byte
	if _, err := rand.Read(keyID[:]); err != nil {
		return keypair{}, domain.Internal(err, "cannot generate a signing key id")
	}

	var salt [32]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return keypair{}, domain.Internal(err, "cannot generate a signing key salt")
	}

	var secret [64]byte
	copy(secret[:], priv)

	payload := make([]byte, 0, secretKeyPayloadLen)
	payload = append(payload, sigAlgEd[:]...)
	payload = append(payload, kdfAlgNone[:]...)
	payload = append(payload, chkAlgBlake2b[:]...)
	payload = append(payload, salt[:]...)
	payload = binary.LittleEndian.AppendUint64(payload, 0)
	payload = binary.LittleEndian.AppendUint64(payload, 0)
	payload = append(payload, keyID[:]...)
	payload = append(payload, secret[:]...)
	payload = append(payload, checksum(keyID, secret)...)

	if len(payload) != secretKeyPayloadLen {
		// Unreachable unless the layout above is edited wrongly, which
		// is precisely when a loud failure is worth more than a
		// signature nothing can verify.
		return keypair{}, domain.Internal(nil,
			"encoded signing key is %d bytes, want %d", len(payload), secretKeyPayloadLen)
	}

	id := strings.ToUpper(hex.EncodeToString(reverse(keyID[:])))

	// The comment is sanitised here as well as at the trusted comment,
	// because a newline in it writes a *third line* into the key file --
	// and both go-minisign and the real binary then fail to parse a key
	// this machine depends on. The identity would be lost on the next load,
	// which is a worse outcome than any comment is worth.
	file := fmt.Sprintf("%s%s\n%s\n",
		commentPrefix, sanitiseComment(comment), base64.StdEncoding.EncodeToString(payload))

	return keypair{
		secretFile: []byte(file),
		publicLine: encodePublicKey(keyID, pub),
		keyID:      id,
	}, nil
}

// checksum is Blake2b-256 over the signature algorithm, the key id and the
// secret key, in that order -- what the reference implementation compares
// against when it loads a key.
func checksum(keyID [8]byte, secret [64]byte) []byte {
	h, _ := blake2b.New256(nil)
	h.Write(sigAlgEd[:])
	h.Write(keyID[:])
	h.Write(secret[:])
	return h.Sum(nil)
}

// encodePublicKey renders the public half as the single base64 line `minisign
// -P` takes.
//
// The bare line rather than a whole public key *file*: this string is stored in
// installation state and pasted onto a command line, and a two-line file with a
// comment is a worse fit for both. `minisign -P` accepts exactly this.
func encodePublicKey(keyID [8]byte, pub ed25519.PublicKey) string {
	payload := make([]byte, 0, publicKeyPayloadLen)
	payload = append(payload, sigAlgEd[:]...)
	payload = append(payload, keyID[:]...)
	payload = append(payload, pub...)
	return base64.StdEncoding.EncodeToString(payload)
}

// publicLineFor derives the public key line from a parsed secret key, so that
// what is recorded in state is derived from the key on disk rather than
// remembered separately.
func publicLineFor(sk gominisign.PrivateKey) string {
	pk := sk.PublicKey()
	return encodePublicKey(pk.KeyId, ed25519.PublicKey(pk.PublicKey[:]))
}

// keyIDFor renders a key id the way minisign prints it: hex, uppercase, and
// byte-reversed, because the id is read as a little-endian integer.
func keyIDFor(sk gominisign.PrivateKey) string {
	id := sk.KeyId
	return strings.ToUpper(hex.EncodeToString(reverse(id[:])))
}

// reverse copies rather than reversing in place: both callers pass a slice of
// a key's own id array, which the caller still holds.
func reverse(b []byte) []byte {
	out := slices.Clone(b)
	slices.Reverse(out)
	return out
}
