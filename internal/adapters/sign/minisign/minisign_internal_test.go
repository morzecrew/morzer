package minisign

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

// TestEnsureKeyRefusesToMintOverAKeyItCannotRead is the branch that protects
// the identity.
//
// EnsureKey mints when there is no key. The dangerous neighbour of that is
// minting when there is a key it merely failed to *read* -- a permissions
// problem, a transient IO error -- because that silently destroys the identity
// every artifact this installation has already emitted was signed with, and
// produces no error while doing it.
//
// Covered here rather than left to the interop tests: they exercise the happy
// path, and this branch only runs when something is already wrong.
func TestEnsureKeyRefusesToMintOverAKeyItCannotRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identity.key")

	// A file that exists and is not a readable key. Corruption rather than
	// a permission bit, so the test does not depend on not being root --
	// under `sudo just ci` a 0000 file is readable and the test would pass
	// for the wrong reason.
	require.NoError(t, os.WriteFile(path, []byte("not a minisign key\n"), 0o400))

	before, err := os.ReadFile(path)
	require.NoError(t, err)

	_, err = New(path, "demo").EnsureKey(context.Background())
	require.Error(t, err, "EnsureKey minted over a key it could not read")
	assert.False(t, errors.Is(err, domain.ErrNoSigningKey),
		"an unreadable key was reported as an absent one, which is the reading that mints over it")

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after, "the unreadable key was overwritten")
}

// A passphrase-encrypted key is refused rather than prompted for. This manager
// signs from a systemd timer, which has no terminal to prompt on.
func TestAnEncryptedKeyIsRefusedRatherThanPrompted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identity.key")

	// Mint one, then flip the KDF algorithm bytes to scrypt so it parses
	// as an encrypted key. Editing the encoded payload rather than
	// constructing one by hand keeps the fixture honest about the format.
	s := New(path, "demo")
	_, err := s.EnsureKey(context.Background())
	require.NoError(t, err)

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	encrypted := encryptedForm(t, string(body))
	// EnsureKey wrote it 0400, which is the property asserted elsewhere and
	// in the way here.
	require.NoError(t, os.Chmod(path, 0o600))
	require.NoError(t, os.WriteFile(path, []byte(encrypted), 0o600))

	_, err = s.Sign(context.Background(), []byte("x"), "morzer demo")
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Message, "passphrase-encrypted")
}

// sanitiseComment has an empty case and two replacement rules, and all three
// have to be decided rather than inherited: a comment minisign cannot read back
// makes a signature file that fails to parse, which looks like a corrupt
// signature rather than a bad comment.
func TestSanitiseCommentMakesAnyStringUsableAsATrustedComment(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"plain", "morzer demo apply", "morzer demo apply"},
		{"newlines become spaces", "morzer\ndemo\rapply", "morzer demo apply"},
		{"tabs become spaces", "morzer\tdemo", "morzer demo"},
		{"unprintable becomes a placeholder", "morzer \x00demo", "morzer ?demo"},
		{"non-ascii becomes a placeholder", "morzer démo", "morzer d?mo"},
		{"surrounding space is trimmed", "  morzer demo  ", "morzer demo"},

		// The empty case, decided rather than inherited. An empty
		// trusted comment is legal minisign, but a signature that says
		// nothing about what it signed is worth less than one that
		// names the tool that made it.
		{"empty falls back", "", "morzer"},
		{"whitespace only falls back", " \n\t ", "morzer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, sanitiseComment(tc.in))
		})
	}
}

// encryptedForm rewrites an unencrypted key's payload so its KDF algorithm
// reads as scrypt, which is how IsEncrypted decides.
func encryptedForm(t *testing.T, file string) string {
	t.Helper()
	lines := strings.Split(strings.TrimRight(file, "\n"), "\n")
	require.Len(t, lines, 2, "a minisign secret key is a comment and a payload")

	payload, err := base64.StdEncoding.DecodeString(lines[1])
	require.NoError(t, err)
	require.Len(t, payload, secretKeyPayloadLen)
	payload[2], payload[3] = 'S', 'c'

	return lines[0] + "\n" + base64.StdEncoding.EncodeToString(payload) + "\n"
}
