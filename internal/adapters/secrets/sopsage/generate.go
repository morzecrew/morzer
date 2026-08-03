package sopsage

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"filippo.io/age"
	"github.com/morzecrew/morzer/internal/domain"
)

// Generate produces a secret value according to a release's declaration.
//
// Every path uses crypto/rand. There is no seeding, no fallback to a weaker
// source, and no "good enough for a dev install" mode: a predictable
// credential in a self-hosted deployment is indistinguishable from no
// credential.
func Generate(g domain.Generator) (string, error) {
	g = g.Resolved()

	switch g.Kind {
	case domain.GeneratorPassword:
		return randomString(g.Length, g.Alphabet)

	case domain.GeneratorHex:
		// Length is in output characters, so the byte count is half of
		// it -- an author writing length: 64 expects a 64-character
		// string, not a 128-character one.
		b, err := randomBytes((g.Length + 1) / 2)
		if err != nil {
			return "", err
		}
		return hex.EncodeToString(b)[:g.Length], nil

	case domain.GeneratorBase64:
		b, err := randomBytes(g.Length)
		if err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(b), nil

	case domain.GeneratorUUID:
		return randomUUID()

	case domain.GeneratorAgeKey:
		identity, err := age.GenerateX25519Identity()
		if err != nil {
			return "", domain.SecretsError(err, "cannot generate an age key")
		}
		return identity.String(), nil

	case domain.GeneratorNone:
		return "", domain.SecretsError(nil, "this secret has no generator and must be supplied").
			WithHint("use `morzer secret set <name>` to provide the value")

	default:
		return "", domain.SecretsError(nil, "unknown secret generator %q", g.Kind)
	}
}

func randomBytes(n int) ([]byte, error) {
	if n <= 0 {
		return nil, domain.SecretsError(nil, "cannot generate %d bytes", n)
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, domain.SecretsError(err, "the system random source is unavailable")
	}
	return b, nil
}

// randomString draws uniformly from an alphabet.
//
// Rejection sampling rather than modulo: taking a random byte mod len(alphabet)
// biases towards the first (256 mod n) characters, which measurably shrinks the
// keyspace of every password the manager generates.
func randomString(length int, alphabet string) (string, error) {
	if length <= 0 {
		return "", domain.SecretsError(nil, "cannot generate a secret of length %d", length)
	}
	runes := []rune(alphabet)
	n := len(runes)
	if n < 2 {
		return "", domain.SecretsError(nil, "the generator alphabet needs at least 2 characters")
	}
	if n > 256 {
		return "", domain.SecretsError(nil, "the generator alphabet must be at most 256 characters")
	}

	// The largest multiple of n that fits in a byte. Values at or above it
	// are discarded rather than folded in.
	limit := byte(256 - (256 % n))

	out := make([]rune, 0, length)
	buf := make([]byte, length*2) // over-read so most runs need one draw

	for len(out) < length {
		if _, err := rand.Read(buf); err != nil {
			return "", domain.SecretsError(err, "the system random source is unavailable")
		}
		for _, b := range buf {
			if b >= limit {
				continue
			}
			out = append(out, runes[int(b)%n])
			if len(out) == length {
				break
			}
		}
	}
	return string(out), nil
}

// randomUUID produces a v4 UUID without a dependency for one function.
func randomUUID() (string, error) {
	b, err := randomBytes(16)
	if err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
